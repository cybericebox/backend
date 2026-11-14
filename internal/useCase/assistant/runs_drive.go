package assistant

import (
	"context"
	"encoding/json"
	"errors"
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

// The brain's final aggregate of a run: its last event of one of these types is the measurement.
var measurementEvents = map[string]bool{"measure": true, "aggregate": true}

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
	stopSent  bool
	revoked   bool
	pause     time.Duration
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
	client := run.AccessClient
	if client == "" {
		client = runClientName(run.ID)
		// Recorded before it is made, so a crash leaves a client that is known and gets revoked.
		if err = u.runs.SetAccessClient(ctx, run.ID, client, now.UTC()); err != nil {
			log.Warn().Err(err).Msg("Recording the VPN client of a run failed")
			return d.backoff()
		}
	}
	if d.config == "" {
		if d.config, err = u.labs.MintLabClient(ctx, run.Actor, run.DeployID, client); err != nil {
			d.config = ""
			d.mintFails++
			log.Warn().Err(err).Int("attempt", d.mintFails).Msg("Making the VPN client of a run failed")
			if d.mintFails >= mintAttempts {
				d.close(ctx, run, assistantModel.RunFailed, assistantModel.FailureLabFailed)
				return 0
			}
			return d.backoff()
		}
	}
	body, err := d.startBody(ctx, run)
	if err != nil {
		log.Warn().Err(err).Msg("Reading the task of a run failed")
		return d.backoff()
	}
	switch outcome := u.startAtBrain(ctx, body); outcome {
	case startAccepted:
		d.reachable(ctx, run)
		if _, err = u.runs.SetRunning(ctx, run.ID, u.now().UTC()); err != nil {
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

func (d *runDriver) startBody(ctx context.Context, run assistantModel.Run) (assistantModel.StartRun, error) {
	target, err := d.u.labs.RunTarget(ctx, run.VersionID, run.VariantID, 0)
	if err != nil {
		return assistantModel.StartRun{}, err
	}
	image, _ := imageAndKind(target.VariantsJS)
	return assistantModel.StartRun{
		RunID: run.ID.String(), Role: assistantModel.RoleGrader, TaskRef: run.ExerciseID.String(), Version: run.VersionID.String(),
		Image: image, Access: assistantModel.VPNAccess(d.config), Start: json.RawMessage(`"immediate"`),
	}, nil
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

// closing tears the run down: the brain is told to stop (unless it said it finished), the run's VPN client is revoked and
// the lab is destroyed. A step that fails is tried again; nothing is skipped, so a lab is never left behind.
func (d *runDriver) closing(ctx context.Context, run assistantModel.Run) time.Duration {
	u := d.u
	if !d.stopSent && !run.StartedAt.IsZero() && run.EndState != assistantModel.RunFinished {
		d.stopAtBrain(ctx, run)
		d.stopSent = true
	}
	if !d.revoked {
		if err := u.labs.RevokeLabClient(ctx, run.Actor, run.DeployID, run.AccessClient); err != nil {
			log.Warn().Err(err).Msg("Revoking the VPN client of a run failed; trying again")
			return d.backoff()
		}
		d.revoked = true
	}
	if err := u.labs.DestroyDeployTest(ctx, run.Actor, run.DeployID); err != nil && !errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()) {
		log.Warn().Err(err).Msg("Destroying the lab of a run failed; trying again")
		return d.backoff()
	}
	if _, err := u.runs.Finish(ctx, run.ID, u.now().UTC()); err != nil {
		log.Warn().Err(err).Msg("Finishing a run failed; trying again")
		return d.backoff()
	}
	return 0
}

// stopAtBrain sends StopRun best effort: an unreachable brain must not hold the lab, so it is tried a few times and then
// left (the brain ends the run itself when its stream is not read).
func (d *runDriver) stopAtBrain(ctx context.Context, run assistantModel.Run) {
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
