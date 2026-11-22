package assistantStatsRepo

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

type fake struct {
	defaults []postgres.ListAssistantStatsDefaultsRow
	eventErr error
	event    int16
	setRows  int64
	cleared  int
	upsert   postgres.UpsertAssistantStatsDefaultParams
	setEvent postgres.SetEventAssistantStatsModeParams
	enabled  postgres.ListAssistantStatsEnabledEventTasksParams
	facts    postgres.ListAssistantStatsTeamFactsParams
	factRows []postgres.ListAssistantStatsTeamFactsRow
	// refs is the stored mapping of the team refs; inserted counts the rows the repository asked to store.
	refs     []postgres.ListAssistantStatsTeamRefsRow
	inserted []postgres.ListAssistantStatsTeamRefsRow
}

func (f *fake) ListAssistantStatsTeamRefs(_ context.Context, events []uuid.UUID) ([]postgres.ListAssistantStatsTeamRefsRow, error) {
	var out []postgres.ListAssistantStatsTeamRefsRow
	for _, r := range f.refs {
		for _, e := range events {
			if r.EventID == e {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (f *fake) InsertAssistantStatsTeamRefs(_ context.Context, p postgres.InsertAssistantStatsTeamRefsParams) error {
	var rows []struct {
		Event uuid.UUID `json:"event_id"`
		Team  uuid.UUID `json:"team_id"`
		Ref   string    `json:"ref"`
	}
	if err := json.Unmarshal(p.Refs, &rows); err != nil {
		return err
	}
	for _, r := range rows {
		have := false
		for _, s := range f.refs {
			have = have || s.EventID == r.Event && s.TeamID == r.Team
		}
		if !have { // ON CONFLICT DO NOTHING
			row := postgres.ListAssistantStatsTeamRefsRow{EventID: r.Event, TeamID: r.Team, Ref: r.Ref}
			f.refs = append(f.refs, row)
			f.inserted = append(f.inserted, row)
		}
	}
	return nil
}

func (f *fake) ListAssistantStatsDefaults(context.Context) ([]postgres.ListAssistantStatsDefaultsRow, error) {
	return f.defaults, nil
}
func (f *fake) UpsertAssistantStatsDefault(_ context.Context, p postgres.UpsertAssistantStatsDefaultParams) (int64, error) {
	f.upsert = p
	return 1, nil
}
func (f *fake) GetEventAssistantStatsMode(context.Context, uuid.UUID) (int16, error) {
	return f.event, f.eventErr
}
func (f *fake) SetEventAssistantStatsMode(_ context.Context, p postgres.SetEventAssistantStatsModeParams) (int64, error) {
	f.setEvent = p
	return f.setRows, nil
}
func (f *fake) ClearEventAssistantStatsMode(context.Context, uuid.UUID) (int64, error) {
	f.cleared++
	return 1, nil
}
func (f *fake) ListAssistantStatsEnabledEventTasks(_ context.Context, p postgres.ListAssistantStatsEnabledEventTasksParams) ([]postgres.ListAssistantStatsEnabledEventTasksRow, error) {
	f.enabled = p
	return nil, nil
}
func (f *fake) ListAssistantStatsTeamFacts(_ context.Context, p postgres.ListAssistantStatsTeamFactsParams) ([]postgres.ListAssistantStatsTeamFactsRow, error) {
	f.facts = p
	return f.factRows, nil
}
func (f *fake) ListAssistantStatsTaskVersions(context.Context, []uuid.UUID) ([]postgres.ListAssistantStatsTaskVersionsRow, error) {
	return nil, nil
}

func TestModesAreStoredAsNumbersAndDefaultRemovesTheRow(t *testing.T) {
	f := &fake{setRows: 1}
	r := New(f)
	ctx := context.Background()
	event := uuid.FromStringOrNil("eeeeeeee-0000-4000-8000-000000000001")
	if ok, err := r.SetEventMode(ctx, event, assistantModel.StatsModeContinuous, time.Unix(1, 0)); !ok || err != nil || f.setEvent.Mode != 3 {
		t.Fatalf("continuous: %v %v %+v", ok, err, f.setEvent)
	}
	if ok, err := r.SetEventMode(ctx, event, assistantModel.StatsModeDefault, time.Unix(1, 0)); !ok || err != nil || f.cleared != 1 {
		t.Fatalf("default: %v %v cleared=%d", ok, err, f.cleared)
	}
	if err := r.SetDefault(ctx, "events", assistantModel.StatsModeFinal, time.Unix(1, 0)); err != nil || f.upsert.Source != "events" || f.upsert.Mode != 2 {
		t.Fatalf("default of a source: %v %+v", err, f.upsert)
	}
	if ok, _ := New(&fake{}).SetEventMode(ctx, event, assistantModel.StatsModeOff, time.Unix(1, 0)); ok {
		t.Fatal("no such event must report false")
	}
}

func TestEventModeAbsentIsDefault(t *testing.T) {
	ctx := context.Background()
	if m, err := New(&fake{eventErr: pgx.ErrNoRows}).EventMode(ctx, uuid.Nil); err != nil || m != assistantModel.StatsModeDefault {
		t.Fatalf("%q %v", m, err)
	}
	if m, _ := New(&fake{event: 2}).EventMode(ctx, uuid.Nil); m != assistantModel.StatsModeFinal {
		t.Fatalf("%q", m)
	}
}

func TestDefaultsReadAsModesAndAnUnknownValueIsOff(t *testing.T) {
	f := &fake{defaults: []postgres.ListAssistantStatsDefaultsRow{{Source: "events", Mode: 3}, {Source: "academy", Mode: 9}}}
	got, err := New(f).Defaults(context.Background())
	if err != nil || got["events"] != assistantModel.StatsModeContinuous || got["academy"] != assistantModel.StatsModeOff {
		t.Fatalf("%v %v", err, got)
	}
}

func TestFactsMapAndEmptyListsAreEmptyArraysNotNull(t *testing.T) {
	f := &fake{factRows: []postgres.ListAssistantStatsTeamFactsRow{
		{Subtasks: 2, Attempts: 3, SolvedSubtasks: 2, SolveSeconds: 42, HasSolveSeconds: true, Hints: 1},
		{Subtasks: 2, Attempts: 1},
	}}
	got, err := New(f).TeamFacts(context.Background(), assistantModel.StatsModeContinuous, time.Unix(5, 0), nil, nil)
	if err != nil || len(got) != 2 || !got[0].HasSolveSeconds || got[0].SolveSeconds != 42 || got[1].HasSolveSeconds {
		t.Fatalf("%v %+v", err, got)
	}
	if f.facts.ExcludedUsers == nil || f.facts.ExerciseIds == nil || f.facts.DefaultMode != 3 {
		t.Fatalf("a nil list would be NULL in SQL and make the filters NULL: %+v", f.facts)
	}
	if _, err = New(f).EnabledTasks(context.Background(), assistantModel.StatsModeOff, nil); err != nil || f.enabled.ExerciseIds == nil {
		t.Fatalf("%v %+v", err, f.enabled)
	}
}

// A grader's run is never a participant: the statistics read event teams only. The queries must not touch the tables of
// the runs or of the test labs, which is how a run could slip into the figures.
func TestTheStatisticsQueriesNeverReadRunsOrTestLabs(t *testing.T) {
	raw, err := os.ReadFile("../postgres/queries/assistant_stats.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"assistant_runs", "assistant_run_events", "assistant_run_answers", "exercise_test_deployments"} {
		if strings.Contains(string(raw), table) {
			t.Fatalf("the statistics queries read %s", table)
		}
	}
}

// A team's id in the statistics is random per (event, team): the same across the tasks of one event, another in every other
// event, and never derived from the team id.
func TestTeamRefsAreRandomPerEventAndStableWithinOne(t *testing.T) {
	eventOne := uuid.FromStringOrNil("eeeeeeee-0000-4000-8000-000000000001")
	eventTwo := uuid.FromStringOrNil("eeeeeeee-0000-4000-8000-000000000002")
	team := uuid.FromStringOrNil("77777777-0000-4000-8000-000000000001")
	ex1 := uuid.FromStringOrNil("aaaaaaaa-0000-4000-8000-000000000001")
	ex2 := uuid.FromStringOrNil("aaaaaaaa-0000-4000-8000-000000000002")
	rows := []postgres.ListAssistantStatsTeamFactsRow{
		{EventID: eventOne, TeamID: team, EventExerciseID: ex1},
		{EventID: eventOne, TeamID: team, EventExerciseID: ex2},
		// the same team id under another event (a stand-in: a real team belongs to one event)
		{EventID: eventTwo, TeamID: team, EventExerciseID: ex1},
	}
	f := &fake{factRows: rows}
	r := New(f)
	n := 0
	r.NewRef = func() string { n++; return strings.Repeat(string(rune('a'+n)), 4) }
	got, err := r.TeamFacts(context.Background(), assistantModel.StatsModeContinuous, time.Unix(5, 0), nil, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].TeamRef == "" || got[0].TeamRef != got[1].TeamRef {
		t.Fatalf("one team, one event: the same ref across its tasks: %q %q", got[0].TeamRef, got[1].TeamRef)
	}
	if got[2].TeamRef == "" || got[2].TeamRef == got[0].TeamRef {
		t.Fatalf("two events must give different refs for the same team: %q %q", got[0].TeamRef, got[2].TeamRef)
	}
	if len(f.inserted) != 2 {
		t.Fatalf("one stored mapping per (event, team): %+v", f.inserted)
	}
	// a later push reads the stored mapping back and makes nothing new
	again, err := r.TeamFacts(context.Background(), assistantModel.StatsModeContinuous, time.Unix(6, 0), nil, nil)
	if err != nil || again[0].TeamRef != got[0].TeamRef || again[2].TeamRef != got[2].TeamRef || len(f.inserted) != 2 {
		t.Fatalf("stable across pushes: %v %+v inserted=%d", err, again, len(f.inserted))
	}
	for _, g := range got {
		if strings.Contains(g.TeamRef, team.String()) || strings.Contains(g.TeamRef, strings.ReplaceAll(team.String(), "-", "")) {
			t.Fatalf("a ref must not be derived from the team id: %q", g.TeamRef)
		}
	}
}

func TestTheDefaultRefIsRandomAndLongEnough(t *testing.T) {
	a, b := randomRef(), randomRef()
	if a == b || len(a) != 32 {
		t.Fatalf("%q %q", a, b)
	}
}

func TestFactsCarryLabStateAndWrongFigures(t *testing.T) {
	f := &fake{factRows: []postgres.ListAssistantStatsTeamFactsRow{
		{Subtasks: 2, WrongSubmissions: 3, HasLab: true, ReadySeconds: 40, HasReadySeconds: true, Restarts: 1, Broken: true,
			StateChecks: []byte(`[{"subtask_ref":"s1","time_to_pass_s":90.5},{"subtask_ref":"s2","time_to_pass_s":null}]`)},
		{Subtasks: 1, StateChecks: []byte(`[]`)},
	}}
	got, err := New(f).TeamFacts(context.Background(), assistantModel.StatsModeContinuous, time.Unix(5, 0), nil, nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	a := got[0]
	if a.WrongSubmissions != 3 || a.Lab == nil || !a.Lab.HasReady || a.Lab.ReadySeconds != 40 || a.Lab.Restarts != 1 || !a.Lab.Broken {
		t.Fatalf("%+v", a)
	}
	if len(a.StateChecks) != 2 || !a.StateChecks[0].Passed || a.StateChecks[0].PassSeconds != 90.5 || a.StateChecks[1].Passed {
		t.Fatalf("%+v", a.StateChecks)
	}
	if got[1].Lab != nil || len(got[1].StateChecks) != 0 {
		t.Fatalf("a task without a lab has no lab facts: %+v", got[1])
	}
}
