// Package budget enforces a key's prepaid token allowance. It joins the pure
// domain rule (apikey.BudgetExhausted) to the recorded usage, deciding — before
// a request is forwarded — whether the key still has tokens to spend.
//
// The check is pre-flight and the authoritative bge-m3 count only arrives with
// the upstream response, so a key can exceed its allowance by the tokens of the
// requests already in flight when the budget is crossed. That bounded overshoot
// is an accepted tradeoff for a soft prepaid budget (see apikey.BudgetExhausted).
package budget

import (
	"context"
	"time"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// TokenSummer sums the tokens a key has spent within a half-open time range. It
// is the single capability the checker needs from persistence; usage.Repository
// (and the GORM UsageRepo) satisfy it. Depending on this narrow interface rather
// than the full repository keeps the budget check easy to fake in tests.
type TokenSummer interface {
	SumTokens(ctx context.Context, apiKeyID uint, from, to time.Time) (int64, error)
}

// Status is the result of a budget check.
type Status struct {
	// Exhausted is true when the key has no allowance left for its period.
	Exhausted bool
	// Remaining is the tokens left against the budget, or apikey.Unlimited (-1)
	// for an unlimited key. Handy for an informational response header.
	Remaining int64
}

// Checker evaluates budgets against recorded usage.
type Checker struct {
	sum   TokenSummer
	clock func() time.Time
}

// NewChecker returns a Checker backed by sum. The clock is injectable so
// month-boundary behaviour is deterministically testable.
func NewChecker(sum TokenSummer, clock func() time.Time) *Checker {
	return &Checker{sum: sum, clock: clock}
}

// Status reports whether k may spend more tokens. An unlimited key short-circuits
// with no database query. A limited key is summed only over the window its
// period cares about — month-to-date for Monthly, all-time for Lifetime — so the
// check costs exactly one aggregate query.
func (c *Checker) Status(ctx context.Context, k apikey.APIKey) (Status, error) {
	if k.IsUnlimited() {
		return Status{Exhausted: false, Remaining: apikey.Unlimited}, nil
	}

	now := c.clock()
	var usedThisMonth, usedLifetime int64

	if k.BudgetPeriod == apikey.Monthly {
		// Sum from the first of the current month up to now.
		from := usage.ReportWindows(now).ThisMonth.From
		used, err := c.sum.SumTokens(ctx, k.ID, from, now)
		if err != nil {
			return Status{}, err
		}
		usedThisMonth = used
	} else {
		// Lifetime: sum every event (zero time is before all stored events).
		used, err := c.sum.SumTokens(ctx, k.ID, time.Time{}, now)
		if err != nil {
			return Status{}, err
		}
		usedLifetime = used
	}

	return Status{
		Exhausted: k.BudgetExhausted(usedThisMonth, usedLifetime),
		Remaining: k.Remaining(usedThisMonth, usedLifetime),
	}, nil
}
