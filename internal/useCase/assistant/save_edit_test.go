package assistant_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/useCase/assistant"
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
	s := editRig(t, false)
	done, err := s.save(t)
	require.NoError(t, err)
	assert.True(t, done)
	assert.NotContains(t, s.line.list(), "import", "no new task is made")
	assert.Equal(t, []string{"brain:archive", "upload", "preview", "commit", "brain:save"}, s.line.list())
	source := exerciseUseCase.PreviewArchiveInput{FileID: fileID, FixedExerciseID: editedTask}
	assert.Equal(t, source, s.importer.previewIn)
	assert.Equal(t, source, s.importer.commitIn.PreviewArchiveInput)
	assert.True(t, s.importer.commitIn.RestoreKeptSecrets)
	assert.Equal(t, []exerciseUseCase.ConfirmedImportUpdate{{ItemKey: "task", Revision: "rev-1", ReplaceDraft: false}}, s.importer.commitIn.ConfirmedUpdates)
	assert.Equal(t, editedTask.String(), bodyID(t, s.savedBodies()[0]))
	assert.Equal(t, archive, s.files.body, "the stored source is the archive of the brain")
}

func archiveHash() string {
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}

// An edit that replaces the working draft of its task is the author's to confirm: the first save is refused with what the
// author needs to see and confirm (the stored archive, the revision, the summary), nothing is claimed or written, and the
// second save with that confirmation commits exactly that preview with the replacement confirmed.
func TestSaveOfAnEditThatReplacesTheDraftNeedsTheAuthorsConfirmation(t *testing.T) {
	s := editRig(t, true)
	_, err := s.save(t)
	require.ErrorIs(t, err, assistantModel.ErrAssistantEditNeedsConfirmation.Err())
	assert.Equal(t, []string{"brain:archive", "upload", "preview"}, s.line.list(), "nothing is committed, claimed or told")
	assert.Empty(t, s.savedBodies())
	assert.Empty(t, failedBodies(s), "it is not a failure: the brain is not told one")
	_, found, _ := s.saves.Get(t.Context(), "s1", userOne)
	assert.False(t, found, "no claim is left behind: the author can confirm at once")

	confirm := &assistant.EditConfirmation{FileID: fileID, Revision: "rev-1", ArchiveHash: archiveHash()}
	done, err := s.uc.SaveSession(t.Context(), actor, "s1", confirm)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, 1, s.line.count("upload"), "the confirmed archive is the stored one: it is not stored again")
	assert.Equal(t, exerciseUseCase.PreviewArchiveInput{FileID: fileID, FixedExerciseID: editedTask}, s.importer.commitIn.PreviewArchiveInput)
	assert.Equal(t, []exerciseUseCase.ConfirmedImportUpdate{{ItemKey: "task", Revision: "rev-1", ReplaceDraft: true}}, s.importer.commitIn.ConfirmedUpdates)
	assert.Equal(t, editedTask.String(), bodyID(t, s.savedBodies()[0]))
}

// A confirmation of something else than what is there now is stale: another archive from the brain, or a task that changed
// since the preview (another revision). Nothing is committed.
func TestSaveOfAnEditWithAStaleConfirmationIsRefused(t *testing.T) {
	for name, confirm := range map[string]*assistant.EditConfirmation{
		"the brain's archive changed": {FileID: fileID, Revision: "rev-1", ArchiveHash: "0000"},
		"the task changed":            {FileID: fileID, Revision: "rev-0", ArchiveHash: archiveHash()},
	} {
		t.Run(name, func(t *testing.T) {
			s := editRig(t, true)
			_, err := s.uc.SaveSession(t.Context(), actor, "s1", confirm)
			require.ErrorIs(t, err, exerciseModel.ErrImportConfirmationStale.Err())
			assert.NotContains(t, s.line.list(), "commit")
			assert.Empty(t, s.savedBodies())
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

// A refusal of the update (a changed task, no right) marks the save failed with its code, tells the brain
// that and does not tell it a task.
func TestSaveOfAnEditedTaskThatTheImportRefusesFailsTheSave(t *testing.T) {
	s := editRig(t, false)
	s.importer.commitErr = exerciseModel.ErrImportConfirmationStale.Err()
	_, err := s.save(t)
	require.ErrorIs(t, err, exerciseModel.ErrImportConfirmationStale.Err())
	assert.Empty(t, s.savedBodies())
	require.Len(t, failedBodies(s), 1)
	assert.Contains(t, failedBodies(s)[0], strconv.Itoa(exerciseModel.ErrImportConfirmationStale.Err().StatusCode().FullCode()))
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

// The brain sets no secret value: an archive that carries one is refused before anything is claimed, for a new task and for the
// edit of one alike.
func TestSaveRefusesAnArchiveThatCarriesASecretValue(t *testing.T) {
	for name, target := range map[string]uuid.UUID{"a new task": uuid.Nil, "an edited task": editedTask} {
		t.Run(name, func(t *testing.T) {
			s := editRig(t, false)
			s.importer.editTarget = target
			s.importer.carriesValue = true
			_, err := s.save(t)
			require.ErrorIs(t, err, assistantModel.ErrAssistantSecretValue.Err())
			assert.Equal(t, []string{"brain:archive"}, s.line.list(), "nothing is imported or claimed")
			assert.Empty(t, s.savedBodies())
			_, found, _ := s.saves.Get(t.Context(), "s1", userOne)
			assert.False(t, found, "no claim is left behind")
		})
	}
}
