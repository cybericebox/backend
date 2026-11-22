package event

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	eventModel "github.com/cybericebox/backend/internal/model/event"
	eventLabModel "github.com/cybericebox/backend/internal/model/eventLab"
)

func labs(n int) []eventLabModel.Lab {
	out := make([]eventLabModel.Lab, n)
	for i := range out {
		out[i] = eventLabModel.Lab{ID: uuid.Must(uuid.NewV7())}
	}
	return out
}

// The pass handles the labs with at most the configured number at once, each lab exactly once.
func TestLifecyclePassIsBoundedAndHandlesEachLabOnce(t *testing.T) {
	u := &EventUseCase{labLifecycleWorkers: 4, labBusy: &labBusySet{}}
	var running, peak atomic.Int32
	var mu sync.Mutex
	handled := map[uuid.UUID]int{}
	handle := func(_ context.Context, l eventLabModel.Lab, _ time.Time) []error {
		now := running.Add(1)
		for {
			p := peak.Load()
			if now <= p || peak.CompareAndSwap(p, now) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		running.Add(-1)
		mu.Lock()
		handled[l.ID]++
		mu.Unlock()
		return nil
	}
	list := labs(20)
	// the same lab twice in a batch is still one transition
	list = append(list, list[0], list[1])
	require.Empty(t, u.reconcileLabs(context.Background(), list, time.Now(), handle))
	assert.Len(t, handled, 20)
	for id, n := range handled {
		assert.Equal(t, 1, n, id.String())
	}
	assert.LessOrEqual(t, int(peak.Load()), 4, "never more than the workers")
	assert.Greater(t, int(peak.Load()), 1, "and really in parallel")
}

// A lab a worker has already (a slow earlier pass overlapping this one) is not moved a second time.
func TestLifecyclePassSkipsALabThatIsBusy(t *testing.T) {
	u := &EventUseCase{labLifecycleWorkers: 2, labBusy: &labBusySet{}}
	list := labs(3)
	require.True(t, u.labBusy.begin(list[0].ID))
	var calls atomic.Int32
	u.reconcileLabs(context.Background(), list, time.Now(), func(context.Context, eventLabModel.Lab, time.Time) []error {
		calls.Add(1)
		return nil
	})
	assert.EqualValues(t, 2, calls.Load())
	u.labBusy.end(list[0].ID)
	u.reconcileLabs(context.Background(), list[:1], time.Now(), func(context.Context, eventLabModel.Lab, time.Time) []error {
		calls.Add(1)
		return nil
	})
	assert.EqualValues(t, 3, calls.Load(), "free again once the other worker is done")
}

// The errors of every lab come back, and one failing lab does not stop the others.
func TestLifecyclePassCollectsEveryError(t *testing.T) {
	u := &EventUseCase{labLifecycleWorkers: 3, labBusy: &labBusySet{}}
	list := labs(6)
	var done atomic.Int32
	errs := u.reconcileLabs(context.Background(), list, time.Now(), func(_ context.Context, l eventLabModel.Lab, _ time.Time) []error {
		done.Add(1)
		if l.ID == list[2].ID || l.ID == list[4].ID {
			return []error{assert.AnError}
		}
		return nil
	})
	assert.EqualValues(t, 6, done.Load())
	assert.Len(t, errs, 2)
}

// A cancelled pass stops taking labs.
func TestLifecyclePassStopsFeedingWhenCancelled(t *testing.T) {
	u := &EventUseCase{labLifecycleWorkers: 1, labBusy: &labBusySet{}}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	u.reconcileLabs(ctx, labs(50), time.Now(), func(context.Context, eventLabModel.Lab, time.Time) []error {
		if calls.Add(1) == 2 {
			cancel()
		}
		return nil
	})
	assert.Less(t, int(calls.Load()), 50)
}

func TestLifecycleWorkersDefault(t *testing.T) {
	assert.Equal(t, 16, labLifecycleWorkers(0))
	assert.Equal(t, 5, labLifecycleWorkers(5))
}

// ── the initial deploy waits for the prewarm ───────────────────────────────

func prewarmFixture(wait time.Duration) (*EventUseCase, eventModel.Event, time.Time, time.Time) {
	deployAt := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	e := eventModel.Event{ID: uuid.Must(uuid.NewV7())}
	e.Lifecycle.StartAt = deployAt.Add(time.Hour)
	return &EventUseCase{prewarmLead: 30 * time.Minute, prewarmWait: wait, prewarm: newPrewarmState()}, e, deployAt, deployAt
}

func (u *EventUseCase) setSummary(e eventModel.Event, s PrewarmSummary) {
	u.prewarm.mu.Lock()
	u.prewarm.summary[e.ID] = s
	u.prewarm.mu.Unlock()
}

func TestTheInitialDeployWaitsWhileImagesAreStillBeingFetched(t *testing.T) {
	u, e, deployAt, now := prewarmFixture(10 * time.Minute)
	u.setSummary(e, PrewarmSummary{Total: 4, Done: 1, Warming: 2, Queued: 1, UpdatedAt: now})
	assert.True(t, u.holdForPrewarm(e, deployAt, now), "images are warming: wait")
	now = deployAt.Add(5 * time.Minute)
	u.setSummary(e, PrewarmSummary{Total: 4, Done: 3, Warming: 1, UpdatedAt: now})
	assert.True(t, u.holdForPrewarm(e, deployAt, now))

	// settled: all done, or some failed (their labs pull upstream; the failure is logged by the prewarm)
	u.setSummary(e, PrewarmSummary{Total: 4, Done: 4, UpdatedAt: now})
	assert.False(t, u.holdForPrewarm(e, deployAt, now))
	u.setSummary(e, PrewarmSummary{Total: 4, Done: 3, Failed: 1, UpdatedAt: now})
	assert.False(t, u.holdForPrewarm(e, deployAt, now), "a failed image does not hold the deploy")
}

func TestTheWaitEndsAtItsBoundAndAtTheEventStart(t *testing.T) {
	u, e, deployAt, _ := prewarmFixture(10 * time.Minute)
	warming := func(at time.Time) { u.setSummary(e, PrewarmSummary{Total: 2, Warming: 2, UpdatedAt: at}) }

	at := deployAt.Add(9*time.Minute + 59*time.Second)
	warming(at)
	assert.True(t, u.holdForPrewarm(e, deployAt, at), "just inside the bound")
	at = deployAt.Add(10 * time.Minute)
	warming(at)
	assert.False(t, u.holdForPrewarm(e, deployAt, at), "timeout: deploy anyway")
	assert.False(t, u.holdForPrewarm(e, deployAt, at.Add(time.Second)), "and stays released")

	// a wait never delays a started event, whatever the bound
	long, e2, deployAt2, _ := prewarmFixture(2 * time.Hour)
	at = e2.Lifecycle.StartAt
	long.setSummary(e2, PrewarmSummary{Warming: 1, UpdatedAt: at})
	assert.False(t, long.holdForPrewarm(e2, deployAt2, at))
}

func TestThereIsNoWaitWithoutAFreshPrewarmAnswer(t *testing.T) {
	u, e, deployAt, now := prewarmFixture(10 * time.Minute)
	assert.False(t, u.holdForPrewarm(e, deployAt, now), "no prewarm was asked: no cache, or nothing to warm")

	u.setSummary(e, PrewarmSummary{Warming: 3, UpdatedAt: now.Add(-3 * time.Minute)})
	assert.False(t, u.holdForPrewarm(e, deployAt, now), "an old answer (the calls are failing) tells nothing")

	off, e2, d2, n2 := prewarmFixture(0)
	off.setSummary(e2, PrewarmSummary{Warming: 3, UpdatedAt: n2})
	assert.False(t, off.holdForPrewarm(e2, d2, n2), "the wait is switched off")

	none, e3, d3, n3 := prewarmFixture(10 * time.Minute)
	none.prewarmLead = 0
	none.setSummary(e3, PrewarmSummary{Warming: 3, UpdatedAt: n3})
	assert.False(t, none.holdForPrewarm(e3, d3, n3), "prewarm itself is off")
}
