// Package labActivityRepo keeps what the laboratory's activity reports say about the labs, as small running figures, and the
// cursor of each lab group's pulls. A page of a group is folded in and the cursor moved in one statement.
package labActivityRepo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

type Queries interface {
	ListLabActivityGroups(context.Context, postgres.ListLabActivityGroupsParams) ([]string, error)
	GetLabActivityCursor(context.Context, string) (postgres.GetLabActivityCursorRow, error)
	ApplyLabActivity(context.Context, postgres.ApplyLabActivityParams) error
	ListAssistantLabActivity(context.Context, postgres.ListAssistantLabActivityParams) ([]postgres.ListAssistantLabActivityRow, error)
	GetLabActivityOfClient(context.Context, postgres.GetLabActivityOfClientParams) (postgres.GetLabActivityOfClientRow, error)
	PurgeLabActivity(context.Context, time.Time) error
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// Batch is what one page of a group's activity adds up to: one entry per key, so the statement never touches a row twice.
type Batch struct {
	Labs     []Lab     `json:"labs"`
	Clients  []Client  `json:"clients"`
	Dsts     []Dst     `json:"dsts"`
	Sessions []Session `json:"sessions"`
}

// Lab says a lab appears in the page; Egress that it had Internet egress. Egress is a fact of the lab, never of a device.
type Lab struct {
	Lab    string `json:"lab"`
	Egress bool   `json:"egress"`
}

// Client is the bytes (out + in) one VPN client exchanged with a lab in the page.
type Client struct {
	Lab    string `json:"lab"`
	Client string `json:"client"`
	Bytes  int64  `json:"bytes"`
}

// Dst is a lab address one VPN client reached.
type Dst struct {
	Lab    string `json:"lab"`
	Client string `json:"client"`
	Dst    string `json:"dst"`
}

// Session is a console session; ClosedMs is 0 while it is open.
type Session struct {
	Lab       string `json:"lab"`
	SessionID string `json:"session_id"`
	Client    string `json:"client"`
	OpenedMs  int64  `json:"opened_ms"`
	ClosedMs  int64  `json:"closed_ms"`
}

// Figures are the activity of one lab or one team's task. EgressUsed is "the lab had any Internet egress"; the other figures
// are in the units the brain's record uses (bytes, distinct addresses, minutes).
type Figures struct {
	// DeviceTouch false: the cluster produces no device_touch events, so VPNBytes and DevicesTouched are not reported.
	DeviceTouch    bool
	EgressUsed     bool
	VPNBytes       int64
	DevicesTouched int64
	ConsoleMinutes float64
}

// Groups are the lab groups to pull: those of the events whose statistics collection is on and that run (or ended within the
// hour).
func (r *Repository) Groups(ctx context.Context, platformDefault assistantModel.StatsMode, now time.Time) ([]string, error) {
	return r.q.ListLabActivityGroups(ctx, postgres.ListLabActivityGroupsParams{DefaultMode: platformDefault.Stored(), Now: now})
}

// Cursor is where the next pull of a group starts and what the laboratory said of the group's reports at the last one.
type Cursor struct {
	Cursor string
	// DeviceTouch: the cluster produces device_touch events; ConsoleMaxSeconds is its console session limit (0: not told).
	DeviceTouch       bool
	ConsoleMaxSeconds int32
}

// Cursor of a group; found is false for a group never pulled.
func (r *Repository) Cursor(ctx context.Context, group string) (Cursor, bool, error) {
	row, err := r.q.GetLabActivityCursor(ctx, group)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return Cursor{}, false, nil
		}
		return Cursor{}, false, err
	}
	return Cursor{Cursor: row.Cursor, DeviceTouch: row.DeviceTouch, ConsoleMaxSeconds: row.ConsoleMaxS}, true, nil
}

// Apply folds a page into the figures and moves the group's cursor, atomically.
func (r *Repository) Apply(ctx context.Context, group string, c Cursor, gap bool, b Batch, now time.Time) error {
	// A nil list would be the JSON scalar null, which jsonb_array_elements refuses: the lists are arrays.
	if b.Labs == nil {
		b.Labs = []Lab{}
	}
	if b.Clients == nil {
		b.Clients = []Client{}
	}
	if b.Dsts == nil {
		b.Dsts = []Dst{}
	}
	if b.Sessions == nil {
		b.Sessions = []Session{}
	}
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return r.q.ApplyLabActivity(ctx, postgres.ApplyLabActivityParams{LabGroupName: group, Cursor: c.Cursor, Gap: gap, DeviceTouch: c.DeviceTouch, ConsoleMaxS: c.ConsoleMaxSeconds, Now: now, Batch: body})
}

// Instance is the activity of one team's task.
type Instance struct {
	EventID, TeamID, EventExerciseID uuid.UUID
	Figures
}

// Instances are the activity of the given teams' tasks that have any report; maxSession caps a console session still open.
func (r *Repository) Instances(ctx context.Context, teams, exercises []uuid.UUID, now time.Time, maxSession time.Duration) ([]Instance, error) {
	rows, err := r.q.ListAssistantLabActivity(ctx, postgres.ListAssistantLabActivityParams{
		TeamIds: nonNil(teams), ExerciseIds: nonNil(exercises), Now: now, MaxSessionSeconds: maxSession.Seconds(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(rows))
	for _, row := range rows {
		out = append(out, Instance{
			EventID: row.EventID, TeamID: row.EventTeamID, EventExerciseID: row.EventExerciseID,
			Figures: Figures{DeviceTouch: row.DeviceTouch, EgressUsed: row.EgressUsed, VPNBytes: row.VpnBytes, DevicesTouched: row.DevicesTouched, ConsoleMinutes: row.ConsoleSeconds / 60},
		})
	}
	return out, nil
}

// OfClient is the activity of one lab as one VPN client saw it (egress is the lab's); found is false while the lab has no report.
func (r *Repository) OfClient(ctx context.Context, group, lab, client string, now time.Time, maxSession time.Duration) (Figures, bool, error) {
	row, err := r.q.GetLabActivityOfClient(ctx, postgres.GetLabActivityOfClientParams{
		Client: client, Now: now, MaxSessionSeconds: maxSession.Seconds(), LabGroupName: group, LabName: lab,
	})
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return Figures{}, false, nil
		}
		return Figures{}, false, err
	}
	return Figures{DeviceTouch: row.DeviceTouch, EgressUsed: row.EgressUsed, VPNBytes: row.VpnBytes, DevicesTouched: row.DevicesTouched, ConsoleMinutes: row.ConsoleSeconds / 60}, true, nil
}

// Purge drops the figures nothing needs any more (see PurgeLabActivity).
func (r *Repository) Purge(ctx context.Context, before time.Time) error {
	return r.q.PurgeLabActivity(ctx, before)
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
