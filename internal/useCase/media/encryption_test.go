package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/config"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	"github.com/cybericebox/daemon/internal/testhelpers"
	media "github.com/cybericebox/daemon/internal/useCase/media"
	"github.com/cybericebox/daemon/pkg/filecrypt"
	"github.com/cybericebox/daemon/pkg/secret"
)

var encCfg = config.MediaConfig{MaxUploadBytes: 1 << 30, UploadChunkBytes: 70000, UploadTTL: time.Hour, GCGrace: time.Hour}

// pattern is n bytes of fixed content that never repeats within one frame, so a misplaced frame shows.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i/251 + 1)
	}
	return b
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func encUC(t *testing.T) (*media.MediaUseCase, *memQueries, *fakeStorage) {
	t.Helper()
	q, st := newMemQueries(), newFakeStorage()
	return media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: st, FilesSealer: testhelpers.FilesSealer(), Config: encCfg}), q, st
}

func streamAll(t *testing.T, uc *media.MediaUseCase, id uuid.UUID) []byte {
	t.Helper()
	rc, _, err := uc.StreamFile(context.Background(), id)
	if err != nil {
		t.Fatalf("StreamFile: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return got
}

func upload(t *testing.T, uc *media.MediaUseCase, data []byte) mediaModel.File {
	t.Helper()
	f, err := uc.UploadFile(context.Background(), "f.bin", "application/octet-stream", bytes.NewReader(data), uuid.Nil)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	return f
}

func uploadPublic(t *testing.T, uc *media.MediaUseCase, refType string, data []byte) mediaModel.File {
	t.Helper()
	f, err := uc.UploadPublicImage(context.Background(), refType, "logo.png", "image/png", bytes.NewReader(data), uuid.Nil)
	if err != nil {
		t.Fatalf("UploadPublicImage: %v", err)
	}
	return f
}

// A private file is stored encrypted under a random name, holds no plain byte, and every read path returns the plain bytes.
func TestPrivateUploadIsEncryptedAndReadsBack(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(3*filecrypt.FrameSize + 10)
	f := upload(t, uc, data)

	blob := q.blob(f.ContentHash)
	if blob.WrappedKey == "" || !strings.HasPrefix(blob.ObjectKey, "media/enc/") {
		t.Fatalf("blob %+v", blob)
	}
	if len(st.objects) != 1 {
		t.Fatalf("only the blob object may remain, got %d", len(st.objects))
	}
	object := st.objects[blob.ObjectKey]
	if bytes.Contains(object, data[1000:1064]) || int64(len(object)) != filecrypt.EncryptedSize(int64(len(data))) {
		t.Fatal("the stored object holds plain bytes or has the wrong size")
	}
	if got := streamAll(t, uc, f.ID); !bytes.Equal(got, data) {
		t.Fatal("StreamFile does not return the plain bytes")
	}

	// A random access read fetches ranges of the object, never the whole of it.
	r, _, err := uc.OpenFileAt(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, off := range []int64{0, filecrypt.FrameSize - 3, 2*filecrypt.FrameSize + 5, int64(len(data)) - 4} {
		buf := make([]byte, 4)
		if _, err = r.ReadAt(buf, off); err != nil || !bytes.Equal(buf, data[off:off+4]) {
			t.Fatalf("ReadAt %d: %v", off, err)
		}
	}
	if st.ranges == 0 {
		t.Fatal("random access did not use range reads")
	}

	// The chunks the browser downloads one by one.
	rc, _, length, err := uc.ReadFileChunk(context.Background(), f.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	chunk, _ := io.ReadAll(rc)
	_ = rc.Close()
	if length != 70000 || !bytes.Equal(chunk, data[70000:140000]) {
		t.Fatalf("chunk 1: length %d", length)
	}
}

func TestEqualPrivateFilesShareOneObject(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(5000)
	a, b := upload(t, uc, data), upload(t, uc, data)
	if a.ID == b.ID || len(st.objects) != 1 {
		t.Fatalf("two files must share one stored object, got %d", len(st.objects))
	}
	if !bytes.Equal(streamAll(t, uc, a.ID), data) || !bytes.Equal(streamAll(t, uc, b.ID), data) {
		t.Fatal("both files must read back")
	}
	if q.blob(a.ContentHash).WrappedKey == "" {
		t.Fatal("the shared blob must be encrypted")
	}
}

// An empty file is a valid file too.
func TestEmptyPrivateFile(t *testing.T) {
	uc, _, _ := encUC(t)
	f := upload(t, uc, nil)
	if got := streamAll(t, uc, f.ID); len(got) != 0 {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestPublicImageStaysPlainAtItsContentAddress(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(3000)
	f := uploadPublic(t, uc, mediaModel.RefTypeEventLogo, data)
	blob := q.blob(f.ContentHash)
	if blob.WrappedKey != "" || blob.ObjectKey != "" {
		t.Fatalf("a public image is a plain blob at the older place: %+v", blob)
	}
	if !bytes.Equal(st.objects[mediaModel.BlobKey(f.ContentHash)], data) || len(st.objects) != 1 {
		t.Fatal("the public image must be stored plain under its content address")
	}
	if !bytes.Equal(streamAll(t, uc, f.ID), data) {
		t.Fatal("read back")
	}
}

// Equal bytes uploaded as a public image and then as a private file: the private file is never left plain.
func TestPrivateUploadMovesAPlainBlobToAnEncryptedObject(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(3000)
	pub := uploadPublic(t, uc, mediaModel.RefTypeEventLogo, data)
	priv := upload(t, uc, data)

	blob := q.blob(pub.ContentHash)
	if blob.WrappedKey == "" || !strings.HasPrefix(blob.ObjectKey, "media/enc/") {
		t.Fatalf("the blob must now be encrypted: %+v", blob)
	}
	if _, plainLeft := st.objects[mediaModel.BlobKey(pub.ContentHash)]; plainLeft || len(st.objects) != 1 {
		t.Fatalf("the plain object must be gone, %d objects", len(st.objects))
	}
	if !bytes.Equal(streamAll(t, uc, pub.ID), data) || !bytes.Equal(streamAll(t, uc, priv.ID), data) {
		t.Fatal("both files must read back from the encrypted object")
	}
}

func TestPublicUploadReusesAnEncryptedBlob(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(3000)
	priv := upload(t, uc, data)
	pub := uploadPublic(t, uc, mediaModel.RefTypeEventLogo, data)
	if len(st.objects) != 1 || q.blob(priv.ContentHash).WrappedKey == "" {
		t.Fatalf("no second object may be stored, got %d", len(st.objects))
	}
	if !bytes.Equal(streamAll(t, uc, pub.ID), data) {
		t.Fatal("read back")
	}
}

// Two uploads of the same file race: the row written first stands, and the loser removes the object it stored.
func TestLostRaceRemovesTheLosersObject(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(2000)
	hash := hashOf(data)
	q.beforeCreate = func(arg postgresArg) {
		// The other writer's row appears just before ours.
		q.blobs[arg.ContentHash] = &blobRowOf{ContentHash: hash, SizeBytes: int64(len(data)), ObjectKey: "media/enc/winner", WrappedKey: "v1:t1:winner"}
		q.beforeCreate = nil
	}
	upload(t, uc, data)
	if len(st.objects) != 0 {
		t.Fatalf("the loser must remove its object and the tmp object, %d left", len(st.objects))
	}
	if q.blob(hash).ObjectKey != "media/enc/winner" {
		t.Fatal("the first writer's blob must stand")
	}
}

// What was stored before encryption existed is served as it always was.
func TestLegacyPlainFileIsServed(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(777)
	hash := hashOf(data)
	st.objects[mediaModel.BlobKey(hash)] = data
	row, _ := q.CreateFile(context.Background(), createParams(uuid.Must(uuid.NewV7()), hash, int64(len(data))))
	if !bytes.Equal(streamAll(t, uc, row.ID), data) {
		t.Fatal("a plain blob must be read as it is")
	}
}

func TestWithoutAFilesKeyOnlyPublicImagesAreStored(t *testing.T) {
	q, st := newMemQueries(), newFakeStorage()
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: st, Config: encCfg})
	if _, err := uc.UploadFile(context.Background(), "a", "b", bytes.NewReader(pattern(10)), uuid.Nil); err == nil {
		t.Fatal("a private upload without a key must be refused")
	}
	if _, err := uc.StartUpload(context.Background(), "a", "", 10, uuid.Must(uuid.NewV7()), mediaModel.UploadPurposeFile); err == nil {
		t.Fatal("a chunked upload without a key must be refused")
	}
	if len(st.objects) != 0 {
		t.Fatalf("nothing may be stored, got %d objects", len(st.objects))
	}
	if _, err := uc.UploadPublicImage(context.Background(), mediaModel.RefTypeEventLogo, "a", "image/png", bytes.NewReader(pattern(10)), uuid.Nil); err != nil {
		t.Fatalf("a public image needs no key: %v", err)
	}
}

// Another master key cannot open a file: the wrapped key does not unwrap.
func TestAnotherKeyCannotReadAFile(t *testing.T) {
	uc, q, st := encUC(t)
	f := upload(t, uc, pattern(500))
	other, err := secret.New("t1:" + strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	uc2 := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: st, FilesSealer: other, Config: encCfg})
	if _, _, err = uc2.StreamFile(context.Background(), f.ID); err == nil {
		t.Fatal("a wrong key must not open the file")
	}
}

// A damaged object is never returned as if it were whole.
func TestDamagedObjectFailsWhileReading(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(2*filecrypt.FrameSize + 1)
	f := upload(t, uc, data)
	object := st.objects[q.blob(f.ContentHash).ObjectKey]
	object[filecrypt.HeaderSize+filecrypt.FrameSize+filecrypt.TagSize+5] ^= 1
	rc, _, err := uc.StreamFile(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); err == nil {
		t.Fatal("a damaged frame was accepted")
	}
}

func TestGCRemovesTheEncryptedObjectAndItsKey(t *testing.T) {
	uc, q, st := encUC(t)
	f := upload(t, uc, pattern(900))
	delete(q.files, f.ID) // no file uses the blob any more
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.objects) != 0 || q.blob(f.ContentHash).ContentHash != "" {
		t.Fatalf("the object and the blob row (with its wrapped key) must be gone: %d objects", len(st.objects))
	}
}

// A resumable upload: its chunks are encrypted, random access reads the chunk objects, and the completed file is encrypted.
func TestChunkedUploadIsEncryptedEndToEnd(t *testing.T) {
	uc, q, st := encUC(t)
	owner := uuid.Must(uuid.NewV7())
	// Chunks 0 and 1 hold the same plain bytes: with a key shared by the upload, only the stream number tells them apart.
	chunk := pattern(70000)
	data := append(append(append([]byte{}, chunk...), chunk...), pattern(4321)...)
	up, err := uc.StartUpload(context.Background(), "big.bin", "application/octet-stream", int64(len(data)), owner, mediaModel.UploadPurposeFile)
	if err != nil || up.ChunkCount() != 3 || up.WrappedKey == "" {
		t.Fatalf("upload %+v err %v", up, err)
	}
	for i := 0; i < 3; i++ {
		part := data[i*70000 : min((i+1)*70000, len(data))]
		if _, err = uc.PutChunk(context.Background(), up.ID, owner, i, bytes.NewReader(part)); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	c0, c1 := st.objects[mediaModel.ChunkKey(up.ID, 0)], st.objects[mediaModel.ChunkKey(up.ID, 1)]
	if bytes.Contains(c0, chunk[100:164]) || bytes.Equal(c0, c1) {
		t.Fatal("the chunk objects must be encrypted, each under its own stream")
	}

	r, _, err := uc.OpenUploadAt(context.Background(), up.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	if _, err = r.ReadAt(buf, 69900); err != nil || !bytes.Equal(buf, data[69900:70100]) {
		t.Fatalf("a read across two chunk objects: %v", err)
	}

	rc, _, err := uc.OpenUpload(context.Background(), up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if all, _ := io.ReadAll(rc); !bytes.Equal(all, data) {
		t.Fatal("OpenUpload must return the plain bytes")
	}

	f, err := uc.CompleteUpload(context.Background(), up.ID, owner, hashOf(data))
	if err != nil {
		t.Fatal(err)
	}
	if blob := q.blob(f.ContentHash); blob.WrappedKey == "" || !strings.HasPrefix(blob.ObjectKey, "media/enc/") {
		t.Fatalf("the completed file must be encrypted: %+v", blob)
	}
	if len(st.objects) != 1 || !bytes.Equal(streamAll(t, uc, f.ID), data) {
		t.Fatalf("only the blob remains and it reads back: %d objects", len(st.objects))
	}
}

// An upload that was started before encryption existed finishes as before, then the file is encrypted.
func TestUploadStartedBeforeEncryptionStillCompletes(t *testing.T) {
	uc, q, st := encUC(t)
	owner, id := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	data := pattern(100000)
	q.uploads[id] = postgresUpload(id, owner, int64(len(data)), 70000, 1) // chunk 0 is already stored, plain
	st.objects[mediaModel.ChunkKey(id, 0)] = data[:70000]
	if _, err := uc.PutChunk(context.Background(), id, owner, 1, bytes.NewReader(data[70000:])); err != nil {
		t.Fatal(err)
	}
	f, err := uc.CompleteUpload(context.Background(), id, owner, hashOf(data))
	if err != nil {
		t.Fatal(err)
	}
	if q.blob(f.ContentHash).WrappedKey == "" || !bytes.Equal(streamAll(t, uc, f.ID), data) {
		t.Fatal("the completed file must be encrypted and read back")
	}
}

type recordedProgress struct {
	states []string
	last   secret.Progress
}

func (r *recordedProgress) SaveProgress(_ context.Context, p secret.Progress, state string, _ time.Time) error {
	r.states = append(r.states, state)
	r.last = p
	return nil
}

// The pass encrypts every plain blob except the public images; a blob that does not match its hash is left and counted.
func TestEncryptLegacyBlobs(t *testing.T) {
	uc, q, st := encUC(t)
	progress := &recordedProgress{}
	uc.SetProgressStore(progress)
	ctx := context.Background()

	plain := func(data []byte, kind string) mediaModel.File {
		hash := hashOf(data)
		st.objects[mediaModel.BlobKey(hash)] = append([]byte{}, data...)
		row, _ := q.CreateFile(ctx, createParams(uuid.Must(uuid.NewV7()), hash, int64(len(data))))
		if kind != "" {
			_ = q.ReplaceFileReferences(ctx, replaceRefs(kind, row.ID))
		}
		return mediaModel.File{ID: row.ID, ContentHash: hash}
	}
	task := plain(pattern(70000), "exercise_version") // a task file: encrypted
	unowned := plain(pattern(1234), "")               // no owner yet: encrypted
	logo := plain(pattern(4321), mediaModel.RefTypeEventLogo)
	corrupt := plain(pattern(999), "exercise_version")
	st.objects[mediaModel.BlobKey(corrupt.ContentHash)][3] ^= 1 // no longer matches its hash

	got, err := uc.EncryptLegacyBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Done != 2 || got.Failed != 1 {
		t.Fatalf("progress %+v", got)
	}
	for _, f := range []mediaModel.File{task, unowned} {
		blob := q.blob(f.ContentHash)
		if blob.WrappedKey == "" || !strings.HasPrefix(blob.ObjectKey, "media/enc/") {
			t.Fatalf("not encrypted: %+v", blob)
		}
		if _, left := st.objects[mediaModel.BlobKey(f.ContentHash)]; left {
			t.Fatal("the plain object must be removed after the switch")
		}
		if !bytes.Equal(streamAll(t, uc, f.ID), pattern(map[string]int{task.ContentHash: 70000, unowned.ContentHash: 1234}[f.ContentHash])) {
			t.Fatal("the encrypted file must read back")
		}
	}
	if q.blob(logo.ContentHash).WrappedKey != "" || st.objects[mediaModel.BlobKey(logo.ContentHash)] == nil {
		t.Fatal("a public image must stay plain")
	}
	if q.blob(corrupt.ContentHash).WrappedKey != "" || st.objects[mediaModel.BlobKey(corrupt.ContentHash)] == nil {
		t.Fatal("a blob that does not match its hash must be left as it is")
	}
	if len(st.objects) != 4 { // logo, corrupt, and the two encrypted ones
		t.Fatalf("no stray object may remain: %d", len(st.objects))
	}
	if last := progress.states[len(progress.states)-1]; last != "finished_with_failures" || progress.states[0] != "running" {
		t.Fatalf("progress states %v", progress.states)
	}

	// Run again: nothing left to move but the broken blob, which is tried and counted again.
	again, err := uc.EncryptLegacyBlobs(ctx)
	if err != nil || again.Total != 1 || again.Done != 0 || again.Failed != 1 {
		t.Fatalf("second pass %+v %v", again, err)
	}
}

// A pass that is cancelled part way can simply run again.
func TestEncryptLegacyBlobsResumesAfterAnInterruption(t *testing.T) {
	_, q, st := encUC(t)
	ctx, cancel := context.WithCancel(context.Background())
	var files []mediaModel.File
	for i := 0; i < 3; i++ {
		data := pattern(100 + i)
		hash := hashOf(data)
		st.objects[mediaModel.BlobKey(hash)] = data
		row, _ := q.CreateFile(ctx, createParams(uuid.Must(uuid.NewV7()), hash, int64(len(data))))
		files = append(files, mediaModel.File{ID: row.ID, ContentHash: hash})
	}
	// The context ends while the first object is being written.
	cancelling := &cancelOnPut{fakeStorage: st, cancel: cancel}
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: cancelling, FilesSealer: testhelpers.FilesSealer(), Config: encCfg})
	if _, err := uc.EncryptLegacyBlobs(ctx); err == nil {
		t.Fatal("an interrupted pass must report it")
	}
	cancelling.cancel = func() {}
	done, err := uc.EncryptLegacyBlobs(context.Background())
	if err != nil || done.Failed != 0 {
		t.Fatalf("resumed pass %+v %v", done, err)
	}
	for _, f := range files {
		if q.blob(f.ContentHash).WrappedKey == "" {
			t.Fatal("every blob must be encrypted after the second pass")
		}
	}
	for key := range st.objects {
		if strings.HasPrefix(key, "media/blobs/") {
			t.Fatalf("a plain object is left: %s", key)
		}
	}
}

type cancelOnPut struct {
	*fakeStorage
	cancel func()
	puts   int
}

func (c *cancelOnPut) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) error {
	c.puts++
	if c.puts == 2 {
		c.cancel()
		return context.Canceled
	}
	return c.fakeStorage.Put(ctx, key, r, size, ct)
}

func legacyAvatar(t *testing.T, q *memQueries, st *fakeStorage, deleted bool, data []byte) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	q.users[id] = deleted
	st.objects[mediaModel.LegacyAvatarKey(id)] = data
	return id
}

// A live user's legacy avatar is stored encrypted, the plain object goes, and the reference is published.
func TestMoveLegacyAvatarsMovesALiveUsersAvatar(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(3000)
	id := legacyAvatar(t, q, st, false, data)
	got, err := uc.MoveLegacyAvatars(context.Background())
	if err != nil || got.Done != 1 || got.Failed != 0 {
		t.Fatalf("progress %+v %v", got, err)
	}
	if _, left := st.objects[mediaModel.LegacyAvatarKey(id)]; left {
		t.Fatal("the legacy object must be removed")
	}
	fileID, ok := q.avatars[id]
	if !ok || !bytes.Equal(streamAll(t, uc, fileID), data) {
		t.Fatal("the avatar must be published and read back")
	}
	if blob := q.blob(hashOf(data)); blob.WrappedKey == "" {
		t.Fatal("the moved avatar must be encrypted")
	}
}

// The legacy object of a deleted user is removed and never adopted.
func TestMoveLegacyAvatarsRemovesADeletedUsersObject(t *testing.T) {
	uc, q, st := encUC(t)
	id := legacyAvatar(t, q, st, true, pattern(500))
	if _, err := uc.MoveLegacyAvatars(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.objects) != 0 || len(q.files) != 0 || len(q.avatars) != 0 {
		t.Fatalf("nothing may be kept or adopted for %s: %d objects, %d files", id, len(st.objects), len(q.files))
	}
}

// The owner deletes the account (or the avatar) after the pass copied the object and before it publishes: the photo does not
// come back, no plain copy is left, and the unreferenced copy is for the media GC.
func TestMoveLegacyAvatarsDoesNotRestoreWhatWasDeletedMeanwhile(t *testing.T) {
	usersOwn := uuid.Must(uuid.NewV7())
	cases := map[string]struct {
		intervene func(q *memQueries, id uuid.UUID)
		wantRef   uuid.UUID // the reference the user must end with (Nil: none)
	}{
		"account deleted":    {func(q *memQueries, id uuid.UUID) { q.users[id] = true }, uuid.Nil},
		"avatar set by user": {func(q *memQueries, id uuid.UUID) { q.avatars[id] = usersOwn }, usersOwn},
	}
	for name, c := range cases {
		uc, q, st := encUC(t)
		id := legacyAvatar(t, q, st, false, pattern(800))
		q.beforeAdopt = func(userID uuid.UUID) { c.intervene(q, userID) }
		if _, err := uc.MoveLegacyAvatars(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, left := st.objects[mediaModel.LegacyAvatarKey(id)]; left {
			t.Fatalf("%s: the plain legacy object must be gone", name)
		}
		if got := q.avatars[id]; got != c.wantRef {
			t.Fatalf("%s: the reference is %s, want %s", name, got, c.wantRef)
		}
	}
}

// Once a pass ended clean, the next run does not probe again.
func TestMoveLegacyAvatarsOnlyRunsUntilItIsDone(t *testing.T) {
	uc, q, st := encUC(t)
	legacyAvatar(t, q, st, false, pattern(100))
	if _, err := uc.MoveLegacyAvatars(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := legacyAvatar(t, q, st, false, pattern(101))
	if got, _ := uc.MoveLegacyAvatars(context.Background()); got.Total != 0 || st.objects[mediaModel.LegacyAvatarKey(late)] == nil {
		t.Fatalf("a finished pass must not run again: %+v", got)
	}
}

// A chunk that is written again (a retry after a lost answer, or two requests for the same chunk) with different bytes is a
// different write: it must not reuse the key and nonce of the first, or the two objects XOR to the XOR of the plain bytes.
func TestAChunkWrittenAgainDoesNotReuseTheNonceSpace(t *testing.T) {
	uc, q, st := encUC(t)
	owner := uuid.Must(uuid.NewV7())
	up, err := uc.StartUpload(context.Background(), "a.bin", "application/octet-stream", 140000, owner, mediaModel.UploadPurposeFile)
	if err != nil {
		t.Fatal(err)
	}
	key := mediaModel.ChunkKey(up.ID, 0)
	if _, err = uc.PutChunk(context.Background(), up.ID, owner, 0, bytes.NewReader(pattern(70000))); err != nil {
		t.Fatal(err)
	}
	first := append([]byte{}, st.objects[key]...)

	// The answer was lost and the advance never happened: the same index comes again, with other bytes.
	u := q.uploads[up.ID]
	u.ChunksReceived = 0
	q.uploads[up.ID] = u
	other := bytes.Repeat([]byte{7}, 70000)
	if _, err = uc.PutChunk(context.Background(), up.ID, owner, 0, bytes.NewReader(other)); err != nil {
		t.Fatal(err)
	}
	second := st.objects[key]
	if bytes.Equal(first[8:filecrypt.HeaderSize], second[8:filecrypt.HeaderSize]) {
		t.Fatal("both writes of the chunk used the same salt")
	}
	// With a shared key and nonce the first frame would XOR to the XOR of the plain bytes; it must not.
	frame1, frame2 := first[filecrypt.HeaderSize:filecrypt.HeaderSize+64], second[filecrypt.HeaderSize:filecrypt.HeaderSize+64]
	p1, p2 := pattern(70000)[:64], other[:64]
	same := true
	for i := range frame1 {
		same = same && frame1[i]^frame2[i] == p1[i]^p2[i]
	}
	if same {
		t.Fatal("the two writes share a keystream")
	}
}

// hookStorage lets a test act when the use case removes an object, or make a removal fail.
type hookStorage struct {
	*fakeStorage
	onRemove   func(key string)
	failRemove map[string]bool
	gets       map[string]int
}

func (h *hookStorage) Get(ctx context.Context, key string) (io.ReadCloser, string, error) {
	if h.gets == nil {
		h.gets = map[string]int{}
	}
	h.gets[key]++
	return h.fakeStorage.Get(ctx, key)
}

func (h *hookStorage) Remove(ctx context.Context, key string) error {
	if h.onRemove != nil {
		h.onRemove(key)
	}
	if h.failRemove[key] {
		return io.ErrClosedPipe
	}
	return h.fakeStorage.Remove(ctx, key)
}

// A public image and a private upload of the same bytes race and the private row wins: the plain object the public upload just
// stored is a plain copy of a private file, and it is removed.
func TestAPublicCopyThatLostToAPrivateRowIsRemoved(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(1500)
	hash := hashOf(data)
	q.beforeCreate = func(arg postgresArg) {
		q.blobs[arg.ContentHash] = &blobRowOf{ContentHash: hash, SizeBytes: int64(len(data)), ObjectKey: "media/enc/winner", WrappedKey: "v1:t1:w"}
		q.beforeCreate = nil
	}
	uploadPublic(t, uc, mediaModel.RefTypeEventLogo, data)
	if _, left := st.objects[mediaModel.BlobKey(hash)]; left {
		t.Fatal("the plain copy of a blob that is encrypted must be removed")
	}
}

// A failed removal is not forgotten: it is kept, and the media GC removes the object once it is past the grace.
func TestAFailedRemovalIsRetriedByTheGC(t *testing.T) {
	q, st := newMemQueries(), newFakeStorage()
	hs := &hookStorage{fakeStorage: st, failRemove: map[string]bool{}}
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: hs, FilesSealer: testhelpers.FilesSealer(), Config: encCfg})
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	uc.SetClock(func() time.Time { return t0 })

	f := upload(t, uc, pattern(700))
	key := q.blob(f.ContentHash).ObjectKey
	delete(q.files, f.ID) // no file uses the blob
	hs.failRemove[key] = true
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.objects[key] == nil || q.blob(f.ContentHash).ContentHash != "" {
		t.Fatal("the row is gone and the object is still there, kept for a retry")
	}
	if _, listed := q.removals[key]; !listed {
		t.Fatal("the failed removal must be remembered")
	}

	hs.failRemove[key] = false
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil || st.objects[key] == nil {
		t.Fatalf("an entry younger than the grace is not retried yet: %v", err)
	}
	uc.SetClock(func() time.Time { return t0.Add(2 * time.Hour) })
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.objects[key] != nil || len(q.removals) != 0 {
		t.Fatal("the object must be removed and the entry forgotten")
	}
}

// A retry never removes a plain object that is live again under the same content address.
func TestARemovalRetryLeavesALivePlainBlob(t *testing.T) {
	uc, q, st := encUC(t)
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	uc.SetClock(func() time.Time { return t0.Add(48 * time.Hour) })
	pub := uploadPublic(t, uc, mediaModel.RefTypeEventLogo, pattern(300))
	q.removals[mediaModel.BlobKey(pub.ContentHash)] = t0
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.objects[mediaModel.BlobKey(pub.ContentHash)] == nil || len(q.removals) != 0 {
		t.Fatal("a live plain blob must stay, and only the entry goes")
	}
}

// The collector removed the blob an upload had found: the row is not written as an empty blob; the upload starts again and
// stores its own object.
func TestABlobCollectedAfterItWasFoundIsNotResurrectedEmpty(t *testing.T) {
	uc, q, st := encUC(t)
	data := pattern(900)
	first := upload(t, uc, data)
	q.beforeCreate = func(arg postgresArg) { // the collector runs between the look-up and the row
		old := q.blobs[arg.ContentHash]
		delete(st.objects, old.ObjectKey)
		delete(q.blobs, arg.ContentHash)
		q.beforeCreate = nil
	}
	second := upload(t, uc, data)
	blob := q.blob(second.ContentHash)
	if blob.WrappedKey == "" || blob.ObjectKey == "" || st.objects[blob.ObjectKey] == nil {
		t.Fatalf("the blob must be stored again, encrypted: %+v", blob)
	}
	if !bytes.Equal(streamAll(t, uc, second.ID), data) {
		t.Fatal("the new file must read back")
	}
	_ = first
}

// The pass moves a blob while the collector is removing its stale plain key: the collector must not delete the row that now
// names the encrypted object; the next pass collects the encrypted object by its own key.
func TestCollectorAndEncryptionPassDoNotOrphanAnObject(t *testing.T) {
	q, st := newMemQueries(), newFakeStorage()
	hs := &hookStorage{fakeStorage: st}
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: hs, FilesSealer: testhelpers.FilesSealer(), Config: encCfg})
	uc.SetClock(func() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) })
	data := pattern(400)
	hash := hashOf(data)
	st.objects[mediaModel.BlobKey(hash)] = append([]byte{}, data...)
	_, _ = q.CreateFile(context.Background(), createParams(uuid.Must(uuid.NewV7()), hash, int64(len(data))))
	for id := range q.files {
		delete(q.files, id) // an orphan blob
	}
	hs.onRemove = func(key string) { // while the collector removes the stale plain key, the pass moves the blob
		hs.onRemove = nil
		if _, err := uc.EncryptLegacyBlobs(context.Background()); err != nil {
			t.Error(err)
		}
	}
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	blob := q.blob(hash)
	if blob.WrappedKey == "" {
		t.Fatal("the row that names the encrypted object must survive the collector's stale decision")
	}
	if err := uc.CleanupOrphanFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.objects) != 0 || q.blob(hash).ContentHash != "" {
		t.Fatalf("the next pass must collect the encrypted object by its own key: %d objects left", len(st.objects))
	}
}

// A call for a public image refuses a kind of file that is not public: it cannot be used to store a private file plain.
func TestUploadPublicImageRefusesAKindThatIsNotPublic(t *testing.T) {
	uc, _, st := encUC(t)
	for _, kind := range []string{"exercise_version", "user_avatar", "event_answer_file", ""} {
		_, err := uc.UploadPublicImage(context.Background(), kind, "a", "image/png", bytes.NewReader(pattern(10)), uuid.Nil)
		if !errors.Is(err, mediaModel.ErrUploadInvalid.Err()) {
			t.Fatalf("%q: %v", kind, err)
		}
	}
	if len(st.objects) != 0 {
		t.Fatalf("nothing may be stored: %d objects", len(st.objects))
	}
	for _, kind := range mediaModel.PublicRefTypes {
		if _, err := uc.UploadPublicImage(context.Background(), kind, "a", "image/png", bytes.NewReader(pattern(10)), uuid.Nil); err != nil {
			t.Fatalf("%q must be accepted: %v", kind, err)
		}
	}
}

// A blob that cannot be encrypted is not read again every hour: it is tried again after a growing delay, and still counted as failed.
func TestABrokenBlobIsRetriedWithABackoff(t *testing.T) {
	q, st := newMemQueries(), newFakeStorage()
	hs := &hookStorage{fakeStorage: st}
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: hs, FilesSealer: testhelpers.FilesSealer(), Config: encCfg})
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	uc.SetClock(func() time.Time { return now })
	data := pattern(5000)
	hash := hashOf(data)
	st.objects[mediaModel.BlobKey(hash)] = append([]byte{}, data...)
	st.objects[mediaModel.BlobKey(hash)][1] ^= 1 // no longer matches its hash
	_, _ = q.CreateFile(context.Background(), createParams(uuid.Must(uuid.NewV7()), hash, int64(len(data))))

	run := func() int {
		got, err := uc.EncryptLegacyBlobs(context.Background())
		if err != nil || got.Failed != 1 || got.Total != 1 {
			t.Fatalf("pass %+v %v", got, err)
		}
		return hs.gets[mediaModel.BlobKey(hash)]
	}
	if reads := run(); reads != 1 {
		t.Fatalf("first pass reads the object once: %d", reads)
	}
	now = now.Add(time.Hour) // the hourly job comes again: still inside the delay
	if reads := run(); reads != 1 {
		t.Fatalf("a blob in its delay is not read again: %d", reads)
	}
	now = now.Add(2 * time.Hour)
	if reads := run(); reads != 2 {
		t.Fatalf("after the delay it is tried again: %d", reads)
	}
	now = now.Add(2 * time.Hour) // the second delay is longer (4 hours)
	if reads := run(); reads != 2 {
		t.Fatalf("the delay grows: %d", reads)
	}
}

// The owner removes the avatar between the pass's read and its adopt: RemoveAvatar clears the picture first, and the adopt
// sees that, so the photo does not come back. No retry publishes it later, the legacy object is gone, and the copy is
// left unreferenced for the media GC.
func TestRemovalBetweenReadAndAdoptLeavesNoAvatar(t *testing.T) {
	uc, q, st := encUC(t)
	id := legacyAvatar(t, q, st, false, pattern(900))
	adopts := 0
	q.beforeAdopt = func(userID uuid.UUID) {
		adopts++
		q.noPicture[userID] = true // RemoveAvatar's first write has committed
	}
	if _, err := uc.MoveLegacyAvatars(context.Background()); err != nil {
		t.Fatal(err)
	}
	if adopts != 1 {
		t.Fatalf("the adopt is tried once, never again after a refusal: %d", adopts)
	}
	if _, ok := q.avatars[id]; ok {
		t.Fatal("the removed photo came back")
	}
	if _, left := st.objects[mediaModel.LegacyAvatarKey(id)]; left {
		t.Fatal("the legacy object must be removed")
	}
	if len(q.files) != 1 {
		t.Fatalf("the uploaded copy exists and is unreferenced (for the media GC): %d files", len(q.files))
	}
	for fileID := range q.files {
		for _, ref := range q.avatars {
			if ref == fileID {
				t.Fatal("the copy must not be referenced")
			}
		}
	}
}

// The account is deleted between the read and the adopt: the same, and nothing is adopted.
func TestAccountDeletionBetweenReadAndAdoptLeavesNoAvatar(t *testing.T) {
	uc, q, st := encUC(t)
	id := legacyAvatar(t, q, st, false, pattern(900))
	q.beforeAdopt = func(userID uuid.UUID) { q.users[userID] = true }
	if _, err := uc.MoveLegacyAvatars(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.avatars[id]; ok {
		t.Fatal("a deleted account must not get an avatar")
	}
	if _, left := st.objects[mediaModel.LegacyAvatarKey(id)]; left {
		t.Fatal("the legacy object of a deleted account must be removed")
	}
}

// A crash after the reference is published and before the legacy object is removed leaves a duplicate: the next pass cleans
// it, and the reference that was published stands (the photo is neither lost nor replaced).
func TestACrashBetweenPublishAndLegacyDeleteIsCleanedByTheNextPass(t *testing.T) {
	q, st := newMemQueries(), newFakeStorage()
	hs := &hookStorage{fakeStorage: st}
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: hs, FilesSealer: testhelpers.FilesSealer(), Config: encCfg})
	id := legacyAvatar(t, q, st, false, pattern(600))
	legacyKey := mediaModel.LegacyAvatarKey(id)
	hs.failRemove = map[string]bool{legacyKey: true} // the process dies (or the delete fails) right after the publish

	got, err := uc.MoveLegacyAvatars(context.Background())
	if err != nil || got.Failed != 1 {
		t.Fatalf("first pass %+v %v", got, err)
	}
	published, ok := q.avatars[id]
	if !ok || st.objects[legacyKey] == nil {
		t.Fatal("the reference is published and the legacy object is still there")
	}

	hs.failRemove = nil
	if got, err = uc.MoveLegacyAvatars(context.Background()); err != nil || got.Failed != 0 {
		t.Fatalf("second pass %+v %v", got, err)
	}
	if st.objects[legacyKey] != nil {
		t.Fatal("the next pass must remove the duplicate")
	}
	if q.avatars[id] != published {
		t.Fatal("the published avatar must stand, not be replaced by the duplicate")
	}
}
