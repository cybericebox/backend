// Package assistantStatsRepo reads and writes the statistics collection modes (the platform default per source, the mode
// of each event) and reads the per-team facts the aggregates for cyber-brain are made of. The facts carry no team, user
// or address: the team is only the unit that is counted.
package assistantStatsRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

type Queries interface {
	ListAssistantStatsDefaults(context.Context) ([]postgres.ListAssistantStatsDefaultsRow, error)
	UpsertAssistantStatsDefault(context.Context, postgres.UpsertAssistantStatsDefaultParams) (int64, error)
	GetEventAssistantStatsMode(context.Context, uuid.UUID) (int16, error)
	SetEventAssistantStatsMode(context.Context, postgres.SetEventAssistantStatsModeParams) (int64, error)
	ClearEventAssistantStatsMode(context.Context, uuid.UUID) (int64, error)
	ListAssistantStatsEnabledEventTasks(context.Context, postgres.ListAssistantStatsEnabledEventTasksParams) ([]postgres.ListAssistantStatsEnabledEventTasksRow, error)
	ListAssistantStatsTeamFacts(context.Context, postgres.ListAssistantStatsTeamFactsParams) ([]postgres.ListAssistantStatsTeamFactsRow, error)
	ListAssistantStatsTaskVersions(context.Context, []uuid.UUID) ([]postgres.ListAssistantStatsTaskVersionsRow, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// Task is an exercise with the version an event pinned.
type Task struct{ ExerciseID, VersionID uuid.UUID }

// Fact is what one engaged team did with one task. Nothing in it says which team.
type Fact struct {
	Task
	Subtasks, Attempts, SolvedSubtasks int64
	// SolveSeconds is from the first opening to the last solved subtask; HasSolveSeconds is false when the team did not
	// solve every subtask.
	SolveSeconds    float64
	HasSolveSeconds bool
	Hints           int64
}

// Defaults are the platform default modes by source; a source without one is off.
func (r *Repository) Defaults(ctx context.Context) (map[string]assistantModel.StatsMode, error) {
	rows, err := r.q.ListAssistantStatsDefaults(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]assistantModel.StatsMode, len(rows))
	for _, row := range rows {
		out[row.Source] = assistantModel.StatsModeFromStored(row.Mode)
	}
	return out, nil
}

// SetDefault sets the platform default of a source (off, final or continuous).
func (r *Repository) SetDefault(ctx context.Context, source string, mode assistantModel.StatsMode, at time.Time) error {
	_, err := r.q.UpsertAssistantStatsDefault(ctx, postgres.UpsertAssistantStatsDefaultParams{Source: source, Mode: mode.Stored(), UpdatedAt: at})
	return err
}

// EventMode is the event's own mode; default when it has none.
func (r *Repository) EventMode(ctx context.Context, event uuid.UUID) (assistantModel.StatsMode, error) {
	stored, err := r.q.GetEventAssistantStatsMode(ctx, event)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return assistantModel.StatsModeDefault, nil
		}
		return "", err
	}
	return assistantModel.StatsModeFromStored(stored), nil
}

// SetEventMode sets the event's own mode (default removes it). It reports false when there is no such event.
func (r *Repository) SetEventMode(ctx context.Context, event uuid.UUID, mode assistantModel.StatsMode, at time.Time) (bool, error) {
	if mode == assistantModel.StatsModeDefault {
		if _, err := r.q.ClearEventAssistantStatsMode(ctx, event); err != nil {
			return false, err
		}
		return true, nil
	}
	n, err := r.q.SetEventAssistantStatsMode(ctx, postgres.SetEventAssistantStatsModeParams{Mode: mode.Stored(), UpdatedAt: at, EventID: event})
	return n > 0, err
}

// EnabledTasks are the tasks of the events whose collection is on (final or continuous). exercises empty: all of them.
func (r *Repository) EnabledTasks(ctx context.Context, platformDefault assistantModel.StatsMode, exercises []uuid.UUID) ([]Task, error) {
	rows, err := r.q.ListAssistantStatsEnabledEventTasks(ctx, postgres.ListAssistantStatsEnabledEventTasksParams{DefaultMode: platformDefault.Stored(), ExerciseIds: nonNil(exercises)})
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(rows))
	for _, row := range rows {
		out = append(out, Task{ExerciseID: row.ExerciseID, VersionID: row.ExerciseVersionID})
	}
	return out, nil
}

// TeamFacts are the per-team facts of the due events as of a moment, without the teams that have a member in excluded.
func (r *Repository) TeamFacts(ctx context.Context, platformDefault assistantModel.StatsMode, asOf time.Time, excluded, exercises []uuid.UUID) ([]Fact, error) {
	rows, err := r.q.ListAssistantStatsTeamFacts(ctx, postgres.ListAssistantStatsTeamFactsParams{
		AsOf: asOf, DefaultMode: platformDefault.Stored(), ExcludedUsers: nonNil(excluded), ExerciseIds: nonNil(exercises),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Fact, 0, len(rows))
	for _, row := range rows {
		out = append(out, Fact{
			Task: Task{ExerciseID: row.ExerciseID, VersionID: row.ExerciseVersionID}, Subtasks: row.Subtasks, Attempts: row.Attempts,
			SolvedSubtasks: row.SolvedSubtasks, SolveSeconds: row.SolveSeconds.Float64, HasSolveSeconds: row.SolveSeconds.Valid, Hints: row.Hints,
		})
	}
	return out, nil
}

// VersionVariants are the variants JSON of the versions, by version id.
func (r *Repository) VersionVariants(ctx context.Context, versions []uuid.UUID) (map[uuid.UUID][]byte, error) {
	rows, err := r.q.ListAssistantStatsTaskVersions(ctx, nonNil(versions))
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]byte, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Variants
	}
	return out, nil
}

// nonNil keeps an empty list an empty array (a nil slice is NULL to the database, and cardinality(NULL) is NULL).
func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
