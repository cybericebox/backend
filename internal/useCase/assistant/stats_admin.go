package assistant

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/model"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

// StatsAdminStore keeps the statistics collection modes.
type StatsAdminStore interface {
	Defaults(ctx context.Context) (map[string]assistantModel.StatsMode, error)
	SetDefault(ctx context.Context, source string, mode assistantModel.StatsMode, at time.Time) error
	EventMode(ctx context.Context, event uuid.UUID) (assistantModel.StatsMode, error)
	SetEventMode(ctx context.Context, event uuid.UUID, mode assistantModel.StatsMode, at time.Time) (bool, error)
}

// SourceMode is the platform default mode of one statistics source.
type SourceMode struct {
	Source string
	Mode   assistantModel.StatsMode
}

// EventStatsMode is the mode an event is set to and the one in force for it.
type EventStatsMode struct {
	Mode      assistantModel.StatsMode
	Effective assistantModel.StatsMode
}

// statsSources are the keys of the statistics sources that exist.
func (u *UseCase) statsSources() []string {
	switch s := u.stats.(type) {
	case nil:
		return nil
	case MultiSource:
		return s.Names()
	default:
		return []string{s.Name()}
	}
}

func (u *UseCase) knownSource(source string) bool {
	for _, name := range u.statsSources() {
		if name == source {
			return true
		}
	}
	return false
}

// StatsDefaults lists every statistics source with its platform default (off unless set).
func (u *UseCase) StatsDefaults(ctx context.Context) ([]SourceMode, error) {
	if u.statsAdmin == nil {
		return nil, nil
	}
	set, err := u.statsAdmin.Defaults(ctx)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to read the statistics defaults").Err()
	}
	out := make([]SourceMode, 0, len(u.statsSources()))
	for _, source := range u.statsSources() {
		mode, ok := set[source]
		if !ok {
			mode = assistantModel.StatsModeOff
		}
		out = append(out, SourceMode{Source: source, Mode: mode})
	}
	return out, nil
}

// SetStatsDefault sets the platform default of a source: off, final or continuous.
func (u *UseCase) SetStatsDefault(ctx context.Context, source string, mode assistantModel.StatsMode) error {
	if u.statsAdmin == nil || !u.knownSource(source) || mode.Stored() == 0 {
		return assistantModel.ErrAssistantStatsInvalid.Err()
	}
	if err := u.statsAdmin.SetDefault(ctx, source, mode, u.now().UTC()); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to save the statistics default").Err()
	}
	return nil
}

// EventStats is the mode of an event and the mode in force for it (its own, else the platform default of the events
// source).
func (u *UseCase) EventStats(ctx context.Context, event uuid.UUID) (EventStatsMode, error) {
	if u.statsAdmin == nil {
		return EventStatsMode{}, assistantModel.ErrAssistantStatsInvalid.Err()
	}
	own, err := u.statsAdmin.EventMode(ctx, event)
	if err != nil {
		return EventStatsMode{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the event statistics mode").Err()
	}
	result := EventStatsMode{Mode: own, Effective: own}
	if own == assistantModel.StatsModeDefault {
		defaults, defaultsErr := u.statsAdmin.Defaults(ctx)
		if defaultsErr != nil {
			return EventStatsMode{}, model.ErrPlatform.WithError(defaultsErr).WithMessage("Failed to read the statistics defaults").Err()
		}
		result.Effective = assistantModel.StatsModeOff
		if mode, ok := defaults[assistantModel.SourceEvents]; ok {
			result.Effective = mode
		}
	}
	return result, nil
}

// SetEventStats sets the mode of an event: default, off, final or continuous.
func (u *UseCase) SetEventStats(ctx context.Context, event uuid.UUID, mode assistantModel.StatsMode) error {
	if u.statsAdmin == nil || !u.knownSource(assistantModel.SourceEvents) {
		return assistantModel.ErrAssistantStatsInvalid.Err()
	}
	switch mode {
	case assistantModel.StatsModeDefault, assistantModel.StatsModeOff, assistantModel.StatsModeFinal, assistantModel.StatsModeContinuous:
	default:
		return assistantModel.ErrAssistantStatsInvalid.Err()
	}
	found, err := u.statsAdmin.SetEventMode(ctx, event, mode, u.now().UTC())
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to save the event statistics mode").Err()
	}
	if !found {
		return assistantModel.ErrAssistantStatsEventNotFound.Err()
	}
	return nil
}
