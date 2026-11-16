// Package labActivity pulls the laboratory's activity reports (contract 2.5) and turns them into the figures the statistics for
// cyber-brain need: whether a team's lab had Internet egress, the VPN bytes of its clients, the distinct lab addresses they
// reached and the console minutes. The laboratory hands out raw facts only; the daemon folds them into small running figures
// and keeps them only as long as the statistics need them.
//
// Egress belongs to a lab: the laboratory does not attribute it to a device, and the daemon never maps it to a device or a
// person. In an event each team has its own lab, so a lab's egress is a team's.
package labActivity

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/daemon/internal/delivery/repository/assistantStatsRepo"
	"github.com/cybericebox/daemon/internal/delivery/repository/labActivityRepo"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
)

const (
	// PullInterval is how often the leader pulls the activity of the groups ("about once a minute", the laboratory's flush).
	PullInterval = time.Minute
	// PurgeInterval is how often the figures nothing needs are dropped.
	PurgeInterval = time.Hour
	// Retention is how long the figures of a lab that no event names (a test lab) are kept after its last report.
	Retention = 7 * 24 * time.Hour
	// DefaultConsoleMaxLifetime caps a console session that never reported its end.
	DefaultConsoleMaxLifetime = 8 * time.Hour
	// pageLimit is the events asked for in one call (the laboratory's own maximum is 5000).
	pageLimit = 2000
	// maxPages bounds the calls one pull of a group makes; the rest waits for the next pass.
	maxPages = 50
	// workers is how many groups are pulled at once.
	workers = 8
)

// Agent is what the pull needs of the laboratory fleet.
type Agent interface {
	// ActivityFeature is what the agent that holds the group says of its activity reports (Enabled false: none are kept).
	ActivityFeature(ctx context.Context, group string) (infraModel.ActivityFeature, error)
	GroupActivity(ctx context.Context, group, cursor string, limit int32) (infraModel.ActivityPage, error)
}

// Store keeps the figures and the cursors (labActivityRepo.Repository).
type Store interface {
	Groups(ctx context.Context, platformDefault assistantModel.StatsMode, now time.Time) ([]string, error)
	Cursor(ctx context.Context, group string) (labActivityRepo.Cursor, bool, error)
	Apply(ctx context.Context, group string, c labActivityRepo.Cursor, gap bool, b labActivityRepo.Batch, now time.Time) error
	Instances(ctx context.Context, teams, exercises []uuid.UUID, now time.Time, maxSession time.Duration) ([]labActivityRepo.Instance, error)
	OfClient(ctx context.Context, group, lab, client string, now time.Time, maxSession time.Duration) (labActivityRepo.Figures, bool, error)
	Purge(ctx context.Context, before time.Time) error
}

// Modes gives the platform default statistics mode of the events source (assistantStatsRepo.Repository).
type Modes interface {
	Defaults(ctx context.Context) (map[string]assistantModel.StatsMode, error)
}

type Dependencies struct {
	// Agent is nil without infrastructure: nothing is pulled and no figure is reported.
	Agent Agent
	Store Store
	Modes Modes
	Now   func() time.Time
	// ConsoleMaxLifetime caps a console session that never reported its end (zero: DefaultConsoleMaxLifetime).
	ConsoleMaxLifetime time.Duration
}

type UseCase struct {
	agent      Agent
	store      Store
	modes      Modes
	now        func() time.Time
	consoleMax time.Duration
}

func New(d Dependencies) *UseCase {
	u := &UseCase{agent: d.Agent, store: d.Store, modes: d.Modes, now: d.Now, consoleMax: d.ConsoleMaxLifetime}
	if u.now == nil {
		u.now = time.Now
	}
	if u.consoleMax <= 0 {
		u.consoleMax = DefaultConsoleMaxLifetime
	}
	return u
}

// Run pulls the activity of the groups until ctx ends, once a minute, and drops what nothing needs once an hour. Only the
// leader replica runs it; the cursors are in the database, so the next leader goes on from them.
func (u *UseCase) Run(ctx context.Context) {
	if u.agent == nil {
		return
	}
	u.PullAll(ctx)
	pull, purge := time.NewTicker(PullInterval), time.NewTicker(PurgeInterval)
	defer pull.Stop()
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pull.C:
			u.PullAll(ctx)
		case <-purge.C:
			if err := u.store.Purge(ctx, u.now().Add(-Retention).UTC()); err != nil {
				log.Warn().Err(err).Msg("Dropping the old lab activity failed")
			}
		}
	}
}

// PullAll pulls the groups of the events whose statistics collection is on.
func (u *UseCase) PullAll(ctx context.Context) {
	mode := assistantModel.StatsModeOff
	if u.modes != nil {
		defaults, err := u.modes.Defaults(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Reading the statistics defaults failed; the lab activity is not pulled this pass")
			return
		}
		if m, ok := defaults[assistantModel.SourceEvents]; ok {
			mode = m
		}
	}
	groups, err := u.store.Groups(ctx, mode, u.now().UTC())
	if err != nil {
		log.Warn().Err(err).Msg("Listing the lab groups to pull failed")
		return
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range min(workers, len(groups)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for group := range jobs {
				if pullErr := u.PullGroup(ctx, group); pullErr != nil && ctx.Err() == nil {
					log.Warn().Err(pullErr).Str("group", group).Msg("Pulling the lab activity of a group failed")
				}
			}
		}()
	}
	for _, g := range groups {
		select {
		case jobs <- g:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
}

// PullGroup reads the activity of one lab group from its cursor on, until the laboratory has no more waiting (or the pass's
// page limit), folding each page into the figures together with the cursor. A cluster without activity reports, or a group the
// laboratory does not know, is not an error: nothing is reported for it.
func (u *UseCase) PullGroup(ctx context.Context, group string) error {
	if u.agent == nil {
		return nil
	}
	feature, err := u.agent.ActivityFeature(ctx, group)
	if err != nil {
		return err
	}
	if !feature.Enabled {
		return nil
	}
	stored, _, err := u.store.Cursor(ctx, group)
	if err != nil {
		return err
	}
	// What the laboratory says now is kept with the cursor: whether device touches are produced, and its console limit.
	cur := labActivityRepo.Cursor{Cursor: stored.Cursor, DeviceTouch: feature.DeviceTouch, ConsoleMaxSeconds: max(feature.ConsoleMaxLifetimeSeconds, 0)}
	for range maxPages {
		page, pageErr := u.agent.GroupActivity(ctx, group, cur.Cursor, pageLimit)
		switch {
		case errors.Is(pageErr, infraModel.ErrActivityDisabled), errors.Is(pageErr, infraModel.ErrActivityGroupGone):
			return nil
		case errors.Is(pageErr, infraModel.ErrActivityCursor):
			// Another agent or a restart: start from the oldest event the laboratory keeps. Facts it still holds from before may be
			// counted twice; the figures are marked as approximate.
			log.Warn().Str("group", group).Msg("The laboratory does not know the activity cursor; the pull starts again")
			cur.Cursor = ""
			if err = u.store.Apply(ctx, group, cur, true, labActivityRepo.Batch{}, u.now().UTC()); err != nil {
				return err
			}
			continue
		case pageErr != nil:
			return pageErr
		}
		if page.Gap {
			log.Warn().Str("group", group).Msg("The laboratory dropped activity events before they were read; the figures are a lower bound")
		}
		cur.Cursor = page.NextCursor
		if err = u.store.Apply(ctx, group, cur, page.Gap, Fold(page.Events), u.now().UTC()); err != nil {
			return err
		}
		if !page.HasMore {
			return nil
		}
	}
	return nil
}

// Fold adds a page of raw facts up: a lab is egress-using if any egress event names it; client bytes are out + in summed; the
// lab addresses a client reached are a set; a console session's open and close events (same id) merge into one. Egress is kept
// per lab and never attributed to a device or a client.
func Fold(events []infraModel.ActivityEvent) labActivityRepo.Batch {
	type clientKey struct{ lab, client string }
	type dstKey struct{ lab, client, dst string }
	type sessionKey struct{ lab, id string }
	labs := map[string]bool{}
	var labOrder []string
	note := func(lab string, egress bool) {
		if _, ok := labs[lab]; !ok {
			labOrder = append(labOrder, lab)
		}
		labs[lab] = labs[lab] || egress
	}
	bytes := map[clientKey]int64{}
	var clientOrder []clientKey
	dsts := map[dstKey]bool{}
	var dstOrder []dstKey
	sessions := map[sessionKey]*labActivityRepo.Session{}
	var sessionOrder []sessionKey
	for _, e := range events {
		if e.Lab == "" {
			continue
		}
		switch {
		case e.Egress != nil:
			note(e.Lab, true)
		case e.Touch != nil:
			note(e.Lab, false)
			k := clientKey{e.Lab, e.Touch.Client}
			if _, ok := bytes[k]; !ok {
				clientOrder = append(clientOrder, k)
			}
			bytes[k] += e.Touch.BytesOut + e.Touch.BytesIn
			d := dstKey{e.Lab, e.Touch.Client, e.Touch.DstAddress}
			if !dsts[d] {
				dsts[d] = true
				dstOrder = append(dstOrder, d)
			}
		case e.Console != nil:
			note(e.Lab, false)
			k := sessionKey{e.Lab, e.Console.SessionID}
			s := sessions[k]
			if s == nil {
				s = &labActivityRepo.Session{Lab: e.Lab, SessionID: e.Console.SessionID, Client: e.Console.Client, OpenedMs: e.Console.OpenedAtMs}
				sessions[k] = s
				sessionOrder = append(sessionOrder, k)
			}
			s.ClosedMs = max(s.ClosedMs, e.Console.ClosedAtMs)
		}
	}
	var b labActivityRepo.Batch
	for _, lab := range labOrder {
		b.Labs = append(b.Labs, labActivityRepo.Lab{Lab: lab, Egress: labs[lab]})
	}
	for _, k := range clientOrder {
		b.Clients = append(b.Clients, labActivityRepo.Client{Lab: k.lab, Client: k.client, Bytes: bytes[k]})
	}
	for _, k := range dstOrder {
		b.Dsts = append(b.Dsts, labActivityRepo.Dst{Lab: k.lab, Client: k.client, Dst: k.dst})
	}
	for _, k := range sessionOrder {
		b.Sessions = append(b.Sessions, *sessions[k])
	}
	return b
}

// Activity gives the activity figures of the teams' tasks (the seam of the statistics: assistant.LabActivitySource). An
// instance whose group was never pulled (the reports are off, or nothing has been pulled yet) is absent: "not reported".
func (u *UseCase) Activity(ctx context.Context, keys []assistantStatsRepo.InstanceKey) (map[assistantStatsRepo.InstanceKey]assistantStatsRepo.Activity, error) {
	var teams, exercises []uuid.UUID
	for _, k := range keys {
		if !slices.Contains(teams, k.TeamID) {
			teams = append(teams, k.TeamID)
		}
		if !slices.Contains(exercises, k.EventExerciseID) {
			exercises = append(exercises, k.EventExerciseID)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := u.store.Instances(ctx, teams, exercises, u.now().UTC(), u.consoleMax)
	if err != nil {
		return nil, err
	}
	want := make(map[assistantStatsRepo.InstanceKey]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	out := make(map[assistantStatsRepo.InstanceKey]assistantStatsRepo.Activity, len(rows))
	for _, row := range rows {
		key := assistantStatsRepo.InstanceKey{EventID: row.EventID, TeamID: row.TeamID, EventExerciseID: row.EventExerciseID}
		if !want[key] {
			continue
		}
		used, minutes := row.EgressUsed, row.ConsoleMinutes
		act := assistantStatsRepo.Activity{EgressUsed: &used, ConsoleMinutes: &minutes}
		// Without device_touch events the laboratory cannot say what a team sent or reached: not reported, never zero.
		if row.DeviceTouch {
			bytes, devices := float64(row.VPNBytes), float64(row.DevicesTouched)
			act.VPNBytes, act.DevicesTouched = &bytes, &devices
		}
		out[key] = act
	}
	return out, nil
}

// LabFigures are the activity of one lab as one client saw it.
type LabFigures struct {
	EgressUsed bool
	// VPNBytes and DevicesTouched are nil while the cluster produces no device_touch events: not reported.
	VPNBytes       *float64
	DevicesTouched *float64
	ConsoleMinutes float64
}

// Refresh pulls the group now, so what the lab did up to this moment is in the figures (the grader's teardown calls it before
// the lab is destroyed).
func (u *UseCase) Refresh(ctx context.Context, group string) error { return u.PullGroup(ctx, group) }

// OfLab is the activity of one lab as one VPN client saw it (egress is the lab's own); nil while the group has never been pulled
// (reports off): not reported. A pulled group with nothing about the lab is a lab with no activity: zeros.
func (u *UseCase) OfLab(ctx context.Context, group, lab, client string) (*LabFigures, error) {
	f, found, err := u.store.OfClient(ctx, group, lab, client, u.now().UTC(), u.consoleMax)
	if err != nil {
		return nil, err
	}
	if !found {
		cur, pulled, cursorErr := u.store.Cursor(ctx, group)
		if cursorErr != nil || !pulled {
			return nil, cursorErr
		}
		f.DeviceTouch = cur.DeviceTouch
	}
	out := &LabFigures{EgressUsed: f.EgressUsed, ConsoleMinutes: f.ConsoleMinutes}
	if f.DeviceTouch {
		bytes, devices := float64(f.VPNBytes), float64(f.DevicesTouched)
		out.VPNBytes, out.DevicesTouched = &bytes, &devices
	}
	return out, nil
}
