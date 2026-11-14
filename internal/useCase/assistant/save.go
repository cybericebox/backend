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
	// SaveStore remembers how a session was saved: the task, or the background import that is making it, and whether the
	// brain was told. Its key (session, author) is unique.
	SaveStore interface {
		Get(ctx context.Context, session string, author uuid.UUID) (assistantModel.Save, bool, error)
		Create(ctx context.Context, s assistantModel.Save, at time.Time) (bool, error)
		SetExercise(ctx context.Context, session string, author, exercise uuid.UUID) error
		SetNotified(ctx context.Context, session string, author uuid.UUID) error
		Delete(ctx context.Context, session string, author uuid.UUID) error
		Unsettled(ctx context.Context, since time.Time, limit int) ([]assistantModel.Save, error)
	}
	// Locker serializes the saves of one session across replicas; release ends the exclusion.
	Locker func(ctx context.Context, key string) (release func(), err error)
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
//  3. POST {"exercise_id"} to the brain.
//
// An import that fails is the platform's own error and the brain is not told anything. An archive that names device
// image files imports in the background: the call returns done=false once the import is queued, and the leader loop
// (SettleSaves) tells the brain when the job is done, which is the page's `saved` event. A failed job leaves nothing
// behind, so the next save starts again.
//
// How the session was saved is stored with its author. A repeated save of the same session by the same author never
// imports again: it tells the brain the stored task (or follows the queued job). Saves of one session run one at a
// time, so two clicks at once import once. done=true: the brain was told.
func (u *UseCase) SaveSession(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) (done bool, err error) {
	if u.importer == nil || u.saves == nil || u.lock == nil {
		return false, assistantModel.ErrAssistantUnavailable.Err()
	}
	release, err := u.lock(ctx, "assistant-save:"+sessionID)
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to lock the assistant save").Err()
	}
	defer release()
	rec, found, err := u.saves.Get(ctx, sessionID, actor.UserID)
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to read the assistant save").Err()
	}
	if found {
		done, restart, advanceErr := u.advance(ctx, rec)
		if advanceErr != nil || !restart {
			return done, advanceErr
		}
	}
	return u.start(ctx, actor, sessionID)
}

// start fetches the archive and imports it: at once, or as a queued background job.
func (u *UseCase) start(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) (bool, error) {
	archive, err := u.fetchArchive(ctx, actor.UserID, sessionID)
	if err != nil {
		return false, err
	}
	rec := assistantModel.Save{Session: sessionID, Author: actor.UserID}
	lim := u.importer.ArchiveLimits(ctx)
	if u.namesImages(archive, lim) {
		if rec.JobID, err = u.queueWithImages(ctx, actor, archive); err != nil {
			return false, err
		}
	} else {
		start, importErr := u.importer.ImportRawArchive(ctx, actor, exerciseUseCase.ImportExerciseInput{Archive: archive, CreatedBy: actor.UserID, Limits: lim})
		switch {
		case importErr != nil:
			return false, importError(importErr)
		case start.JobID != uuid.Nil:
			rec.JobID = start.JobID
		default:
			if rec.ExerciseID, err = onlyExercise(start); err != nil {
				return false, err
			}
		}
	}
	if _, err = u.saves.Create(ctx, rec, u.now().UTC()); err != nil {
		// The task (or the queued job) exists but is not recorded: a retry would import again.
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to record the assistant save").Err()
	}
	if rec.Pending() {
		return false, nil
	}
	return true, u.notify(ctx, rec)
}

// advance moves a stored save forward: a pending import is followed, an imported task is announced to the brain (again
// when it was announced before: the brain's save is idempotent and the page may have missed `saved`). restart says the
// import failed and its row is gone, so the save can start again.
func (u *UseCase) advance(ctx context.Context, rec assistantModel.Save) (done, restart bool, err error) {
	if rec.Pending() {
		state, ids, jobErr := u.jobOutcome(ctx, rec)
		if jobErr != nil {
			return false, false, jobErr
		}
		switch state {
		case exerciseModel.ImportJobDone:
			if len(ids) != 1 {
				return false, true, u.drop(ctx, rec)
			}
			if err = u.saves.SetExercise(ctx, rec.Session, rec.Author, ids[0]); err != nil {
				return false, false, model.ErrPlatform.WithError(err).WithMessage("Failed to record the imported task").Err()
			}
			rec.ExerciseID = ids[0]
		case exerciseModel.ImportJobFailed:
			return false, true, u.drop(ctx, rec)
		default:
			return false, false, nil
		}
	}
	return true, false, u.notify(ctx, rec)
}

func (u *UseCase) drop(ctx context.Context, rec assistantModel.Save) error {
	if err := u.saves.Delete(ctx, rec.Session, rec.Author); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to clear the failed assistant save").Err()
	}
	return nil
}

// jobOutcome reads the state of the background import of a save as its author. A job that is gone counts as failed.
func (u *UseCase) jobOutcome(ctx context.Context, rec assistantModel.Save) (exerciseModel.ImportJobState, []uuid.UUID, error) {
	job, err := u.importer.ArchiveImportJob(ctx, rec.JobID, rec.Author)
	if err != nil {
		if errors.Is(err, exerciseModel.ErrImportJobNotFound.Err()) {
			return exerciseModel.ImportJobFailed, nil, nil
		}
		return "", nil, err
	}
	return job.State, job.ExerciseIDs, nil
}

// notify tells the brain the task of the save and records that it did.
func (u *UseCase) notify(ctx context.Context, rec assistantModel.Save) error {
	body, err := json.Marshal(map[string]string{"exercise_id": rec.ExerciseID.String()})
	if err != nil {
		return err
	}
	resp, err := u.Proxy(ctx, rec.Author, Call{
		Method: http.MethodPost, Path: "/v1/assistant/sessions/" + rec.Session + "/save",
		Header: http.Header{"Content-Type": {"application/json"}}, Body: bytes.NewReader(body), ContentLength: int64(len(body)),
	})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if err = u.saves.SetNotified(ctx, rec.Session, rec.Author); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to record that the brain was told").Err()
	}
	return nil
}

const (
	// SettleInterval is how often the leader follows the background imports of saves and retries announcements.
	SettleInterval = 10 * time.Second
	// settleWindow: a save older than this is left alone.
	settleWindow = 7 * 24 * time.Hour
	settleBatch  = 50
)

// SettleSaves finishes the saves that wait for the leader: it follows queued imports (a done job tells the brain, a
// failed one clears the save so the page can save again) and announces tasks the brain was not told yet. A failure of
// one save does not stop the others. Only the leader replica runs it.
func (u *UseCase) SettleSaves(ctx context.Context) error {
	if u.saves == nil || u.lock == nil || u.importer == nil || !u.Enabled() {
		return nil
	}
	list, err := u.saves.Unsettled(ctx, u.now().Add(-settleWindow), settleBatch)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list the assistant saves").Err()
	}
	var errs []error
	for _, item := range list {
		if err = u.settle(ctx, item); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (u *UseCase) settle(ctx context.Context, item assistantModel.Save) error {
	release, err := u.lock(ctx, "assistant-save:"+item.Session)
	if err != nil {
		return err
	}
	defer release()
	// A save of the page may have moved it on while the list was read.
	rec, found, err := u.saves.Get(ctx, item.Session, item.Author)
	if err != nil || !found || (!rec.Pending() && rec.Notified) {
		return err
	}
	_, _, err = u.advance(ctx, rec)
	return err
}

// queueWithImages stores the archive as a file of the caller and queues the background import that pushes the images.
func (u *UseCase) queueWithImages(ctx context.Context, actor exerciseUseCase.Actor, archive []byte) (uuid.UUID, error) {
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
	return u.importer.QueueArchiveImport(ctx, exerciseUseCase.QueueImportInput{Source: exerciseModel.ImportSourceFile, SourceID: stored.ID, CreatedBy: actor.UserID})
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
