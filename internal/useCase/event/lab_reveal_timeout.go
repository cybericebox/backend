package event

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/repository/eventLabRevealRepo"
	"github.com/cybericebox/backend/internal/model"
	eventModel "github.com/cybericebox/backend/internal/model/event"
	eventStandModel "github.com/cybericebox/backend/internal/model/eventStand"
	inboxUseCase "github.com/cybericebox/backend/internal/useCase/notification/inbox"
)

// openTimedOutReveals is the reveal timeout of the all_ready sets of the event: a set still waiting
// eventStandModel.RevealTimeout after it would have opened is opened for the teams whose labs are Ready (the publish
// step after it applies the same Ready test to each team, so a stuck team gets the tasks once its lab is Ready). The
// statement that opens a barrier also marks its alarm due; raisePendingRevealAlarms sends it.
func (u *EventUseCase) openTimedOutReveals(ctx context.Context, e eventModel.Event, now time.Time) error {
	opened, err := eventLabRevealRepo.New(u.repo).OpenTimedOut(ctx, e.ID, now, eventStandModel.RevealTimeout)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to open timed-out task reveals").Err()
	}
	if opened > 0 {
		log.Warn().Str("event_id", e.ID.String()).Int64("sets", opened).Msg("Tasks shown at the reveal timeout without every team")
	}
	return nil
}

// raisePendingRevealAlarms sends the organizers' alarm of every barrier the timeout opened and whose alarm is still
// due, naming the teams that have not got the tasks; it runs after the publish step, so a team that just got them is
// not named. A sent alarm (or one with nobody left to name) is marked; a failed one stays due and the next pass tries
// again. The inbox drops a copy it already has (one request per barrier), so a mark lost after the send repeats
// nothing. Without an inbox nothing is sent and nothing is marked.
func (u *EventUseCase) raisePendingRevealAlarms(ctx context.Context, e eventModel.Event, now time.Time) error {
	if u.revealTimeoutInbox == nil {
		return nil
	}
	reveals := eventLabRevealRepo.New(u.repo)
	pending, err := reveals.PendingTimeoutAlarms(ctx, e.ID)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list the reveal timeout alarms").Err()
	}
	var errs []error
	for _, set := range pending {
		if len(set.StuckTeamNames) > 0 {
			if alarmErr := u.revealTimeoutInbox.RevealTimedOut(ctx, inboxUseCase.RevealTimeout{
				EventID: e.ID, EventName: e.Name, EventTag: e.Tag,
				EventExerciseID: set.EventExerciseID, Revision: set.Revision, TaskName: set.TaskName,
				StuckTeams: set.StuckTeamNames, Timeout: eventStandModel.RevealTimeout, At: now,
			}); alarmErr != nil {
				log.Error().Err(alarmErr).Str("event_id", e.ID.String()).Str("event_exercise_id", set.EventExerciseID.String()).
					Msg("Failed to raise the reveal timeout alarm; the next pass tries again")
				errs = append(errs, alarmErr)
				continue
			}
		}
		if markErr := reveals.MarkTimeoutAlarmSent(ctx, set.EventExerciseID, set.Revision, now); markErr != nil {
			errs = append(errs, model.ErrPlatform.WithError(markErr).WithMessage("Failed to mark the reveal timeout alarm sent").Err())
		}
	}
	return errors.Join(errs...)
}
