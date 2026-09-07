package transfer

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestExecTimeoutWatchdogFailsHungTask verifies the Kody finding: an executor
// that ignores context cancellation and never returns must not pin its
// MaxActive slot forever. When the ExecTimeout watchdog fires, the task must
// transition to UploadStateFailed with the timeout message (so the state is
// observable) and the slot must be released so later uploads can start. A
// late return from the hung executor must not overwrite the timeout failure.
func TestExecTimeoutWatchdogFailsHungTask(t *testing.T) {
	// The executor blocks until release, then until hung is closed: it
	// deliberately ignores ctx cancellation — the pathological case the
	// watchdog exists for. Closing hung at the end simulates the hung upload
	// finally unwinding so the completion path runs.
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	hung := make(chan struct{})
	var hungOnce sync.Once
	defer hungOnce.Do(func() { close(hung) })
	mgr := NewUploadTaskManager(func(_ context.Context, _ io.Reader, _ int64, _ string, _ bool, _ string, _ bool) (any, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		<-hung
		return map[string]any{"cid": "QmLate"}, nil
	}, time.Hour)
	mgr.MaxActive = 1
	mgr.ExecTimeout = 50 * time.Millisecond

	id, err := mgr.Start(context.Background(), io.NopCloser(strings.NewReader("x")), 1, "hang.bin", false)
	require.NoError(t, err)
	<-started

	// The watchdog must force-fail the task even though the executor ignores
	// cancellation and is still blocked inside the upload.
	require.Eventually(t, func() bool {
		task, err := mgr.Get(id)
		return err == nil && task.State == UploadStateFailed
	}, 2*time.Second, 5*time.Millisecond, "a hung executor must be force-failed by the ExecTimeout watchdog")

	task, err := mgr.Get(id)
	require.NoError(t, err)
	require.Contains(t, task.Err, execTimeoutMessage)
	require.NotNil(t, task.FinishedAt, "the timeout failure must be marked finished so prune can evict it")

	// The freed slot must be observable right away: a second upload starts
	// despite MaxActive=1 and the first executor still being blocked.
	_, err = mgr.Start(context.Background(), io.NopCloser(strings.NewReader("y")), 1, "next.bin", false)
	require.NoError(t, err, "the ExecTimeout watchdog must release the MaxActive slot of a hung executor")

	// When the hung executor finally returns, the timeout failure stays
	// authoritative — no late-result overwrite. The goroutine is fully
	// unblocked here, so any clobbering (a completion path that ignores
	// having been force-failed) would show up within this window.
	hungOnce.Do(func() { close(hung) })
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		task, err := mgr.Get(id)
		require.NoError(t, err)
		require.Equal(t, UploadStateFailed, task.State,
			"late executor return must not overwrite the timeout failure")
		require.Contains(t, task.Err, execTimeoutMessage)
		require.Nil(t, task.Result, "the late executor result must be discarded")
		time.Sleep(5 * time.Millisecond)
	}

	// A timeout-failed task is terminal: cancelling it must not resurrect it.
	err = mgr.Cancel(id)
	require.Error(t, err, "a timeout-failed task is not cancellable")
}

// TestPruneTombstonesInFinishedAtOrder verifies the Kody finding: pruneLocked
// walks m.tasks in random map-iteration order, so tombstoning inline used to
// append the tombstone FIFO in a non-deterministic order that does not match
// FinishedAt. The retirement loop stops at the first young tombstone, so a
// batch appended out of order strands older tombstones behind younger ones —
// e.g. an expired-Prepared snapshot (fresh FinishedAt) parked at the FIFO
// head blocks already-aged terminal tombstones behind it for up to an extra
// TTL, non-deterministically. The batch must land in the FIFO in FinishedAt
// order so retirement is deterministic: the aged terminal tombstones retire
// in the same pass while the fresh expired snapshots survive.
func TestPruneTombstonesInFinishedAtOrder(t *testing.T) {
	mgr := NewUploadTaskManager(func(_ context.Context, _ io.Reader, _ int64, _ string, _ bool, _ string, _ bool) (any, error) {
		return map[string]any{"cid": "QmX"}, nil
	}, time.Hour)
	mgr.PreparedTTL = time.Minute

	// Three async uploads that complete immediately; their FinishedAt values
	// are then backdated to distinct times (ids[1] oldest, ids[2], ids[0]).
	ids := make([]string, 0, 3)
	for _, name := range []string{"one.bin", "two.bin", "three.bin"} {
		id, err := mgr.Start(context.Background(), io.NopCloser(strings.NewReader("x")), 1, name, false)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Eventually(t, func() bool {
		for _, id := range ids {
			task, err := mgr.Get(id)
			if err != nil || task.State != UploadStateCompleted {
				return false
			}
		}
		return true
	}, 2*time.Second, 5*time.Millisecond)

	// Two prepared handles that will lapse in the same prune pass (their
	// expired snapshots get a fresh FinishedAt = prune time, i.e. the
	// youngest of the batch).
	pa, err := mgr.Prepare("expired-one.bin", mgr.PreparedTTL)
	require.NoError(t, err)
	pb, err := mgr.Prepare("expired-two.bin", mgr.PreparedTTL)
	require.NoError(t, err)

	base := time.Now().Add(-2 * time.Hour)
	mgr.mu.Lock()
	finishes := map[string]time.Time{
		ids[0]: base.Add(10 * time.Minute),
		ids[1]: base,
		ids[2]: base.Add(5 * time.Minute),
	}
	for id, fin := range finishes {
		mgr.tasks[id].task.FinishedAt = &fin
	}
	mgr.tasks[pa].task.CreatedAt = base.Add(-time.Minute)
	mgr.tasks[pb].task.CreatedAt = base.Add(-time.Minute)
	mgr.mu.Unlock()

	// A single prune pass tombstones all five. With the batch appended in
	// FinishedAt order, the terminal tombstones (which just crossed the
	// retention cutoff) sit at the FIFO head and retire in the SAME pass,
	// deterministically — never stranding behind the young expired snapshots.
	mgr.List()

	mgr.mu.Lock()
	require.Empty(t, mgr.tasks, "the pruned batch must leave the live task map")
	require.Len(t, mgr.tombstones, 2, "aged terminal tombstones must retire in the same pass, keeping only the fresh expired snapshots")
	require.ElementsMatch(t, []string{pa, pb}, tombstoneKeys(mgr.tombstones))
	require.ElementsMatch(t, []string{pa, pb}, mgr.tombstoneOrder,
		"the surviving FIFO must contain exactly the fresh expired snapshots")
	require.Len(t, mgr.tombstoneOrder, len(mgr.tombstones))

	// Retirement must walk the FIFO oldest-first: aging exactly the HEAD
	// tombstone past the retention window retires exactly it, leaving the
	// younger successor in place until it ages too. (The two expired
	// snapshots share a FinishedAt, so their relative FIFO order is a
	// stable-sort tie — aging the head keeps the pin order-independent.)
	aged, survivor := mgr.tombstoneOrder[0], mgr.tombstoneOrder[1]
	old := time.Now().Add(-2 * time.Hour)
	*mgr.tombstones[aged].FinishedAt = old
	mgr.mu.Unlock()

	mgr.List()

	mgr.mu.Lock()
	require.Equal(t, []string{survivor}, mgr.tombstoneOrder,
		"only the aged head tombstone must retire; the younger successor must stay")
	require.NotContains(t, mgr.tombstones, aged)
	require.Contains(t, mgr.tombstones, survivor)

	// Aging the remaining tombstone retires it and empties the FIFO.
	*mgr.tombstones[survivor].FinishedAt = old
	mgr.mu.Unlock()

	mgr.List()

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.tombstoneOrder, "the FIFO must be empty after the last tombstone retires")
	require.Empty(t, mgr.tombstones)
}

// tombstoneKeys lists the ids in a tombstone map (test helper).
func tombstoneKeys(m map[string]*UploadTask) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
