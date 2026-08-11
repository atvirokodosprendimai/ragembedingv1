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
		Landing:       landingStub(),
		Dashboard:     dashboard,
		DashboardAuth: auth,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}.Handler()
}

// landingStub stands in for the public documentation page.
func landingStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("how to call the API"))
	})
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

	for _, path := range []string{"/admin", "/admin/keys/1", "/admin/queue", "/admin/assets/dashboard.css"} {
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

	w := get(t, h, "/admin")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.NotContains(t, w.Body.String(), "every key")
}

// TestLandingIsPublic: the site root is documentation for API users, served
// without credentials, and it must never expose operator data.
func TestLandingIsPublic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := testRouter(t, dashboardStub(), BasicAuth("admin", "hunter2", logger))

	for _, path := range []string{"/", "/kada-reikia-embeddingu-api", "/robots.txt", "/sitemap.xml", "/llms.txt"} {
		w := get(t, h, path)
		require.Equalf(t, http.StatusOK, w.Code, "public path %s", path)
		require.NotContains(t, w.Body.String(), "every key")
	}

	// An unknown path must 404 rather than fall through to a marketing page: a
	// mistyped API route answering 200 with HTML is worse than a clean error.
	require.Equal(t, http.StatusNotFound, get(t, h, "/nera-tokio-puslapio").Code)
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
