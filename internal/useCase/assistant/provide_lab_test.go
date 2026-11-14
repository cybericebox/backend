package assistant_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
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

func TestProvideLabDeploysAsTheSubjectsUserAndAnswersTheDeviceAndAnAccess(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
	p.labs.readyAfter = 1
	p.serve(t)
	eventually(t, "the request answered", func() bool { return len(p.brain.answered("r1")) == 1 })

	var got struct {
		Result struct {
			DeviceRef string                            `json:"device_ref"`
			Access    map[string]struct{ Entry string } `json:"access"`
		}
	}
	if err := json.Unmarshal([]byte(p.brain.answered("r1")[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Result.DeviceRef != runDeploy.String() || !strings.Contains(got.Result.Access["vpn"].Entry, "PrivateKey = run-key") {
		t.Fatalf("%s", p.brain.answered("r1")[0])
	}
	if p.labs.deployOwner != runActor.UserID || p.labs.deployLen != 0 {
		t.Fatalf("the user's own lab with the default lease: %v %v", p.labs.deployOwner, p.labs.deployLen)
	}
	// the lab is kept as a lab-role row that holds the client; it is not a grader run
	var lab assistantModel.Run
	for _, r := range p.runs.runs {
		lab = r
	}
	if lab.Role != assistantModel.RoleLab || lab.State != assistantModel.RunRunning || !strings.HasPrefix(lab.AccessClient, "g-") || lab.Actor != runActor.UserID {
		t.Fatalf("%+v", lab)
	}
	if listed, _ := p.uc.GraderRuns(context.Background(), runActor, runExercise, runVersion, runVariant); len(listed) != 0 {
		t.Fatalf("a lab is not a grader run: %v", listed)
	}
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
		"the lab fails":      {body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, prep: func(p *provideRig) { p.labs.failed = true }, code: "lab_unavailable"},
		"the client fails":   {body: func(p *provideRig) string { return p.task(p.subject, labImage()) }, prep: func(p *provideRig) { p.labs.mintErr = fmt.Errorf("agent") }, code: "lab_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			if c.prep != nil {
				c.prep(p)
			}
			p.brain.frames = []outboxFrame{p.frame(1, "r1", c.body(p))}
			p.serve(t)
			eventually(t, "the request answered", func() bool { return len(p.brain.answered("r1")) == 1 })
			if got := refusalCode(t, p.brain.answered("r1")[0]); got != c.code {
				t.Fatalf("%s", p.brain.answered("r1")[0])
			}
			eventually(t, "the cursor to move", func() bool { return p.cursor.get() == 1 })
		})
	}
}

func TestALabTheBrainAskedForIsRevokedAndDestroyedWhenItIsGone(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
	p.serve(t)
	p.drive(t)
	eventually(t, "the request answered", func() bool { return len(p.brain.answered("r1")) == 1 })
	var id uuid.UUID
	for k := range p.runs.runs {
		id = k
	}
	client := p.runs.state(id).AccessClient
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
}

func TestALabRowLeftBehindByADeadRequestIsClosed(t *testing.T) {
	p := newProvideRig(t, nil)
	now := p.clock.now()
	stale := assistantModel.Run{ID: uuid.Must(uuid.NewV7()), VariantID: runVariant, VersionID: runVersion, DeployID: runDeploy, Actor: runActor.UserID, Role: assistantModel.RoleLab, State: assistantModel.RunStarting, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	_ = p.runs.Create(context.Background(), stale, now)
	p.drive(t)
	if got := p.waitState(t, stale.ID, assistantModel.RunFailed); got.FailureReason != assistantModel.FailureLabNotReady {
		t.Fatalf("%+v", got)
	}
	eventually(t, "the lab to go", func() bool { return p.labs.count("destroy") == 1 })
}

func TestTheUsersCapAlsoCountsTheirBrainLabs(t *testing.T) {
	p := newProvideRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 1 })
	p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage())), p.frame(2, "r2", p.task(p.subject, labImage()))}
	p.serve(t)
	eventually(t, "both answered", func() bool { return p.cursor.get() == 2 })
	if refusalCode(t, p.brain.answered("r2")[0]) != "lab_unavailable" || strings.Contains(p.brain.answered("r1")[0], `"error"`) {
		t.Fatalf("%v %v", p.brain.answered("r1"), p.brain.answered("r2"))
	}
}
