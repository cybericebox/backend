package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	"github.com/cybericebox/daemon/internal/model/challengeAttempt"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
	labActivityUseCase "github.com/cybericebox/daemon/internal/useCase/labActivity"
)

const (
	// labReadyTimeout is how long a run waits for its lab to come up.
	labReadyTimeout = 10 * time.Minute
	// mintAttempts is how often the run's VPN client is tried before the run fails.
	mintAttempts = 3
	// pauseMax bounds the doubling pause between retries.
	pauseMax = 30 * time.Second
	// maxActiveScan is how many active runs one pass of the leader looks at.
	maxActiveScan = 200
	// stopAttempts is how often the brain is told to stop before the teardown goes on without it.
	stopAttempts = 3
	// verdictAttempts is how often a verdict is sent before it is left for the next pass.
	verdictAttempts = 3
)

// Run event types the platform acts on; every other type is stored and shown as it is.
const (
	eventAnswer      = "answer"
	eventRunFinished = "run_finished"
)

// The brain's final measurement of a run: its last event of this type (run_finished always comes after it) is the
// measurement.
var measurementEvents = map[string]bool{"measure": true}

// DriveRuns follows every active run until ctx ends (the leadership is lost, or the process stops): it starts the run at
// the brain once its lab is ready, reads the brain's events and stores them, judges the answers, and tears the lab down
// when the run ends. All of it resumes from the stored state, so a new leader carries on where the last one stopped.
// Only the leader replica runs it.
func (u *UseCase) DriveRuns(ctx context.Context) {
	if u.runsReady() != nil {
		return
	}
	var (
		mu      sync.Mutex
		driving = map[uuid.UUID]bool{}
		wg      sync.WaitGroup
	)
	scan := func() {
		runs, err := u.runs.Active(ctx, maxActiveScan)
		if err != nil {
			log.Error().Err(err).Msg("Listing the active runs failed")
			return
		}
		for _, r := range runs {
			mu.Lock()
			busy := driving[r.ID]
			driving[r.ID] = true
			mu.Unlock()
			if busy {
				continue
			}
			wg.Add(1)
			go func(id uuid.UUID) {
				defer wg.Done()
				defer func() {
					mu.Lock()
					delete(driving, id)
					mu.Unlock()
				}()
				(&runDriver{u: u, id: id}).run(ctx)
			}(r.ID)
		}
	}
	scan()
	t := time.NewTicker(u.runPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
			scan()
		}
	}
}

// runDriver drives one run; what it keeps between steps is only a cache (the VPN config of this leadership) and counters.
type runDriver struct {
	u  *UseCase
	id uuid.UUID

	// verdicts serializes the sends of verdicts: the stream and the retry of the unsent ones run side by side.
	verdicts sync.Mutex

	config    string
	mintFails int
	// ctlFails counts the failed tries to read the control address of a tools device.
	ctlFails int
	stopSent bool
	refused  bool
	// stateFlushed: the verdicts of the state subtasks that never passed were sent (once, before the teardown).
	stateFlushed bool
	// factsSent: the lab facts of the run are with the brain; factsTries counts the passes that tried to send them.
	factsSent  bool
	factsTries int
	// activityTries counts the passes that tried to read the activity of the run's lab before it was destroyed.
	activityTries int
	// materialsSent: the materials of this run are with the brain (kept for this leadership; a new leader sends them again).
	materialsSent bool
	revoked       bool
	pause         time.Duration
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// backoff returns the next pause: the first one, then doubled up to pauseMax.
func (d *runDriver) backoff() time.Duration {
	if d.pause == 0 {
		d.pause = d.u.runPause
	} else {
		d.pause = min(d.pause*2, pauseMax)
	}
	return d.pause
}

func (d *runDriver) run(ctx context.Context) {
	for ctx.Err() == nil {
		run, found, err := d.u.runs.Get(ctx, d.id)
		if err != nil {
			log.Warn().Err(err).Msg("Reading a run failed")
			if !sleep(ctx, d.backoff()) {
				return
			}
			continue
		}
		if !found || !run.State.Active() {
			return
		}
		var wait time.Duration
		if assistantModel.IsLabRole(run.Role) {
			wait = d.lab(ctx, run)
			if !sleep(ctx, wait) {
				return
			}
			continue
		}
		switch run.State {
		case assistantModel.RunStarting:
			wait = d.starting(ctx, run)
		case assistantModel.RunRunning:
			wait = d.running(ctx, run)
		case assistantModel.RunClosing:
			wait = d.closing(ctx, run)
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

func (d *runDriver) close(ctx context.Context, run assistantModel.Run, end assistantModel.RunState, reason string) {
	if _, err := d.u.runs.Close(ctx, run.ID, end, reason, d.u.now().UTC()); err != nil {
		log.Error().Err(err).Msg("Closing a run failed")
	}
	log.Info().Str("run", run.ID.String()).Str("end", string(end)).Str("reason", reason).Msg("A run is ending")
}

// down notes that the brain cannot be reached and fails the run once that has lasted the grace. It reports whether it did.
func (d *runDriver) down(ctx context.Context, run assistantModel.Run) bool {
	now := d.u.now().UTC()
	since := run.StreamDownSince
	if since.IsZero() {
		since = now
		if err := d.u.runs.SetStreamDown(ctx, run.ID, since, now); err != nil {
			log.Warn().Err(err).Msg("Recording the unreachable brain failed")
		}
	}
	if now.Sub(since) >= d.u.runGrace {
		d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureBrainUnreachable)
		return true
	}
	return false
}

func (d *runDriver) reachable(ctx context.Context, run assistantModel.Run) {
	if run.StreamDownSince.IsZero() {
		return
	}
	if err := d.u.runs.SetStreamDown(ctx, run.ID, time.Time{}, d.u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Clearing the unreachable brain failed")
	}
}

// starting waits for the lab, gives the run its own VPN client and starts the run at the brain.
func (d *runDriver) starting(ctx context.Context, run assistantModel.Run) time.Duration {
	u, now := d.u, d.u.now()
	if !now.Before(run.ExpiresAt) {
		d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureTimeout)
		return 0
	}
	state, err := u.labs.TestLabState(ctx, run.Actor, run.DeployID)
	switch {
	case err != nil:
		log.Warn().Err(err).Msg("Reading the lab of a run failed")
		return d.backoff()
	case state.Gone || state.Failed:
		d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
		return 0
	case !state.Ready:
		if now.Sub(run.CreatedAt) > labReadyTimeout {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabNotReady)
			return 0
		}
		return u.runPoll
	}
	if run.LabReadyAt.IsZero() {
		if recordErr := u.runs.SetLabReady(ctx, run.ID, now.UTC()); recordErr != nil {
			log.Warn().Err(recordErr).Msg("Recording the moment a run's lab was ready failed")
		}
	}
	target, err := u.labs.RunTarget(ctx, run.VersionID, run.VariantID, 0)
	if err != nil {
		log.Warn().Err(err).Msg("Reading the task of a run failed")
		return d.backoff()
	}
	if run.Opponent > 0 {
		return d.startingOpponent(ctx, run, target)
	}
	// A variant without devices has no lab to join: no client, the run gets its materials and answers only.
	if target.HasLab {
		if wait, done := d.access(ctx, run); !done {
			return wait
		}
	}
	if !d.materialsSent {
		switch outcome := d.sendMaterials(ctx, run); outcome {
		case startRefused:
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureStartRefused)
			return 0
		case startRetry:
			if d.down(ctx, run) {
				return 0
			}
			return d.backoff()
		}
		d.materialsSent = true
	}
	return d.started(ctx, run, d.startBody(run, target))
}

// started sends StartRun and acts on the answer: accepted marks the run running, a refusal fails it, anything else is tried
// again within the grace.
func (d *runDriver) started(ctx context.Context, run assistantModel.Run, body assistantModel.StartRun) time.Duration {
	u := d.u
	switch outcome := u.startAtBrain(ctx, body); outcome {
	case startAccepted:
		d.reachable(ctx, run)
		if _, err := u.runs.SetRunning(ctx, run.ID, u.now().UTC()); err != nil {
			log.Warn().Err(err).Msg("Marking a run as running failed")
			return d.backoff()
		}
		d.pause = 0
		return 0
	case startRefused:
		d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureStartRefused)
		return 0
	default:
		if d.down(ctx, run) {
			return 0
		}
		return d.backoff()
	}
}

// startingOpponent is starting for a run of an opponent of the manifest: the lab is ready (with the tools device in it). The
// run gets a VPN client of its own, waits for the subtask its opponent starts after to be solved in the lab,
// and is started with the manifest's role, band, goal and operates, the platform's budget and the VPN access of its client
// (the runner of the tools device is on the lab network). It gets no materials: an opponent plays against the lab, it does not read the participant's task.
func (d *runDriver) startingOpponent(ctx context.Context, run assistantModel.Run, target exerciseUseCase.RunTarget) time.Duration {
	u := d.u
	if run.Opponent > len(target.Opponents) {
		d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureStartRefused)
		return 0
	}
	opponent := target.Opponents[run.Opponent-1]
	if wait, done := d.access(ctx, run); !done {
		return wait
	}
	if !opponent.Start.Immediate() {
		solved, err := u.labs.RunSolvedSubtasks(ctx, run.Actor, run.DeployID)
		if err != nil {
			log.Warn().Err(err).Msg("Reading the solved subtasks of a lab failed")
			return d.backoff()
		}
		if !solved[opponent.Start.After] {
			return u.runPoll // the run is not started until the platform sees that subtask solved (ExpiresAt bounds the wait)
		}
	}
	addr, wait, ok := d.runnerAddr(ctx, run)
	if !ok {
		return wait
	}
	image, _ := imageAndKind(target.VariantsJS)
	body := assistantModel.StartRun{
		RunID: run.ID.String(), Role: string(opponent.Role), TaskRef: run.ExerciseID.String(), Version: run.VersionID.String(),
		Image: image, Access: assistantModel.VPNAccess(d.config).WithAddr(addr), Start: json.RawMessage(`"immediate"`),
		Band: opponent.Band, Goal: opponent.Goal, Operates: opponent.Operates, Foothold: opponent.Foothold, Brief: opponent.Brief, Budget: u.opponentBudget(),
	}
	return d.started(ctx, run, body)
}

// runnerAddr is host:port of the runner of the run's tools device on the control gateway: the /24 the laboratory gave the gateway
// plus the device's fixed host. ok is false when it cannot be known yet (the run is closed after a few tries) and wait says when
// to try again.
func (d *runDriver) runnerAddr(ctx context.Context, run assistantModel.Run) (addr string, wait time.Duration, ok bool) {
	host, err := d.u.labs.ControlAddress(ctx, run.Actor, run.DeployID)
	if err != nil {
		d.ctlFails++
		log.Warn().Err(err).Int("attempt", d.ctlFails).Msg("Reading the control address of the tools device failed")
		if d.ctlFails >= mintAttempts {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
			return "", 0, false
		}
		return "", d.backoff(), false
	}
	return net.JoinHostPort(host, strconv.Itoa(exerciseModel.RunnerPort)), 0, true
}

// access gives the run its own VPN client (recorded before it is made, so a crash leaves a client that is known and gets
// revoked). done is false when the step has to be left and tried again after wait, or the run was closed.
func (d *runDriver) access(ctx context.Context, run assistantModel.Run) (wait time.Duration, done bool) {
	u := d.u
	client := run.AccessClient
	if client == "" {
		client = runClientName(run.ID)
		if err := u.runs.SetAccessClient(ctx, run.ID, client, u.now().UTC()); err != nil {
			log.Warn().Err(err).Msg("Recording the VPN client of a run failed")
			return d.backoff(), false
		}
	}
	if d.config != "" {
		return 0, true
	}
	config, err := u.labs.MintLabClient(ctx, run.Actor, run.DeployID, client)
	if err != nil {
		d.mintFails++
		log.Warn().Err(err).Int("attempt", d.mintFails).Msg("Making the VPN client of a run failed")
		if d.mintFails >= mintAttempts {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
			return 0, false
		}
		return d.backoff(), false
	}
	d.config = config
	return 0, true
}

func (d *runDriver) startBody(run assistantModel.Run, target exerciseUseCase.RunTarget) assistantModel.StartRun {
	image, _ := imageAndKind(target.VariantsJS)
	access := assistantModel.NoAccess()
	if d.config != "" {
		access = assistantModel.VPNAccess(d.config)
	}
	return assistantModel.StartRun{
		RunID: run.ID.String(), Role: assistantModel.RoleGrader, TaskRef: run.ExerciseID.String(), Version: run.VersionID.String(),
		Image: image, Access: access, Start: json.RawMessage(`"immediate"`),
	}
}

// materialsAttempts is how often the upload of the materials is tried when the brain answers a server error.
const materialsAttempts = 3

// sendMaterials gives the brain the participant's view of the task before the run starts: the task as text and the
// attachments a participant downloads, built from what the lab and the task say. A server error is retried, and a refusal
// that cannot change (or the retries running out) fails the run; 409 means the brain has them already.
func (d *runDriver) sendMaterials(ctx context.Context, run assistantModel.Run) startOutcome {
	u := d.u
	m, err := u.labs.RunMaterials(ctx, run.Actor, run.DeployID)
	if err != nil {
		log.Warn().Err(err).Msg("Building the materials of a run failed")
		return startRefused
	}
	parts := []brain.Part{{Field: "task", Filename: "task.json", ContentType: "application/json", Data: m.Task}}
	for _, f := range m.Files {
		parts = append(parts, brain.Part{Field: "file", Filename: f.Name, ContentType: f.ContentType, Data: f.Data})
	}
	c, err := u.client(ctx)
	if err != nil {
		return startRetry
	}
	pause := u.runPause
	for attempt := 1; ; attempt++ {
		err = c.PostMultipart(ctx, "/v1/runs/"+run.ID.String()+"/materials", parts)
		var api *brain.APIError
		switch {
		case err == nil:
			return startAccepted
		case !errors.As(err, &api):
			return startRetry // the brain cannot be reached: the grace decides
		case api.Status == http.StatusConflict:
			return startAccepted
		case api.Status < http.StatusInternalServerError && api.Status != http.StatusTooManyRequests:
			log.Warn().Int("status", api.Status).Str("code", api.Code).Msg("The brain refused the materials of a run")
			return startRefused
		}
		if attempt >= materialsAttempts {
			log.Warn().Err(err).Msg("The materials of a run were not accepted")
			return startRefused
		}
		if !sleep(ctx, pause) {
			return startRetry
		}
		pause *= 2
	}
}

type startOutcome int

const (
	startAccepted startOutcome = iota
	startRefused
	startRetry
)

// startAtBrain sends StartRun. A refusal that cannot change (400, 401, 403, 404, 415, 422) fails the run; 409 means the
// brain has this run already (an earlier leader started it), which is as good as accepted; anything else is retried.
func (u *UseCase) startAtBrain(ctx context.Context, body assistantModel.StartRun) startOutcome {
	c, err := u.client(ctx)
	if err != nil {
		return startRetry
	}
	err = c.PostJSON(ctx, "/v1/runs", body)
	if err == nil {
		return startAccepted
	}
	var api *brain.APIError
	if errors.As(err, &api) {
		switch api.Status {
		case http.StatusConflict:
			return startAccepted
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			log.Warn().Int("status", api.Status).Str("code", api.Code).Msg("The brain refused a run")
			return startRefused
		}
	}
	log.Warn().Err(err).Msg("Starting a run at the brain failed")
	return startRetry
}

func runClientName(id uuid.UUID) string { return "g-" + strings.ReplaceAll(id.String(), "-", "")[:12] }

// running reads the brain's event stream until it ends and keeps what it says.
func (d *runDriver) running(ctx context.Context, run assistantModel.Run) time.Duration {
	u := d.u
	if !u.now().Before(run.ExpiresAt) {
		d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureTimeout)
		return 0
	}
	d.judgeStateTasks(ctx, run, false)
	d.sendUnsent(ctx, run)
	c, err := u.client(ctx)
	if err != nil {
		if d.down(ctx, run) {
			return 0
		}
		return d.backoff()
	}
	// The read never outlives the run: at its end the step above closes it.
	readCtx, cancel := context.WithDeadline(ctx, run.ExpiresAt)
	defer cancel()
	// A stop, or any other decision about the run, must not wait for a quiet stream to end: the read is cut at once.
	labLooked := time.Now() // the wall clock: the pace of a poll, not a moment of the platform's record
	go func() {
		t := time.NewTicker(u.runPoll)
		defer t.Stop()
		for {
			select {
			case <-readCtx.Done():
				return
			case <-t.C:
				if !u.now().Before(run.ExpiresAt) {
					cancel()
					return
				}
				if r, found, err := u.runs.Get(readCtx, run.ID); err == nil && (!found || r.State != assistantModel.RunRunning) {
					cancel()
					return
				}
				// A lab that is gone (destroyed by its author, its lease over, failed) ends the run: its client is revoked and
				// whatever is left of it destroyed now, not when the run's own window ends.
				if time.Since(labLooked) >= labWatchPolls*u.runPoll {
					labLooked = time.Now()
					if state, err := u.labs.TestLabState(readCtx, run.Actor, run.DeployID); err == nil && (state.Gone || state.Failed) {
						d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
						cancel()
						return
					}
				}
				// The state subtasks are judged by the lab: a pass is told as soon as it is seen.
				d.judgeStateTasks(readCtx, run, false)
				// A verdict that did not get through is sent again without waiting for the stream to end.
				d.sendUnsent(readCtx, run)
			}
		}
	}()
	resp, err := c.Do(readCtx, brain.Request{
		Method: http.MethodGet, Path: "/v1/runs/" + run.ID.String() + "/events",
		Query:  map[string][]string{"after": {strconv.FormatInt(run.LastSeq, 10)}},
		Header: http.Header{"Accept": {"text/event-stream"}},
	})
	if err != nil {
		var api *brain.APIError
		if errors.As(err, &api) && (api.Status == http.StatusNotFound || api.Status == http.StatusGone) {
			// The brain does not know the run (any more): nothing will come, the lab is not kept for it.
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureBrainFailed)
			return 0
		}
		if ctx.Err() != nil {
			return 0
		}
		if d.down(ctx, run) {
			return 0
		}
		return d.backoff()
	}
	defer func() { _ = resp.Body.Close() }()
	d.reachable(ctx, run)
	d.pause = 0
	err = brain.ReadFrames(resp.Body, func(f brain.Frame) error { return d.frame(readCtx, run, f) })
	if ctx.Err() != nil {
		return 0
	}
	if err != nil {
		log.Debug().Err(err).Msg("The run event stream ended with an error")
	}
	// A closed stream is not the end of the run: it starts the grace and is reopened after the last stored event.
	fresh, found, getErr := u.runs.Get(ctx, run.ID)
	if getErr != nil || !found || fresh.State != assistantModel.RunRunning {
		return 0
	}
	if d.down(ctx, fresh) {
		return 0
	}
	return d.backoff()
}

// frame stores one frame of the stream and acts on it.
func (d *runDriver) frame(ctx context.Context, run assistantModel.Run, f brain.Frame) error {
	seq, err := strconv.ParseInt(f.ID, 10, 64)
	if err != nil || seq <= 0 {
		return nil // a heartbeat, or a frame without a sequence number
	}
	if seq <= run.LastSeq {
		return nil // stored already
	}
	typ := f.Event
	if typ == "" {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(f.Data, &probe)
		typ = probe.Type
	}
	u, now := d.u, d.u.now().UTC()
	event := assistantModel.RunEvent{Seq: seq, Type: typ, Data: string(f.Data), At: now}
	switch {
	case typ == eventAnswer:
		// Judged and told before the frame is stored: a crash in between replays the frame, and the stored judgement
		// answers it again.
		if err = d.answer(ctx, run, seq, f.Data); err != nil {
			return err
		}
	case typ == eventRunFinished:
		var finished struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(f.Data, &finished)
		// The outcome is decided before the frame is stored, for the same reason; the measurement is already stored.
		if finished.Status == "done" {
			d.close(ctx, run, assistantModel.RunFinished, "")
		} else {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureBrainFailed)
		}
	case measurementEvents[typ]:
		if err = u.runs.SetMeasurement(ctx, run.ID, f.Data, now); err != nil {
			return err
		}
	}
	if _, err = u.runs.AppendEvent(ctx, run.ID, event, now); err != nil {
		return err
	}
	if typ == eventRunFinished {
		return errStreamDone
	}
	return nil
}

// errStreamDone ends the read after run_finished: the brain closes the stream anyway.
var errStreamDone = errors.New("the run is finished")

type answerEvent struct {
	SubtaskRef string          `json:"subtask_ref"`
	Value      json.RawMessage `json:"value"`
}

// valueText is the answer as text: a JSON string is its content, anything else its JSON.
func (a answerEvent) valueText() string {
	var text string
	if json.Unmarshal(a.Value, &text) == nil {
		return text
	}
	return string(a.Value)
}

// answer judges one answer of the run with the lab's flag checker and tells the brain whether it was right, never the
// expected value. The judgement is stored once per answer event and a retry sends the same verdict.
func (d *runDriver) answer(ctx context.Context, run assistantModel.Run, seq int64, data []byte) error {
	u := d.u
	var in answerEvent
	if err := json.Unmarshal(data, &in); err != nil {
		in = answerEvent{}
	}
	stored, found, err := u.runs.Answer(ctx, run.ID, seq)
	if err != nil {
		return err
	}
	if !found {
		if err = d.waitForAnswerSlot(ctx, run.ID, in.SubtaskRef); err != nil {
			return err
		}
		correct, judgeErr := d.judge(ctx, run, in)
		if judgeErr != nil {
			return judgeErr
		}
		at := u.now().UTC()
		if _, err = u.runs.AddAnswer(ctx, run.ID, seq, in.SubtaskRef, correct, at); err != nil {
			return err
		}
		if stored, found, err = u.runs.Answer(ctx, run.ID, seq); err != nil || !found {
			return errors.Join(err, errors.New("the judged answer was not stored"))
		}
	}
	if !stored.VerdictSent {
		d.sendVerdict(ctx, run, stored)
	}
	return nil
}

// judge asks the checker, retrying a platform failure until ctx ends. A lab that is gone judges nothing right.
func (d *runDriver) judge(ctx context.Context, run assistantModel.Run, in answerEvent) (bool, error) {
	pause := d.u.runPause
	for {
		correct, err := d.u.labs.JudgeAnswer(ctx, run.Actor, run.DeployID, in.SubtaskRef, in.valueText())
		if err == nil {
			return correct, nil
		}
		if errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()) {
			return false, nil
		}
		log.Warn().Err(err).Msg("Judging an answer failed; trying again")
		if !sleep(ctx, pause) {
			return false, ctx.Err()
		}
		pause = min(pause*2, pauseMax)
	}
}

// waitForAnswerSlot holds an answer back while the run submits faster than a participant may: the participant limits
// (per task and over all tasks) apply to the run, and an answer over them waits its turn instead of being refused.
func (d *runDriver) waitForAnswerSlot(ctx context.Context, run uuid.UUID, subtask string) error {
	for {
		now := d.u.now()
		recent, err := d.u.runs.RecentAnswers(ctx, run, challengeAttempt.TeamRateLimit.Since(now))
		if err != nil {
			return err
		}
		wait := AnswerDelay(recent, subtask, now)
		if wait <= 0 {
			return nil
		}
		if !sleep(ctx, wait) {
			return ctx.Err()
		}
	}
}

// AnswerDelay is how long an answer for the subtask has to wait: the participant limit per task (ChallengeRateLimit) and
// the one over all tasks of a team (TeamRateLimit), both counted on the run's own judged answers. Zero: now.
func AnswerDelay(recent []assistantModel.RunAnswer, subtask string, now time.Time) time.Duration {
	var same []assistantModel.RunAnswer
	for _, a := range recent {
		if a.SubtaskRef == subtask {
			same = append(same, a)
		}
	}
	return max(limitDelay(same, challengeAttempt.ChallengeRateLimit, now), limitDelay(recent, challengeAttempt.TeamRateLimit, now))
}

// limitDelay is how long until the sliding window has room: at the limit, the oldest of the latest Attempts leaves it.
func limitDelay(answers []assistantModel.RunAnswer, limit challengeAttempt.RateLimit, now time.Time) time.Duration {
	since := limit.Since(now)
	var inside []time.Time
	for _, a := range answers {
		if a.At.After(since) {
			inside = append(inside, a.At)
		}
	}
	if int32(len(inside)) < limit.Attempts { //nolint:gosec // a handful of answers
		return 0
	}
	sort.Slice(inside, func(i, j int) bool { return inside[i].Before(inside[j]) })
	oldest := inside[len(inside)-int(limit.Attempts)]
	return max(oldest.Add(limit.Window).Sub(now), time.Millisecond)
}

// sendVerdict tells the brain whether an answer was right. A refusal that cannot change counts as delivered; a failure
// leaves the verdict unsent for the next pass. The body is the stored judgement, so every retry says the same.
func (d *runDriver) sendVerdict(ctx context.Context, run assistantModel.Run, a assistantModel.RunAnswer) {
	u := d.u
	d.verdicts.Lock()
	defer d.verdicts.Unlock()
	if stored, found, err := u.runs.Answer(ctx, run.ID, a.Seq); err == nil && found && stored.VerdictSent {
		return // told already, by the other sender
	}
	c, err := u.client(ctx)
	if err != nil {
		return
	}
	body := assistantModel.Verdict{SubtaskRef: a.SubtaskRef, Correct: a.Correct, At: a.At.UTC().Format(time.RFC3339Nano)}
	pause := u.runPause
	for attempt := 1; attempt <= verdictAttempts; attempt++ {
		err = c.PostJSON(ctx, "/v1/runs/"+run.ID.String()+"/verdict", body)
		if err == nil || finalRefusal(err) {
			if markErr := u.runs.SetAnswerSent(ctx, run.ID, a.Seq); markErr != nil {
				log.Warn().Err(markErr).Msg("Recording a delivered verdict failed")
			}
			return
		}
		if attempt < verdictAttempts && !sleep(ctx, pause) {
			return
		}
		pause *= 2
	}
	log.Warn().Err(err).Msg("A verdict was not delivered; it is sent again on the next pass")
}

// finalRefusal says the brain answered with a status that retrying cannot change.
func finalRefusal(err error) bool {
	var api *brain.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// sendUnsent sends again the verdicts the brain was not told (a failed send, a leader that changed in between).
func (d *runDriver) sendUnsent(ctx context.Context, run assistantModel.Run) {
	unsent, err := d.u.runs.Unsent(ctx, run.ID)
	if err != nil {
		log.Warn().Err(err).Msg("Listing the undelivered verdicts failed")
		return
	}
	for _, a := range unsent {
		d.sendVerdict(ctx, run, a)
	}
}

// labWatchPolls is how many polls pass between two looks of a running run at its lab (to see that it is still there).
const labWatchPolls = 5

// labAnswerWindow is how long a lab whose answer does not get through is kept before it is closed.
const labAnswerWindow = labReadyTimeout

// bestEffort bounds a call to the brain that the teardown must not wait on.
const bestEffort = 15 * time.Second

// closing tears the run down. Nothing here depends on the brain: the brain is told to stop and a refusal is sent for a lab
// request that was not answered, each at most once and within bestEffort, and whatever they say the run's VPN client is
// revoked and the lab destroyed. A step that fails on the platform's side is tried again on the next pass (the other
// step is still tried), so a lab or a key is never kept alive by a brain that is down.
func (d *runDriver) closing(ctx context.Context, run assistantModel.Run) time.Duration {
	u := d.u
	if assistantModel.IsLabRole(run.Role) && run.RequestID != "" && !run.Answered && !d.refused {
		d.refused = true
		d.refuseLab(ctx, run)
	}
	if !d.stopSent && !assistantModel.IsLabRole(run.Role) && !run.StartedAt.IsZero() && run.EndState != assistantModel.RunFinished {
		d.stopSent = true
		d.stopAtBrain(ctx, run)
	}
	if !d.stateFlushed && run.Role == assistantModel.RoleGrader && !run.StartedAt.IsZero() {
		d.stateFlushed = true
		d.flushStateTasks(ctx, run)
	}
	// What the lab did is read while the lab is still known to the laboratory and the platform; the figures stay on the run.
	if run.Role == assistantModel.RoleGrader && !run.StartedAt.IsZero() && run.LabActivity == nil {
		raw, done := d.captureActivity(ctx, run)
		if !done {
			return d.backoff()
		}
		run.LabActivity = raw
	}
	failed := false
	if !d.revoked {
		if err := u.labs.RevokeLabClient(ctx, run.Actor, run.DeployID, run.AccessClient); err != nil {
			log.Warn().Err(err).Msg("Revoking the VPN client of a run failed; trying again")
			failed = true
		} else {
			d.revoked = true
		}
	}
	if err := u.labs.DestroyDeployTest(ctx, run.Actor, run.DeployID); err != nil && !errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()) {
		log.Warn().Err(err).Msg("Destroying the lab of a run failed; trying again")
		failed = true
	}
	if failed {
		return d.backoff()
	}
	// The lab facts are not read from the lab (it is gone by now) but from what was recorded, so the brain being down only
	// delays them: a few passes are spent on it, then the run is finished without them.
	if run.Role == assistantModel.RoleGrader && !run.StartedAt.IsZero() && !d.factsSent && d.factsTries < labFactsPasses {
		d.factsTries++
		if !d.sendLabFacts(ctx, run) {
			return d.backoff()
		}
	}
	if _, err := u.runs.Finish(ctx, run.ID, u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Finishing a run failed; trying again")
		return d.backoff()
	}
	return 0
}

// activityPasses is how many passes of the teardown try to read the lab's activity before the run goes on without it.
const activityPasses = 3

// captureActivity reads the laboratory's activity reports of the run's lab (egress of the lab, the VPN bytes, the lab addresses
// and the console minutes of the run's own client) and keeps them on the run, so its lab facts can say them after the lab is
// gone. Reports that are off, a lab the laboratory does not know, or a failing read after a few passes are recorded as "not
// reported": the brain then gets null for them, never a guess.
func (d *runDriver) captureActivity(ctx context.Context, run assistantModel.Run) (raw []byte, done bool) {
	u := d.u
	ctx, cancel := context.WithTimeout(ctx, bestEffort)
	defer cancel()
	notReported := assistantModel.RunLabActivity{}
	figures := notReported
	if u.activity != nil {
		d.activityTries++
		group, lab, err := u.labs.RunLabRef(ctx, run.Actor, run.DeployID)
		if err == nil {
			if err = u.activity.Refresh(ctx, group); err == nil {
				var f *labActivityUseCase.LabFigures
				if f, err = u.activity.OfLab(ctx, group, lab, run.AccessClient); err == nil && f != nil {
					figures = assistantModel.RunLabActivity{Reported: true, EgressUsed: f.EgressUsed, VPNBytes: f.VPNBytes, DevicesTouched: f.DevicesTouched, ConsoleMinutes: f.ConsoleMinutes}
				}
			}
		}
		if err != nil && !errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()) && d.activityTries < activityPasses {
			log.Warn().Err(err).Msg("Reading the activity of a run's lab failed; trying again")
			return nil, false
		}
	}
	raw, err := json.Marshal(figures)
	if err != nil {
		return nil, true
	}
	if err = u.runs.SetLabActivity(ctx, run.ID, raw, u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Recording the activity of a run's lab failed; trying again")
		return nil, false
	}
	return raw, true
}

// labFactsPasses is how many passes of the teardown try to deliver the lab facts before the run is finished without them.
const labFactsPasses = 5

// sendLabFacts tells the brain the lab-side facts of a grader run (`POST /v1/runs/{run_id}/lab_facts`): the time the lab took
// to come up, its restarts, whether the topology has Internet egress, and the laboratory's activity figures (not reported
// yet: null). They are the single-run counterpart of the lab_health and effort figures of a participants' aggregate. The
// call is idempotent at the brain (a repeat is a 409, which counts as delivered). It reports whether the brain has them,
// or nothing can be said (no recorded ready time), so the run does not wait for them.
func (d *runDriver) sendLabFacts(ctx context.Context, run assistantModel.Run) bool {
	u := d.u
	if run.LabReadyAt.IsZero() {
		d.factsSent = true // a run from before the figure was recorded
		return true
	}
	target, err := u.labs.RunTarget(ctx, run.VersionID, run.VariantID, 0)
	if err != nil {
		log.Warn().Err(err).Msg("Reading the task of a run for its lab facts failed")
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, bestEffort)
	defer cancel()
	c, err := u.client(ctx)
	if err != nil {
		return false
	}
	body := assistantModel.LabFacts{
		ReadyTimeS: max(run.LabReadyAt.Sub(run.CreatedAt).Seconds(), 0), Restarts: 0, EgressEnabled: target.EgressEnabled,
	}
	// The activity figures are the laboratory's, read before the lab was destroyed; null while they are not reported. Egress is
	// the lab's own, never a device's or a person's.
	var activity assistantModel.RunLabActivity
	if len(run.LabActivity) > 0 && json.Unmarshal(run.LabActivity, &activity) == nil && activity.Reported {
		body.EgressUsed, body.VPNBytes, body.DevicesTouched, body.ConsoleMinutes = &activity.EgressUsed, activity.VPNBytes, activity.DevicesTouched, &activity.ConsoleMinutes
	}
	err = c.PostJSON(ctx, "/v1/runs/"+run.ID.String()+"/lab_facts", body)
	if err == nil || finalRefusal(err) {
		d.factsSent = true
		return true
	}
	log.Warn().Err(err).Msg("The lab facts of a run were not delivered; trying again")
	return false
}

// stopAtBrain sends StopRun best effort: an unreachable brain must not hold the lab, so it is tried a few times and then
// left (the brain ends the run itself when its stream is not read).
func (d *runDriver) stopAtBrain(ctx context.Context, run assistantModel.Run) {
	ctx, cancel := context.WithTimeout(ctx, bestEffort)
	defer cancel()
	c, err := d.u.client(ctx)
	if err != nil {
		return
	}
	pause := d.u.runPause
	for attempt := 1; attempt <= stopAttempts; attempt++ {
		err = c.PostJSON(ctx, "/v1/runs/"+run.ID.String()+"/stop", struct{}{})
		if err == nil || finalRefusal(err) {
			return
		}
		if attempt < stopAttempts && !sleep(ctx, pause) {
			return
		}
		pause *= 2
	}
	log.Warn().Err(err).Msg("The brain was not told to stop a run; the lab is torn down anyway")
}

// lab follows a lab the brain asked for: it waits for the lab, makes the WireGuard client and answers the outbox request,
// then follows the lab until it is gone; at the end the client is revoked and the lab destroyed. A request that cannot
// be served (the lab fails, or is not ready in time) is answered with the refusal. Everything is in the row, so a new
// leader goes on: a lab that was not answered yet gets its client made again (the old one is replaced) and the answer.
func (d *runDriver) lab(ctx context.Context, run assistantModel.Run) time.Duration {
	u, now := d.u, d.u.now()
	switch run.State {
	case assistantModel.RunStarting:
		if now.Sub(run.CreatedAt) > labReadyTimeout {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabNotReady)
			return 0
		}
		state, err := u.labs.TestLabState(ctx, run.Actor, run.DeployID)
		switch {
		case err != nil:
			log.Warn().Err(err).Msg("Reading the lab of the brain failed")
			return d.backoff()
		case state.Gone || state.Failed:
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
			return 0
		case !state.Ready:
			return u.runPoll
		}
		return d.labReady(ctx, run)
	case assistantModel.RunRunning:
		// The end of the lab comes first, whatever the brain does: an expired lab, or one whose answer has been trying for
		// longer than the lab may take to come up, is closed (its request is then refused once, best effort).
		unanswered := !run.Answered && run.RequestID != ""
		if !now.Before(run.ExpiresAt) || unanswered && now.Sub(run.StartedAt) > labAnswerWindow {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabNotReady)
			return 0
		}
		state, err := u.labs.TestLabState(ctx, run.Actor, run.DeployID)
		switch {
		case err != nil:
			log.Warn().Err(err).Msg("Reading the lab of the brain failed")
			return d.backoff()
		case state.Gone || state.Failed:
			d.close(ctx, run, assistantModel.RunFinished, "")
			return 0
		}
		if unanswered {
			return d.labAnswer(ctx, run)
		}
		d.pause = 0
		return u.runPoll
	default:
		return d.closing(ctx, run)
	}
}

// labReady makes the lab's client and marks the lab running; the answer follows.
func (d *runDriver) labReady(ctx context.Context, run assistantModel.Run) time.Duration {
	u := d.u
	client := run.AccessClient
	if client == "" {
		client = runClientName(run.ID)
		// Recorded before it is made, so a crash leaves a client that is known and gets revoked.
		if err := u.runs.SetAccessClient(ctx, run.ID, client, u.now().UTC()); err != nil {
			log.Warn().Err(err).Msg("Recording the VPN client of a lab failed")
			return d.backoff()
		}
	}
	if err := d.mint(ctx, run, client); err != nil {
		d.mintFails++
		log.Warn().Err(err).Int("attempt", d.mintFails).Msg("Making the VPN client of a lab failed")
		if d.mintFails >= mintAttempts {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
			return 0
		}
		return d.backoff()
	}
	if _, err := u.runs.SetRunning(ctx, run.ID, u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Marking a lab as running failed")
		return d.backoff()
	}
	return 0
}

func (d *runDriver) mint(ctx context.Context, run assistantModel.Run, client string) error {
	if d.config != "" {
		return nil
	}
	config, err := d.u.labs.MintLabClient(ctx, run.Actor, run.DeployID, client)
	if err != nil {
		return err
	}
	d.config = config
	return nil
}

// labAnswer sends the result of the request the lab was made for, and records it as delivered.
func (d *runDriver) labAnswer(ctx context.Context, run assistantModel.Run) time.Duration {
	u := d.u
	if err := d.mint(ctx, run, run.AccessClient); err != nil {
		log.Warn().Err(err).Msg("Making the VPN client of a lab again failed")
		return d.backoff()
	}
	access := assistantModel.VPNAccess(d.config)
	if run.Role == assistantModel.RoleTools {
		addr, wait, ok := d.runnerAddr(ctx, run)
		if !ok {
			return wait
		}
		access = access.WithAddr(addr)
	}
	result := map[string]any{"device_ref": run.DeployID.String(), "access": access}
	if err := u.answer(ctx, run.RequestID, result, nil); err != nil {
		log.Warn().Err(err).Msg("Answering a lab request failed; trying again")
		return d.backoff()
	}
	if err := u.runs.SetAnswered(ctx, run.ID, u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Recording an answered lab request failed")
		return d.backoff()
	}
	d.pause = 0
	return 0
}

// refuseLab answers the request of a lab that ended before it was answered, once and within bestEffort: the teardown does
// not wait for the brain. A refusal that did not get through leaves the request unanswered; the brain asks again.
func (d *runDriver) refuseLab(ctx context.Context, run assistantModel.Run) {
	ctx, cancel := context.WithTimeout(ctx, bestEffort)
	defer cancel()
	if err := d.u.answer(ctx, run.RequestID, nil, labRefusal(run.FailureReason)); err != nil {
		log.Warn().Err(err).Msg("Refusing a lab request failed; the lab is torn down anyway")
		return
	}
	if err := d.u.runs.SetAnswered(ctx, run.ID, d.u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Recording a refused lab request failed")
	}
}

// stateSeq is the sequence number a state subtask's verdict is kept under: the brain's own answers have positive ones, so
// the verdicts the platform decides itself take the negative numbers, one per state subtask in the variant's order.
func stateSeq(index int) int64 { return -int64(index) - 1 }

// judgeStateTasks tells the brain, once per state subtask, that its check passed. The brain submits nothing for such a
// subtask and only acts; the lab's check decides, from the results the laboratory recorded for the run's lab. With final
// set (the run is ending) a subtask that never passed is told as not correct. A verdict is sent only when the subtask
// changes from nothing to a result, never again.
func (d *runDriver) judgeStateTasks(ctx context.Context, run assistantModel.Run, final bool) {
	if run.Role != assistantModel.RoleGrader {
		return
	}
	u := d.u
	tasks, err := u.labs.RunStateTasks(ctx, run.Actor, run.DeployID)
	if err != nil {
		log.Warn().Err(err).Msg("Reading the state subtasks of a run failed")
		return
	}
	for i, task := range tasks {
		if !task.Passed && !final {
			continue
		}
		seq := stateSeq(i)
		if _, found, readErr := u.runs.Answer(ctx, run.ID, seq); readErr != nil || found {
			continue // told already (or the store is down: the next pass tries again)
		}
		if _, addErr := u.runs.AddAnswer(ctx, run.ID, seq, task.Key, task.Passed, u.now().UTC()); addErr != nil {
			log.Warn().Err(addErr).Msg("Recording the verdict of a state subtask failed")
			continue
		}
		if stored, found, readErr := u.runs.Answer(ctx, run.ID, seq); readErr == nil && found {
			d.sendVerdict(ctx, run, stored)
		}
	}
}

// flushStateTasks is the last look before the teardown: every state subtask that did not pass is told as not correct, and
// the verdicts the brain has not got yet are sent, once and within bestEffort (the teardown does not wait on the brain).
func (d *runDriver) flushStateTasks(ctx context.Context, run assistantModel.Run) {
	ctx, cancel := context.WithTimeout(ctx, bestEffort)
	defer cancel()
	d.judgeStateTasks(ctx, run, true)
	d.sendUnsent(ctx, run)
}
