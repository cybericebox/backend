package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/delivery/repository/draftKeyRepo"
	"github.com/cybericebox/backend/internal/model/rbac"
	"github.com/cybericebox/backend/internal/testhelpers"
)

func draftID(n int) string { return fmt.Sprintf("%043d", n) }

// TestDraftKeys_TouchIsKeyedOnTheOwnerAndTheLifetime: the key of a person opens for that person only and only while it
// is not stale; a touch moves its last use.
func TestDraftKeys_TouchIsKeyedOnTheOwnerAndTheLifetime(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	repo := draftKeyRepo.New(db.Queries)
	alice := newInboxUser(t, db.Queries, "Alice", "A.", rbac.RoleUser)
	bob := newInboxUser(t, db.Queries, "Bob", "B.", rbac.RoleUser)
	now := time.Now()
	stale := now.Add(-48 * time.Hour)

	require.NoError(t, repo.Create(ctx, draftID(1), alice, "sealed-1", now.Add(-time.Hour), 20))

	sealed, found, err := repo.Touch(ctx, draftID(1), alice, now, stale)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "sealed-1", sealed)

	_, found, err = repo.Touch(ctx, draftID(1), bob, now, stale)
	require.NoError(t, err)
	require.False(t, found, "the key of another person is never found")

	// Not used for longer than the lifetime: not found, and the read of its owner deletes it.
	require.NoError(t, repo.Create(ctx, draftID(2), alice, "sealed-2", now.Add(-72*time.Hour), 20))
	_, found, err = repo.Touch(ctx, draftID(2), alice, now, stale)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, repo.DeleteStaleOf(ctx, alice, stale))
	var left int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM user_draft_keys WHERE user_id = $1`, alice).Scan(&left))
	require.Equal(t, 1, left)

	// A reseal replaces the stored key only while the row still holds the value it was read as.
	require.NoError(t, repo.Reseal(ctx, draftID(1), alice, "not-what-is-stored", "sealed-x"))
	require.NoError(t, repo.Reseal(ctx, draftID(1), bob, "sealed-1", "sealed-x"))
	sealed, _, err = repo.Touch(ctx, draftID(1), alice, now, stale)
	require.NoError(t, err)
	require.Equal(t, "sealed-1", sealed)
	require.NoError(t, repo.Reseal(ctx, draftID(1), alice, "sealed-1", "sealed-new"))
	sealed, _, err = repo.Touch(ctx, draftID(1), alice, now, stale)
	require.NoError(t, err)
	require.Equal(t, "sealed-new", sealed)

	// The touch moved the last use, so the key outlives the bound it was created under.
	_, found, err = repo.Touch(ctx, draftID(1), alice, now, now.Add(-30*time.Minute))
	require.NoError(t, err)
	require.True(t, found, "the last use was moved to now")
}

// TestDraftKeys_DeletesAreScopedToTheBrowserAndThePerson: sign-out deletes only the key of this browser, signing out the
// other sessions keeps only it, the cleanup deletes only stale keys, the cap keeps the most recently used keys and a
// deleted user takes the keys along.
func TestDraftKeys_DeletesAreScopedToTheBrowserAndThePerson(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	repo := draftKeyRepo.New(db.Queries)
	alice := newInboxUser(t, db.Queries, "Alice", "A.", rbac.RoleUser)
	bob := newInboxUser(t, db.Queries, "Bob", "B.", rbac.RoleUser)
	now := time.Now()
	count := func(where string, args ...any) int {
		var n int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM user_draft_keys WHERE `+where, args...).Scan(&n))
		return n
	}
	for i := 1; i <= 3; i++ {
		require.NoError(t, repo.Create(ctx, draftID(i), alice, "a", now.Add(-time.Duration(i)*time.Minute), 20))
	}
	require.NoError(t, repo.Create(ctx, draftID(10), bob, "b", now, 20))

	// Bob's sign-out with a cookie that names Alice's key deletes nothing.
	require.NoError(t, repo.Delete(ctx, draftID(1), bob))
	require.Equal(t, 3, count("user_id = $1", alice))
	// Alice signs out in browser 1: only that key goes.
	require.NoError(t, repo.Delete(ctx, draftID(1), alice))
	require.Equal(t, 0, count("id = $1", draftID(1)))
	require.Equal(t, 2, count("user_id = $1", alice))
	require.Equal(t, 1, count("user_id = $1", bob))

	// Alice signs out her other sessions from browser 2.
	require.NoError(t, repo.DeleteAllExcept(ctx, alice, draftID(2)))
	require.Equal(t, 1, count("user_id = $1", alice))
	require.Equal(t, 1, count("id = $1", draftID(2)))
	require.Equal(t, 1, count("user_id = $1", bob))

	// The cap keeps the most recently used keys.
	for i := 20; i < 25; i++ {
		require.NoError(t, repo.Create(ctx, draftID(i), alice, "a", now.Add(time.Duration(i)*time.Second), 3))
	}
	require.Equal(t, 3, count("user_id = $1", alice))
	require.Equal(t, 3, count("user_id = $1 AND id IN ($2, $3, $4)", alice, draftID(22), draftID(23), draftID(24)))

	// The periodic cleanup deletes the stale keys of everyone and nothing else.
	_, err := db.Pool.Exec(ctx, `UPDATE user_draft_keys SET last_used_at = now() - interval '3 days' WHERE id = $1`, draftID(10))
	require.NoError(t, err)
	n, err := repo.DeleteStale(ctx, now.Add(-48*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, 0, count("user_id = $1", bob))
	require.Equal(t, 3, count("user_id = $1", alice))

	// A deleted user takes the keys along.
	_, err = db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, alice)
	require.NoError(t, err)
	require.Equal(t, 0, count("true"))
}
