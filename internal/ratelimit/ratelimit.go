package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Window is the length of a rate-limit period. The product speaks in
// requests-per-minute, so the window is one minute.
const Window = time.Minute

// Decision is the outcome of an Allow check. When Allowed is false, RetryAfter
// is the time the caller should wait before the current window resets.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// RetryAfterSeconds renders RetryAfter as an integer second count for the HTTP
// Retry-After header, rounding up so a client never retries a fraction of a
// second too early. A denied decision always reports at least 1 second.
func (d Decision) RetryAfterSeconds() int {
	if d.Allowed {
		return 0
	}
	secs := int(math.Ceil(d.RetryAfter.Seconds()))
	if secs < 1 {
		return 1
	}
	return secs
}

// window tracks how many requests a single key has made in its current period.
type window struct {
	start time.Time
	count int
}

// Limiter is a per-key fixed-window request counter. Each key gets its own
// window because limits differ per API key, and the window rolls from a key's
// first request in the period rather than snapping to wall-clock minutes, which
// makes the Retry-After hint exact (time until *this* window ends).
//
// It is safe for concurrent use. The clock is injectable so window rollover and
// Retry-After arithmetic are tested deterministically.
type Limiter struct {
	mu      sync.Mutex
	clock   func() time.Time
	windows map[uint]*window
}

// New returns a Limiter driven by the wall clock.
func New() *Limiter { return NewWithClock(time.Now) }

// NewWithClock returns a Limiter driven by clock; tests inject a fake clock.
func NewWithClock(clock func() time.Time) *Limiter {
	return &Limiter{
		clock:   clock,
		windows: make(map[uint]*window),
	}
}

// Allow records one request against keyID and reports whether it is within the
// key's limit. It permits exactly limit requests per window; the next request is
// denied with the time remaining until the window resets.
//
// Note: the windows map retains one small entry per key seen. Keys are
// operator-issued and few, so no eviction is implemented; if key cardinality
// ever grows unbounded this would need periodic pruning.
func (l *Limiter) Allow(keyID uint, limit int) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()
	w, ok := l.windows[keyID]
	// Start a fresh window on the first request or once the previous one has
	// fully elapsed.
	if !ok || now.Sub(w.start) >= Window {
		w = &window{start: now, count: 0}
		l.windows[keyID] = w
	}

	if w.count >= limit {
		retry := max(Window-now.Sub(w.start), 0)
		return Decision{Allowed: false, RetryAfter: retry}
	}

	w.count++
	return Decision{Allowed: true}
}
