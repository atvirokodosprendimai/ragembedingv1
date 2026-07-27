package proxy

import (
	"context"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
)

// ctxKey is an unexported type so the context value cannot collide with keys set
// by other packages.
type ctxKey struct{}

// apiKeyCtxKey is the single key under which the authenticated APIKey is stored.
var apiKeyCtxKey ctxKey

// WithAPIKey returns a copy of ctx carrying the authenticated key. The auth
// middleware calls this once a Bearer token resolves to an active key.
func WithAPIKey(ctx context.Context, k *apikey.APIKey) context.Context {
	return context.WithValue(ctx, apiKeyCtxKey, k)
}

// APIKeyFrom extracts the authenticated key from ctx. ok is false when no key is
// present, which the handler treats as an unauthenticated request.
func APIKeyFrom(ctx context.Context) (*apikey.APIKey, bool) {
	k, ok := ctx.Value(apiKeyCtxKey).(*apikey.APIKey)
	return k, ok
}
