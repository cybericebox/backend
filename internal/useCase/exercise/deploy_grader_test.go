package exercise_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/daemon/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	"github.com/cybericebox/daemon/internal/useCase/exercise"
)

func graderRow(owner, deployID uuid.UUID) postgres.ExerciseTestDeployment {
	return postgres.ExerciseTestDeployment{ID: deployID, CreatedBy: owner, GroupName: "tu-group", LabName: "l-lab", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestMintLabClient_OwnClientReachesOnlyItsLabAndKeepsTheAuthorsPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: "[Interface]\nPrivateKey = real\n"}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	uc.SetLabClientSource(func(_ context.Context, o uuid.UUID) []exercise.LabClient {
		if o != owner {
			t.Errorf("the source is asked for the owner of the lab: %v", o)
		}
		return []exercise.LabClient{{Name: "g-run", DeployID: deployID}, {Name: "g-other", DeployID: other}}
	})

	config, err := uc.MintLabClient(context.Background(), owner, deployID, "g-run")
	if err != nil || config != "[Interface]\nPrivateKey = real\n" {
		t.Fatalf("%q %v", config, err)
	}
	if infra.client != "g-run" || infra.ensured != 1 {
		t.Fatalf("the run's own client is made once, never the author's: %q %d", infra.client, infra.ensured)
	}
	if infra.policyGroup != "tu-group" || len(infra.policies) != 3 {
		t.Fatalf("policy of the group: %+v", infra.policies)
	}
	byName := map[string][]string{}
	for _, p := range infra.policies {
		byName[p.Name] = p.AllowedLabs
	}
	if len(byName["g-run"]) != 1 || byName["g-run"][0][:2] != "l-" || len(byName["g-other"]) != 1 {
		t.Fatalf("each run client reaches its own lab only: %+v", byName)
	}
	if _, ok := byName["g-run"]; !ok || byName["g-run"][0] == byName["g-other"][0] {
		t.Fatalf("two runs must not share a lab: %+v", byName)
	}
}

func TestMintLabClient_AConfigWithoutTheKeyIsRefused(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: "PrivateKey = __PRIVATE_KEY__"}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	if config, err := uc.MintLabClient(context.Background(), owner, deployID, "g-run"); err == nil || config != "" {
		t.Fatalf("a placeholder key is no config: %q %v", config, err)
	}
}

func TestRevokeLabClient_RemovesTheClientAndTakesItOutOfThePolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	infra := &fakeInfra{}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	uc.SetLabClientSource(func(context.Context, uuid.UUID) []exercise.LabClient {
		return []exercise.LabClient{{Name: "g-run", DeployID: deployID}}
	})
	if err := uc.RevokeLabClient(context.Background(), owner, deployID, "g-run"); err != nil {
		t.Fatal(err)
	}
	if len(infra.deletedClients) != 1 || infra.deletedClients[0] != "tu-group/g-run" {
		t.Fatalf("the key is revoked: %v", infra.deletedClients)
	}
	for _, p := range infra.policies {
		if p.Name == "g-run" {
			t.Fatalf("a revoked client must not stay in the policy: %+v", infra.policies)
		}
	}
}

func TestRevokeLabClient_ALabThatIsGoneNeedsNothing(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(postgres.ExerciseTestDeployment{}, pgxNoRows())
	infra := &fakeInfra{}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	if err := uc.RevokeLabClient(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "g-run"); err != nil || len(infra.deletedClients) != 0 {
		t.Fatalf("%v %v", err, infra.deletedClients)
	}
}

func TestJudgeAnswer_ByKeyWithTheLabFlagCheckerAndNeverTheValue(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, versionID, variantID, taskID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	flags, _ := json.Marshal([]exerciseModel.DeployFlag{{TaskID: taskID, Name: "Login", Flag: "ICE{Abc}"}})
	row := graderRow(owner, deployID)
	row.VersionID, row.VariantID, row.Flags = versionID, variantID, flags
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil).AnyTimes()
	q.EXPECT().MarkExerciseTestDeploySolved(gomock.Any(), gomock.Any()).Return(postgres.ExerciseTestDeployment{}, nil).AnyTimes()
	variants, _ := json.Marshal([]exerciseModel.Variant{{ID: variantID, Tasks: []exerciseModel.Task{{ID: taskID, Key: "login", Name: "Login"}}}})
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, Variants: variants}, nil).AnyTimes()
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q})

	for _, c := range []struct {
		ref, value string
		want       bool
	}{
		{"login", "ICE{Abc}", true}, {taskID.String(), "ICE{Abc}", true}, {"login", "ICE{abc}", false}, {"login", "", false}, {"nope", "ICE{Abc}", false},
	} {
		got, err := uc.JudgeAnswer(context.Background(), owner, deployID, c.ref, c.value)
		if err != nil || got != c.want {
			t.Errorf("%q %q = %v %v, want %v", c.ref, c.value, got, err, c.want)
		}
	}
}

func TestTestLabState(t *testing.T) {
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for name, c := range map[string]struct {
		status exerciseModel.LabDeployStatus
		expiry time.Duration
		want   exercise.LabState
	}{
		"ready":    {exerciseModel.LabDeployStatus{Phase: "Ready", Ready: true}, time.Hour, exercise.LabState{Ready: true}},
		"starting": {exerciseModel.LabDeployStatus{Phase: "Provisioning"}, time.Hour, exercise.LabState{}},
		"failed":   {exerciseModel.LabDeployStatus{Phase: "Failed"}, time.Hour, exercise.LabState{Failed: true}},
		"removing": {exerciseModel.LabDeployStatus{Phase: "Removing"}, time.Hour, exercise.LabState{Gone: true}},
		"expired":  {exerciseModel.LabDeployStatus{Phase: "Ready", Ready: true}, -time.Minute, exercise.LabState{Gone: true}},
	} {
		ctrl := gomock.NewController(t)
		q := postgresMocks.NewMockQuerier(ctrl)
		row := graderRow(owner, deployID)
		row.ExpiresAt = time.Now().Add(c.expiry)
		q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil)
		uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: &fakeInfra{status: c.status}})
		got, err := uc.TestLabState(context.Background(), owner, deployID)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v %v, want %+v", name, got, err, c.want)
		}
	}
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(postgres.ExerciseTestDeployment{}, pgxNoRows())
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: &fakeInfra{}})
	if got, err := uc.TestLabState(context.Background(), owner, deployID); err != nil || !got.Gone {
		t.Errorf("a lab that is not there is gone: %+v %v", got, err)
	}
}

func pgxNoRows() error { return pgx.ErrNoRows }

func TestRunTarget_PicksTheVariantOrTheFirstOneWithDevices(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	versionID, exerciseID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	plain, withLab, second := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	variants, _ := json.Marshal([]exerciseModel.Variant{
		{ID: plain},
		{ID: withLab, Topology: exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web", Image: "nginx:1"}}}},
		{ID: second, Topology: exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web", Image: "nginx:2"}}}},
	})
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, ExerciseID: exerciseID, Variants: variants}, nil).AnyTimes()
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q})

	got, err := uc.RunTarget(context.Background(), versionID, uuid.Nil, 0)
	if err != nil || got.VariantID != withLab || !got.HasLab || got.ExerciseID != exerciseID || got.VersionID != versionID {
		t.Fatalf("a lab asked for by task and version gets the first variant with devices: %+v %v", got, err)
	}
	if !strings.Contains(string(got.VariantsJS), "nginx:2") {
		t.Fatalf("the fingerprint is of the whole version: %s", got.VariantsJS)
	}
	if got, err = uc.RunTarget(context.Background(), versionID, plain, 0); err != nil || got.VariantID != plain || got.HasLab {
		t.Fatalf("a variant without devices has no lab: %+v %v", got, err)
	}
	if got, err = uc.RunTarget(context.Background(), versionID, second, 0); err != nil || got.VariantID != second || !got.HasLab {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = uc.RunTarget(context.Background(), versionID, uuid.Must(uuid.NewV7()), 0); !errors.Is(err, exerciseModel.ErrExerciseVersionNotFound.Err()) {
		t.Fatalf("%v", err)
	}
}
