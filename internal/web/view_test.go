package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
)

// TestBuildQueueStates pins the pressure strip's states, since each one renders
// a different thing on screen and only the view model decides which: an idle
// pool, a busy-but-unqueued pool, a saturated pool with a queue, and a queue old
// enough that promotion has kicked in.
func TestBuildQueueStates(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		vm := buildQueue(queue.Stats{Capacity: 4, PromoteAfter: 5 * time.Second})

		require.True(t, vm.Idle)
		require.False(t, vm.Saturated)
		require.False(t, vm.Waiting)
		require.Equal(t, "0/4", vm.InFlightLabel)
		require.Len(t, vm.Slots, 4)
		require.NotContains(t, vm.Slots, true)
	})

	t.Run("busy without a queue", func(t *testing.T) {
		vm := buildQueue(queue.Stats{Capacity: 4, InFlight: 2, PromoteAfter: 5 * time.Second})

		require.False(t, vm.Idle)
		require.False(t, vm.Saturated, "spare capacity is not saturation")
		require.False(t, vm.Waiting)
		require.Equal(t, []bool{true, true, false, false}, vm.Slots)
	})

	t.Run("saturated with a queue", func(t *testing.T) {
		vm := buildQueue(queue.Stats{
			Capacity: 2, InFlight: 2, Waiting: 9,
			PromoteAfter: 5 * time.Second,
			OldestWait:   2500 * time.Millisecond,
			Classes: []queue.ClassStat{
				{Priority: 9, Waiting: 1, OldestWait: 120 * time.Millisecond},
				{Priority: 0, Waiting: 8, OldestWait: 2500 * time.Millisecond},
			},
			Admitted: 1234, Promoted: 7,
		})

		require.True(t, vm.Saturated)
		require.True(t, vm.Waiting)
		require.False(t, vm.Aged, "nobody has waited past the promotion window yet")
		require.Equal(t, "9 queued", vm.WaitingLabel)
		require.Equal(t, "2.5s", vm.OldestLabel)
		require.Equal(t, "1,234", vm.AdmittedLabel)

		require.Len(t, vm.Classes, 2)
		// Highest rank first, and only it carries the accent.
		require.Equal(t, "p9", vm.Classes[0].Label)
		require.True(t, vm.Classes[0].Top)
		require.Equal(t, "120ms", vm.Classes[0].WaitLabel)
		require.False(t, vm.Classes[1].Top)
		// Depth bars scale against the deepest class.
		require.Equal(t, "100%", vm.Classes[1].Width)
	})

	t.Run("aged past the promotion window", func(t *testing.T) {
		vm := buildQueue(queue.Stats{
			Capacity: 2, InFlight: 2, Waiting: 3,
			PromoteAfter: 5 * time.Second,
			OldestWait:   6 * time.Second,
			Classes: []queue.ClassStat{
				{Priority: 9, Waiting: 1, OldestWait: time.Second},
				{Priority: 0, Waiting: 2, OldestWait: 6 * time.Second},
			},
		})

		require.True(t, vm.Aged, "the strip must show that promotion is in play")
		require.Equal(t, "5.0s", vm.PromoteLabel)
		require.False(t, vm.Classes[0].Aged, "the fresh priority class is not being promoted")
		require.True(t, vm.Classes[1].Aged)
	})
}

// TestDashboardServesItsStylesheetWhenMounted is the regression test for a bug
// the browser caught and the router tests did not: chi's Mount routes on its own
// RoutePath and leaves r.URL.Path alone, so once the dashboard moved to /admin a
// bare StripPrefix("/assets/") stopped matching and the stylesheet 404'd — the
// whole UI rendered unstyled while every status-code assertion still passed.
func TestDashboardServesItsStylesheetWhenMounted(t *testing.T) {
	srv := NewServer(nil, nil, nil, nil, "bge-m3", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Mount exactly as the gateway does.
	root := chi.NewRouter()
	root.Mount(BasePath, srv.Handler())

	r := httptest.NewRequest(http.MethodGet, BasePath+"/assets/dashboard.css", nil)
	w := httptest.NewRecorder()
	root.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	// A browser refuses a stylesheet served as text/plain, so the type matters
	// as much as the status.
	require.Contains(t, w.Header().Get("Content-Type"), "text/css")
	require.Contains(t, w.Body.String(), "--amber")
}

// TestLandingServesItsStylesheet covers the same ground for the public page,
// which is mounted at the site root rather than under a prefix.
func TestLandingServesItsStylesheet(t *testing.T) {
	h := NewLanding("bge-m3", 25, 100, NewPlan(50, 21), NewContact("info@ituoga.lt", "+37063594444", "https://letas.lt"), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	r := httptest.NewRequest(http.MethodGet, "/assets/landing.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "text/css")
}

// TestNewContactFormatsTheNumberForBothUses: a tel: link needs unpunctuated
// digits, a reader needs grouping, and the company link needs a bare host.
func TestNewContactFormatsTheNumberForBothUses(t *testing.T) {
	c := NewContact("info@ituoga.lt", "+37063594444", "https://letas.lt")

	require.Equal(t, "+37063594444", c.Phone, "the dialable form must stay unpunctuated")
	require.Equal(t, "+370 635 94444", c.PhoneLabel)
	require.Equal(t, "letas.lt", c.CompanyName)
	require.Equal(t, "https://letas.lt", c.CompanyURL)
}

// TestGroupPhoneLeavesForeignNumbersAlone: guessing at the grouping of a number
// shape we do not know produces something wrong, which is worse than plain.
func TestGroupPhoneLeavesForeignNumbersAlone(t *testing.T) {
	for _, n := range []string{"+442071838750", "+37052345678", "12345", ""} {
		require.Equal(t, n, groupPhone(n))
	}
}
