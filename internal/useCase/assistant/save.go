package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/daemon/internal/model"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	"github.com/cybericebox/daemon/internal/model/rbac"
	adminAuditUseCase "github.com/cybericebox/daemon/internal/useCase/adminAudit"
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
	// SaveStore remembers how a session was saved. A save is claimed first (pending), then done with the task or failed;
	// the claim is atomic, so one request owns it.
	SaveStore interface {
		Get(ctx context.Context, session string, author uuid.UUID) (assistantModel.Save, bool, error)
		Claim(ctx context.Context, session string, author uuid.UUID, archiveHash string, now, staleBefore time.Time) (bool, error)
		SetJob(ctx context.Context, session string, author, job uuid.UUID) error
		Complete(ctx context.Context, session string, author, exercise uuid.UUID) (bool, error)
		Fail(ctx context.Context, session string, author uuid.UUID) error
		SetNotified(ctx context.Context, session string, author uuid.UUID) error
		Unsettled(ctx context.Context, since time.Time, limit int) ([]assistantModel.Save, error)
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
//  2. claim the save (a row per session and author: pending, with the archive's hash),
//  3. import it through the authorized import path (catalog target, the caller is the author) and complete the row,
//  4. POST {"exercise_id"} to the brain.
//
// An import that fails is the platform's own error, the row is marked failed (the next save claims it again) and the
// brain is not told. An archive that names device image files imports in the background: the call returns done=false
// once the job is queued and recorded, and the leader loop (SettleSaves) completes the row and tells the brain when the
// job is done, which is the page's `saved` event.
//
// A repeated save never imports again: of a done save it tells the brain the stored task, of a pending one it follows
// the job or, for an import still running inside another request, answers done=false. Two saves at once: one claims,
// the other answers done=false. No lock is held while the import runs. done=true: the brain was told.
func (u *UseCase) SaveSession(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) (done bool, err error) {
	if u.importer == nil || u.saves == nil {
		return false, assistantModel.ErrAssistantUnavailable.Err()
	}
	rec, found, err := u.saves.Get(ctx, sessionID, actor.UserID)
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to read the assistant save").Err()
	}
	if found {
		done, claimable, advanceErr := u.advance(ctx, rec)
		if advanceErr != nil || !claimable {
			return done, advanceErr
		}
	}
	return u.start(ctx, actor, sessionID)
}

const (
	// claimLease is how long a claim without an import job is trusted: an import made inside a request is done within it,
	// and a claim older than it belongs to a request that died.
	claimLease = 5 * time.Minute
	// recordAttempts is how often the result of an import is written before giving up.
	recordAttempts = 3
	recordPause    = 50 * time.Millisecond
)

// start fetches the archive, claims the save and imports: at once, or as a queued background job.
func (u *UseCase) start(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) (bool, error) {
	archive, err := u.fetchArchive(ctx, actor.UserID, sessionID)
	if err != nil {
		return false, err
	}
	lim := u.importer.ArchiveLimits(ctx)
	images := u.namesImages(archive, lim)
	if images {
		// Checked before the claim: a refusal must not leave a claim behind.
		if err = u.importer.AuthorizeImport(ctx, actor, nil); err != nil {
			return false, err
		}
	}
	sum := sha256.Sum256(archive)
	now := u.now().UTC()
	claimed, err := u.saves.Claim(ctx, sessionID, actor.UserID, hex.EncodeToString(sum[:]), now, now.Add(-claimLease))
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to claim the assistant save").Err()
	}
	if !claimed {
		// Another request owns it and will tell the brain.
		return false, nil
	}
	rec := assistantModel.Save{Session: sessionID, Author: actor.UserID}
	if images {
		return false, u.queue(ctx, rec, actor, archive)
	}
	start, importErr := u.importer.ImportRawArchive(ctx, actor, exerciseUseCase.ImportExerciseInput{Archive: archive, CreatedBy: actor.UserID, Limits: lim})
	if importErr != nil {
		return false, u.failed(ctx, rec, importError(importErr))
	}
	if start.JobID != uuid.Nil {
		return false, u.recordJob(ctx, rec, start.JobID)
	}
	rec.ExerciseID, err = onlyExercise(start)
	if err != nil {
		return false, u.failed(ctx, rec, err)
	}
	if err = u.complete(ctx, rec); err != nil {
		return false, err
	}
	return true, u.notify(ctx, rec)
}

// queue stores the archive as a file and queues the background import of the claimed save.
func (u *UseCase) queue(ctx context.Context, rec assistantModel.Save, actor exerciseUseCase.Actor, archive []byte) error {
	job, err := u.queueWithImages(ctx, actor, archive)
	if err != nil {
		return u.failed(ctx, rec, err)
	}
	return u.recordJob(ctx, rec, job)
}

// recordJob writes the queued job to the claim. The job runs on its own: if this write fails the claim stays pending
// without a job and, after the lease, the save starts again.
func (u *UseCase) recordJob(ctx context.Context, rec assistantModel.Save, job uuid.UUID) error {
	err := u.retry(ctx, func(ctx context.Context) error { return u.saves.SetJob(ctx, rec.Session, rec.Author, job) })
	if err != nil {
		log.Error().Err(err).Str("job", job.String()).Msg("The queued assistant import is not recorded")
		return model.ErrPlatform.WithError(err).WithMessage("Failed to record the queued assistant import").Err()
	}
	return nil
}

// complete writes the imported task to the claim. The import has no idempotency key of its own (a raw import creates a
// new task each time), so a failed write is retried before it is given up.
func (u *UseCase) complete(ctx context.Context, rec assistantModel.Save) error {
	var changed bool
	err := u.retry(ctx, func(ctx context.Context) (err error) {
		changed, err = u.saves.Complete(ctx, rec.Session, rec.Author, rec.ExerciseID)
		return err
	})
	if err != nil {
		log.Error().Err(err).Str("exercise", rec.ExerciseID.String()).Msg("The imported assistant task is not recorded")
		return model.ErrPlatform.WithError(err).WithMessage("Failed to record the imported assistant task").Err()
	}
	if changed {
		u.recordAudit(ctx, rec)
	}
	return nil
}

// recordAudit writes the admin audit entry of a task the assistant saved, once (when the claim becomes done), whichever
// request or the leader finished it: the same entry shape the middleware writes for a normal import (actor, the task as
// the target), with the source named. The task exists: a failed write is only logged.
func (u *UseCase) recordAudit(ctx context.Context, rec assistantModel.Save) {
	if u.audit == nil {
		return
	}
	err := u.audit(context.WithoutCancel(ctx), adminAuditUseCase.Entry{
		ActorID: rec.Author, Permission: string(rbac.PermSelf), Method: http.MethodPost,
		Route: "/api/assistant/sessions/:id/save", ResponseStatus: http.StatusOK,
		Target: "exercise:" + rec.ExerciseID.String() + " source:assistant",
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to record the audit entry of an assistant save")
	}
}

// retry runs the write a few times, detached from a client that left, so an import that happened is recorded.
func (u *UseCase) retry(ctx context.Context, write func(context.Context) error) (err error) {
	detached := context.WithoutCancel(ctx)
	for attempt := range recordAttempts {
		if err = write(detached); err == nil {
			return nil
		}
		if attempt < recordAttempts-1 {
			time.Sleep(recordPause)
		}
	}
	return err
}

// failed marks the claim failed (so the next save claims it again) and returns the error that ended it.
func (u *UseCase) failed(ctx context.Context, rec assistantModel.Save, cause error) error {
	if err := u.saves.Fail(context.WithoutCancel(ctx), rec.Session, rec.Author); err != nil {
		log.Error().Err(err).Msg("Failed to mark the assistant save as failed")
	}
	return cause
}

// advance moves a stored save forward. A done save is announced to the brain (again when it was announced before: the
// brain's save is idempotent and the page may have missed `saved`). A pending save with a job follows the job (done:
// complete and announce; failed: mark failed). A pending save without a job is an import running in another request,
// until its lease ends. claimable says a new request may claim the row (failed, or a claim whose request died).
func (u *UseCase) advance(ctx context.Context, rec assistantModel.Save) (done, claimable bool, err error) {
	switch rec.State {
	case assistantModel.SaveDone:
		return true, false, u.notify(ctx, rec)
	case assistantModel.SaveFailed:
		return false, true, nil
	}
	if rec.JobID == uuid.Nil {
		return false, !u.now().Before(rec.ClaimedAt.Add(claimLease)), nil
	}
	state, ids, err := u.jobOutcome(ctx, rec)
	if err != nil {
		return false, false, err
	}
	switch state {
	case exerciseModel.ImportJobDone:
		if len(ids) != 1 {
			return false, true, u.saves.Fail(ctx, rec.Session, rec.Author)
		}
		rec.ExerciseID = ids[0]
		if err = u.complete(ctx, rec); err != nil {
			return false, false, err
		}
		return true, false, u.notify(ctx, rec)
	case exerciseModel.ImportJobFailed:
		return false, true, u.saves.Fail(ctx, rec.Session, rec.Author)
	}
	return false, false, nil
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

// SettleSaves finishes the saves that wait for the leader: it follows queued imports (a done job completes the save and
// tells the brain, a failed one marks it failed so the page can save again) and announces tasks the brain was not told
// yet. Every step is a compare-and-set on the save, so two replicas settling the same one do no harm. A failure of one
// save does not stop the others. Only the leader replica runs it.
func (u *UseCase) SettleSaves(ctx context.Context) error {
	if u.saves == nil || u.importer == nil || !u.Enabled() {
		return nil
	}
	list, err := u.saves.Unsettled(ctx, u.now().Add(-settleWindow), settleBatch)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list the assistant saves").Err()
	}
	var errs []error
	for _, item := range list {
		if _, _, err = u.advance(ctx, item); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// queueWithImages (after AuthorizeImport) stores the archive as a file of the caller and queues the background import that pushes the images.
func (u *UseCase) queueWithImages(ctx context.Context, actor exerciseUseCase.Actor, archive []byte) (uuid.UUID, error) {
	if u.files == nil {
		return uuid.Nil, model.ErrPlatform.WithMessage("Image import is not wired").Err()
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
