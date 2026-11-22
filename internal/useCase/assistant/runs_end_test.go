package assistant_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	"github.com/cybericebox/backend/internal/model/errorJournal"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

// journalSink is the error journal of a test: what was reported.
type journalSink struct {
	mu     sync.Mutex
	events []errorJournal.Event
}

func (j *journalSink) Report(e errorJournal.Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, e)
}
func (j *journalSink) all() []errorJournal.Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]errorJournal.Event(nil), j.events...)
}

func captureJournal(t *testing.T) *journalSink {
	t.Helper()
	sink := &journalSink{}
	errorJournal.SetReporter(sink)
	t.Cleanup(func() { errorJournal.SetReporter(nil) })
	return sink
}

// run_finished "stopped" (StopRun, or the brain shutting down) is a cancelled run: it ends stopped, not failed, with the same
// teardown and no failure reason; "failed" stays a failure.
func TestARunTheBrainStoppedEndsStoppedNotFailed(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","run_id":"`+id+`","status":"stopped"}`)
	got := r.waitState(t, run.ID, assistantModel.RunStopped)
	if got.FailureReason != "" {
		t.Fatalf("a cancelled run is no failure: %+v", got)
	}
	if indexOf(r.labs.called(), "revoke:"+got.AccessClient) < 0 || r.labs.count("destroy") != 1 {
		t.Fatalf("the same teardown: %v", r.labs.called())
	}
	if len(r.brn.Stops()) != 0 {
		t.Fatalf("the brain stopped it itself: no StopRun: %v", r.brn.Stops())
	}
}

// A verdict the brain refuses for the platform's credentials (PermissionDenied, Unauthenticated) is kept unsent and, once it
// persists over a few passes, raises one alarm in the error journal naming the run.
func TestAVerdictRefusedForTheCredentialsRaisesAnAlarmAndStaysUnsent(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated} {
		t.Run(code.String(), func(t *testing.T) {
			sink := captureJournal(t)
			r := newRunRig(t, nil)
			r.labs.stateTasks = []exerciseUseCase.StateSubtask{{Key: "service", Passed: true}}
			r.brn.FailVerdicts(1<<20, code)
			run := r.start(t)
			r.drive(t)
			r.waitState(t, run.ID, assistantModel.RunRunning)
			eventually(t, "the alarm", func() bool { return len(sink.all()) >= 1 })
			time.Sleep(50 * time.Millisecond)
			alarms := sink.all()
			if len(alarms) != 1 || alarms[0].Details["run"] != run.ID.String() || !strings.Contains(alarms[0].Message, code.String()) {
				t.Fatalf("one alarm naming the run: %+v", alarms)
			}
			if unsent, _ := r.runs.Unsent(t.Context(), run.ID); len(unsent) != 1 {
				t.Fatalf("the verdict stays unsent: %+v", unsent)
			}
		})
	}
}

// An internal error of the brain is an ordinary retry on the next pass: no alarm.
func TestAVerdictFailingWithAnInternalErrorIsRetriedWithoutAnAlarm(t *testing.T) {
	sink := captureJournal(t)
	r := newRunRig(t, nil)
	r.labs.stateTasks = []exerciseUseCase.StateSubtask{{Key: "service", Passed: true}}
	r.brn.FailVerdicts(5, codes.Internal)
	run := r.start(t)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	eventually(t, "the verdict after the failures", func() bool { return len(r.brn.VerdictRequests(run.ID.String())) == 1 })
	if alarms := sink.all(); len(alarms) != 0 {
		t.Fatalf("no alarm: %+v", alarms)
	}
}
