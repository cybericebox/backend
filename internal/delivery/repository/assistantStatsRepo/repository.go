// Package assistantStatsRepo reads and writes the statistics collection modes (the platform default per source, the mode
// of each event) and reads the per-team facts the aggregates for cyber-brain are made of. The facts carry no team, user
// or address: the team is only the unit that is counted.
package assistantStatsRepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
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
	ListAssistantStatsTeamRefs(context.Context, []uuid.UUID) ([]postgres.ListAssistantStatsTeamRefsRow, error)
	InsertAssistantStatsTeamRefs(context.Context, postgres.InsertAssistantStatsTeamRefsParams) error
}

type Repository struct {
	q Queries
	// NewRef makes the random id of a team in the statistics of an event; Now stamps the stored mapping.
	NewRef func() string
	Now    func() time.Time
}

func New(q Queries) *Repository { return &Repository{q: q, NewRef: randomRef, Now: time.Now} }

// randomRef is 128 random bits: it is not derived from anything, so it cannot be traced back to a team or linked to another
// event's id.
func randomRef() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("no randomness for a team ref: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// Task is an exercise with the version an event pinned.
type Task struct{ ExerciseID, VersionID uuid.UUID }

// InstanceKey names one team's run of one task in the platform: what the laboratory's activity reports are asked by. It
// stays inside the daemon: the aggregates are built without it.
type InstanceKey struct{ EventID, TeamID, EventExerciseID uuid.UUID }

// LabFact is how the lab of one team-instance went. HasReady is false while the lab never came up.
type LabFact struct {
	ReadySeconds float64
	HasReady     bool
	Restarts     int64
	Broken       bool
}

// StateCheckFact is one state-checked subtask of a team: Passed says whether it passed, PassSeconds is from the lab being
// ready to the pass.
type StateCheckFact struct {
	SubtaskRef  string
	Passed      bool
	PassSeconds float64
}

// Activity is what the laboratory's activity reports say of one team-instance. A nil figure is not reported (an older
// laboratory, or the reports are off); it is never a measured zero.
type Activity struct {
	EgressUsed                               *bool
	VPNBytes, DevicesTouched, ConsoleMinutes *float64
}

// Fact is what one engaged team did with one task. It names the team only by TeamRef, a random id of the team in its
// event; Instance is for the activity source and is never copied into an aggregate.
type Fact struct {
	Task
	Subtasks, Attempts, SolvedSubtasks int64
	// SolveSeconds is from the first opening to the last solved subtask; HasSolveSeconds is false when the team did not
	// solve every subtask.
	SolveSeconds    float64
	HasSolveSeconds bool
	Hints           int64
	// WrongSubmissions are the team's wrong answers (those before its solve, when it solved the task).
	WrongSubmissions int64
	// Lab is nil for a task without a lab.
	Lab         *LabFact
	StateChecks []StateCheckFact
	TeamRef     string
	Instance    InstanceKey
	// Activity is filled by the statistics source from the laboratory's activity reports; nil: nothing reported.
	Activity *Activity
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
	refs, err := r.teamRefs(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]Fact, 0, len(rows))
	for _, row := range rows {
		fact := Fact{
			Task: Task{ExerciseID: row.ExerciseID, VersionID: row.ExerciseVersionID}, Subtasks: row.Subtasks, Attempts: row.Attempts,
			SolvedSubtasks: row.SolvedSubtasks, SolveSeconds: row.SolveSeconds, HasSolveSeconds: row.HasSolveSeconds, Hints: row.Hints,
			WrongSubmissions: row.WrongSubmissions,
			TeamRef:          refs[teamKey{row.EventID, row.TeamID}],
			Instance:         InstanceKey{EventID: row.EventID, TeamID: row.TeamID, EventExerciseID: row.EventExerciseID},
		}
		if row.HasLab {
			fact.Lab = &LabFact{ReadySeconds: row.ReadySeconds, HasReady: row.HasReadySeconds, Restarts: row.Restarts, Broken: row.Broken}
		}
		var checks []struct {
			SubtaskRef string   `json:"subtask_ref"`
			TimeToPass *float64 `json:"time_to_pass_s"`
		}
		if len(row.StateChecks) > 0 {
			if err = json.Unmarshal(row.StateChecks, &checks); err != nil {
				return nil, fmt.Errorf("read the state checks: %w", err)
			}
		}
		for _, c := range checks {
			sc := StateCheckFact{SubtaskRef: c.SubtaskRef}
			if c.TimeToPass != nil {
				sc.Passed, sc.PassSeconds = true, *c.TimeToPass
			}
			fact.StateChecks = append(fact.StateChecks, sc)
		}
		out = append(out, fact)
	}
	return out, nil
}

type teamKey struct{ event, team uuid.UUID }

// teamRefs gives every (event, team) of the rows its random id: the one stored, or a new one that is stored now so it stays
// the same in every later push. Two writers racing keep the first stored id (the second reads it back).
func (r *Repository) teamRefs(ctx context.Context, rows []postgres.ListAssistantStatsTeamFactsRow) (map[teamKey]string, error) {
	wanted := map[teamKey]bool{}
	eventSet := map[uuid.UUID]bool{}
	var events []uuid.UUID
	for _, row := range rows {
		wanted[teamKey{row.EventID, row.TeamID}] = true
		if !eventSet[row.EventID] {
			eventSet[row.EventID] = true
			events = append(events, row.EventID)
		}
	}
	out := make(map[teamKey]string, len(wanted))
	if len(wanted) == 0 {
		return out, nil
	}
	load := func() error {
		stored, err := r.q.ListAssistantStatsTeamRefs(ctx, events)
		if err != nil {
			return err
		}
		for _, s := range stored {
			out[teamKey{s.EventID, s.TeamID}] = s.Ref
		}
		return nil
	}
	if err := load(); err != nil {
		return nil, err
	}
	type newRef struct {
		Event uuid.UUID `json:"event_id"`
		Team  uuid.UUID `json:"team_id"`
		Ref   string    `json:"ref"`
	}
	var fresh []newRef
	for key := range wanted {
		if _, ok := out[key]; !ok {
			fresh = append(fresh, newRef{key.event, key.team, r.NewRef()})
		}
	}
	if len(fresh) == 0 {
		return out, nil
	}
	body, err := json.Marshal(fresh)
	if err != nil {
		return nil, err
	}
	if err = r.q.InsertAssistantStatsTeamRefs(ctx, postgres.InsertAssistantStatsTeamRefsParams{Now: r.Now().UTC(), Refs: body}); err != nil {
		return nil, err
	}
	if err = load(); err != nil {
		return nil, err
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
