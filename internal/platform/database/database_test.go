package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// newTestDB opens a fresh, migrated database in a temp file. A file (not
// :memory:) is used deliberately: an in-memory SQLite is per-connection, and
// GORM's pool can hand goose and the repos different connections.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	require.NoError(t, Migrate(db))
	return db
}

func objectExists(t *testing.T, db *gorm.DB, typ, name string) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", typ, name,
	).Scan(&n).Error)
	return n > 0
}

func TestMigrateCreatesSchema(t *testing.T) {
	db := newTestDB(t)

	require.True(t, objectExists(t, db, "table", "api_keys"))
	require.True(t, objectExists(t, db, "table", "usage_events"))
	require.True(t, objectExists(t, db, "index", "idx_api_keys_key_hash"))
	require.True(t, objectExists(t, db, "index", "idx_usage_key_time"))

	// Migrate is idempotent: re-running against an up-to-date DB is a no-op.
	require.NoError(t, Migrate(db))
}

func TestAPIKeyRepoRoundTrip(t *testing.T) {
	db := newTestDB(t)
	repo := NewAPIKeyRepo(db)
	ctx := context.Background()

	plaintext, err := apikey.GenerateKey()
	require.NoError(t, err)
	k := &apikey.APIKey{
		Name:         "acme",
		KeyHash:      apikey.HashKey(plaintext),
		BatchMax:     25,
		RatePerMin:   400,
		TokenBudget:  100_000_000,
		BudgetPeriod: apikey.Monthly,
	}
	require.NoError(t, repo.Create(ctx, k))
	require.NotZero(t, k.ID, "Create should populate the autoincrement id")

	got, err := repo.ByHash(ctx, apikey.HashKey(plaintext))
	require.NoError(t, err)
	require.Equal(t, k.ID, got.ID)
	require.Equal(t, apikey.Monthly, got.BudgetPeriod)
	require.Equal(t, int64(100_000_000), got.TokenBudget)
	require.False(t, got.IsRevoked())

	// Unknown hash maps to the domain sentinel, not a GORM error.
	_, err = repo.ByHash(ctx, apikey.HashKey("sk-rag-nope"))
	require.ErrorIs(t, err, apikey.ErrNotFound)

	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, repo.Revoke(ctx, k.ID))
	revoked, err := repo.ByHash(ctx, apikey.HashKey(plaintext))
	require.NoError(t, err)
	require.True(t, revoked.IsRevoked())
}

// TestAPIKeyRepoUpdateLimits covers retuning an already-issued key, which is how
// an operator raises a cap or promotes a credential without reissuing it.
func TestAPIKeyRepoUpdateLimits(t *testing.T) {
	db := newTestDB(t)
	repo := NewAPIKeyRepo(db)
	ctx := context.Background()

	plaintext, err := apikey.GenerateKey()
	require.NoError(t, err)
	k := &apikey.APIKey{
		Name:         "main-site",
		KeyHash:      apikey.HashKey(plaintext),
		BatchMax:     25,
		RatePerMin:   400,
		TokenBudget:  apikey.Unlimited,
		BudgetPeriod: apikey.Lifetime,
	}
	require.NoError(t, repo.Create(ctx, k))

	// A key issued without an explicit rank lands on the free tier.
	stored, err := repo.ByID(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, apikey.NormalPriority, stored.Priority)

	require.NoError(t, repo.UpdateLimits(ctx, k.ID, apikey.Limits{
		BatchMax:     50,
		RatePerMin:   1000,
		TokenBudget:  250_000_000,
		BudgetPeriod: apikey.Monthly,
		Priority:     apikey.MaxPriority,
	}))

	updated, err := repo.ByHash(ctx, apikey.HashKey(plaintext))
	require.NoError(t, err)
	require.Equal(t, 50, updated.BatchMax)
	require.Equal(t, 1000, updated.RatePerMin)
	require.Equal(t, int64(250_000_000), updated.TokenBudget)
	require.Equal(t, apikey.Monthly, updated.BudgetPeriod)
	require.Equal(t, apikey.MaxPriority, updated.Priority)
	// The identity and the audit trail are untouched by a limits change.
	require.Equal(t, "main-site", updated.Name)
	require.Equal(t, apikey.HashKey(plaintext), updated.KeyHash)

	// An unknown id is loud: silently discarding a limit change would leave an
	// operator believing they raised a cap that never moved.
	require.ErrorIs(t, repo.UpdateLimits(ctx, 4242, updated.Limits()), apikey.ErrNotFound)
}

// TestUsageRepoSumTokens exercises the time-bucketed accounting the dashboard
// relies on. Events are stamped at fixed times and summed over the reporting
// windows for a fixed "now".
func TestUsageRepoSumTokens(t *testing.T) {
	db := newTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	const keyID = 1
	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 9, 0, 0, 0, time.UTC)
	}
	events := []*usage.Event{
		{APIKeyID: keyID, Model: "bge-m3", PromptTokens: 100, BatchSize: 3, CreatedAt: at(2026, time.July, 15)}, // today
		{APIKeyID: keyID, Model: "bge-m3", PromptTokens: 50, BatchSize: 1, CreatedAt: at(2026, time.July, 10)},  // this month, before this week
		{APIKeyID: keyID, Model: "bge-m3", PromptTokens: 30, BatchSize: 2, CreatedAt: at(2026, time.June, 15)},  // past month
		{APIKeyID: keyID, Model: "bge-m3", PromptTokens: 20, BatchSize: 1, CreatedAt: at(2026, time.May, 15)},   // before
		{APIKeyID: 999, Model: "bge-m3", PromptTokens: 777, BatchSize: 1, CreatedAt: at(2026, time.July, 15)},   // other key, must not leak
	}
	for _, e := range events {
		require.NoError(t, repo.Record(ctx, e))
	}

	now := time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC)
	w := usage.ReportWindows(now)
	sum := func(r usage.Range) int64 {
		total, err := repo.SumTokens(ctx, keyID, r.From, r.To)
		require.NoError(t, err)
		return total
	}

	require.Equal(t, int64(100), sum(w.Today))     // only the 07-15 event
	require.Equal(t, int64(100), sum(w.ThisWeek))  // 07-10 is before Monday 07-13
	require.Equal(t, int64(150), sum(w.ThisMonth)) // 100 + 50
	require.Equal(t, int64(30), sum(w.PastMonth))  // June
	require.Equal(t, int64(20), sum(w.Before))     // May and earlier

	// A key with no events sums to zero, not an error.
	empty, err := repo.SumTokens(ctx, 42, w.Before.From, now)
	require.NoError(t, err)
	require.Zero(t, empty)
}
