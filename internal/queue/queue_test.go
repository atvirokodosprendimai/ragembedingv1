package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- helpers ----------------------------------------------------------------

// fakeClock is a manually advanced clock so promotion is tested without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// enqueue starts an Acquire in the background and reports the priority it won
// on, so a test can assert the order slots were handed out in.
func enqueue(t *testing.T, q *Queue, priority int, admitted chan<- int) {
	t.Helper()
	go func() {
		release, err := q.Acquire(context.Background(), priority)
		if err != nil {
			return
		}
		admitted <- priority
		release()
	}()
}

// waitForWaiting blocks until exactly n requests are queued, so a test never
// releases a slot before the waiters it is ordering have actually parked.
func waitForWaiting(t *testing.T, q *Queue, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return q.Stats().Waiting == n },
		2*time.Second, time.Millisecond, "expected %d waiters, got %d", n, q.Stats().Waiting)
}

// --- tests ------------------------------------------------------------------

func TestAcquireIsImmediateWhileCapacityRemains(t *testing.T) {
	q := New(2, DefaultPromoteAfter)

	r1, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)
	r2, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	s := q.Stats()
	require.Equal(t, 2, s.InFlight)
	require.Equal(t, 0, s.Waiting)

	r1()
	r2()
	require.Equal(t, 0, q.Stats().InFlight)
}

func TestHigherPriorityIsAdmittedFirst(t *testing.T) {
	q := New(1, DefaultPromoteAfter)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	admitted := make(chan int, 3)
	// Queue low priority first: rank, not arrival order, must decide.
	enqueue(t, q, 0, admitted)
	waitForWaiting(t, q, 1)
	enqueue(t, q, 5, admitted)
	waitForWaiting(t, q, 2)
	enqueue(t, q, 9, admitted)
	waitForWaiting(t, q, 3)

	hold()
	require.Equal(t, 9, <-admitted)
	require.Equal(t, 5, <-admitted)
	require.Equal(t, 0, <-admitted)
}

func TestFIFOWithinAPriorityClass(t *testing.T) {
	q := New(1, DefaultPromoteAfter)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	// Distinct priorities would order themselves; same-priority waiters must
	// come out in arrival order, so each carries its own channel to identify it.
	order := make(chan int, 3)
	for i := 1; i <= 3; i++ {
		i := i
		go func() {
			release, err := q.Acquire(context.Background(), 3)
			if err != nil {
				return
			}
			order <- i
			release()
		}()
		waitForWaiting(t, q, i)
	}

	hold()
	require.Equal(t, 1, <-order)
	require.Equal(t, 2, <-order)
	require.Equal(t, 3, <-order)
}

func TestAgedWaiterIsPromotedAheadOfHigherPriority(t *testing.T) {
	clock := newFakeClock()
	q := NewWithClock(1, 5*time.Second, clock.Now)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	admitted := make(chan int, 2)
	// A free request queues first, then sits long enough to be promoted.
	enqueue(t, q, 0, admitted)
	waitForWaiting(t, q, 1)
	clock.Advance(5 * time.Second)
	enqueue(t, q, 9, admitted)
	waitForWaiting(t, q, 2)

	hold()
	require.Equal(t, 0, <-admitted, "the aged free request should have been promoted")
	require.Equal(t, 9, <-admitted)

	require.Eventually(t, func() bool { return q.Stats().Promoted == 1 },
		time.Second, time.Millisecond, "promotion should be counted")
}

func TestPriorityStillWinsBeforeThePromotionThreshold(t *testing.T) {
	clock := newFakeClock()
	q := NewWithClock(1, 5*time.Second, clock.Now)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	admitted := make(chan int, 2)
	enqueue(t, q, 0, admitted)
	waitForWaiting(t, q, 1)
	// One tick short of the threshold: rank must still decide.
	clock.Advance(5*time.Second - time.Nanosecond)
	enqueue(t, q, 9, admitted)
	waitForWaiting(t, q, 2)

	hold()
	require.Equal(t, 9, <-admitted)
	require.Equal(t, 0, <-admitted)
	require.Zero(t, q.Stats().Promoted)
}

func TestCancelledWaiterLeavesTheQueue(t *testing.T) {
	q := New(1, DefaultPromoteAfter)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := q.Acquire(ctx, 9)
		errc <- err
	}()
	waitForWaiting(t, q, 1)

	cancel()
	require.ErrorIs(t, <-errc, context.Canceled)
	require.Equal(t, 0, q.Stats().Waiting)
	require.Equal(t, uint64(1), q.Stats().Cancelled)

	// The slot the client walked away from is still usable by the next request.
	hold()
	next, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)
	next()
}

func TestSlotIsNotLeakedWhenCancelRacesTheGrant(t *testing.T) {
	q := New(1, DefaultPromoteAfter)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	// Cancel and release concurrently so the waiter can be granted a slot it
	// will never use; the queue must recycle it either way.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if release, err := q.Acquire(ctx, 9); err == nil {
			release()
		}
	}()
	waitForWaiting(t, q, 1)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cancel() }()
	go func() { defer wg.Done(); hold() }()
	wg.Wait()
	<-done

	require.Eventually(t, func() bool { return q.Stats().InFlight == 0 },
		time.Second, time.Millisecond, "capacity should return to idle, got %+v", q.Stats())

	// Capacity is intact: the single slot can be taken again without blocking.
	next, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)
	next()
}

func TestReleaseIsIdempotent(t *testing.T) {
	q := New(1, DefaultPromoteAfter)
	release, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	release()
	release()
	require.Equal(t, 0, q.Stats().InFlight, "a double release must not invent capacity")
}

func TestNewArrivalsDoNotBargePastTheQueue(t *testing.T) {
	q := New(1, DefaultPromoteAfter)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	admitted := make(chan int, 1)
	enqueue(t, q, 0, admitted) // queued while the only slot is held
	waitForWaiting(t, q, 1)

	// A high-priority arrival that shows up *after* the slot is released must
	// still queue behind the waiter the slot was handed to.
	hold()
	require.Equal(t, 0, <-admitted)
}

func TestStatsReportPressureByClass(t *testing.T) {
	clock := newFakeClock()
	q := NewWithClock(1, DefaultPromoteAfter, clock.Now)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)
	defer hold()

	admitted := make(chan int, 3)
	enqueue(t, q, 0, admitted)
	waitForWaiting(t, q, 1)
	clock.Advance(2 * time.Second)
	enqueue(t, q, 0, admitted)
	waitForWaiting(t, q, 2)
	enqueue(t, q, 9, admitted)
	waitForWaiting(t, q, 3)

	s := q.Stats()
	require.Equal(t, 1, s.Capacity)
	require.Equal(t, 1, s.InFlight)
	require.Equal(t, 3, s.Waiting)
	require.Equal(t, 2*time.Second, s.OldestWait)
	require.Equal(t, uint64(1), s.Admitted)

	// Highest priority first, and each class reports its own depth and age.
	require.Len(t, s.Classes, 2)
	require.Equal(t, ClassStat{Priority: 9, Waiting: 1, OldestWait: 0}, s.Classes[0])
	require.Equal(t, ClassStat{Priority: 0, Waiting: 2, OldestWait: 2 * time.Second}, s.Classes[1])
}

func TestConcurrentAcquireNeverExceedsCapacity(t *testing.T) {
	const (
		capacity = 4
		workers  = 200
	)
	q := New(capacity, time.Millisecond)

	var inFlight, peak atomic.Int64
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := q.Acquire(context.Background(), i%3)
			if err != nil {
				return
			}
			defer release()

			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			inFlight.Add(-1)
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, peak.Load(), int64(capacity), "admitted more than capacity at once")
	require.Equal(t, 0, q.Stats().InFlight)
	require.Equal(t, uint64(workers), q.Stats().Admitted)
}

// TestDeepAgedBacklogCannotBuryPriority is the regression test for the failure a
// real load run exposed: with capacity 2 and a 20-deep free backlog, every
// waiter aged past the promotion window, so promoting them all in arrival order
// turned the queue back into FIFO and the priority key waited ~7x longer than it
// should have. Promotions must alternate with strict-priority admissions.
func TestDeepAgedBacklogCannotBuryPriority(t *testing.T) {
	clock := newFakeClock()
	q := NewWithClock(1, 5*time.Second, clock.Now)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	// A deep free backlog, all of it queued before the priority request and all
	// of it old enough to be promoted.
	const backlog = 10
	admitted := make(chan int, backlog+1)
	for i := 1; i <= backlog; i++ {
		enqueue(t, q, 0, admitted)
		waitForWaiting(t, q, i)
	}
	clock.Advance(30 * time.Second)
	enqueue(t, q, 9, admitted)
	waitForWaiting(t, q, backlog+1)

	// Drain: the aged backlog gets every other slot, so the priority request is
	// served second rather than after all ten.
	hold()
	require.Equal(t, 0, <-admitted, "an aged waiter takes the first slot")
	require.Equal(t, 9, <-admitted, "priority must not wait for the whole backlog")
}

// TestPromotionsAlternateUnderSustainedPressure pins the split the alternation
// produces: with both classes always contending, aged traffic and priority
// traffic take every other slot.
func TestPromotionsAlternateUnderSustainedPressure(t *testing.T) {
	clock := newFakeClock()
	q := NewWithClock(1, 5*time.Second, clock.Now)
	hold, err := q.Acquire(context.Background(), 0)
	require.NoError(t, err)

	admitted := make(chan int, 6)
	for i := 1; i <= 3; i++ {
		enqueue(t, q, 0, admitted)
		waitForWaiting(t, q, i)
	}
	clock.Advance(30 * time.Second) // the free waiters are all promotable
	for i := 1; i <= 3; i++ {
		enqueue(t, q, 9, admitted)
		waitForWaiting(t, q, 3+i)
	}

	hold()
	got := make([]int, 0, 6)
	for range 6 {
		got = append(got, <-admitted)
	}
	require.Equal(t, []int{0, 9, 0, 9, 0, 9}, got)
}
