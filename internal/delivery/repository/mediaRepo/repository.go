// Package mediaRepo is the repository for the media subsystem: logical files,
// content-addressed blobs and ownership references. sqlc/pgtype mapping stays
// here.
package mediaRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
)

type Queries interface {
	CreateFile(ctx context.Context, arg postgres.CreateFileParams) (postgres.CreateFileRow, error)
	GetFileBlob(ctx context.Context, contentHash string) (postgres.GetFileBlobRow, error)
	AdoptBlobEncryption(ctx context.Context, arg postgres.AdoptBlobEncryptionParams) (int64, error)
	ListPlainBlobs(ctx context.Context, arg postgres.ListPlainBlobsParams) ([]postgres.ListPlainBlobsRow, error)
	CountPlainBlobs(ctx context.Context) (int64, error)
	ListUserIDsAfter(ctx context.Context, arg postgres.ListUserIDsAfterParams) ([]postgres.ListUserIDsAfterRow, error)
	AdoptLegacyAvatar(ctx context.Context, arg postgres.AdoptLegacyAvatarParams) (int64, error)
	GetFileByID(ctx context.Context, id uuid.UUID) (postgres.File, error)
	BlobExists(ctx context.Context, contentHash string) (bool, error)
	ReplaceFileReferences(ctx context.Context, arg postgres.ReplaceFileReferencesParams) error
	AddFileReference(ctx context.Context, arg postgres.AddFileReferenceParams) error
	GetFileReferenceIDs(ctx context.Context, arg postgres.GetFileReferenceIDsParams) ([]uuid.UUID, error)
	DeleteFileReferences(ctx context.Context, arg postgres.DeleteFileReferencesParams) (int64, error)
	DeleteFileReferencesBatch(ctx context.Context, arg postgres.DeleteFileReferencesBatchParams) (int64, error)
	DeleteUnreferencedFiles(ctx context.Context, createdBefore time.Time) ([]postgres.DeleteUnreferencedFilesRow, error)
	ListOrphanBlobs(ctx context.Context, touchedBefore time.Time) ([]postgres.ListOrphanBlobsRow, error)
	DeleteBlob(ctx context.Context, contentHash string) (int64, error)
	CreateMediaUpload(ctx context.Context, arg postgres.CreateMediaUploadParams) (postgres.MediaUpload, error)
	GetMediaUpload(ctx context.Context, id uuid.UUID) (postgres.MediaUpload, error)
	AdvanceMediaUpload(ctx context.Context, arg postgres.AdvanceMediaUploadParams) (int64, error)
	DeleteMediaUpload(ctx context.Context, id uuid.UUID) (int64, error)
	ListExpiredMediaUploads(ctx context.Context, arg postgres.ListExpiredMediaUploadsParams) ([]postgres.MediaUpload, error)
}

type Repository struct {
	q Queries
}

func New(q Queries) *Repository {
	return &Repository{q: q}
}

// ── ownership references (file_references join table) ──

// ReplaceReferences syncs an owner's reference set to exactly fileIDs.
func (r *Repository) ReplaceReferences(ctx context.Context, refType string, refID uuid.UUID, fileIDs []uuid.UUID) error {
	return r.q.ReplaceFileReferences(ctx, postgres.ReplaceFileReferencesParams{RefType: refType, RefID: refID, FileIds: fileIDs})
}

func (r *Repository) AddReference(ctx context.Context, refType string, refID, fileID uuid.UUID) error {
	return r.q.AddFileReference(ctx, postgres.AddFileReferenceParams{RefType: refType, RefID: refID, FileID: fileID})
}

// ReferenceIDs returns the file ids referenced by (refType, refID).
func (r *Repository) ReferenceIDs(ctx context.Context, refType string, refID uuid.UUID) ([]uuid.UUID, error) {
	return r.q.GetFileReferenceIDs(ctx, postgres.GetFileReferenceIDsParams{RefType: refType, RefID: refID})
}

// DeleteReferences drops every reference owned by refID.
func (r *Repository) DeleteReferences(ctx context.Context, refType string, refID uuid.UUID) (int64, error) {
	return r.q.DeleteFileReferences(ctx, postgres.DeleteFileReferencesParams{RefType: refType, RefID: refID})
}

// DeleteReferencesBatch drops references for a set of owners.
func (r *Repository) DeleteReferencesBatch(ctx context.Context, refType string, refIDs []uuid.UUID) (int64, error) {
	return r.q.DeleteFileReferencesBatch(ctx, postgres.DeleteFileReferencesBatchParams{RefType: refType, RefIds: refIDs})
}

// ── garbage collection ──

// DeleteUnreferenced removes files with no references older than the cutoff.
func (r *Repository) DeleteUnreferenced(ctx context.Context, createdBefore time.Time) error {
	_, err := r.q.DeleteUnreferencedFiles(ctx, createdBefore)
	return err
}

// ListOrphanBlobs returns the blobs no file references, older than the cutoff (the hash and where the object lies).
func (r *Repository) ListOrphanBlobs(ctx context.Context, touchedBefore time.Time) ([]mediaModel.Blob, error) {
	rows, err := r.q.ListOrphanBlobs(ctx, touchedBefore)
	if err != nil {
		return nil, err
	}
	out := make([]mediaModel.Blob, 0, len(rows))
	for _, row := range rows {
		out = append(out, mediaModel.Blob{ContentHash: row.ContentHash, ObjectKey: row.ObjectKey})
	}
	return out, nil
}

// GetBlob returns the blob of a content hash; not found propagates the raw repo error.
func (r *Repository) GetBlob(ctx context.Context, contentHash string) (mediaModel.Blob, error) {
	row, err := r.q.GetFileBlob(ctx, contentHash)
	if err != nil {
		return mediaModel.Blob{}, err
	}
	return mediaModel.Blob{ContentHash: row.ContentHash, SizeBytes: row.SizeBytes, ObjectKey: row.ObjectKey, WrappedKey: row.WrappedKey}, nil
}

// AdoptBlobEncryption moves a blob that is still plain to an encrypted object; false when it no longer is plain.
func (r *Repository) AdoptBlobEncryption(ctx context.Context, contentHash, objectKey, wrappedKey string) (bool, error) {
	n, err := r.q.AdoptBlobEncryption(ctx, postgres.AdoptBlobEncryptionParams{ContentHash: contentHash, ObjectKey: objectKey, WrappedKey: wrappedKey})
	return n == 1, err
}

// ListPlainBlobs returns up to limit plain blobs after the cursor that the encryption pass has to move.
func (r *Repository) ListPlainBlobs(ctx context.Context, after string, limit int) ([]mediaModel.Blob, error) {
	rows, err := r.q.ListPlainBlobs(ctx, postgres.ListPlainBlobsParams{After: after, PublicRefTypes: mediaModel.PublicRefTypes, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]mediaModel.Blob, 0, len(rows))
	for _, row := range rows {
		out = append(out, mediaModel.Blob{ContentHash: row.ContentHash, SizeBytes: row.SizeBytes, ObjectKey: row.ObjectKey})
	}
	return out, nil
}

// CountPlainBlobs counts the blobs stored plain.
func (r *Repository) CountPlainBlobs(ctx context.Context) (int64, error) {
	return r.q.CountPlainBlobs(ctx)
}

// DeleteBlob removes a blob by content hash.
func (r *Repository) DeleteBlob(ctx context.Context, contentHash string) (int64, error) {
	return r.q.DeleteBlob(ctx, contentHash)
}

// CreateFile records the file and the use of its blob. blob is the blob as this caller would create it; when the blob
// already exists it keeps its own object, and the blob returned is the one that now stands.
func (r *Repository) CreateFile(ctx context.Context, f mediaModel.File, blob mediaModel.Blob) (mediaModel.File, mediaModel.Blob, error) {
	row, err := r.q.CreateFile(ctx, postgres.CreateFileParams{
		ObjectKey:   blob.ObjectKey,
		WrappedKey:  blob.WrappedKey,
		ID:          f.ID,
		Name:        f.Name,
		ContentType: f.ContentType,
		SizeBytes:   f.SizeBytes,
		ContentHash: f.ContentHash,
		CreatedAt:   f.CreatedAt,
		CreatedBy:   f.CreatedBy,
	})
	if err != nil {
		return mediaModel.File{}, mediaModel.Blob{}, err
	}
	file := ToDomain(postgres.File{
		ID: row.ID, Name: row.Name, ContentType: row.ContentType, SizeBytes: row.SizeBytes, ContentHash: row.ContentHash,
		CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
	})
	return file, mediaModel.Blob{ContentHash: row.ContentHash, SizeBytes: row.SizeBytes, ObjectKey: row.BlobObjectKey, WrappedKey: row.BlobWrappedKey}, nil
}

func (r *Repository) GetFileByID(ctx context.Context, id uuid.UUID) (mediaModel.File, error) {
	row, err := r.q.GetFileByID(ctx, id)
	if err != nil {
		return mediaModel.File{}, err
	}
	return ToDomain(row), nil
}

func ToDomain(row postgres.File) mediaModel.File {
	return mediaModel.File{
		ID:          row.ID,
		Name:        row.Name,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		ContentHash: row.ContentHash,
		CreatedAt:   row.CreatedAt,
		CreatedBy:   row.CreatedBy,
	}
}

// ── resumable uploads ──

func uploadToDomain(row postgres.MediaUpload) mediaModel.Upload {
	return mediaModel.Upload{
		ID: row.ID, CreatedBy: row.CreatedBy, Name: row.Name, ContentType: row.ContentType, SizeBytes: row.SizeBytes,
		ChunkBytes: row.ChunkBytes, ChunksReceived: int(row.ChunksReceived), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		Purpose: mediaModel.UploadPurpose(row.Purpose), WrappedKey: row.WrappedKey,
	}
}

// CreateUpload stores a new upload.
func (r *Repository) CreateUpload(ctx context.Context, u mediaModel.Upload) (mediaModel.Upload, error) {
	row, err := r.q.CreateMediaUpload(ctx, postgres.CreateMediaUploadParams{
		ID: u.ID, CreatedBy: u.CreatedBy, Name: u.Name, ContentType: u.ContentType, SizeBytes: u.SizeBytes,
		ChunkBytes: u.ChunkBytes, CreatedAt: u.CreatedAt, ExpiresAt: u.ExpiresAt, Purpose: string(u.Purpose), WrappedKey: u.WrappedKey,
	})
	if err != nil {
		return mediaModel.Upload{}, err
	}
	return uploadToDomain(row), nil
}

// GetUpload loads an upload; not found propagates the raw repo error.
func (r *Repository) GetUpload(ctx context.Context, id uuid.UUID) (mediaModel.Upload, error) {
	row, err := r.q.GetMediaUpload(ctx, id)
	if err != nil {
		return mediaModel.Upload{}, err
	}
	return uploadToDomain(row), nil
}

// AdvanceUpload counts the next chunk as received, only when chunksReceived is still what the caller read; zero
// rows means the client is out of step. The expiry moves forward with every chunk.
func (r *Repository) AdvanceUpload(ctx context.Context, id, owner uuid.UUID, chunksReceived int, expiresAt time.Time) (int64, error) {
	return r.q.AdvanceMediaUpload(ctx, postgres.AdvanceMediaUploadParams{
		ID: id, CreatedBy: owner, ExpectedChunks: int32(chunksReceived), ExpiresAt: expiresAt,
	})
}

// DeleteUpload drops the upload row.
func (r *Repository) DeleteUpload(ctx context.Context, id uuid.UUID) (int64, error) {
	return r.q.DeleteMediaUpload(ctx, id)
}

// ListExpiredUploads returns up to limit uploads that waited past the cutoff.
func (r *Repository) ListExpiredUploads(ctx context.Context, cutoff time.Time, limit int) ([]mediaModel.Upload, error) {
	rows, err := r.q.ListExpiredMediaUploads(ctx, postgres.ListExpiredMediaUploadsParams{ExpiresAt: cutoff, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]mediaModel.Upload, 0, len(rows))
	for _, row := range rows {
		out = append(out, uploadToDomain(row))
	}
	return out, nil
}

// UserRef is a user as the legacy avatar pass sees it.
type UserRef struct {
	ID      uuid.UUID
	Deleted bool
}

// ListUsers returns up to limit users (deleted ones too) with an id after the cursor.
func (r *Repository) ListUsers(ctx context.Context, after uuid.UUID, limit int) ([]UserRef, error) {
	rows, err := r.q.ListUserIDsAfter(ctx, postgres.ListUserIDsAfterParams{After: after, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]UserRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, UserRef{ID: row.ID, Deleted: row.Deleted})
	}
	return out, nil
}

// AdoptLegacyAvatar publishes the avatar reference in one statement, only for a user who still exists and has no avatar
// reference; false when it was refused.
func (r *Repository) AdoptLegacyAvatar(ctx context.Context, userID, fileID uuid.UUID) (bool, error) {
	n, err := r.q.AdoptLegacyAvatar(ctx, postgres.AdoptLegacyAvatarParams{UserID: userID, FileID: fileID})
	return n == 1, err
}
