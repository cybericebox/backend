package notificationTypes

import (
	"encoding/json"

	inboxModel "github.com/cybericebox/backend/internal/model/notification/inbox"
)

// labRevealTimeoutPayload is the alarm to the organizers of an event: the tasks of an all_ready set were shown after the
// reveal timeout without the teams whose labs were not ready.
type labRevealTimeoutPayload struct {
	ScopeEventID   string `var:"scope_event_id" desc:"Event identifier" default:"01900000-0000-7000-8000-000000000000"`
	EventTag       string `var:"event_tag" desc:"Event short tag" default:"ctf-2026"`
	EventName      string `var:"event_name" desc:"Event public name" default:"Cyber ICE Box CTF"`
	TaskName       string `var:"task_name" desc:"Task whose tasks were shown" default:"Web server"`
	Teams          string `var:"teams" desc:"The teams still waiting for a ready lab, comma separated" default:"Blue Team, Red Team"`
	Count          string `var:"count" desc:"How many teams still wait" default:"2"`
	TimeoutMinutes string `var:"timeout_minutes" desc:"The reveal timeout, minutes" default:"5"`
	UserID         string `var:"user_id" desc:"Recipient identifier" default:"01900000-0000-7000-8000-000000000003"`
	UserEmail      string `var:"user_email" desc:"Recipient email" default:"moderator@example.org"`
	UserFirstName  string `var:"user_first_name" desc:"Recipient first name" default:"Jane"`
	UserLastName   string `var:"user_last_name" desc:"Recipient last name" default:"Doe"`
	UserPicture    string `var:"user_picture" desc:"Recipient profile image URL" default:"https://example.org/avatar.png"`
	UserName       string `var:"user_name" desc:"Recipient full name" default:"Jane Doe"`
}

func (labRevealTimeoutPayload) NotificationType() NotificationType {
	return inboxModel.TypeLabRevealTimedOut
}
func (labRevealTimeoutPayload) NotificationChannels() []NotificationChannel {
	return []NotificationChannel{NotificationChannelInApp}
}
func (labRevealTimeoutPayload) Marshal() (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

func init() {
	Register(labRevealTimeoutPayload{})
}
