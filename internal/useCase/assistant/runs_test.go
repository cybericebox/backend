package assistant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain/brainfake"
	"github.com/cybericebox/daemon/internal/delivery/repository/assistantRunRepo"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	"github.com/cybericebox/daemon/internal/model/rbac"
	userModel "github.com/cybericebox/daemon/internal/model/user"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

// memRuns is the run store in memory.
type memRuns struct {
	mu      sync.Mutex
	runs    map[uuid.UUID]assistantModel.Run
	order   []uuid.UUID
	events  map[uuid.UUID][]assistantModel.RunEvent
	answers map[uuid.UUID][]assistantModel.RunAnswer
	users   map[uuid.UUID]uuid.UUID
}

func newMemRuns() *memRuns {
	return &memRuns{runs: map[uuid.UUID]assistantModel.Run{}, events: map[uuid.UUID][]assistantModel.RunEvent{}, answers: map[uuid.UUID][]assistantModel.RunAnswer{}, users: map[uuid.UUID]uuid.UUID{}}
}

func (m *memRuns) Create(_ context.Context, run assistantModel.Run, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if run.Role == assistantModel.RoleGrader && r.Role == assistantModel.RoleGrader && r.VariantID == run.VariantID && r.State.Active() {
			return assistantRunRepo.ErrActive
		}
	}
	m.runs[run.ID] = run
	m.order = append(m.order, run.ID)
	return nil
}
func (m *memRuns) Get(_ context.Context, id uuid.UUID) (assistantModel.Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	return r, ok, nil
}
func (m *memRuns) OfVariant(_ context.Context, variant uuid.UUID, _ int) ([]assistantModel.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.Run
	for i := len(m.order) - 1; i >= 0; i-- {
		if r := m.runs[m.order[i]]; r.VariantID == variant {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memRuns) Active(context.Context, int) ([]assistantModel.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.Run
	for _, id := range m.order {
		if r := m.runs[id]; r.State.Active() {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memRuns) CountActiveOf(_ context.Context, runActor uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.Actor == runActor && r.State.Active() {
			n++
		}
	}
	return n, nil
}
func (m *memRuns) update(id uuid.UUID, fn func(*assistantModel.Run) bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok || !fn(&r) {
		return false
	}
	m.runs[id] = r
	return true
}
func (m *memRuns) SetAccessClient(_ context.Context, id uuid.UUID, client string, _ time.Time) error {
	m.update(id, func(r *assistantModel.Run) bool {
		r.AccessClient = client
		return r.State == assistantModel.RunStarting
	})
	return nil
}
func (m *memRuns) SetRunning(_ context.Context, id uuid.UUID, now time.Time) (bool, error) {
	return m.update(id, func(r *assistantModel.Run) bool {
		if r.State != assistantModel.RunStarting {
			return false
		}
		r.State, r.StartedAt = assistantModel.RunRunning, now
		return true
	}), nil
}
func (m *memRuns) Close(_ context.Context, id uuid.UUID, end assistantModel.RunState, reason string, _ time.Time) (bool, error) {
	return m.update(id, func(r *assistantModel.Run) bool {
		if r.State != assistantModel.RunStarting && r.State != assistantModel.RunRunning {
			return false
		}
		r.State, r.EndState, r.FailureReason = assistantModel.RunClosing, end, reason
		return true
	}), nil
}
func (m *memRuns) Finish(_ context.Context, id uuid.UUID, now time.Time) (bool, error) {
	return m.update(id, func(r *assistantModel.Run) bool {
		if r.State != assistantModel.RunClosing {
			return false
		}
		r.State, r.FinishedAt = r.EndState, now
		return true
	}), nil
}
func (m *memRuns) SetMeasurement(_ context.Context, id uuid.UUID, measurement []byte, _ time.Time) error {
	m.update(id, func(r *assistantModel.Run) bool { r.Measurement = measurement; return true })
	return nil
}
func (m *memRuns) SetStreamDown(_ context.Context, id uuid.UUID, since, _ time.Time) error {
	m.update(id, func(r *assistantModel.Run) bool { r.StreamDownSince = since; return true })
	return nil
}
func (m *memRuns) AppendEvent(_ context.Context, id uuid.UUID, e assistantModel.RunEvent, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, have := range m.events[id] {
		if have.Seq == e.Seq {
			return false, nil
		}
	}
	m.events[id] = append(m.events[id], e)
	r := m.runs[id]
	r.LastSeq = max(r.LastSeq, e.Seq)
	m.runs[id] = r
	return true, nil
}
func (m *memRuns) Events(_ context.Context, id uuid.UUID, after int64, limit int) ([]assistantModel.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.RunEvent
	for _, e := range m.events[id] {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *memRuns) AddAnswer(_ context.Context, id uuid.UUID, seq int64, subtask string, correct bool, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.answers[id] {
		if a.Seq == seq {
			return false, nil
		}
	}
	m.answers[id] = append(m.answers[id], assistantModel.RunAnswer{Seq: seq, SubtaskRef: subtask, Correct: correct, At: now})
	return true, nil
}
func (m *memRuns) Answer(_ context.Context, id uuid.UUID, seq int64) (assistantModel.RunAnswer, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.answers[id] {
		if a.Seq == seq {
			return a, true, nil
		}
	}
	return assistantModel.RunAnswer{}, false, nil
}
func (m *memRuns) SetAnswerSent(_ context.Context, id uuid.UUID, seq int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.answers[id] {
		if a.Seq == seq {
			m.answers[id][i].VerdictSent = true
		}
	}
	return nil
}
func (m *memRuns) Answers(_ context.Context, id uuid.UUID) ([]assistantModel.RunAnswer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]assistantModel.RunAnswer(nil), m.answers[id]...), nil
}
func (m *memRuns) Unsent(_ context.Context, id uuid.UUID) ([]assistantModel.RunAnswer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.RunAnswer
	for _, a := range m.answers[id] {
		if !a.VerdictSent {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memRuns) RecentAnswers(_ context.Context, id uuid.UUID, since time.Time) ([]assistantModel.RunAnswer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.RunAnswer
	for _, a := range m.answers[id] {
		if a.At.After(since) {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memRuns) UserOfSubject(_ context.Context, subject uuid.UUID) (uuid.UUID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[subject]
	return u, ok, nil
}

func (m *memRuns) LabOfRequest(_ context.Context, requestID string) (assistantModel.Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.Role == assistantModel.RoleLab && r.RequestID == requestID {
			return r, true, nil
		}
	}
	return assistantModel.Run{}, false, nil
}
func (m *memRuns) SetAnswered(_ context.Context, id uuid.UUID, _ time.Time) error {
	m.update(id, func(r *assistantModel.Run) bool { r.Answered = true; return true })
	return nil
}

func (m *memRuns) LabCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.Role == assistantModel.RoleLab {
			n++
		}
	}
	return n
}

func (m *memRuns) state(id uuid.UUID) assistantModel.Run {
	r, _, _ := m.Get(context.Background(), id)
	return r
}

// fakeLabs is the exercise side of a run: a lab that becomes ready after a number of looks, and everything the run asks.
type fakeLabs struct {
	mu          sync.Mutex
	authErr     error
	deployErr   error
	hasLab      bool
	readyAfter  int
	looks       int
	failed      bool
	gone        bool
	authorized  []exerciseUseCase.Actor
	mintErr     error
	judged      map[string]string // subtask ref -> the value that is right
	calls       []string
	deployLen   time.Duration
	deployCount int
	destroyErr  error
	revokeErr   error
	deployOwner uuid.UUID
}

func newFakeLabs() *fakeLabs { return &fakeLabs{hasLab: true, judged: map[string]string{}} }

func (f *fakeLabs) deploys() int { f.mu.Lock(); defer f.mu.Unlock(); return f.deployCount }
func (f *fakeLabs) lookCount() int     { f.mu.Lock(); defer f.mu.Unlock(); return f.looks }
func (f *fakeLabs) record(call string) { f.mu.Lock(); f.calls = append(f.calls, call); f.mu.Unlock() }
func (f *fakeLabs) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
func (f *fakeLabs) count(call string) int {
	n := 0
	for _, c := range f.called() {
		if c == call {
			n++
		}
	}
	return n
}
func (f *fakeLabs) AuthorizeTestDeploy(_ context.Context, actor exerciseUseCase.Actor, _, _, _ uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "authorize")
	f.authorized = append(f.authorized, actor)
	return f.authErr
}
func (f *fakeLabs) DeployVariantTest(_ context.Context, owner, _, _ uuid.UUID, length time.Duration) (exerciseModel.DeployHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "deploy")
	f.deployLen, f.deployOwner = length, owner
	f.deployCount++
	if f.deployErr != nil {
		return exerciseModel.DeployHandle{}, f.deployErr
	}
	return exerciseModel.DeployHandle{Group: runDeploy.String(), Lab: "l-lab"}, nil
}
func (f *fakeLabs) DestroyDeployTest(context.Context, uuid.UUID, uuid.UUID) error {
	f.record("destroy")
	return f.destroyErr
}
func (f *fakeLabs) TestLabState(context.Context, uuid.UUID, uuid.UUID) (exerciseUseCase.LabState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.looks++
	if f.gone {
		return exerciseUseCase.LabState{Gone: true}, nil
	}
	if f.failed {
		return exerciseUseCase.LabState{Failed: true}, nil
	}
	return exerciseUseCase.LabState{Ready: f.looks > f.readyAfter}, nil
}
func (f *fakeLabs) MintLabClient(_ context.Context, _, _ uuid.UUID, client string) (string, error) {
	f.record("mint:" + client)
	if f.mintErr != nil {
		return "", f.mintErr
	}
	return "[Interface]\nPrivateKey = run-key\n", nil
}
func (f *fakeLabs) RevokeLabClient(_ context.Context, _, _ uuid.UUID, client string) error {
	f.record("revoke:" + client)
	return f.revokeErr
}
func (f *fakeLabs) JudgeAnswer(_ context.Context, _, _ uuid.UUID, ref, value string) (bool, error) {
	f.record("judge:" + ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	want, ok := f.judged[ref]
	return ok && value == want, nil
}
func (f *fakeLabs) RunMaterials(context.Context, uuid.UUID, uuid.UUID) (exerciseUseCase.Materials, error) {
	f.record("materials")
	return exerciseUseCase.Materials{
		Task:  []byte(`{"title":"Web login","description":"","subtasks":[{"key":"login","title":"Log in","prompt":"Open the site","answer_kind":"flag"}]}`),
		Files: []exerciseUseCase.MaterialFile{{Name: "notes.txt", ContentType: "text/plain", Data: []byte("participant file")}},
	}, nil
}
func (f *fakeLabs) RunTarget(_ context.Context, runVersion, _ uuid.UUID, want time.Duration) (exerciseUseCase.RunTarget, error) {
	variants := `[{"topology":{"devices":[{"image":"nginx:1"},{"image":"redis:7"}]}}]`
	return exerciseUseCase.RunTarget{ExerciseID: runExercise, VersionID: runVersion, VariantsJS: []byte(variants), HasLab: f.hasLab, Lease: want}, nil
}

var (
	runExercise = uuid.Must(uuid.NewV7())
	runVersion  = uuid.Must(uuid.NewV7())
	runVariant  = uuid.Must(uuid.NewV7())
	runDeploy   = uuid.Must(uuid.NewV7())
	runActor    = exerciseUseCase.Actor{UserID: uuid.Must(uuid.NewV7())}
)

// memUsers is the user store: the role the authorization needs.
type memUsers struct {
	mu    sync.Mutex
	users map[uuid.UUID]userModel.User
}

func (m *memUsers) GetByID(_ context.Context, id uuid.UUID) (userModel.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return userModel.User{}, pgx.ErrNoRows
}

type runRig struct {
	users *memUsers
	*rig
	runs *memRuns
	labs *fakeLabs
	brn  *brainfake.Runs
}

func newRunRig(t *testing.T, mutate func(*assistant.Dependencies)) *runRig {
	t.Helper()
	runs, labs := newMemRuns(), newFakeLabs()
	users := &memUsers{users: map[uuid.UUID]userModel.User{runActor.UserID: {ID: runActor.UserID, Role: rbac.RoleUser, Status: userModel.UserStatusActive}}}
	r := newRig(t, func(d *assistant.Dependencies) {
		d.Runs, d.Labs, d.Users = runs, labs, users
		d.RunPoll, d.RunPause = 3*time.Millisecond, time.Millisecond
		d.RunGrace, d.RunMaxDuration = time.Minute, time.Hour
		if mutate != nil {
			mutate(d)
		}
	})
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	brn := brainfake.NewRuns()
	r.fake.Runs = brn
	return &runRig{rig: r, runs: runs, labs: labs, brn: brn, users: users}
}

func (r *runRig) start(t *testing.T) assistantModel.Run {
	t.Helper()
	run, err := r.uc.StartGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// drive runs the leader's loop until stop is called (the leadership lost).
func (r *runRig) drive(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.uc.DriveRuns(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("the run driver did not stop with its context")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func (r *runRig) waitState(t *testing.T, id uuid.UUID, want assistantModel.RunState) assistantModel.Run {
	t.Helper()
	eventually(t, "the run to be "+string(want), func() bool { return r.runs.state(id).State == want })
	return r.runs.state(id)
}

func TestStartGraderRunDeploysTheLabAsTheActorAndKeepsTheRunStarting(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	if run.State != assistantModel.RunStarting || run.Role != assistantModel.RoleGrader || run.Actor != runActor.UserID || run.DeployID != runDeploy {
		t.Fatalf("%+v", run)
	}
	if r.labs.deployOwner != runActor.UserID || r.labs.deployLen != time.Hour {
		t.Fatalf("the lab is the runActor's own, long enough for the run: %v %v", r.labs.deployOwner, r.labs.deployLen)
	}
	if got := r.runs.state(run.ID); got.ID != run.ID || !got.ExpiresAt.After(got.CreatedAt) {
		t.Fatalf("stored: %+v", got)
	}
}

func TestStartGraderRunRefusals(t *testing.T) {
	t.Run("not authorized: nothing is deployed", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.labs.authErr = errors.New("forbidden")
		if _, err := r.uc.StartGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant); err == nil {
			t.Fatal("must fail")
		}
		if r.labs.deployCount != 0 {
			t.Fatal("no lab for someone who may not test the task")
		}
	})
	t.Run("one active run per variant", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.start(t)
		if _, err := r.uc.StartGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant); !errors.Is(err, assistantModel.ErrAssistantRunActive.Err()) {
			t.Fatalf("%v", err)
		}
		if r.labs.deployCount != 1 {
			t.Fatalf("a second lab must not be made: %d", r.labs.deployCount)
		}
	})
	t.Run("a user's cap", func(t *testing.T) {
		r := newRunRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 1 })
		r.start(t)
		if _, err := r.uc.StartGraderRun(context.Background(), runActor, runExercise, runVersion, uuid.Must(uuid.NewV7())); !errors.Is(err, assistantModel.ErrAssistantRunLimit.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a lost race for the variant takes the new lab down again", func(t *testing.T) {
		r := newRunRig(t, nil)
		// the pre-check sees nothing, then another request wins the variant
		racing := &racingRuns{memRuns: r.runs}
		r2 := newRunRig(t, func(d *assistant.Dependencies) { d.Runs = racing })
		racing.other = r2.runs
		_, err := r2.uc.StartGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant)
		if !errors.Is(err, assistantModel.ErrAssistantRunActive.Err()) || r2.labs.count("destroy") != 1 {
			t.Fatalf("%v %v", err, r2.labs.called())
		}
	})
	t.Run("the brain is not configured", func(t *testing.T) {
		r := newRunRig(t, func(d *assistant.Dependencies) { d.Config.Endpoint = "" })
		if _, err := r.uc.StartGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant); !errors.Is(err, assistantModel.ErrAssistantUnavailable.Err()) {
			t.Fatalf("%v", err)
		}
	})
}

// racingRuns hides the active run from the pre-check and refuses the create like the unique index would.
type racingRuns struct {
	*memRuns
	other *memRuns
}

func (r *racingRuns) OfVariant(context.Context, uuid.UUID, int) ([]assistantModel.Run, error) {
	return nil, nil
}
func (r *racingRuns) Create(context.Context, assistantModel.Run, time.Time) error {
	return assistantRunRepo.ErrActive
}

func mustJSON(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return out
}

func TestAGraderRunFromStartToTeardown(t *testing.T) {
	r := newRunRig(t, nil)
	r.labs.readyAfter = 2
	r.labs.judged["login"] = "ICE{real}"
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)

	// the materials come first, then the run starts at the brain: a participant's VPN, no band, no budget, no goal
	r.waitState(t, run.ID, assistantModel.RunRunning)
	eventually(t, "StartRun", func() bool { return len(r.brn.Started()) == 1 })
	if order := r.brn.Order(); len(order) != 2 || order[0] != "materials:"+id || order[1] != "start:"+id {
		t.Fatalf("materials first, then the start: %v", order)
	}
	uploaded := r.brn.Materials(id)
	if len(uploaded) != 1 || uploaded[0].RequestedWith != "cybericebox-daemon" {
		t.Fatalf("%+v", uploaded)
	}
	form := parseUpload(t, uploaded[0])
	if form["task"].filename != "task.json" || !strings.Contains(form["task"].body, `"answer_kind":"flag"`) || form["file"].filename != "notes.txt" || form["file"].body != "participant file" {
		t.Fatalf("%+v", form)
	}
	body := mustJSON(t, r.brn.Started()[0])
	for _, forbidden := range []string{"band", "budget", "goal"} {
		if _, has := body[forbidden]; has {
			t.Fatalf("the grader has no %s: %s", forbidden, r.brn.Started()[0])
		}
	}
	var role, taskRef, version, image, start string
	_ = json.Unmarshal(body["role"], &role)
	_ = json.Unmarshal(body["task_ref"], &taskRef)
	_ = json.Unmarshal(body["version"], &version)
	_ = json.Unmarshal(body["image"], &image)
	_ = json.Unmarshal(body["start"], &start)
	var access map[string]struct{ Entry string }
	_ = json.Unmarshal(body["access"], &access)
	var runID string
	_ = json.Unmarshal(body["run_id"], &runID)
	if runID != id || role != "grader" || taskRef != runExercise.String() || version != runVersion.String() || start != "immediate" || len(image) != 64 {
		t.Fatalf("%s", r.brn.Started()[0])
	}
	if len(access) != 1 || !strings.Contains(access["vpn"].Entry, "PrivateKey = run-key") {
		t.Fatalf("the full config of the run's own client: %v", access)
	}
	if !strings.HasPrefix(r.runs.state(run.ID).AccessClient, "g-") {
		t.Fatalf("the client is recorded: %+v", r.runs.state(run.ID))
	}

	// events are stored verbatim with the brain's sequence numbers
	r.brn.Emit(id, "status", `{"type":"status","run_id":"`+id+`","status":"working"}`)
	r.brn.Emit(id, "answer", `{"type":"answer","run_id":"`+id+`","subtask_ref":"login","value":"ICE{wrong}"}`)
	r.brn.Emit(id, "answer", `{"type":"answer","run_id":"`+id+`","subtask_ref":"login","value":"ICE{real}"}`)
	r.brn.Emit(id, "measure", `{"type":"measure","run_id":"`+id+`","steps":7}`)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","run_id":"`+id+`","status":"done"}`)

	done := r.waitState(t, run.ID, assistantModel.RunFinished)
	if done.FailureReason != "" || done.FinishedAt.IsZero() || done.LastSeq != 5 {
		t.Fatalf("%+v", done)
	}
	if string(done.Measurement) != `{"type":"measure","run_id":"`+id+`","steps":7}` {
		t.Fatalf("the final aggregate is the measurement: %s", done.Measurement)
	}
	events, _ := r.runs.Events(context.Background(), run.ID, 0, 100)
	if len(events) != 5 || events[1].Type != "answer" || !strings.Contains(events[1].Data, "ICE{wrong}") || events[4].Type != "run_finished" {
		t.Fatalf("%+v", events)
	}

	// the verdicts: right or wrong and when, never the expected value
	verdicts := r.brn.Verdicts(id)
	if len(verdicts) != 2 {
		t.Fatalf("%v", verdicts)
	}
	first, second := mustJSON(t, verdicts[0]), mustJSON(t, verdicts[1])
	if string(first["correct"]) != "false" || string(second["correct"]) != "true" || string(first["subtask_ref"]) != `"login"` || len(first) != 3 {
		t.Fatalf("%s %s", verdicts[0], verdicts[1])
	}
	for _, v := range verdicts {
		if strings.Contains(string(v), "ICE{") {
			t.Fatalf("a verdict must carry no value: %s", v)
		}
	}
	answers, _ := r.runs.Answers(context.Background(), run.ID)
	if len(answers) != 2 || !answers[1].Correct || !answers[0].VerdictSent {
		t.Fatalf("the per-answer record stays: %+v", answers)
	}

	// torn down: the client revoked before the lab is destroyed; the brain is not told to stop a run that finished
	calls := r.labs.called()
	revoke, destroy := indexOf(calls, "revoke:"+done.AccessClient), indexOf(calls, "destroy")
	if revoke < 0 || destroy < 0 || revoke > destroy {
		t.Fatalf("%v", calls)
	}
	if len(r.brn.Stops()) != 0 {
		t.Fatalf("%v", r.brn.Stops())
	}
}

type uploadedPart struct{ filename, contentType, body string }

func parseUpload(t *testing.T, u brainfake.Upload) map[string]uploadedPart {
	t.Helper()
	_, params, err := mime.ParseMediaType(u.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uploadedPart{}
	reader := multipart.NewReader(bytes.NewReader(u.Body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(part)
		out[part.FormName()] = uploadedPart{filename: part.FileName(), contentType: part.Header.Get("Content-Type"), body: string(body)}
	}
	return out
}

func indexOf(list []string, item string) int {
	for i, v := range list {
		if v == item {
			return i
		}
	}
	return -1
}

func TestARunThatFailsAtTheBrainIsTornDownAsFailed(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","run_id":"`+id+`","status":"failed"}`)
	got := r.waitState(t, run.ID, assistantModel.RunFailed)
	if got.FailureReason != assistantModel.FailureBrainFailed {
		t.Fatalf("%+v", got)
	}
	eventually(t, "the lab to go", func() bool { return r.labs.count("destroy") >= 1 })
}

func TestAStreamThatClosesIsReopenedAfterTheLastStoredEvent(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "status", `{"type":"status"}`)
	r.brn.Emit(id, "step", `{"type":"step"}`)
	eventually(t, "two events", func() bool { return r.runs.state(run.ID).LastSeq == 2 })

	r.brn.DropStreams() // a blip: not the end of the run
	eventually(t, "a second connection", func() bool { return r.brn.Opened(id) >= 2 })
	r.brn.AllowStreams()
	r.brn.Emit(id, "step", `{"type":"step","n":3}`)
	eventually(t, "the third event", func() bool { return r.runs.state(run.ID).LastSeq == 3 })

	afters := r.brn.Afters(id)
	if afters[0] != 0 || afters[len(afters)-1] != 2 {
		t.Fatalf("the stream resumes after the last stored event: %v", afters)
	}
	events, _ := r.runs.Events(context.Background(), run.ID, 0, 100)
	if len(events) != 3 {
		t.Fatalf("no event twice: %+v", events)
	}
	if got := r.runs.state(run.ID); got.State != assistantModel.RunRunning || !got.StreamDownSince.IsZero() {
		t.Fatalf("a stream that is back clears the grace: %+v", got)
	}
}

func TestAnUnreachableBrainFailsTheRunAfterTheGraceAndTearsDown(t *testing.T) {
	r := newRunRig(t, func(d *assistant.Dependencies) { d.RunGrace = 60 * time.Second })
	run := r.start(t)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)

	r.brn.SetEventsStatus(503)
	r.brn.DropStreams()
	eventually(t, "the grace to start", func() bool { return !r.runs.state(run.ID).StreamDownSince.IsZero() })
	if r.runs.state(run.ID).State != assistantModel.RunRunning {
		t.Fatal("a short outage is waited out")
	}
	r.clock.advance(61 * time.Second)
	got := r.waitState(t, run.ID, assistantModel.RunFailed)
	if got.FailureReason != assistantModel.FailureBrainUnreachable {
		t.Fatalf("%+v", got)
	}
	calls := r.labs.called()
	if indexOf(calls, "revoke:"+got.AccessClient) < 0 || indexOf(calls, "destroy") < 0 {
		t.Fatalf("the access is revoked and the lab destroyed: %v", calls)
	}
}

func TestARunPastItsMaxDurationFails(t *testing.T) {
	r := newRunRig(t, func(d *assistant.Dependencies) { d.RunMaxDuration = 10 * time.Minute })
	run := r.start(t)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.clock.advance(11 * time.Minute)
	got := r.waitState(t, run.ID, assistantModel.RunFailed)
	if got.FailureReason != assistantModel.FailureTimeout {
		t.Fatalf("%+v", got)
	}
	eventually(t, "the lab to go", func() bool { return r.labs.count("destroy") >= 1 })
	eventually(t, "the brain to be told", func() bool { return len(r.brn.Stops()) == 1 })
}

func TestStoppingARunTellsTheBrainRevokesAndDestroys(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)

	stopped, err := r.uc.StopGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant, run.ID)
	if err != nil || stopped.State != assistantModel.RunClosing && stopped.State != assistantModel.RunStopped {
		t.Fatalf("%v %+v", err, stopped)
	}
	got := r.waitState(t, run.ID, assistantModel.RunStopped)
	if len(r.brn.Stops()) != 1 || r.brn.Stops()[0] != run.ID.String() {
		t.Fatalf("%v", r.brn.Stops())
	}
	if indexOf(r.labs.called(), "revoke:"+got.AccessClient) < 0 || r.labs.count("destroy") != 1 {
		t.Fatalf("%v", r.labs.called())
	}
	if again, err := r.uc.StopGraderRun(context.Background(), runActor, runExercise, runVersion, runVariant, run.ID); err != nil || again.State != assistantModel.RunStopped {
		t.Fatalf("stopping twice changes nothing: %v %+v", err, again)
	}
}

func TestALabThatFailsOrIsRefusedEndsTheRunWithItsReason(t *testing.T) {
	t.Run("the lab failed", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.labs.failed = true
		run := r.start(t)
		r.drive(t)
		got := r.waitState(t, run.ID, assistantModel.RunFailed)
		if got.FailureReason != assistantModel.FailureLabFailed || len(r.brn.Started()) != 0 {
			t.Fatalf("%+v %v", got, r.brn.Started())
		}
		eventually(t, "the lab to go", func() bool { return r.labs.count("destroy") == 1 })
	})
	t.Run("the brain refuses the run", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.SetStartStatus(422)
		run := r.start(t)
		r.drive(t)
		got := r.waitState(t, run.ID, assistantModel.RunFailed)
		if got.FailureReason != assistantModel.FailureStartRefused {
			t.Fatalf("%+v", got)
		}
		eventually(t, "the lab to go", func() bool { return r.labs.count("destroy") == 1 })
		if len(r.brn.Stops()) != 0 {
			t.Fatalf("a run that never started needs no stop: %v", r.brn.Stops())
		}
	})
	t.Run("the VPN client cannot be made", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.labs.mintErr = errors.New("agent down")
		run := r.start(t)
		r.drive(t)
		if got := r.waitState(t, run.ID, assistantModel.RunFailed); got.FailureReason != assistantModel.FailureLabFailed {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("an unreachable brain at the start fails after the grace", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.SetStartStatus(503)
		run := r.start(t)
		r.drive(t)
		eventually(t, "the grace to start", func() bool { return !r.runs.state(run.ID).StreamDownSince.IsZero() })
		r.clock.advance(2 * time.Minute)
		if got := r.waitState(t, run.ID, assistantModel.RunFailed); got.FailureReason != assistantModel.FailureBrainUnreachable {
			t.Fatalf("%+v", got)
		}
	})
}

func TestANewLeaderResumesARunFromTheStoredState(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	id := run.ID.String()
	stop := r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "status", `{"type":"status"}`)
	r.brn.Emit(id, "step", `{"type":"step"}`)
	eventually(t, "two events", func() bool { return r.runs.state(run.ID).LastSeq == 2 })
	stop() // the leadership is lost

	r.brn.Emit(id, "step", `{"type":"step","n":3}`)
	r.drive(t) // another replica leads now
	eventually(t, "the third event", func() bool { return r.runs.state(run.ID).LastSeq == 3 })
	afters := r.brn.Afters(id)
	if afters[len(afters)-1] != 2 {
		t.Fatalf("the new leader reads after the stored sequence: %v", afters)
	}
	if len(r.brn.Started()) != 1 {
		t.Fatalf("a running run is not started again: %d", len(r.brn.Started()))
	}
}

func TestAVerdictThatFailsIsSentAgainWithTheSameBody(t *testing.T) {
	r := newRunRig(t, nil)
	r.labs.judged["login"] = "ICE{real}"
	r.brn.FailVerdicts(4, 503) // more than one send's attempts: left for the next pass
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "answer", `{"type":"answer","run_id":"`+id+`","subtask_ref":"login","value":"ICE{real}"}`)
	eventually(t, "the verdict to arrive and be recorded as sent", func() bool {
		answers, _ := r.runs.Answers(context.Background(), run.ID)
		return len(r.brn.Verdicts(id)) == 1 && len(answers) == 1 && answers[0].VerdictSent
	})
	r.brn.DropStreams() // the next pass looks at the unsent verdicts again
	time.Sleep(50 * time.Millisecond)
	if n := len(r.brn.Verdicts(id)); n != 1 {
		t.Fatalf("a delivered verdict is not sent again: %d", n)
	}
	if got := r.labs.count("judge:login"); got != 1 {
		t.Fatalf("an answer is judged once, however often its verdict is retried: %d", got)
	}
}

func TestAnAnswerIsNotJudgedAgainWhenItsFrameIsReplayed(t *testing.T) {
	r := newRunRig(t, nil)
	r.labs.judged["login"] = "ICE{real}"
	run := r.start(t)
	id := run.ID.String()
	// the answer was judged and told by a leader that died before it stored the frame
	r.runs.answers[run.ID] = []assistantModel.RunAnswer{{Seq: 1, SubtaskRef: "login", Correct: true, At: time.Now(), VerdictSent: true}}
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "answer", `{"type":"answer","run_id":"`+id+`","subtask_ref":"login","value":"ICE{real}"}`)
	eventually(t, "the frame to be stored", func() bool { return r.runs.state(run.ID).LastSeq == 1 })
	if r.labs.count("judge:login") != 0 || len(r.brn.Verdicts(id)) != 0 {
		t.Fatalf("the stored judgement answers the replay: %v %v", r.labs.called(), r.brn.Verdicts(id))
	}
}

func TestRunsOfAnotherVariantAreNotFound(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	_, err := r.uc.GraderRun(context.Background(), runActor, runExercise, runVersion, uuid.Must(uuid.NewV7()), run.ID)
	if !errors.Is(err, assistantModel.ErrAssistantRunNotFound.Err()) {
		t.Fatalf("%v", err)
	}
	view, err := r.uc.GraderRun(context.Background(), runActor, runExercise, runVersion, runVariant, run.ID)
	if err != nil || view.Run.ID != run.ID {
		t.Fatalf("%v", err)
	}
	listed, err := r.uc.GraderRuns(context.Background(), runActor, runExercise, runVersion, runVariant)
	if err != nil || len(listed) != 1 {
		t.Fatalf("%v %v", err, listed)
	}
	r.labs.authErr = errors.New("forbidden")
	if _, err = r.uc.GraderRun(context.Background(), runActor, runExercise, runVersion, runVariant, run.ID); err == nil {
		t.Fatal("the test deploy's authorization applies to every route")
	}
	if _, err = r.uc.GraderRunEvents(context.Background(), runActor, runExercise, runVersion, runVariant, run.ID); err == nil {
		t.Fatal("and to the event stream")
	}
}

func TestRunEventsAreServedFromStorageAfterASequence(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	for i := int64(1); i <= 3; i++ {
		_, _ = r.runs.AppendEvent(context.Background(), run.ID, assistantModel.RunEvent{Seq: i, Type: "step", Data: `{"n":1}`}, time.Now())
	}
	read, err := r.uc.GraderRunEvents(context.Background(), runActor, runExercise, runVersion, runVariant, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, state, err := read(context.Background(), 1, 10)
	if err != nil || len(events) != 2 || events[0].Seq != 2 || state != assistantModel.RunStarting {
		t.Fatalf("%v %+v %v", err, events, state)
	}
}

func TestLabClientsAreTheClientsOfTheOwnersActiveRuns(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	_ = r.runs.SetAccessClient(context.Background(), run.ID, "g-abc", time.Now())
	got := r.uc.LabClients(context.Background(), runActor.UserID)
	if len(got) != 1 || got[0].Name != "g-abc" || got[0].DeployID != runDeploy {
		t.Fatalf("%+v", got)
	}
	if other := r.uc.LabClients(context.Background(), uuid.Must(uuid.NewV7())); len(other) != 0 {
		t.Fatalf("%+v", other)
	}
}

func TestAnswerDelayFollowsTheParticipantLimits(t *testing.T) {
	now := time.Now()
	at := func(ago time.Duration, ref string) assistantModel.RunAnswer {
		return assistantModel.RunAnswer{SubtaskRef: ref, At: now.Add(-ago)}
	}
	// under both limits: no wait
	if d := assistant.AnswerDelay([]assistantModel.RunAnswer{at(time.Second, "a"), at(2*time.Second, "a")}, "a", now); d != 0 {
		t.Fatalf("%v", d)
	}
	// five answers for one task in 30s: the next waits until the oldest leaves the window
	five := []assistantModel.RunAnswer{at(25*time.Second, "a"), at(20*time.Second, "a"), at(10*time.Second, "a"), at(5*time.Second, "a"), at(time.Second, "a")}
	if d := assistant.AnswerDelay(five, "a", now); d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("the oldest was 25s ago, so about 5s: %v", d)
	}
	// the limit is per task: another task is free
	if d := assistant.AnswerDelay(five, "b", now); d != 0 {
		t.Fatalf("%v", d)
	}
	// twenty answers over all tasks in a minute: the team limit holds the next one
	var many []assistantModel.RunAnswer
	for i := 0; i < 20; i++ {
		many = append(many, at(time.Duration(50-i)*time.Second/2*1, "t"+string(rune('a'+i))))
	}
	if d := assistant.AnswerDelay(many, "new", now); d <= 0 {
		t.Fatalf("the limit over all tasks must hold: %v", d)
	}
}

// The routes authorize for the exercise in the path; a run, a version or a variant of another exercise is not found on
// every route, however the ids are mixed.
func TestAnotherExercisesIdsAreNotFoundOnEveryMeasureRoute(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	other := uuid.Must(uuid.NewV7())
	ctx := context.Background()
	notFound := assistantModel.ErrAssistantRunNotFound.Err()

	if _, err := r.uc.GraderRun(ctx, runActor, other, runVersion, runVariant, run.ID); !errors.Is(err, notFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := r.uc.StopGraderRun(ctx, runActor, other, runVersion, runVariant, run.ID); !errors.Is(err, notFound) {
		t.Errorf("stop: %v", err)
	}
	if _, err := r.uc.GraderRunEvents(ctx, runActor, other, runVersion, runVariant, run.ID); !errors.Is(err, notFound) {
		t.Errorf("events: %v", err)
	}
	if listed, err := r.uc.GraderRuns(ctx, runActor, other, runVersion, runVariant); err != nil || len(listed) != 0 {
		t.Errorf("list must show nothing of another exercise: %v %v", listed, err)
	}
	if _, err := r.uc.StartGraderRun(ctx, runActor, other, runVersion, runVariant); !errors.Is(err, exerciseModel.ErrExerciseVersionNotFound.Err()) {
		t.Errorf("start: %v", err)
	}
	if r.runs.state(run.ID).State != assistantModel.RunStarting {
		t.Fatal("a stop with the wrong exercise must not touch the run")
	}
	if r.labs.deployCount != 1 {
		t.Fatalf("a start with the wrong exercise must not deploy a lab: %d", r.labs.deployCount)
	}
}

func TestTheMaterialsAreRetriedOnServerErrorsAndAFailureRefusesTheRun(t *testing.T) {
	t.Run("a server error twice, then accepted", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.FailMaterials(503, 502)
		run := r.start(t)
		r.drive(t)
		r.waitState(t, run.ID, assistantModel.RunRunning)
		if len(r.brn.Materials(run.ID.String())) != 1 || len(r.brn.Started()) != 1 {
			t.Fatalf("%v", r.brn.Order())
		}
	})
	t.Run("a server error that does not stop", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.FailMaterials(503, 503, 503)
		run := r.start(t)
		r.drive(t)
		got := r.waitState(t, run.ID, assistantModel.RunFailed)
		if got.FailureReason != assistantModel.FailureStartRefused || len(r.brn.Started()) != 0 {
			t.Fatalf("%+v %v", got, r.brn.Order())
		}
		eventually(t, "the lab to go", func() bool { return r.labs.count("destroy") >= 1 })
		if indexOf(r.labs.called(), "revoke:"+got.AccessClient) < 0 {
			t.Fatalf("the client is revoked: %v", r.labs.called())
		}
	})
	t.Run("a refusal that cannot change", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.FailMaterials(422)
		run := r.start(t)
		r.drive(t)
		if got := r.waitState(t, run.ID, assistantModel.RunFailed); got.FailureReason != assistantModel.FailureStartRefused || len(r.brn.Started()) != 0 {
			t.Fatalf("%+v", got)
		}
	})
}

func TestAStaticGraderRunEndToEnd(t *testing.T) {
	r := newRunRig(t, nil)
	r.labs.hasLab = false
	r.labs.judged["login"] = "ICE{real}"
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	eventually(t, "StartRun", func() bool { return len(r.brn.Started()) == 1 })
	if order := r.brn.Order(); len(order) != 2 || order[0] != "materials:"+id {
		t.Fatalf("%v", order)
	}
	body := mustJSON(t, r.brn.Started()[0])
	if string(body["access"]) != `{"none":{}}` {
		t.Fatalf("a task with no lab is started with the access kind none: %s", body["access"])
	}
	for _, c := range r.labs.called() {
		if strings.HasPrefix(c, "mint:") {
			t.Fatalf("a static run gets no VPN client: %v", r.labs.called())
		}
	}
	if r.runs.state(run.ID).AccessClient != "" {
		t.Fatalf("%+v", r.runs.state(run.ID))
	}

	r.brn.Emit(id, "answer", `{"type":"answer","run_id":"`+id+`","subtask_ref":"login","value":"ICE{real}"}`)
	r.brn.Emit(id, "measure", `{"type":"measure","runs":1,"solved":1}`)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","run_id":"`+id+`","status":"done"}`)
	done := r.waitState(t, run.ID, assistantModel.RunFinished)
	if string(done.Measurement) != `{"type":"measure","runs":1,"solved":1}` {
		t.Fatalf("%s", done.Measurement)
	}
	verdicts := r.brn.Verdicts(id)
	if len(verdicts) != 1 || string(mustJSON(t, verdicts[0])["correct"]) != "true" {
		t.Fatalf("answers are judged by the same checker: %v", verdicts)
	}
	// torn down by closing the run: the deploy row is removed, nothing was revoked
	if r.labs.count("destroy") != 1 {
		t.Fatalf("%v", r.labs.called())
	}
	if len(r.brn.Stops()) != 0 {
		t.Fatal("a run that finished is not stopped")
	}
}

func TestOnlyTheMeasureEventIsTheMeasurement(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "aggregate", `{"type":"aggregate","not":"this"}`)
	r.brn.Emit(id, "measure", `{"type":"measure","runs":3}`)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","status":"done"}`)
	done := r.waitState(t, run.ID, assistantModel.RunFinished)
	if string(done.Measurement) != `{"type":"measure","runs":3}` {
		t.Fatalf("%s", done.Measurement)
	}
}
