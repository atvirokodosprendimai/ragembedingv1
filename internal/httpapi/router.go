package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/proxy"
)

// Router assembles the gateway's HTTP surface from its collaborators. Fields are
// the composition root's responsibility to populate; Dashboard is optional and
// only mounted when non-nil.
type Router struct {
	// Keys resolves API keys for the auth middleware.
	Keys apikey.Repository
	// Embeddings is the /v1/embeddings enforcement handler.
	Embeddings *proxy.Handler
	// Dashboard, if set, is mounted at the site root for the usage UI.
	Dashboard http.Handler
	// Logger is used by middleware and handlers.
	Logger *slog.Logger
}

// Handler builds the chi router. The embeddings API sits behind Bearer auth; the
// health check is public; the dashboard (operator-facing) is mounted last so its
// routes never shadow the API.
func (rt Router) Handler() http.Handler {
	r := chi.NewRouter()

	// Baseline middleware: a request id for correlation and panic recovery so one
	// bad request can't take the process down. Client IP is deliberately not
	// derived from forwarded headers (spoofable) — enforcement is per API key.
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// Public liveness probe.
	r.Get("/healthz", healthz)

	// Authenticated embeddings API.
	r.Group(func(r chi.Router) {
		r.Use(BearerAuth(rt.Keys, rt.Logger))
		r.Post("/v1/embeddings", rt.Embeddings.Embeddings)
	})

	// Operator dashboard (added in the web task). Mounted at root so it serves
	// "/", "/keys/{id}", static assets, etc.
	if rt.Dashboard != nil {
		r.Mount("/", rt.Dashboard)
	}

	return r
}

// healthz is a minimal liveness probe. It reports the process is up and serving;
// deeper readiness (DB, upstream) is intentionally out of scope for a liveness
// check that must never flap.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
