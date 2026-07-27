package web

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// intStr and uintStr are tiny helpers so the templates can print numeric fields
// without embedding strconv calls in the markup.
func intStr(n int) string   { return strconv.Itoa(n) }
func uintStr(n uint) string { return strconv.FormatUint(uint64(n), 10) }

// The *VM types are the dashboard's view model: the backend assembles them from
// the repositories and templ renders them. Every presentation string (labels,
// bar widths, datastar expressions) is precomputed here so the templates stay a
// pure, logic-free projection — the datastar way of building UI as a struct.

// PageVM is the whole dashboard page.
type PageVM struct {
	Model            string        // embedding model badge (bge-m3)
	TotalKeys        int           // count for the masthead
	TokensTodayLabel string        // aggregate tokens today, humanized
	SignalsInit      string        // data-signals JSON, e.g. {selectedKey: 3}
	HasKeys          bool          // false => render the empty state
	Keys             []KeyListItem // sidebar rows
	Detail           *DetailVM     // initially-selected key detail (nil if none)
}

// KeyListItem is one row in the key sidebar.
type KeyListItem struct {
	ID          uint
	Label       string // name, or "key #id" when unnamed
	StatusClass string // "active" | "revoked" (drives the status dot colour)
	Revoked     bool
	TodayLabel  string // tokens today, humanized
	Width       string // today bar width, e.g. "42%"
	OnClick     string // datastar expression: select + fetch detail
	ActiveExpr  string // datastar expression: is this row selected
}

// DetailVM is the selected key's detail panel. Its templ root carries id="detail"
// so datastar can morph it in place on selection or refresh.
type DetailVM struct {
	ID          uint
	Label       string
	Revoked     bool
	Batch       string
	Rate        string
	BudgetLabel string     // "unlimited" or humanized allowance
	Period      string     // monthly | lifetime
	Buckets     []BucketVM // the five reporting buckets
	RefreshExpr string     // datastar expression to re-fetch this detail

	HasBudget            bool   // true for prepaid keys (draws the meter)
	Over                 bool   // budget exhausted (meter turns red)
	BudgetUsedLabel      string // tokens used in the budgeted period
	BudgetRemainingLabel string // tokens remaining
	BudgetWidth          string // meter fill width, e.g. "73%"
}

// BucketVM is one reporting bucket (today, this week, ...).
type BucketVM struct {
	Label       string // "today", "this week", ...
	TokensLabel string // humanized token count
	Width       string // bar width relative to the largest bucket
	Emphasis    bool   // today is emphasised as the live figure
}

// buildPage assembles the full page: every key with its today total, the
// aggregate, and the newest key's detail pre-selected.
func (s *Server) buildPage(ctx context.Context) (PageVM, error) {
	keys, err := s.keys.List(ctx)
	if err != nil {
		return PageVM{}, err
	}
	now := s.now()
	windows := usage.ReportWindows(now)

	// Gather each key's today total in one pass so we can also derive the max
	// (for bar scaling) and the aggregate.
	todays := make([]int64, len(keys))
	var maxToday, totalToday int64
	for i, k := range keys {
		today, err := s.usage.SumTokens(ctx, k.ID, windows.Today.From, now)
		if err != nil {
			return PageVM{}, err
		}
		todays[i] = today
		maxToday = max(maxToday, today)
		totalToday += today
	}

	vm := PageVM{
		Model:            s.model,
		TotalKeys:        len(keys),
		TokensTodayLabel: humanize.Comma(totalToday),
		HasKeys:          len(keys) > 0,
		SignalsInit:      "{selectedKey: 0}",
	}
	for i, k := range keys {
		vm.Keys = append(vm.Keys, KeyListItem{
			ID:          k.ID,
			Label:       keyLabel(k),
			StatusClass: statusClass(k),
			Revoked:     k.IsRevoked(),
			TodayLabel:  humanize.Comma(todays[i]),
			Width:       barWidth(todays[i], maxToday),
			// selectedKey has no underscore so it round-trips to the backend; the
			// @get patches #detail with the chosen key.
			OnClick:    fmt.Sprintf("$selectedKey = %d; @get('/keys/%d')", k.ID, k.ID),
			ActiveExpr: fmt.Sprintf("$selectedKey === %d", k.ID),
		})
	}
	if len(keys) > 0 {
		// Default to the first active key so the landing view shows a live key
		// rather than a revoked one; fall back to the newest if all are revoked.
		def := 0
		for i, k := range keys {
			if !k.IsRevoked() {
				def = i
				break
			}
		}
		detail, err := s.buildDetail(ctx, keys[def], now)
		if err != nil {
			return PageVM{}, err
		}
		vm.Detail = &detail
		vm.SignalsInit = fmt.Sprintf("{selectedKey: %d}", keys[def].ID)
	}
	return vm, nil
}

// buildDetail computes the five buckets and, for prepaid keys, the budget meter
// for one key. Lifetime and monthly usage are derived from the disjoint buckets
// (this month + past month + before = all time), so no extra query is needed.
func (s *Server) buildDetail(ctx context.Context, k apikey.APIKey, now time.Time) (DetailVM, error) {
	w := usage.ReportWindows(now)
	rep, err := usage.BuildReport(w, func(r usage.Range) (int64, error) {
		return s.usage.SumTokens(ctx, k.ID, r.From, r.To)
	})
	if err != nil {
		return DetailVM{}, err
	}

	peak := max(rep.Today, rep.ThisWeek, rep.ThisMonth, rep.PastMonth, rep.Before)
	d := DetailVM{
		ID:          k.ID,
		Label:       keyLabel(k),
		Revoked:     k.IsRevoked(),
		Batch:       fmt.Sprintf("%d", k.BatchMax),
		Rate:        fmt.Sprintf("%d", k.RatePerMin),
		BudgetLabel: budgetLabel(k.TokenBudget),
		Period:      string(k.BudgetPeriod),
		RefreshExpr: fmt.Sprintf("@get('/keys/%d')", k.ID),
		Buckets: []BucketVM{
			{Label: "today", TokensLabel: humanize.Comma(rep.Today), Width: barWidth(rep.Today, peak), Emphasis: true},
			{Label: "this week", TokensLabel: humanize.Comma(rep.ThisWeek), Width: barWidth(rep.ThisWeek, peak)},
			{Label: "this month", TokensLabel: humanize.Comma(rep.ThisMonth), Width: barWidth(rep.ThisMonth, peak)},
			{Label: "past month", TokensLabel: humanize.Comma(rep.PastMonth), Width: barWidth(rep.PastMonth, peak)},
			{Label: "before", TokensLabel: humanize.Comma(rep.Before), Width: barWidth(rep.Before, peak)},
		},
	}

	if !k.IsUnlimited() {
		lifetimeUsed := rep.ThisMonth + rep.PastMonth + rep.Before
		used := lifetimeUsed
		if k.BudgetPeriod == apikey.Monthly {
			used = rep.ThisMonth
		}
		remaining := max(k.TokenBudget-used, 0)
		pct := int(min(used*100/k.TokenBudget, 100))

		d.HasBudget = true
		d.Over = used >= k.TokenBudget
		d.BudgetUsedLabel = humanize.Comma(used)
		d.BudgetRemainingLabel = humanize.Comma(remaining)
		d.BudgetWidth = fmt.Sprintf("%d%%", pct)
	}
	return d, nil
}

// keyLabel is the display name for a key, falling back to its id when unnamed.
func keyLabel(k apikey.APIKey) string {
	if k.Name != "" {
		return k.Name
	}
	return fmt.Sprintf("key #%d", k.ID)
}

func statusClass(k apikey.APIKey) string {
	if k.IsRevoked() {
		return "revoked"
	}
	return "active"
}

func budgetLabel(budget int64) string {
	if budget == apikey.Unlimited {
		return "unlimited"
	}
	return humanize.Comma(budget)
}

// barWidth renders value as a percentage of peak for a bar's inline width. A
// non-zero value never renders as a hairline: it gets at least 2% so the bar is
// visible, which reads more honestly than an invisible-but-nonzero bar.
func barWidth(value, peak int64) string {
	if peak <= 0 || value <= 0 {
		return "0%"
	}
	pct := int(value * 100 / peak)
	if pct < 2 {
		pct = 2
	}
	return fmt.Sprintf("%d%%", pct)
}
