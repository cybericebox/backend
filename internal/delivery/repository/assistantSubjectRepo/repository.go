// Package assistantSubjectRepo stores the random pseudonym a user has at cyber-brain.
package assistantSubjectRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
)

type Queries interface {
	GetAssistantSubject(context.Context, uuid.UUID) (uuid.UUID, error)
	CreateAssistantSubject(context.Context, postgres.CreateAssistantSubjectParams) (int64, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// Get is the user's pseudonym; found is false when none was made yet.
func (r *Repository) Get(ctx context.Context, user uuid.UUID) (subject uuid.UUID, found bool, err error) {
	subject, err = r.q.GetAssistantSubject(ctx, user)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return subject, true, nil
}

// Create stores a pseudonym for the user. It reports false when the user already has one (it stays).
func (r *Repository) Create(ctx context.Context, user, subject uuid.UUID, at time.Time) (bool, error) {
	n, err := r.q.CreateAssistantSubject(ctx, postgres.CreateAssistantSubjectParams{UserID: user, Subject: subject, CreatedAt: at})
	return n > 0, err
}
