package usage

import (
	"context"
	"time"
)

// Event is one recorded, successful embedding call: it says how many bge-m3
// input tokens a key spent and when. It is written only after the upstream
// returns usage.prompt_tokens, so every Event carries an authoritative count.
// It doubles as the GORM model.
type Event struct {
	ID           uint      `gorm:"primaryKey"`
	APIKeyID     uint      `gorm:"column:api_key_id;index:idx_usage_key_time,priority:1"`
	Model        string    `gorm:"column:model"`
	PromptTokens int64     `gorm:"column:prompt_tokens"`
	BatchSize    int       `gorm:"column:batch_size"`
	CreatedAt    time.Time `gorm:"column:created_at;index:idx_usage_key_time,priority:2"`
}

// TableName pins the table name to match the goose migration.
func (Event) TableName() string { return "usage_events" }

// Range is a half-open time interval [From, To): From inclusive, To exclusive.
type Range struct {
	From time.Time
	To   time.Time
}

// Windows are the five reporting buckets the dashboard shows. The first three
// are cumulative and nested (each includes the ones before it) so an operator
// reads "spent today / this week / this month" at a glance; the last two are
// disjoint historical periods.
type Windows struct {
	// Today is midnight-to-now in the reference location.
	Today Range
	// ThisWeek is from the most recent Monday 00:00 to now.
	ThisWeek Range
	// ThisMonth is from the 1st of the current month 00:00 to now.
	ThisMonth Range
	// PastMonth is the entire previous calendar month.
	PastMonth Range
	// Before is everything earlier than the previous calendar month.
	Before Range
}

// ReportWindows computes the five buckets relative to now, in now's location.
// It is pure time math (no database, no wall clock) so boundary behaviour —
// week start, month rollover, previous-month spans — is deterministically
// testable.
func ReportWindows(now time.Time) Windows {
	loc := now.Location()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	// Week starts Monday. Go's Weekday has Sunday=0..Saturday=6; shifting by 6
	// and taking mod 7 maps Monday->0, Sunday->6, i.e. days to step back.
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	startOfWeek := startOfDay.AddDate(0, 0, -daysSinceMonday)

	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	startOfPrevMonth := startOfMonth.AddDate(0, -1, 0)

	return Windows{
		Today:     Range{From: startOfDay, To: now},
		ThisWeek:  Range{From: startOfWeek, To: now},
		ThisMonth: Range{From: startOfMonth, To: now},
		PastMonth: Range{From: startOfPrevMonth, To: startOfMonth},
		// The zero From means "since the beginning of time"; all stored events
		// are more recent, so summing from it captures the full history.
		Before: Range{From: time.Time{}, To: startOfPrevMonth},
	}
}

// Report is the token total per bucket, ready for the dashboard view model.
type Report struct {
	Today     int64
	ThisWeek  int64
	ThisMonth int64
	PastMonth int64
	Before    int64
}

// Summer sums the prompt tokens recorded within a half-open range. It is the one
// seam BuildReport needs, letting the report be assembled from either the real
// repository or a fake in tests.
type Summer func(r Range) (int64, error)

// BuildReport fills every bucket by summing over its window. It stops at the
// first error so a failing query surfaces rather than reporting silent zeros.
func BuildReport(w Windows, sum Summer) (Report, error) {
	var (
		rep Report
		err error
	)
	if rep.Today, err = sum(w.Today); err != nil {
		return Report{}, err
	}
	if rep.ThisWeek, err = sum(w.ThisWeek); err != nil {
		return Report{}, err
	}
	if rep.ThisMonth, err = sum(w.ThisMonth); err != nil {
		return Report{}, err
	}
	if rep.PastMonth, err = sum(w.PastMonth); err != nil {
		return Report{}, err
	}
	if rep.Before, err = sum(w.Before); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// Repository is the persistence port for usage. The GORM implementation lives in
// the platform layer; consumers depend on this interface.
type Repository interface {
	// Record persists one usage event.
	Record(ctx context.Context, e *Event) error
	// SumTokens returns the total prompt tokens for a key within [from, to).
	SumTokens(ctx context.Context, apiKeyID uint, from, to time.Time) (int64, error)
}
