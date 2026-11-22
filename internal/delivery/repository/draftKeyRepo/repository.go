// Package draftKeyRepo stores the sealed draft keys of the browsers of a person.
package draftKeyRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
)

// Queries is the narrow slice of the sqlc Querier this repository needs.
type Queries interface {
	TouchUserDraftKey(ctx context.Context, arg postgres.TouchUserDraftKeyParams) (string, error)
	InsertUserDraftKey(ctx context.Context, arg postgres.InsertUserDraftKeyParams) error
	TrimUserDraftKeys(ctx context.Context, arg postgres.TrimUserDraftKeysParams) (int64, error)
	DeleteUserDraftKey(ctx context.Context, arg postgres.DeleteUserDraftKeyParams) (int64, error)
	DeleteUserDraftKeysExcept(ctx context.Context, arg postgres.DeleteUserDraftKeysExceptParams) (int64, error)
	DeleteAllUserDraftKeys(ctx context.Context, userID uuid.UUID) (int64, error)
	DeleteStaleUserDraftKeys(ctx context.Context, arg postgres.DeleteStaleUserDraftKeysParams) (int64, error)
	DeleteStaleDraftKeys(ctx context.Context, staleBefore time.Time) (int64, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// Touch returns the sealed key id of the person when it was used after staleBefore, and moves its last use to at.
// found is false for an unknown id, the id of another person and a stale key.
func (r *Repository) Touch(ctx context.Context, id string, userID uuid.UUID, at, staleBefore time.Time) (sealed string, found bool, err error) {
	sealed, err = r.q.TouchUserDraftKey(ctx, postgres.TouchUserDraftKeyParams{
		ID: id, UserID: userID, UsedAt: at, StaleBefore: staleBefore,
	})
	if repositoryTools.IsObjectNotFoundError(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return sealed, true, nil
}

// Create stores a new sealed key and keeps at most keep keys of the person (the most recently used).
func (r *Repository) Create(ctx context.Context, id string, userID uuid.UUID, sealed string, at time.Time, keep int) error {
	if err := r.q.InsertUserDraftKey(ctx, postgres.InsertUserDraftKeyParams{
		ID: id, UserID: userID, KeyCiphertext: sealed, CreatedAt: at,
	}); err != nil {
		return err
	}
	_, err := r.q.TrimUserDraftKeys(ctx, postgres.TrimUserDraftKeysParams{UserID: userID, Keep: int64(keep)})
	return err
}

// Delete deletes the key id of the person (none when it is another person's).
func (r *Repository) Delete(ctx context.Context, id string, userID uuid.UUID) error {
	_, err := r.q.DeleteUserDraftKey(ctx, postgres.DeleteUserDraftKeyParams{ID: id, UserID: userID})
	return err
}

// DeleteAllExcept deletes every key of the person but keepID.
func (r *Repository) DeleteAllExcept(ctx context.Context, userID uuid.UUID, keepID string) error {
	_, err := r.q.DeleteUserDraftKeysExcept(ctx, postgres.DeleteUserDraftKeysExceptParams{UserID: userID, KeepID: keepID})
	return err
}

// DeleteAll deletes every key of the person.
func (r *Repository) DeleteAll(ctx context.Context, userID uuid.UUID) error {
	_, err := r.q.DeleteAllUserDraftKeys(ctx, userID)
	return err
}

// DeleteStaleOf deletes the keys of the person last used at or before staleBefore.
func (r *Repository) DeleteStaleOf(ctx context.Context, userID uuid.UUID, staleBefore time.Time) error {
	_, err := r.q.DeleteStaleUserDraftKeys(ctx, postgres.DeleteStaleUserDraftKeysParams{UserID: userID, StaleBefore: staleBefore})
	return err
}

// DeleteStale deletes every key last used at or before staleBefore and returns how many went.
func (r *Repository) DeleteStale(ctx context.Context, staleBefore time.Time) (int64, error) {
	return r.q.DeleteStaleDraftKeys(ctx, staleBefore)
}
