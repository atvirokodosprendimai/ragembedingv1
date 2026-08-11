package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/live"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
)

// assets embeds the dashboard and landing stylesheets so the gateway serves them
// itself, with no external files to deploy. They are real .css files (not templ)
// so CSS braces never collide with templ's expression syntax.
//
//go:embed static/dashboard.css static/landing.css
var assets embed.FS

// poolReporter is the dashboard's read-only view of the admission queue. It is
// declared at the consumer so the dashboard depends on "tell me the pressure"
// and nothing else — it can neither admit nor release.
type poolReporter interface {
	Stats() queue.Stats
}

// liveHub is the read side the dashboard streams from: subscribe to a key and
// receive a snapshot whenever its usage changes. Declared at the consumer so the
// dashboard cannot publish, only watch.
type liveHub interface {
	Subscribe(ctx context.Context, keyID uint) (<-chan live.Snapshot, func())
}

// queueInterval is how often the live stream repaints the pool-pressure strip.
// The queue changes continuously and has no change events to subscribe to, so
// the server picks the cadence — and being the server's choice, it can be tuned
// without touching a single line of markup.
const queueInterval = 5 * time.Second

// Server renders the operator usage dashboard over the key and usage stores. It
// is read-only: it reports what keys exist, how many bge-m3 tokens each has
// spent across the reporting buckets, and how contended the Ollama pool is right
// now, but issues no keys (that is ragctl's job).
//
// It is a multi-page app: each key is a real URL, and one SSE connection per
// page pushes everything that moves.
type Server struct {
	keys   apikey.Repository
	usage  usage.Repository
	pool   poolReporter
	hub    liveHub
	model  string
	now    func() time.Time
	logger *slog.Logger
}

// NewServer constructs the dashboard server. now is injectable so bucket
// boundaries are deterministic in tests.
func NewServer(keys apikey.Repository, usage usage.Repository, pool poolReporter, hub liveHub, model string, now func() time.Time, logger *slog.Logger) *Server {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{keys: keys, usage: usage, pool: pool, hub: hub, model: model, now: now, logger: logger}
}

// Handler returns the dashboard's routes: a page per key, the single live
// stream that drives it, and the embedded stylesheet.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.handleIndex)
	r.Get("/keys/{id}", s.handleKeyPage)
	r.Get("/keys/{id}/live", s.handleKeyLive)

	// Serve the stylesheet from the embedded FS. The prefix includes BasePath
	// because chi's Mount routes on its own RoutePath and leaves r.URL.Path
	// untouched — StripPrefix sees the full "/admin/assets/…" and would 404 on a
	// bare "/assets/" prefix (which is exactly what happened when the dashboard
	// moved off the site root, serving the whole UI unstyled).
	sub, _ := fs.Sub(assets, "static")
	r.Handle("/assets/*", http.StripPrefix(BasePath+"/assets/", http.FileServer(http.FS(sub))))
	return r
}

// handleIndex sends the operator to a key's page. The dashboard is a
// multi-page app, so every view has its own URL; the index is just the door.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	keys, err := s.keys.List(r.Context())
	if err != nil {
		s.logger.Error("dashboard: list keys", "err", err)
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	if id, ok := defaultKeyID(keys); ok {
		http.Redirect(w, r, fmt.Sprintf("%s/keys/%d", BasePath, id), http.StatusSeeOther)
		return
	}
	// No keys yet: render the empty state in place rather than redirecting
	// somewhere that does not exist.
	s.render(w, r, PageVM{Model: s.model, Queue: buildQueue(s.pool.Stats())})
}

// handleKeyPage renders one key's page: masthead, pool pressure, the key list
// and that key's detail. A real page for a real URL — bookmarkable, shareable,
// and reachable with the back button.
func (s *Server) handleKeyPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	vm, err := s.buildPage(r.Context(), uint(id))
	if errors.Is(err, apikey.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.logger.Error("dashboard: build page", "id", id, "err", err)
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, vm)
}

// render writes a page, logging a render failure the client will never see
// because the header is already committed.
func (s *Server) render(w http.ResponseWriter, r *http.Request, vm PageVM) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := Page(vm).Render(r.Context(), w); err != nil {
		s.logger.Error("dashboard: render page", "err", err)
	}
}

// handleKeyLive is the page's single live connection. One SSE stream carries
// everything that moves: the pool-pressure strip on the server's own cadence,
// and this key's usage the moment a call is recorded against it.
//
// The server decides how long to hold the connection and how often to speak;
// the page only says "subscribe". It ends when the client goes away — closing
// the tab or navigating to another key cancels the request context, which
// unsubscribes and retires the key's goroutine if nobody else is watching.
func (s *Server) handleKeyLive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	keyID := uint(id)

	key, err := s.keys.ByID(r.Context(), keyID)
	if err != nil {
		// A stream for a key that does not exist has nothing to say; the page
		// itself already 404'd, so this is only reachable by a hand-made request.
		http.NotFound(w, r)
		return
	}

	// The server's WriteTimeout exists to shed slow clients on ordinary requests,
	// but this response is deliberately long-lived: left in place it would cut
	// every dashboard's stream dead at WriteTimeout with nothing on screen to say
	// so. Clear the deadline for this connection only.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		// Not fatal: on a ResponseWriter that cannot do this the stream still
		// works, it just ends when the server's timeout says so.
		s.logger.Warn("dashboard: cannot clear write deadline for the live stream", "err", err)
	}

	// The watched key's usage appears in three places on screen. Read the sidebar
	// context once, at connect, so each pushed update can keep all three in step
	// without another query — a header that contradicts the panel under it is
	// worse than one that updates a moment later.
	sidebar, err := s.sidebarContext(r.Context(), keyID)
	if err != nil {
		s.logger.Error("dashboard: live sidebar context", "key_id", keyID, "err", err)
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}

	snapshots, unsubscribe := s.hub.Subscribe(r.Context(), keyID)
	defer unsubscribe()

	sse := datastar.NewSSE(w, r)
	ticker := time.NewTicker(queueInterval)
	defer ticker.Stop()

	// Paint the pressure strip immediately so the first frame is not empty; the
	// key detail arrives from the hub's priming snapshot a moment later.
	if err := sse.PatchElementTempl(QueuePanel(buildQueue(s.pool.Stats()))); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			// Client gone (tab closed, navigated away) or the gateway is shutting
			// down. Returning runs the deferred unsubscribe.
			return

		case <-ticker.C:
			if err := sse.PatchElementTempl(QueuePanel(buildQueue(s.pool.Stats()))); err != nil {
				return
			}

		case snap := <-snapshots:
			// One snapshot, three fragments — the panel, the key's row in the
			// sidebar, and the masthead aggregate — so the whole screen agrees.
			if err := sse.PatchElementTempl(Detail(s.detailFrom(*key, snap))); err != nil {
				return
			}
			if err := sse.PatchElementTempl(keyRow(sidebar.row(*key, snap.Report.Today))); err != nil {
				return
			}
			if err := sse.PatchElementTempl(TokensToday(sidebar.totalLabel(snap.Report.Today))); err != nil {
				return
			}
		}
	}
}
