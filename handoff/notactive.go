package handoff

import (
	"fmt"
	"html"
	"net/http"

	"go.uber.org/zap"
)

// NotActiveReason describes why a one-time hand-off URL is no longer active:
// it was consumed (used) or its TTL elapsed (expired).
type NotActiveReason string

const (
	ReasonExpired NotActiveReason = "expired"
	ReasonUsed    NotActiveReason = "used"
)

// String returns the wire form of the reason.
func (r NotActiveReason) String() string { return string(r) }

// NotActivePageFunc renders the "link no longer active" page a human sees when
// they re-open a consumed or expired one-time URL. Concrete servers may inject
// a branded page (Endpoint.WithNotActivePage); the zero configuration renders
// a minimal, neutral HTML page — enough to explain the state, with no
// product-specific theme.
type NotActivePageFunc func(w http.ResponseWriter, r *http.Request, reason NotActiveReason, detail string)

// defaultNotActivePage is the neutral built-in spent-link page.
func defaultNotActivePage(w http.ResponseWriter, _ *http.Request, reason NotActiveReason, detail string) {
	title := "Link No Longer Active"
	if reason == ReasonExpired {
		title = "Link Expired"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	body := fmt.Sprintf(
		"<!DOCTYPE html><html><head><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(detail),
	)
	_, _ = w.Write([]byte(body))
}

// nopLogger is the per-instance safe default for hand-off endpoint lifecycle
// logging. Endpoints log only lifecycle events (mint, consume, expire); a
// caller that wants them surfaced injects its logger via WithLogger, so the
// package keeps no global logger state.
func nopLogger() *zap.Logger { return zap.NewNop() }
