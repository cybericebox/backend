package assistantStatsRepo

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
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
