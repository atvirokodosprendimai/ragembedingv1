package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// dashboardStub stands in for the real dashboard: reaching it at all is the
// failure this test suite is about.
func dashboardStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("every key and its spend"))
	})
}

func testRouter(t *testing.T, dashboard http.Handler, auth func(http.Handler) http.Handler) http.Handler {
	t.Helper()
	return Router{
		Dashboard:     dashboard,
		DashboardAuth: auth,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}.Handler()
}

func get(t *testing.T, h http.Handler, path string, creds ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if len(creds) == 2 {
		r.SetBasicAuth(creds[0], creds[1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestDashboardRequiresCredentials covers the whole dashboard surface, not just
// its index: the key detail, the queue poll and the static assets are all served
// from the same mount and must all be guarded.
func TestDashboardRequiresCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := testRouter(t, dashboardStub(), BasicAuth("admin", "hunter2", logger))

	for _, path := range []string{"/", "/keys/1", "/queue", "/assets/dashboard.css"} {
		t.Run(path, func(t *testing.T) {
			w := get(t, h, path)
			require.Equal(t, http.StatusUnauthorized, w.Code)
			require.NotContains(t, w.Body.String(), "every key")

			ok := get(t, h, path, "admin", "hunter2")
			require.Equal(t, http.StatusOK, ok.Code)
		})
	}
}

// TestDashboardIsNotServedWithoutAuth is the fail-closed guarantee: a wiring
// mistake that drops the middleware must take the dashboard offline, never
// publish it.
func TestDashboardIsNotServedWithoutAuth(t *testing.T) {
	h := testRouter(t, dashboardStub(), nil)

	w := get(t, h, "/")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.NotContains(t, w.Body.String(), "every key")
}

// TestHealthzStaysPublic: the liveness probe must not need a credential, or load
// balancers will mark a healthy gateway down.
func TestHealthzStaysPublic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := testRouter(t, dashboardStub(), BasicAuth("admin", "hunter2", logger))

	w := get(t, h, "/healthz")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}
