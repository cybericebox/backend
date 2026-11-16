package exercise

import (
	"context"
	"errors"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// EditBase is what an edit started from: the version the editor was given and the revision of the task's working copy then
// ("" when it had none). It travels in the document, so the save of the edit can tell whether the working copy has moved on.
type EditBase struct {
	VersionID     uuid.UUID
	DraftRevision string
}

// EditTarget is the task an archive is the edit of (ExerciseID uuid.Nil: no edit, a new task) and what the edit started from.
type EditTarget struct {
	ExerciseID uuid.UUID
	Base       EditBase
}

// EditedArchiveTarget says which task an archive is the edited version of: the task its document names (it came from an
// ExportTaskVersionForEdit), when the caller may edit that task and it is not archived. uuid.Nil is no edit: an archive that
// names no task, one that names a task the caller may not edit or that is gone, or a bundle of several tasks, is a new task.
// It reads nothing else and writes nothing. A broken archive is an error.
func (u *ExerciseUseCase) EditedArchiveTarget(ctx context.Context, actor Actor, archive []byte, lim exerciseModel.ArchiveLimits) (EditTarget, error) {
	parsed, err := ParseExerciseArchive(archive, "", lim)
	if err != nil {
		return EditTarget{}, err
	}
	if parsed.SourceExerciseID == uuid.Nil {
		return EditTarget{}, nil
	}
	if _, _, err = u.previewTarget(ctx, actor, parsed.SourceExerciseID); err != nil {
		if errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) || errors.Is(err, exerciseModel.ErrExerciseNotFound.Err()) || errors.Is(err, exerciseModel.ErrExerciseArchived.Err()) {
			return EditTarget{}, nil
		}
		return EditTarget{}, err
	}
	return EditTarget{ExerciseID: parsed.SourceExerciseID, Base: parsed.EditBase}, nil
}

// ArchiveCarriesSecretValue says whether an archive holds the value of a secret variable (an empty secret is only declared). A
// task made by the brain never does: the brain sets no secret value. A broken archive is an error.
func (u *ExerciseUseCase) ArchiveCarriesSecretValue(archive []byte, lim exerciseModel.ArchiveLimits) (bool, error) {
	parsed, err := ParseExerciseArchive(archive, "", lim)
	if err != nil {
		return false, err
	}
	return firstSecretValue(parsed.Versions) != "", nil
}
