package ratelimit

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clock is a controllable time source for deterministic window tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newFixture() (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, time.July, 15, 10, 0, 0, 0, time.UTC)}
	return NewWithClock(c.now), c
}

// TestAllowsExactlyLimitThenDenies pins the boundary: with a limit of 3, the
// first three requests pass and the fourth is denied within the same window.
func TestAllowsExactlyLimitThenDenies(t *testing.T) {
	lim, _ := newFixture()
	const keyID, limit = 1, 3

	for i := 0; i < limit; i++ {
		require.True(t, lim.Allow(keyID, limit).Allowed, "request %d should be allowed", i+1)
	}
	d := lim.Allow(keyID, limit)
	require.False(t, d.Allowed)
	require.Equal(t, Window, d.RetryAfter, "at t=0 the whole window remains")
	require.Equal(t, 60, d.RetryAfterSeconds())
}

// TestRetryAfterShrinksWithinWindow checks the hint counts down as the window
// elapses, so a client waits only as long as actually needed.
func TestRetryAfterShrinksWithinWindow(t *testing.T) {
	lim, clk := newFixture()
	const keyID, limit = 1, 1

	require.True(t, lim.Allow(keyID, limit).Allowed)

	clk.advance(20 * time.Second)
	d := lim.Allow(keyID, limit)
	require.False(t, d.Allowed)
	require.Equal(t, 40*time.Second, d.RetryAfter)
	require.Equal(t, 40, d.RetryAfterSeconds())
}

// TestWindowRollover verifies the counter resets after a full window, so a key
// that backed off correctly is served again.
func TestWindowRollover(t *testing.T) {
	lim, clk := newFixture()
	const keyID, limit = 1, 2

	require.True(t, lim.Allow(keyID, limit).Allowed)
	require.True(t, lim.Allow(keyID, limit).Allowed)
	require.False(t, lim.Allow(keyID, limit).Allowed)

	clk.advance(Window) // window fully elapsed
	require.True(t, lim.Allow(keyID, limit).Allowed, "new window should allow again")
}

// TestPerKeyIsolation confirms one key's exhaustion never affects another.
func TestPerKeyIsolation(t *testing.T) {
	lim, _ := newFixture()
	const limit = 1

	require.True(t, lim.Allow(1, limit).Allowed)
	require.False(t, lim.Allow(1, limit).Allowed) // key 1 exhausted
	require.True(t, lim.Allow(2, limit).Allowed)  // key 2 unaffected
}

// TestRetryAfterSecondsRoundsUp guards the header value: a sub-second remainder
// rounds up to at least 1 so the client never retries too early.
func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	lim, clk := newFixture()
	const keyID, limit = 1, 1

	require.True(t, lim.Allow(keyID, limit).Allowed)
	clk.advance(Window - 200*time.Millisecond) // 0.2s remains
	d := lim.Allow(keyID, limit)
	require.False(t, d.Allowed)
	require.Equal(t, 1, d.RetryAfterSeconds())
}

// TestConcurrentAllowIsRaceFree runs many goroutines through one key and asserts
// the total number of allowed requests never exceeds the limit — the property a
// mutex-guarded counter must uphold. Run with -race.
func TestConcurrentAllowIsRaceFree(t *testing.T) {
	lim, _ := newFixture()
	const keyID, limit, goroutines = 7, 50, 200

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if lim.Allow(keyID, limit).Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, limit, allowed, "exactly %d requests may pass in one window", limit)
}
