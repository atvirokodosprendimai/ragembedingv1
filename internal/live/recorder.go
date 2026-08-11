package live

import (
	"context"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// writer is the one capability the recorder wraps: persist a usage event.
// Declaring it here keeps this package independent of the GORM repository.
type writer interface {
	Record(ctx context.Context, e *usage.Event) error
}

// Recorder is the write side's notification hook. It persists the event first —
// the store stays authoritative — and only then tells the hub, so the dashboard
// can never show tokens that failed to be written.
//
// It satisfies the proxy handler's own usageRecorder interface, so the request
// path is wired to this instead of the repository and learns nothing about the
// read side.
type Recorder struct {
	next writer
	hub  *Hub
}

// NewRecorder wraps next so successful writes are published to hub.
func NewRecorder(next writer, hub *Hub) *Recorder {
	return &Recorder{next: next, hub: hub}
}

// Record persists the event and publishes its tokens to the key's actor.
func (r *Recorder) Record(ctx context.Context, e *usage.Event) error {
	if err := r.next.Record(ctx, e); err != nil {
		return err
	}
	// Publish is non-blocking and no-ops when nobody is watching this key, so
	// the request path pays nothing for an unwatched dashboard.
	r.hub.Publish(e.APIKeyID, e.PromptTokens)
	return nil
}
