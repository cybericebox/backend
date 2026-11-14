// Package assistantSaveRepo remembers which catalog task a finished assistant session was saved as.
package assistantSaveRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
)

type Queries interface {
	GetAssistantSave(context.Context, postgres.GetAssistantSaveParams) (uuid.UUID, error)
	CreateAssistantSave(context.Context, postgres.CreateAssistantSaveParams) (int64, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// Get is the task the author already saved the session as; found is false when none.
func (r *Repository) Get(ctx context.Context, session string, author uuid.UUID) (id uuid.UUID, found bool, err error) {
	id, err = r.q.GetAssistantSave(ctx, postgres.GetAssistantSaveParams{SessionID: session, AuthorID: author})
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return id, true, nil
}

// Put records the import. It reports false when a row for the session and author is already there (it stays).
func (r *Repository) Put(ctx context.Context, session string, author, exercise uuid.UUID, at time.Time) (bool, error) {
	n, err := r.q.CreateAssistantSave(ctx, postgres.CreateAssistantSaveParams{SessionID: session, AuthorID: author, ExerciseID: exercise, CreatedAt: at})
	return n > 0, err
}
