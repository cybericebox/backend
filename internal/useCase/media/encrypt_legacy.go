package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/daemon/internal/delivery/repository/keyRotationRepo"
	"github.com/cybericebox/daemon/internal/model"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	"github.com/cybericebox/daemon/internal/secretctx"
	"github.com/cybericebox/daemon/pkg/filecrypt"
	"github.com/cybericebox/daemon/pkg/secret"
)

// LegacyEncryptionSource is the name of this pass in the progress table that `keys status` prints.
const LegacyEncryptionSource = "file_blobs.legacy_objects_to_encrypted"

// legacyBatch is how many plain blobs one query lists.
const legacyBatch = 100

// ProgressStore records the counts of a run (the progress table of the key re-encryption).
type ProgressStore interface {
	SaveProgress(ctx context.Context, p secret.Progress, state string, startedAt time.Time) error
}

// SetProgressStore wires the progress table; without it the pass runs and logs but leaves no row.
func (u *MediaUseCase) SetProgressStore(s ProgressStore) { u.progress = s }

// EncryptLegacyBlobs is the pass that encrypts the objects stored plain before encryption existed. For each plain blob it
// writes a new encrypted object under a random name, switches the blob row to it (only while the row is still plain, so a
// concurrent upload cannot be overwritten), and then removes the old object. Public images are left plain on purpose.
//
// It can be stopped and started again at any point: a blob is either still plain (and listed again) or already switched. A
// blob whose old object is missing or does not match its hash is counted failed and left as it is; nothing is deleted
// unless the new object was verified against the hash of the plain bytes.
func (u *MediaUseCase) EncryptLegacyBlobs(ctx context.Context) (secret.Progress, error) {
	started := time.Now()
	p := secret.Progress{Family: keyRotationRepo.FamilyFiles, Source: LegacyEncryptionSource, KeyID: u.currentKeyID()}
	if !u.storageConfigured || !u.sealerConfigured {
		return p, nil
	}
	remaining, err := u.files.CountPlainBlobs(ctx)
	if err != nil {
		return p, model.ErrPlatform.WithError(err).WithMessage("Failed to count plain blobs").Err()
	}
	if remaining == 0 {
		return p, nil // nothing to do: no row is written for an empty pass
	}
	save := func(state string) {
		p.UpdatedAt = time.Now()
		if u.progress == nil {
			return
		}
		// A failed write of the table must not stop the pass; the counts are in the log.
		if serr := u.progress.SaveProgress(context.WithoutCancel(ctx), p, state, started); serr != nil {
			log.Warn().Msg("media: the progress of the encryption pass could not be written")
		}
	}
	save(keyRotationRepo.StateRunning)

	after := ""
	for {
		blobs, lerr := u.files.ListPlainBlobs(ctx, after, legacyBatch)
		if lerr != nil {
			save(keyRotationRepo.StateFailed)
			return p, model.ErrPlatform.WithError(lerr).WithMessage("Failed to list plain blobs").Err()
		}
		if len(blobs) == 0 {
			break
		}
		for _, blob := range blobs {
			if ctx.Err() != nil {
				save(keyRotationRepo.StateInterrupted)
				return p, ctx.Err()
			}
			p.Total++
			if u.inBackoff(blob.ContentHash) {
				// Tried and failed lately: not read again yet, but still counted, so `keys status` keeps showing it.
				p.Failed++
				if p.ByReason == nil {
					p.ByReason = map[secret.Reason]int{}
				}
				p.ByReason[secret.ReasonCorrupt]++
				after = blob.ContentHash
				continue
			}
			switch encErr := u.encryptBlob(ctx, blob); {
			case encErr == nil:
				u.forgetFailure(blob.ContentHash)
				p.Done++
			case errors.Is(encErr, errBlobChanged):
				p.Conflicts++
			case ctx.Err() != nil:
				save(keyRotationRepo.StateInterrupted)
				return p, ctx.Err()
			default:
				p.Failed++
				if p.ByReason == nil {
					p.ByReason = map[secret.Reason]int{}
				}
				p.ByReason[secret.ReasonCorrupt]++
				retryAt := u.rememberFailure(blob.ContentHash)
				log.Warn().Time("retry_at", retryAt).Err(encErr).Str("blob", blob.ContentHash).Msg("media: a plain blob could not be encrypted, it stays as it is")
			}
			after = blob.ContentHash
		}
		save(keyRotationRepo.StateRunning)
	}
	switch {
	case p.Failed > 0 || p.Conflicts > 0:
		save(keyRotationRepo.StateFinishedWithFailures)
	default:
		save(keyRotationRepo.StateFinished)
	}
	return p, nil
}

// blobFailure is what the pass remembers of a blob it could not encrypt.
type blobFailure struct {
	failures int
	retryAt  time.Time
}

const (
	legacyBackoffBase = 2 * time.Hour
	legacyBackoffMax  = 7 * 24 * time.Hour
)

// inBackoff reports whether a blob failed lately and is not to be read again yet.
func (u *MediaUseCase) inBackoff(hash string) bool {
	u.legacyFailuresMu.Lock()
	defer u.legacyFailuresMu.Unlock()
	f, ok := u.legacyFailures[hash]
	return ok && u.now().Before(f.retryAt)
}

// rememberFailure records a failure and returns when the blob may be tried again: after 2 hours, then twice as long each time,
// at most a week. The memory is the process's: a restart tries every broken blob once more.
func (u *MediaUseCase) rememberFailure(hash string) time.Time {
	u.legacyFailuresMu.Lock()
	defer u.legacyFailuresMu.Unlock()
	if u.legacyFailures == nil {
		u.legacyFailures = map[string]blobFailure{}
	}
	f := u.legacyFailures[hash]
	f.failures++
	delay := legacyBackoffBase << min(f.failures-1, 10)
	f.retryAt = u.now().Add(min(delay, legacyBackoffMax))
	u.legacyFailures[hash] = f
	return f.retryAt
}

func (u *MediaUseCase) forgetFailure(hash string) {
	u.legacyFailuresMu.Lock()
	defer u.legacyFailuresMu.Unlock()
	delete(u.legacyFailures, hash)
}

// errBlobChanged: another writer moved the blob while this pass worked on it; it is no longer plain, nothing is wrong.
var errBlobChanged = errors.New("media: the blob changed meanwhile")

func (u *MediaUseCase) currentKeyID() string {
	if c, ok := u.sealer.(interface{ CurrentKeyID() string }); ok && u.sealerConfigured {
		return c.CurrentKeyID()
	}
	return "-"
}

// encryptBlob moves one plain blob to an encrypted object.
func (u *MediaUseCase) encryptBlob(ctx context.Context, blob mediaModel.Blob) error {
	src, _, err := u.storage.Get(ctx, blob.Key())
	if err != nil {
		return fmt.Errorf("open the plain object: %w", err)
	}
	defer func() { _ = src.Close() }()

	fileKey, err := filecrypt.NewKey()
	if err != nil {
		return err
	}
	hasher := sha256.New()
	counter := &countingReader{r: io.TeeReader(io.LimitReader(src, blob.SizeBytes+1), hasher)}
	newKey := mediaModel.EncryptedBlobKey(uuid.Must(uuid.NewV7()))
	if err = u.encryptedPut(ctx, newKey, fileKey, counter, blob.SizeBytes, "application/octet-stream"); err != nil {
		u.removeQuietly(ctx, newKey)
		return fmt.Errorf("write the encrypted object: %w", err)
	}
	// The new object is trusted only when what went into it is exactly what the row says the file is.
	if counter.n != blob.SizeBytes || hex.EncodeToString(hasher.Sum(nil)) != blob.ContentHash {
		u.removeQuietly(ctx, newKey)
		return errors.New("the plain object does not match its hash or size")
	}
	wrapped, err := u.wrap(fileKey, secretctx.FileKey(blob.ContentHash))
	if err != nil {
		u.removeQuietly(ctx, newKey)
		return err
	}
	adopted, err := u.files.AdoptBlobEncryption(ctx, blob.ContentHash, newKey, wrapped)
	if err != nil {
		u.removeQuietly(ctx, newKey)
		return err
	}
	if !adopted {
		u.removeQuietly(ctx, newKey)
		return errBlobChanged
	}
	u.removeQuietly(ctx, blob.Key())
	return nil
}
