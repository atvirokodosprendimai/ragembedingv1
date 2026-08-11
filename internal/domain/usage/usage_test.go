package usage

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReportWindows pins the bucket boundaries against a known instant:
// Wednesday 2026-07-15 10:30 UTC.
func TestReportWindows(t *testing.T) {
	now := time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC)
	w := ReportWindows(now)

	// Today: midnight to now.
	require.Equal(t, time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC), w.Today.From)
	require.Equal(t, now, w.Today.To)

	// This week: the Monday of that week is 2026-07-13.
	require.Equal(t, time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC), w.ThisWeek.From)
	require.Equal(t, time.Monday, w.ThisWeek.From.Weekday())
	require.Equal(t, now, w.ThisWeek.To)

	// This month: the 1st to now.
	require.Equal(t, time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC), w.ThisMonth.From)
	require.Equal(t, 1, w.ThisMonth.From.Day())

	// Past month: the whole of June 2026.
	require.Equal(t, time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC), w.PastMonth.From)
	require.Equal(t, time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC), w.PastMonth.To)

	// Before: everything earlier than June 2026.
	require.True(t, w.Before.From.IsZero())
	require.Equal(t, w.PastMonth.From, w.Before.To)
}

// TestReportWindowsSundayIsSameWeek guards the Monday-start edge case: a Sunday
// must belong to the week that began the previous Monday, not start a new one.
func TestReportWindowsSundayIsSameWeek(t *testing.T) {
	sunday := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC) // Sunday
	require.Equal(t, time.Sunday, sunday.Weekday())
	w := ReportWindows(sunday)
	require.Equal(t, time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC), w.ThisWeek.From)
}

// TestReportWindowsJanuaryRollsToDecember guards the year boundary for the
// previous-month span.
func TestReportWindowsJanuaryRollsToDecember(t *testing.T) {
	jan := time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC)
	w := ReportWindows(jan)
	require.Equal(t, time.Date(2025, time.December, 1, 0, 0, 0, 0, time.UTC), w.PastMonth.From)
	require.Equal(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), w.PastMonth.To)
}

func TestBuildReport(t *testing.T) {
	now := time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC)
	w := ReportWindows(now)

	// Fake summer returns a distinct value per bucket keyed by From, proving each
	// window is summed independently and mapped to the right field.
	sum := func(r Range) (int64, error) {
		switch {
		case r.From.Equal(w.Today.From) && r.To.Equal(w.Today.To):
			return 10, nil
		case r.From.Equal(w.ThisWeek.From):
			return 20, nil
		case r.From.Equal(w.ThisMonth.From):
			return 30, nil
		case r.From.Equal(w.PastMonth.From):
			return 40, nil
		default: // Before
			return 50, nil
		}
	}

	rep, err := BuildReport(w, sum)
	require.NoError(t, err)
	require.Equal(t, Report{Today: 10, ThisWeek: 20, ThisMonth: 30, PastMonth: 40, Before: 50}, rep)
}

func TestBuildReportPropagatesError(t *testing.T) {
	w := ReportWindows(time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC))
	boom := errors.New("db down")
	_, err := BuildReport(w, func(Range) (int64, error) { return 0, boom })
	require.ErrorIs(t, err, boom)
}
