package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// guarded wraps a handler that records whether it was ever reached, which is the
// real assertion for an auth middleware: a rejected request must not run it.
func guarded(t *testing.T, user, password string) (http.Handler, *bool) {
	t.Helper()
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return BasicAuth(user, password, logger)(next), &reached
}

func TestBasicAuthChallengesWithoutCredentials(t *testing.T) {
	h, reached := guarded(t, "admin", "hunter2")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusUnauthorized, w.Code)
	// The challenge is what makes a browser show its login prompt.
	require.Contains(t, w.Header().Get("WWW-Authenticate"), `Basic realm="ragembed operator dashboard"`)
	require.False(t, *reached)
}

func TestBasicAuthRejectsWrongCredentials(t *testing.T) {
	cases := map[string]struct{ user, pass string }{
		"wrong password":    {"admin", "wrong"},
		"wrong user":        {"root", "hunter2"},
		"both wrong":        {"root", "wrong"},
		"empty password":    {"admin", ""},
		"password as user":  {"hunter2", "hunter2"},
		"prefix of correct": {"admin", "hunter"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, reached := guarded(t, "admin", "hunter2")

			r := httptest.NewRequest(http.MethodGet, "/keys/1", nil)
			r.SetBasicAuth(c.user, c.pass)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.False(t, *reached, "a rejected request must not reach the dashboard")
		})
	}
}

func TestBasicAuthRejectsMalformedHeader(t *testing.T) {
	h, reached := guarded(t, "admin", "hunter2")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer sk-rag-something") // an API key is not a dashboard credential
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.False(t, *reached)
}

func TestBasicAuthAdmitsCorrectCredentials(t *testing.T) {
	h, reached := guarded(t, "admin", "hunter2")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth("admin", "hunter2")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, *reached)
}
