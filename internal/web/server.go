package web

import (
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
)

// assets embeds the dashboard stylesheet so the gateway serves it itself, with
// no external files to deploy. It is a real .css file (not templ) so CSS braces
// never collide with templ's expression syntax.
//
//go:embed static/dashboard.css
var assets embed.FS

// poolReporter is the dashboard's read-only view of the admission queue. It is
// declared at the consumer so the dashboard depends on "tell me the pressure"
// and nothing else — it can neither admit nor release.
type poolReporter interface {
	Stats() queue.Stats
}

// Server renders the operator usage dashboard over the key and usage stores. It
// is read-only: it reports what keys exist, how many bge-m3 tokens each has
// spent across the reporting buckets, and how contended the Ollama pool is right
// now, but issues no keys (that is ragctl's job).
type Server struct {
	keys   apikey.Repository
	usage  usage.Repository
	pool   poolReporter
	model  string
	now    func() time.Time
	logger *slog.Logger
}

// NewServer constructs the dashboard server. now is injectable so bucket
// boundaries are deterministic in tests.
func NewServer(keys apikey.Repository, usage usage.Repository, pool poolReporter, model string, now func() time.Time, logger *slog.Logger) *Server {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{keys: keys, usage: usage, pool: pool, model: model, now: now, logger: logger}
}

// Handler returns the dashboard's routes: the full page, the per-key detail
// fragment (a datastar SSE patch), and the embedded stylesheet.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.handleIndex)
	r.Get("/keys/{id}", s.handleKeyDetail)
	r.Get("/queue", s.handleQueue)

	// Serve /assets/dashboard.css from the embedded FS.
	sub, _ := fs.Sub(assets, "static")
	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	return r
}

// handleIndex renders the whole dashboard.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	vm, err := s.buildPage(r.Context())
	if err != nil {
		s.logger.Error("dashboard: build page", "err", err)
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := Page(vm).Render(r.Context(), w); err != nil {
		// The header is already committed; log and move on.
		s.logger.Error("dashboard: render page", "err", err)
	}
}

// handleQueue patches the live pressure strip. It is polled every couple of
// seconds while the panel is live, so it must stay cheap: reading the queue is
// one mutex-guarded snapshot with no database work at all.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElementTempl(QueuePanel(buildQueue(s.pool.Stats()))); err != nil {
		s.logger.Error("dashboard: patch queue", "err", err)
	}
}

// handleKeyDetail returns one key's detail as a datastar element patch. Following
// the datastar convention, every outcome — including "not found" and errors — is
// a 200 carrying a fragment that morphs #detail, so the client always updates the
// same slot rather than seeing a broken request.
func (s *Server) handleKeyDetail(w http.ResponseWriter, r *http.Request) {
	sse := datastar.NewSSE(w, r)

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		_ = sse.PatchElementTempl(detailError("invalid key id"))
		return
	}

	key, err := s.keys.ByID(r.Context(), uint(id))
	if errors.Is(err, apikey.ErrNotFound) {
		_ = sse.PatchElementTempl(detailError("key not found"))
		return
	}
	if err != nil {
		s.logger.Error("dashboard: load key", "id", id, "err", err)
		_ = sse.PatchElementTempl(detailError("could not load key"))
		return
	}

	detail, err := s.buildDetail(r.Context(), *key, s.now())
	if err != nil {
		s.logger.Error("dashboard: build detail", "id", id, "err", err)
		_ = sse.PatchElementTempl(detailError("could not load usage"))
		return
	}
	if err := sse.PatchElementTempl(Detail(detail)); err != nil {
		s.logger.Error("dashboard: patch detail", "id", id, "err", err)
	}
}
