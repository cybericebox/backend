package assistant_test

import (
	"errors"
	"strconv"
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
	s := editRig(t, false)
	done, err := s.save(t)
	require.NoError(t, err)
	assert.True(t, done)
	assert.NotContains(t, s.line.list(), "import", "no new task is made")
	assert.Equal(t, []string{"brain:archive", "upload", "preview", "commit", "discard", "brain:save"}, s.line.list())
	source := exerciseUseCase.PreviewArchiveInput{FileID: fileID, FixedExerciseID: editedTask}
	assert.Equal(t, source, s.importer.previewIn)
	assert.Equal(t, source, s.importer.commitIn.PreviewArchiveInput)
	assert.True(t, s.importer.commitIn.RestoreKeptSecrets)
	assert.Equal(t, []exerciseUseCase.ConfirmedImportUpdate{{ItemKey: "task", Revision: "rev-1", ReplaceDraft: false}}, s.importer.commitIn.ConfirmedUpdates)
	assert.Equal(t, editedTask.String(), bodyID(t, s.savedBodies()[0]))
	assert.Equal(t, archive, s.files.body, "the stored source is the archive of the brain")
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

var (
	workingCopy  = uuid.FromStringOrNil("77777777-7777-4777-8777-777777777777")
	publishedRow = uuid.FromStringOrNil("88888888-8888-4888-8888-888888888888")
)

// editBasedOn is an edit export that started from the version `from`, when the working copy of the task was at `revision` ("":
// it had none); the preview of the stored archive finds the working copy at `now` (draft id and revision).
func editBasedOn(t *testing.T, from uuid.UUID, revision string, draft *uuid.UUID, now string) *saveRig {
	t.Helper()
	s := editRig(t, draft != nil)
	s.importer.editBase = exerciseUseCase.EditBase{VersionID: from, DraftRevision: revision, PublishedVersionID: publishedRow}
	s.importer.previewItems[0].PublishedVersionID = &publishedRow
	s.importer.previewItems[0].DraftVersionID = draft
	s.importer.previewItems[0].DraftRevision = now
	return s
}

// An edit of the working copy that is still at the revision it was exported at replaces that working copy with its own result:
// nothing is asked, and the commit confirms the preview.
func TestSaveOfAnEditOfTheWorkingCopyAtItsRevisionCommitsAtOnce(t *testing.T) {
	s := editBasedOn(t, workingCopy, "R1", &workingCopy, "R1")
	done, err := s.save(t)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, []exerciseUseCase.ConfirmedImportUpdate{{ItemKey: "task", Revision: "rev-1", ReplaceDraft: true}}, s.importer.commitIn.ConfirmedUpdates)
	assert.NotContains(t, s.line.list(), "stale")
}

// A working copy that moved on, appeared or went since the export is not the one the edit started from: the save is the
// concurrent-edit 409, nothing is claimed, committed or told, and it is not a failure of the import.
func TestSaveOfAnEditIsRefusedWhenTheWorkingCopyMovedOnAppearedOrWent(t *testing.T) {
	for name, s := range map[string]*saveRig{
		"moved on": editBasedOn(t, workingCopy, "R1", &workingCopy, "R2"),
		"appeared": editBasedOn(t, publishedRow, "", &workingCopy, "R9"),
		"gone":     editBasedOn(t, workingCopy, "R1", nil, ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.save(t)
			require.ErrorIs(t, err, exerciseModel.ErrDraftModified.Err())
			assert.Contains(t, s.line.list(), "stale")
			assert.NotContains(t, s.line.list(), "commit")
			assert.Empty(t, s.savedBodies())
			assert.Empty(t, failedBodies(s))
			_, found, _ := s.saves.Get(t.Context(), "s1", userOne)
			assert.False(t, found, "no claim is left behind")
		})
	}
}

// An edit of a task with no working copy, exported and saved with none, is a plain update.
func TestSaveOfAnEditWithNoWorkingCopyEitherWayIsAPlainUpdate(t *testing.T) {
	s := editBasedOn(t, publishedRow, "", nil, "")
	done, err := s.save(t)
	require.NoError(t, err)
	assert.True(t, done)
}

// An edit exported with no working copy is applied only while the task's published version is still the one it was exported at:
// another editor publishing a version since is the concurrent-edit 409, and nothing is claimed, committed or told.
func TestSaveOfAnEditWithNoWorkingCopyIsRefusedWhenAnotherVersionWasPublished(t *testing.T) {
	republished := uuid.FromStringOrNil("99999999-9999-4999-8999-999999999999")
	for name, now := range map[string]*uuid.UUID{"another version published": &republished, "unpublished": nil} {
		t.Run(name, func(t *testing.T) {
			s := editBasedOn(t, publishedRow, "", nil, "")
			s.importer.previewItems[0].PublishedVersionID = now
			_, err := s.save(t)
			require.ErrorIs(t, err, exerciseModel.ErrDraftModified.Err())
			assert.Contains(t, s.line.list(), "stale")
			assert.NotContains(t, s.line.list(), "commit")
			assert.Empty(t, s.savedBodies())
			_, found, _ := s.saves.Get(t.Context(), "s1", userOne)
			assert.False(t, found, "no claim is left behind")
		})
	}
}

// The archive an edit stores for its preview is removed again whenever the edit is not committed from it: a refusal (409), a
// failed preview, a claim another request won and a failed commit. A commit done at once does not need it either; a queued job
// reads it later, so it stays.
func TestTheStoredArchiveOfAnEditIsRemovedWhenNothingUsesIt(t *testing.T) {
	cases := map[string]struct {
		arrange func(s *saveRig)
		kept    bool
	}{
		"refused as stale": {arrange: func(s *saveRig) { s.importer.previewItems[0].DraftRevision = "R9" }},
		"failed preview":   {arrange: func(s *saveRig) { s.importer.previewErr = errors.New("boom") }},
		"lost claim":       {arrange: func(s *saveRig) { s.saves.lose = true }},
		"failed commit":    {arrange: func(s *saveRig) { s.importer.commitErr = exerciseModel.ErrImportConfirmationStale.Err() }},
		"committed":        {arrange: func(*saveRig) {}},
		"queued job":       {arrange: func(s *saveRig) { s.importer.commitStart = exerciseUseCase.ArchiveImportStart{JobID: jobID} }, kept: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := editBasedOn(t, publishedRow, "", nil, "")
			c.arrange(s)
			_, _ = s.save(t)
			if c.kept {
				assert.Empty(t, s.files.discarded, "the queued job reads the stored archive")
				return
			}
			assert.Equal(t, []uuid.UUID{fileID}, s.files.discarded, "no stored archive is left behind")
		})
	}
}

// The finished task is the edit of a task the user may no longer edit, that is gone or archived: the save is refused with its own
// code before anything is stored or claimed, never saved as a new task.
func TestSaveOfAnEditOfATaskNoLongerEditableIsRefused(t *testing.T) {
	for name, c := range map[string]struct{ from, want error }{
		"forbidden": {exerciseModel.ErrExerciseForbidden.Err(), assistantModel.ErrAssistantEditForbidden.Err()},
		"gone":      {exerciseModel.ErrExerciseNotFound.Err(), assistantModel.ErrAssistantEditGone.Err()},
		"archived":  {exerciseModel.ErrExerciseArchived.Err(), assistantModel.ErrAssistantEditArchived.Err()},
	} {
		t.Run(name, func(t *testing.T) {
			s := editRig(t, false)
			s.importer.editErr = c.from
			_, err := s.save(t)
			require.ErrorIs(t, err, c.want)
			assert.Equal(t, []string{"brain:archive"}, s.line.list(), "nothing is stored, imported or claimed")
			_, found, _ := s.saves.Get(t.Context(), "s1", userOne)
			assert.False(t, found)
		})
	}
}
