package loopback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// These tests pin the behavior of the dual-mode LoopbackServer. The loopback
// transport is central to local host connections, so these intentionally
// document its observable behavior and guard it against regressions.

func TestURLForBaseURLMode(t *testing.T) {
	l := &LoopbackServer{}
	l.SetBaseURL("https://tunnel.example.dev/")
	// Trailing slash is trimmed; URLFor joins prefix/token under the base URL.
	if got := l.URLFor("auth", "tok123"); got != "https://tunnel.example.dev/auth/tok123" {
		t.Fatalf("URLFor = %q", got)
	}
	if got := l.Origin(); got != "https://tunnel.example.dev" {
		t.Fatalf("Origin = %q", got)
	}
}

func TestURLForLoopbackMode(t *testing.T) {
	l := &LoopbackServer{}
	if err := l.EnsureLoopback(func(mux *http.ServeMux) {
		mux.HandleFunc("/seed/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("seed-page-body"))
		})
	}); err != nil {
		t.Fatalf("EnsureLoopback: %v", err)
	}
	t.Cleanup(func() { l.Stop(context.Background()) })

	url := l.URLFor("seed", "tok456")
	if url[:7] != "http://" {
		t.Fatalf("URLFor must be loopback http URL, got %q", url)
	}
	// The loopback listener actually serves the registered route.
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestEnsureLoopbackNoopWhenBaseURLSet(t *testing.T) {
	l := &LoopbackServer{}
	l.SetBaseURL("https://tunnel.example.dev")
	// HTTP/tunnel mode: a base URL is set, so no loopback listener is bound —
	// there must be no redundant port opened.
	if err := l.EnsureLoopback(func(mux *http.ServeMux) {}); err != nil {
		t.Fatalf("EnsureLoopback: %v", err)
	}
	if got := l.Origin(); got != "https://tunnel.example.dev" {
		t.Fatalf("Origin = %q, want base URL", got)
	}
}

func TestEnsureLoopbackIdempotent(t *testing.T) {
	l := &LoopbackServer{}
	if err := l.EnsureLoopback(nil); err != nil {
		t.Fatalf("EnsureLoopback: %v", err)
	}
	first := l.Origin()
	if err := l.EnsureLoopback(nil); err != nil {
		t.Fatalf("second EnsureLoopback: %v", err)
	}
	if again := l.Origin(); again != first {
		t.Fatalf("listener re-bound: %q then %q", first, again)
	}
	l.Stop(context.Background())
}

func TestAcceptedOriginsIncludesTrusted(t *testing.T) {
	l := &LoopbackServer{}
	l.SetBaseURL("https://tunnel.example.dev")
	l.AddTrustedOrigins("https://host-a.example", "https://host-b.example/", "https://HOST-A.example")
	got := l.AcceptedOrigins()
	// Base origin first, trusted origins deduplicated (case-insensitive,
	// trailing slash trimmed).
	want := []string{"https://tunnel.example.dev", "https://host-a.example", "https://host-b.example"}
	if len(got) != len(want) {
		t.Fatalf("AcceptedOrigins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AcceptedOrigins[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAcceptedOriginsNoBaseURL(t *testing.T) {
	l := &LoopbackServer{}
	got := l.AcceptedOrigins()
	// Without a base URL or listener the loopback placeholder origin is used.
	if len(got) != 1 || got[0] != "http://127.0.0.1:0" {
		t.Fatalf("AcceptedOrigins = %v", got)
	}
}

func TestConcurrencySafeUse(t *testing.T) {
	l := &LoopbackServer{}
	l.SetBaseURL("https://tunnel.example.dev")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.URLFor("x", "y")
			_ = l.Origin()
			_ = l.AcceptedOrigins()
			l.AddTrustedOrigins("https://z.example")
		}()
	}
	wg.Wait()
}

func TestStopShutsDownListener(t *testing.T) {
	l := &LoopbackServer{}
	if err := l.EnsureLoopback(nil); err != nil {
		t.Fatalf("EnsureLoopback: %v", err)
	}
	url := l.Origin()
	l.Stop(context.Background())
	// After Stop the listener is gone: request must fail.
	client := &http.Client{Timeout: 2 * time.Second}
	if _, err := client.Get(url + "/anything"); err == nil {
		t.Fatal("expected request to fail after Stop")
	}
	// Idempotent.
	l.Stop(context.Background())
}

func TestSetSafeAfterConstruction(t *testing.T) {
	// SetBaseURL after the loopback listener started switches to base mode: the
	// origin flips to the base URL without rebinding (documented behavior).
	l := &LoopbackServer{}
	if err := l.EnsureLoopback(nil); err != nil {
		t.Fatalf("EnsureLoopback: %v", err)
	}
	t.Cleanup(func() { l.Stop(context.Background()) })
	base := httptest.NewServer(http.NotFoundHandler())
	defer base.Close()
	l.SetBaseURL(base.URL)
	if got := l.Origin(); got != base.URL {
		t.Fatalf("Origin = %q, want %q", got, base.URL)
	}
}
