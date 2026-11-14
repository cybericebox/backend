package assistant_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	"github.com/cybericebox/daemon/internal/model/rbac"
	userModel "github.com/cybericebox/daemon/internal/model/user"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
)

// the fingerprint of the images the fake lab's variants name: sha256 over the sorted distinct references
func labImage() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"nginx:1", "redis:7"}, "\n")))
	return hex.EncodeToString(sum[:])
}

type provideRig struct {
	*runRig
	brain   *outboxBrain
	cursor  *memCursor
	subject uuid.UUID
}

func newProvideRig(t *testing.T, mutate func(*assistant.Dependencies), frames ...outboxFrame) *provideRig {
	t.Helper()
	cursor := &memCursor{}
	r := newRunRig(t, func(d *assistant.Dependencies) {
		d.Cursor = cursor
		d.WatchMin, d.WatchMax, d.AnswerPause = 5*time.Millisecond, 20*time.Millisecond, time.Millisecond
		if mutate != nil {
			mutate(d)
		}
	})
	b := &outboxBrain{frames: frames}
	r.fake.Assistant = b.handler()
	subject := uuid.Must(uuid.NewV7())
	r.runs.users[subject] = runActor.UserID
	return &provideRig{runRig: r, brain: b, cursor: cursor, subject: subject}
}

func (p *provideRig) frame(seq int64, id, body string) outboxFrame {
	return outboxFrame{seq: seq, event: "provide_lab", data: fmt.Sprintf(`{"request_id":%q,"type":"provide_lab",%s}`, id, body)}
}

func (p *provideRig) task(subject uuid.UUID, image string) string {
	return fmt.Sprintf(`"subject":%q,"source":{"task":{"task_ref":%q,"version":%q,"image":%q}}`, subject.String(), runExercise.String(), runVersion.String(), image)
}

// serve runs the outbox of the rig until stop.
func (p *provideRig) serve(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.uc.WatchOutbox(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (p *provideRig) answeredOnce(t *testing.T, id string) string {
	t.Helper()
	eventually(t, "the request "+id+" to be answered", func() bool { return len(p.brain.answered(id)) >= 1 })
	if n := len(p.brain.answered(id)); n != 1 {
		t.Fatalf("answered %d times: %v", n, p.brain.answered(id))
	}
	return p.brain.answered(id)[0]
}

func (p *provideRig) lab(t *testing.T) assistantModel.Run {
	t.Helper()
	p.runs.mu.Lock()
	defer p.runs.mu.Unlock()
	for _, r := range p.runs.runs {
		if r.Role == assistantModel.RoleLab {
			return r
		}
	}
	t.Fatal("no lab row")
	return assistantModel.Run{}
}

func TestProvideLabClaimsThenTheDriverAnswersTheDeviceAndAnAccess(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
	p.labs.readyAfter = 2
	p.serve(t)
	// claimed at once: the cursor moves and the lab row is there, with no answer yet
	eventually(t, "the cursor to move", func() bool { return p.cursor.get() == 1 })
	if lab := p.lab(t); lab.RequestID != "r1" || lab.State != assistantModel.RunStarting || lab.Answered {
		t.Fatalf("%+v", lab)
	}
	if len(p.brain.answered("r1")) != 0 {
		t.Fatal("not answered before the lab is ready")
	}
	p.drive(t)
	var got struct {
		Result struct {
			DeviceRef string                            `json:"device_ref"`
			Access    map[string]struct{ Entry string } `json:"access"`
		}
	}
	if err := json.Unmarshal([]byte(p.answeredOnce(t, "r1")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Result.DeviceRef != runDeploy.String() || !strings.Contains(got.Result.Access["vpn"].Entry, "PrivateKey = run-key") {
		t.Fatalf("%v", p.brain.answered("r1"))
	}
	if p.labs.deployOwner != runActor.UserID || p.labs.deployLen != 0 {
		t.Fatalf("the user's own lab with the default lease: %v %v", p.labs.deployOwner, p.labs.deployLen)
	}
	eventually(t, "the answer to be recorded", func() bool { return p.lab(t).Answered })
	lab := p.lab(t)
	if lab.Role != assistantModel.RoleLab || lab.State != assistantModel.RunRunning || !strings.HasPrefix(lab.AccessClient, "g-") || lab.Actor != runActor.UserID {
		t.Fatalf("%+v", lab)
	}
	if listed, _ := p.uc.GraderRuns(context.Background(), runActor, runExercise, runVersion, runVariant); len(listed) != 0 {
		t.Fatalf("a lab is not a grader run: %v", listed)
	}
}

func TestProvideLabAuthorizesTheTaskAgainstTheSubjectsUser(t *testing.T) {
	t.Run("the policy of the test deploy is asked with the user's role", func(t *testing.T) {
		p := newProvideRig(t, nil)
		p.users.users[runActor.UserID] = userModel.User{ID: runActor.UserID, Role: rbac.RoleAdmin, Status: userModel.UserStatusActive}
		p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
		p.serve(t)
		eventually(t, "the cursor to move", func() bool { return p.cursor.get() == 1 })
		if len(p.labs.authorized) != 1 || p.labs.authorized[0].UserID != runActor.UserID || p.labs.authorized[0].Role != rbac.RoleAdmin {
			t.Fatalf("%+v", p.labs.authorized)
		}
	})
	for name, prep := range map[string]func(p *provideRig){
		"the policy refuses": func(p *provideRig) { p.labs.authErr = assistantModel.ErrAssistantForbidden.Err() },
		"the task is hidden": func(p *provideRig) { p.labs.authErr = exerciseModel.ErrExerciseNotFound.Err() },
		"the user is blocked": func(p *provideRig) {
			p.users.users[runActor.UserID] = userModel.User{ID: runActor.UserID, Status: userModel.UserStatusBlocked}
		},
		"the user is gone": func(p *provideRig) { delete(p.users.users, runActor.UserID) },
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			prep(p)
			p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
			p.serve(t)
			if code := refusalCode(t, p.answeredOnce(t, "r1")); code != "forbidden" || p.labs.deployCount != 0 {
				t.Fatalf("%s deployed %d", code, p.labs.deployCount)
			}
		})
	}
	t.Run("a failure of the platform is not forbidden", func(t *testing.T) {
		p := newProvideRig(t, nil)
		p.labs.authErr = errors.New("database down")
		p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
		p.serve(t)
		if code := refusalCode(t, p.answeredOnce(t, "r1")); code != "internal" {
			t.Fatalf("%s", code)
		}
	})
}

func TestProvideLabRefusals(t *testing.T) {
	unknown := uuid.Must(uuid.NewV7())
	for name, c := range map[string]struct {
		body func(p *provideRig) string
		prep func(p *provideRig)
		code string
	}{
		"unknown subject": {body: func(p *provideRig) string { return p.task(unknown, labImage()) }, code: "unknown_subject"},
		"image mismatch":  {body: func(p *provideRig) string { return p.task(p.subject, "00") }, code: "image_mismatch"},
		"no lab":          {body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, prep: func(p *provideRig) { p.labs.hasLab = false }, code: "no_lab"},
		"a draft": {body: func(p *provideRig) string {
			return fmt.Sprintf(`"subject":%q,"source":{"draft":"d-1"}`, p.subject.String())
		}, code: "not_supported_yet"},
		"no source": {body: func(p *provideRig) string { return fmt.Sprintf(`"subject":%q`, p.subject.String()) }, code: "invalid_request"},
		"a subject that is no id": {body: func(*provideRig) string {
			return `"subject":"x","source":{"task":{"task_ref":"y","version":"z","image":""}}`
		}, code: "invalid_request"},
		"the lab is refused": {body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, prep: func(p *provideRig) { p.labs.deployErr = fmt.Errorf("limit") }, code: "lab_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			if c.prep != nil {
				c.prep(p)
			}
			p.brain.frames = []outboxFrame{p.frame(1, "r1", c.body(p))}
			p.serve(t)
			if got := refusalCode(t, p.answeredOnce(t, "r1")); got != c.code {
				t.Fatalf("%s", p.brain.answered("r1")[0])
			}
			eventually(t, "the cursor to move", func() bool { return p.cursor.get() == 1 })
		})
	}
}

func TestALabThatFailsIsRefusedByTheDriver(t *testing.T) {
	for name, c := range map[string]struct {
		prep func(p *provideRig)
		code string
	}{
		"the lab fails":    {prep: func(p *provideRig) { p.labs.failed = true }, code: "lab_unavailable"},
		"the lab is gone":  {prep: func(p *provideRig) { p.labs.gone = true }, code: "lab_unavailable"},
		"the client fails": {prep: func(p *provideRig) { p.labs.mintErr = errors.New("agent") }, code: "lab_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			c.prep(p)
			p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
			p.serve(t)
			p.drive(t)
			if got := refusalCode(t, p.answeredOnce(t, "r1")); got != c.code {
				t.Fatalf("%s", got)
			}
			id := p.lab(t).ID
			p.waitState(t, id, assistantModel.RunFailed)
			eventually(t, "the lab to go", func() bool { return p.labs.count("destroy") >= 1 })
			eventually(t, "the refusal to be recorded", func() bool { return p.lab(t).Answered })
		})
	}
	t.Run("not ready in time", func(t *testing.T) {
		p := newProvideRig(t, nil)
		p.labs.readyAfter = 1 << 30
		p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
		p.serve(t)
		p.drive(t)
		eventually(t, "the lab row", func() bool { return p.runs.LabCount() == 1 })
		p.clock.advance(11 * time.Minute)
		if got := refusalCode(t, p.answeredOnce(t, "r1")); got != "lab_not_ready" {
			t.Fatalf("%s", got)
		}
	})
}

func TestProvideDataStaysFastWhileALabIsComingUp(t *testing.T) {
	p := newProvideRig(t, nil)
	p.labs.readyAfter = 1 << 30 // the lab never comes up during the test
	data := outboxFrame{seq: 2, event: "provide_data", data: `{"request_id":"d1","type":"provide_data","task_refs":[]}`}
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage())), data}
	p.serve(t)
	p.drive(t)
	eventually(t, "both requests read and the data answered", func() bool { return p.cursor.get() == 2 && len(p.brain.answered("d1")) == 1 })
	if len(p.brain.answered("r1")) != 0 || p.lab(t).State != assistantModel.RunStarting {
		t.Fatal("the lab is still coming up and its request still waits")
	}
}

func TestARequestSeenAgainFindsItsLabInsteadOfMakingAnother(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
	p.labs.readyAfter = 1 << 30
	p.serve(t)
	eventually(t, "the cursor to move", func() bool { return p.cursor.get() == 1 })
	// the cursor write was lost, so the brain sends the request again
	p.cursor.mu.Lock()
	p.cursor.seq = 0
	p.cursor.mu.Unlock()
	p.brain.mu.Lock()
	p.brain.frames = append(p.brain.frames, p.frame(2, "r2", p.task(p.subject, labImage())))
	p.brain.mu.Unlock()
	// a dropped stream reconnects and reads from the cursor again
	eventually(t, "the stream to read the request twice", func() bool { return p.brain.watchCount() >= 2 && p.cursor.get() >= 1 })
	time.Sleep(50 * time.Millisecond)
	p.runs.mu.Lock()
	labsOfR1 := 0
	for _, r := range p.runs.runs {
		if r.RequestID == "r1" {
			labsOfR1++
		}
	}
	p.runs.mu.Unlock()
	if labsOfR1 != 1 {
		t.Fatalf("a request has one lab, however often it arrives: %d", labsOfR1)
	}
}

func TestALeaderThatChangesWhileTheLabComesUpStillAnswers(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
	p.labs.readyAfter = 3
	p.serve(t)
	stop := p.drive(t)
	eventually(t, "the lab to be looked at", func() bool { return p.labs.lookCount() >= 1 })
	stop() // the leadership is lost while the lab starts
	p.drive(t)
	if got := p.answeredOnce(t, "r1"); strings.Contains(got, `"error"`) {
		t.Fatalf("%s", got)
	}
}

func TestALabTheBrainAskedForIsRevokedAndDestroyedWhenItIsGone(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
	p.serve(t)
	p.drive(t)
	p.answeredOnce(t, "r1")
	eventually(t, "the answer to be recorded", func() bool { return p.lab(t).Answered })
	id, client := p.lab(t).ID, p.lab(t).AccessClient
	p.labs.mu.Lock()
	p.labs.gone = true // the lab's lease is over
	p.labs.mu.Unlock()
	p.waitState(t, id, assistantModel.RunFinished)
	calls := p.labs.called()
	if indexOf(calls, "revoke:"+client) < 0 || indexOf(calls, "destroy") < 0 {
		t.Fatalf("%v", calls)
	}
	if len(p.brn.Stops()) != 0 || len(p.brn.Started()) != 0 {
		t.Fatal("a lab is no run at the brain")
	}
	if len(p.brain.answered("r1")) != 1 {
		t.Fatalf("the answer is sent once: %v", p.brain.answered("r1"))
	}
}

func TestALabRowLeftBehindByADeadRequestIsClosed(t *testing.T) {
	p := newProvideRig(t, nil)
	now := p.clock.now()
	stale := assistantModel.Run{ID: uuid.Must(uuid.NewV7()), VariantID: runVariant, VersionID: runVersion, DeployID: runDeploy, Actor: runActor.UserID, Role: assistantModel.RoleLab, State: assistantModel.RunStarting, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	_ = p.runs.Create(context.Background(), stale, now)
	p.labs.readyAfter = 1 << 30
	p.drive(t)
	if got := p.waitState(t, stale.ID, assistantModel.RunFailed); got.FailureReason != assistantModel.FailureLabNotReady {
		t.Fatalf("%+v", got)
	}
	eventually(t, "the lab to go", func() bool { return p.labs.count("destroy") == 1 })
}

func TestTheUsersCapAlsoCountsTheirBrainLabs(t *testing.T) {
	p := newProvideRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 1 })
	p.labs.readyAfter = 1 << 30
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage())), p.frame(2, "r2", p.task(p.subject, labImage()))}
	p.serve(t)
	if refusalCode(t, p.answeredOnce(t, "r2")) != "lab_unavailable" {
		t.Fatalf("%v", p.brain.answered("r2"))
	}
	if len(p.brain.answered("r1")) != 0 {
		t.Fatal("the first request still waits for its lab")
	}
}

// Teardown never depends on the brain: with the brain refusing every answer, a lab that is gone or expired is still
// revoked and destroyed, and the row ends.
func TestAnUnansweredLabIsClosedAndTornDownWhileTheBrainIsDown(t *testing.T) {
	t.Run("the lab is gone", func(t *testing.T) {
		p := newProvideRig(t, nil)
		p.brain.answerCodes = repeatStatus(500, 200)
		p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
		p.serve(t)
		p.drive(t)
		eventually(t, "the lab to be running, unanswered", func() bool {
			l := p.runs.LabCount() == 1 && p.lab(t).State == assistantModel.RunRunning
			return l
		})
		id, client := p.lab(t).ID, p.lab(t).AccessClient
		p.labs.mu.Lock()
		p.labs.gone = true
		p.labs.mu.Unlock()
		p.waitState(t, id, assistantModel.RunFinished)
		if calls := p.labs.called(); indexOf(calls, "revoke:"+client) < 0 || indexOf(calls, "destroy") < 0 {
			t.Fatalf("the client is revoked and the lab destroyed: %v", calls)
		}
	})
	t.Run("past its expiry the answer is no longer tried", func(t *testing.T) {
		p := newProvideRig(t, nil)
		p.brain.answerCodes = repeatStatus(500, 200)
		p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
		p.serve(t)
		p.drive(t)
		eventually(t, "the lab to be running, unanswered", func() bool {
			return p.runs.LabCount() == 1 && p.lab(t).State == assistantModel.RunRunning
		})
		id := p.lab(t).ID
		p.clock.advance(25 * time.Hour) // past the cap of a lab the brain asked for
		p.waitState(t, id, assistantModel.RunFailed)
		eventually(t, "the lab to go", func() bool { return p.labs.count("destroy") >= 1 })
	})
	t.Run("an answer that keeps failing is given up after the window", func(t *testing.T) {
		p := newProvideRig(t, nil)
		p.brain.answerCodes = repeatStatus(500, 400)
		p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
		p.serve(t)
		p.drive(t)
		eventually(t, "the lab to be running, unanswered", func() bool {
			return p.runs.LabCount() == 1 && p.lab(t).State == assistantModel.RunRunning
		})
		id := p.lab(t).ID
		p.clock.advance(11 * time.Minute)
		got := p.waitState(t, id, assistantModel.RunFailed)
		if got.FailureReason != assistantModel.FailureLabNotReady {
			t.Fatalf("%+v", got)
		}
		eventually(t, "the lab to go", func() bool { return p.labs.count("destroy") >= 1 })
	})
}

func repeatStatus(status, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = status
	}
	return out
}

func (p *provideRig) deviceFrame(seq int64, id, deviceType, body string) outboxFrame {
	return outboxFrame{seq: seq, event: "provide_device", data: fmt.Sprintf(`{"request_id":%q,"type":"provide_device","device_type":%q,%s}`, id, deviceType, body)}
}

func (p *provideRig) toolsLab(t *testing.T) assistantModel.Run {
	t.Helper()
	p.runs.mu.Lock()
	defer p.runs.mu.Unlock()
	for _, r := range p.runs.runs {
		if r.Role == assistantModel.RoleTools {
			return r
		}
	}
	t.Fatal("no tools lab row")
	return assistantModel.Run{}
}

func TestProvideDeviceToolsAnswersTheEDAAccessOfTheDevice(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.deviceFrame(1, "r1", "tools", p.task(p.subject, labImage()))}
	p.labs.readyAfter = 1
	p.serve(t)
	p.drive(t)
	var got struct {
		Result struct {
			DeviceRef string                       `json:"device_ref"`
			Access    map[string]map[string]string `json:"access"`
		}
	}
	if err := json.Unmarshal([]byte(p.answeredOnce(t, "r1")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Result.DeviceRef != runDeploy.String() || got.Result.Access["eda"]["url"] != "https://proxy.example/tools" || got.Result.Access["eda"]["lab_access_token"] != "secret-bearer" {
		t.Fatalf("%v", p.brain.answered("r1"))
	}
	if _, vpn := got.Result.Access["vpn"]; vpn {
		t.Fatal("the VPN config of the run's client is not handed out for a tools device")
	}
	if p.labs.count("deploy-tools") != 1 || p.labs.count("deploy") != 1 {
		t.Fatalf("%v", p.labs.called())
	}
	eventually(t, "the answer to be recorded", func() bool { return p.toolsLab(t).Answered })
	lab := p.toolsLab(t)
	if lab.Role != assistantModel.RoleTools || lab.Actor != runActor.UserID || p.labs.edaRun != lab.ID.String() || p.labs.edaDevice != "" {
		t.Fatalf("%+v run %q device %q", lab, p.labs.edaRun, p.labs.edaDevice)
	}
	if !p.labs.edaUntil.After(time.Now()) || p.labs.edaUntil.After(lab.ExpiresAt) {
		t.Fatalf("the token ends with the run's window: %v (lab ends %v)", p.labs.edaUntil, lab.ExpiresAt)
	}
	if assistantModel.ExcludeRun(lab.Role) != true {
		t.Fatal("a tools lab never counts in the participant statistics")
	}
}

func TestProvideDeviceRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		deviceType string
		body       func(p *provideRig) string
		prep       func(p *provideRig)
		code       string
	}{
		"a configured host": {deviceType: "configured_host", body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, code: "not_supported_yet"},
		"an unknown type":   {deviceType: "mystery", body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, code: "not_supported_yet"},
		"a draft": {deviceType: "tools", body: func(p *provideRig) string {
			return fmt.Sprintf(`"subject":%q,"source":{"draft":"d-1"}`, p.subject.String())
		}, code: "not_supported_yet"},
		"no tools image": {deviceType: "tools", body: func(p *provideRig) string { return p.task(p.subject, labImage()) },
			prep: func(p *provideRig) { p.labs.toolsErr = exerciseModel.ErrToolsNoImage }, code: "not_supported_yet"},
		"a lab without a VPN": {deviceType: "tools", body: func(p *provideRig) string { return p.task(p.subject, labImage()) },
			prep: func(p *provideRig) { p.labs.toolsErr = exerciseModel.ErrToolsNoVPN }, code: "not_supported_yet"},
		"no lab":          {deviceType: "tools", body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, prep: func(p *provideRig) { p.labs.hasLab = false }, code: "no_lab"},
		"unknown subject": {deviceType: "tools", body: func(p *provideRig) string { return p.task(uuid.Must(uuid.NewV7()), labImage()) }, code: "unknown_subject"},
		"forbidden":       {deviceType: "tools", body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, prep: func(p *provideRig) { p.labs.authErr = assistantModel.ErrAssistantForbidden.Err() }, code: "forbidden"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			if c.prep != nil {
				c.prep(p)
			}
			p.brain.frames = []outboxFrame{p.deviceFrame(1, "r1", c.deviceType, c.body(p))}
			p.serve(t)
			if got := refusalCode(t, p.answeredOnce(t, "r1")); got != c.code {
				t.Fatalf("%s", p.brain.answered("r1")[0])
			}
			if p.labs.count("deploy") != 0 {
				t.Fatal("a refused request deploys nothing")
			}
		})
	}
}

func TestAToolsLabWhoseAccessFailsIsClosedRefusedAndTornDown(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.deviceFrame(1, "r1", "tools", p.task(p.subject, labImage()))}
	p.labs.edaErr = fmt.Errorf("no grpc entry")
	p.serve(t)
	p.drive(t)
	if got := refusalCode(t, p.answeredOnce(t, "r1")); got != "lab_unavailable" {
		t.Fatalf("%s", p.brain.answered("r1")[0])
	}
	id := p.toolsLab(t).ID
	p.waitState(t, id, assistantModel.RunFailed)
	calls := p.labs.called()
	if indexOf(calls, "destroy") < 0 || indexOf(calls, "revoke:"+p.toolsLab(t).AccessClient) < 0 {
		t.Fatalf("a lab that cannot give its access is revoked and destroyed: %v", calls)
	}
}

func TestAToolsLabIsRevokedAndDestroyedWhenItIsGone(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.deviceFrame(1, "r1", "tools", p.task(p.subject, labImage()))}
	p.serve(t)
	p.drive(t)
	p.answeredOnce(t, "r1")
	eventually(t, "the answer to be recorded", func() bool { return p.toolsLab(t).Answered })
	id, client := p.toolsLab(t).ID, p.toolsLab(t).AccessClient
	p.labs.mu.Lock()
	p.labs.gone = true
	p.labs.mu.Unlock()
	p.waitState(t, id, assistantModel.RunFinished)
	calls := p.labs.called()
	if indexOf(calls, "revoke:"+client) < 0 || indexOf(calls, "destroy") < 0 {
		t.Fatalf("%v", calls)
	}
}
