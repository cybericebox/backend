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
	return exerciseModel.Opponent{Role: exerciseModel.OpponentAttack, Band: 3, Goal: []string{"root", "user"}, Foothold: "dmz", Brief: "think like a thief"}
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
	if got.Access["eda"]["url"] != "https://tools-abc123g.lab.example.com:443" || len(got.Access["eda"]) != 1 || len(got.Access) != 1 {
		t.Fatalf("the address of the tools device only: %s", r.brn.Started()[0])
	}
	if strings.Contains(string(r.brn.Started()[0]), "secret") || strings.Contains(string(r.brn.Started()[0]), `"token"`) || strings.Contains(string(r.brn.Started()[0]), "lab_access") {
		t.Fatalf("StartRun carries no secret: %s", r.brn.Started()[0])
	}
	if got := r.runs.state(run.ID); got.AccessClient != "" {
		t.Fatalf("the bot is no lab client: %+v", got)
	}
	for _, call := range r.labs.called() {
		if strings.HasPrefix(call, "mint:") {
			t.Fatalf("no client is made for an opponent: %v", r.labs.called())
		}
	}
	// the tools device is placed with the operated devices and told its run and the brain's CA
	place := r.labs.places[0]
	if len(place.Beside) != 1 || place.Beside[0] != "web" || place.Foothold != "" {
		t.Fatalf("a defender sits with the devices it operates: %+v", place)
	}
	vars := place.Env
	if len(vars) != 2 || vars[0].Name != "EDA_RUN_ID" || vars[0].Value != run.ID.String() || vars[1].Name != "EDA_TRUST_CA" || !strings.Contains(vars[1].Value, "BEGIN CERTIFICATE") {
		t.Fatalf("%+v", vars)
	}
}

func TestAnAttackersToolsDeviceSitsOnItsFoothold(t *testing.T) {
	r := opponentRig(t, attacker())
	r.tryOpponent(t, 1)
	if place := r.labs.places[0]; place.Foothold != "dmz" || len(place.Beside) != 0 {
		t.Fatalf("%+v", place)
	}
	r.drive(t)
	eventually(t, "StartRun", func() bool { return len(r.brn.Started()) == 1 })
	if !strings.Contains(string(r.brn.Started()[0]), `"foothold":"dmz"`) {
		t.Fatalf("StartRun carries the foothold: %s", r.brn.Started()[0])
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
	if r.labs.count("destroy") != 1 {
		t.Fatalf("the lab with the tools device is destroyed: %v", r.labs.called())
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
	r.waitState(t, run.ID, assistantModel.RunStopped)
	if len(r.brn.Stops()) != 1 || r.brn.Stops()[0] != run.ID.String() {
		t.Fatalf("the brain is told: %v", r.brn.Stops())
	}
	if r.labs.count("destroy") != 1 {
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

func TestARunWhoseLabIsGoneIsTornDownAtOnceNotAtTheEndOfItsWindow(t *testing.T) {
	for name, start := range map[string]func(r *runRig) assistantModel.Run{
		"an opponent run": func(r *runRig) assistantModel.Run {
			r.labs.opponents = []exerciseModel.Opponent{attacker()}
			return r.tryOpponent(t, 1)
		},
		"a grader run": func(r *runRig) assistantModel.Run { return r.start(t) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRunRig(t, nil)
			run := start(r)
			r.drive(t)
			r.waitState(t, run.ID, assistantModel.RunRunning)
			r.labs.mu.Lock()
			r.labs.gone = true // destroyed by its author, or its lease is over
			r.labs.mu.Unlock()
			got := r.waitState(t, run.ID, assistantModel.RunFailed)
			if got.FailureReason != assistantModel.FailureLabFailed {
				t.Fatalf("%+v", got)
			}
			if r.labs.count("destroy") != 1 || len(r.brn.Stops()) != 1 || got.Opponent == 0 && indexOf(r.labs.called(), "revoke:"+got.AccessClient) < 0 {
				t.Fatalf("the lab is destroyed, a grader's client revoked and the brain told: %v %v", r.labs.called(), r.brn.Stops())
			}
		})
	}
}
