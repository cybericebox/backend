package assistant_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/useCase/assistant"
)

// endWithoutTeardown ends runs the way a leader that stopped between finishing them and destroying their lab leaves them: every
// run of the lab is over, the lab is still there.
func (r *runRig) endWithoutTeardown(t *testing.T, runs ...assistantModel.Run) {
	t.Helper()
	ctx := context.Background()
	for _, run := range runs {
		if _, err := r.runs.Close(ctx, run.ID, assistantModel.RunFinished, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := r.runs.Finish(ctx, run.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// A lab whose runs all ended but that was never destroyed (the destroy after the last run failed, or the leader stopped right
// after finishing it) is found by the leader and destroyed; a failing destroy is tried again on the next sweep.
func TestTheLeaderDestroysALabWhoseRunsAllEndedWithoutIt(t *testing.T) {
	r := newRunRig(t, func(d *assistant.Dependencies) { d.LabSweep = 5 * time.Millisecond })
	r.labs.opponents = []exerciseModel.Opponent{attacker(), defender()}
	runs := r.tryOpponents(t, 1, 2)
	r.endWithoutTeardown(t, runs...)
	if r.labs.count("destroy") != 0 || !r.labs.isPresent(runs[0].DeployID) {
		t.Fatal("the lab is left behind")
	}
	r.labs.mu.Lock()
	r.labs.destroyErr = errors.New("agent down")
	r.labs.mu.Unlock()
	r.drive(t)
	eventually(t, "a first try", func() bool { return r.labs.count("destroy") >= 1 })
	r.labs.mu.Lock()
	r.labs.destroyErr = nil
	r.labs.mu.Unlock()
	eventually(t, "the lab to be destroyed", func() bool { return !r.labs.isPresent(runs[0].DeployID) })
	tries := r.labs.count("destroy")
	time.Sleep(50 * time.Millisecond)
	if r.labs.count("destroy") != tries {
		t.Fatalf("a lab that is gone is not destroyed again: %d then %d", tries, r.labs.count("destroy"))
	}
}

// A lab in which a run still plays is never swept, even when the other runs of it ended.
func TestTheSweepLeavesALabARunStillPlaysIn(t *testing.T) {
	r := newRunRig(t, func(d *assistant.Dependencies) { d.LabSweep = 5 * time.Millisecond })
	r.labs.opponents = []exerciseModel.Opponent{attacker(), defender()}
	runs := r.tryOpponents(t, 1, 2)
	r.drive(t)
	r.waitState(t, runs[1].ID, assistantModel.RunRunning)
	r.endWithoutTeardown(t, runs[0])
	time.Sleep(50 * time.Millisecond)
	if r.labs.count("destroy") != 0 {
		t.Fatalf("the lab stays while a run plays in it: %v", r.labs.called())
	}
}

// The runs of one request are stored in one step, after the lab is made: no driver can see one of them (and end it, and take the
// lab down) before the others exist.
func TestTheRunsOfOneRequestAreStoredTogether(t *testing.T) {
	r := newRunRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 3 })
	r.labs.opponents = []exerciseModel.Opponent{attacker(), defender(), attacker()}
	runs := r.tryOpponents(t, 3, 1, 2)
	batches, singles := r.runs.batchesMade()
	if singles != 0 || len(batches) != 1 || len(batches[0]) != 3 || batches[0][0] != runs[0].ID || batches[0][2] != runs[2].ID {
		t.Fatalf("one batch of three, nothing one by one: %v %d", batches, singles)
	}
}

// Two requests of one user at the same moment cannot pass the cap of active runs together: both pass the early check, the store
// lets one of them in, and the other's lab is taken down again.
func TestConcurrentRequestsCannotPassTheCapTogether(t *testing.T) {
	r := newRunRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 2 })
	r.labs.opponents = []exerciseModel.Opponent{attacker(), defender(), attacker(), defender()}
	var arrived sync.WaitGroup
	arrived.Add(2)
	r.labs.beforeDeploy = func() { arrived.Done(); arrived.Wait() } // both have passed the early check
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, indexes := range [][]int{{1, 2}, {3, 4}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.uc.StartOpponentRuns(context.Background(), runActor, runExercise, runVersion, runVariant, indexes)
		}()
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, assistantModel.ErrAssistantRunLimit.Err()):
			limited++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || limited != 1 {
		t.Fatalf("one request in, one refused: %v", errs)
	}
	if n, _ := r.runs.CountActiveOf(context.Background(), runActor.UserID); n != 2 {
		t.Fatalf("never more than the cap: %d", n)
	}
	if r.labs.deploys() != 2 || r.labs.count("destroy") != 1 {
		t.Fatalf("the refused request's lab is taken down: %v", r.labs.called())
	}
}

// A task with its own device named like a control gateway of the platform cannot get the opponents' devices: a typed refusal,
// not a platform error.
func TestATasksOwnControlGatewayNameIsATypedRefusal(t *testing.T) {
	r := opponentRig(t, attacker(), defender())
	r.labs.toolsErr = exerciseModel.ErrToolsNoControl
	_, err := r.uc.StartOpponentRuns(context.Background(), runActor, runExercise, runVersion, runVariant, []int{1, 2})
	if !errors.Is(err, assistantModel.ErrAssistantOpponentControlName.Err()) {
		t.Fatalf("%v", err)
	}
}
