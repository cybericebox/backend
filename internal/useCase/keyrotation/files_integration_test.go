package keyrotation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/delivery/repository/keyRotationRepo"
	"github.com/cybericebox/backend/internal/secretctx"
	"github.com/cybericebox/backend/internal/useCase/keyrotation"
)

// A master key change moves only the wrapped file keys: the key of each file stays the same, no file is touched, and a blob
// stored plain (no wrapped key) is not a row of the pass.
func TestRun_FilesRewrapsTheWrappedKeysOnly(t *testing.T) {
	w := newWorld(t)
	const hashA, hashB, hashPlain = "hash-a", "hash-b", "hash-plain"
	fileKeyA, fileKeyB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	uploadID := id(7)

	insertBlob := func(hash, object, wrapped string) {
		w.exec(`INSERT INTO file_blobs (content_hash, size_bytes, ref_count, created_at, object_key, wrapped_key) VALUES ($1, 10, 1, $2, $3, $4)`,
			hash, w.clock, object, wrapped)
	}
	insertBlob(hashA, "media/enc/a", w.seal(w.oldC, fileKeyA, secretctx.FileKey(hashA)))
	insertBlob(hashB, "media/enc/b", w.seal(w.oldC, fileKeyB, secretctx.FileKey(hashB)))
	insertBlob(hashPlain, "", "")
	w.exec(`INSERT INTO media_uploads (id, created_by, name, content_type, size_bytes, chunk_bytes, created_at, expires_at, purpose, wrapped_key)
VALUES ($1, $2, 'u', 't', 10, 4, $3, $3, '', $4)`, uploadID, id(70), w.clock, w.seal(w.oldC, "upload-key", secretctx.UploadKey(uploadID)))

	uc := keyrotation.New(w.repo, keyrotation.Rings{keyRotationRepo.FamilyFiles: w.ring})
	rep, err := uc.Run(w.ctx, keyRotationRepo.FamilyFiles, keyrotation.Options{})
	require.NoError(t, err)
	require.Zero(t, rep.Failed())

	wrapped := func(hash string) string {
		var ct string
		require.NoError(t, w.pool.QueryRow(w.ctx, `SELECT wrapped_key FROM file_blobs WHERE content_hash = $1`, hash).Scan(&ct))
		return ct
	}
	require.Equal(t, "k2", keyOf(wrapped(hashA)))
	require.Equal(t, "k2", keyOf(wrapped(hashB)))
	require.Empty(t, wrapped(hashPlain), "a plain blob has no wrapped key to move")

	// The key of the file is the same after the move; only its wrapping changed.
	opened, err := w.ring.DecryptWithContext(wrapped(hashA), secretctx.FileKey(hashA))
	require.NoError(t, err)
	require.Equal(t, fileKeyA, string(opened))

	var upload string
	require.NoError(t, w.pool.QueryRow(w.ctx, `SELECT wrapped_key FROM media_uploads WHERE id = $1`, uploadID).Scan(&upload))
	require.Equal(t, "k2", keyOf(upload))

	// The objects are untouched: the pass never reads or writes the bucket.
	var object string
	require.NoError(t, w.pool.QueryRow(w.ctx, `SELECT object_key FROM file_blobs WHERE content_hash = $1`, hashA).Scan(&object))
	require.Equal(t, "media/enc/a", object)
}
