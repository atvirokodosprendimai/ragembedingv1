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

func TestValidPriority(t *testing.T) {
	cases := []struct {
		p    int
		want bool
	}{
		{p: NormalPriority, want: true}, // the default every key is issued with
		{p: 5, want: true},              // an intermediate tier
		{p: MaxPriority, want: true},    // the main site's rank
		{p: MaxPriority + 1, want: false},
		{p: -1, want: false}, // negative ranks would sort below the default
	}
	for _, c := range cases {
		require.Equalf(t, c.want, ValidPriority(c.p), "ValidPriority(%d)", c.p)
	}
}

// TestLimitsValidate pins every bound an operator can get wrong from the CLI.
// Each invalid case here would otherwise produce a key that rejects every
// request, with a message blaming the client rather than the configuration.
func TestLimitsValidate(t *testing.T) {
	sound := Limits{BatchMax: 25, RatePerMin: 400, TokenBudget: Unlimited, BudgetPeriod: Lifetime, Priority: 0}
	require.NoError(t, sound.Validate())

	prepaid := sound
	prepaid.TokenBudget = 100_000_000
	prepaid.BudgetPeriod = Monthly
	require.NoError(t, prepaid.Validate())

	cases := map[string]func(*Limits){
		"zero batch":        func(l *Limits) { l.BatchMax = 0 },
		"negative batch":    func(l *Limits) { l.BatchMax = -1 },
		"zero rate":         func(l *Limits) { l.RatePerMin = 0 },
		"zero budget":       func(l *Limits) { l.TokenBudget = 0 },
		"budget below -1":   func(l *Limits) { l.TokenBudget = -5 },
		"unknown period":    func(l *Limits) { l.BudgetPeriod = "weekly" },
		"empty period":      func(l *Limits) { l.BudgetPeriod = "" },
		"priority over max": func(l *Limits) { l.Priority = MaxPriority + 1 },
		"negative priority": func(l *Limits) { l.Priority = -1 },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			l := sound
			break_(&l)
			require.Error(t, l.Validate())
		})
	}
}

// TestApplyLimitsLeavesTheKeyUntouchedOnError: a rejected change must not
// half-apply, or a typo silently rewrites the limits it did reach.
func TestApplyLimitsLeavesTheKeyUntouchedOnError(t *testing.T) {
	k := APIKey{BatchMax: 25, RatePerMin: 400, TokenBudget: Unlimited, BudgetPeriod: Lifetime, Priority: 9}
	before := k.Limits()

	err := k.ApplyLimits(Limits{BatchMax: 50, RatePerMin: 0, TokenBudget: Unlimited, BudgetPeriod: Lifetime})
	require.Error(t, err)
	require.Equal(t, before, k.Limits(), "a rejected change must leave the key exactly as it was")

	require.NoError(t, k.ApplyLimits(Limits{
		BatchMax: 50, RatePerMin: 1000, TokenBudget: 5_000_000, BudgetPeriod: Monthly, Priority: 3,
	}))
	require.Equal(t, 50, k.BatchMax)
	require.Equal(t, 1000, k.RatePerMin)
	require.Equal(t, int64(5_000_000), k.TokenBudget)
	require.Equal(t, Monthly, k.BudgetPeriod)
	require.Equal(t, 3, k.Priority)
}
