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

// handoffSessions signs nothing real: it records what it was asked and returns a recognisable handoff.
type handoffSessions struct {
	got labaccess.Session
	now time.Time
	err error
}

func (h *handoffSessions) Issue(_ context.Context, in labaccess.Session, now time.Time) (labaccess.Link, error) {
	h.got, h.now = in, now
	return labaccess.Link{Token: "signed.handoff", ExpiresAt: in.ExpiresAt, TokenExpiresAt: now.Add(time.Minute)}, h.err
}

func edaFixture(t *testing.T, sessions exercise.ITestSessions, access []exerciseModel.LabAccess) (*exercise.ExerciseUseCase, uuid.UUID, uuid.UUID) {
	t.Helper()
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{Phase: "Ready", Ready: true, Access: access}}
	return exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra, Sessions: sessions}), owner, deployID
}

var edaAccess = []exerciseModel.LabAccess{
	{Device: "web", Port: 80, Protocol: "http", URL: "https://web-abc.lab.example.com"},
	{Device: "tools", Port: 9099, Protocol: "grpc", URL: "https://tools-abc123g.lab.example.com:443"},
}

func TestEDAAddress_IsTheGRPCEntryOfTheDeviceWithNoCredential(t *testing.T) {
	uc, owner, deployID := edaFixture(t, &handoffSessions{}, edaAccess)
	got, err := uc.EDAAddress(context.Background(), owner, deployID, "")
	if err != nil || got != "https://tools-abc123g.lab.example.com:443" {
		t.Fatalf("an empty device is the lab's grpc device: %q %v", got, err)
	}
}

func TestTunnelHandoff_IsSignedForTheGroupTheAuthorsClientAndTheTunnel(t *testing.T) {
	signer := &handoffSessions{}
	uc, owner, deployID := edaFixture(t, signer, edaAccess)
	until := time.Now().Add(40 * time.Minute)
	got, err := uc.TunnelHandoff(context.Background(), owner, deployID, "", until)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://tools-abc123g.lab.example.com:443" || got.Token != "signed.handoff" || got.ExpiresAt.Sub(signer.now) != time.Minute {
		t.Fatalf("%+v", got)
	}
	if signer.got.Group != "tu-group" || signer.got.Use != labaccess.UseTunnel || signer.got.AccessURL != got.URL || !signer.got.ExpiresAt.Equal(until) || signer.got.Client == "" {
		t.Fatalf("the session of the tunnel: %+v", signer.got)
	}
}

func TestTunnelHandoff_Refusals(t *testing.T) {
	until := time.Now().Add(time.Hour)
	t.Run("the lab has no grpc device", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &handoffSessions{}, edaAccess[:1])
		if _, err := uc.TunnelHandoff(context.Background(), owner, deployID, "", until); !errors.Is(err, exerciseModel.ErrTestDeployNoGRPC.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the named device has no grpc entry", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &handoffSessions{}, edaAccess)
		if _, err := uc.TunnelHandoff(context.Background(), owner, deployID, "web", until); !errors.Is(err, exerciseModel.ErrTestDeployNoGRPC.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the entry has no address yet", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &handoffSessions{}, []exerciseModel.LabAccess{{Device: "tools", Protocol: "grpc"}})
		if _, err := uc.TunnelHandoff(context.Background(), owner, deployID, "", until); !errors.Is(err, exerciseModel.ErrTestDeployNoGRPC.Err()) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("signing fails", func(t *testing.T) {
		uc, owner, deployID := edaFixture(t, &handoffSessions{err: errors.New("no key")}, edaAccess)
		got, err := uc.TunnelHandoff(context.Background(), owner, deployID, "", until)
		if err == nil || got.Token != "" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}
