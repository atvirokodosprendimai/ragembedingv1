package apikey

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAllowsBatch(t *testing.T) {
	k := APIKey{BatchMax: 25}
	cases := []struct {
		n    int
		want bool
	}{
		{n: 0, want: false},  // empty request
		{n: 1, want: true},   // minimum
		{n: 25, want: true},  // exactly at the ceiling
		{n: 26, want: false}, // one over
		{n: -3, want: false}, // nonsense
	}
	for _, c := range cases {
		require.Equalf(t, c.want, k.AllowsBatch(c.n), "AllowsBatch(%d)", c.n)
	}
}

func TestBudgetExhausted(t *testing.T) {
	cases := []struct {
		name          string
		key           APIKey
		usedThisMonth int64
		usedLifetime  int64
		want          bool
	}{
		{
			name: "unlimited never exhausted",
			key:  APIKey{TokenBudget: Unlimited, BudgetPeriod: Lifetime},
			// even absurd usage does not exhaust an unlimited key
			usedThisMonth: 1 << 40, usedLifetime: 1 << 50,
			want: false,
		},
		{
			name:          "monthly under budget",
			key:           APIKey{TokenBudget: 100, BudgetPeriod: Monthly},
			usedThisMonth: 99, usedLifetime: 10_000,
			want: false, // lifetime is huge but monthly is what counts
		},
		{
			name:          "monthly at budget",
			key:           APIKey{TokenBudget: 100, BudgetPeriod: Monthly},
			usedThisMonth: 100, usedLifetime: 100,
			want: true, // >= budget exhausts
		},
		{
			name:          "lifetime under budget",
			key:           APIKey{TokenBudget: 100, BudgetPeriod: Lifetime},
			usedThisMonth: 100, usedLifetime: 99,
			want: false, // monthly is high but lifetime is what counts
		},
		{
			name:          "lifetime exhausted",
			key:           APIKey{TokenBudget: 100_000_000, BudgetPeriod: Lifetime},
			usedThisMonth: 0, usedLifetime: 100_000_000,
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, c.key.BudgetExhausted(c.usedThisMonth, c.usedLifetime))
		})
	}
}

func TestRemaining(t *testing.T) {
	// Unlimited signals -1 (no finite remaining).
	require.Equal(t, Unlimited, APIKey{TokenBudget: Unlimited}.Remaining(0, 0))

	// Monthly: budget minus month-to-date, clamped at zero.
	monthly := APIKey{TokenBudget: 100, BudgetPeriod: Monthly}
	require.Equal(t, int64(40), monthly.Remaining(60, 999))
	require.Equal(t, int64(0), monthly.Remaining(120, 0))

	// Lifetime: budget minus lifetime total.
	lifetime := APIKey{TokenBudget: 100, BudgetPeriod: Lifetime}
	require.Equal(t, int64(25), lifetime.Remaining(999, 75))
}

func TestIsRevoked(t *testing.T) {
	require.False(t, APIKey{}.IsRevoked())
	now := time.Now()
	require.True(t, APIKey{RevokedAt: &now}.IsRevoked())
}

func TestBudgetPeriodValid(t *testing.T) {
	require.True(t, Monthly.Valid())
	require.True(t, Lifetime.Valid())
	require.False(t, BudgetPeriod("weekly").Valid())
	require.False(t, BudgetPeriod("").Valid())
}

// TestGenerateAndHashKey checks the credential lifecycle: keys are prefixed,
// unique, and hash deterministically so ByHash lookups work while the plaintext
// stays unrecoverable from storage.
func TestGenerateAndHashKey(t *testing.T) {
	k1, err := GenerateKey()
	require.NoError(t, err)
	k2, err := GenerateKey()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(k1, keyPrefix))
	require.NotEqual(t, k1, k2, "generated keys must be unique")

	// Hashing is deterministic and not the identity function.
	require.Equal(t, HashKey(k1), HashKey(k1))
	require.NotEqual(t, HashKey(k1), HashKey(k2))
	require.NotContains(t, HashKey(k1), k1, "hash must not embed the plaintext")
	require.Len(t, HashKey(k1), 64) // hex-encoded SHA-256
}
