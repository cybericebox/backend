package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

// StatsInterval is how often the leader pushes the aggregates of the events whose collection is on.
const StatsInterval = time.Hour

// maxStatsBatch caps the aggregates in one PushData call.
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
func (u *UseCase) provideData(ctx context.Context, req OutboxRequest) (any, *AnswerError) {
	if u.stats == nil {
		return nil, &AnswerError{Code: codeCollectionDisabled, Message: "statistics collection is off"}
	}
	var in struct {
		TaskRefs []assistantModel.TaskRef `json:"task_refs"`
	}
	if err := json.Unmarshal(req.Raw, &in); err != nil || len(in.TaskRefs) == 0 || len(in.TaskRefs) > maxStatsBatch {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "task_refs must list 1 to 500 tasks"}
	}
	on, err := u.stats.CollectionOn(ctx, in.TaskRefs)
	if err != nil {
		log.Error().Err(err).Msg("Reading the statistics collection switches failed")
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not read the statistics settings"}
	}
	var enabled, disabled []assistantModel.TaskRef
	for _, ref := range in.TaskRefs {
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
	return map[string]any{"aggregates": entries}, nil
}

// PushStatistics sends the aggregates of every task whose collection is on to the brain (`POST /v1/data`), in batches.
// The figures are cumulative, so a repeat replaces the earlier one and no state is kept between runs. Nothing is sent
// when there is nothing to send.
func (u *UseCase) PushStatistics(ctx context.Context) error {
	if u.stats == nil || !u.Enabled() {
		return nil
	}
	aggregates, err := u.stats.Aggregates(ctx, nil, u.now().UTC())
	if err != nil {
		return fmt.Errorf("read the statistics aggregates: %w", err)
	}
	c, err := u.client(ctx)
	if err != nil {
		return err
	}
	for start := 0; start < len(aggregates); start += maxStatsBatch {
		batch := aggregates[start:min(start+maxStatsBatch, len(aggregates))]
		body, marshalErr := json.Marshal(map[string]any{"aggregates": batch})
		if marshalErr != nil {
			return marshalErr
		}
		resp, doErr := c.Do(ctx, brain.Request{
			Method: http.MethodPost, Path: "/v1/data",
			Header: http.Header{"Content-Type": {"application/json"}}, Body: bytes.NewReader(body), ContentLength: int64(len(body)),
		})
		if doErr != nil {
			return fmt.Errorf("push the statistics: %w", doErr)
		}
		_ = resp.Body.Close()
	}
	return nil
}
