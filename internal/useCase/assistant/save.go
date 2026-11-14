package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/model"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
	appErr "github.com/cybericebox/daemon/pkg/err"
)

type (
	// Importer is the authorized import path of the exercise use case.
	Importer interface {
		AuthorizeImport(ctx context.Context, actor exerciseUseCase.Actor, ownerEventID *uuid.UUID) error
		ArchiveLimits(ctx context.Context) exerciseModel.ArchiveLimits
		ImportRawArchive(ctx context.Context, actor exerciseUseCase.Actor, in exerciseUseCase.ImportExerciseInput) (exerciseUseCase.ArchiveImportStart, error)
		QueueArchiveImport(ctx context.Context, in exerciseUseCase.QueueImportInput) (uuid.UUID, error)
		ArchiveImportJob(ctx context.Context, id, user uuid.UUID) (exerciseUseCase.ImportJobView, error)
	}
	// FileStore stores the archive for a background import (the media use case).
	FileStore interface {
		UploadFile(ctx context.Context, name, contentType string, r io.Reader, createdBy uuid.UUID) (mediaModel.File, error)
	}
)

// SaveSession saves the finished task of a session into the platform catalog as the caller and tells the brain the
// task's id so it can end the session with `saved`:
//
//  1. fetch the archive from the brain,
//  2. import it through the authorized import path (catalog target, the caller is the author),
//  3. POST the new exercise id to the brain.
//
// An import that fails is the platform's own error and the brain is not told anything. An archive that names device
// image files imports in the background; the call waits for that job (bounded) and the brain is told once it is done.
func (u *UseCase) SaveSession(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) error {
	if u.importer == nil {
		return assistantModel.ErrAssistantUnavailable.Err()
	}
	archive, err := u.fetchArchive(ctx, actor.UserID, sessionID)
	if err != nil {
		return err
	}
	exerciseID, err := u.importFinished(ctx, actor, archive)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"ExerciseID": exerciseID.String()})
	if err != nil {
		return err
	}
	resp, err := u.Proxy(ctx, actor.UserID, Call{
		Method: http.MethodPost, Path: "/v1/assistant/sessions/" + sessionID + "/save",
		Header: http.Header{"Content-Type": {"application/json"}}, Body: bytes.NewReader(body), ContentLength: int64(len(body)),
	})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.Body.Close()
}

func (u *UseCase) fetchArchive(ctx context.Context, user uuid.UUID, sessionID string) ([]byte, error) {
	resp, err := u.Proxy(ctx, user, Call{Method: http.MethodGet, Path: "/v1/assistant/sessions/" + sessionID + "/archive"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := exerciseUseCase.ReadArchiveBytes(resp.Body, u.importer.ArchiveLimits(ctx).ArchiveBytes)
	if err != nil {
		return nil, importError(err)
	}
	return data, nil
}

// importFinished imports the archive as the actor and returns the id of the one task it created.
func (u *UseCase) importFinished(ctx context.Context, actor exerciseUseCase.Actor, archive []byte) (uuid.UUID, error) {
	lim := u.importer.ArchiveLimits(ctx)
	if u.namesImages(archive, lim) {
		return u.importWithImages(ctx, actor, archive)
	}
	start, err := u.importer.ImportRawArchive(ctx, actor, exerciseUseCase.ImportExerciseInput{Archive: archive, CreatedBy: actor.UserID, Limits: lim})
	if err != nil {
		return uuid.Nil, importError(err)
	}
	if start.JobID != uuid.Nil {
		return u.waitForJob(ctx, actor.UserID, start.JobID)
	}
	return onlyExercise(start)
}

// importError keeps a typed error of the import (its code and public context reach the page); an untyped failure of the
// archive itself (a broken zip) is a failed import, the rest of the untyped ones stay internal.
func importError(failure error) error {
	failure = exerciseUseCase.ArchivePasswordError(failure)
	var typed appErr.Error
	switch {
	case errors.As(failure, &typed):
		return failure
	case exerciseUseCase.IsArchiveFormatError(failure):
		return assistantModel.ErrAssistantImportFailed.WithError(failure).Err()
	default:
		return model.ErrPlatform.WithError(failure).WithMessage("Failed to import the finished task").Err()
	}
}

func onlyExercise(start exerciseUseCase.ArchiveImportStart) (uuid.UUID, error) {
	if len(start.Exercises) != 1 {
		return uuid.Nil, assistantModel.ErrAssistantImportFailed.WithPublicContext("tasks", len(start.Exercises)).Err()
	}
	return start.Exercises[0].ID, nil
}

// importWithImages stores the archive as a file of the caller and queues the background import that pushes the images.
func (u *UseCase) importWithImages(ctx context.Context, actor exerciseUseCase.Actor, archive []byte) (uuid.UUID, error) {
	if u.files == nil {
		return uuid.Nil, model.ErrPlatform.WithMessage("Image import is not wired").Err()
	}
	if err := u.importer.AuthorizeImport(ctx, actor, nil); err != nil {
		return uuid.Nil, err
	}
	stored, err := u.files.UploadFile(ctx, "assistant-task.cybericebox.zip", "application/zip", bytes.NewReader(archive), actor.UserID)
	if err != nil {
		return uuid.Nil, err
	}
	jobID, err := u.importer.QueueArchiveImport(ctx, exerciseUseCase.QueueImportInput{Source: exerciseModel.ImportSourceFile, SourceID: stored.ID, CreatedBy: actor.UserID})
	if err != nil {
		return uuid.Nil, err
	}
	return u.waitForJob(ctx, actor.UserID, jobID)
}

// waitForJob polls the background import until it is done or failed, at most importWait. A job still running after that
// keeps running; the caller is told so and saving can be asked for again.
func (u *UseCase) waitForJob(ctx context.Context, user, jobID uuid.UUID) (uuid.UUID, error) {
	deadline := time.NewTimer(u.importWait)
	defer deadline.Stop()
	tick := time.NewTicker(u.importPoll)
	defer tick.Stop()
	for {
		job, err := u.importer.ArchiveImportJob(ctx, jobID, user)
		if err != nil {
			return uuid.Nil, err
		}
		switch job.State {
		case exerciseModel.ImportJobDone:
			if len(job.ExerciseIDs) != 1 {
				return uuid.Nil, assistantModel.ErrAssistantImportFailed.WithPublicContext("tasks", len(job.ExerciseIDs)).Err()
			}
			return job.ExerciseIDs[0], nil
		case exerciseModel.ImportJobFailed:
			failed := assistantModel.ErrAssistantImportFailed
			if job.Error != nil {
				failed = failed.WithPublicContext("importCode", job.Error.Code)
			}
			return uuid.Nil, failed.Err()
		}
		select {
		case <-ctx.Done():
			return uuid.Nil, ctx.Err()
		case <-deadline.C:
			return uuid.Nil, assistantModel.ErrAssistantImportPending.Err()
		case <-tick.C:
		}
	}
}
