package assistant_test

import (
	"errors"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

var editedTask = uuid.FromStringOrNil("66666666-6666-4666-8666-666666666666")

func editRig(t *testing.T, replacesDraft bool) *saveRig {
	t.Helper()
	s := newSaveRig(t, false)
	s.importer.editTarget = editedTask
	s.importer.previewItems = []exerciseUseCase.ArchiveImportPreviewItem{{ItemKey: "task", ConfirmationRevision: "rev-1", ReplacesDraft: replacesDraft}}
	s.importer.commitStart = exerciseUseCase.ArchiveImportStart{Updated: 1, Exercises: []exerciseUseCase.ExerciseView{{ID: editedTask}}}
	return s
}

// The edit of a task that came from the platform goes back into that task as an update, confirmed against the preview of the
// stored archive, with the kept secrets put back; nothing is imported as a new task, and the brain is told the same task.
func TestSaveOfAnEditedTaskUpdatesTheTaskItCameFromAndRestoresItsSecrets(t *testing.T) {
	for name, replaces := range map[string]bool{"no working draft": false, "a working draft is replaced": true} {
		t.Run(name, func(t *testing.T) {
			s := editRig(t, replaces)
			done, err := s.save(t)
			require.NoError(t, err)
			assert.True(t, done)
			assert.NotContains(t, s.line.list(), "import", "no new task is made")
			assert.Equal(t, []string{"brain:archive", "upload", "preview", "commit", "brain:save"}, s.line.list())
			source := exerciseUseCase.PreviewArchiveInput{FileID: fileID, FixedExerciseID: editedTask}
			assert.Equal(t, source, s.importer.previewIn)
			assert.Equal(t, source, s.importer.commitIn.PreviewArchiveInput)
			assert.True(t, s.importer.commitIn.RestoreKeptSecrets)
			assert.Equal(t, []exerciseUseCase.ConfirmedImportUpdate{{ItemKey: "task", Revision: "rev-1", ReplaceDraft: replaces}}, s.importer.commitIn.ConfirmedUpdates)
			assert.Equal(t, editedTask.String(), bodyID(t, s.savedBodies()[0]))
			assert.Equal(t, archive, s.files.body, "the stored source is the archive of the brain")
		})
	}
}

// An archive that is not the edit of a task of the caller is saved as a new task, exactly as before: the placeholder of an edit
// export is never restored there (it is refused by the import).
func TestSaveOfAnArchiveThatIsNotAnEditIsANewTask(t *testing.T) {
	s := newSaveRig(t, false) // the fake importer names no task
	done, err := s.save(t)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Contains(t, s.line.list(), "import")
	assert.NotContains(t, s.line.list(), "commit")
	assert.False(t, s.importer.commitIn.RestoreKeptSecrets)
}

// A refusal of the update (an unresolved secret, a changed task, no right) marks the save failed with its code, tells the brain
// that and does not tell it a task.
func TestSaveOfAnEditedTaskThatTheImportRefusesFailsTheSave(t *testing.T) {
	s := editRig(t, false)
	s.importer.commitErr = exerciseModel.ErrImportSecretUnresolved.WithPublicContext("path", "versions[1]").Err()
	_, err := s.save(t)
	require.ErrorIs(t, err, exerciseModel.ErrImportSecretUnresolved.Err())
	assert.Empty(t, s.savedBodies())
	require.Len(t, failedBodies(s), 1)
	assert.Contains(t, failedBodies(s)[0], "22911")
	rec, found, _ := s.saves.Get(t.Context(), "s1", userOne)
	require.True(t, found)
	assert.Equal(t, assistantModel.SaveFailed, rec.State)
}

func TestSaveOfAnEditedTaskWithAFailedPreviewFailsTheSave(t *testing.T) {
	s := editRig(t, false)
	s.importer.previewErr = errors.New("boom")
	_, err := s.save(t)
	require.Error(t, err)
	assert.NotContains(t, s.line.list(), "commit")
	assert.Empty(t, s.savedBodies())
}

// An edit that names device image files is imported by a queued job (the update options travel with it); the leader finishes it.
func TestSaveOfAnEditedTaskWithImagesQueuesTheUpdateAsAJob(t *testing.T) {
	s := editRig(t, false)
	s.importer.commitStart = exerciseUseCase.ArchiveImportStart{JobID: jobID}
	done, err := s.save(t)
	require.NoError(t, err)
	assert.False(t, done)
	assert.True(t, s.importer.commitIn.RestoreKeptSecrets)
	assert.Empty(t, s.savedBodies(), "the brain is told when the job is done")
}
