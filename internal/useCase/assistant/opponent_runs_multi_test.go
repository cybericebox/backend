package assistant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/useCase/assistant"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

func defender() exerciseModel.Opponent {
	return exerciseModel.Opponent{Role: exerciseModel.OpponentDefense, Band: 2, Goal: []string{"root"}, Operates: []string{"web"}}
}

func (r *runRig) tryOpponents(t *testing.T, indexes ...int) []assistantModel.Run {
	t.Helper()
	runs, err := r.uc.StartOpponentRuns(context.Background(), runActor, runExercise, runVersion, runVariant, indexes)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestSeveralOpponentsOfAVariantPlayInOneLabEachWithItsOwnRunAndDevice(t *testing.T) {
	r := opponentRig(t, attacker(), defender(), attacker())
	r.fake.Opponents.Answer(func(req *brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
		return &brainapiv1.OpponentSpec{SpecId: "spec-" + req.GetRole(), Deploy: &brainapiv1.Deploy{Image: "registry.brain.test/eda/" + req.GetRole() + ":1"}}, nil
	})
	runs := r.tryOpponents(t, 3, 2)
	if len(runs) != 2 || runs[0].ID == runs[1].ID || runs[0].DeployID != runs[1].DeployID || runs[0].DeployID == uuid.Nil {
		t.Fatalf("two runs of one lab: %+v", runs)
	}
	if runs[0].Opponent != 3 || runs[0].ToolsIndex != 0 || runs[0].Role != "attack" || runs[1].Opponent != 2 || runs[1].ToolsIndex != 1 || runs[1].Role != "defense" {
		t.Fatalf("each run knows its opponent and the index of its device: %+v", runs)
	}
	if runs[0].SpecID != "spec-attack" || runs[1].SpecID != "spec-defense" {
		t.Fatalf("each run has its own spec: %+v", runs)
	}
	if r.labs.count("deploy-tools") != 1 || r.labs.deploys() != 1 {
		t.Fatalf("one lab: %v", r.labs.called())
	}
	calls := r.labs.placeCalls
	if len(calls) != 1 || len(calls[0]) != 2 || calls[0][0].Foothold != "dmz" || len(calls[0][1].Beside) != 1 || calls[0][0].Image != "registry.brain.test/eda/attack:1" {
		t.Fatalf("the placements in the order of the runs: %+v", calls)
	}
	asked := r.fake.Opponents.Requests()
	if len(asked) != 2 || asked[0].GetRunId() != runs[0].ID.String() || asked[1].GetRunId() != runs[1].ID.String() {
		t.Fatalf("GetOpponent once per run: %v", asked)
	}

	r.drive(t)
	r.waitState(t, runs[0].ID, assistantModel.RunRunning)
	r.waitState(t, runs[1].ID, assistantModel.RunRunning)
	eventually(t, "StartRun twice", func() bool { return len(r.brn.StartedRequests()) == 2 })
	addrs, entries := map[string]string{}, map[string]bool{}
	for _, start := range r.brn.StartedRequests() {
		addrs[start.GetRunId()] = start.GetAccess().GetVpn().GetAddr()
		entries[start.GetAccess().GetVpn().GetEntry()] = true
	}
	if addrs[runs[0].ID.String()] != "10.8.4.10:9099" || addrs[runs[1].ID.String()] != "10.8.4.11:9099" {
		t.Fatalf("each run reaches the runner of its own device: %v", addrs)
	}
	a, b := r.runs.state(runs[0].ID), r.runs.state(runs[1].ID)
	if a.AccessClient == "" || a.AccessClient == b.AccessClient || r.labs.count("mint:"+a.AccessClient) != 1 || r.labs.count("mint:"+b.AccessClient) != 1 {
		t.Fatalf("each run has its own VPN client: %q %q %v", a.AccessClient, b.AccessClient, r.labs.called())
	}
	clients := map[string]int{}
	for _, c := range r.uc.LabClients(context.Background(), runActor.UserID) {
		if c.Control {
			clients[c.Name] = c.ToolsIndex
		}
	}
	if len(clients) != 2 || clients[a.AccessClient] != 0 || clients[b.AccessClient] != 1 {
		t.Fatalf("the group policy gives each client its own device: %v", clients)
	}
}

func TestTheSharedLabIsDestroyedOnlyWhenItsLastRunEnds(t *testing.T) {
	r := opponentRig(t, attacker(), defender())
	runs := r.tryOpponents(t, 1, 2)
	r.drive(t)
	r.waitState(t, runs[0].ID, assistantModel.RunRunning)
	r.waitState(t, runs[1].ID, assistantModel.RunRunning)

	first := runs[0].ID.String()
	r.brn.Emit(first, "run_finished", `{"type":"run_finished","status":"done"}`)
	ended := r.waitState(t, runs[0].ID, assistantModel.RunFinished)
	if r.labs.count("revoke:"+ended.AccessClient) != 1 {
		t.Fatalf("the run that ended revokes its own client: %v", r.labs.called())
	}
	if r.labs.count("destroy") != 0 {
		t.Fatalf("the lab stays while another run plays in it: %v", r.labs.called())
	}
	if got := r.runs.state(runs[1].ID); got.State != assistantModel.RunRunning || r.labs.count("revoke:"+got.AccessClient) != 0 {
		t.Fatalf("the other run goes on with its client: %+v", got)
	}

	if _, err := r.uc.StopOpponentRun(context.Background(), runActor, runExercise, runVersion, runVariant, 2, runs[1].ID); err != nil {
		t.Fatal(err)
	}
	last := r.waitState(t, runs[1].ID, assistantModel.RunStopped)
	eventually(t, "the lab to be destroyed", func() bool { return r.labs.count("destroy") == 1 })
	if r.labs.count("revoke:"+last.AccessClient) != 1 {
		t.Fatalf("%v", r.labs.called())
	}
}

func TestAFailedSharedLabEndsEveryRunAndIsDestroyed(t *testing.T) {
	r := opponentRig(t, attacker(), defender())
	runs := r.tryOpponents(t, 1, 2)
	r.drive(t)
	r.waitState(t, runs[0].ID, assistantModel.RunRunning)
	r.waitState(t, runs[1].ID, assistantModel.RunRunning)
	r.labs.mu.Lock()
	r.labs.gone = true
	r.labs.mu.Unlock()
	r.waitState(t, runs[0].ID, assistantModel.RunFailed)
	r.waitState(t, runs[1].ID, assistantModel.RunFailed)
	eventually(t, "the lab to be destroyed", func() bool { return r.labs.count("destroy") >= 1 })
}

func TestStartOpponentRunsRefusals(t *testing.T) {
	ctx := context.Background()
	start := func(r *runRig, indexes ...int) error {
		_, err := r.uc.StartOpponentRuns(ctx, runActor, runExercise, runVersion, runVariant, indexes)
		return err
	}
	t.Run("none, a duplicate, an unknown one", func(t *testing.T) {
		r := opponentRig(t, attacker(), defender())
		for _, indexes := range [][]int{nil, {1, 1}, {1, 3}, {0, 1}} {
			if err := start(r, indexes...); !errors.Is(err, assistantModel.ErrAssistantOpponentNotFound.Err()) {
				t.Fatalf("%v: %v", indexes, err)
			}
		}
		if r.labs.deploys() != 0 {
			t.Fatal("no lab")
		}
	})
	t.Run("more than MaxOpponents", func(t *testing.T) {
		many := make([]exerciseModel.Opponent, exerciseModel.MaxOpponents+1)
		indexes := make([]int, len(many))
		for i := range many {
			many[i], indexes[i] = attacker(), i+1
		}
		r := newRunRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 100 })
		r.labs.opponents = many
		if err := start(r, indexes...); !errors.Is(err, assistantModel.ErrAssistantOpponentNotFound.Err()) {
			t.Fatalf("%v", err)
		}
		if r.labs.deploys() != 0 {
			t.Fatal("no lab")
		}
	})
	t.Run("an attacker without a foothold among them: nothing is deployed", func(t *testing.T) {
		lost := attacker()
		lost.Foothold = ""
		r := opponentRig(t, defender(), lost)
		if err := start(r, 1, 2); !errors.Is(err, assistantModel.ErrAssistantOpponentSpecInvalid.Err()) || r.labs.deploys() != 0 {
			t.Fatalf("%v %d", err, r.labs.deploys())
		}
	})
	t.Run("each run counts toward the user's cap", func(t *testing.T) {
		r := newRunRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 2 })
		r.labs.opponents = []exerciseModel.Opponent{attacker(), defender(), attacker()}
		if err := start(r, 1, 2, 3); !errors.Is(err, assistantModel.ErrAssistantRunLimit.Err()) || r.labs.deploys() != 0 {
			t.Fatalf("%v", err)
		}
		r.tryOpponents(t, 1, 2)
	})
	t.Run("an opponent that is already active", func(t *testing.T) {
		r := opponentRig(t, attacker(), defender())
		r.tryOpponent(t, 2)
		if err := start(r, 1, 2); !errors.Is(err, assistantModel.ErrAssistantRunActive.Err()) || r.labs.deploys() != 1 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a lab that cannot hold the devices", func(t *testing.T) {
		r := opponentRig(t, attacker(), defender())
		r.labs.toolsErr = exerciseModel.ErrToolsTooMany
		if err := start(r, 1, 2); !errors.Is(err, assistantModel.ErrAssistantOpponentUnavailable.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a lost race for one opponent takes every run and the lab down", func(t *testing.T) {
		r := opponentRig(t, attacker(), defender())
		r.runs.refuseOpponent = 2
		if err := start(r, 1, 2); !errors.Is(err, assistantModel.ErrAssistantRunActive.Err()) {
			t.Fatalf("%v", err)
		}
		if r.labs.count("destroy") != 1 {
			t.Fatalf("the lab is taken down: %v", r.labs.called())
		}
		for _, run := range r.runs.all() {
			if run.State.Active() && run.State != assistantModel.RunClosing {
				t.Fatalf("no run is left starting: %+v", run)
			}
		}
	})
}
