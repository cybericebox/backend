package media_test

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/daemon/internal/delivery/repository/mediaRepo"
	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
)

// memQueries is an in-memory stand-in for the media queries with the semantics the use case relies on: a blob is shared by
// content hash (the first writer's object and key stand), a plain blob can be adopted once, references decide which blobs
// the encryption pass skips. Methods the tests do not use panic through the nil embedded interface.
type memQueries struct {
	mediaRepo.Queries
	mu      sync.Mutex
	files   map[uuid.UUID]postgres.File
	blobs   map[string]*postgres.GetFileBlobRow
	refs    map[uuid.UUID][]string // file id -> reference kinds
	uploads map[uuid.UUID]postgres.MediaUpload
	// beforeCreate runs inside CreateFile before the blob is looked at: a test uses it to let another writer win a race.
	beforeCreate func(arg postgres.CreateFileParams)
}

func newMemQueries() *memQueries {
	return &memQueries{
		files: map[uuid.UUID]postgres.File{}, blobs: map[string]*postgres.GetFileBlobRow{},
		refs: map[uuid.UUID][]string{}, uploads: map[uuid.UUID]postgres.MediaUpload{},
	}
}

func (m *memQueries) CreateFile(_ context.Context, arg postgres.CreateFileParams) (postgres.CreateFileRow, error) {
	if m.beforeCreate != nil {
		m.beforeCreate(arg)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[arg.ContentHash]; !ok {
		m.blobs[arg.ContentHash] = &postgres.GetFileBlobRow{
			ContentHash: arg.ContentHash, SizeBytes: arg.SizeBytes, ObjectKey: arg.ObjectKey, WrappedKey: arg.WrappedKey,
		}
	}
	f := postgres.File{
		ID: arg.ID, Name: arg.Name, ContentType: arg.ContentType, SizeBytes: arg.SizeBytes, ContentHash: arg.ContentHash,
		CreatedAt: arg.CreatedAt, CreatedBy: arg.CreatedBy,
	}
	m.files[arg.ID] = f
	b := m.blobs[arg.ContentHash]
	return postgres.CreateFileRow{
		ID: f.ID, Name: f.Name, ContentType: f.ContentType, SizeBytes: f.SizeBytes, ContentHash: f.ContentHash,
		CreatedAt: f.CreatedAt, CreatedBy: f.CreatedBy, BlobObjectKey: b.ObjectKey, BlobWrappedKey: b.WrappedKey,
	}, nil
}

func (m *memQueries) GetFileByID(_ context.Context, id uuid.UUID) (postgres.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[id]
	if !ok {
		return postgres.File{}, pgx.ErrNoRows
	}
	return f, nil
}

func (m *memQueries) GetFileBlob(_ context.Context, hash string) (postgres.GetFileBlobRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[hash]
	if !ok {
		return postgres.GetFileBlobRow{}, pgx.ErrNoRows
	}
	return *b, nil
}

func (m *memQueries) AdoptBlobEncryption(_ context.Context, arg postgres.AdoptBlobEncryptionParams) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[arg.ContentHash]
	if !ok || b.WrappedKey != "" {
		return 0, nil
	}
	b.ObjectKey, b.WrappedKey = arg.ObjectKey, arg.WrappedKey
	return 1, nil
}

func (m *memQueries) ReplaceFileReferences(_ context.Context, arg postgres.ReplaceFileReferencesParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range arg.FileIds {
		m.refs[id] = append(m.refs[id], arg.RefType)
	}
	return nil
}

func (m *memQueries) CountPlainBlobs(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := int64(0)
	for _, b := range m.blobs {
		if b.WrappedKey == "" {
			n++
		}
	}
	return n, nil
}

// ListPlainBlobs mirrors the SQL: plain blobs after the cursor, except those with a public reference and no other.
func (m *memQueries) ListPlainBlobs(_ context.Context, arg postgres.ListPlainBlobsParams) ([]postgres.ListPlainBlobsRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []postgres.ListPlainBlobsRow
	for hash, b := range m.blobs {
		if b.WrappedKey != "" || hash <= arg.After {
			continue
		}
		public, other := false, false
		for id, f := range m.files {
			if f.ContentHash != hash {
				continue
			}
			for _, kind := range m.refs[id] {
				if slices.Contains(arg.PublicRefTypes, kind) {
					public = true
				} else {
					other = true
				}
			}
		}
		if public && !other {
			continue
		}
		out = append(out, postgres.ListPlainBlobsRow{ContentHash: hash, SizeBytes: b.SizeBytes, ObjectKey: b.ObjectKey})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContentHash < out[j].ContentHash })
	if int(arg.RowLimit) < len(out) {
		out = out[:arg.RowLimit]
	}
	return out, nil
}

func (m *memQueries) CreateMediaUpload(_ context.Context, arg postgres.CreateMediaUploadParams) (postgres.MediaUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := postgres.MediaUpload{
		ID: arg.ID, CreatedBy: arg.CreatedBy, Name: arg.Name, ContentType: arg.ContentType, SizeBytes: arg.SizeBytes,
		ChunkBytes: arg.ChunkBytes, CreatedAt: arg.CreatedAt, ExpiresAt: arg.ExpiresAt, Purpose: arg.Purpose, WrappedKey: arg.WrappedKey,
	}
	m.uploads[arg.ID] = u
	return u, nil
}

func (m *memQueries) GetMediaUpload(_ context.Context, id uuid.UUID) (postgres.MediaUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return postgres.MediaUpload{}, pgx.ErrNoRows
	}
	return u, nil
}

func (m *memQueries) AdvanceMediaUpload(_ context.Context, arg postgres.AdvanceMediaUploadParams) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[arg.ID]
	if !ok || u.ChunksReceived != arg.ExpectedChunks {
		return 0, nil
	}
	u.ChunksReceived++
	u.ExpiresAt = arg.ExpiresAt
	m.uploads[arg.ID] = u
	return 1, nil
}

func (m *memQueries) DeleteMediaUpload(_ context.Context, id uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, id)
	return 1, nil
}

func (m *memQueries) DeleteUnreferencedFiles(context.Context, time.Time) ([]postgres.DeleteUnreferencedFilesRow, error) {
	return nil, nil
}

func (m *memQueries) ListExpiredMediaUploads(context.Context, postgres.ListExpiredMediaUploadsParams) ([]postgres.MediaUpload, error) {
	return nil, nil
}

func (m *memQueries) ListOrphanBlobs(context.Context, time.Time) ([]postgres.ListOrphanBlobsRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []postgres.ListOrphanBlobsRow
	for hash, b := range m.blobs {
		used := false
		for _, f := range m.files {
			used = used || f.ContentHash == hash
		}
		if !used {
			out = append(out, postgres.ListOrphanBlobsRow{ContentHash: hash, ObjectKey: b.ObjectKey})
		}
	}
	return out, nil
}

func (m *memQueries) DeleteBlob(_ context.Context, hash string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, hash)
	return 1, nil
}

func (m *memQueries) blob(hash string) postgres.GetFileBlobRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.blobs[hash]; ok {
		return *b
	}
	return postgres.GetFileBlobRow{}
}

type (
	postgresArg = postgres.CreateFileParams
	blobRowOf   = postgres.GetFileBlobRow
)

// createParams is the parameters of a plain file row, as the older code wrote it.
func createParams(id uuid.UUID, hash string, size int64) postgres.CreateFileParams {
	return postgres.CreateFileParams{ID: id, Name: "f", ContentType: "t", SizeBytes: size, ContentHash: hash, CreatedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
}

func replaceRefs(kind string, fileID uuid.UUID) postgres.ReplaceFileReferencesParams {
	return postgres.ReplaceFileReferencesParams{RefType: kind, RefID: uuid.Must(uuid.NewV7()), FileIds: []uuid.UUID{fileID}}
}

// postgresUpload is an upload row of the older form: no wrapped key, so its chunks are plain.
func postgresUpload(id, owner uuid.UUID, size, chunk int64, received int32) postgres.MediaUpload {
	return postgres.MediaUpload{
		ID: id, CreatedBy: owner, Name: "old.bin", ContentType: "application/octet-stream", SizeBytes: size, ChunkBytes: chunk,
		ChunksReceived: received, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
}
