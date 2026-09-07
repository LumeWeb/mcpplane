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
