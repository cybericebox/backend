package labActivity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/daemon/internal/delivery/repository/assistantStatsRepo"
	"github.com/cybericebox/daemon/internal/delivery/repository/labActivityRepo"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
	"github.com/cybericebox/daemon/internal/useCase/labActivity"
)

// fakeAgent serves the pages in order and records the cursors it was asked with.
type fakeAgent struct {
	enabled bool
	pages   []infraModel.ActivityPage
	errs    []error // an error for the call of the same index, nil for none
	asked   []string
	calls   int
}

func (a *fakeAgent) ActivityEnabled(context.Context, string) (bool, error) { return a.enabled, nil }

func (a *fakeAgent) GroupActivity(_ context.Context, _, cursor string, _ int32) (infraModel.ActivityPage, error) {
	a.asked = append(a.asked, cursor)
	i := a.calls
	a.calls++
	if i < len(a.errs) && a.errs[i] != nil {
		return infraModel.ActivityPage{}, a.errs[i]
	}
	if i >= len(a.pages) {
		return infraModel.ActivityPage{NextCursor: cursor}, nil
	}
	return a.pages[i], nil
}

// memStore folds batches the way the statement does, to check what a pull hands to the store.
type memStore struct {
	cursors  map[string]string
	gaps     map[string]bool
	applied  []labActivityRepo.Batch
	egress   map[string]bool
	bytes    map[string]int64
	instance []labActivityRepo.Instance
}

func newStore() *memStore {
	return &memStore{cursors: map[string]string{}, gaps: map[string]bool{}, egress: map[string]bool{}, bytes: map[string]int64{}}
}

func (s *memStore) Groups(context.Context, assistantModel.StatsMode, time.Time) ([]string, error) {
	return []string{"g-1"}, nil
}

func (s *memStore) Cursor(_ context.Context, g string) (string, bool, error) {
	c, ok := s.cursors[g]
	return c, ok, nil
}

func (s *memStore) Apply(_ context.Context, g, cursor string, gap bool, b labActivityRepo.Batch, _ time.Time) error {
	s.cursors[g] = cursor
	s.gaps[g] = s.gaps[g] || gap
	s.applied = append(s.applied, b)
	for _, l := range b.Labs {
		s.egress[l.Lab] = s.egress[l.Lab] || l.Egress
	}
	for _, c := range b.Clients {
		s.bytes[c.Lab+"/"+c.Client] += c.Bytes
	}
	return nil
}

func (s *memStore) Instances(context.Context, []uuid.UUID, []uuid.UUID, time.Time, time.Duration) ([]labActivityRepo.Instance, error) {
	return s.instance, nil
}

func (s *memStore) OfClient(_ context.Context, _, lab, client string, _ time.Time, _ time.Duration) (labActivityRepo.Figures, bool, error) {
	if _, ok := s.egress[lab]; !ok {
		return labActivityRepo.Figures{}, false, nil
	}
	return labActivityRepo.Figures{EgressUsed: s.egress[lab], VPNBytes: s.bytes[lab+"/"+client]}, true, nil
}

func (s *memStore) Purge(context.Context, time.Time) error { return nil }

func newUC(a *fakeAgent, s *memStore) *labActivity.UseCase {
	return labActivity.New(labActivity.Dependencies{Agent: a, Store: s, Now: func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) }})
}

func egress(lab string) infraModel.ActivityEvent {
	return infraModel.ActivityEvent{Lab: lab, Egress: &infraModel.InternetEgress{SourceAddress: "10.0.0.5", DstAddress: "8.8.8.8", DstPort: 53}}
}

func touch(lab, client, dst string, out, in int64) infraModel.ActivityEvent {
	return infraModel.ActivityEvent{Lab: lab, Touch: &infraModel.DeviceTouch{Client: client, DstAddress: dst, BytesOut: out, BytesIn: in}}
}

func TestAPullFollowsHasMoreAndPassesTheCursorBackUnchanged(t *testing.T) {
	a := &fakeAgent{enabled: true, pages: []infraModel.ActivityPage{
		{Events: []infraModel.ActivityEvent{touch("l-1", "p1", "10.0.0.7", 10, 20)}, NextCursor: "opaque/1==", HasMore: true},
		{Events: []infraModel.ActivityEvent{touch("l-1", "p1", "10.0.0.7", 1, 2)}, NextCursor: "opaque/2==", HasMore: false},
	}}
	s := newStore()
	s.cursors["g-1"] = "stored-before"
	require.NoError(t, newUC(a, s).PullGroup(context.Background(), "g-1"))
	assert.Equal(t, []string{"stored-before", "opaque/1=="}, a.asked, "each call starts where the last answer said, and the first where the stored cursor was")
	assert.Equal(t, "opaque/2==", s.cursors["g-1"])
	assert.EqualValues(t, 33, s.bytes["l-1/p1"], "out + in of both pages")
}

func TestNothingIsPulledWhileTheReportsAreOffOrTheLaboratoryRefuses(t *testing.T) {
	off := &fakeAgent{enabled: false}
	s := newStore()
	require.NoError(t, newUC(off, s).PullGroup(context.Background(), "g-1"))
	assert.Zero(t, off.calls, "features.activity.enabled is checked first")
	assert.Empty(t, s.cursors)

	// FAILED_PRECONDITION and an unknown group are not failures either
	for _, refusal := range []error{infraModel.ErrActivityDisabled, infraModel.ErrActivityGroupGone} {
		a := &fakeAgent{enabled: true, errs: []error{refusal}}
		s = newStore()
		require.NoError(t, newUC(a, s).PullGroup(context.Background(), "g-1"))
		assert.Empty(t, s.cursors, "nothing is stored, so the figures stay null")
	}

	broken := &fakeAgent{enabled: true, errs: []error{errors.New("agent down")}}
	assert.Error(t, newUC(broken, newStore()).PullGroup(context.Background(), "g-1"), "a failing agent is reported")
}

func TestAGapIsKeptAndDoesNotFailThePull(t *testing.T) {
	a := &fakeAgent{enabled: true, pages: []infraModel.ActivityPage{{Events: []infraModel.ActivityEvent{egress("l-1")}, NextCursor: "c", Gap: true}}}
	s := newStore()
	require.NoError(t, newUC(a, s).PullGroup(context.Background(), "g-1"))
	assert.True(t, s.gaps["g-1"])
	assert.True(t, s.egress["l-1"])
}

func TestAnUnknownCursorStartsFromTheOldestEvent(t *testing.T) {
	a := &fakeAgent{enabled: true, errs: []error{infraModel.ErrActivityCursor},
		pages: []infraModel.ActivityPage{{}, {Events: []infraModel.ActivityEvent{egress("l-1")}, NextCursor: "fresh"}}}
	s := newStore()
	s.cursors["g-1"] = "stale"
	require.NoError(t, newUC(a, s).PullGroup(context.Background(), "g-1"))
	assert.Equal(t, []string{"stale", ""}, a.asked)
	assert.Equal(t, "fresh", s.cursors["g-1"])
	assert.True(t, s.gaps["g-1"], "what may be counted twice or missed is marked")
}

// Egress belongs to the lab. A page folds into one entry per lab, and no egress fact is tied to a device, a client or an address.
func TestFoldKeepsEgressPerLabAndMergesAConsoleSession(t *testing.T) {
	b := labActivity.Fold([]infraModel.ActivityEvent{
		egress("l-1"), egress("l-1"),
		touch("l-1", "p1", "10.0.0.7", 5, 5), touch("l-1", "p1", "10.0.0.8", 1, 1), touch("l-1", "p1", "10.0.0.7", 2, 2), touch("l-2", "p2", "10.0.1.7", 1, 0),
		{Lab: "l-1", Console: &infraModel.ConsoleSession{SessionID: "s1", Device: "web", Client: "p1", OpenedAtMs: 1000}},
		{Lab: "l-1", Console: &infraModel.ConsoleSession{SessionID: "s1", Device: "web", Client: "p1", OpenedAtMs: 1000, ClosedAtMs: 61000}},
		{Lab: "", Console: &infraModel.ConsoleSession{SessionID: "s9"}},
	})
	require.Len(t, b.Labs, 2)
	assert.Equal(t, labActivityRepo.Lab{Lab: "l-1", Egress: true}, b.Labs[0])
	assert.Equal(t, labActivityRepo.Lab{Lab: "l-2", Egress: false}, b.Labs[1], "a lab that only appears in other facts did not use egress")
	require.Len(t, b.Clients, 2)
	assert.EqualValues(t, 16, b.Clients[0].Bytes)
	assert.Len(t, b.Dsts, 3, "distinct lab addresses per client")
	require.Len(t, b.Sessions, 1)
	assert.EqualValues(t, 61000, b.Sessions[0].ClosedMs, "open and close events of one session are one session")
}

func TestTheSeamGivesFiguresOnlyForTheInstancesThatHaveThem(t *testing.T) {
	event, team, ex := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	other := assistantStatsRepo.InstanceKey{EventID: event, TeamID: uuid.Must(uuid.NewV7()), EventExerciseID: ex}
	key := assistantStatsRepo.InstanceKey{EventID: event, TeamID: team, EventExerciseID: ex}
	s := newStore()
	s.instance = []labActivityRepo.Instance{{EventID: event, TeamID: team, EventExerciseID: ex, Figures: labActivityRepo.Figures{EgressUsed: true, VPNBytes: 4096, DevicesTouched: 3, ConsoleMinutes: 12.5}}}
	got, err := newUC(&fakeAgent{}, s).Activity(context.Background(), []assistantStatsRepo.InstanceKey{key, other})
	require.NoError(t, err)
	require.Len(t, got, 1, "an instance without a report is absent: not reported, not zero")
	a := got[key]
	assert.True(t, *a.EgressUsed)
	assert.EqualValues(t, 4096, *a.VPNBytes)
	assert.EqualValues(t, 3, *a.DevicesTouched)
	assert.EqualValues(t, 12.5, *a.ConsoleMinutes)
}

func TestOfLabIsNilUntilTheGroupIsPulledAndZeroAfterwards(t *testing.T) {
	s := newStore()
	uc := newUC(&fakeAgent{}, s)
	f, err := uc.OfLab(context.Background(), "g-1", "l-1", "c")
	require.NoError(t, err)
	assert.Nil(t, f, "reports off or never pulled: not reported")

	s.cursors["g-1"] = "c" // pulled, and the lab did nothing
	f, err = uc.OfLab(context.Background(), "g-1", "l-1", "c")
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.False(t, f.EgressUsed)
	assert.Zero(t, f.VPNBytes)

	s.egress["l-1"], s.bytes["l-1/c"] = true, 77
	f, _ = uc.OfLab(context.Background(), "g-1", "l-1", "c")
	assert.True(t, f.EgressUsed)
	assert.EqualValues(t, 77, f.VPNBytes)
}

func TestPullAllWithoutAnAgentDoesNothing(t *testing.T) {
	uc := labActivity.New(labActivity.Dependencies{Store: newStore()})
	require.NoError(t, uc.PullGroup(context.Background(), "g-1"))
}
