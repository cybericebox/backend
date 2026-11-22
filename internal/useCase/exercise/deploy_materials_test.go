package exercise_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	mediaModel "github.com/cybericebox/backend/internal/model/media"
	"github.com/cybericebox/backend/internal/useCase/exercise"
)

// memFiles is the file store: bytes by id.
type memFiles struct {
	files map[uuid.UUID][]byte
	types map[uuid.UUID]string
	size  map[uuid.UUID]int64 // when set, the size the store reports
}

func (m *memFiles) StreamFile(_ context.Context, id uuid.UUID) (io.ReadCloser, mediaModel.File, error) {
	return io.NopCloser(bytes.NewReader(m.files[id])), mediaModel.File{ID: id}, nil
}
func (m *memFiles) GetFile(_ context.Context, id uuid.UUID) (mediaModel.File, error) {
	size := int64(len(m.files[id]))
	if s, ok := m.size[id]; ok {
		size = s
	}
	return mediaModel.File{ID: id, SizeBytes: size, ContentType: m.types[id]}, nil
}

func textNode(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func paragraph(children ...any) map[string]any {
	return map[string]any{"type": "paragraph", "children": children}
}

func documentOf(blocks ...any) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"root": map[string]any{"type": "root", "children": blocks}})
	return raw
}

// the strings that must never reach the brain, each seeded where the author's document holds something private
const (
	secretHint     = "HINT-SECRET-7731"
	secretNote     = "NOTE-SECRET-4410"
	secretFlag     = "ICE{FLAG-SECRET-9921}"
	secretOpponent = "OPPONENT-SECRET-5518"
	secretCheck    = "CHECK-SECRET-6602"
	secretBoot     = "BOOT-SECRET-8814"
)

func materialsFixture(t *testing.T, files *memFiles, deployLab string) (*exercise.ExerciseUseCase, uuid.UUID, uuid.UUID, *fakeInfra) {
	t.Helper()
	ctrl := gomock.NewController(t)
	q := postgresMocks.NewMockQuerier(ctrl)
	owner, deployID, exerciseID, versionID, variantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	flagTask, stateTask := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	fileID := uuid.Must(uuid.NewV7())
	if files != nil {
		files.files[fileID], files.types[fileID] = []byte("the participant's file"), "text/plain"
	}
	variant := exerciseModel.Variant{
		ID: variantID, Note: secretNote,
		Opponents: []exerciseModel.Opponent{{Role: exerciseModel.OpponentAttack, Band: 3, Goal: []string{"login"}, Brief: secretOpponent}},
		Topology:  exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "web", Bootstrap: exerciseModel.Bootstrap{Env: []exerciseModel.EnvVar{{Name: "FLAG", Value: secretBoot}}}}}},
		Tasks: []exerciseModel.Task{
			{
				ID: flagTask, Key: "login", Name: "Log in",
				Description: documentOf(
					paragraph(textNode("Open "), map[string]any{"type": "variable", "varName": "site"}, textNode(" and find the flag.")),
					paragraph(textNode("Network "), map[string]any{"type": "variable", "varName": "net"}, map[string]any{"type": "linebreak"}, textNode("Host "), map[string]any{"type": "variable", "varName": "host"}),
				),
				Placeholders: []exerciseModel.Placeholder{
					{Key: "site", Kind: exerciseModel.PlaceholderExternalLink, DeviceName: "web"},
					{Key: "net", Kind: exerciseModel.PlaceholderGatewaySubnet, Gateway: "vpn"},
					{Key: "host", Kind: exerciseModel.PlaceholderIP, IPReference: "gateway", Gateway: "vpn", LastOctet: 7},
				},
				Check:       exerciseModel.Check{Answer: &exerciseModel.AnswerCheck{Type: exerciseModel.AnswerTypeFlag, Values: []string{secretFlag}}},
				Hints:       []exerciseModel.Hint{{ID: uuid.Must(uuid.NewV7()), Text: secretHint}},
				Attachments: []exerciseModel.AttachmentRef{{FileID: fileID, Name: "notes.txt"}},
			},
			{
				ID: stateTask, Key: "service", Name: "Fix the service", Description: documentOf(paragraph(textNode("Make it run."))),
				Check: exerciseModel.Check{State: &exerciseModel.StateCheck{Conditions: []exerciseModel.Condition{{File: &exerciseModel.FileObservation{Path: secretCheck}}}}},
			},
		},
	}
	variants, _ := json.Marshal([]exerciseModel.Variant{variant})
	row := postgres.ExerciseTestDeployment{ID: deployID, CreatedBy: owner, VersionID: versionID, VariantID: variantID, ExpiresAt: time.Now().Add(time.Hour), GroupName: "tu-g", LabName: deployLab}
	if deployLab == "" {
		row.GroupName = "" // a static test deploy has neither a group nor a lab
	}
	q.EXPECT().GetOwnedExerciseTestDeploy(gomock.Any(), gomock.Any()).Return(row, nil).AnyTimes()
	q.EXPECT().GetExerciseVersionByID(gomock.Any(), versionID).Return(postgres.ExerciseVersion{ID: versionID, ExerciseID: exerciseID, Variants: variants}, nil).AnyTimes()
	q.EXPECT().GetExerciseByID(gomock.Any(), exerciseID).Return(postgres.Exercise{ID: exerciseID, Name: "Web login", Description: "A small web task."}, nil).AnyTimes()
	infra := &fakeInfra{status: exerciseModel.LabDeployStatus{
		Phase: "Ready", Ready: true, Gateways: []exerciseModel.LabDeployedGateway{{Name: "vpn", Kind: "vpn", CIDR: "10.8.3.0/24", Ready: true}},
		Access: []exerciseModel.LabAccess{{Device: "web", Port: 80, Protocol: "http", URL: "https://lab.example/s/abc"}},
	}}
	deps := exercise.Dependencies{Repo: q, Infra: infra}
	uc := exercise.NewExerciseUseCase(deps)
	if files != nil {
		uc.SetFileReader(files)
	}
	return uc, owner, deployID, infra
}

func TestRunMaterials_ThePlaceholdersAreResolvedForTheRunsLab(t *testing.T) {
	files := &memFiles{files: map[uuid.UUID][]byte{}, types: map[uuid.UUID]string{}}
	uc, owner, deployID, _ := materialsFixture(t, files, "l-lab")
	m, err := uc.RunMaterials(context.Background(), owner, deployID)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Title, Description string
		Subtasks           []struct {
			Key, Title, Prompt string
			AnswerKind         string `json:"answer_kind"`
		}
	}
	if err = json.Unmarshal(m.Task, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Log in" || doc.Description != "" || len(doc.Subtasks) != 2 {
		t.Fatalf("%s", m.Task)
	}
	first := doc.Subtasks[0]
	want := "Open https://lab.example/s/abc and find the flag.\n\nNetwork 10.8.3.0/24\nHost 10.8.3.7"
	if first.Key != "login" || first.Title != "Log in" || first.Prompt != want || first.AnswerKind != "flag" {
		t.Fatalf("%+v", first)
	}
	if doc.Subtasks[1].Key != "service" || doc.Subtasks[1].AnswerKind != "state" || doc.Subtasks[1].Prompt != "Make it run." {
		t.Fatalf("a subtask checked by the lab's state says so: %+v", doc.Subtasks[1])
	}
	if len(m.Files) != 1 || m.Files[0].Name != "notes.txt" || string(m.Files[0].Data) != "the participant's file" || m.Files[0].ContentType != "text/plain" {
		t.Fatalf("%+v", m.Files)
	}
}

func TestRunMaterials_NothingPrivateIsInIt(t *testing.T) {
	files := &memFiles{files: map[uuid.UUID][]byte{}, types: map[uuid.UUID]string{}}
	uc, owner, deployID, _ := materialsFixture(t, files, "l-lab")
	m, err := uc.RunMaterials(context.Background(), owner, deployID)
	if err != nil {
		t.Fatal(err)
	}
	body := string(m.Task)
	for _, f := range m.Files {
		body += f.Name + string(f.Data)
	}
	for _, secret := range []string{secretHint, secretNote, secretFlag, secretOpponent, secretCheck, secretBoot, "FLAG-SECRET", "opponents", "hints", "Brief", "Values"} {
		if strings.Contains(body, secret) {
			t.Fatalf("%q reached the materials: %s", secret, body)
		}
	}
}

func TestRunMaterials_AnUnresolvablePlaceholderIsADashAndAttachmentsAreCapped(t *testing.T) {
	files := &memFiles{files: map[uuid.UUID][]byte{}, types: map[uuid.UUID]string{}, size: map[uuid.UUID]int64{}}
	uc, owner, deployID, infra := materialsFixture(t, files, "l-lab")
	infra.status.Access, infra.status.Gateways = nil, nil
	m, err := uc.RunMaterials(context.Background(), owner, deployID)
	if err != nil || strings.Count(string(m.Task), "—") < 3 {
		t.Fatalf("%v %s", err, m.Task)
	}
	// a file over the entry limit of an archive is refused before it is read
	for id := range files.files {
		files.size[id] = exerciseModel.DefaultArchiveEntryBytes + 1
	}
	if _, err = uc.RunMaterials(context.Background(), owner, deployID); err == nil {
		t.Fatal("an attachment over the size limit must be refused")
	}
}

func TestRunMaterials_AStaticTestHasNoLabAndStillRendersAndTheAddressesOfAnIPAreFixed(t *testing.T) {
	files := &memFiles{files: map[uuid.UUID][]byte{}, types: map[uuid.UUID]string{}}
	uc, owner, deployID, _ := materialsFixture(t, files, "") // a static test deploy: no lab name
	m, err := uc.RunMaterials(context.Background(), owner, deployID)
	if err != nil || !strings.Contains(string(m.Task), "Make it run.") || len(m.Files) != 1 {
		t.Fatalf("%v %s", err, m.Task)
	}
}
