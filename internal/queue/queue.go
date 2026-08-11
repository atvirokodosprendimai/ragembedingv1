package queue

import (
	"container/list"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultPromoteAfter is how long a waiter may sit in the queue before it is
// promoted ahead of higher-priority work. It is the knob that turns "priority"
// into "priority, but nobody starves": five seconds is long enough that the
// priority site effectively never waits behind free traffic, and short enough
// that a free client sees a bounded, explainable delay rather than a hang.
const DefaultPromoteAfter = 5 * time.Second

// Release returns an admitted request's slot to the queue. It is safe to call
// more than once; only the first call has an effect, so a handler can defer it
// unconditionally.
type Release func()

// waiter is one request parked in the queue.
type waiter struct {
	priority int
	since    time.Time
	// ready is closed when the waiter has been granted a slot. Closing (rather
	// than sending) means the grant never blocks on a waiter that has already
	// walked away.
	ready chan struct{}
	// elem is this waiter's position in its priority class, kept so a cancelled
	// waiter is removed in O(1) instead of scanning the list.
	elem *list.Element
	// granted and promoted are written under the queue's mutex by the grant, and
	// read under it by a cancelling waiter.
	granted  bool
	promoted bool
}

// Queue is a bounded-concurrency, priority-aware admission gate. The zero value
// is not usable; construct one with New.
//
// It is safe for concurrent use. The clock is injectable so promotion timing is
// tested deterministically.
type Queue struct {
	mu       sync.Mutex
	capacity int
	// free is the number of unclaimed slots. It is only ever positive when no
	// waiter is queued, because a released slot is handed straight to a waiter.
	free         int
	promoteAfter time.Duration
	clock        func() time.Time
	// classes maps a priority to that class's FIFO of waiters. Empty classes are
	// deleted so the map stays as small as the live traffic mix.
	classes map[int]*list.List
	waiting int

	// Lifetime counters, surfaced through Stats for the dashboard.
	admitted  uint64
	promoted  uint64
	cancelled uint64
}

// New returns a Queue admitting at most capacity concurrent requests and
// promoting any waiter older than promoteAfter. capacity must be at least 1 and
// promoteAfter positive; both are validated by config at startup, so a bad value
// here is a programming error rather than an operator mistake.
func New(capacity int, promoteAfter time.Duration) *Queue {
	return NewWithClock(capacity, promoteAfter, time.Now)
}

// NewWithClock is New with an injectable clock; tests drive promotion with a
// fake clock instead of sleeping.
func NewWithClock(capacity int, promoteAfter time.Duration, clock func() time.Time) *Queue {
	if capacity < 1 {
		panic(fmt.Sprintf("queue: capacity must be >= 1, got %d", capacity))
	}
	if promoteAfter <= 0 {
		panic(fmt.Sprintf("queue: promoteAfter must be > 0, got %s", promoteAfter))
	}
	return &Queue{
		capacity:     capacity,
		free:         capacity,
		promoteAfter: promoteAfter,
		clock:        clock,
		classes:      make(map[int]*list.List),
	}
}

// Acquire blocks until a slot is available for a request of the given priority
// (higher goes first) and returns the func that gives the slot back. It returns
// ctx.Err() if the caller gives up first — a hung-up client or a shutting-down
// gateway — in which case no slot is held and Release must not be called.
func (q *Queue) Acquire(ctx context.Context, priority int) (Release, error) {
	q.mu.Lock()
	// Fast path: capacity to spare and nobody queued. The "nobody queued" half is
	// what forbids barging; it can only ever be true together with free > 0,
	// since a released slot is handed straight to the queue.
	if q.free > 0 && q.waiting == 0 {
		q.free--
		q.admitted++
		q.mu.Unlock()
		return q.releaser(), nil
	}

	w := &waiter{priority: priority, since: q.clock(), ready: make(chan struct{})}
	q.pushLocked(w)
	q.mu.Unlock()

	select {
	case <-w.ready:
		q.mu.Lock()
		q.admitted++
		if w.promoted {
			q.promoted++
		}
		q.mu.Unlock()
		return q.releaser(), nil

	case <-ctx.Done():
		q.mu.Lock()
		defer q.mu.Unlock()
		if w.granted {
			// Raced: the slot was handed over as the context died. Nobody will
			// call our Release, so pass the slot straight on rather than leaking
			// capacity for the lifetime of the process.
			q.releaseLocked()
			q.cancelled++
			return nil, ctx.Err()
		}
		q.removeLocked(w)
		q.cancelled++
		return nil, ctx.Err()
	}
}

// releaser returns a one-shot Release. sync.Once makes a double release (a
// defer plus an explicit call, say) harmless instead of silently inflating
// capacity.
func (q *Queue) releaser() Release {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.releaseLocked()
		})
	}
}

// releaseLocked gives one slot back, handing it directly to the next waiter when
// there is one. The caller must hold q.mu.
func (q *Queue) releaseLocked() {
	if w, promoted := q.popLocked(q.clock()); w != nil {
		w.granted = true
		w.promoted = promoted
		close(w.ready)
		return
	}
	q.free++
}

// pushLocked appends w to the tail of its priority class, creating the class on
// first use. The caller must hold q.mu.
func (q *Queue) pushLocked(w *waiter) {
	l, ok := q.classes[w.priority]
	if !ok {
		l = list.New()
		q.classes[w.priority] = l
	}
	w.elem = l.PushBack(w)
	q.waiting++
}

// removeLocked unlinks w from its class. The caller must hold q.mu.
func (q *Queue) removeLocked(w *waiter) {
	l, ok := q.classes[w.priority]
	if !ok {
		return
	}
	l.Remove(w.elem)
	q.waiting--
	if l.Len() == 0 {
		delete(q.classes, w.priority)
	}
}

// popLocked picks the next waiter to admit and reports whether it won its slot
// by promotion rather than by rank. Only class heads are considered: each class
// is FIFO, so its head is both the oldest waiter in that class and the only
// candidate the priority rule would pick. That makes the scan O(number of
// priority classes in play) — a handful — rather than O(waiters).
//
// The caller must hold q.mu.
func (q *Queue) popLocked(now time.Time) (*waiter, bool) {
	var top, oldest *waiter
	for _, l := range q.classes {
		// Empty classes are deleted on removal, so a class in the map always has
		// a head.
		head := l.Front().Value.(*waiter)
		if top == nil || head.priority > top.priority {
			top = head
		}
		if oldest == nil || head.since.Before(oldest.since) {
			oldest = head
		}
	}
	if top == nil {
		return nil, false
	}
	// Anti-starvation beats rank: a waiter that has been queued longer than
	// promoteAfter goes first even if higher-priority work is waiting. Without
	// this, sustained priority traffic would postpone free traffic forever.
	if oldest != top && now.Sub(oldest.since) >= q.promoteAfter {
		q.removeLocked(oldest)
		return oldest, true
	}
	q.removeLocked(top)
	return top, false
}

// ClassStat is one priority class's share of the queue.
type ClassStat struct {
	Priority   int
	Waiting    int
	OldestWait time.Duration
}

// Stats is a point-in-time snapshot of queue pressure, for the dashboard and
// logs.
type Stats struct {
	Capacity int
	InFlight int
	Waiting  int
	// Classes holds every non-empty priority class, highest priority first.
	Classes []ClassStat
	// OldestWait is how long the longest-waiting request has been queued.
	OldestWait time.Duration
	// Lifetime counters since process start.
	Admitted  uint64
	Promoted  uint64
	Cancelled uint64
}

// Stats returns a snapshot of the queue.
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.clock()
	s := Stats{
		Capacity:  q.capacity,
		InFlight:  q.capacity - q.free,
		Waiting:   q.waiting,
		Admitted:  q.admitted,
		Promoted:  q.promoted,
		Cancelled: q.cancelled,
	}
	for p, l := range q.classes {
		head := l.Front().Value.(*waiter)
		// The head is the class's oldest waiter, so its wait is the class's max.
		wait := now.Sub(head.since)
		s.Classes = append(s.Classes, ClassStat{Priority: p, Waiting: l.Len(), OldestWait: wait})
		s.OldestWait = max(s.OldestWait, wait)
	}
	// Map iteration is random; sort so the dashboard's rows never shuffle.
	sort.Slice(s.Classes, func(i, j int) bool { return s.Classes[i].Priority > s.Classes[j].Priority })
	return s
}
