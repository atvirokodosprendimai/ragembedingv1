package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// envKeys is every variable Load reads. clearEnv sets each to "" (which the
// env* helpers treat as unset) via t.Setenv, so the surrounding test process's
// environment can't leak into a test and the values are auto-restored after.
var envKeys = []string{
	"LISTEN_ADDR", "DB_PATH", "CADDY_UPSTREAM_URL", "EMBED_MODEL",
	"DEFAULT_BATCH_MAX", "DEFAULT_RATE_PER_MIN", "DEFAULT_TOKEN_BUDGET", "DEFAULT_BUDGET_PERIOD",
	"DEFAULT_PRIORITY", "UPSTREAM_MAX_CONCURRENT", "QUEUE_PROMOTE_AFTER",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
}

// TestLoadDefaults pins the documented fallbacks so a later "harmless" tweak to
// a default is a conscious, reviewed change rather than a silent one.
func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.ListenAddr)
	require.Equal(t, "ragembed.db", cfg.DBPath)
	require.Equal(t, "http://localhost:11435", cfg.CaddyUpstreamURL)
	require.Equal(t, "bge-m3", cfg.EmbedModel)
	require.Equal(t, 25, cfg.Defaults.BatchMax)
	require.Equal(t, 400, cfg.Defaults.RatePerMin)
	require.Equal(t, int64(-1), cfg.Defaults.TokenBudget)
	require.Equal(t, "lifetime", cfg.Defaults.BudgetPeriod)
	require.Equal(t, 0, cfg.Defaults.Priority)
	require.Equal(t, 10, cfg.Queue.MaxConcurrent)
	require.Equal(t, 5*time.Second, cfg.Queue.PromoteAfter)
}

// TestLoadEnvOverridesDefaults proves real env vars take precedence, including a
// 100M prepaid monthly budget — the exact prepaid/on-demand scenario the product
// calls for.
func TestLoadEnvOverridesDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("LISTEN_ADDR", ":9000")
	t.Setenv("CADDY_UPSTREAM_URL", "http://caddy:11435")
	t.Setenv("DEFAULT_BATCH_MAX", "10")
	t.Setenv("DEFAULT_RATE_PER_MIN", "1000")
	t.Setenv("DEFAULT_TOKEN_BUDGET", "100000000")
	t.Setenv("DEFAULT_BUDGET_PERIOD", "monthly")
	t.Setenv("DEFAULT_PRIORITY", "3")
	t.Setenv("UPSTREAM_MAX_CONCURRENT", "24")
	t.Setenv("QUEUE_PROMOTE_AFTER", "1500ms")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, ":9000", cfg.ListenAddr)
	require.Equal(t, "http://caddy:11435", cfg.CaddyUpstreamURL)
	require.Equal(t, 10, cfg.Defaults.BatchMax)
	require.Equal(t, 1000, cfg.Defaults.RatePerMin)
	require.Equal(t, int64(100000000), cfg.Defaults.TokenBudget)
	require.Equal(t, "monthly", cfg.Defaults.BudgetPeriod)
	require.Equal(t, 3, cfg.Defaults.Priority)
	require.Equal(t, 24, cfg.Queue.MaxConcurrent)
	require.Equal(t, 1500*time.Millisecond, cfg.Queue.PromoteAfter)
}

// TestLoadRejectsInvalid asserts the fail-fast guards: each case is a value that
// would otherwise make the gateway reject or mishandle every request.
func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"zero batch":         {"DEFAULT_BATCH_MAX": "0"},
		"zero rate":          {"DEFAULT_RATE_PER_MIN": "0"},
		"zero budget":        {"DEFAULT_TOKEN_BUDGET": "0"},
		"below -1 budget":    {"DEFAULT_TOKEN_BUDGET": "-5"},
		"unknown period":     {"DEFAULT_BUDGET_PERIOD": "weekly"},
		"non-absolute url":   {"CADDY_UPSTREAM_URL": "not-a-url"},
		"schemeless url":     {"CADDY_UPSTREAM_URL": "//caddy:11435"},
		"priority over max":  {"DEFAULT_PRIORITY": "10"},
		"negative priority":  {"DEFAULT_PRIORITY": "-1"},
		"zero concurrency":   {"UPSTREAM_MAX_CONCURRENT": "0"},
		"zero promote-after": {"QUEUE_PROMOTE_AFTER": "0s"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			_, err := Load()
			require.Error(t, err)
		})
	}
}

// TestLoadIgnoresUnparseableOverride documents that a typo in a numeric override
// falls back to the default instead of crashing the process.
func TestLoadIgnoresUnparseableOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEFAULT_BATCH_MAX", "twenty-five")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 25, cfg.Defaults.BatchMax)
}
