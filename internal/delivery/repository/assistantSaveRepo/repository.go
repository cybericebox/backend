// Package assistantSaveRepo remembers how a finished assistant session was saved: the catalog task (or the background
// import job that is making it) and whether the brain was told. The primary key (session, author) is the guard against
// two saves at once.
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
	CreateAssistantSave(context.Context, postgres.CreateAssistantSaveParams) (int64, error)
	SetAssistantSaveExercise(context.Context, postgres.SetAssistantSaveExerciseParams) (int64, error)
	SetAssistantSaveNotified(context.Context, postgres.SetAssistantSaveNotifiedParams) (int64, error)
	DeleteAssistantSave(context.Context, postgres.DeleteAssistantSaveParams) (int64, error)
	ListUnsettledAssistantSaves(context.Context, postgres.ListUnsettledAssistantSavesParams) ([]postgres.ListUnsettledAssistantSavesRow, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

func nullID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

func idOf(n uuid.NullUUID) uuid.UUID {
	if !n.Valid {
		return uuid.Nil
	}
	return n.UUID
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
	return assistantModel.Save{Session: session, Author: author, ExerciseID: idOf(row.ExerciseID), JobID: idOf(row.JobID), Notified: row.Notified}, true, nil
}

// Create records a save (a task, or a queued import job). It reports false when the row is already there (it stays).
func (r *Repository) Create(ctx context.Context, s assistantModel.Save, at time.Time) (bool, error) {
	n, err := r.q.CreateAssistantSave(ctx, postgres.CreateAssistantSaveParams{
		SessionID: s.Session, AuthorID: s.Author, ExerciseID: nullID(s.ExerciseID), JobID: nullID(s.JobID), CreatedAt: at,
	})
	return n > 0, err
}

// SetExercise stores the task a finished background import created.
func (r *Repository) SetExercise(ctx context.Context, session string, author, exercise uuid.UUID) error {
	_, err := r.q.SetAssistantSaveExercise(ctx, postgres.SetAssistantSaveExerciseParams{SessionID: session, AuthorID: author, ExerciseID: exercise})
	return err
}

// SetNotified records that the brain was told the task.
func (r *Repository) SetNotified(ctx context.Context, session string, author uuid.UUID) error {
	_, err := r.q.SetAssistantSaveNotified(ctx, postgres.SetAssistantSaveNotifiedParams{SessionID: session, AuthorID: author})
	return err
}

// Delete removes the save (a failed import), so the next save starts again.
func (r *Repository) Delete(ctx context.Context, session string, author uuid.UUID) error {
	_, err := r.q.DeleteAssistantSave(ctx, postgres.DeleteAssistantSaveParams{SessionID: session, AuthorID: author})
	return err
}

// Unsettled lists the saves newer than since that still wait for an import to finish or for the brain to be told.
func (r *Repository) Unsettled(ctx context.Context, since time.Time, limit int) ([]assistantModel.Save, error) {
	rows, err := r.q.ListUnsettledAssistantSaves(ctx, postgres.ListUnsettledAssistantSavesParams{Since: since, MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.Save, 0, len(rows))
	for _, row := range rows {
		out = append(out, assistantModel.Save{Session: row.SessionID, Author: row.AuthorID, ExerciseID: idOf(row.ExerciseID), JobID: idOf(row.JobID), Notified: row.Notified})
	}
	return out, nil
}
