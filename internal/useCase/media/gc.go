package media

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/model"
)

// CleanupOrphanFiles is the GC pass: drop grace-expired unreferenced files,
// then physically remove blobs nothing points at. S3 removal precedes the row
// delete; a failed removal leaves the row for the next pass (at-least-once).
//
// ListOrphanBlobs is also grace-gated on file_blobs.touched_at (same GCGrace
// cutoff): without it, a concurrent dedup upload of the exact content GC just
// listed as orphan (Stat sees the blob, skips the copy, CreateFile bumps
// ref_count) can still lose the race against the unconditional storage.Remove
// below — the row survives (DeleteBlob's ref_count <= 0 guard refuses it) but
// the live S3 object is already gone. Requiring the blob to have gone
// untouched for the whole grace window means a fresh upload's touch (which
// CreateFile stamps unconditionally, on both the insert and the dedup-hit
// conflict path) keeps it out of this pass's candidate list entirely.
func (u *MediaUseCase) CleanupOrphanFiles(ctx context.Context) error {
	if !u.storageConfigured {
		return nil // nothing uploaded ever — nothing to collect
	}
	// Resumable uploads that waited too long for their next chunk go first, with the chunks stored so far.
	if err := u.CleanupExpiredUploads(ctx); err != nil {
		return err
	}
	cutoff := u.now().Add(-u.cfg.GCGrace)
	if err := u.files.DeleteUnreferenced(ctx, cutoff); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to delete unreferenced files").Err()
	}
	orphans, err := u.files.ListOrphanBlobs(ctx, cutoff)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list orphan blobs").Err()
	}
	for _, blob := range orphans {
		hash := blob.ContentHash
		if blob.Encrypted {
			// The row goes first and names the object it held at that moment (the pass that encrypts older blobs may have
			// moved it since the listing); that object is removed, and a failed removal is kept for a retry. From the
			// moment the row is gone nothing can read a copy of the object that a versioned bucket keeps.
			key, found, err := u.files.DeleteEncryptedBlob(ctx, hash, cutoff)
			if err != nil {
				return model.ErrPlatform.WithError(err).WithMessage("Failed to delete blob row").Err()
			}
			if found {
				u.removeQuietly(ctx, key)
			}
			continue
		}
		// A plain blob: its object is content-addressed, so the object goes first and the row after, guarded (a row that
		// was moved to an encrypted object meanwhile is left for the next pass, which treats it as encrypted).
		if err = u.storage.Remove(ctx, blob.Key()); err != nil {
			log.Warn().Err(err).Str("blob", hash).Msg("media gc: blob removal failed, will retry next pass")
			continue
		}
		if _, err = u.files.DeleteBlob(ctx, hash); err != nil {
			return model.ErrPlatform.WithError(err).WithMessage("Failed to delete blob row").Err()
		}
	}
	return u.retryRemovals(ctx, cutoff)
}

// removalBatch is how many remembered objects one pass retries.
const removalBatch = 200

// retryRemovals removes the objects whose removal once failed (and that are older than the grace, so no upload in flight is
// meant to use them). A key under media/blobs/ is content-addressed: a plain blob of that hash may have been stored again
// since, and then the object is live, so only the entry goes.
func (u *MediaUseCase) retryRemovals(ctx context.Context, before time.Time) error {
	keys, err := u.files.ListObjectRemovals(ctx, before, removalBatch)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list objects to remove").Err()
	}
	for _, key := range keys {
		if hash, ok := strings.CutPrefix(key, "media/blobs/"); ok {
			if blob, found, berr := u.getBlob(ctx, hash); berr == nil && found && !blob.Encrypted() {
				_ = u.files.DeleteObjectRemoval(ctx, key) // live again
				continue
			}
		}
		if err = u.storage.Remove(ctx, key); err != nil {
			log.Warn().Err(err).Str("object", key).Msg("media gc: an object could not be removed yet, it stays in the list")
			continue
		}
		if err = u.files.DeleteObjectRemoval(ctx, key); err != nil {
			return model.ErrPlatform.WithError(err).WithMessage("Failed to update the list of objects to remove").Err()
		}
	}
	return nil
}
