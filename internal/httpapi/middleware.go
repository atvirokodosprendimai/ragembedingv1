package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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

// dashboardRealm names the protection space in the WWW-Authenticate challenge.
// Browsers show it in the credential prompt and scope the saved password to it.
const dashboardRealm = "ragembed operator dashboard"

// BasicAuth guards the operator dashboard with a single HTTP Basic credential.
// The dashboard lists every key, its limits and its spend, so it is operator-only
// — a different audience from the API's per-client Bearer keys, hence a separate
// mechanism rather than a reuse of apikey.
//
// Security notes:
//   - Both fields are compared in constant time, over SHA-256 digests so the
//     comparison is fixed-width: neither the outcome nor its duration leaks how
//     long the credential is or how much of it was right.
//   - Username and password are always both compared, so a wrong username costs
//     the same as a wrong password and cannot be enumerated by timing.
//   - Basic auth sends the credential on every request, base64-encoded, not
//     encrypted. It is only private if TLS terminates in front of the gateway.
func BasicAuth(user, password string, logger *slog.Logger) func(http.Handler) http.Handler {
	// Digest the expected values once, at wiring time, rather than per request.
	wantUser := sha256.Sum256([]byte(user))
	wantPass := sha256.Sum256([]byte(password))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, ok := r.BasicAuth()
			if !ok {
				// No (or malformed) credentials: challenge rather than accuse, so
				// a browser shows its login prompt.
				challenge(w)
				return
			}

			gu := sha256.Sum256([]byte(gotUser))
			gp := sha256.Sum256([]byte(gotPass))
			userOK := subtle.ConstantTimeCompare(gu[:], wantUser[:]) == 1
			passOK := subtle.ConstantTimeCompare(gp[:], wantPass[:]) == 1
			if !userOK || !passOK {
				// Log the source but never the attempted credential: operators
				// mistype passwords into the username box, and this log is not
				// the place to collect them. RemoteAddr is used rather than a
				// forwarded header, which a client controls.
				logger.Warn("dashboard auth failed", "remote", r.RemoteAddr, "path", r.URL.Path)
				challenge(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// challenge asks for credentials. The dashboard is HTML, not the JSON API, so it
// answers in plain text rather than the OpenAI error envelope.
func challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="`+dashboardRealm+`", charset="UTF-8"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
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
