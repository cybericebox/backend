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
// step after it checks each team's own lab, so a stuck team gets the tasks once its lab is Ready), and the organizers
// get one alarm per set naming the stuck teams. The barrier opens whether or not the alarm goes out; a failed alarm
// is logged and returned.
func (u *EventUseCase) openTimedOutReveals(ctx context.Context, e eventModel.Event, now time.Time) error {
	opened, err := eventLabRevealRepo.New(u.repo).OpenTimedOut(ctx, e.ID, now, eventStandModel.RevealTimeout)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to open timed-out task reveals").Err()
	}
	var errs []error
	for _, set := range opened {
		if len(set.StuckTeamNames) == 0 {
			continue // every lab became Ready at the last moment: nothing to tell
		}
		log.Warn().Str("event_id", e.ID.String()).Str("event_exercise_id", set.EventExerciseID.String()).
			Strs("stuck_teams", set.StuckTeamNames).Msg("Tasks shown at the reveal timeout without the teams whose labs are not ready")
		if u.revealTimeoutInbox == nil {
			continue
		}
		if alarmErr := u.revealTimeoutInbox.RevealTimedOut(ctx, inboxUseCase.RevealTimeout{
			EventID: e.ID, EventName: e.Name, EventTag: e.Tag,
			EventExerciseID: set.EventExerciseID, Revision: set.Revision, TaskName: set.TaskName,
			StuckTeams: set.StuckTeamNames, Timeout: eventStandModel.RevealTimeout, At: now,
		}); alarmErr != nil {
			log.Error().Err(alarmErr).Str("event_id", e.ID.String()).Msg("Failed to raise the reveal timeout alarm")
			errs = append(errs, alarmErr)
		}
	}
	return errors.Join(errs...)
}
