package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	"github.com/cybericebox/daemon/internal/model"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	"github.com/cybericebox/daemon/internal/secretctx"
	"github.com/cybericebox/daemon/pkg/filecrypt"
)

// countingReader tracks how many bytes passed through the use case's own
// stream so an oversized upload is detected even when the client lies about
// Content-Length. This is a USE-CASE-LEVEL guard only — by the time bytes
// reach here, the HTTP handler has already fully parsed the multipart body
// into memory/temp files; it is not a substitute for capping the request
// body size at the HTTP boundary (see the handler's use of
// http.MaxBytesReader before FormFile).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// UploadFile stores a file. The payload is encrypted as it streams into a temporary object while it is hashed (the hash is of
// the plain bytes), then promoted to the blob (server-side copy, so no plain byte is ever held) and the logical file row is
// recorded. Every file stored through it is private: it is encrypted. The public images (a logo, a page image, a mail image)
// use UploadPublicImage.
func (u *MediaUseCase) UploadFile(ctx context.Context, name, contentType string, r io.Reader, createdBy uuid.UUID) (mediaModel.File, error) {
	return u.UploadLargeFile(ctx, name, contentType, r, createdBy, u.cfg.MaxUploadBytes)
}

// UploadPublicImage stores a public image: it is served to anyone who opens the page or the mail it belongs to, so it is
// stored plain (see mediaModel.PublicRefTypes). Nothing but those kinds of image may be stored through it.
func (u *MediaUseCase) UploadPublicImage(ctx context.Context, name, contentType string, r io.Reader, createdBy uuid.UUID) (mediaModel.File, error) {
	return u.upload(ctx, name, contentType, r, createdBy, u.cfg.MaxUploadBytes, true)
}

// UploadLargeFile is UploadFile with a cap of its own: the platform's own generated files (an export «з образами»,
// up to 512 MiB + 10 GiB) are larger than one file a person uploads (MaxUploadBytes).
func (u *MediaUseCase) UploadLargeFile(ctx context.Context, name, contentType string, r io.Reader, createdBy uuid.UUID, maxBytes int64) (mediaModel.File, error) {
	return u.upload(ctx, name, contentType, r, createdBy, maxBytes, false)
}

func (u *MediaUseCase) upload(ctx context.Context, name, contentType string, r io.Reader, createdBy uuid.UUID, maxBytes int64, public bool) (mediaModel.File, error) {
	if !u.storageConfigured {
		return mediaModel.File{}, mediaModel.ErrMediaStorageNotConfigured.Err()
	}
	if !public && !u.sealerConfigured {
		return mediaModel.File{}, model.ErrPlatform.WithError(ErrFilesKeyMissing).WithMessage("File encryption is not configured").Err()
	}

	fileID := uuid.Must(uuid.NewV7())
	tmpKey := mediaModel.TmpKey(fileID)

	hasher := sha256.New()
	counter := &countingReader{r: io.TeeReader(io.LimitReader(r, maxBytes+1), hasher)}
	var fileKey []byte
	var putErr error
	if public {
		putErr = u.storage.Put(ctx, tmpKey, counter, -1, contentType)
	} else {
		var err error
		if fileKey, err = filecrypt.NewKey(); err != nil {
			return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to make a file key").Err()
		}
		putErr = u.encryptedPut(ctx, tmpKey, fileKey, 0, counter, -1, "application/octet-stream")
	}
	if putErr != nil {
		_ = u.storage.Remove(ctx, tmpKey)
		return mediaModel.File{}, model.ErrPlatform.WithError(putErr).WithMessage("Failed to store upload").Err()
	}
	// Best-effort tmp cleanup on every exit path below.
	defer func() { _ = u.storage.Remove(ctx, tmpKey) }()

	if counter.n > maxBytes {
		return mediaModel.File{}, mediaModel.ErrFileTooLarge.Err()
	}

	hash := hex.EncodeToString(hasher.Sum(nil))
	var wrapped string
	if !public {
		var err error
		if wrapped, err = u.wrap(fileKey, secretctx.FileKey(hash)); err != nil {
			return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to wrap the file key").Err()
		}
	}
	return u.publish(ctx, fileID, tmpKey, wrapped, name, contentType, counter.n, hash, createdBy)
}

// publish promotes the assembled tmp object to the blob and records the logical file row. The caller removes tmpKey.
// wrapped is the key of the tmp object (empty: it is plain, a public image).
//
//   - A plain tmp object goes to the content-addressed place media/blobs/<hash> (skipped when a blob of the hash exists):
//     equal content is the same bytes, so concurrent uploads cannot disagree.
//   - An encrypted one goes to a random name media/enc/<id>. Equal content has different keys and different ciphertext, so
//     of two concurrent uploads of the same file the row that is written first wins, and the other drops its object.
//   - A blob that exists encrypted is reused whichever kind comes: a public image of the same bytes is served decrypted.
//   - A blob that exists plain and now gets a private upload is moved to the encrypted object (adopted): a private file is
//     never left plain because a public image once had the same bytes.
func (u *MediaUseCase) publish(
	ctx context.Context,
	fileID uuid.UUID,
	tmpKey, wrapped, name, contentType string,
	size int64,
	hash string,
	createdBy uuid.UUID,
) (mediaModel.File, error) {
	private := wrapped != ""
	blob, found, err := u.getBlob(ctx, hash)
	if err != nil {
		return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to look up blob").Err()
	}
	fresh := mediaModel.Blob{ContentHash: hash, SizeBytes: size}
	ownKey := "" // the encrypted object this call stores, if any
	switch {
	case found && (blob.Encrypted() || !private):
		// Reuse: nothing to store.
	case !private:
		exists, statErr := u.storage.Stat(ctx, mediaModel.BlobKey(hash))
		if statErr != nil {
			return mediaModel.File{}, model.ErrPlatform.WithError(statErr).WithMessage("Failed to stat blob").Err()
		}
		if !exists {
			if err = u.storage.Copy(ctx, tmpKey, mediaModel.BlobKey(hash)); err != nil {
				return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to promote blob").Err()
			}
		}
	default:
		ownKey = mediaModel.EncryptedBlobKey(uuid.Must(uuid.NewV7()))
		if err = u.storage.Copy(ctx, tmpKey, ownKey); err != nil {
			return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to promote blob").Err()
		}
		fresh.ObjectKey, fresh.WrappedKey = ownKey, wrapped
	}

	created, standing, err := u.files.CreateFile(ctx, mediaModel.File{
		ID:          fileID,
		Name:        name,
		ContentType: contentType,
		SizeBytes:   size,
		ContentHash: hash,
		CreatedAt:   time.Now(),
		CreatedBy:   uuid.NullUUID{UUID: createdBy, Valid: createdBy != uuid.Nil},
	}, fresh)
	if err != nil {
		if ownKey != "" {
			u.removeQuietly(ctx, ownKey)
		}
		return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to record file").Err()
	}
	if ownKey != "" && standing.ObjectKey != ownKey {
		u.settleBlob(ctx, hash, ownKey, wrapped, standing)
	}
	return created, nil
}

// settleBlob is the end of a private upload whose blob already stood when the row was written: if that blob is encrypted the
// object this call stored is surplus and goes; if it is plain, this call's encrypted object replaces it.
func (u *MediaUseCase) settleBlob(ctx context.Context, hash, ownKey, wrapped string, standing mediaModel.Blob) {
	if standing.Encrypted() {
		u.removeQuietly(ctx, ownKey)
		return
	}
	adopted, err := u.files.AdoptBlobEncryption(ctx, hash, ownKey, wrapped)
	if err != nil || !adopted {
		if err != nil {
			log.Warn().Err(err).Str("blob", hash).Msg("media: a plain blob could not be moved to an encrypted object; it stays plain for the encryption pass")
		}
		u.removeQuietly(ctx, ownKey)
		return
	}
	u.removeQuietly(ctx, standing.Key())
}

func (u *MediaUseCase) removeQuietly(ctx context.Context, key string) {
	if err := u.storage.Remove(ctx, key); err != nil {
		log.Warn().Err(err).Str("object", key).Msg("media: an object could not be removed")
	}
}

// getBlob reads the blob of a hash; found is false when there is none.
func (u *MediaUseCase) getBlob(ctx context.Context, hash string) (mediaModel.Blob, bool, error) {
	blob, err := u.files.GetBlob(ctx, hash)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return mediaModel.Blob{}, false, nil
		}
		return mediaModel.Blob{}, false, err
	}
	return blob, true, nil
}

// objectOf says how to open the object of a file: its blob, with the plain size of the file.
func (u *MediaUseCase) objectOf(ctx context.Context, f mediaModel.File) (object, error) {
	blob, found, err := u.getBlob(ctx, f.ContentHash)
	if err != nil {
		return object{}, model.ErrPlatform.WithError(err).WithMessage("Failed to look up blob").Err()
	}
	if !found {
		return object{}, mediaModel.ErrFileNotFound.Err()
	}
	return object{key: blob.Key(), wrapped: blob.WrappedKey, ctx: secretctx.FileKey(f.ContentHash), size: f.SizeBytes}, nil
}

// StreamFile opens the blob stream for a logical file, decrypted when it is stored encrypted. The caller closes the
// reader.
//
// Unconfigured storage is an infrastructure fault here, not a client error:
// file rows only exist because UploadFile once had storage, so hitting this
// guard means the deployment lost its storage config. Reported as a platform
// error (the client-facing ErrMediaStorageNotConfigured keeps its single call
// site in UploadFile).
func (u *MediaUseCase) StreamFile(ctx context.Context, id uuid.UUID) (io.ReadCloser, mediaModel.File, error) {
	if !u.storageConfigured {
		return nil, mediaModel.File{}, model.ErrPlatform.WithMessage("File storage is not configured").Err()
	}
	f, err := u.GetFile(ctx, id)
	if err != nil {
		return nil, mediaModel.File{}, err
	}
	o, err := u.objectOf(ctx, f)
	if err != nil {
		return nil, mediaModel.File{}, err
	}
	rc, err := u.open(ctx, o)
	if err != nil {
		return nil, mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to open blob stream").Err()
	}
	return rc, f, nil
}

// ChunkCount is how many chunks of chunkBytes a file of size bytes takes (0 for a non-positive chunk size).
func ChunkCount(size, chunkBytes int64) int {
	if chunkBytes <= 0 {
		return 0
	}
	return int((size + chunkBytes - 1) / chunkBytes)
}

// ReadFileChunk opens chunk index (0-based) of a stored file, chunks being UploadChunkBytes long (the last one holds
// the rest), and reports the chunk length. An index outside the file reads as a missing file. The caller closes the
// reader.
func (u *MediaUseCase) ReadFileChunk(ctx context.Context, id uuid.UUID, index int) (io.ReadCloser, mediaModel.File, int64, error) {
	if !u.storageConfigured {
		return nil, mediaModel.File{}, 0, model.ErrPlatform.WithMessage("File storage is not configured").Err()
	}
	f, err := u.GetFile(ctx, id)
	if err != nil {
		return nil, mediaModel.File{}, 0, err
	}
	chunk := u.cfg.UploadChunkBytes
	if index < 0 || index >= ChunkCount(f.SizeBytes, chunk) {
		return nil, mediaModel.File{}, 0, mediaModel.ErrFileNotFound.Err()
	}
	offset := int64(index) * chunk
	length := min(chunk, f.SizeBytes-offset)
	o, err := u.objectOf(ctx, f)
	if err != nil {
		return nil, mediaModel.File{}, 0, err
	}
	rc, err := u.openRange(ctx, o, offset, length)
	if err != nil {
		return nil, mediaModel.File{}, 0, model.ErrPlatform.WithError(err).WithMessage("Failed to open blob chunk").Err()
	}
	return rc, f, length, nil
}

// GetFile returns a logical file's metadata (no blob access, so it works
// without object storage) — e.g. to validate references and sum sizes.
func (u *MediaUseCase) GetFile(ctx context.Context, id uuid.UUID) (mediaModel.File, error) {
	f, err := u.files.GetFileByID(ctx, id)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return mediaModel.File{}, mediaModel.ErrFileNotFound.Err()
		}
		return mediaModel.File{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get file").Err()
	}
	return f, nil
}
