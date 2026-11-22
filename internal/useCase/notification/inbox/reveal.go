package inboxUseCase

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"

	dispatchModel "github.com/cybericebox/backend/internal/model/notification/dispatch"
	inboxModel "github.com/cybericebox/backend/internal/model/notification/inbox"
)

// RevealTimeout is an all_ready barrier the reveal timeout opened while some teams of its cohort had no Ready lab.
type RevealTimeout struct {
	EventID   uuid.UUID
	EventName string
	EventTag  string
	// EventExerciseID and Revision name the barrier: one alarm per barrier.
	EventExerciseID uuid.UUID
	Revision        int64
	TaskName        string
	// StuckTeams are the names of the teams still waiting for a Ready lab.
	StuckTeams []string
	Timeout    time.Duration
	At         time.Time
}

// RevealTimedOut tells the organizers of the event (the managers with write access) once per barrier which teams
// did not get the tasks at the reveal timeout. A barrier already reported (open or closed by an organizer) is not sent
// again.
func (r *RequestRouter) RevealTimedOut(ctx context.Context, a RevealTimeout) error {
	ref := inboxModel.LabRevealTimeoutRef(a.EventExerciseID, a.Revision)
	sent, err := r.inbox.SubjectExists(ctx, ref)
	if err != nil {
		return fmt.Errorf("inbox requests: check the reveal timeout alarm: %w", err)
	}
	if sent {
		return nil
	}
	managers, err := r.queries.ListEventWriteManagerUserIDs(ctx, a.EventID)
	if err != nil {
		return fmt.Errorf("inbox requests: list event managers: %w", err)
	}
	vars := map[string]any{
		"scope_event_id": a.EventID.String(), "event_tag": a.EventTag, "event_name": a.EventName,
		"task_name": a.TaskName, "teams": strings.Join(a.StuckTeams, ", "), "count": strconv.Itoa(len(a.StuckTeams)),
		"timeout_minutes": strconv.Itoa(int(math.Ceil(a.Timeout.Minutes()))),
	}
	meta := inboxModel.NewMeta(inboxModel.TypeLabRevealTimedOut, inboxModel.RoleManager, ref).Raised(a.At)
	return r.fanOut(ctx, managers, inboxModel.TypeLabRevealTimedOut, vars, meta, dispatchModel.WithEventScope(a.EventID))
}
