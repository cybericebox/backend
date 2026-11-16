package exercise

import (
	"context"
	"errors"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// EditedArchiveTarget says which task an archive is the edited version of: the task its document names (it came from an
// ExportTaskVersionForEdit), when the caller may edit that task and it is not archived. uuid.Nil is no edit: an archive that
// names no task, one that names a task the caller may not edit or that is gone, or a bundle of several tasks, is a new task.
// It reads nothing else and writes nothing. A broken archive is an error.
func (u *ExerciseUseCase) EditedArchiveTarget(ctx context.Context, actor Actor, archive []byte, lim exerciseModel.ArchiveLimits) (uuid.UUID, error) {
	parsed, err := ParseExerciseArchive(archive, "", lim)
	if err != nil {
		return uuid.Nil, err
	}
	if parsed.SourceExerciseID == uuid.Nil {
		return uuid.Nil, nil
	}
	if _, _, err = u.previewTarget(ctx, actor, parsed.SourceExerciseID); err != nil {
		if errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) || errors.Is(err, exerciseModel.ErrExerciseNotFound.Err()) || errors.Is(err, exerciseModel.ErrExerciseArchived.Err()) {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	return parsed.SourceExerciseID, nil
}
