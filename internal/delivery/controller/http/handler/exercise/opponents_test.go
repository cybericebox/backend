package exercise_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

const secretBrief = "SECRET-OPPONENT-BRIEF"

func versionWithOpponents(exID, versionID uuid.UUID) exerciseUseCase.VersionView {
	return exerciseUseCase.VersionView{ID: versionID, ExerciseID: exID, Status: "published", Variants: []exerciseModel.Variant{{
		Tasks: []exerciseModel.Task{{Key: "root", Name: "Root"}},
		Opponents: []exerciseModel.Opponent{{
			Role: exerciseModel.OpponentAttackDefense, Band: 4, Goal: []string{"root"}, Start: exerciseModel.OpponentStart{After: "root"},
			Operates: []string{"web"}, Brief: secretBrief,
		}},
	}}}
}

func getVersionBody(t *testing.T, access exerciseUseCase.Access) string {
	t.Helper()
	uid, exID, versionID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	uc := &fakeUC{versionView: versionWithOpponents(exID, versionID), access: &access}
	w := httptest.NewRecorder()
	newEngine(uc, uid).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/exercises/"+exID.String()+"/versions/"+versionID.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestOpponentsReachTheAuthorsOfTheExerciseOnly(t *testing.T) {
	author := getVersionBody(t, exerciseUseCase.Access{Full: true, Flags: true})
	for _, want := range []string{`"Opponents"`, secretBrief, `"Role":"attack-defense"`, `"Band":4`, `"StartAfter":"root"`, `"Operates":["web"]`} {
		if !strings.Contains(author, want) {
			t.Errorf("the author's view lacks %s: %s", want, author)
		}
	}
	for name, access := range map[string]exerciseUseCase.Access{
		"event viewer (full access, no flags)": {Full: true},
		"published-only reader":                {},
	} {
		body := getVersionBody(t, access)
		for _, leak := range []string{`"Opponents"`, secretBrief, "attack-defense", `"Operates"`} {
			if strings.Contains(body, leak) {
				t.Errorf("%s sees %s: %s", name, leak, body)
			}
		}
	}
}

func TestAnEditorSaveKeepsTheOpponentsAndAMissingListRemovesThem(t *testing.T) {
	uid, exID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	uc := &fakeUC{versionView: exerciseUseCase.VersionView{ID: uuid.Must(uuid.NewV7()), ExerciseID: exID}}
	r := newEngine(uc, uid)
	put := func(variant string) {
		t.Helper()
		body := `{"Variants":[` + variant + `]}`
		req := httptest.NewRequest(http.MethodPut, "/api/exercises/"+exID.String()+"/draft", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	topology := `"Topology":{"VPN":{},"Internet":{}}`
	put(`{"Index":1,"Tasks":[],` + topology + `,"Opponents":[{"Role":"defense","Band":2,"Goal":["root"],"StartAfter":"recon","Operates":["web"],"Brief":"hold"}]}`)
	got := uc.savedVariants[0].Opponents
	want := exerciseModel.Opponent{
		Role: exerciseModel.OpponentDefense, Band: 2, Goal: []string{"root"}, Start: exerciseModel.OpponentStart{After: "recon"},
		Operates: []string{"web"}, Brief: "hold",
	}
	if len(got) != 1 || got[0].Role != want.Role || got[0].Band != 2 || got[0].Start != want.Start || got[0].Brief != "hold" || got[0].Operates[0] != "web" || got[0].Goal[0] != "root" {
		t.Fatalf("saved opponents: %+v", got)
	}
	put(`{"Index":1,"Tasks":[],` + topology + `}`)
	if len(uc.savedVariants[0].Opponents) != 0 {
		t.Fatalf("a variant sent without opponents has none: %+v", uc.savedVariants[0].Opponents)
	}
}
