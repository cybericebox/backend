package exercise_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"go.uber.org/mock/gomock"

	postgresMocks "github.com/cybericebox/daemon/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	"github.com/cybericebox/daemon/internal/useCase/exercise"
	"github.com/cybericebox/daemon/pkg/labaccess"
)

// bearerSessions signs nothing real: it records what it was asked and returns a recognisable token.
type bearerSessions struct {
	got labaccess.Bearer
	now time.Time
	err error
}

func (b *bearerSessions) Issue(context.Context, labaccess.Session, time.Time) (labaccess.Link, error) {
	return labaccess.Link{}, errors.New("not used")
}
func (b *bearerSessions) IssueBearer(_ context.Context, in labaccess.Bearer, now time.Time) (labaccess.BearerToken, error) {
	b.got, b.now = in, now
	return labaccess.BearerToken{URL: in.AccessURL, Token: "signed.jwt.token", ExpiresAt: in.ExpiresAt}, b.err
}

// plainSessions can only issue handoff links.
type plainSessions struct{}

func (plainSessions) Issue(context.Context, labaccess.Session, time.Time) (labaccess.Link, error) {
	return labaccess.Link{}, nil
}

func edaFixture(t *testing.T, sessions exercise.ITestSessions, access []exerciseModel.LabAccess) (*exercise.ExerciseUseCase, uuid.UUID, uuid.UUID) {
	t.Helper()
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{Phase: "Ready", Ready: true, Access: access}}
	return exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra, Sessions: sessions}), owner, deployID
}

func TestMintEDAAccess_TheTokenNamesTheGroupTheHostTheClientAndTheRun(t *testing.T) {
	signer := &bearerSessions{}
	uc, owner, deployID := edaFixture(t, signer, []exerciseModel.LabAccess{
		{Device: "tools", Port: 9099, Protocol: "grpc", URL: "https://tools-abc123g.lab.example.com:443"},
		{Device: "web", Port: 80, Protocol: "http", URL: "https://web-abc.lab.example.com"},
	})
	until := time.Now().Add(40 * time.Minute)
	got, err := uc.MintEDAAccess(context.Background(), owner, deployID, "tools", "g-run1", "run-1", until)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://tools-abc123g.lab.example.com:443" || got.Token != "signed.jwt.token" || !got.ExpiresAt.Equal(until) {
		t.Fatalf("%+v", got)
	}
	if signer.got.Group != "tu-group" || signer.got.Client != "g-run1" || signer.got.Run != "run-1" || signer.got.AccessURL != got.URL || !signer.got.ExpiresAt.Equal(until) {
		t.Fatalf("the token is for the lab's group, the per-run client and the run, up to the end of the window: %+v", signer.got)
	}
}

func TestMintEDAAccess_Refusals(t *testing.T) {
	until := time.Now().Add(time.Hour)
	t.Run("the device has no grpc entry", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &bearerSessions{}, []exerciseModel.LabAccess{{Device: "tools", Port: 22, Protocol: "terminal", URL: "https://x"}})
		if _, err := uc.MintEDAAccess(context.Background(), owner, deployID, "tools", "c", "r", until); !errors.Is(err, exerciseModel.ErrTestDeployNoGRPC.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the entry has no address yet", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &bearerSessions{}, []exerciseModel.LabAccess{{Device: "tools", Protocol: "grpc"}})
		if _, err := uc.MintEDAAccess(context.Background(), owner, deployID, "tools", "c", "r", until); !errors.Is(err, exerciseModel.ErrTestDeployNoGRPC.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the signer cannot sign bearer tokens", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, plainSessions{}, []exerciseModel.LabAccess{{Device: "tools", Protocol: "grpc", URL: "https://t.example"}})
		if _, err := uc.MintEDAAccess(context.Background(), owner, deployID, "tools", "c", "r", until); err == nil {
			t.Fatal("a signer without bearer support must refuse")
		}
	})
	t.Run("signing fails", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &bearerSessions{err: errors.New("no key")}, []exerciseModel.LabAccess{{Device: "tools", Protocol: "grpc", URL: "https://t.example"}})
		got, err := uc.MintEDAAccess(context.Background(), owner, deployID, "tools", "c", "r", until)
		if err == nil || got.Token != "" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}
