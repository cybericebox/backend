package assistant_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

// A re-sent provide_lab for a lab that is over is answered with the refusal, never left waiting.
func TestAReSentRequestForAClosedLabIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		answered bool
		state    assistantModel.RunState
		reason   string
		code     string
	}{
		"failed, never answered":    {false, assistantModel.RunFailed, assistantModel.FailureLabFailed, "lab_unavailable"},
		"not ready, never answered": {false, assistantModel.RunFailed, assistantModel.FailureLabNotReady, "lab_not_ready"},
		"answered, the lab is gone": {true, assistantModel.RunFinished, "", "lab_unavailable"},
		"answered, the lab stopped": {true, assistantModel.RunStopped, "", "lab_unavailable"},
		"closing, answered":         {true, assistantModel.RunClosing, "", "lab_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			now := p.clock.now()
			row := assistantModel.Run{ID: uuid.Must(uuid.NewV7()), Role: assistantModel.RoleLab, RequestID: "r1", Answered: c.answered,
				State: c.state, FailureReason: c.reason, Actor: runActor.UserID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			p.runs.mu.Lock()
			p.runs.runs[row.ID] = row
			p.runs.order = append(p.runs.order, row.ID)
			p.runs.mu.Unlock()
			p.brain.frames = []outboxFrame{p.frame(1, "r1", p.task(p.subject, labImage()))}
			p.serve(t)
			if got := refusalCode(t, p.answeredOnce(t, "r1")); got != c.code {
				t.Fatalf("%s", got)
			}
			if p.labs.deployCount != 0 {
				t.Fatal("no new lab for a request that has one")
			}
		})
	}
}

// failingCloseRuns fails the next Close calls (a database that is down).
type failingCloseRuns struct {
	*memRuns
	mu    sync.Mutex
	fails int
}

func (f *failingCloseRuns) Close(ctx context.Context, id uuid.UUID, end assistantModel.RunState, reason string, now time.Time) (bool, error) {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return false, errors.New("database down")
	}
	f.mu.Unlock()
	return f.memRuns.Close(ctx, id, end, reason, now)
}

// run_finished is stored only once its outcome is: a close that fails leaves the event unread, and the reopened stream
// delivers it again.
func TestRunFinishedIsNotStoredBeforeTheRunIsClosed(t *testing.T) {
	failing := &failingCloseRuns{memRuns: newMemRuns()}
	r := newRunRig(t, func(d *assistant.Dependencies) { d.Runs = failing })
	r.runs = failing.memRuns
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	failing.mu.Lock()
	failing.fails = 2
	failing.mu.Unlock()
	r.brn.Emit(id, "status", `{"type":"status"}`)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","status":"done"}`)
	got := r.waitState(t, run.ID, assistantModel.RunFinished)
	if got.LastSeq != 2 {
		t.Fatalf("%+v", got)
	}
	events, _ := r.runs.Events(context.Background(), run.ID, 0, 10)
	if len(events) != 2 || events[1].Type != "run_finished" {
		t.Fatalf("%+v", events)
	}
	if r.brn.Opened(id) < 2 {
		t.Fatal("the stream was reopened to deliver run_finished again")
	}
}

// A state subtask's verdict is state=true, keyed by (run_id, subtask_ref), with answer_seq unset.
func TestAStateVerdictIsSentAsAStateVerdict(t *testing.T) {
	r := newRunRig(t, nil)
	r.labs.stateTasks = []exerciseUseCase.StateSubtask{{Key: "service", Passed: true}}
	run := r.start(t)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	eventually(t, "the verdict", func() bool { return len(r.brn.VerdictRequests(run.ID.String())) == 1 })
	v := r.brn.VerdictRequests(run.ID.String())[0]
	if !v.GetState() || v.GetAnswerSeq() != 0 || v.GetSubtaskRef() != "service" || !v.GetCorrect() {
		t.Fatalf("%v", v)
	}
}
