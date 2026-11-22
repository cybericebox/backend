package assistant_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/assistantStatsRepo"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/useCase/assistant"
)

var (
	exA   = uuid.FromStringOrNil("aaaaaaaa-0000-4000-8000-000000000001")
	exB   = uuid.FromStringOrNil("aaaaaaaa-0000-4000-8000-000000000002")
	verA1 = uuid.FromStringOrNil("bbbbbbbb-0000-4000-8000-000000000001")
	verB1 = uuid.FromStringOrNil("bbbbbbbb-0000-4000-8000-000000000002")
	asOf  = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
)

func fact(ex, ver uuid.UUID, subtasks, attempts, solved int64, seconds *float64, hints int64) assistantStatsRepo.Fact {
	f := assistantStatsRepo.Fact{Task: assistantStatsRepo.Task{ExerciseID: ex, VersionID: ver}, Subtasks: subtasks, Attempts: attempts, SolvedSubtasks: solved, Hints: hints}
	if seconds != nil {
		f.SolveSeconds, f.HasSolveSeconds = *seconds, true
	}
	return f
}

func secs(v float64) *float64 { return &v }

const dynamicVariants = `[{"topology":{"devices":[{"image":"reg/b:1"},{"image":"reg/a:2"},{"image":""}]}},{"topology":{"devices":[{"image":"reg/a:2"}]}}]`
const reorderedVariants = `[{"topology":{"devices":[{"image":"reg/a:2"}]}},{"topology":{"devices":[{"image":" reg/b:1 "}]}}]`

func TestAggregatesCountSolvesRateTimesAndHints(t *testing.T) {
	facts := []assistantStatsRepo.Fact{
		fact(exA, verA1, 2, 5, 2, secs(100), 0),  // solved, fast
		fact(exA, verA1, 2, 9, 2, secs(300), 1),  // solved
		fact(exA, verA1, 2, 7, 2, secs(500), 1),  // solved
		fact(exA, verA1, 2, 11, 2, secs(900), 3), // solved, slow
		fact(exA, verA1, 2, 4, 1, nil, 0),        // tried, did not finish
		fact(exA, verA1, 2, 0, 0, nil, 0),        // only opened it
	}
	got := assistant.BuildAggregates(facts, map[uuid.UUID][]byte{verA1: []byte(dynamicVariants)}, asOf)
	if len(got) != 1 {
		t.Fatalf("aggregates: %+v", got)
	}
	a := got[0]
	if a.TaskRef.TaskRef != exA.String() || a.Version != verA1.String() || a.TaskKind != "dynamic" || !a.AsOf.Equal(asOf) {
		t.Fatalf("identity: %+v", a)
	}
	if a.Solves != 4 || a.Attempts != 36 {
		t.Fatalf("solves=%d attempts=%d", a.Solves, a.Attempts)
	}
	// 4 solves of 6 teams that engaged: the teams that only opened the task are in the denominator.
	if want := 4.0 / 6.0; a.SolveRate != want {
		t.Fatalf("solve_rate = %v, want %v", a.SolveRate, want)
	}
	// Times of the solving teams 100, 300, 500, 900: percentile_cont is linear between neighbours.
	if a.Time.Median != 400 || a.Time.P25 != 250 || a.Time.P75 != 600 {
		t.Fatalf("time = %+v", a.Time)
	}
	want := []assistantModel.HintBucket{{Hints: 0, Teams: 3}, {Hints: 1, Teams: 2}, {Hints: 3, Teams: 1}}
	if len(a.HintsUsed) != len(want) {
		t.Fatalf("hints = %+v", a.HintsUsed)
	}
	for i := range want {
		if a.HintsUsed[i] != want[i] {
			t.Fatalf("hints = %+v", a.HintsUsed)
		}
	}
}

func TestATaskNobodySolvedHasZeroTimesAndRate(t *testing.T) {
	got := assistant.BuildAggregates([]assistantStatsRepo.Fact{fact(exB, verB1, 1, 3, 0, nil, 0)}, nil, asOf)
	if len(got) != 1 || got[0].Solves != 0 || got[0].SolveRate != 0 || got[0].Time != (assistantModel.TimeStats{}) || got[0].TaskKind != "static" || got[0].Image != "" {
		t.Fatalf("%+v", got)
	}
}

func TestTheImageIsAFingerprintOfTheSortedDistinctReferences(t *testing.T) {
	one := assistant.BuildAggregates([]assistantStatsRepo.Fact{fact(exA, verA1, 1, 1, 1, secs(1), 0)}, map[uuid.UUID][]byte{verA1: []byte(dynamicVariants)}, asOf)
	two := assistant.BuildAggregates([]assistantStatsRepo.Fact{fact(exA, verA1, 1, 1, 1, secs(1), 0)}, map[uuid.UUID][]byte{verA1: []byte(reorderedVariants)}, asOf)
	if one[0].Image == "" || len(one[0].Image) != 64 || one[0].Image != two[0].Image {
		t.Fatalf("the fingerprint does not depend on order, repeats or blanks: %q %q", one[0].Image, two[0].Image)
	}
	other := assistant.BuildAggregates([]assistantStatsRepo.Fact{fact(exA, verA1, 1, 1, 1, secs(1), 0)}, map[uuid.UUID][]byte{verA1: []byte(`[{"topology":{"devices":[{"image":"reg/c:9"}]}}]`)}, asOf)
	if other[0].Image == one[0].Image {
		t.Fatal("another image set must be another fingerprint")
	}
}

func TestVersionsOfOneTaskAreReportedApart(t *testing.T) {
	v2 := uuid.FromStringOrNil("bbbbbbbb-0000-4000-8000-0000000000ff")
	got := assistant.BuildAggregates([]assistantStatsRepo.Fact{fact(exA, verA1, 1, 1, 1, secs(1), 0), fact(exA, v2, 1, 1, 0, nil, 0)}, nil, asOf)
	if len(got) != 2 || got[0].Version == got[1].Version {
		t.Fatalf("%+v", got)
	}
}

// ── the events source ──────────────────────────────────────────────────────

type fakeEventStore struct {
	defaults map[string]assistantModel.StatsMode
	enabled  []assistantStatsRepo.Task
	facts    []assistantStatsRepo.Fact
	variants map[uuid.UUID][]byte

	gotDefault   assistantModel.StatsMode
	gotExcluded  []uuid.UUID
	gotExercises []uuid.UUID
}

func (s *fakeEventStore) Defaults(context.Context) (map[string]assistantModel.StatsMode, error) {
	return s.defaults, nil
}

func (s *fakeEventStore) EnabledTasks(_ context.Context, d assistantModel.StatsMode, ex []uuid.UUID) ([]assistantStatsRepo.Task, error) {
	s.gotDefault, s.gotExercises = d, ex
	return s.enabled, nil
}

func (s *fakeEventStore) TeamFacts(_ context.Context, d assistantModel.StatsMode, _ time.Time, excluded, ex []uuid.UUID) ([]assistantStatsRepo.Fact, error) {
	s.gotDefault, s.gotExcluded, s.gotExercises = d, excluded, ex
	return s.facts, nil
}

func (s *fakeEventStore) VersionVariants(context.Context, []uuid.UUID) (map[uuid.UUID][]byte, error) {
	return s.variants, nil
}

type refusers struct{ ids []uuid.UUID }

func (r refusers) Excluded(context.Context) ([]uuid.UUID, error) { return r.ids, nil }

func TestTheEventsSourcePassesThePlatformDefaultAndTheParticipantFilterToTheQuery(t *testing.T) {
	refuser := uuid.FromStringOrNil("99999999-0000-4000-8000-000000000001")
	store := &fakeEventStore{defaults: map[string]assistantModel.StatsMode{"events": assistantModel.StatsModeContinuous}, facts: []assistantStatsRepo.Fact{fact(exA, verA1, 1, 2, 1, secs(10), 0)}}
	src := assistant.EventsSource{Store: store, Exclusion: refusers{ids: []uuid.UUID{refuser}}}
	got, err := src.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	if store.gotDefault != assistantModel.StatsModeContinuous {
		t.Fatalf("the events default reached the query: %q", store.gotDefault)
	}
	if len(store.gotExcluded) != 1 || store.gotExcluded[0] != refuser {
		t.Fatalf("the participant filter reached the query: %v", store.gotExcluded)
	}
	// With no filter set (today) nobody is excluded.
	none := assistant.EventsSource{Store: store}
	if _, err = none.Aggregates(context.Background(), nil, asOf); err != nil || len(store.gotExcluded) != 0 {
		t.Fatalf("no filter: %v %v", err, store.gotExcluded)
	}
	if _, err = (assistant.EventsSource{Store: store, Exclusion: assistant.NoExclusion{}}).Aggregates(context.Background(), nil, asOf); err != nil || len(store.gotExcluded) != 0 {
		t.Fatalf("no-op filter: %v %v", err, store.gotExcluded)
	}
}

func TestWithoutADefaultTheEventsSourceIsOff(t *testing.T) {
	store := &fakeEventStore{}
	if _, err := (assistant.EventsSource{Store: store}).Aggregates(context.Background(), nil, asOf); err != nil {
		t.Fatal(err)
	}
	if store.gotDefault != assistantModel.StatsModeOff {
		t.Fatalf("default = %q", store.gotDefault)
	}
}

func TestTheEventsSourceReportsOnlyTheRequestedRefs(t *testing.T) {
	store := &fakeEventStore{facts: []assistantStatsRepo.Fact{fact(exA, verA1, 1, 1, 1, secs(1), 0), fact(exB, verB1, 1, 1, 1, secs(1), 0)}}
	src := assistant.EventsSource{Store: store}
	got, err := src.Aggregates(context.Background(), []assistantModel.TaskRef{{TaskRef: exB.String(), Version: verB1.String(), Image: ""}}, asOf)
	if err != nil || len(got) != 1 || got[0].TaskRef.TaskRef != exB.String() {
		t.Fatalf("%v %+v", err, got)
	}
	if len(store.gotExercises) != 1 || store.gotExercises[0] != exB {
		t.Fatalf("only that exercise was asked for: %v", store.gotExercises)
	}
	// A ref that is not a uuid is nothing to look up.
	if got, err = src.Aggregates(context.Background(), []assistantModel.TaskRef{{TaskRef: "not-a-uuid"}}, asOf); err != nil || len(got) != 0 {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestCollectionOnFollowsTheEnabledTasksOfTheEvents(t *testing.T) {
	store := &fakeEventStore{enabled: []assistantStatsRepo.Task{{ExerciseID: exA, VersionID: verA1}}}
	src := assistant.EventsSource{Store: store}
	refA := assistantModel.TaskRef{TaskRef: exA.String(), Version: verA1.String()}
	refOtherVersion := assistantModel.TaskRef{TaskRef: exA.String(), Version: verB1.String()}
	refB := assistantModel.TaskRef{TaskRef: exB.String(), Version: verB1.String()}
	bad := assistantModel.TaskRef{TaskRef: "x", Version: "y"}
	on, err := src.CollectionOn(context.Background(), []assistantModel.TaskRef{refA, refOtherVersion, refB, bad})
	if err != nil || !on[refA] || on[refOtherVersion] || on[refB] || on[bad] {
		t.Fatalf("%v %v", err, on)
	}
}

func TestMultiSourceCombinesSourcesAndKeepsTheirNames(t *testing.T) {
	a := &fakeStats{on: map[assistantModel.TaskRef]bool{refA: true}, all: []assistantModel.Aggregate{aggregate(refA)}}
	b := &fakeStats{on: map[assistantModel.TaskRef]bool{refB: true}, all: []assistantModel.Aggregate{aggregate(refB)}}
	ms := assistant.MultiSource{a, b}
	all, err := ms.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(all) != 2 {
		t.Fatalf("%v %v", err, all)
	}
	on, _ := ms.CollectionOn(context.Background(), []assistantModel.TaskRef{refA, refB})
	if !on[refA] || !on[refB] {
		t.Fatalf("%v", on)
	}
	if got := ms.Names(); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// What the events source builds, sent end to end, carries aggregate fields only.
func TestAnEventsSourcePushCarriesOnlyAggregateFields(t *testing.T) {
	store := &fakeEventStore{
		defaults: map[string]assistantModel.StatsMode{"events": assistantModel.StatsModeContinuous},
		facts:    []assistantStatsRepo.Fact{fact(exA, verA1, 2, 5, 2, secs(120), 1), fact(exA, verA1, 2, 1, 0, nil, 0)},
		variants: map[uuid.UUID][]byte{verA1: []byte(dynamicVariants)},
	}
	stats := assistant.MultiSource{assistant.EventsSource{Store: store, Exclusion: assistant.NoExclusion{}}}
	r := newRig(t, func(d *assistant.Dependencies) { d.Stats = stats })
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := &dataBrain{fake: r.fake}
	if err := r.uc.PushStatistics(context.Background()); err != nil || len(b.bodies()) != 1 {
		t.Fatalf("push: %v %d", err, len(b.bodies()))
	}
	checkKeys(t, "PushData from the events source", []byte(b.bodies()[0]))
	var sent struct{ Aggregates []assistantModel.Aggregate }
	if err := json.Unmarshal([]byte(b.bodies()[0]), &sent); err != nil || len(sent.Aggregates) != 1 || sent.Aggregates[0].Solves != 1 {
		t.Fatalf("%v %+v", err, sent)
	}
}

func TestEveryAggregateNamesItsSourceAndSourcesAreNotMerged(t *testing.T) {
	store := &fakeEventStore{facts: []assistantStatsRepo.Fact{fact(exA, verA1, 1, 1, 1, secs(1), 0)}}
	events := assistant.EventsSource{Store: store}
	got, err := events.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(got) != 1 || got[0].Source != "events" {
		t.Fatalf("%v %+v", err, got)
	}

	// A second source with the same task: the brain gets one entry per source.
	academyAggregate := aggregate(refA)
	academyAggregate.Source = "academy"
	academy := &namedStats{name: "academy", all: []assistantModel.Aggregate{academyAggregate}}
	all, err := assistant.MultiSource{events, academy}.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(all) != 2 {
		t.Fatalf("%v %+v", err, all)
	}
	sources := map[string]bool{all[0].Source: true, all[1].Source: true}
	if !sources["events"] || !sources["academy"] {
		t.Fatalf("one entry per source: %+v", all)
	}
	raw, _ := json.Marshal(all[0])
	if !strings.Contains(string(raw), `"source":`) {
		t.Fatalf("the source is in the payload: %s", raw)
	}
}

type namedStats struct {
	name string
	all  []assistantModel.Aggregate
}

func (n *namedStats) Name() string { return n.name }
func (n *namedStats) Aggregates(context.Context, []assistantModel.TaskRef, time.Time) ([]assistantModel.Aggregate, error) {
	return n.all, nil
}
func (n *namedStats) CollectionOn(context.Context, []assistantModel.TaskRef) (map[assistantModel.TaskRef]bool, error) {
	return nil, nil
}

// ── the lab-side signals and the per-team records ───────────────────────────

const egressVariants = `[{"topology":{"devices":[{"type":"container","image":"reg/a:1"},{"type":"internet_gateway"}]}}]`

func team(ex, ver uuid.UUID, ref string, subtasks, solved, wrong, hints int64, seconds *float64) assistantStatsRepo.Fact {
	f := fact(ex, ver, subtasks, wrong+solved, solved, seconds, hints)
	f.WrongSubmissions, f.TeamRef = wrong, ref
	return f
}

func withLab(f assistantStatsRepo.Fact, ready float64, hasReady bool, restarts int64, broken bool) assistantStatsRepo.Fact {
	f.Lab = &assistantStatsRepo.LabFact{ReadySeconds: ready, HasReady: hasReady, Restarts: restarts, Broken: broken}
	return f
}

func TestTheWrongSubmissionsAreSplitBetweenSolversAndNonsolvers(t *testing.T) {
	facts := []assistantStatsRepo.Fact{
		team(exA, verA1, "r1", 1, 1, 0, 0, secs(10)),
		team(exA, verA1, "r2", 1, 1, 2, 0, secs(20)),
		team(exA, verA1, "r3", 1, 1, 4, 0, secs(30)),
		team(exA, verA1, "r4", 1, 0, 10, 0, nil),
		team(exA, verA1, "r5", 1, 0, 0, 0, nil),
	}
	a := assistant.BuildAggregates(facts, nil, asOf)[0]
	if a.TeamsEngaged != 5 {
		t.Fatalf("teams_engaged = %d", a.TeamsEngaged)
	}
	if got := a.WrongSubmissionsPerTeam; got == nil || *got != (assistantModel.Distribution{P25: 1, Median: 2, P75: 3}) {
		t.Fatalf("solvers: %+v", got)
	}
	if got := a.WrongSubmissionsPerNonsolver; got == nil || *got != (assistantModel.Distribution{P25: 2.5, Median: 5, P75: 7.5}) {
		t.Fatalf("non-solvers: %+v", got)
	}
	none := assistant.BuildAggregates(facts[3:], nil, asOf)[0]
	if none.WrongSubmissionsPerTeam != nil {
		t.Fatalf("nobody solved: there is no per-solver figure: %+v", none.WrongSubmissionsPerTeam)
	}
}

func TestStateChecksGiveTheTimeFromLabReadyToPassAndAStateOnlyTaskHasNoWrongFigures(t *testing.T) {
	state := func(ref string, passed ...*float64) assistantStatsRepo.Fact {
		f := team(exA, verA1, ref, int64(len(passed)), 0, 0, 0, nil)
		for i, p := range passed {
			c := assistantStatsRepo.StateCheckFact{SubtaskRef: []string{"s1", "s2"}[i]}
			if p != nil {
				c.Passed, c.PassSeconds = true, *p
				f.SolvedSubtasks++
			}
			f.StateChecks = append(f.StateChecks, c)
		}
		return f
	}
	facts := []assistantStatsRepo.Fact{state("a", secs(100), secs(400)), state("b", secs(300), nil), state("c", nil, nil), state("d", secs(500), secs(800))}
	a := assistant.BuildAggregates(facts, nil, asOf)[0]
	if len(a.StateChecks) != 2 || a.StateChecks[0].SubtaskRef != "s1" || a.StateChecks[1].SubtaskRef != "s2" {
		t.Fatalf("%+v", a.StateChecks)
	}
	if s1 := a.StateChecks[0]; s1.TeamsPassed != 3 || s1.TimeToPassS != (assistantModel.Distribution{P25: 200, Median: 300, P75: 400}) {
		t.Fatalf("s1: %+v", s1)
	}
	if s2 := a.StateChecks[1]; s2.TeamsPassed != 2 {
		t.Fatalf("s2: %+v", s2)
	}
	if a.WrongSubmissionsPerTeam != nil || a.WrongSubmissionsPerNonsolver != nil {
		t.Fatalf("no answer is submitted for a state-only task: %+v %+v", a.WrongSubmissionsPerTeam, a.WrongSubmissionsPerNonsolver)
	}
	if a.Solves != 2 {
		t.Fatalf("the two teams that passed both solved it: %d", a.Solves)
	}
	plain := assistant.BuildAggregates([]assistantStatsRepo.Fact{team(exB, verB1, "x", 1, 1, 0, 0, secs(5))}, nil, asOf)[0]
	if plain.StateChecks != nil {
		t.Fatalf("a task without state checks omits them: %+v", plain.StateChecks)
	}
}

func TestLabHealthAndEgressEnabledComeFromTheLabsAndTheTopology(t *testing.T) {
	facts := []assistantStatsRepo.Fact{
		withLab(team(exA, verA1, "a", 1, 1, 0, 0, secs(1)), 30, true, 0, false),
		withLab(team(exA, verA1, "b", 1, 0, 0, 0, nil), 50, true, 2, false),
		withLab(team(exA, verA1, "c", 1, 0, 0, 0, nil), 70, true, 1, true),
		withLab(team(exA, verA1, "d", 1, 0, 0, 0, nil), 0, false, 1, true),
	}
	a := assistant.BuildAggregates(facts, map[uuid.UUID][]byte{verA1: []byte(egressVariants)}, asOf)[0]
	if !a.EgressEnabled || a.TaskKind != "dynamic" {
		t.Fatalf("an Internet gateway in the topology: %+v", a)
	}
	h := a.LabHealth
	if h == nil || h.RestartsPerTeam != 1 || h.BrokenShare != 0.5 || h.ReadyTimeS == nil || *h.ReadyTimeS != (assistantModel.Distribution{P25: 40, Median: 50, P75: 60}) {
		t.Fatalf("%+v", h)
	}
	static := assistant.BuildAggregates([]assistantStatsRepo.Fact{team(exB, verB1, "z", 1, 1, 0, 0, secs(1))}, nil, asOf)[0]
	if static.EgressEnabled || static.LabHealth != nil {
		t.Fatalf("a task without a lab or a gateway: %+v", static)
	}
	noGateway := assistant.BuildAggregates(facts[:1], map[uuid.UUID][]byte{verA1: []byte(dynamicVariants)}, asOf)[0]
	if noGateway.EgressEnabled {
		t.Fatal("egress is off without an Internet gateway")
	}
}

type fixedActivity map[assistantStatsRepo.InstanceKey]assistantStatsRepo.Activity

func (f fixedActivity) Activity(_ context.Context, keys []assistantStatsRepo.InstanceKey) (map[assistantStatsRepo.InstanceKey]assistantStatsRepo.Activity, error) {
	out := map[assistantStatsRepo.InstanceKey]assistantStatsRepo.Activity{}
	for _, k := range keys {
		if a, ok := f[k]; ok {
			out[k] = a
		}
	}
	return out, nil
}

// The laboratory's activity figures are null until a source reports them, and then they are distributions over the teams
// that were reported (an instance without a report stays out).
func TestTheActivityFiguresAreNullWithoutASourceAndFilledFromTheSeam(t *testing.T) {
	keyOf := func(n byte) assistantStatsRepo.InstanceKey {
		return assistantStatsRepo.InstanceKey{EventID: exA, TeamID: uuid.FromBytesOrNil([]byte{n, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1})}
	}
	f1, f2, f3 := team(exA, verA1, "a", 1, 1, 0, 0, secs(1)), team(exA, verA1, "b", 1, 0, 0, 0, nil), team(exA, verA1, "c", 1, 0, 0, 0, nil)
	f1.Instance, f2.Instance, f3.Instance = keyOf(1), keyOf(2), keyOf(3)
	store := &fakeEventStore{facts: []assistantStatsRepo.Fact{f1, f2, f3}}

	plain, err := assistant.EventsSource{Store: store}.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(plain) != 1 {
		t.Fatalf("%v %+v", err, plain)
	}
	raw, _ := json.Marshal(plain[0])
	for _, key := range []string{"egress_share", "vpn_bytes_per_team", "devices_touched_per_team", "console_minutes_per_team"} {
		if !strings.Contains(string(raw), `"`+key+`":null`) {
			t.Fatalf("%s must be null while nothing reports it: %s", key, raw)
		}
	}
	if strings.Contains(string(raw), "vpn_bytes\"") || strings.Contains(string(raw), `"egress_used"`) {
		t.Fatalf("a record without a report carries no effort facts: %s", raw)
	}

	yes, no := true, false
	src := assistant.EventsSource{Store: store, Activity: fixedActivity{
		keyOf(1): {EgressUsed: &yes, VPNBytes: secs(1000), DevicesTouched: secs(2), ConsoleMinutes: secs(10)},
		keyOf(2): {EgressUsed: &no, VPNBytes: secs(3000), DevicesTouched: secs(4)},
	}}
	got, err := src.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	a := got[0]
	if a.EgressShare == nil || *a.EgressShare != 0.5 {
		t.Fatalf("one of the two reported teams used egress: %v", a.EgressShare)
	}
	if a.VPNBytesPerTeam == nil || *a.VPNBytesPerTeam != (assistantModel.Distribution{P25: 1500, Median: 2000, P75: 2500}) {
		t.Fatalf("%+v", a.VPNBytesPerTeam)
	}
	if a.ConsoleMinutesPerTeam == nil || a.ConsoleMinutesPerTeam.Median != 10 {
		t.Fatalf("%+v", a.ConsoleMinutesPerTeam)
	}
	reported := 0
	for _, r := range a.TeamRecords {
		if r.VPNBytes != nil {
			reported++
		}
	}
	if reported != 2 {
		t.Fatalf("the effort facts are on the records of the reported teams only: %d", reported)
	}
}

func TestTeamRecordsAreOnePerTeamShuffledAndCarryOnlyTheirOwnFacts(t *testing.T) {
	facts := []assistantStatsRepo.Fact{
		team(exA, verA1, "ref-1", 1, 1, 2, 1, secs(10)),
		team(exA, verA1, "ref-2", 1, 0, 5, 0, nil),
		team(exA, verA1, "ref-3", 1, 1, 0, 2, secs(30)),
	}
	reverse := func(n int, swap func(i, j int)) {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			swap(i, j)
		}
	}
	a := assistant.BuildAggregatesWith(facts, nil, asOf, reverse)[0]
	if len(a.TeamRecords) != 3 || a.TeamRecords[0].TeamRef != "ref-3" || a.TeamRecords[2].TeamRef != "ref-1" {
		t.Fatalf("the records are put through the shuffle: %+v", a.TeamRecords)
	}
	r := a.TeamRecords[2]
	if !r.Solved || r.SolveTimeS == nil || *r.SolveTimeS != 10 || r.WrongSubmissions != 2 || r.HintsUsed != 1 || r.StateChecks == nil {
		t.Fatalf("%+v", r)
	}
	if lost := a.TeamRecords[1]; lost.Solved || lost.SolveTimeS != nil || lost.WrongSubmissions != 5 {
		t.Fatalf("%+v", lost)
	}
	// without the injected shuffle the order is random, but every record is still there
	for range 3 {
		seen := map[string]bool{}
		for _, rec := range assistant.BuildAggregates(facts, nil, asOf)[0].TeamRecords {
			seen[rec.TeamRef] = true
		}
		if len(seen) != 3 {
			t.Fatalf("%v", seen)
		}
	}
}

var uuidShape = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// Nothing that names a user, a team or an address leaves in the aggregate: the keys are the aggregate figures and the record
// fields, the only identifier is the random team_ref, and no id of the platform (the keys of the instance) is anywhere.
func TestTheAggregateWithTeamRecordsNamesNoUserTeamOrAddress(t *testing.T) {
	teamID := uuid.FromStringOrNil("77777777-0000-4000-8000-000000000042")
	event := uuid.FromStringOrNil("eeeeeeee-0000-4000-8000-000000000042")
	exEvent := uuid.FromStringOrNil("dddddddd-0000-4000-8000-000000000042")
	f := withLab(team(exA, verA1, "9f3c2b7a1d4e4c0a8b6d5e7f01234567", 1, 1, 3, 1, secs(77)), 20, true, 1, false)
	f.Instance = assistantStatsRepo.InstanceKey{EventID: event, TeamID: teamID, EventExerciseID: exEvent}
	store := &fakeEventStore{facts: []assistantStatsRepo.Fact{f}, variants: map[uuid.UUID][]byte{verA1: []byte(egressVariants)}}
	got, err := assistant.EventsSource{Store: store}.Aggregates(context.Background(), nil, asOf)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	raw, _ := json.Marshal(got)
	checkKeys(t, "the aggregate with its team records", raw)
	for _, id := range []uuid.UUID{teamID, event, exEvent} {
		if strings.Contains(string(raw), id.String()) {
			t.Fatalf("the platform id %s is in the payload: %s", id, raw)
		}
	}
	// the only uuids are the task, its version (the brain's own keys); the record has none besides the random ref
	for _, found := range uuidShape.FindAllString(string(raw), -1) {
		if found != exA.String() && found != verA1.String() {
			t.Fatalf("an unexpected id %s: %s", found, raw)
		}
	}
	var out []struct {
		TeamRecords []struct {
			TeamRef string `json:"team_ref"`
		} `json:"team_records"`
	}
	if err = json.Unmarshal(raw, &out); err != nil || len(out[0].TeamRecords) != 1 || out[0].TeamRecords[0].TeamRef != "9f3c2b7a1d4e4c0a8b6d5e7f01234567" {
		t.Fatalf("%v %s", err, raw)
	}
}
