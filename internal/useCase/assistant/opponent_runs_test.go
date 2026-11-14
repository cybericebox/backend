package assistant_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
)

func opponentRig(t *testing.T, opponents ...exerciseModel.Opponent) *runRig {
	t.Helper()
	r := newRunRig(t, func(d *assistant.Dependencies) {
		d.OpponentBudget = assistantModel.RunBudget{Actions: 40, Tokens: 9000, Time: 600}
	})
	r.labs.opponents = opponents
	return r
}

func attacker() exerciseModel.Opponent {
	return exerciseModel.Opponent{Role: exerciseModel.OpponentAttack, Band: 3, Goal: []string{"root", "user"}, Brief: "think like a thief"}
}

func (r *runRig) tryOpponent(t *testing.T, index int) assistantModel.Run {
	t.Helper()
	run, err := r.uc.StartOpponentRun(context.Background(), runActor, runExercise, runVersion, runVariant, index)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestAnOpponentRunStartsWithTheManifestTheBudgetAndTheEDAAccess(t *testing.T) {
	r := opponentRig(t, attacker(), exerciseModel.Opponent{Role: exerciseModel.OpponentDefense, Band: 2, Goal: []string{"root"}, Operates: []string{"web"}})
	r.labs.readyAfter = 1
	run := r.tryOpponent(t, 2)
	if run.Role != "defense" || run.Opponent != 2 || run.State != assistantModel.RunStarting || run.Actor != runActor.UserID {
		t.Fatalf("%+v", run)
	}
	if r.labs.count("deploy-tools") != 1 {
		t.Fatalf("the lab is deployed with the tools device: %v", r.labs.called())
	}
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	eventually(t, "StartRun", func() bool { return len(r.brn.Started()) == 1 })
	if order := r.brn.Order(); len(order) != 1 || order[0] != "start:"+run.ID.String() {
		t.Fatalf("an opponent gets no materials: %v", order)
	}
	var got struct {
		Role     string                       `json:"role"`
		Band     int                          `json:"band"`
		Goal     []string                     `json:"goal"`
		Operates []string                     `json:"operates"`
		Start    string                       `json:"start"`
		Budget   assistantModel.RunBudget     `json:"budget"`
		Access   map[string]map[string]string `json:"access"`
	}
	if err := json.Unmarshal([]byte(r.brn.Started()[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Role != "defense" || got.Band != 2 || len(got.Goal) != 1 || got.Goal[0] != "root" || len(got.Operates) != 1 || got.Operates[0] != "web" || got.Start != "immediate" {
		t.Fatalf("%s", r.brn.Started()[0])
	}
	if got.Budget != (assistantModel.RunBudget{Actions: 40, Tokens: 9000, Time: 600}) {
		t.Fatalf("the platform's budget: %+v", got.Budget)
	}
	if got.Access["eda"]["url"] != "https://proxy.example/tools" || got.Access["eda"]["token"] != "secret-bearer" || len(got.Access) != 1 {
		t.Fatalf("the eda access of the tools device only: %s", r.brn.Started()[0])
	}
	if r.labs.edaRun != run.ID.String() || r.labs.edaDevice != "" || !r.labs.edaUntil.After(run.ExpiresAt) {
		t.Fatalf("the token is for this run, up to its window plus the grace: run %q until %v", r.labs.edaRun, r.labs.edaUntil)
	}
	if !strings.HasPrefix(r.runs.state(run.ID).AccessClient, "g-") {
		t.Fatalf("the run's own client: %+v", r.runs.state(run.ID))
	}
}

func TestAnOpponentThatStartsAfterASubtaskWaitsForItToBeSolvedInTheLab(t *testing.T) {
	late := attacker()
	late.Start = exerciseModel.OpponentStart{After: "login"}
	r := opponentRig(t, late)
	run := r.tryOpponent(t, 1)
	r.drive(t)
	eventually(t, "the lab to be looked at", func() bool { return r.labs.lookCount() > 3 })
	time.Sleep(30 * time.Millisecond)
	if got := r.runs.state(run.ID); got.State != assistantModel.RunStarting || len(r.brn.Started()) != 0 {
		t.Fatalf("not started before the subtask is solved: %+v", got)
	}
	r.labs.setSolved("login")
	r.waitState(t, run.ID, assistantModel.RunRunning)
	if len(r.brn.Started()) != 1 {
		t.Fatalf("%v", r.brn.Started())
	}
	// the wait is bounded: an opponent whose subtask is never solved ends with the run's window
}

func TestAnOpponentRunIsTornDownLikeAnyRun(t *testing.T) {
	r := opponentRig(t, attacker())
	run := r.tryOpponent(t, 1)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	id := run.ID.String()
	r.brn.Emit(id, "measure", `{"type":"measure","run_id":"`+id+`","steps":7}`)
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","run_id":"`+id+`","status":"done"}`)
	got := r.waitState(t, run.ID, assistantModel.RunFinished)
	if !strings.Contains(string(got.Measurement), `"steps":7`) {
		t.Fatalf("the measurement is stored: %s", got.Measurement)
	}
	if indexOf(r.labs.called(), "revoke:"+got.AccessClient) < 0 || r.labs.count("destroy") != 1 {
		t.Fatalf("the client is revoked and the lab with the tools device destroyed: %v", r.labs.called())
	}
	events, _ := r.runs.Events(context.Background(), run.ID, 0, 10)
	if len(events) != 2 {
		t.Fatalf("events are stored verbatim: %v", events)
	}
	if assistantModel.ExcludeRun(got.Role) != true {
		t.Fatal("an opponent never counts in the participant statistics")
	}
}

func TestStoppingAnOpponentRunTellsTheBrainRevokesAndDestroys(t *testing.T) {
	r := opponentRig(t, attacker())
	run := r.tryOpponent(t, 1)
	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	if _, err := r.uc.StopOpponentRun(context.Background(), runActor, runExercise, runVersion, runVariant, 1, run.ID); err != nil {
		t.Fatal(err)
	}
	got := r.waitState(t, run.ID, assistantModel.RunStopped)
	if len(r.brn.Stops()) != 1 || r.brn.Stops()[0] != run.ID.String() {
		t.Fatalf("the brain is told: %v", r.brn.Stops())
	}
	if indexOf(r.labs.called(), "revoke:"+got.AccessClient) < 0 || r.labs.count("destroy") != 1 {
		t.Fatalf("%v", r.labs.called())
	}
}

func TestOpponentRunRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("not authorized: nothing is deployed", func(t *testing.T) {
		r := opponentRig(t, attacker())
		r.labs.authErr = errors.New("forbidden")
		if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 1); err == nil || r.labs.deployCount != 0 {
			t.Fatalf("%v %d", err, r.labs.deployCount)
		}
	})
	t.Run("no such opponent", func(t *testing.T) {
		r := opponentRig(t, attacker())
		for _, index := range []int{0, 2, -1} {
			if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, index); !errors.Is(err, assistantModel.ErrAssistantOpponentNotFound.Err()) {
				t.Fatalf("%d: %v", index, err)
			}
		}
		if r.labs.deployCount != 0 {
			t.Fatal("no lab")
		}
	})
	t.Run("a task of another exercise or variant", func(t *testing.T) {
		r := opponentRig(t, attacker())
		if _, err := r.uc.StartOpponentRun(ctx, runActor, uuid.Must(uuid.NewV7()), runVersion, runVariant, 1); err == nil {
			t.Fatal("the exercise must match the version")
		}
		if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, uuid.Must(uuid.NewV7()), 1); err == nil {
			t.Fatal("the variant must match")
		}
		if r.labs.deployCount != 0 {
			t.Fatal("no lab")
		}
	})
	t.Run("a variant without a lab", func(t *testing.T) {
		r := opponentRig(t, attacker())
		r.labs.hasLab = false
		if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 1); !errors.Is(err, exerciseModel.ErrTestDeployNoLab.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("no tools image is configured", func(t *testing.T) {
		r := opponentRig(t, attacker())
		r.labs.toolsErr = exerciseModel.ErrToolsNoImage
		if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 1); !errors.Is(err, assistantModel.ErrAssistantOpponentUnavailable.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("one active run per opponent, but another opponent may run", func(t *testing.T) {
		r := opponentRig(t, attacker(), attacker())
		r.tryOpponent(t, 1)
		if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 1); !errors.Is(err, assistantModel.ErrAssistantRunActive.Err()) {
			t.Fatalf("%v", err)
		}
		r.tryOpponent(t, 2)
	})
	t.Run("a user's cap", func(t *testing.T) {
		r := newRunRig(t, func(d *assistant.Dependencies) { d.MaxActiveRuns = 1 })
		r.labs.opponents = []exerciseModel.Opponent{attacker(), attacker()}
		r.tryOpponent(t, 1)
		if _, err := r.uc.StartOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 2); !errors.Is(err, assistantModel.ErrAssistantRunLimit.Err()) {
			t.Fatalf("%v", err)
		}
	})
}

func TestOpponentRunsAreBoundToTheirOpponentAndVariant(t *testing.T) {
	ctx := context.Background()
	r := opponentRig(t, attacker(), attacker())
	run := r.tryOpponent(t, 1)
	grader := r.start(t)
	other := uuid.Must(uuid.NewV7())
	notFound := assistantModel.ErrAssistantRunNotFound.Err()

	if listed, err := r.uc.OpponentRuns(ctx, runActor, runExercise, runVersion, runVariant, 1); err != nil || len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("%v %v", err, listed)
	}
	if listed, err := r.uc.OpponentRuns(ctx, runActor, runExercise, runVersion, runVariant, 2); err != nil || len(listed) != 0 {
		t.Fatalf("another opponent has none: %v %v", err, listed)
	}
	if listed, _ := r.uc.GraderRuns(ctx, runActor, runExercise, runVersion, runVariant); len(listed) != 1 || listed[0].ID != grader.ID {
		t.Fatalf("an opponent run is not a grader run: %v", listed)
	}
	for name, call := range map[string]func() error{
		"get another opponent": func() error {
			_, err := r.uc.OpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 2, run.ID)
			return err
		},
		"get as the grader run": func() error {
			_, err := r.uc.GraderRun(ctx, runActor, runExercise, runVersion, runVariant, run.ID)
			return err
		},
		"get a grader run as an opponent's": func() error {
			_, err := r.uc.OpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 1, grader.ID)
			return err
		},
		"get under another exercise": func() error {
			_, err := r.uc.OpponentRun(ctx, runActor, other, runVersion, runVariant, 1, run.ID)
			return err
		},
		"get under another version": func() error {
			_, err := r.uc.OpponentRun(ctx, runActor, runExercise, other, runVariant, 1, run.ID)
			return err
		},
		"get under another variant": func() error {
			_, err := r.uc.OpponentRun(ctx, runActor, runExercise, runVersion, other, 1, run.ID)
			return err
		},
		"stop another opponent": func() error {
			_, err := r.uc.StopOpponentRun(ctx, runActor, runExercise, runVersion, runVariant, 2, run.ID)
			return err
		},
		"stop as the grader run": func() error {
			_, err := r.uc.StopGraderRun(ctx, runActor, runExercise, runVersion, runVariant, run.ID)
			return err
		},
		"events of another opponent": func() error {
			_, err := r.uc.OpponentRunEvents(ctx, runActor, runExercise, runVersion, runVariant, 2, run.ID)
			return err
		},
		"events as the grader run": func() error {
			_, err := r.uc.GraderRunEvents(ctx, runActor, runExercise, runVersion, runVariant, run.ID)
			return err
		},
	} {
		if err := call(); !errors.Is(err, notFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := r.runs.state(run.ID); got.State != assistantModel.RunStarting {
		t.Fatalf("a refused stop changes nothing: %+v", got)
	}
	if _, err := r.uc.OpponentRunEvents(ctx, runActor, runExercise, runVersion, runVariant, 1, run.ID); err != nil {
		t.Fatalf("its own events: %v", err)
	}
}
