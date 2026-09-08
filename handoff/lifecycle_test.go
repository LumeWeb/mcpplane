package handoff

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.lumeweb.com/mcpplane/session"
)

// stubHandler is a minimal per-secret Handler used to drive the core.
type stubHandler struct {
	consumeOnGET bool
	consumedPOST bool
}

func (h *stubHandler) RenderGET(w http.ResponseWriter, _ *http.Request, _ string, _ *Item) {
	_, _ = w.Write([]byte("page"))
}
func (h *stubHandler) ConsumeOnGET() bool { return h.consumeOnGET }
func (h *stubHandler) ConsumePOST(http.ResponseWriter, *http.Request, string, *Item) bool {
	h.consumedPOST = true
	return true
}

// TestMintResolveConsumeLifecycle pins the mint -> pending -> consumed lifecycle:
// a minted token resolves to its pending item; GET with ConsumeOnGET removes it;
// the consumed token is tombstoned so a re-open reports USED (never a bare 404);
// the backing handle store entry is retired via the reaper contract's TTL.
func TestMintResolveConsumeLifecycle(t *testing.T) {
	h := New("testflow", &stubHandler{consumeOnGET: true}, time.Minute)
	defer h.Stop(context.Background())
	h.now = func() time.Time { return time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC) }

	token, ok := session.StrongRandomID(), true
	_ = ok
	h.items[token] = &Item{Payload: map[string]any{"secret": true}, expiresAt: h.now().Add(time.Minute)}

	item, reason := h.Resolve(token)
	require.NotNil(t, item)
	require.Empty(t, reason, "pending token has no spent reason")
	require.Equal(t, map[string]any{"secret": true}, item.Payload)

	// Consume it: tombstoned as USED, re-resolve reports the spent reason.
	h.Remove(token)
	item, reason = h.Resolve(token)
	require.Nil(t, item)
	require.Equal(t, ReasonUsed, reason, "consumed token must report USED, not silently 404")
	require.Contains(t, h.Spent(), token, "consumed token is retained as a tombstone")
}

// TestResolveExpiredToken pins the expiry path: a token past its TTL resolves
// to nil with ReasonExpired and is tombstoned.
func TestResolveExpiredToken(t *testing.T) {
	h := New("testflow", &stubHandler{}, time.Minute)
	defer h.Stop(context.Background())
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return base }

	h.items["tok"] = &Item{Payload: "x", expiresAt: base.Add(time.Minute)}

	h.now = func() time.Time { return base.Add(2 * time.Minute) }
	item, reason := h.Resolve("tok")
	require.Nil(t, item)
	require.Equal(t, ReasonExpired, reason)
	require.Contains(t, h.Spent(), "tok")
}

// TestUnknownTokenIsBare404 pins that a token that never existed resolves nil
// with NO spent reason (the dispatcher keeps its 404; only known-but-spent
// links get the explanation page).
func TestUnknownTokenIsBare404(t *testing.T) {
	h := New("testflow", &stubHandler{}, time.Minute)
	defer h.Stop(context.Background())
	item, reason := h.Resolve("never-existed")
	require.Nil(t, item)
	require.Empty(t, reason)
}

// TestSpentPageRendering pins the spent-page dispatch: a consumed/expired
// one-time URL renders the not-active page (410 Gone) through the injected
// renderer — not a bare 404 — while never-existing tokens keep 404.
func TestSpentPageRendering(t *testing.T) {
	var rendered []NotActiveReason
	page := func(w http.ResponseWriter, _ *http.Request, reason NotActiveReason, _ string) {
		rendered = append(rendered, reason)
		w.WriteHeader(http.StatusGone)
	}

	h := New("testflow", &stubHandler{}, time.Minute).WithNotActivePage(page)
	defer h.Stop(context.Background())
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return base }

	// A used token.
	h.items["used"] = &Item{Payload: "x", expiresAt: base.Add(time.Minute)}
	h.Remove("used")
	// An expired token.
	h.items["expired"] = &Item{Payload: "x", expiresAt: base.Add(time.Minute)}
	h.now = func() time.Time { return base.Add(2 * time.Minute) }

	mux := http.NewServeMux()
	h.RegisterHandlers(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/testflow/used")
	require.NoError(t, err)
	require.Equal(t, http.StatusGone, resp.StatusCode, "used link renders 410, not 404")
	_ = resp.Body.Close()

	resp, err = http.Get(srv.URL + "/testflow/expired")
	require.NoError(t, err)
	require.Equal(t, http.StatusGone, resp.StatusCode, "expired link renders 410")
	_ = resp.Body.Close()

	resp, err = http.Get(srv.URL + "/testflow/never-existed")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "unknown token keeps 404")
	_ = resp.Body.Close()

	// The injected page saw both spent reasons.
	require.ElementsMatch(t, []NotActiveReason{ReasonUsed, ReasonExpired}, rendered)
}

// TestDefaultSpentPage pins the built-in neutral page: HTML content type and a
// 410 status for both spent reasons (no product-specific theme needed).
func TestDefaultSpentPage(t *testing.T) {
	for _, tt := range []struct {
		reason NotActiveReason
		want   string
	}{
		{ReasonUsed, "cannot be used again"},
		{ReasonExpired, "expired before it was used"},
	} {
		rec := httptest.NewRecorder()
		defaultNotActivePage(rec, nil, tt.reason, detailForTest(tt.reason))
		require.Equal(t, http.StatusGone, rec.Code, tt.reason)
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		require.Contains(t, rec.Body.String(), tt.want)
	}
}

func detailForTest(r NotActiveReason) string {
	if r == ReasonExpired {
		return "This one-time link expired before it was used."
	}
	return "This one-time link cannot be used again."
}

// TestInstanceLoggerIsNotGlobal pins the boundary rule: the logger is
// instance state. A fresh endpoint has a usable (non-nil, safe default)
// logger; WithLogger replaces it per-instance; a nil injection is ignored.
func TestInstanceLoggerIsNotGlobal(t *testing.T) {
	h := New("a", &stubHandler{}, time.Minute)
	defer h.Stop(context.Background())
	require.NotNil(t, h.Logf(), "default logger must be safe (nop), never nil")

	named := zap.NewNop().With(zap.String("ep", "a"))
	h.WithLogger(named)
	assert.NotNil(t, h.Logf())

	// Nil injection must not clobber the existing logger.
	h.WithLogger(nil)
	require.NotNil(t, h.Logf())

	// Another endpoint is independent.
	other := New("b", &stubHandler{}, time.Minute)
	defer other.Stop(context.Background())
	require.NotNil(t, other.Logf())
}

// TestMaxSpentTombstonesFIFO pins the bounded tombstone retention: when the
// spent map exceeds MaxSpentTombstones, the OLDEST tombstones are evicted
// FIFO under the same lock as resolution.
func TestMaxSpentTombstonesFIFO(t *testing.T) {
	h := New("testflow", &stubHandler{}, time.Minute)
	defer h.Stop(context.Background())
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return base }

	// Fill beyond the cap.
	for i := 0; i < MaxSpentTombstones+10; i++ {
		token := "tok-" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + fmt.Sprintf("%d", i)
		h.items[token] = &Item{Payload: "x", expiresAt: base.Add(time.Minute)}
		h.Remove(token)
	}
	spent := h.Spent()
	require.LessOrEqual(t, len(spent), MaxSpentTombstones,
		"spent map must be capped at MaxSpentTombstones")
}

// TestClaimSingleUse pins Claim: it atomically removes the item and tombstones
// it so a second claim of the same token cannot re-enter blocking work.
func TestClaimSingleUse(t *testing.T) {
	h := New("testflow", &stubHandler{}, time.Minute)
	defer h.Stop(context.Background())
	base := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return base }

	h.items["tok"] = &Item{Payload: "x", expiresAt: base.Add(time.Minute)}

	require.True(t, h.Claim("tok"), "first claim wins")
	require.False(t, h.Claim("tok"), "second claim must not re-resolve the token")
	item, reason := h.Resolve("tok")
	require.Nil(t, item)
	require.Equal(t, ReasonUsed, reason)
}

// TestMintWithLoggerDoesNotDeadlock pins the non-reentrancy landmine: Mint
// mutated h.items under h.mu and then called Logf, which also acquires h.mu.
// A regression there hangs every one-time hand-off mint (oob login, seed drop,
// restore). Run Mint in a goroutine and t.Fatalf on timeout so a regression is
// caught as a failure, not a hung test. A logger IS set via WithLogger so the
// real Logf path (double h.mu acquisition in the bug) executes.
func TestMintWithLoggerDoesNotDeadlock(t *testing.T) {
	h := New("testflow", &stubHandler{}, time.Minute)
	defer h.Stop(context.Background())
	h.WithLogger(zap.NewNop().With(zap.String("ep", "test")))
	// Base-URL mode keeps EnsureLoopback a no-op, so the test is hermetic.
	h.SetBaseURL("https://tunnel.example.dev")

	minted := make(chan string, 1)
	go func() { minted <- h.Mint(map[string]any{"payload": true}) }()

	select {
	case url := <-minted:
		wantPrefix := "https://tunnel.example.dev/testflow/"
		require.True(t, strings.HasPrefix(url, wantPrefix),
			"Mint must return a valid one-time URL, got %q", url)
		token := strings.TrimPrefix(url, wantPrefix)
		require.NotEmpty(t, token, "Mint must mint a token in the URL path")
		item, reason := h.Resolve(token)
		require.NotNil(t, item, "minted token must resolve to a pending item")
		require.Empty(t, reason, "freshly minted token has no spent reason")
	case <-time.After(5 * time.Second):
		t.Fatal("Mint deadlocked: logging acquired h.mu while the lock was held")
	}
}

// TestSameOriginCSRF pins the browser-only CSRF gate: matching Origin passes,
// Referer fallback parses to scheme://host, and requests with neither header
// are rejected.
func TestSameOriginCSRF(t *testing.T) {
	r := func(headers map[string]string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "https://loopback/x", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req
	}
	require.True(t, SameOrigin(r(map[string]string{"Origin": "https://loopback"}), "https://loopback"))
	require.True(t, SameOrigin(r(map[string]string{"Origin": "https://LoopBack"}), "https://loopback"),
		"origin comparison is case-insensitive")
	require.True(t, SameOrigin(r(map[string]string{"Referer": "https://loopback/form?x=1"}), "https://loopback"),
		"Referer falls back to scheme://host matching")
	require.False(t, SameOrigin(r(map[string]string{"Origin": "https://evil"}), "https://loopback"))
	require.False(t, SameOrigin(r(nil), "https://loopback"), "no Origin/Referer at all is rejected")
}
