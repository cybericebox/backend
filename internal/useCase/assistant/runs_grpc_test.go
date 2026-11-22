package assistant_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

// The stream is reopened with after_seq = the last stored event, and anything at or below it that the brain sends again is
// dropped: no event is stored twice, no answer judged twice.
func TestARunEventStreamThatRepeatsEventsIsDeduped(t *testing.T) {
	r := newRunRig(t, nil)
	r.labs.judged["login"] = "ICE{real}"
	run := r.start(t)
	id := run.ID.String()
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.Emit(id, "status", `{"type":"status"}`)
	r.brn.Emit(id, "answer", `{"type":"answer","subtask_ref":"login","value":"ICE{real}"}`)
	eventually(t, "two events", func() bool { return r.runs.state(run.ID).LastSeq == 2 })
	// the same seq again on the open stream, and a reopened stream that sends everything from the first event
	r.brn.EmitAt(id, 2, "answer", `{"type":"answer","subtask_ref":"login","value":"ICE{real}"}`)
	r.brn.Replay(id)
	r.brn.DropStreams()
	eventually(t, "a second stream", func() bool { return r.brn.Opened(id) >= 2 })
	r.brn.AllowStreams()
	r.brn.EmitAt(id, 3, "run_finished", `{"type":"run_finished","status":"done"}`)
	r.waitState(t, run.ID, assistantModel.RunFinished)
	events, _ := r.runs.Events(context.Background(), run.ID, 0, 100)
	if len(events) != 3 {
		t.Fatalf("each event once: %+v", events)
	}
	if n := r.labs.count("judge:login"); n != 1 {
		t.Fatalf("the answer is judged once: %d", n)
	}
	if v := r.brn.VerdictRequests(id); len(v) != 1 || v[0].GetAnswerSeq() != 2 {
		t.Fatalf("one verdict, keyed by the answer's seq: %v", v)
	}
	if afters := r.brn.Afters(id); afters[len(afters)-1] < 2 {
		t.Fatalf("reopened after the last stored event: %v", afters)
	}
}

// A stream that closes without run_finished is not the end of the run; NotFound (the brain does not know the run) is.
func TestARunTheBrainDoesNotKnowIsClosedAsBrainFailed(t *testing.T) {
	r := newRunRig(t, nil)
	run := r.start(t)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	r.brn.SetEventsStatus(codes.NotFound)
	r.brn.DropStreams()
	got := r.waitState(t, run.ID, assistantModel.RunFailed)
	if got.FailureReason != assistantModel.FailureBrainFailed {
		t.Fatalf("%+v", got)
	}
}

func TestStartRunCodesFollowTheErrorModel(t *testing.T) {
	t.Run("AlreadyExists: an earlier leader started it", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.SetStartStatus(codes.AlreadyExists)
		run := r.start(t)
		r.drive(t)
		r.waitState(t, run.ID, assistantModel.RunRunning)
	})
	t.Run("a rate limit is tried again", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.SetStartStatus(codes.ResourceExhausted)
		run := r.start(t)
		r.drive(t)
		eventually(t, "a few tries", func() bool { return r.labs.lookCount() > 3 })
		r.brn.SetStartStatus(codes.OK)
		r.waitState(t, run.ID, assistantModel.RunRunning)
	})
	t.Run("an internal error of the brain refuses the run", func(t *testing.T) {
		r := newRunRig(t, nil)
		r.brn.SetStartStatus(codes.Internal)
		run := r.start(t)
		r.drive(t)
		if got := r.waitState(t, run.ID, assistantModel.RunFailed); got.FailureReason != assistantModel.FailureStartRefused {
			t.Fatalf("%+v", got)
		}
	})
}
