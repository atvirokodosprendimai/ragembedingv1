// Command gateway is the Ollama embeddings proxy: it authenticates API keys,
// enforces per-token batch/rate/budget limits, forwards accepted requests to the
// Caddy load balancer, and serves the usage dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/budget"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/config"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/httpapi"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/platform/database"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/proxy"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/ratelimit"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/web"
)

// upstreamTimeout bounds a single forwarded embedding call. Embedding under load
// can be slow, so this is generous relative to a typical JSON API.
const upstreamTimeout = 60 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil {
		logger.Error("gateway exited with error", "err", err)
		os.Exit(1)
	}
}

// run wires the dependency graph and serves until an interrupt. It returns an
// error rather than calling os.Exit so the wiring stays testable and defers run.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Storage: open the no-cgo SQLite DB and bring the schema up to date.
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	if err := database.Migrate(db); err != nil {
		return err
	}
	keyRepo := database.NewAPIKeyRepo(db)
	usageRepo := database.NewUsageRepo(db)

	// Enforcement collaborators.
	limiter := ratelimit.New()
	budgetChecker := budget.NewChecker(usageRepo, time.Now)
	forwarder := proxy.NewHTTPForwarder(cfg.CaddyUpstreamURL, upstreamTimeout)
	embeddings := proxy.NewHandler(budgetChecker, limiter, usageRepo, forwarder, cfg.EmbedModel, time.Now, logger)

	// Operator dashboard over the same DB.
	dashboard := web.NewServer(keyRepo, usageRepo, cfg.EmbedModel, time.Now, logger)

	router := httpapi.Router{
		Keys:       keyRepo,
		Embeddings: embeddings,
		Dashboard:  dashboard.Handler(),
		Logger:     logger,
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: router.Handler(),
		// Conservative timeouts guard against slow-loris style clients; the read
		// timeout is generous because embedding request bodies can be large.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      upstreamTimeout + 15*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Serve until an interrupt, then drain in-flight requests before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("gateway listening",
			"addr", cfg.ListenAddr, "upstream", cfg.CaddyUpstreamURL, "model", cfg.EmbedModel)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		// ErrServerClosed only happens after Shutdown, which this path pre-empts.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
