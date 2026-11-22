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

// The publish answer carries the content warnings of the version under the exact names the editor reads.
func TestPublish_ReturnsContentWarnings(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	exID := uuid.Must(uuid.NewV7())
	uc := &fakeUC{versionView: exerciseUseCase.VersionView{ID: uuid.Must(uuid.NewV7()), ExerciseID: exID, Status: "published",
		Warnings: []exerciseModel.ContentWarning{{Code: exerciseModel.WarnAttachmentsInSomeVariants, Task: "flag", WithFiles: []int{1}, WithoutFiles: []int{2, 3}}}}}
	w := httptest.NewRecorder()
	newEngine(uc, uid).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/exercises/"+exID.String()+"/publish", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	want := `"Warnings":[{"Code":"attachments_in_some_variants","Task":"flag","WithFiles":[1],"WithoutFiles":[2,3]}]`
	if !strings.Contains(w.Body.String(), want) {
		t.Fatalf("publish lacks %s: %s", want, w.Body.String())
	}
}

func TestPublish_NoContentWarningsIsAnEmptyArray(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	exID := uuid.Must(uuid.NewV7())
	uc := &fakeUC{versionView: exerciseUseCase.VersionView{ID: uuid.Must(uuid.NewV7()), ExerciseID: exID, Status: "published"}}
	w := httptest.NewRecorder()
	newEngine(uc, uid).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/exercises/"+exID.String()+"/publish", nil))
	if !strings.Contains(w.Body.String(), `"Warnings":[]`) {
		t.Fatalf("publish lacks an empty Warnings array: %s", w.Body.String())
	}
}
