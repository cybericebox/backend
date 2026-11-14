package assistant_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/assistantStatsRepo"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
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
	b := &dataBrain{}
	r.fake.Assistant = b
	if err := r.uc.PushStatistics(context.Background()); err != nil || len(b.bodies) != 1 {
		t.Fatalf("push: %v %d", err, len(b.bodies))
	}
	checkKeys(t, "PushData from the events source", []byte(b.bodies[0]))
	var sent struct{ Aggregates []assistantModel.Aggregate }
	if err := json.Unmarshal([]byte(b.bodies[0]), &sent); err != nil || len(sent.Aggregates) != 1 || sent.Aggregates[0].Solves != 1 {
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
