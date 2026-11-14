// Package assistantSaveRepo remembers how a finished assistant session was saved: claimed first (pending), then done
// with the catalog task or failed, and whether the brain was told. The primary key (session, author) makes one request
// the owner of a claim.
package assistantSaveRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

type Queries interface {
	GetAssistantSave(context.Context, postgres.GetAssistantSaveParams) (postgres.GetAssistantSaveRow, error)
	ClaimAssistantSave(context.Context, postgres.ClaimAssistantSaveParams) (int64, error)
	SetAssistantSaveJob(context.Context, postgres.SetAssistantSaveJobParams) (int64, error)
	CompleteAssistantSave(context.Context, postgres.CompleteAssistantSaveParams) (int64, error)
	FailAssistantSave(context.Context, postgres.FailAssistantSaveParams) (int64, error)
	SetAssistantSaveNotified(context.Context, postgres.SetAssistantSaveNotifiedParams) (int64, error)
	ListUnsettledAssistantSaves(context.Context, postgres.ListUnsettledAssistantSavesParams) ([]postgres.ListUnsettledAssistantSavesRow, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

func idOf(n uuid.NullUUID) uuid.UUID {
	if !n.Valid {
		return uuid.Nil
	}
	return n.UUID
}

func toSave(session string, author uuid.UUID, state string, exercise, job uuid.NullUUID, notified bool, claimed time.Time) assistantModel.Save {
	return assistantModel.Save{
		Session: session, Author: author, State: assistantModel.SaveState(state), ExerciseID: idOf(exercise), JobID: idOf(job),
		Notified: notified, ClaimedAt: claimed,
	}
}

// Get reads the save of the session by the author; found is false when there is none.
func (r *Repository) Get(ctx context.Context, session string, author uuid.UUID) (assistantModel.Save, bool, error) {
	row, err := r.q.GetAssistantSave(ctx, postgres.GetAssistantSaveParams{SessionID: session, AuthorID: author})
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return assistantModel.Save{}, false, nil
		}
		return assistantModel.Save{}, false, err
	}
	return toSave(session, author, row.State, row.ExerciseID, row.JobID, row.Notified, row.ClaimedAt.Time), true, nil
}

// Claim makes the caller the owner of the save: a new row, a failed one, or a pending one without a job claimed before
// staleBefore. It reports false when another request holds it.
func (r *Repository) Claim(ctx context.Context, session string, author uuid.UUID, archiveHash string, now, staleBefore time.Time) (bool, error) {
	n, err := r.q.ClaimAssistantSave(ctx, postgres.ClaimAssistantSaveParams{
		SessionID: session, AuthorID: author, ArchiveHash: archiveHash, Now: now, StaleBefore: staleBefore,
	})
	return n > 0, err
}

// SetJob records the background import queued for the claim.
func (r *Repository) SetJob(ctx context.Context, session string, author, job uuid.UUID) error {
	_, err := r.q.SetAssistantSaveJob(ctx, postgres.SetAssistantSaveJobParams{SessionID: session, AuthorID: author, JobID: job})
	return err
}

// Complete stores the task and ends the claim (done). It reports false when the save was not pending.
func (r *Repository) Complete(ctx context.Context, session string, author, exercise uuid.UUID) (bool, error) {
	n, err := r.q.CompleteAssistantSave(ctx, postgres.CompleteAssistantSaveParams{SessionID: session, AuthorID: author, ExerciseID: exercise})
	return n > 0, err
}

// Fail ends the claim as failed: the next save claims the row again.
func (r *Repository) Fail(ctx context.Context, session string, author uuid.UUID) error {
	_, err := r.q.FailAssistantSave(ctx, postgres.FailAssistantSaveParams{SessionID: session, AuthorID: author})
	return err
}

// SetNotified records that the brain was told the task.
func (r *Repository) SetNotified(ctx context.Context, session string, author uuid.UUID) error {
	_, err := r.q.SetAssistantSaveNotified(ctx, postgres.SetAssistantSaveNotifiedParams{SessionID: session, AuthorID: author})
	return err
}

// Unsettled lists the saves newer than since that wait for a queued import or for the brain to be told.
func (r *Repository) Unsettled(ctx context.Context, since time.Time, limit int) ([]assistantModel.Save, error) {
	rows, err := r.q.ListUnsettledAssistantSaves(ctx, postgres.ListUnsettledAssistantSavesParams{Since: since, MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.Save, 0, len(rows))
	for _, row := range rows {
		out = append(out, toSave(row.SessionID, row.AuthorID, row.State, row.ExerciseID, row.JobID, row.Notified, row.ClaimedAt.Time))
	}
	return out, nil
}
