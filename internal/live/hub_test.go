package live

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubStore stands in for the usage store: it counts loads so a test can prove
// the read side is not querying per tick per viewer.
type stubStore struct {
	mu     sync.Mutex
	report usage.Report
	err    error
	loads  atomic.Int64
}

func (s *stubStore) load(context.Context, uint, time.Time) (usage.Report, error) {
	s.loads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report, s.err
}

func (s *stubStore) set(r usage.Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.report = r
}

// waitFor reads the next snapshot, failing the test rather than hanging forever.
func waitFor(t *testing.T, ch <-chan Snapshot) Snapshot {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a snapshot")
		return Snapshot{}
	}
}

func TestSubscriberGetsTheCurrentReportImmediately(t *testing.T) {
	store := &stubStore{report: usage.Report{Today: 100, ThisWeek: 250, ThisMonth: 900}}
	hub := NewHub(store.load, time.Now, time.Hour, discardLogger())

	ch, cancel := hub.Subscribe(context.Background(), 7)
	defer cancel()

	snap := waitFor(t, ch)
	require.Equal(t, uint(7), snap.KeyID)
	require.Equal(t, int64(100), snap.Report.Today)
}

// TestCreditReachesSubscribersWithoutTouchingTheStore is the whole point of the
// read side: a recorded call shows up on screen immediately, and does not cost
// a query.
func TestCreditReachesSubscribersWithoutTouchingTheStore(t *testing.T) {
	store := &stubStore{report: usage.Report{Today: 100, ThisWeek: 100, ThisMonth: 100, PastMonth: 40, Before: 5}}
	hub := NewHub(store.load, time.Now, time.Hour, discardLogger())

	ch, cancel := hub.Subscribe(context.Background(), 1)
	defer cancel()
	waitFor(t, ch) // initial paint

	loadsBefore := store.loads.Load()
	hub.Publish(1, 42)

	snap := waitFor(t, ch)
	// The three nested live buckets move; the closed historical ones do not.
	require.Equal(t, int64(142), snap.Report.Today)
	require.Equal(t, int64(142), snap.Report.ThisWeek)
	require.Equal(t, int64(142), snap.Report.ThisMonth)
	require.Equal(t, int64(40), snap.Report.PastMonth)
	require.Equal(t, int64(5), snap.Report.Before)
	require.Equal(t, loadsBefore, store.loads.Load(), "a credit must not query the store")
}

func TestResyncCorrectsDrift(t *testing.T) {
	store := &stubStore{report: usage.Report{Today: 10}}
	// A resync fast enough to observe, slow enough not to race the assertions.
	hub := NewHub(store.load, time.Now, 20*time.Millisecond, discardLogger())

	ch, cancel := hub.Subscribe(context.Background(), 1)
	defer cancel()
	waitFor(t, ch)

	// The store moves underneath us — a bucket rolled over, or another process
	// wrote. The actor must converge on the store's figures.
	store.set(usage.Report{Today: 0, ThisWeek: 999})
	require.Eventually(t, func() bool {
		select {
		case s := <-ch:
			return s.Report.ThisWeek == 999 && s.Report.Today == 0
		default:
			return false
		}
	}, 2*time.Second, 5*time.Millisecond, "resync should converge on the store")
}

func TestLastUnsubscribeRetiresTheKeysGoroutine(t *testing.T) {
	store := &stubStore{}
	hub := NewHub(store.load, time.Now, time.Hour, discardLogger())

	ch1, cancel1 := hub.Subscribe(context.Background(), 3)
	_, cancel2 := hub.Subscribe(context.Background(), 3)
	waitFor(t, ch1)
	require.Equal(t, 1, hub.Watching(), "both viewers share one actor")

	cancel1()
	require.Equal(t, 1, hub.Watching(), "a remaining viewer keeps the actor alive")

	cancel2()
	require.Equal(t, 0, hub.Watching(), "the last viewer leaving retires the key")

	// Cancelling twice is harmless — handlers defer it and may also call it.
	cancel2()
	require.Equal(t, 0, hub.Watching())
}

func TestPublishForAnUnwatchedKeyIsANoOp(t *testing.T) {
	store := &stubStore{}
	hub := NewHub(store.load, time.Now, time.Hour, discardLogger())

	// Nobody is watching: this must not panic, block, or start anything.
	hub.Publish(999, 1000)
	require.Equal(t, 0, hub.Watching())
}

func TestReloadFailureKeepsTheLastKnownFigures(t *testing.T) {
	store := &stubStore{report: usage.Report{Today: 500}}
	hub := NewHub(store.load, time.Now, 20*time.Millisecond, discardLogger())

	ch, cancel := hub.Subscribe(context.Background(), 1)
	defer cancel()
	require.Equal(t, int64(500), waitFor(t, ch).Report.Today)

	// The database goes away. A dashboard showing slightly stale numbers is far
	// better than one that suddenly reports zero spend.
	store.mu.Lock()
	store.err = errors.New("database is gone")
	store.mu.Unlock()

	snap := waitFor(t, ch)
	require.Equal(t, int64(500), snap.Report.Today)
}

// TestConcurrentSubscribeAndPublishIsRaceFree runs under -race: viewers come and
// go while the write side keeps publishing, which is exactly the shape that
// deadlocks or races if the actor's lock and the hub's lock are entangled.
func TestConcurrentSubscribeAndPublishIsRaceFree(t *testing.T) {
	store := &stubStore{report: usage.Report{Today: 1}}
	hub := NewHub(store.load, time.Now, time.Millisecond, discardLogger())

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keyID := uint(i%4 + 1)
			ch, cancel := hub.Subscribe(context.Background(), keyID)
			defer cancel()
			for range 10 {
				hub.Publish(keyID, 1)
				select {
				case <-ch:
				case <-time.After(50 * time.Millisecond):
				}
			}
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool { return hub.Watching() == 0 },
		2*time.Second, 5*time.Millisecond, "every actor should retire once its viewers leave")
}

// fakeWriter records what the write side persisted.
type fakeWriter struct {
	events []*usage.Event
	err    error
}

func (f *fakeWriter) Record(_ context.Context, e *usage.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func TestRecorderPublishesOnlyAfterASuccessfulWrite(t *testing.T) {
	store := &stubStore{report: usage.Report{Today: 10}}
	hub := NewHub(store.load, time.Now, time.Hour, discardLogger())
	ch, cancel := hub.Subscribe(context.Background(), 1)
	defer cancel()
	waitFor(t, ch)

	// A failed write must not move the dashboard: it would be showing tokens
	// that were never persisted and will vanish on the next resync.
	failing := NewRecorder(&fakeWriter{err: errors.New("disk full")}, hub)
	require.Error(t, failing.Record(context.Background(), &usage.Event{APIKeyID: 1, PromptTokens: 77}))
	select {
	case s := <-ch:
		t.Fatalf("a failed write must not publish, got %+v", s.Report)
	case <-time.After(100 * time.Millisecond):
	}

	writer := &fakeWriter{}
	rec := NewRecorder(writer, hub)
	require.NoError(t, rec.Record(context.Background(), &usage.Event{APIKeyID: 1, PromptTokens: 33}))
	require.Len(t, writer.events, 1, "the store stays authoritative")
	require.Equal(t, int64(43), waitFor(t, ch).Report.Today)
}
