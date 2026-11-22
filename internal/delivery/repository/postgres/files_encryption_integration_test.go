package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	"github.com/cybericebox/backend/internal/testhelpers"
)

// These tests need a database (Docker). Run them with:
//   GOWORK=off go test -timeout 300s -run 'FilesEncryption' ./internal/delivery/repository/postgres/

var filesNow = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func newFile(t *testing.T, db *testhelpers.TestDB, hash string, at time.Time, requireExisting bool, objectKey, wrappedKey string) (postgres.CreateFileRow, error) {
	t.Helper()
	return db.Queries.CreateFile(context.Background(), postgres.CreateFileParams{
		ID: uuid.Must(uuid.NewV7()), Name: "f", ContentType: "t", SizeBytes: 3, ContentHash: hash, CreatedAt: at,
		RequireExisting: requireExisting, ObjectKey: objectKey, WrappedKey: wrappedKey,
	})
}

func blobRow(t *testing.T, db *testhelpers.TestDB, hash string) (refCount int, touched time.Time, objectKey, wrapped string, found bool) {
	t.Helper()
	err := db.Pool.QueryRow(context.Background(), `SELECT ref_count, touched_at, object_key, wrapped_key FROM file_blobs WHERE content_hash = $1`, hash).
		Scan(&refCount, &touched, &objectKey, &wrapped)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, "", "", false
	}
	require.NoError(t, err)
	return refCount, touched, objectKey, wrapped, true
}

func fileCount(t *testing.T, db *testhelpers.TestDB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM files`).Scan(&n))
	return n
}

// The whole CreateFile statement: a new blob, a dedup hit, a reuse that must find its blob, and a reuse whose blob is gone.
func TestFilesEncryptionCreateFileStatement(t *testing.T) {
	db := testhelpers.SetupTestDB(t)

	// A new blob: it is created with the object and the wrapped key of the caller.
	first, err := newFile(t, db, "hash-a", filesNow, false, "media/enc/a", "wrapped-a")
	require.NoError(t, err)
	require.Equal(t, "media/enc/a", first.BlobObjectKey)
	require.Equal(t, "wrapped-a", first.BlobWrappedKey)
	refs, _, object, wrapped, _ := blobRow(t, db, "hash-a")
	require.Equal(t, 1, refs)
	require.Equal(t, "media/enc/a", object)
	require.Equal(t, "wrapped-a", wrapped)

	// A dedup hit: the blob keeps the first writer's object and key, and the answer says so; the count and touched_at move.
	later := filesNow.Add(time.Hour)
	second, err := newFile(t, db, "hash-a", later, false, "media/enc/other", "wrapped-other")
	require.NoError(t, err)
	require.Equal(t, "media/enc/a", second.BlobObjectKey)
	require.Equal(t, "wrapped-a", second.BlobWrappedKey)
	refs, touched, object, wrapped, _ := blobRow(t, db, "hash-a")
	require.Equal(t, 2, refs)
	require.True(t, touched.Equal(later))
	require.Equal(t, "media/enc/a", object)
	require.Equal(t, "wrapped-a", wrapped)

	// A reuse (nothing was stored) of a blob that exists: only the count moves, and the standing keys are returned.
	third, err := newFile(t, db, "hash-a", later.Add(time.Hour), true, "", "")
	require.NoError(t, err)
	require.Equal(t, "media/enc/a", third.BlobObjectKey)
	require.Equal(t, "wrapped-a", third.BlobWrappedKey)
	refs, _, _, _, _ = blobRow(t, db, "hash-a")
	require.Equal(t, 3, refs)
	require.Equal(t, 3, fileCount(t, db))

	// A reuse of a blob that is gone (the collector removed it): nothing is written, not even an empty blob.
	_, err = newFile(t, db, "hash-gone", later, true, "", "")
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, _, _, _, found := blobRow(t, db, "hash-gone")
	require.False(t, found, "a blob must not come back as an empty row")
	require.Equal(t, 3, fileCount(t, db), "no file row for a refused reuse")

	// A plain blob (public image) is the same statement with empty keys.
	plain, err := newFile(t, db, "hash-p", filesNow, false, "", "")
	require.NoError(t, err)
	require.Empty(t, plain.BlobObjectKey)
	require.Empty(t, plain.BlobWrappedKey)
}

func insertBlob(t *testing.T, db *testhelpers.TestDB, hash string, refCount int, touched time.Time, objectKey, wrapped string) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(), `INSERT INTO file_blobs (content_hash, size_bytes, ref_count, created_at, touched_at, object_key, wrapped_key)
VALUES ($1, 3, $2, $3, $3, $4, $5)`, hash, refCount, touched, objectKey, wrapped)
	require.NoError(t, err)
}

// DeleteEncryptedBlob takes the row of an unreferenced, long untouched, encrypted blob and says which object it held; every
// guard refuses (no row is deleted, no error).
func TestFilesEncryptionDeleteEncryptedBlobGuards(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	old, cutoff := filesNow.Add(-48*time.Hour), filesNow.Add(-24*time.Hour)

	insertBlob(t, db, "ok", 0, old, "media/enc/ok", "w")
	insertBlob(t, db, "referenced", 1, old, "media/enc/ref", "w")
	insertBlob(t, db, "fresh", 0, filesNow, "media/enc/fresh", "w")
	insertBlob(t, db, "plain", 0, old, "", "")
	insertBlob(t, db, "negative", -1, old, "media/enc/neg", "w")

	key, err := db.Queries.DeleteEncryptedBlob(ctx, postgres.DeleteEncryptedBlobParams{ContentHash: "ok", TouchedBefore: cutoff})
	require.NoError(t, err)
	require.Equal(t, "media/enc/ok", key)
	_, _, _, _, found := blobRow(t, db, "ok")
	require.False(t, found)

	for _, hash := range []string{"referenced", "fresh", "plain", "ok", "missing"} {
		_, err = db.Queries.DeleteEncryptedBlob(ctx, postgres.DeleteEncryptedBlobParams{ContentHash: hash, TouchedBefore: cutoff})
		require.ErrorIs(t, err, pgx.ErrNoRows, hash)
	}
	for _, hash := range []string{"referenced", "fresh", "plain"} {
		_, _, _, _, found = blobRow(t, db, hash)
		require.True(t, found, "%s must stay", hash)
	}
	// A blob whose count went below zero is still unreferenced.
	key, err = db.Queries.DeleteEncryptedBlob(ctx, postgres.DeleteEncryptedBlobParams{ContentHash: "negative", TouchedBefore: cutoff})
	require.NoError(t, err)
	require.Equal(t, "media/enc/neg", key)

	// The plain-blob delete refuses an encrypted blob (the pass may have moved it since the listing) and takes a plain one.
	insertBlob(t, db, "moved", 0, old, "media/enc/moved", "w")
	n, err := db.Queries.DeleteBlob(ctx, "moved")
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = db.Queries.DeleteBlob(ctx, "plain")
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// The listing says which orphans are encrypted.
	insertBlob(t, db, "orphan-enc", 0, old, "media/enc/o", "w")
	insertBlob(t, db, "orphan-plain", 0, old, "", "")
	rows, err := db.Queries.ListOrphanBlobs(ctx, cutoff)
	require.NoError(t, err)
	encrypted := map[string]bool{}
	for _, r := range rows {
		encrypted[r.ContentHash] = r.Encrypted
	}
	require.True(t, encrypted["orphan-enc"])
	require.False(t, encrypted["orphan-plain"])
}

// The list of objects that could not be removed: adding twice keeps one entry, the listing is by age, a cutoff leaves the
// younger ones, and an entry can be forgotten.
func TestFilesEncryptionObjectRemovals(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	add := func(key string, at time.Time) {
		require.NoError(t, db.Queries.AddObjectRemoval(ctx, postgres.AddObjectRemovalParams{ObjectKey: key, CreatedAt: at}))
	}
	add("media/enc/b", filesNow.Add(-2*time.Hour))
	add("media/enc/a", filesNow.Add(-3*time.Hour))
	add("media/enc/b", filesNow) // already listed: the entry keeps its first time
	add("media/enc/young", filesNow)

	list := func(before time.Time) []string {
		keys, err := db.Queries.ListObjectRemovals(ctx, postgres.ListObjectRemovalsParams{CreatedAt: before, Limit: 10})
		require.NoError(t, err)
		return keys
	}
	require.Equal(t, []string{"media/enc/a", "media/enc/b"}, list(filesNow.Add(-time.Hour)), "oldest first, the young one is not due")
	require.Equal(t, []string{"media/enc/a"}, list(filesNow.Add(-150*time.Minute)))
	limited, err := db.Queries.ListObjectRemovals(ctx, postgres.ListObjectRemovalsParams{CreatedAt: filesNow.Add(time.Hour), Limit: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"media/enc/a"}, limited)

	require.NoError(t, db.Queries.DeleteObjectRemoval(ctx, "media/enc/a"))
	require.Equal(t, []string{"media/enc/b"}, list(filesNow.Add(-time.Hour)))
}

// Of several writers that want to move one plain blob to an encrypted object, exactly one wins; the others are refused.
func TestFilesEncryptionAdoptBlobEncryptionIsACompareAndSwap(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	insertBlob(t, db, "plain-blob", 1, filesNow, "", "")

	const writers = 8
	var wg sync.WaitGroup
	results := make([]int64, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := db.Queries.AdoptBlobEncryption(ctx, postgres.AdoptBlobEncryptionParams{
				ContentHash: "plain-blob", ObjectKey: "media/enc/w" + string(rune('a'+i)), WrappedKey: "wrapped-" + string(rune('a'+i)),
			})
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
			results[i] = n
		}()
	}
	wg.Wait()
	winners, winner := 0, -1
	for i, n := range results {
		if n == 1 {
			winners++
			winner = i
		}
	}
	require.Equal(t, 1, winners, "exactly one writer moves the blob: %v", results)
	_, _, object, wrapped, _ := blobRow(t, db, "plain-blob")
	require.Equal(t, "media/enc/w"+string(rune('a'+winner)), object)
	require.Equal(t, "wrapped-"+string(rune('a'+winner)), wrapped)
}

// The adopt of a legacy avatar against a RemoveAvatar that is in flight on another connection. RemoveAvatar's first write
// clears users.picture; while that transaction is open the adopt must wait on the user row (the FOR UPDATE inside the
// INSERT ... SELECT), and when it commits the adopt sees the cleared picture and publishes nothing.
func TestFilesEncryptionAdoptLegacyAvatarWaitsForARemoval(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	userID := uuid.Must(uuid.NewV7())
	_, err := db.Pool.Exec(ctx, `INSERT INTO users (id, email, first_name, last_name, role, status, email_confirmed, last_seen, created_at, picture)
VALUES ($1, 'owner@x.test', 'a', 'b', 'user', 'active', true, $2, $2, '/api/auth/avatar/x?v=1')`, userID, filesNow)
	require.NoError(t, err)
	file, err := newFile(t, db, "avatar-hash", filesNow, false, "media/enc/av", "w")
	require.NoError(t, err)

	// Connection 1: the owner removes the avatar. Its write is done, its transaction is not committed yet.
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, err = tx.Exec(ctx, `UPDATE users SET picture = '' WHERE id = $1`, userID)
	require.NoError(t, err)

	// Connection 2: the pass adopts the legacy avatar. It must block on the row, not decide on the old picture.
	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, adoptErr := db.Queries.AdoptLegacyAvatar(ctx, postgres.AdoptLegacyAvatarParams{UserID: userID, FileID: file.ID})
		done <- result{n, adoptErr}
	}()

	// It is waiting on a lock (checked in the server, not by sleeping): a backend that waits on a lock and runs the adopt.
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
WHERE wait_event_type = 'Lock' AND query LIKE '%file_references%'`).Scan(&waiting))
		return waiting == 1
	}, 20*time.Second, 50*time.Millisecond, "the adopt did not wait for the user row")
	select {
	case r := <-done:
		t.Fatalf("the adopt finished while the removal was open: %+v", r)
	default:
	}

	require.NoError(t, tx.Commit(ctx))
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Zero(t, r.n, "the avatar was removed: nothing may be published")
	case <-ctx.Done():
		t.Fatal("the adopt did not finish after the removal committed")
	}
	var refs int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM file_references WHERE ref_type = 'user_avatar' AND ref_id = $1`, userID).Scan(&refs))
	require.Zero(t, refs)

	// The other order: the adopt commits first, the removal comes after and takes the reference away.
	other := uuid.Must(uuid.NewV7())
	_, err = db.Pool.Exec(ctx, `INSERT INTO users (id, email, first_name, last_name, role, status, email_confirmed, last_seen, created_at, picture)
VALUES ($1, 'other@x.test', 'a', 'b', 'user', 'active', true, $2, $2, '/api/auth/avatar/y?v=1')`, other, filesNow)
	require.NoError(t, err)
	n, err := db.Queries.AdoptLegacyAvatar(ctx, postgres.AdoptLegacyAvatarParams{UserID: other, FileID: file.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	_, err = db.Pool.Exec(ctx, `UPDATE users SET picture = '' WHERE id = $1`, other)
	require.NoError(t, err)
	require.NoError(t, db.Queries.ReplaceFileReferences(ctx, postgres.ReplaceFileReferencesParams{RefType: "user_avatar", RefID: other, FileIds: []uuid.UUID{}}))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM file_references WHERE ref_type = 'user_avatar' AND ref_id = $1`, other).Scan(&refs))
	require.Zero(t, refs)
}

// Migration 0245: the down migration refuses while an encrypted blob (or upload) exists and changes nothing; with none it
// drops the columns and the table; the up migration then applies again.
func TestFilesEncryptionMigration(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	up := readMigration(t, "0245_files_encryption.up.sql")
	down := readMigration(t, "0245_files_encryption.down.sql")

	columns := func(table string) map[string]bool {
		rows, err := db.Pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = $1`, table)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			out[name] = true
		}
		require.NoError(t, rows.Err())
		return out
	}
	tableExists := func(name string) bool {
		var ok bool
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok))
		return ok
	}
	require.True(t, columns("file_blobs")["wrapped_key"] && columns("file_blobs")["object_key"])
	require.True(t, columns("media_uploads")["wrapped_key"])
	require.True(t, tableExists("object_removals"))

	// Defaults of existing rows: empty, which means plain.
	insertBlob(t, db, "plain-row", 1, filesNow, "", "")
	insertBlob(t, db, "encrypted-row", 1, filesNow, "media/enc/x", "wrapped")

	_, err := db.Pool.Exec(ctx, down)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be reverted")
	require.True(t, columns("file_blobs")["wrapped_key"], "a refused down migration changes nothing")
	require.True(t, tableExists("object_removals"))
	_, _, _, wrapped, found := blobRow(t, db, "encrypted-row")
	require.True(t, found)
	require.Equal(t, "wrapped", wrapped)

	// An encrypted upload in progress refuses it too.
	_, err = db.Pool.Exec(ctx, `DELETE FROM file_blobs WHERE content_hash = 'encrypted-row'`)
	require.NoError(t, err)
	userID := uuid.Must(uuid.NewV7())
	_, err = db.Pool.Exec(ctx, `INSERT INTO users (id, email, first_name, last_name, role, status, email_confirmed, last_seen, created_at)
VALUES ($1, 'u@x.test', 'a', 'b', 'user', 'active', true, $2, $2)`, userID, filesNow)
	require.NoError(t, err)
	uploadID := uuid.Must(uuid.NewV7())
	_, err = db.Pool.Exec(ctx, `INSERT INTO media_uploads (id, created_by, name, content_type, size_bytes, chunk_bytes, created_at, expires_at, purpose, wrapped_key)
VALUES ($1, $2, 'u', 't', 10, 4, $3, $3, '', 'wrapped')`, uploadID, userID, filesNow)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, down)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be reverted")

	// Nothing encrypted is left: the down migration applies, and the up migration applies again.
	_, err = db.Pool.Exec(ctx, `DELETE FROM media_uploads WHERE id = $1`, uploadID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, down)
	require.NoError(t, err)
	require.False(t, columns("file_blobs")["wrapped_key"])
	require.False(t, columns("file_blobs")["object_key"])
	require.False(t, columns("media_uploads")["wrapped_key"])
	require.False(t, tableExists("object_removals"))

	_, err = db.Pool.Exec(ctx, up)
	require.NoError(t, err)
	require.True(t, columns("file_blobs")["wrapped_key"] && columns("file_blobs")["object_key"])
	require.True(t, columns("media_uploads")["wrapped_key"])
	require.True(t, tableExists("object_removals"))
	_, _, object, wrapped, found := blobRow(t, db, "plain-row")
	require.True(t, found)
	require.Empty(t, object)
	require.Empty(t, wrapped)
}
