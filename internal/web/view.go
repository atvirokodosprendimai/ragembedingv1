package web

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/live"
	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
)

// BasePath is where the operator dashboard is mounted. Every URL the dashboard
// generates — the datastar fetches and the stylesheet — is built from this, so
// the mount point and the links can never drift apart.
const BasePath = "/admin"

// datastarVersion pins the client bundle. It is assembled from parts rather than
// written as one literal because "…datastar@v1.0.2/…" matches an email address:
// Cloudflare's obfuscator rewrote exactly this URL to "[email protected]" in a
// previous edit, the script 400'd, and the dashboard silently lost every
// interaction with no error on screen.
const datastarVersion = "v1.0.2"

// datastarCDN is the client bundle URL. Keep it in sync with the datastar-go SDK
// version in go.mod (v1.2.x speaks the v1.0.x wire format).
var datastarCDN = "https://cdn.jsdelivr.net/gh/starfederation/datastar" + "@" + datastarVersion + "/bundles/datastar.js"

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
	HasKeys          bool          // false => render the empty state
	LiveURL          string        // the one SSE stream this page subscribes to
	Queue            QueueVM       // live admission-queue pressure strip
	Keys             []KeyListItem // sidebar rows
	Detail           *DetailVM     // the selected key's detail (nil if none)
}

// KeyListItem is one row in the key sidebar. Each row is a plain link: the
// dashboard is a multi-page app, so selecting a key is navigation — it has a
// URL, a history entry, and works with the back button and middle-click.
type KeyListItem struct {
	ID          uint
	Label       string // name, or "key #id" when unnamed
	Href        string // "/admin/keys/3"
	Current     bool   // the key this page is showing
	StatusClass string // "active" | "revoked" (drives the status dot colour)
	Revoked     bool
	Elevated    bool   // priority above the free tier => show the rank tag
	PrioLabel   string // "p9"
	TodayLabel  string // tokens today, humanized
	Width       string // today bar width, e.g. "42%"
}

// QueueVM is the live admission-queue strip: how much of the Ollama pool is
// busy, who is waiting for it, and whether anyone has waited long enough to be
// promoted. Its templ root carries id="queue" so datastar morphs it in place on
// each poll.
//
// The slot strip is drawn as one block per concurrent slot, sized to fill the
// row, so it reads at a glance for a pool-sized capacity (tens of slots); the
// numeric label always carries the exact figure.
type QueueVM struct {
	Capacity      int
	Slots         []bool // one entry per slot, true = busy
	InFlightLabel string // "7/10"
	Saturated     bool   // every slot busy => the strip goes hot
	Idle          bool   // nothing in flight and nothing queued => empty state

	Waiting      bool   // anything queued at all
	WaitingLabel string // "8 queued"
	Classes      []QueueClassVM
	OldestLabel  string // longest current wait, e.g. "3.2s"
	Aged         bool   // someone has waited past the promotion window
	PromoteLabel string // the configured window, e.g. "5s"

	AdmittedLabel string
	PromotedLabel string
}

// QueueClassVM is one priority class's share of the queue.
type QueueClassVM struct {
	Label      string // "p9"
	CountLabel string
	WaitLabel  string // that class's longest wait
	Width      string // depth bar width, relative to the deepest class
	Top        bool   // the highest-ranked class waiting => accent colour
	Aged       bool   // this class is past the promotion window
}

// DetailVM is the selected key's detail panel. Its templ root carries id="detail"
// so datastar can morph it in place on selection or refresh.
type DetailVM struct {
	ID          uint
	Label       string
	Revoked     bool
	Batch       string
	Rate        string
	Prio        string     // admission-queue rank, e.g. "p9"
	Elevated    bool       // above the free tier => the stat is accented
	BudgetLabel string     // "unlimited" or humanized allowance
	Period      string     // monthly | lifetime
	Buckets     []BucketVM // the five reporting buckets

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
func (s *Server) buildPage(ctx context.Context, selected uint) (PageVM, error) {
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
		Queue:            buildQueue(s.pool.Stats()),
		LiveURL:          fmt.Sprintf("%s/keys/%d/live", BasePath, selected),
	}

	found := -1
	for i, k := range keys {
		if k.ID == selected {
			found = i
		}
		vm.Keys = append(vm.Keys, KeyListItem{
			ID:          k.ID,
			Label:       keyLabel(k),
			Href:        fmt.Sprintf("%s/keys/%d", BasePath, k.ID),
			Current:     k.ID == selected,
			StatusClass: statusClass(k),
			Revoked:     k.IsRevoked(),
			Elevated:    k.Priority > apikey.NormalPriority,
			PrioLabel:   priorityLabel(k.Priority),
			TodayLabel:  humanize.Comma(todays[i]),
			Width:       barWidth(todays[i], maxToday),
		})
	}
	if found < 0 {
		// The URL names a key that does not exist. Surfacing the domain sentinel
		// lets the handler answer 404 rather than render a page about nothing.
		return PageVM{}, apikey.ErrNotFound
	}

	detail, err := s.buildDetail(ctx, keys[found], now)
	if err != nil {
		return PageVM{}, err
	}
	vm.Detail = &detail
	return vm, nil
}

// defaultKeyID picks the key the bare /admin URL should open: the first active
// one, so the operator lands on a key that is actually in use rather than a
// revoked one. It reports false when there are no keys at all.
func defaultKeyID(keys []apikey.APIKey) (uint, bool) {
	if len(keys) == 0 {
		return 0, false
	}
	for _, k := range keys {
		if !k.IsRevoked() {
			return k.ID, true
		}
	}
	return keys[0].ID, true
}

// buildQueue projects a queue snapshot into the pressure strip. All the
// presentation decisions — which class is the accent, what counts as "aged",
// how wide each bar is — are made here so the template stays logic-free.
func buildQueue(s queue.Stats) QueueVM {
	vm := QueueVM{
		Capacity:      s.Capacity,
		Slots:         make([]bool, s.Capacity),
		InFlightLabel: fmt.Sprintf("%d/%d", s.InFlight, s.Capacity),
		Saturated:     s.Capacity > 0 && s.InFlight >= s.Capacity,
		Idle:          s.InFlight == 0 && s.Waiting == 0,
		Waiting:       s.Waiting > 0,
		WaitingLabel:  fmt.Sprintf("%d queued", s.Waiting),
		OldestLabel:   shortDuration(s.OldestWait),
		Aged:          s.OldestWait >= s.PromoteAfter,
		PromoteLabel:  shortDuration(s.PromoteAfter),
		AdmittedLabel: humanize.Comma(int64(s.Admitted)),
		PromotedLabel: humanize.Comma(int64(s.Promoted)),
	}
	for i := range s.InFlight {
		vm.Slots[i] = true
	}

	// Scale the depth bars against the deepest class so the mix is readable
	// whether two or two hundred requests are queued.
	var deepest int
	for _, c := range s.Classes {
		deepest = max(deepest, c.Waiting)
	}
	for i, c := range s.Classes {
		vm.Classes = append(vm.Classes, QueueClassVM{
			Label:      priorityLabel(c.Priority),
			CountLabel: strconv.Itoa(c.Waiting),
			WaitLabel:  shortDuration(c.OldestWait),
			Width:      barWidth(int64(c.Waiting), int64(deepest)),
			// Stats sorts classes highest-first, so the first one is the best
			// rank currently waiting — the one an operator cares about most.
			Top:  i == 0,
			Aged: c.OldestWait >= s.PromoteAfter,
		})
	}
	return vm
}

// priorityLabel renders a queue rank the way the strip and the key rows refer to
// it ("p9"), short enough to sit inside a tag.
func priorityLabel(p int) string { return "p" + strconv.Itoa(p) }

// shortDuration renders a wait compactly: sub-second waits in milliseconds
// (where the interesting resolution is), longer ones as one decimal of a second.
// A queue that is moving should read "0ms", not "0s".
func shortDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// buildDetail reads a key's report from the store and projects it. It is the
// first-paint path: the page render, before the live stream takes over.
func (s *Server) buildDetail(ctx context.Context, k apikey.APIKey, now time.Time) (DetailVM, error) {
	w := usage.ReportWindows(now)
	rep, err := usage.BuildReport(w, func(r usage.Range) (int64, error) {
		return s.usage.SumTokens(ctx, k.ID, r.From, r.To)
	})
	if err != nil {
		return DetailVM{}, err
	}
	return detailVM(k, rep), nil
}

// detailFrom projects a live snapshot. It is the streaming path, and it shares
// detailVM with the first paint so a pushed update and a fresh page render can
// never disagree about how a number is displayed.
func (s *Server) detailFrom(k apikey.APIKey, snap live.Snapshot) DetailVM {
	return detailVM(k, snap.Report)
}

// detailVM turns a key and its report into the panel: the five buckets and, for
// prepaid keys, the budget meter. Lifetime and monthly usage are derived from
// the disjoint buckets (this month + past month + before = all time), so no
// extra query is needed. It touches neither the clock nor the database, which is
// what lets the live stream reuse it.
func detailVM(k apikey.APIKey, rep usage.Report) DetailVM {
	peak := max(rep.Today, rep.ThisWeek, rep.ThisMonth, rep.PastMonth, rep.Before)
	d := DetailVM{
		ID:          k.ID,
		Label:       keyLabel(k),
		Revoked:     k.IsRevoked(),
		Batch:       fmt.Sprintf("%d", k.BatchMax),
		Rate:        fmt.Sprintf("%d", k.RatePerMin),
		Prio:        priorityLabel(k.Priority),
		Elevated:    k.Priority > apikey.NormalPriority,
		BudgetLabel: budgetLabel(k.TokenBudget),
		Period:      string(k.BudgetPeriod),
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
	return d
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

// sidebarCtx is the bit of page state a live stream needs to keep the sidebar
// row and the masthead total in step with the panel it is pushing. It is read
// once when the stream opens: the figures for *other* keys do not move while an
// operator watches this one, and re-querying them on every push would put the
// database back in the hot path that the read side exists to keep it out of.
type sidebarCtx struct {
	othersToday int64 // today's tokens across every other key
	peakToday   int64 // largest today figure, for bar scaling
}

// sidebarContext reads the sidebar figures for keyID's stream.
func (s *Server) sidebarContext(ctx context.Context, keyID uint) (sidebarCtx, error) {
	keys, err := s.keys.List(ctx)
	if err != nil {
		return sidebarCtx{}, err
	}
	now := s.now()
	from := usage.ReportWindows(now).Today.From

	var sc sidebarCtx
	for _, k := range keys {
		today, err := s.usage.SumTokens(ctx, k.ID, from, now)
		if err != nil {
			return sidebarCtx{}, err
		}
		sc.peakToday = max(sc.peakToday, today)
		if k.ID != keyID {
			sc.othersToday += today
		}
	}
	return sc, nil
}

// row rebuilds the watched key's sidebar row around a fresh today figure. The
// row is always the current one — it is the key whose page this is.
func (c sidebarCtx) row(k apikey.APIKey, today int64) KeyListItem {
	return KeyListItem{
		ID:          k.ID,
		Label:       keyLabel(k),
		Href:        fmt.Sprintf("%s/keys/%d", BasePath, k.ID),
		Current:     true,
		StatusClass: statusClass(k),
		Revoked:     k.IsRevoked(),
		Elevated:    k.Priority > apikey.NormalPriority,
		PrioLabel:   priorityLabel(k.Priority),
		TodayLabel:  humanize.Comma(today),
		// A key that has just overtaken the previous peak scales against itself,
		// so its bar fills rather than overflowing.
		Width: barWidth(today, max(c.peakToday, today)),
	}
}

// totalLabel is the masthead aggregate with the watched key's fresh figure
// swapped in for its stale one.
func (c sidebarCtx) totalLabel(today int64) string {
	return humanize.Comma(c.othersToday + today)
}
