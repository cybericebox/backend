package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// StatsInterval is how often the leader pushes the aggregates of the events whose collection is on.
const StatsInterval = time.Hour

// maxStatsBatch caps the tasks one provide_data request may ask for (PushData batches are split by size instead).
const maxStatsBatch = 500

// StatsSource gives the aggregates of the tasks of the events whose statistics collection is on. Hidden teams and
// moderators never count in them, and no run does: a run of the platform (the grader's, an opponent's) is left out by
// assistantModel.ExcludeRun, which any source that reads run-shaped facts must call. It returns
// aggregates only: nothing in them identifies a person, a team or an address.
type StatsSource interface {
	// Name is the source's key in the platform defaults ("events"; the Academy will be another).
	Name() string
	// Aggregates are the cumulative figures per (task, version, image) as of now. refs nil: every task with collection on;
	// else only those refs.
	Aggregates(ctx context.Context, refs []assistantModel.TaskRef, asOf time.Time) ([]assistantModel.Aggregate, error)
	// CollectionOn says, for each ref, whether collection is on for it (an event that uses the task has it on).
	CollectionOn(ctx context.Context, refs []assistantModel.TaskRef) (map[assistantModel.TaskRef]bool, error)
}

// MultiSource combines the statistics sources; each reports its own tasks and has its own platform default.
type MultiSource []StatsSource

// Names are the keys of the sources.
func (m MultiSource) Names() []string {
	names := make([]string, 0, len(m))
	for _, s := range m {
		names = append(names, s.Name())
	}
	return names
}

func (m MultiSource) Name() string { return strings.Join(m.Names(), ",") }

func (m MultiSource) Aggregates(ctx context.Context, refs []assistantModel.TaskRef, asOf time.Time) ([]assistantModel.Aggregate, error) {
	var out []assistantModel.Aggregate
	for _, s := range m {
		part, err := s.Aggregates(ctx, refs, asOf)
		if err != nil {
			return nil, fmt.Errorf("statistics source %s: %w", s.Name(), err)
		}
		out = append(out, part...)
	}
	return out, nil
}

func (m MultiSource) CollectionOn(ctx context.Context, refs []assistantModel.TaskRef) (map[assistantModel.TaskRef]bool, error) {
	out := make(map[assistantModel.TaskRef]bool, len(refs))
	for _, s := range m {
		part, err := s.CollectionOn(ctx, refs)
		if err != nil {
			return nil, fmt.Errorf("statistics source %s: %w", s.Name(), err)
		}
		for ref, on := range part {
			out[ref] = out[ref] || on
		}
	}
	return out, nil
}

// provideData answers the brain's pull request: the aggregates of the requested tasks, or collection_disabled when
// collection is off for all of them.
func (u *UseCase) provideData(ctx context.Context, req OutboxRequest) (*brainapiv1.RequestAnswer, *AnswerError) {
	if u.stats == nil {
		return nil, &AnswerError{Code: codeCollectionDisabled, Message: "statistics collection is off"}
	}
	tasks := req.Req.GetProvideData().GetTasks()
	if len(tasks) == 0 || len(tasks) > maxStatsBatch {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "tasks must list 1 to 500 tasks"}
	}
	refs := make([]assistantModel.TaskRef, 0, len(tasks))
	for _, t := range tasks {
		refs = append(refs, assistantModel.TaskRef{TaskRef: t.GetTaskRef(), Version: t.GetVersion(), Image: t.GetImage()})
	}
	on, err := u.stats.CollectionOn(ctx, refs)
	if err != nil {
		log.Error().Err(err).Msg("Reading the statistics collection switches failed")
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not read the statistics settings"}
	}
	var enabled, disabled []assistantModel.TaskRef
	for _, ref := range refs {
		if on[ref] {
			enabled = append(enabled, ref)
		} else {
			disabled = append(disabled, ref)
		}
	}
	if len(enabled) == 0 {
		return nil, &AnswerError{Code: codeCollectionDisabled, Message: "statistics collection is off for the requested tasks"}
	}
	aggregates, err := u.stats.Aggregates(ctx, enabled, u.now().UTC())
	if err != nil {
		log.Error().Err(err).Msg("Reading the statistics aggregates failed")
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not read the statistics"}
	}
	// The tasks whose collection is off are listed with the code, so the brain can tell "off" from "no data yet".
	entries := make([]any, 0, len(aggregates)+len(disabled))
	for _, a := range aggregates {
		entries = append(entries, a)
	}
	for _, ref := range disabled {
		entries = append(entries, assistantModel.Unavailable{TaskRef: ref, Error: codeCollectionDisabled})
	}
	raw, err := json.Marshal(map[string]any{"aggregates": entries})
	if err != nil {
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not encode the statistics"}
	}
	return &brainapiv1.RequestAnswer{Result: &brainapiv1.RequestAnswer_DataJson{DataJson: raw}}, nil
}

// PushStatistics sends the aggregates of every task whose collection is on to the brain (PushData): one JSON object per
// aggregate, in batches split by size under the message limit. The figures are cumulative, so a repeat replaces the earlier one
// and no state is kept between runs; an aggregate the brain rejects is logged and goes again with the next push. Nothing is
// sent when there is nothing to send.
func (u *UseCase) PushStatistics(ctx context.Context) error {
	if u.stats == nil || !u.Enabled() {
		return nil
	}
	aggregates, err := u.stats.Aggregates(ctx, nil, u.now().UTC())
	if err != nil {
		return fmt.Errorf("read the statistics aggregates: %w", err)
	}
	if len(aggregates) == 0 {
		return nil
	}
	c, err := u.control(ctx)
	if err != nil {
		return err
	}
	items := make([][]byte, 0, len(aggregates))
	for _, a := range aggregates {
		raw, marshalErr := json.Marshal(a)
		if marshalErr != nil {
			return marshalErr
		}
		items = append(items, raw)
	}
	rejected, err := c.PushData(ctx, items)
	for _, r := range rejected {
		log.Warn().Str("task_ref", r.GetTaskRef()).Str("version", r.GetVersion()).Str("reason", r.GetReason()).Msg("The brain rejected a statistics aggregate")
	}
	if err != nil {
		return fmt.Errorf("push the statistics: %w", err)
	}
	return nil
}
