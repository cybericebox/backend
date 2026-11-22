package inboxModel

import (
	"strconv"

	"github.com/gofrs/uuid"
)

// TypeLabRevealTimedOut tells the organizers of an event that the tasks of an all_ready set were shown after the
// reveal timeout without waiting for every team: the teams named in it still have no Ready lab and get the tasks on
// their own once it is Ready.
const TypeLabRevealTimedOut = "event.lab.reveal_timeout"

func init() {
	categoryRules[TypeLabRevealTimedOut] = map[RecipientRole]Category{RoleManager: CategoryRequests, "": CategoryPersonal}
	// An organizer closes the alarm by hand once seen; the stuck teams open on their own.
	manuallyResolvable[TypeLabRevealTimedOut] = true
}

// LabRevealTimeoutRef ties every organizer's copy of the alarm of one barrier (the set and its revision) together.
func LabRevealTimeoutRef(eventExerciseID uuid.UUID, revision int64) string {
	return "reveal_timeout:" + eventExerciseID.String() + ":" + strconv.FormatInt(revision, 10)
}
