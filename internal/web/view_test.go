package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/queue"
)

// TestBuildQueueStates pins the pressure strip's states, since each one renders
// a different thing on screen and only the view model decides which: an idle
// pool, a busy-but-unqueued pool, a saturated pool with a queue, and a queue old
// enough that promotion has kicked in.
func TestBuildQueueStates(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		vm := buildQueue(queue.Stats{Capacity: 4, PromoteAfter: 5 * time.Second})

		require.True(t, vm.Idle)
		require.False(t, vm.Saturated)
		require.False(t, vm.Waiting)
		require.Equal(t, "0/4", vm.InFlightLabel)
		require.Len(t, vm.Slots, 4)
		require.NotContains(t, vm.Slots, true)
	})

	t.Run("busy without a queue", func(t *testing.T) {
		vm := buildQueue(queue.Stats{Capacity: 4, InFlight: 2, PromoteAfter: 5 * time.Second})

		require.False(t, vm.Idle)
		require.False(t, vm.Saturated, "spare capacity is not saturation")
		require.False(t, vm.Waiting)
		require.Equal(t, []bool{true, true, false, false}, vm.Slots)
	})

	t.Run("saturated with a queue", func(t *testing.T) {
		vm := buildQueue(queue.Stats{
			Capacity: 2, InFlight: 2, Waiting: 9,
			PromoteAfter: 5 * time.Second,
			OldestWait:   2500 * time.Millisecond,
			Classes: []queue.ClassStat{
				{Priority: 9, Waiting: 1, OldestWait: 120 * time.Millisecond},
				{Priority: 0, Waiting: 8, OldestWait: 2500 * time.Millisecond},
			},
			Admitted: 1234, Promoted: 7,
		})

		require.True(t, vm.Saturated)
		require.True(t, vm.Waiting)
		require.False(t, vm.Aged, "nobody has waited past the promotion window yet")
		require.Equal(t, "9 queued", vm.WaitingLabel)
		require.Equal(t, "2.5s", vm.OldestLabel)
		require.Equal(t, "1,234", vm.AdmittedLabel)

		require.Len(t, vm.Classes, 2)
		// Highest rank first, and only it carries the accent.
		require.Equal(t, "p9", vm.Classes[0].Label)
		require.True(t, vm.Classes[0].Top)
		require.Equal(t, "120ms", vm.Classes[0].WaitLabel)
		require.False(t, vm.Classes[1].Top)
		// Depth bars scale against the deepest class.
		require.Equal(t, "100%", vm.Classes[1].Width)
	})

	t.Run("aged past the promotion window", func(t *testing.T) {
		vm := buildQueue(queue.Stats{
			Capacity: 2, InFlight: 2, Waiting: 3,
			PromoteAfter: 5 * time.Second,
			OldestWait:   6 * time.Second,
			Classes: []queue.ClassStat{
				{Priority: 9, Waiting: 1, OldestWait: time.Second},
				{Priority: 0, Waiting: 2, OldestWait: 6 * time.Second},
			},
		})

		require.True(t, vm.Aged, "the strip must show that promotion is in play")
		require.Equal(t, "5.0s", vm.PromoteLabel)
		require.False(t, vm.Classes[0].Aged, "the fresh priority class is not being promoted")
		require.True(t, vm.Classes[1].Aged)
	})
}
