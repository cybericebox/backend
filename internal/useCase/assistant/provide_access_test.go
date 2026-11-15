package assistant_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
)

func (p *provideRig) accessFrame(seq int64, id, field, ref string) outboxFrame {
	return outboxFrame{seq: seq, event: "provide_access", data: fmt.Sprintf(`{"request_id":%q,"type":"provide_access",%q:%q}`, id, field, ref)}
}

// row puts a run row of the given role and state into the store.
func (p *provideRig) row(t *testing.T, role string, opponent int, state assistantModel.RunState) assistantModel.Run {
	t.Helper()
	now := time.Now()
	run := assistantModel.Run{
		ID: uuid.Must(uuid.NewV7()), ExerciseID: runExercise, VersionID: runVersion, VariantID: runVariant, DeployID: runDeploy, Actor: runActor.UserID,
		Role: role, Opponent: opponent, State: state, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := p.runs.Create(context.Background(), run, now); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestProvideAccessAnswersAFreshHandoffForTheToolsDeviceOfARun(t *testing.T) {
	p := newProvideRig(t, nil)
	tools := p.row(t, assistantModel.RoleTools, 0, assistantModel.RunRunning)
	opponent := p.row(t, "attack", 1, assistantModel.RunStarting)
	p.brain.frames = []outboxFrame{
		p.accessFrame(1, "a1", "run_id", opponent.ID.String()),
		p.accessFrame(2, "a2", "device_ref", tools.ID.String()),
		p.accessFrame(3, "a3", "device_ref", tools.ID.String()), // as often as the run needs it
	}
	p.serve(t)
	for _, id := range []string{"a1", "a2", "a3"} {
		var got struct {
			Result struct {
				Handoff   string `json:"handoff"`
				URL       string `json:"url"`
				ExpiresAt string `json:"expires_at"`
			}
		}
		if err := json.Unmarshal([]byte(p.answeredOnce(t, id)), &got); err != nil {
			t.Fatal(err)
		}
		if got.Result.Handoff != "secret-handoff" || got.Result.URL != "https://tools-abc123g.lab.example.com:443" || got.Result.ExpiresAt != "2026-11-15T12:01:00Z" {
			t.Fatalf("%s: %v", id, p.brain.answered(id))
		}
	}
	if p.labs.handoffOwner != runActor.UserID || p.labs.handoffDeploy != runDeploy || p.labs.edaDevice != "" {
		t.Fatalf("the handoff is for the run's own lab: %v %v %q", p.labs.handoffOwner, p.labs.handoffDeploy, p.labs.edaDevice)
	}
	if !p.labs.edaUntil.After(time.Now()) {
		t.Fatalf("the session ends after now: %v", p.labs.edaUntil)
	}
}

func TestProvideAccessRefusals(t *testing.T) {
	foreign := uuid.Must(uuid.NewV7()).String()
	for name, c := range map[string]struct {
		ref  func(p *provideRig) (field, ref string)
		prep func(p *provideRig)
		code string
	}{
		"a run of nobody's": {ref: func(*provideRig) (string, string) { return "run_id", foreign }, code: "run_not_found"},
		"a grader run": {ref: func(p *provideRig) (string, string) {
			return "run_id", p.row(t, assistantModel.RoleGrader, 0, assistantModel.RunRunning).ID.String()
		}, code: "run_not_found"},
		"a lab made for a VPN": {ref: func(p *provideRig) (string, string) {
			return "device_ref", p.row(t, assistantModel.RoleLab, 0, assistantModel.RunRunning).ID.String()
		}, code: "run_not_found"},
		"an ended run": {ref: func(p *provideRig) (string, string) {
			return "run_id", p.row(t, "attack", 1, assistantModel.RunFinished).ID.String()
		}, code: "run_ended"},
		"a run that is closing": {ref: func(p *provideRig) (string, string) {
			return "run_id", p.row(t, "attack", 1, assistantModel.RunClosing).ID.String()
		}, code: "run_ended"},
		"a lab that is gone": {ref: func(p *provideRig) (string, string) {
			return "run_id", p.row(t, "attack", 1, assistantModel.RunRunning).ID.String()
		}, prep: func(p *provideRig) { p.labs.handoffErr = exerciseModel.ErrTestDeployNotFound.Err() }, code: "run_ended"},
		"a lab with no grpc device": {ref: func(p *provideRig) (string, string) {
			return "run_id", p.row(t, "attack", 1, assistantModel.RunRunning).ID.String()
		}, prep: func(p *provideRig) { p.labs.handoffErr = exerciseModel.ErrTestDeployNoGRPC.Err() }, code: "no_device"},
		"not an id": {ref: func(*provideRig) (string, string) { return "run_id", "x" }, code: "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvideRig(t, nil)
			if c.prep != nil {
				c.prep(p)
			}
			field, ref := c.ref(p)
			p.brain.frames = []outboxFrame{p.accessFrame(1, "a1", field, ref)}
			p.serve(t)
			answer := p.answeredOnce(t, "a1")
			if got := refusalCode(t, answer); got != c.code {
				t.Fatalf("%s", answer)
			}
			if strings.Contains(answer, "secret") {
				t.Fatalf("a refusal carries no handoff: %s", answer)
			}
		})
	}
}
