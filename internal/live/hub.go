package live

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// DefaultResync is how often an actor reloads its key's report from the store.
// Credits keep the read model current between reloads; the reload exists to
// correct the two things a credit cannot know about — a bucket boundary that has
// since rolled over (midnight, Monday, the 1st) and any write that reached the
// store by another route.
const DefaultResync = 30 * time.Second

// Loader reads a key's authoritative report from the write store. It is a
// function rather than an interface because it is the only thing the hub needs
// from persistence, and it keeps this package free of any repository type.
type Loader func(ctx context.Context, keyID uint, now time.Time) (usage.Report, error)

// Snapshot is an immutable view of one key's usage at a moment. Subscribers get
// copies, so nothing they do can reach back into the actor's state.
type Snapshot struct {
	KeyID  uint
	Report usage.Report
	At     time.Time
}

// Hub owns one actor per watched key and routes writes to them.
//
// It is safe for concurrent use. The clock is injectable so resync and snapshot
// timestamps are deterministic in tests.
type Hub struct {
	mu    sync.Mutex
	keys  map[uint]*keyActor
	load  Loader
	clock func() time.Time
	// resync is how often each actor reloads from the store.
	resync time.Duration
	logger *slog.Logger
}

// NewHub returns a hub that loads reports with load. resync <= 0 uses
// DefaultResync; clock and logger may be nil.
func NewHub(load Loader, clock func() time.Time, resync time.Duration, logger *slog.Logger) *Hub {
	if clock == nil {
		clock = time.Now
	}
	if resync <= 0 {
		resync = DefaultResync
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		keys:   make(map[uint]*keyActor),
		load:   load,
		clock:  clock,
		resync: resync,
		logger: logger,
	}
}

// Subscribe starts watching keyID and returns the channel snapshots arrive on
// plus the func that stops watching. The first snapshot is sent as soon as the
// actor has a report, so a caller can paint immediately without a separate read.
//
// The returned cancel func must be called (defer it): it is what retires the
// key's goroutine once the last viewer leaves.
func (h *Hub) Subscribe(ctx context.Context, keyID uint) (<-chan Snapshot, func()) {
	// Buffered by one: a subscriber that is mid-render never blocks the actor,
	// and a snapshot that arrives while the buffer is full is dropped in favour
	// of the newer one — for a live display, the latest value is the only one
	// that matters.
	ch := make(chan Snapshot, 1)

	h.mu.Lock()
	a, ok := h.keys[keyID]
	if !ok {
		a = newKeyActor(keyID, h)
		h.keys[keyID] = a
		go a.run(ctx)
	}
	a.addSub(ch)
	h.mu.Unlock()

	// Prime the new subscriber from whatever the actor already knows, so a second
	// viewer of the same key paints instantly instead of waiting for a resync.
	a.primeSub(ch)

	var once sync.Once
	return ch, func() { once.Do(func() { h.unsubscribe(keyID, ch) }) }
}

// unsubscribe detaches ch and retires the key's actor when nobody is left.
func (h *Hub) unsubscribe(keyID uint, ch chan Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()

	a, ok := h.keys[keyID]
	if !ok {
		return
	}
	if remaining := a.removeSub(ch); remaining == 0 {
		// Retiring under the hub lock is what makes this race-free: a Subscribe
		// arriving now blocks until the map no longer holds the stopped actor and
		// therefore starts a fresh one.
		delete(h.keys, keyID)
		a.stop()
	}
}

// Publish tells keyID's actor that tokens were just recorded against it. It is
// called by the write side after a successful store write, and never blocks: if
// nobody is watching the key there is no actor and nothing to do.
func (h *Hub) Publish(keyID uint, tokens int64) {
	h.mu.Lock()
	a, ok := h.keys[keyID]
	h.mu.Unlock()
	if !ok {
		return
	}
	a.credit(tokens)
}

// Watching reports how many keys currently have an actor. It exists for tests
// and for the operator log line at shutdown.
func (h *Hub) Watching() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.keys)
}
