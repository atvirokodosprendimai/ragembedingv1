package budget

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
)

// fakeSummer distinguishes a monthly query from a lifetime query by its From:
// the lifetime query starts at the zero time.
type fakeSummer struct {
	month, lifetime int64
	err             error
	called          bool
}

func (f *fakeSummer) SumTokens(_ context.Context, _ uint, from, _ time.Time) (int64, error) {
	f.called = true
	if f.err != nil {
		return 0, f.err
	}
	if from.IsZero() {
		return f.lifetime, nil
	}
	return f.month, nil
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

var now = time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC)

// TestUnlimitedSkipsQuery: an unlimited key is never exhausted and must not even
// touch the summer (a DB round-trip for a key with no cap is wasted work).
func TestUnlimitedSkipsQuery(t *testing.T) {
	sum := &fakeSummer{}
	c := NewChecker(sum, fixedClock(now))

	st, err := c.Status(context.Background(), apikey.APIKey{TokenBudget: apikey.Unlimited})
	require.NoError(t, err)
	require.False(t, st.Exhausted)
	require.Equal(t, apikey.Unlimited, st.Remaining)
	require.False(t, sum.called, "unlimited keys must not query usage")
}

func TestMonthlyBudget(t *testing.T) {
	cases := []struct {
		name          string
		month         int64
		wantExhausted bool
		wantRemaining int64
	}{
		{name: "under", month: 60, wantExhausted: false, wantRemaining: 40},
		{name: "at limit", month: 100, wantExhausted: true, wantRemaining: 0},
		{name: "over", month: 130, wantExhausted: true, wantRemaining: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sum := &fakeSummer{month: tc.month, lifetime: 999_999} // lifetime must be ignored
			c := NewChecker(sum, fixedClock(now))
			k := apikey.APIKey{ID: 1, TokenBudget: 100, BudgetPeriod: apikey.Monthly}

			st, err := c.Status(context.Background(), k)
			require.NoError(t, err)
			require.Equal(t, tc.wantExhausted, st.Exhausted)
			require.Equal(t, tc.wantRemaining, st.Remaining)
		})
	}
}

func TestLifetimeBudget(t *testing.T) {
	// month is huge but must be ignored for a lifetime key.
	sum := &fakeSummer{month: 999_999, lifetime: 100_000_000}
	c := NewChecker(sum, fixedClock(now))
	k := apikey.APIKey{ID: 1, TokenBudget: 100_000_000, BudgetPeriod: apikey.Lifetime}

	st, err := c.Status(context.Background(), k)
	require.NoError(t, err)
	require.True(t, st.Exhausted)
	require.Equal(t, int64(0), st.Remaining)
}

func TestErrorPropagates(t *testing.T) {
	boom := errors.New("db down")
	c := NewChecker(&fakeSummer{err: boom}, fixedClock(now))
	k := apikey.APIKey{ID: 1, TokenBudget: 100, BudgetPeriod: apikey.Lifetime}

	_, err := c.Status(context.Background(), k)
	require.ErrorIs(t, err, boom)
}
