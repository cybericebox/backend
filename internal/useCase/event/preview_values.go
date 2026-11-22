package event

import (
	"context"
	"fmt"

	"github.com/gofrs/uuid"

	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	"github.com/cybericebox/backend/internal/model"
	eventModel "github.com/cybericebox/backend/internal/model/event"
	"github.com/cybericebox/backend/internal/model/usertext"
)

// EventPreviewValues are the variable values the template previews of the event use (eventPreviewValues).
func (u *EventUseCase) EventPreviewValues(ctx context.Context, eventID uuid.UUID) (map[string]string, error) {
	e, err := u.events.GetByID(ctx, eventID)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return nil, eventModel.ErrEventNotFound.Err()
		}
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to get event").Err()
	}
	return eventPreviewValues(e, u.eventDomain), nil
}

// eventPreviewValues are the variable values of this event's template previews (email and in-app): the event's
// own name, tag, site links in the format dispatch uses and its times. What only a real recipient has (a name, an
// address, a team) is a clear placeholder; the invitation link is the event's own one without a setup token.
func eventPreviewValues(e eventModel.Event, eventDomain string) map[string]string {
	site := fmt.Sprintf("https://%s.%s", e.Tag, eventDomain)
	values := map[string]string{
		"scope_event_id":  e.ID.String(),
		"event_tag":       e.Tag,
		"event_name":      e.Name,
		"event_url":       site + "/",
		"invite_url":      site + "/invite",
		"team_url":        site + "/participation?tab=team",
		"start_at":        kyivTime(e.Lifecycle.StartAt),
		"user_first_name": usertext.PreviewFirstName,
		"user_last_name":  usertext.PreviewLastName,
		"user_name":       usertext.PreviewParticipantName,
		"user_email":      "participant@" + e.Tag + "." + eventDomain,
		"team_name":       usertext.PreviewTeamName,
	}
	if e.Lifecycle.FinishAt != nil {
		values["finish_at"] = kyivTime(*e.Lifecycle.FinishAt)
	}
	return values
}
