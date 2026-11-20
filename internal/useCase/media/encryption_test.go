package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func uploadPublic(t *testing.T, uc *media.MediaUseCase, data []byte) mediaModel.File {
	t.Helper()
	f, err := uc.UploadPublicImage(context.Background(), "logo.png", "image/png", bytes.NewReader(data), uuid.Nil)
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
	f := uploadPublic(t, uc, data)
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
	pub := uploadPublic(t, uc, data)
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
	pub := uploadPublic(t, uc, data)
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
	if _, err := uc.UploadPublicImage(context.Background(), "a", "image/png", bytes.NewReader(pattern(10)), uuid.Nil); err != nil {
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
