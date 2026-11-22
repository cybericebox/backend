package exerciseModel_test

import (
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// independentVersion is n independent variants with the same two subtasks (keys "intro" and "flag").
func independentVersion(n int) exerciseModel.ExerciseVersion {
	variants := make([]exerciseModel.Variant, 0, n)
	introID, flagID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for i := range n {
		intro, flag := validTask(), validTask()
		intro.ID, flag.ID = introID, flagID
		intro.Key, intro.Name = "intro", "Intro"
		flag.Key, flag.Name = "flag", "Find the flag"
		intro.Check = exerciseModel.FlagCheck("ICE{intro" + string(rune('a'+i)) + "}")
		flag.Check = exerciseModel.FlagCheck("ICE{flag" + string(rune('a'+i)) + "}")
		variants = append(variants, exerciseModel.Variant{
			ID:    uuid.Must(uuid.NewV7()),
			Tasks: []exerciseModel.Task{intro, flag},
		})
	}
	return exerciseModel.ExerciseVersion{Variants: variants}
}

func TestContentWarnings_IndependentVariantsWithFilesInSomeOnly(t *testing.T) {
	version := independentVersion(3)
	version.Variants[0].Tasks[1].Attachments = attachment("a.bin")
	version.Variants[2].Tasks[1].Attachments = attachment("c.bin")

	assert.Equal(t, []exerciseModel.ContentWarning{{
		Code:         exerciseModel.WarnAttachmentsInSomeVariants,
		Task:         "flag",
		WithFiles:    []int{1, 3},
		WithoutFiles: []int{2},
	}}, version.ContentWarnings())
	// A warning, not an error: the version still publishes.
	require.NoError(t, version.ValidateForPublish())
}

func TestContentWarnings_NoneWhenFilesAreEverywhereOrNowhere(t *testing.T) {
	version := independentVersion(2)
	assert.Equal(t, []exerciseModel.ContentWarning{}, version.ContentWarnings())

	for i := range version.Variants {
		version.Variants[i].Tasks[0].Attachments = attachment("all.bin")
	}
	assert.Equal(t, []exerciseModel.ContentWarning{}, version.ContentWarnings())
}

// The variants are numbered by their Index, the number the editor shows.
func TestContentWarnings_VariantsAreNumberedByTheirIndex(t *testing.T) {
	version := independentVersion(3)
	for i := range version.Variants {
		version.Variants[i].Index = int32(10 + i)
	}
	version.Variants[1].Tasks[1].Attachments = attachment("b.bin")

	warnings := version.ContentWarnings()
	require.Len(t, warnings, 1)
	assert.Equal(t, []int{11}, warnings[0].WithFiles)
	assert.Equal(t, []int{10, 12}, warnings[0].WithoutFiles)
}

func TestContentWarnings_TaskWithoutKeyIsNamed(t *testing.T) {
	version := independentVersion(2)
	version.Variants[0].Tasks[0].Key = ""
	version.Variants[1].Tasks[0].Attachments = attachment("b.bin")

	warnings := version.ContentWarnings()
	require.Len(t, warnings, 1)
	assert.Equal(t, "Intro", warnings[0].Task)
	assert.Equal(t, []int{2}, warnings[0].WithFiles)
	assert.Equal(t, []int{1}, warnings[0].WithoutFiles)
}

// Linked variants are the publish error of linked.go (ErrLinkedAttachmentMissing), never this warning: only
// independent variants are compared, numbered by their position in the version.
func TestContentWarnings_LinkedVariantsAreNotCompared(t *testing.T) {
	version := linkedPublishVersion(2)
	version.Variants[1].Tasks[0].Attachments = attachment("own.txt")
	assert.Equal(t, []exerciseModel.ContentWarning{}, version.ContentWarnings())

	independent := independentVersion(1).Variants[0]
	independent.Tasks = independent.Tasks[1:]
	independent.Tasks[0].ID = uuid.Nil
	independent.Tasks[0].Attachments = attachment("own.txt")
	version = linkedPublishVersion(1)
	version.Variants = append(version.Variants, independent)
	assert.Equal(t, []exerciseModel.ContentWarning{{
		Code:         exerciseModel.WarnAttachmentsInSomeVariants,
		Task:         "Find the flag", // the base names the subtask: it has no key
		WithFiles:    []int{3},
		WithoutFiles: []int{1},
	}}, version.ContentWarnings())
}
