package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	"github.com/cybericebox/daemon/internal/testhelpers"
)

func TestMedia_RefcountAndGC(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	mk := func(hash string, at time.Time) postgres.File {
		row, err := db.Queries.CreateFile(ctx, postgres.CreateFileParams{
			ID: uuid.Must(uuid.NewV7()), Name: "f", ContentType: "t",
			SizeBytes: 3, ContentHash: hash, CreatedAt: at,
		})
		if err != nil {
			t.Fatalf("CreateFile: %v", err)
		}
		return postgres.File{ID: row.ID, Name: row.Name, ContentType: row.ContentType, SizeBytes: row.SizeBytes,
			ContentHash: row.ContentHash, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy}
	}

	f1 := mk("hash-a", now.Add(-48*time.Hour))
	f2 := mk("hash-a", now.Add(-48*time.Hour)) // same content → same blob, refcount 2
	f3 := mk("hash-b", now)                    // fresh: inside grace window

	// Reference f2 so GC must keep it.
	refID := uuid.Must(uuid.NewV7())
	if err := db.Queries.ReplaceFileReferences(ctx, postgres.ReplaceFileReferencesParams{
		RefType: "exercise_version", RefID: refID, FileIds: []uuid.UUID{f2.ID},
	}); err != nil {
		t.Fatalf("ReplaceFileReferences: %v", err)
	}

	// GetFileReferenceIDs is the read path GetAvatar (and any other reference
	// consumer) uses to resolve an owner's current file set.
	ids, err := db.Queries.GetFileReferenceIDs(ctx, postgres.GetFileReferenceIDsParams{
		RefType: "exercise_version", RefID: refID,
	})
	if err != nil || len(ids) != 1 || ids[0] != f2.ID {
		t.Fatalf("GetFileReferenceIDs: ids=%v err=%v", ids, err)
	}
	// An owner with no references at all yields an empty slice, not an error —
	// repositories don't validate on read.
	unreferencedID := uuid.Must(uuid.NewV7())
	ids, err = db.Queries.GetFileReferenceIDs(ctx, postgres.GetFileReferenceIDsParams{
		RefType: "exercise_version", RefID: unreferencedID,
	})
	if err != nil || len(ids) != 0 {
		t.Fatalf("GetFileReferenceIDs for unreferenced owner: ids=%v err=%v", ids, err)
	}

	// GC pass: only f1 (old + unreferenced) must go.
	if _, err := db.Queries.DeleteUnreferencedFiles(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("DeleteUnreferencedFiles: %v", err)
	}
	if _, err := db.Queries.GetFileByID(ctx, f1.ID); err == nil {
		t.Fatal("f1 must be deleted")
	}
	if _, err := db.Queries.GetFileByID(ctx, f2.ID); err != nil {
		t.Fatal("referenced f2 must survive")
	}
	if _, err := db.Queries.GetFileByID(ctx, f3.ID); err != nil {
		t.Fatal("fresh f3 must survive the grace window")
	}

	// hash-a refcount dropped 2→1 → not orphaned; drop the reference and GC again.
	if _, err := db.Queries.DeleteFileReferences(ctx, postgres.DeleteFileReferencesParams{
		RefType: "exercise_version", RefID: refID,
	}); err != nil {
		t.Fatalf("DeleteFileReferences: %v", err)
	}
	if _, err := db.Queries.DeleteUnreferencedFiles(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("second GC: %v", err)
	}
	graceCutoff := now.Add(-24 * time.Hour)
	orphans, err := db.Queries.ListOrphanBlobs(ctx, graceCutoff)
	if err != nil || len(orphans) != 1 || orphans[0].ContentHash != "hash-a" {
		t.Fatalf("orphans: %v err=%v", orphans, err)
	}
	if n, err := db.Queries.DeleteBlob(ctx, "hash-a"); err != nil || n != 1 {
		t.Fatalf("DeleteBlob: n=%d err=%v", n, err)
	}

	// --- Finding 4 regression: ListOrphanBlobs must also gate on the blob's
	// OWN touched_at, not just ref_count <= 0. The TOCTOU it guards against:
	// GC lists a blob as orphan, a concurrent dedup upload of the exact same
	// content bumps ref_count (and touches the blob) a moment later, and
	// GC's unconditional storage.Remove for the already-fetched orphan entry
	// still deletes the now-live S3 object. CreateFile always keeps
	// ref_count positive on a fresh call, so "ref_count <= 0 with a recent
	// touch" can't be reached through the public Create/DeleteUnreferenced
	// flow alone — it is set up directly here to test ListOrphanBlobs'
	// filter (the actual regression surface) in isolation.
	hashD := "hash-d"
	_ = mk(hashD, now.Add(-48*time.Hour)) // old, unreferenced → genuine orphan
	if _, err = db.Queries.DeleteUnreferencedFiles(ctx, graceCutoff); err != nil {
		t.Fatalf("GC for hash-d: %v", err)
	}
	// Simulate a concurrent touch (e.g. a dedup upload racing GC's listing)
	// landing on the blob without changing its now-zero ref_count.
	if _, err = db.Pool.Exec(ctx, `UPDATE file_blobs SET touched_at = $1 WHERE content_hash = $2`, now, hashD); err != nil {
		t.Fatalf("simulate touch on hash-d: %v", err)
	}
	orphans, err = db.Queries.ListOrphanBlobs(ctx, graceCutoff)
	if err != nil {
		t.Fatalf("ListOrphanBlobs after touch: %v", err)
	}
	for _, h := range orphans {
		if h.ContentHash == hashD {
			t.Fatalf("freshly touched orphan blob %q must not be listed for GC removal: %v", hashD, orphans)
		}
	}
	// Once the touch itself ages past the grace window with no further
	// activity, the same blob is genuinely stale and must be listed again.
	orphans, err = db.Queries.ListOrphanBlobs(ctx, now.Add(48*time.Hour))
	if err != nil || len(orphans) != 1 || orphans[0].ContentHash != hashD {
		t.Fatalf("stale-touch orphan blob must be listed once its own grace expires: orphans=%v err=%v", orphans, err)
	}
	if n, err := db.Queries.DeleteBlob(ctx, hashD); err != nil || n != 1 {
		t.Fatalf("DeleteBlob(hash-d): n=%d err=%v", n, err)
	}
}

// TestMedia_GetFileReferenceIDs_OrderedNewestFirst is the regression test for
// the avatar-migration review finding: GetFileReferenceIDs had no ORDER BY, so
// a concurrent double-upload leaving two references for the same owner (e.g.
// user_avatar) made GetAvatar's fileIDs[0] pick non-deterministic. The query
// now orders by files.created_at DESC, file_id DESC — newest file wins, with
// a deterministic tiebreak when two files share a timestamp.
func TestMedia_GetFileReferenceIDs_OrderedNewestFirst(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	mk := func(hash string, at time.Time) postgres.File {
		row, err := db.Queries.CreateFile(ctx, postgres.CreateFileParams{
			ID: uuid.Must(uuid.NewV7()), Name: "f", ContentType: "t",
			SizeBytes: 3, ContentHash: hash, CreatedAt: at,
		})
		if err != nil {
			t.Fatalf("CreateFile: %v", err)
		}
		return postgres.File{ID: row.ID, Name: row.Name, ContentType: row.ContentType, SizeBytes: row.SizeBytes,
			ContentHash: row.ContentHash, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy}
	}

	older := mk("hash-old", now.Add(-time.Hour))
	newer := mk("hash-new", now)

	refID := uuid.Must(uuid.NewV7())
	// Two references coexisting for one owner is exactly the leftover state a
	// racing double-upload can produce (ReplaceFileReferences's diff-sync
	// reads its "keep" set before the other call's insert is visible); set
	// both up directly here to test GetFileReferenceIDs' ordering in
	// isolation.
	if err := db.Queries.ReplaceFileReferences(ctx, postgres.ReplaceFileReferencesParams{
		RefType: "user_avatar", RefID: refID, FileIds: []uuid.UUID{older.ID, newer.ID},
	}); err != nil {
		t.Fatalf("ReplaceFileReferences: %v", err)
	}

	ids, err := db.Queries.GetFileReferenceIDs(ctx, postgres.GetFileReferenceIDsParams{
		RefType: "user_avatar", RefID: refID,
	})
	if err != nil || len(ids) != 2 {
		t.Fatalf("GetFileReferenceIDs: ids=%v err=%v", ids, err)
	}
	if ids[0] != newer.ID {
		t.Fatalf("newest file must sort first (GetAvatar picks index 0): want %v, got %v", newer.ID, ids)
	}

	// Tiebreak: two files with the identical created_at must still sort
	// deterministically, by file_id DESC.
	tieAt := now.Add(time.Hour)
	tieA := mk("hash-tie-a", tieAt)
	tieB := mk("hash-tie-b", tieAt)
	wantFirst, wantSecond := tieA.ID, tieB.ID
	if wantFirst.String() < wantSecond.String() {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	tieRefID := uuid.Must(uuid.NewV7())
	if err = db.Queries.ReplaceFileReferences(ctx, postgres.ReplaceFileReferencesParams{
		RefType: "user_avatar", RefID: tieRefID, FileIds: []uuid.UUID{tieA.ID, tieB.ID},
	}); err != nil {
		t.Fatalf("ReplaceFileReferences (tie): %v", err)
	}
	tieIDs, err := db.Queries.GetFileReferenceIDs(ctx, postgres.GetFileReferenceIDsParams{
		RefType: "user_avatar", RefID: tieRefID,
	})
	if err != nil || len(tieIDs) != 2 || tieIDs[0] != wantFirst || tieIDs[1] != wantSecond {
		t.Fatalf("equal-created_at tiebreak must be file_id DESC: want [%v %v], got %v (err=%v)", wantFirst, wantSecond, tieIDs, err)
	}
}

// The blob keeps the object and the wrapped key of whoever created it, a plain blob is moved once, and the encryption pass lists
// every plain blob except one that only public images use.
func TestEncryptedBlobQueries(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	create := func(hash, objectKey, wrapped string) postgres.CreateFileRow {
		row, err := db.Queries.CreateFile(ctx, postgres.CreateFileParams{
			ID: uuid.Must(uuid.NewV7()), Name: "f", ContentType: "t", SizeBytes: 3, ContentHash: hash, CreatedAt: now,
			ObjectKey: objectKey, WrappedKey: wrapped,
		})
		if err != nil {
			t.Fatalf("CreateFile: %v", err)
		}
		return row
	}
	reference := func(kind string, fileID uuid.UUID) {
		if err := db.Queries.ReplaceFileReferences(ctx, postgres.ReplaceFileReferencesParams{
			RefType: kind, RefID: uuid.Must(uuid.NewV7()), FileIds: []uuid.UUID{fileID},
		}); err != nil {
			t.Fatalf("ReplaceFileReferences: %v", err)
		}
	}

	first := create("hash-a", "media/enc/a", "wrapped-1")
	second := create("hash-a", "media/enc/b", "wrapped-2") // a dedup hit: the blob keeps the first writer's object and key
	if first.BlobObjectKey != "media/enc/a" || second.BlobObjectKey != "media/enc/a" || second.BlobWrappedKey != "wrapped-1" {
		t.Fatalf("the standing blob must be returned to the later writer: %+v / %+v", first, second)
	}

	create("hash-p", "", "") // plain, no owner yet
	logo := create("hash-l", "", "")
	reference("event_logo", logo.ID)
	shared := create("hash-m", "", "")
	reference("event_logo", shared.ID)
	reference("exercise_version", shared.ID) // a public image and a private file share these bytes

	public := []string{"event_logo", "event_favicon", "event_preview_picture", "event_content_image"}
	list := func(after string) []string {
		rows, err := db.Queries.ListPlainBlobs(ctx, postgres.ListPlainBlobsParams{After: after, PublicRefTypes: public, RowLimit: 10})
		if err != nil {
			t.Fatalf("ListPlainBlobs: %v", err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.ContentHash)
		}
		return out
	}
	if got := list(""); len(got) != 2 || got[0] != "hash-m" || got[1] != "hash-p" {
		t.Fatalf("the pass must list the shared and the unowned blob, not the logo or the encrypted one: %v", got)
	}
	if got := list("hash-m"); len(got) != 1 || got[0] != "hash-p" {
		t.Fatalf("after the cursor: %v", got)
	}

	moved, err := db.Queries.AdoptBlobEncryption(ctx, postgres.AdoptBlobEncryptionParams{ContentHash: "hash-p", ObjectKey: "media/enc/p", WrappedKey: "wrapped-p"})
	if err != nil || moved != 1 {
		t.Fatalf("a plain blob moves once: %d %v", moved, err)
	}
	if moved, err = db.Queries.AdoptBlobEncryption(ctx, postgres.AdoptBlobEncryptionParams{ContentHash: "hash-p", ObjectKey: "media/enc/other", WrappedKey: "x"}); err != nil || moved != 0 {
		t.Fatalf("an encrypted blob must not be overwritten: %d %v", moved, err)
	}
	blob, err := db.Queries.GetFileBlob(ctx, "hash-p")
	if err != nil || blob.ObjectKey != "media/enc/p" || blob.WrappedKey != "wrapped-p" {
		t.Fatalf("blob %+v %v", blob, err)
	}
	if n, err := db.Queries.CountPlainBlobs(ctx); err != nil || n != 2 { // hash-l and hash-m
		t.Fatalf("plain blobs left: %d %v", n, err)
	}
}
