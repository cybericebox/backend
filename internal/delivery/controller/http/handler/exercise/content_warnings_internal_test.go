package exercise

import (
	"testing"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// An explicit import answers the content warnings of every item at the top level, naming the exercise.
func TestImportCommitResponse_CollectsContentWarnings(t *testing.T) {
	exID := uuid.Must(uuid.NewV7())
	results := []exerciseModel.ImportResult{{ItemKey: "a", Mode: "new", ExerciseID: exID, Report: &exerciseModel.ImportReport{
		Warnings: []exerciseModel.ImportWarning{
			{Code: exerciseModel.ImportWarnNoExerciseID},
			{Code: exerciseModel.WarnAttachmentsInSomeVariants, Task: "flag", WithFiles: []int{2}, WithoutFiles: []int{1}},
		},
	}}, {ItemKey: "b", Mode: "new"}}
	got := importContentWarnings(results)
	if len(got) != 1 || got[0].ExerciseID != exID || got[0].ItemKey != "a" || got[0].Task != "flag" ||
		got[0].Code != exerciseModel.WarnAttachmentsInSomeVariants || len(got[0].WithFiles) != 1 || got[0].WithoutFiles[0] != 1 {
		t.Fatalf("unexpected warnings: %+v", got)
	}
	withWarnings := withResultWarnings(results)
	if len(withWarnings[0].Warnings) != 1 || withWarnings[0].Warnings[0].Task != "flag" || withWarnings[0].Warnings[0].WithFiles[0] != 2 {
		t.Fatalf("the result entry lacks its warnings: %+v", withWarnings[0])
	}
	if withWarnings[1].Warnings == nil || len(withWarnings[1].Warnings) != 0 {
		t.Fatalf("a result without warnings has an empty, non-nil slice: %#v", withWarnings[1].Warnings)
	}
	if empty := importContentWarnings(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("want an empty, non-nil slice, got %#v", empty)
	}
}
