package live

import (
	"context"
	"sync"
	"time"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// keyActor owns one key's read model.
//
// The split of responsibilities is deliberate:
//
//   - report is touched only by run(), so the read model itself needs no lock —
//     credits and resyncs are applied by one goroutine, in the order they arrive.
//   - subs is a plain mutex-guarded set instead of another channel, because
//     fan-out must never block the writer and a channel handshake for
//     subscriber bookkeeping is exactly the shape that deadlocks against the
//     hub's own lock.
type keyActor struct {
	keyID uint
	hub   *Hub

	credits chan int64
	done    chan struct{}

	mu     sync.Mutex
	subs   map[chan Snapshot]struct{}
	latest Snapshot
	primed bool
}

func newKeyActor(keyID uint, hub *Hub) *keyActor {
	return &keyActor{
		keyID: keyID,
		hub:   hub,
		// Buffered so a burst of recorded calls never blocks the request path:
		// the write side must not wait on the dashboard.
		credits: make(chan int64, 64),
		done:    make(chan struct{}),
		subs:    make(map[chan Snapshot]struct{}),
	}
}

// run is the actor: the only goroutine that mutates this key's report.
func (a *keyActor) run(ctx context.Context) {
	resync := time.NewTicker(a.hub.resync)
	defer resync.Stop()

	report := a.reload(ctx)
	a.publish(report)

	for {
		select {
		case <-a.done:
			return
		case <-ctx.Done():
			// The gateway is shutting down; subscribers are going away with it.
			return

		case tokens := <-a.credits:
			// A credit only moves the buckets that contain "now". Today, this
			// week and this month are nested and all include it; the historical
			// buckets are closed and cannot change.
			report.Today += tokens
			report.ThisWeek += tokens
			report.ThisMonth += tokens
			a.publish(report)

		case <-resync.C:
			// Authoritative refresh: corrects both a bucket boundary that has
			// rolled over since the last load and any drift from a dropped credit.
			report = a.reload(ctx)
			a.publish(report)
		}
	}
}

// reload reads the key's report from the store, keeping the last known figures
// if the store is unavailable: a transient database error should leave the
// dashboard showing slightly stale numbers, not zeros.
func (a *keyActor) reload(ctx context.Context) usage.Report {
	rep, err := a.hub.load(ctx, a.keyID, a.hub.clock())
	if err != nil {
		a.hub.logger.Error("live: reload usage report", "key_id", a.keyID, "err", err)
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.latest.Report
	}
	return rep
}

// publish records the new state and fans it out to every subscriber.
func (a *keyActor) publish(report usage.Report) {
	snap := Snapshot{KeyID: a.keyID, Report: report, At: a.hub.clock()}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.latest, a.primed = snap, true
	for ch := range a.subs {
		send(ch, snap)
	}
}

// send delivers without ever blocking the actor. A full buffer means the
// subscriber has not read the previous snapshot yet, so the older one is
// discarded — on a live display the newest value supersedes it anyway.
func send(ch chan Snapshot, snap Snapshot) {
	select {
	case ch <- snap:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
}

func (a *keyActor) addSub(ch chan Snapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subs[ch] = struct{}{}
}

// primeSub gives a joining subscriber the current state immediately, if the
// actor has any yet, so a second viewer does not stare at an empty panel until
// the next resync.
func (a *keyActor) primeSub(ch chan Snapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.primed {
		send(ch, a.latest)
	}
}

// removeSub detaches ch and reports how many subscribers remain.
func (a *keyActor) removeSub(ch chan Snapshot) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.subs, ch)
	return len(a.subs)
}

// credit hands tokens to the actor. It never blocks the caller (the request
// path): if the buffer is full the credit is dropped and the next resync
// reconciles from the store.
func (a *keyActor) credit(tokens int64) {
	select {
	case a.credits <- tokens:
	default:
		a.hub.logger.Warn("live: credit buffer full, dropping (resync will reconcile)", "key_id", a.keyID)
	}
}

// stop ends the actor's goroutine. The hub calls it under its own lock when the
// last subscriber leaves.
func (a *keyActor) stop() { close(a.done) }
