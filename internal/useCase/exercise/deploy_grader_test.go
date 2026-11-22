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

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	labAccessModel "github.com/cybericebox/backend/internal/model/labAccess"
	labCheckModel "github.com/cybericebox/backend/internal/model/labCheck"
	"github.com/cybericebox/backend/internal/useCase/exercise"
)

// agentConfig is the client config the laboratory agent returns: the peer's AllowedIPs is what the agent chose.
func agentConfig(allowed string) string {
	return "[Interface]\nPrivateKey = real\nAddress = 10.8.3.9\n\n[Peer]\nPublicKey = server\nEndpoint = vpn.example:51820\nAllowedIPs = " + allowed + "\nPersistentKeepalive = 15\n"
}

func graderRow(owner, deployID uuid.UUID) postgres.ExerciseTestDeployment {
	return postgres.ExerciseTestDeployment{ID: deployID, CreatedBy: owner, GroupName: "tu-group", LabName: "l-lab", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestMintLabClient_OwnClientReachesOnlyItsLabAndKeepsTheAuthorsPolicy(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: agentConfig("10.8.0.0/10"), Gateways: gws("vpn", "10.8.3.0/24")}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	uc.SetLabClientSource(func(_ context.Context, o uuid.UUID) []exercise.LabClient {
		if o != owner {
			t.Errorf("the source is asked for the owner of the lab: %v", o)
		}
		return []exercise.LabClient{{Name: "g-run", DeployID: deployID}, {Name: "g-other", DeployID: other}}
	})

	config, err := uc.MintLabClient(context.Background(), owner, deployID, "g-run")
	if err != nil || config != agentConfig("10.8.0.0/10") {
		t.Fatalf("the profile is the laboratory's own: %q %v", config, err)
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

type fakeLabChecks struct {
	results []labCheckModel.Result
}

func (f *fakeLabChecks) List(context.Context, string, string) ([]labCheckModel.Result, error) {
	return f.results, nil
}
func (f *fakeLabChecks) AskRun(context.Context, string, string, time.Duration, int, time.Time) (bool, time.Duration, error) {
	return false, 0, nil
}

func TestRunStateTasks_ReadsTheRecordedLabChecks(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, versionID, variantID, device := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	variants, _ := json.Marshal([]exerciseModel.Variant{{ID: variantID, Tasks: []exerciseModel.Task{
		{ID: uuid.Must(uuid.NewV7()), Key: "flag", Check: exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeFlag}}},
		{ID: uuid.Must(uuid.NewV7()), Key: "service", Check: exerciseModel.Check{State: &exerciseModel.StateCheck{Conditions: []exerciseModel.Condition{{DeviceID: device}}}}},
		{ID: uuid.Must(uuid.NewV7()), Key: "fw", Check: exerciseModel.Check{State: &exerciseModel.StateCheck{Conditions: []exerciseModel.Condition{{DeviceID: device}}}}},
	}}})
	row := graderRow(owner, deployID)
	row.VersionID, row.VariantID = versionID, variantID
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil).AnyTimes()
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, Variants: variants}, nil).AnyTimes()
	checks := &fakeLabChecks{}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: &fakeInfra{}})
	uc.SetLabChecks(checks)

	got, err := uc.RunStateTasks(context.Background(), owner, deployID)
	if err != nil || len(got) != 2 || got[0].Key != "service" || got[1].Key != "fw" || got[0].Passed || got[1].Passed {
		t.Fatalf("the state subtasks in the variant's order, none passed yet: %+v %v", got, err)
	}
	first := time.Now().Add(-time.Minute)
	checks.results = []labCheckModel.Result{{CheckID: exerciseModel.CheckID("service", 1), State: labCheckModel.StatePassed, FirstPassedAt: &first, ChangedAt: first}}
	if got, err = uc.RunStateTasks(context.Background(), owner, deployID); err != nil || !got[0].Passed || got[1].Passed {
		t.Fatalf("%+v %v", got, err)
	}
	// a check that passed once and fails now has still passed (the first pass is what is reported)
	checks.results[0].State = labCheckModel.StateNotPassed
	if got, err = uc.RunStateTasks(context.Background(), owner, deployID); err != nil || !got[0].Passed {
		t.Fatalf("%+v %v", got, err)
	}
}

// The checker the run's answers go through is the one of the author's own flag check: a text answer is compared with the
// task's own ignore_case and trim, a flag exactly.
func TestJudgeAnswer_TextAnswersFollowTheTasksCompareOptions(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, versionID, variantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	loose, strict, plain := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	noTrim := false
	tasks := []exerciseModel.Task{
		{ID: loose, Key: "loose", Check: exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeText, IgnoreCase: true}}},
		{ID: strict, Key: "strict", Check: exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeText, Trim: &noTrim}}},
		{ID: plain, Key: "plain", Check: exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeFlag}}},
	}
	flags := []exerciseModel.DeployFlag{}
	for _, task := range tasks {
		flags = append(flags, exerciseModel.NewDeployFlag(task, "Open Sesame"))
	}
	flags[2].Flag = "ICE{abc}"
	stored, _ := json.Marshal(flags)
	variants, _ := json.Marshal([]exerciseModel.Variant{{ID: variantID, Tasks: tasks}})
	row := graderRow(owner, deployID)
	row.VersionID, row.VariantID, row.Flags = versionID, variantID, stored
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil).AnyTimes()
	q.EXPECT().MarkExerciseTestDeploySolved(gomock.Any(), gomock.Any()).Return(postgres.ExerciseTestDeployment{}, nil).AnyTimes()
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, Variants: variants}, nil).AnyTimes()
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q})
	for _, c := range []struct {
		ref, value string
		want       bool
	}{
		{"loose", "open sesame", true}, {"loose", "  OPEN SESAME  ", true}, {"loose", "open  sesame", false},
		{"strict", "Open Sesame", true}, {"strict", "open sesame", false}, {"strict", " Open Sesame", false},
		{"plain", "ICE{abc}", true}, {"plain", "ice{abc}", false},
	} {
		got, err := uc.JudgeAnswer(context.Background(), owner, deployID, c.ref, c.value)
		if err != nil || got != c.want {
			t.Errorf("%s %q = %v %v, want %v", c.ref, c.value, got, err, c.want)
		}
	}
}

// The laboratory writes the AllowedIPs of the client itself (contract 2.4: exactly the set its rules let it reach): the daemon
// hands the profile out as it came, and does not rewrite it.
func TestMintLabClient_TheProfileIsHandedOutAsTheLaboratoryWroteIt(t *testing.T) {
	for name, agentWrote := range map[string]string{"one gateway": "10.8.3.0/24", "several gateways": "10.8.3.0/24, 10.8.5.0/24"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			q := postgresMocks.NewMockQuerier(ctrl)
			owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
			q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: agentConfig(agentWrote), Gateways: gws("vpn", "10.8.3.0/24", "vpn2", "10.8.5.0/24")}}
			uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
			config, err := uc.MintLabClient(context.Background(), owner, deployID, "g-run")
			if err != nil || config != agentConfig(agentWrote) {
				t.Fatalf("the profile as the laboratory wrote it: %q %v", config, err)
			}
		})
	}
}

func TestRunTarget_ALinkedVariantTakesTheOpponentsOfItsBase(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	versionID, exerciseID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	base, linked, own := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	device := []exerciseModel.Device{{Name: "web", Image: "nginx:1"}}
	opponent := exerciseModel.Opponent{Role: exerciseModel.OpponentAttack, Band: 3, Goal: []string{"root"}}
	variants, _ := json.Marshal([]exerciseModel.Variant{
		{ID: base, Topology: exerciseModel.Topology{Devices: device}, Opponents: []exerciseModel.Opponent{opponent}},
		{ID: linked, LinkedTo: uuid.NullUUID{UUID: base, Valid: true}, Topology: exerciseModel.Topology{Devices: device}},
		{ID: own, Topology: exerciseModel.Topology{Devices: device}},
	})
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, ExerciseID: exerciseID, Variants: variants}, nil).AnyTimes()
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q})
	for id, want := range map[uuid.UUID]int{base: 1, linked: 1, own: 0} {
		got, err := uc.RunTarget(context.Background(), versionID, id, 0)
		if err != nil || len(got.Opponents) != want || want == 1 && (got.Opponents[0].Band != 3 || got.Opponents[0].Goal[0] != "root") {
			t.Errorf("%s: %+v %v", id, got.Opponents, err)
		}
	}
}

func TestRunSolvedSubtasks_StatePassedAndAnswersFoundInTheLab(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, versionID, variantID, device := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	flagID, otherID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	variants, _ := json.Marshal([]exerciseModel.Variant{{ID: variantID, Tasks: []exerciseModel.Task{
		{ID: flagID, Key: "flag", Check: exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeFlag}}},
		{ID: otherID, Key: "other", Check: exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeFlag}}},
		{ID: uuid.Must(uuid.NewV7()), Key: "service", Check: exerciseModel.Check{State: &exerciseModel.StateCheck{Conditions: []exerciseModel.Condition{{DeviceID: device}}}}},
	}}})
	solved, _ := json.Marshal([]uuid.UUID{flagID})
	row := graderRow(owner, deployID)
	row.VersionID, row.VariantID, row.Solved = versionID, variantID, solved
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil).AnyTimes()
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, Variants: variants}, nil).AnyTimes()
	checks := &fakeLabChecks{}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: &fakeInfra{}})
	uc.SetLabChecks(checks)

	got, err := uc.RunSolvedSubtasks(context.Background(), owner, deployID)
	if err != nil || len(got) != 1 || !got["flag"] {
		t.Fatalf("only the answer the author found: %v %v", got, err)
	}
	first := time.Now().Add(-time.Minute)
	checks.results = []labCheckModel.Result{{CheckID: exerciseModel.CheckID("service", 1), State: labCheckModel.StatePassed, FirstPassedAt: &first, ChangedAt: first}}
	if got, err = uc.RunSolvedSubtasks(context.Background(), owner, deployID); err != nil || len(got) != 2 || !got["flag"] || !got["service"] || got["other"] {
		t.Fatalf("%v %v", got, err)
	}
}

func TestMintLabClient_TheClientOfABotReachesTheControlGatewayAndOneAddressOfItOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil)
	q.EXPECT().ListOwnedExerciseTestDeploys(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{VPNConfig: agentConfig("10.8.0.0/10"), Gateways: gws("vpn", "10.8.3.0/24", "ctl", "10.8.4.0/24")}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	uc.SetLabClientSource(func(context.Context, uuid.UUID) []exercise.LabClient {
		return []exercise.LabClient{{Name: "g-bot", DeployID: deployID, Control: true}}
	})
	config, err := uc.MintLabClient(context.Background(), owner, deployID, "g-bot")
	if err != nil || config != agentConfig("10.8.0.0/10") {
		t.Fatalf("the profile of a bot is the laboratory's own: %q %v", config, err)
	}
	var bot *labAccessModel.ClientPolicy
	for i, p := range infra.policies {
		if p.Name == "g-bot" {
			bot = &infra.policies[i]
		}
	}
	if bot == nil || len(bot.AllowedLabs) != 0 || bot.Control == nil || bot.Control.Host != 10 || bot.Control.Lab[:2] != "l-" {
		t.Fatalf("a bot's client has the control address of its lab and no lab of its own: %+v", infra.policies)
	}
}

func TestControlAddress_IsTheDevicesFixedHostOfTheGatewaysNetwork(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(graderRow(owner, deployID), nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{Gateways: gws("ctl", "10.8.4.0/24")}}
	uc := exercise.NewExerciseUseCase(exercise.Dependencies{Repo: q, Infra: infra})
	if got, err := uc.ControlAddress(context.Background(), owner, deployID); err != nil || got != "10.8.4.10" {
		t.Fatalf("%q %v", got, err)
	}
	for name, cidr := range map[string]string{"none yet": "", "not a /24": "10.8.4.0/23", "not ipv4": "fd00::/64", "garbage": "x"} {
		infra.status.Gateways = gws("ctl", cidr)
		if _, err := uc.ControlAddress(context.Background(), owner, deployID); !errors.Is(err, exerciseModel.ErrToolsNoControl) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// gws makes the gateways of a lab status from name, cidr pairs; the kind of each is vpn.
func gws(pairs ...string) []exerciseModel.LabDeployedGateway {
	var out []exerciseModel.LabDeployedGateway
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, exerciseModel.LabDeployedGateway{Name: pairs[i], Kind: "vpn", CIDR: pairs[i+1], Ready: true})
	}
	return out
}
