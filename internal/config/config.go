package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Budget period identifiers. These mirror apikey.BudgetPeriod but are kept as
// bare strings here so config carries no dependency on the domain layer (the
// dependency only ever points domain -> config, never the reverse).
const (
	periodMonthly  = "monthly"
	periodLifetime = "lifetime"
)

// maxPriority mirrors apikey.MaxPriority as a bare int, for the same
// no-dependency-on-the-domain reason as the period identifiers above.
const maxPriority = 9

// Defaults are the per-token limits applied to a new API key when the operator
// does not override them at creation time. They are the fallbacks the whole
// system leans on, so they live in one struct that both the CLI (issuing keys)
// and the gateway (validating requests) read.
type Defaults struct {
	// BatchMax is the largest number of inputs allowed in one /v1/embeddings
	// request. The product default is 25.
	BatchMax int
	// RatePerMin is the largest number of requests a key may make per minute.
	// The product default is 400.
	RatePerMin int
	// TokenBudget caps bge-m3 input tokens. -1 means unlimited (on-demand); any
	// positive value is a prepaid allowance.
	TokenBudget int64
	// BudgetPeriod is either "monthly" (allowance resets each calendar month) or
	// "lifetime" (cumulative, never resets).
	BudgetPeriod string
	// Priority is the admission-queue rank a new key is issued with. 0 is the
	// free tier; the operator raises it (up to 9) for front-of-house traffic.
	Priority int
}

// Dashboard is the operator dashboard's access control. The dashboard lists
// every key, its limits and its spend, so it is operator-only: with no password
// configured the gateway refuses to serve it at all rather than exposing it.
type Dashboard struct {
	// User is the Basic-auth username.
	User string
	// Password is the Basic-auth password. Empty means "no credentials
	// configured", which disables the dashboard. The embeddings API is
	// unaffected either way.
	Password string
}

// Enabled reports whether the dashboard has credentials and may be served.
func (d Dashboard) Enabled() bool { return d.Password != "" }

// Queue configures the admission queue that fronts the Ollama pool. Its job is
// to keep the operator's own site off the back of a batch client's flood, so the
// two knobs are "how much work the pool can take at once" and "how long anyone
// may be made to wait for it".
type Queue struct {
	// MaxConcurrent is how many requests may be in flight upstream at once. It
	// should match what the pool behind Caddy can actually chew through — one
	// slot per Ollama backend is the sane starting point. Set it too high and
	// the queue stops meaning anything (everything piles up inside Ollama
	// instead, where the gateway cannot rank it); too low and the pool idles.
	MaxConcurrent int
	// PromoteAfter is how long a request may sit in the queue before it is
	// admitted ahead of higher-priority work. It is the anti-starvation valve
	// that keeps "priority" from becoming "free users never get served".
	PromoteAfter time.Duration
}

// Config is the fully resolved gateway configuration.
type Config struct {
	// ListenAddr is the gateway's own HTTP bind address (host:port).
	ListenAddr string
	// DBPath is the SQLite file path for keys and usage.
	DBPath string
	// CaddyUpstreamURL is the single URL the gateway forwards embedding requests
	// to; Caddy load-balances it across the Ollama backends.
	CaddyUpstreamURL string
	// EmbedModel is the embedding model name recorded with usage (bge-m3).
	EmbedModel string
	// ContactEmail is published on the landing page as the address to ask for a
	// key at. It is operator-facing contact detail, not a secret.
	ContactEmail string
	// Defaults are the per-token limit fallbacks.
	Defaults Defaults
	// Queue is the upstream admission queue's configuration.
	Queue Queue
	// Dashboard guards the operator UI.
	Dashboard Dashboard
}

// Load resolves configuration from the process environment, first sourcing a
// .env file in the working directory if one exists. Real environment variables
// always win over .env values (godotenv never overwrites an already-set var),
// which is the precedence operators expect: .env for local defaults, real env
// for deployment overrides. Load validates the result so a misconfigured
// gateway fails fast at startup rather than mid-request.
func Load() (Config, error) {
	// A missing .env is normal (deployments inject real env vars), so only a
	// present-but-unreadable file is worth surfacing.
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			return Config{}, fmt.Errorf("config: loading .env: %w", err)
		}
	}

	cfg := Config{
		ListenAddr:       envStr("LISTEN_ADDR", ":8080"),
		DBPath:           envStr("DB_PATH", "ragembed.db"),
		CaddyUpstreamURL: envStr("CADDY_UPSTREAM_URL", "http://localhost:11435"),
		EmbedModel:       envStr("EMBED_MODEL", "bge-m3"),
		ContactEmail:     envStr("CONTACT_EMAIL", "info@ituoga.lt"),
		Defaults: Defaults{
			BatchMax:     envInt("DEFAULT_BATCH_MAX", 25),
			RatePerMin:   envInt("DEFAULT_RATE_PER_MIN", 400),
			TokenBudget:  envInt64("DEFAULT_TOKEN_BUDGET", -1),
			BudgetPeriod: envStr("DEFAULT_BUDGET_PERIOD", periodLifetime),
			Priority:     envInt("DEFAULT_PRIORITY", 0),
		},
		Queue: Queue{
			// One slot per Ollama backend in the reference topology.
			MaxConcurrent: envInt("UPSTREAM_MAX_CONCURRENT", 10),
			PromoteAfter:  envDuration("QUEUE_PROMOTE_AFTER", 5*time.Second),
		},
		Dashboard: Dashboard{
			User: envStr("DASHBOARD_USER", "admin"),
			// No default: an operator UI must never be reachable because someone
			// forgot to set a password.
			Password: os.Getenv("DASHBOARD_PASSWORD"),
		},
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate rejects configurations that would make the gateway behave nonsensibly
// (a zero batch size would reject every request; an unparseable upstream would
// fail every forward). Catching these at startup turns a class of silent
// misconfigurations into a loud, immediate error.
func (c Config) validate() error {
	if c.Defaults.BatchMax < 1 {
		return fmt.Errorf("config: DEFAULT_BATCH_MAX must be >= 1, got %d", c.Defaults.BatchMax)
	}
	if c.Defaults.RatePerMin < 1 {
		return fmt.Errorf("config: DEFAULT_RATE_PER_MIN must be >= 1, got %d", c.Defaults.RatePerMin)
	}
	// -1 (unlimited) or any positive prepaid allowance is valid; 0 or below -1
	// is not, since it would block every request or make no sense.
	if c.Defaults.TokenBudget < -1 || c.Defaults.TokenBudget == 0 {
		return fmt.Errorf("config: DEFAULT_TOKEN_BUDGET must be -1 (unlimited) or > 0, got %d", c.Defaults.TokenBudget)
	}
	if c.Defaults.BudgetPeriod != periodMonthly && c.Defaults.BudgetPeriod != periodLifetime {
		return fmt.Errorf("config: DEFAULT_BUDGET_PERIOD must be %q or %q, got %q", periodMonthly, periodLifetime, c.Defaults.BudgetPeriod)
	}
	if c.Defaults.Priority < 0 || c.Defaults.Priority > maxPriority {
		return fmt.Errorf("config: DEFAULT_PRIORITY must be between 0 and %d, got %d", maxPriority, c.Defaults.Priority)
	}
	// A queue of zero would admit nothing at all, and a non-positive promotion
	// window would promote every waiter immediately, collapsing priority into
	// plain FIFO. Both are silent behaviour changes, so they fail at startup.
	if c.Queue.MaxConcurrent < 1 {
		return fmt.Errorf("config: UPSTREAM_MAX_CONCURRENT must be >= 1, got %d", c.Queue.MaxConcurrent)
	}
	if c.Queue.PromoteAfter <= 0 {
		return fmt.Errorf("config: QUEUE_PROMOTE_AFTER must be > 0, got %s", c.Queue.PromoteAfter)
	}
	// An unset DASHBOARD_USER falls back to "admin", but a blank one ("  ") would
	// stick: half a credential is a misconfiguration, not a weaker setting.
	if c.Dashboard.Enabled() && strings.TrimSpace(c.Dashboard.User) == "" {
		return errors.New("config: DASHBOARD_USER must not be blank when DASHBOARD_PASSWORD is set")
	}
	u, err := url.Parse(c.CaddyUpstreamURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("config: CADDY_UPSTREAM_URL must be an absolute URL, got %q", c.CaddyUpstreamURL)
	}
	return nil
}

// envStr returns the environment value for key, or def when unset/empty.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt returns the integer environment value for key, or def when unset or
// unparseable. An unparseable override falling back to the default is
// intentional: a typo shouldn't crash the process, and validate() still guards
// the resulting value's range.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envDuration is envInt for a Go duration string ("5s", "1500ms"). Like the
// other env helpers, an unparseable override falls back to the default rather
// than crashing the process; validate() still guards the resulting value.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// envInt64 is envInt for the 64-bit token budget (values can exceed an int on
// 32-bit builds, e.g. a multi-hundred-million-token prepaid allowance).
func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
