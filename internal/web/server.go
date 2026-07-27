package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// Server renders the operator usage dashboard over the key and usage stores. It
// is read-only: it reports what keys exist and how many bge-m3 tokens each has
// spent across the reporting buckets, but issues no keys (that is ragctl's job).
type Server struct {
	keys   apikey.Repository
	usage  usage.Repository
	model  string
	now    func() time.Time
	logger *slog.Logger
}

// NewServer constructs the dashboard server. now is injectable so bucket
// boundaries are deterministic in tests.
func NewServer(keys apikey.Repository, usage usage.Repository, model string, now func() time.Time, logger *slog.Logger) *Server {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{keys: keys, usage: usage, model: model, now: now, logger: logger}
}

// Handler returns the dashboard's routes. The full terminal/data-ops UI is built
// in the web task; this placeholder keeps the composition root wired and the
// route reachable in the meantime.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ragembed dashboard — coming online"))
	})
	return mux
}
