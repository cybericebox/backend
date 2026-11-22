package assistant_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

const pullSecret = "short-lived-pull-token-9f2c"

// syncBuffer is a log sink the test reads while the driver writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

// captureLogs sends the global logger to a buffer for the test (debug level: everything).
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	sink := &syncBuffer{}
	before, level := log.Logger, zerolog.GlobalLevel()
	log.Logger = zerolog.New(sink)
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	t.Cleanup(func() { log.Logger = before; zerolog.SetGlobalLevel(level) })
	return sink
}

func spec(expires time.Time) *brainapiv1.OpponentSpec {
	return &brainapiv1.OpponentSpec{
		SpecId: "spec-7",
		Deploy: &brainapiv1.Deploy{
			Image:     "registry.brain.test/eda/runner@sha256:abc",
			Env:       map[string]string{"EDA_TRUST_CA": "ca-pem", "EDA_RUN_ID": "set-by-the-brain"},
			Resources: &brainapiv1.Resources{CpuMillis: 250, MemBytes: 1 << 30},
			Pull:      &brainapiv1.ImagePull{Registry: "registry.brain.test", Username: "pull-bot", Password: pullSecret, ExpiresAt: timestamppb.New(expires)},
		},
	}
}

// The new flow: GetOpponent(run_id, role, band, task_ref, version) gives the spec; the tools device is deployed with its image,
// env and size and its pull credential goes to the lab only; StartRun names the spec and carries the fixed inputs (access.vpn
// entry and addr, foothold). The pull credential is in no stored row and no log line.
func TestAnOpponentIsDeployedFromItsGetOpponentSpecAndThePullSecretIsNeverKept(t *testing.T) {
	logs := captureLogs(t)
	r := opponentRig(t, attacker())
	r.fake.Opponents.Answer(func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
		return spec(time.Now().Add(time.Hour)), nil
	})
	run := r.tryOpponent(t, 1)

	asked := r.fake.Opponents.Requests()
	if len(asked) != 1 || asked[0].GetRunId() != run.ID.String() || asked[0].GetRole() != "attack" || asked[0].GetBand() != 3 ||
		asked[0].GetTaskRef() != runExercise.String() || asked[0].GetVersion() != runVersion.String() {
		t.Fatalf("GetOpponent: %v", asked)
	}
	place := r.labs.places[0]
	if place.Image != "registry.brain.test/eda/runner@sha256:abc" || place.CPUMillis != 250 || place.MemoryBytes != 1<<30 || place.Foothold != "dmz" {
		t.Fatalf("the spec's image and size, the manifest's place: %+v", place)
	}
	if len(place.Env) != 2 || place.Env[0] != (exerciseModel.EnvVar{Name: "EDA_RUN_ID", Value: "set-by-the-brain"}) || place.Env[1].Name != "EDA_TRUST_CA" {
		t.Fatalf("the env as the brain baked it, in a stable order: %+v", place.Env)
	}
	if place.Pull == nil || place.Pull.Password != pullSecret || place.Pull.Registry != "registry.brain.test" || place.Pull.Username != "pull-bot" {
		t.Fatalf("the pull credential goes to the lab's deploy: %+v", place.Pull)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", place.Pull, place.Pull, place.Pull), pullSecret) {
		t.Fatal("a printed pull credential must not show its password")
	}
	if stored := r.runs.state(run.ID); stored.SpecID != "spec-7" {
		t.Fatalf("the run keeps the spec it was deployed from: %+v", stored)
	}

	r.drive(t)
	r.waitState(t, run.ID, assistantModel.RunRunning)
	eventually(t, "StartRun", func() bool { return len(r.brn.StartedRequests()) == 1 })
	start := r.brn.StartedRequests()[0]
	if start.GetSpecId() != "spec-7" || start.GetFoothold() != "dmz" || start.GetAccess().GetVpn().GetEntry() == "" || start.GetAccess().GetVpn().GetAddr() != "10.8.4.10:9099" ||
		start.GetBrief() != "think like a thief" || len(start.GetGoal()) != 2 {
		t.Fatalf("StartRun with the spec and the fixed inputs: %v", start)
	}
	id := run.ID.String()
	r.brn.Emit(id, "run_finished", `{"type":"run_finished","status":"done"}`)
	r.waitState(t, run.ID, assistantModel.RunFinished)

	// nothing the platform keeps or logs carries the credential
	r.runs.mu.Lock()
	rows := fmt.Sprintf("%+v %+v %+v", r.runs.runs, r.runs.events, r.runs.answers)
	r.runs.mu.Unlock()
	if strings.Contains(rows, pullSecret) || strings.Contains(rows, "pull-bot") {
		t.Fatal("the pull credential is stored")
	}
	if strings.Contains(logs.String(), pullSecret) {
		t.Fatal("the pull credential is logged")
	}
}

// A brain without opponent specs answers Unimplemented: the platform deploys its fallback tools image (BRAIN_TOOLS_IMAGE).
func TestWithoutGetOpponentTheFallbackToolsImageIsDeployed(t *testing.T) {
	r := opponentRig(t, attacker())
	run := r.tryOpponent(t, 1)
	place := r.labs.places[0]
	if place.Image != "" || place.Pull != nil || place.CPUMillis != 0 || len(place.Env) != 2 || place.Env[0].Value != run.ID.String() {
		t.Fatalf("the platform's tools image and EDA env: %+v", place)
	}
	if r.runs.state(run.ID).SpecID != "" {
		t.Fatal("no spec")
	}
	if len(r.fake.Opponents.Requests()) != 1 {
		t.Fatal("the brain was asked first")
	}
}

func TestAnOpponentSpecThePlatformCannotDeployIsRefusedBeforeAnyLab(t *testing.T) {
	for name, c := range map[string]struct {
		opponent exerciseModel.Opponent
		answer   func() (*brainapiv1.OpponentSpec, error)
		want     error
	}{
		"no image": {attacker(), func() (*brainapiv1.OpponentSpec, error) {
			s := spec(time.Now().Add(time.Hour))
			s.Deploy.Image = ""
			return s, nil
		}, assistantModel.ErrAssistantOpponentSpecInvalid.Err()},
		"a pull credential that stays expired": {attacker(), func() (*brainapiv1.OpponentSpec, error) {
			return spec(time.Now().Add(-time.Minute)), nil
		}, assistantModel.ErrAssistantOpponentSpecInvalid.Err()},
		"the brain is unavailable": {attacker(), func() (*brainapiv1.OpponentSpec, error) {
			return nil, status.Error(codes.Unavailable, "down")
		}, assistantModel.ErrAssistantBrainUnreachable.Err()},
		"the brain refuses": {attacker(), func() (*brainapiv1.OpponentSpec, error) {
			return nil, status.Error(codes.FailedPrecondition, "no opponent for this band")
		}, assistantModel.ErrAssistantOpponentUnavailable.Err()},
	} {
		t.Run(name, func(t *testing.T) {
			r := opponentRig(t, c.opponent)
			r.fake.Opponents.Answer(func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) { return c.answer() })
			_, err := r.uc.StartOpponentRun(context.Background(), runActor, runExercise, runVersion, runVariant, 1)
			if !errors.Is(err, c.want) {
				t.Fatalf("%v", err)
			}
			if r.labs.deployCount != 0 {
				t.Fatal("nothing is deployed")
			}
		})
	}
}

// A pull credential that expired on the way is fetched once more.
func TestAnExpiredPullCredentialIsFetchedAgain(t *testing.T) {
	r := opponentRig(t, attacker())
	calls := 0
	r.fake.Opponents.Answer(func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
		calls++
		if calls == 1 {
			return spec(time.Now().Add(-time.Second)), nil
		}
		return spec(time.Now().Add(time.Hour)), nil
	})
	r.tryOpponent(t, 1)
	if calls != 2 || r.labs.places[0].Pull == nil {
		t.Fatalf("%d calls", calls)
	}
}

// The size is a hint: the lab's limits decide, and an opponent that cannot fit at a workable size is refused with a clear error.
func TestAnOpponentThatDoesNotFitTheLabIsRefused(t *testing.T) {
	r := opponentRig(t, attacker())
	r.fake.Opponents.Answer(func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
		return spec(time.Now().Add(time.Hour)), nil
	})
	r.labs.toolsErr = exerciseModel.ErrToolsTooLarge
	if _, err := r.uc.StartOpponentRun(context.Background(), runActor, runExercise, runVersion, runVariant, 1); !errors.Is(err, assistantModel.ErrAssistantOpponentTooLarge.Err()) {
		t.Fatalf("%v", err)
	}
	if n := len(r.runs.runs); n != 0 {
		t.Fatalf("no run is kept: %d", n)
	}
}

// A reusable pull credential (no expires_at, as the brain sends today) is deployed as it is, without asking the brain again.
func TestAReusablePullCredentialWithoutExpiryIsDeployed(t *testing.T) {
	r := opponentRig(t, attacker())
	r.fake.Opponents.Answer(func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
		s := spec(time.Now())
		s.Deploy.Pull.ExpiresAt = nil
		return s, nil
	})
	r.tryOpponent(t, 1)
	if len(r.fake.Opponents.Requests()) != 1 {
		t.Fatalf("asked %d times", len(r.fake.Opponents.Requests()))
	}
	if place := r.labs.places[0]; place.Pull == nil || place.Pull.Password != pullSecret {
		t.Fatalf("the credential goes to the lab: %+v", place.Pull)
	}
}

// The inputs StartRun carries are fixed: an attacker or attack-defender whose manifest names no foothold is refused before the
// brain is asked (and so before the fallback tools image could be deployed).
func TestAnOpponentWithoutAFootholdIsRefusedBeforeTheBrainIsAsked(t *testing.T) {
	for _, role := range []exerciseModel.OpponentRole{exerciseModel.OpponentAttack, exerciseModel.OpponentAttackDefense} {
		t.Run(string(role), func(t *testing.T) {
			o := attacker()
			o.Role, o.Foothold = role, ""
			r := opponentRig(t, o)
			_, err := r.uc.StartOpponentRun(context.Background(), runActor, runExercise, runVersion, runVariant, 1)
			if !errors.Is(err, assistantModel.ErrAssistantOpponentSpecInvalid.Err()) {
				t.Fatalf("%v", err)
			}
			if n := len(r.fake.Opponents.Requests()); n != 0 {
				t.Fatalf("the brain was asked %d times", n)
			}
			if r.labs.deployCount != 0 {
				t.Fatal("nothing is deployed")
			}
		})
	}
}
