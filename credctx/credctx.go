// Package credctx is the neutral leaf package that owns the context key under
// which the per-request API credential JWT is carried. It lives in its own
// package so that out-of-band transport machinery (transfer uploads/downloads,
// hand-off endpoints) and the server assembly layers can both read/write the
// same credential from a context without an import cycle.
package credctx

import "context"

type key struct{}

// With stores the API JWT in ctx under the shared unexported key.
func With(ctx context.Context, jwt string) context.Context {
	return context.WithValue(ctx, key{}, jwt)
}

// From returns the API JWT stored on ctx, or "" when absent.
func From(ctx context.Context) string {
	if v, ok := ctx.Value(key{}).(string); ok {
		return v
	}
	return ""
}
