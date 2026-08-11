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
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/httpapi"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/live"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/platform/database"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/proxy"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
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

	// Read side: one goroutine per key that somebody is currently watching. The
	// request path writes usage once and publishes to it, so the dashboard shows
	// a call the moment it is recorded instead of re-querying on a timer.
	hub := live.NewHub(
		func(ctx context.Context, keyID uint, now time.Time) (usage.Report, error) {
			return usage.BuildReport(usage.ReportWindows(now), func(rg usage.Range) (int64, error) {
				return usageRepo.SumTokens(ctx, keyID, rg.From, rg.To)
			})
		},
		time.Now, live.DefaultResync, logger,
	)
	// The handler records through the hub's recorder: store first (authoritative),
	// then notify. It still only sees its own narrow interface.
	recorder := live.NewRecorder(usageRepo, hub)

	// Enforcement collaborators.
	limiter := ratelimit.New()
	budgetChecker := budget.NewChecker(usageRepo, time.Now)
	forwarder := proxy.NewHTTPForwarder(cfg.CaddyUpstreamURL, upstreamTimeout)
	// One admission queue for the whole process: it is the only thing that knows
	// how much of the Ollama pool is in use, so it must be shared by every
	// request rather than created per handler.
	pool := queue.New(cfg.Queue.MaxConcurrent, cfg.Queue.PromoteAfter)
	embeddings := proxy.NewHandler(budgetChecker, limiter, recorder, forwarder, pool, cfg.EmbedModel, time.Now, logger)

	// Operator dashboard over the same DB, plus a read-only view of the queue so
	// it can show live pool pressure.
	dashboard := web.NewServer(keyRepo, usageRepo, pool, hub, cfg.EmbedModel, time.Now, logger)

	// Public API documentation. It is built from the same config the gateway
	// enforces, so the limits it advertises cannot drift from the real ones.
	contact := web.NewContact(cfg.ContactEmail, cfg.ContactPhone, cfg.CompanyURL)
	landing := web.NewLanding(cfg.EmbedModel, cfg.Defaults.BatchMax, cfg.Defaults.RatePerMin, contact, logger)

	// The dashboard is operator-only and is not served without a credential:
	// it lists every key, its limits and its spend.
	var dashboardAuth func(http.Handler) http.Handler
	if cfg.Dashboard.Enabled() {
		dashboardAuth = httpapi.BasicAuth(cfg.Dashboard.User, cfg.Dashboard.Password, logger)
	} else {
		logger.Warn("dashboard disabled: set DASHBOARD_PASSWORD to serve it (the embeddings API is unaffected)")
	}

	router := httpapi.Router{
		Keys:          keyRepo,
		Embeddings:    embeddings,
		Landing:       landing.Handler(),
		Dashboard:     dashboard.Handler(),
		DashboardAuth: dashboardAuth,
		Logger:        logger,
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
			"addr", cfg.ListenAddr, "upstream", cfg.CaddyUpstreamURL, "model", cfg.EmbedModel,
			"pool_slots", cfg.Queue.MaxConcurrent, "promote_after", cfg.Queue.PromoteAfter)
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
