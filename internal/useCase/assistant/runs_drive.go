package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/model/challengeAttempt"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
	labActivityUseCase "github.com/cybericebox/backend/internal/useCase/labActivity"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
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
func (d *runDriver) started(ctx context.Context, run assistantModel.Run, body *brainapiv1.StartRunRequest) time.Duration {
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
	// The lab-side inputs of the spec are filled here: VPN_ENTRY is the run's own client, CONTROL_ADDR the runner of its tools
	// device, FOOTHOLD the manifest's segment (the kinds were checked when the spec was taken, before the lab was deployed).
	body := &brainapiv1.StartRunRequest{
		RunId: run.ID.String(), Role: string(opponent.Role), TaskRef: run.ExerciseID.String(), Version: run.VersionID.String(),
		Image: image, Access: vpnAccess(d.config, addr), Start: immediateStart(),
		Band: uint32(max(opponent.Band, 0)), Goal: opponent.Goal, Operates: opponent.Operates, Foothold: opponent.Foothold, Brief: opponent.Brief,
		Budget: u.opponentBudget(), SpecId: run.SpecID,
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

func (d *runDriver) startBody(run assistantModel.Run, target exerciseUseCase.RunTarget) *brainapiv1.StartRunRequest {
	image, _ := imageAndKind(target.VariantsJS)
	access := noAccess()
	if d.config != "" {
		access = vpnAccess(d.config, "")
	}
	return &brainapiv1.StartRunRequest{
		RunId: run.ID.String(), Role: assistantModel.RoleGrader, TaskRef: run.ExerciseID.String(), Version: run.VersionID.String(),
		Image: image, Access: access, Start: immediateStart(),
	}
}

// vpnAccess is Access.vpn: the full WireGuard configuration of the run's own client and, for a tools-device runner, its
// address on the control gateway ("": a plain participant vantage).
func vpnAccess(entry, addr string) *brainapiv1.Access {
	return &brainapiv1.Access{Kind: &brainapiv1.Access_Vpn_{Vpn: &brainapiv1.Access_Vpn{Entry: entry, Addr: addr}}}
}

// noAccess is Access.none: a run that joins no lab (only its materials and answers).
func noAccess() *brainapiv1.Access {
	return &brainapiv1.Access{Kind: &brainapiv1.Access_None_{None: &brainapiv1.Access_None{}}}
}

func immediateStart() *brainapiv1.Start {
	return &brainapiv1.Start{When: &brainapiv1.Start_Immediate{Immediate: &emptypb.Empty{}}}
}

// materialsAttempts is how often the upload of the materials is tried while the brain rate limits it.
const materialsAttempts = 3

// sendMaterials gives the brain the participant's view of the task before the run starts (UploadMaterials): the task as text
// and the attachments a participant downloads, built from what the lab and the task say. An unreachable brain is left to the
// grace, a rate limit is retried (with the delay the brain asks for), and a refusal that cannot change (or the retries running
// out) fails the run; AlreadyExists means the brain has them already.
func (d *runDriver) sendMaterials(ctx context.Context, run assistantModel.Run) startOutcome {
	u := d.u
	m, err := u.labs.RunMaterials(ctx, run.Actor, run.DeployID)
	if err != nil {
		log.Warn().Err(err).Msg("Building the materials of a run failed")
		return startRefused
	}
	files := make([]brain.File, 0, len(m.Files))
	for _, f := range m.Files {
		files = append(files, brain.File{Name: f.Name, ContentType: f.ContentType, Data: f.Data})
	}
	c, err := u.control(ctx)
	if err != nil {
		return startRetry
	}
	err = brain.Retry(ctx, materialsAttempts, u.runPause, func(ctx context.Context) error {
		err := c.UploadMaterials(ctx, run.ID.String(), m.Task, files)
		if brain.Code(err) == codes.ResourceExhausted {
			return err // retried here
		}
		return noRetry(err)
	})
	return startOutcomeOf(err, "The brain refused the materials of a run")
}

// noRetry keeps an error from Retry (the caller decides about an unreachable brain itself).
func noRetry(err error) error {
	if err == nil {
		return nil
	}
	return finalErr{err}
}

// finalErr is an error Retry does not try again.
type finalErr struct{ error }

func (e finalErr) Unwrap() error { return e.error }

// startOutcomeOf maps the answer of a call that starts a run: success, a brain that cannot be reached now (the grace decides),
// or a refusal that cannot change. A rate limit the retries did not get past refuses the run.
func startOutcomeOf(err error, refused string) startOutcome {
	var final finalErr
	if errors.As(err, &final) {
		err = final.error
	}
	switch code := brain.Code(err); {
	case err == nil:
		return startAccepted
	case code == codes.Unavailable || code == codes.DeadlineExceeded || code == codes.Unknown || code == codes.Canceled:
		return startRetry
	default:
		log.Warn().Str("code", code.String()).Str("reason", brain.Reason(err)).Msg(refused)
		return startRefused
	}
}

type startOutcome int

const (
	startAccepted startOutcome = iota
	startRefused
	startRetry
)

// startAtBrain sends StartRun. AlreadyExists means the brain has this run already (an earlier leader started it), which is as
// good as accepted; an unreachable brain (Unavailable, the network) is retried within the grace and so is a rate limit; any
// other code is a refusal that cannot change and fails the run.
func (u *UseCase) startAtBrain(ctx context.Context, body *brainapiv1.StartRunRequest) startOutcome {
	c, err := u.control(ctx)
	if err != nil {
		return startRetry
	}
	err = c.StartRun(ctx, body)
	if brain.Code(err) == codes.ResourceExhausted {
		return startRetry
	}
	return startOutcomeOf(err, "The brain refused a run")
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
	c, err := u.control(ctx)
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
	stream, err := c.RunEvents(readCtx, run.ID.String(), uint64(max(run.LastSeq, 0)))
	var event *brainapiv1.RunEvent
	if err == nil {
		// A server stream reports its refusal (NotFound) with the first read.
		event, err = stream.Recv()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if brain.Code(err) == codes.NotFound {
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
	d.reachable(ctx, run)
	d.pause = 0
	// last is the newest sequence number stored by this read: a brain that re-sends what it sent is deduped.
	last := run.LastSeq
	for err == nil {
		if err = d.frame(readCtx, run, &last, event); err == nil {
			event, err = stream.Recv()
		}
	}
	if ctx.Err() != nil {
		return 0
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, errStreamDone) {
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

// frame stores one event of the stream and acts on it. last is the newest sequence number stored: an event at or below it is
// a repeat (the stream was reopened after it, or the brain sent it twice) and is dropped.
func (d *runDriver) frame(ctx context.Context, run assistantModel.Run, last *int64, f *brainapiv1.RunEvent) error {
	if f.GetSeq() == 0 || f.GetSeq() > maxSeq {
		return nil // an event without a usable sequence number
	}
	seq := int64(f.GetSeq())
	if seq <= *last {
		return nil // stored already
	}
	typ := f.GetType()
	data := f.GetData()
	if typ == "" {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &probe)
		typ = probe.Type
	}
	u, now := d.u, d.u.now().UTC()
	event := assistantModel.RunEvent{Seq: seq, Type: typ, Data: string(data), At: now}
	var err error
	switch {
	case typ == eventAnswer:
		// Judged and told before the event is stored: a crash in between replays the event, and the stored judgement
		// answers it again.
		if err = d.answer(ctx, run, seq, data); err != nil {
			return err
		}
	case typ == eventRunFinished:
		var finished struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(data, &finished)
		// The outcome is decided before the frame is stored, for the same reason; the measurement is already stored.
		if finished.Status == "done" {
			d.close(ctx, run, assistantModel.RunFinished, "")
		} else {
			d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureBrainFailed)
		}
	case measurementEvents[typ]:
		if err = u.runs.SetMeasurement(ctx, run.ID, data, now); err != nil {
			return err
		}
	}
	if _, err = u.runs.AppendEvent(ctx, run.ID, event, now); err != nil {
		return err
	}
	*last = seq
	if typ == eventRunFinished {
		return errStreamDone
	}
	return nil
}

// errStreamDone ends the read after run_finished: the brain closes the stream anyway. Only run_finished ends a run; a stream
// that closes before it is reopened.
var errStreamDone = errors.New("the run is finished")

// maxSeq is the largest sequence number the run's store keeps (a signed 64-bit column).
const maxSeq = 1<<63 - 1

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
	c, err := u.control(ctx)
	if err != nil {
		return
	}
	// answer_seq is the seq of the answer event: the idempotency key of the verdict with run_id. A state subtask's verdict has no
	// answer event; its stored key is negative and goes as its two's complement, which no event of the brain reaches.
	body := &brainapiv1.VerdictRequest{
		RunId: run.ID.String(), AnswerSeq: uint64(a.Seq), SubtaskRef: a.SubtaskRef, Correct: a.Correct, //nolint:gosec // see above
		At: timestamppb.New(a.At.UTC()),
	}
	err = brain.Retry(ctx, verdictAttempts, u.runPause, func(ctx context.Context) error { return c.PostVerdict(ctx, body) })
	if err == nil || finalRefusal(err) {
		if markErr := u.runs.SetAnswerSent(ctx, run.ID, a.Seq); markErr != nil {
			log.Warn().Err(markErr).Msg("Recording a delivered verdict failed")
		}
		return
	}
	log.Warn().Err(err).Msg("A verdict was not delivered; it is sent again on the next pass")
}

// finalRefusal says the brain answered with a code that no retry can change (the request is wrong, or the run is gone): the
// call counts as done. An unreachable brain, a rate limit, a refused certificate or an internal error of the brain are not
// final: the next pass tries again.
func finalRefusal(err error) bool {
	switch brain.Code(err) {
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition, codes.OutOfRange:
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

// sendLabFacts tells the brain the lab-side facts of a grader run (PostLabFacts, best effort within 5 s): the time the lab took
// to come up, its restarts, whether the topology has Internet egress, and the laboratory's activity figures (not reported:
// the optional fields are left unset). They are the single-run counterpart of the lab_health and effort figures of a
// participants' aggregate. The call is idempotent at the brain (a repeat is AlreadyExists, which counts as delivered). It reports whether the brain has them,
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
	c, err := u.control(ctx)
	if err != nil {
		return false
	}
	err = c.PostLabFacts(ctx, LabFactsRequest(run, target.EgressEnabled))
	if err == nil || finalRefusal(err) {
		d.factsSent = true
		return true
	}
	log.Warn().Err(err).Msg("The lab facts of a run were not delivered; trying again")
	return false
}

// LabFactsRequest is the PostLabFacts of a grader run: the time its lab took to come up, its restarts, the topology's egress,
// and the activity figures of its lab read before the lab was destroyed, left unset (not measured) while the laboratory does
// not report them. Egress is the lab's own, never a device's or a person's.
func LabFactsRequest(run assistantModel.Run, egressEnabled bool) *brainapiv1.LabFactsRequest {
	body := &brainapiv1.LabFactsRequest{
		RunId: run.ID.String(), ReadyTimeS: max(run.LabReadyAt.Sub(run.CreatedAt).Seconds(), 0), Restarts: 0, EgressEnabled: egressEnabled,
	}
	var activity assistantModel.RunLabActivity
	if len(run.LabActivity) > 0 && json.Unmarshal(run.LabActivity, &activity) == nil && activity.Reported {
		body.EgressUsed, body.VpnBytes, body.DevicesTouched, body.ConsoleMinutes = &activity.EgressUsed, activity.VPNBytes, activity.DevicesTouched, &activity.ConsoleMinutes
	}
	return body
}

// stopAtBrain sends StopRun best effort (5 s a call; NotFound is a success): an unreachable brain must not hold the lab, so it
// is tried a few times within bestEffort and then left (the brain ends the run itself when its stream is not read).
func (d *runDriver) stopAtBrain(ctx context.Context, run assistantModel.Run) {
	ctx, cancel := context.WithTimeout(ctx, bestEffort)
	defer cancel()
	c, err := d.u.control(ctx)
	if err != nil {
		return
	}
	err = brain.Retry(ctx, stopAttempts, d.u.runPause, func(ctx context.Context) error { return c.StopRun(ctx, run.ID.String()) })
	if err == nil || finalRefusal(err) {
		return
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
		if _, again := u.reanswer.LoadAndDelete(run.ID); again {
			// The brain asked again: the answer it was sent was lost with a stream.
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
	addr := ""
	if run.Role == assistantModel.RoleTools {
		var wait time.Duration
		var ok bool
		if addr, wait, ok = d.runnerAddr(ctx, run); !ok {
			return wait
		}
	}
	result := &brainapiv1.RequestAnswer{Result: &brainapiv1.RequestAnswer_Device{Device: &brainapiv1.DeviceResult{
		DeviceRef: run.DeployID.String(), Access: vpnAccess(d.config, addr),
	}}}
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
