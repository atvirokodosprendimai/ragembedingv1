package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/proxy"
)

// keyLookup is the one capability the auth middleware needs from persistence:
// resolve a key by its hash. Declaring it here (rather than depending on the
// full apikey.Repository) keeps the middleware's contract minimal.
type keyLookup interface {
	ByHash(ctx context.Context, hash string) (*apikey.APIKey, error)
}

// BearerAuth authenticates requests by the Authorization: Bearer <key> header.
// It resolves the key by hash, rejects unknown or revoked keys, and — on success
// — attaches the key to the request context for the handler to read. Errors use
// the same OpenAI-style envelope as the rest of the gateway.
//
// Timing note: an unknown key and a revoked key both return 401 so a caller
// cannot distinguish "no such key" from "revoked key" by status alone.
func BearerAuth(keys keyLookup, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r.Header.Get("Authorization"))
			if token == "" {
				proxy.WriteError(w, http.StatusUnauthorized, "invalid_request_error",
					"missing or malformed Authorization header (expected 'Bearer <key>')")
				return
			}

			k, err := keys.ByHash(r.Context(), apikey.HashKey(token))
			if errors.Is(err, apikey.ErrNotFound) {
				proxy.WriteError(w, http.StatusUnauthorized, "invalid_request_error", "invalid API key")
				return
			}
			if err != nil {
				logger.Error("api key lookup failed", "err", err)
				proxy.WriteError(w, http.StatusInternalServerError, "internal_error", "could not verify API key")
				return
			}
			if k.IsRevoked() {
				proxy.WriteError(w, http.StatusUnauthorized, "invalid_request_error", "invalid API key")
				return
			}

			next.ServeHTTP(w, r.WithContext(proxy.WithAPIKey(r.Context(), k)))
		})
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header,
// or returns "" if the header is absent or not a bearer credential. The scheme
// match is case-insensitive per RFC 7235.
func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
