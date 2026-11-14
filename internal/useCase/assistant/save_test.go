package assistant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain/brainfake"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	"github.com/cybericebox/daemon/internal/model/rbac"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

var (
	taskID  = uuid.FromStringOrNil("33333333-3333-4333-8333-333333333333")
	jobID   = uuid.FromStringOrNil("44444444-4444-4444-8444-444444444444")
	fileID  = uuid.FromStringOrNil("55555555-5555-4555-8555-555555555555")
	archive = []byte("PK-the-finished-task")
	actor   = exerciseUseCase.Actor{UserID: userOne, Role: rbac.Role("")}
)

// timeline records the order of everything the save flow does, at the brain and at the importer.
type timeline struct {
	mu     sync.Mutex
	events []string
}

func (l *timeline) add(e string) { l.mu.Lock(); defer l.mu.Unlock(); l.events = append(l.events, e) }
func (l *timeline) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}
func (l *timeline) count(e string) int {
	n := 0
	for _, x := range l.list() {
		if x == e {
			n++
		}
	}
	return n
}

type fakeImporter struct {
	mu      sync.Mutex
	line    *timeline
	start   exerciseUseCase.ArchiveImportStart
	err     error
	input   exerciseUseCase.ImportExerciseInput
	authErr error
	queued  exerciseUseCase.QueueImportInput
	job     exerciseUseCase.ImportJobView // what the job is now
	jobErr  error
	delay   time.Duration
}

func (f *fakeImporter) AuthorizeImport(context.Context, exerciseUseCase.Actor, *uuid.UUID) error {
	f.line.add("authorize")
	return f.authErr
}

func (f *fakeImporter) ArchiveLimits(context.Context) exerciseModel.ArchiveLimits {
	return exerciseModel.ArchiveLimits{ArchiveBytes: 1 << 20}
}

func (f *fakeImporter) ImportRawArchive(_ context.Context, _ exerciseUseCase.Actor, in exerciseUseCase.ImportExerciseInput) (exerciseUseCase.ArchiveImportStart, error) {
	f.line.add("import")
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = in
	return f.start, f.err
}

func (f *fakeImporter) QueueArchiveImport(_ context.Context, in exerciseUseCase.QueueImportInput) (uuid.UUID, error) {
	f.line.add("queue")
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = in
	return jobID, nil
}

func (f *fakeImporter) ArchiveImportJob(context.Context, uuid.UUID, uuid.UUID) (exerciseUseCase.ImportJobView, error) {
	f.line.add("poll")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.job, f.jobErr
}

func (f *fakeImporter) setJob(v exerciseUseCase.ImportJobView) { f.mu.Lock(); f.job = v; f.mu.Unlock() }

type fakeFiles struct {
	line *timeline
	body []byte
}

func (f *fakeFiles) UploadFile(_ context.Context, _, _ string, r io.Reader, _ uuid.UUID) (mediaModel.File, error) {
	f.line.add("upload")
	f.body, _ = io.ReadAll(r)
	return mediaModel.File{ID: fileID}, nil
}

// memSaves is the save store in memory; locker is a per-key mutex like the advisory lock.
type memSaves struct {
	mu   sync.Mutex
	rows map[string]assistantModel.Save
}

func key(session string, author uuid.UUID) string { return session + "/" + author.String() }

func (m *memSaves) Get(_ context.Context, session string, author uuid.UUID) (assistantModel.Save, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key(session, author)]
	return r, ok, nil
}

func (m *memSaves) Create(_ context.Context, s assistantModel.Save, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[key(s.Session, s.Author)]; ok {
		return false, nil
	}
	m.rows[key(s.Session, s.Author)] = s
	return true, nil
}

func (m *memSaves) update(session string, author uuid.UUID, f func(*assistantModel.Save)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[key(session, author)]; ok {
		f(&r)
		m.rows[key(session, author)] = r
	}
}

func (m *memSaves) SetExercise(_ context.Context, session string, author, exercise uuid.UUID) error {
	m.update(session, author, func(r *assistantModel.Save) { r.ExerciseID = exercise })
	return nil
}

func (m *memSaves) SetNotified(_ context.Context, session string, author uuid.UUID) error {
	m.update(session, author, func(r *assistantModel.Save) { r.Notified = true })
	return nil
}

func (m *memSaves) Delete(_ context.Context, session string, author uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, key(session, author))
	return nil
}

func (m *memSaves) Unsettled(context.Context, time.Time, int) ([]assistantModel.Save, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.Save
	for _, r := range m.rows {
		if r.Pending() || !r.Notified {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memSaves) row(session string) (assistantModel.Save, bool) {
	r, ok, _ := m.Get(context.Background(), session, userOne)
	return r, ok
}

type locker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (l *locker) lock(_ context.Context, k string) (func(), error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*sync.Mutex{}
	}
	m, ok := l.locks[k]
	if !ok {
		m = &sync.Mutex{}
		l.locks[k] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock, nil
}

type saveRig struct {
	*rig
	line     *timeline
	importer *fakeImporter
	files    *fakeFiles
	saves    *memSaves
	mu       sync.Mutex // guards saved and subjects: the brain handler runs on server goroutines
	saved    []string   // bodies of the brain's /save
	subjects []string
	archive  func(w http.ResponseWriter)
	// brainSaveStatus makes the brain's /save fail with this status (0: ok).
	brainSaveStatus int
}

func newSaveRig(t *testing.T, images bool) *saveRig {
	t.Helper()
	line := &timeline{}
	imp := &fakeImporter{line: line, start: exerciseUseCase.ArchiveImportStart{Exercises: []exerciseUseCase.ExerciseView{{ID: taskID}}}}
	files := &fakeFiles{line: line}
	s := &saveRig{line: line, importer: imp, files: files, saves: &memSaves{rows: map[string]assistantModel.Save{}}}
	s.rig = enrolledRig(t, func(d *assistant.Dependencies) {
		d.Saves, d.Lock = s.saves, (&locker{}).lock
		d.Importer, d.Files = imp, files
		d.NamesImages = func([]byte, exerciseModel.ArchiveLimits) bool { return images }
	})
	s.archive = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive)
	}
	s.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		s.subjects = append(s.subjects, req.Header.Get(assistant.SubjectHeader))
		s.mu.Unlock()
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/assistant/sessions/s1/archive":
			line.add("brain:archive")
			s.archive(w)
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/save"):
			line.add("brain:save")
			b, _ := io.ReadAll(req.Body)
			s.mu.Lock()
			s.saved = append(s.saved, string(b))
			status := s.brainSaveStatus
			s.mu.Unlock()
			if status != 0 {
				brainfake.Fail(w, status, "down", "down")
				return
			}
			_, _ = w.Write([]byte("{}"))
		default:
			brainfake.Fail(w, http.StatusNotFound, "not_found", "no route")
		}
	})
	return s
}

func (s *saveRig) save(t *testing.T) (bool, error) {
	t.Helper()
	return s.uc.SaveSession(context.Background(), actor, "s1")
}

func (s *saveRig) savedBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.saved...)
}

func bodyID(t *testing.T, body string) string {
	t.Helper()
	var sent map[string]string
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("the brain save body holds exactly exercise_id: %q", body)
	}
	return sent["exercise_id"]
}

func TestSaveFetchesImportsThenTellsTheBrainTheExerciseID(t *testing.T) {
	s := newSaveRig(t, false)
	done, err := s.save(t)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if got := s.line.list(); len(got) != 3 || got[0] != "brain:archive" || got[1] != "import" || got[2] != "brain:save" {
		t.Fatalf("order: %v", got)
	}
	if id := bodyID(t, s.savedBodies()[0]); id != taskID.String() {
		t.Fatalf("exercise_id = %q", id)
	}
	if !bytes.Equal(s.importer.input.Archive, archive) || s.importer.input.CreatedBy != userOne || s.importer.input.OwnerEventID != nil {
		t.Fatalf("the archive must be imported as the caller into the catalog: %+v", s.importer.input)
	}
	if r, ok := s.saves.row("s1"); !ok || r.ExerciseID != taskID || !r.Notified {
		t.Fatalf("stored save: %+v %v", r, ok)
	}
	for _, subject := range s.subjects {
		if subject != s.uc.Subject(userOne) {
			t.Fatalf("every brain call carries the caller's subject, got %q", subject)
		}
	}
}

func TestSaveDoesNotTellTheBrainWhenTheImportFailsAndStoresNothing(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	_, err := s.save(t)
	if !errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) {
		t.Fatalf("the import's own error must come back, got %v", err)
	}
	if got := s.line.list(); len(got) != 2 || got[1] != "import" || len(s.savedBodies()) != 0 {
		t.Fatalf("the brain must not be told: %v", got)
	}
	if _, ok := s.saves.row("s1"); ok {
		t.Fatal("a failed import must leave nothing stored")
	}
}

func TestSaveWithABrokenArchiveIsAFailedImportNotAnInternalError(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = errors.New("zip: not a valid zip file")
	if _, err := s.save(t); err == nil || len(s.savedBodies()) != 0 {
		t.Fatalf("err=%v saved=%v", err, s.savedBodies())
	}
}

func TestSaveWithoutAFinishedArchiveStopsBeforeImporting(t *testing.T) {
	s := newSaveRig(t, false)
	s.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		brainfake.Fail(w, http.StatusConflict, "no_archive", "the session has no archive yet")
	})
	if _, err := s.save(t); !errors.Is(err, assistantModel.ErrAssistantNotReady.Err()) {
		t.Fatalf("got %v", err)
	}
	if s.line.count("import") != 0 {
		t.Fatal("nothing may be imported without an archive")
	}
}

func TestSaveRefusesAnArchiveLargerThanTheImportLimit(t *testing.T) {
	s := newSaveRig(t, false)
	s.archive = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20))
	}
	if _, err := s.save(t); err == nil || s.line.count("import") != 0 || len(s.savedBodies()) != 0 {
		t.Fatalf("an oversized archive was accepted: %v", err)
	}
}

func TestSaveTwoTasksInOneArchiveIsAFailedImport(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.start = exerciseUseCase.ArchiveImportStart{Exercises: []exerciseUseCase.ExerciseView{{ID: taskID}, {ID: jobID}}}
	if _, err := s.save(t); !errors.Is(err, assistantModel.ErrAssistantImportFailed.Err()) || len(s.savedBodies()) != 0 {
		t.Fatalf("got %v saved=%v", err, s.savedBodies())
	}
}

func TestRepeatedSaveSkipsFetchAndImportAndTellsTheBrainTheStoredID(t *testing.T) {
	s := newSaveRig(t, false)
	for range 2 {
		if done, err := s.save(t); err != nil || !done {
			t.Fatalf("done=%v err=%v", done, err)
		}
	}
	if got := s.line.list(); len(got) != 4 || got[0] != "brain:archive" || got[1] != "import" || got[2] != "brain:save" || got[3] != "brain:save" {
		t.Fatalf("order: %v", got)
	}
	saved := s.savedBodies()
	if len(saved) != 2 || bodyID(t, saved[0]) != bodyID(t, saved[1]) {
		t.Fatalf("both saves carry the one stored id: %v", saved)
	}
}

func TestSaveAfterAFailedImportImportsAgain(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	if _, err := s.save(t); err == nil {
		t.Fatal("expected the import error")
	}
	s.importer.err = nil
	if done, err := s.save(t); err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if s.line.count("import") != 2 || len(s.savedBodies()) != 1 {
		t.Fatalf("a failed import stores nothing: imports=%d saved=%d", s.line.count("import"), len(s.savedBodies()))
	}
}

func TestSavesOfOneSessionAtOnceImportOnce(t *testing.T) {
	for _, images := range []bool{false, true} {
		s := newSaveRig(t, images)
		s.importer.delay = 50 * time.Millisecond
		s.importer.setJob(exerciseUseCase.ImportJobView{State: exerciseModel.ImportJobRunning})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.save(t)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		imports := s.line.count("import") + s.line.count("queue")
		if imports != 1 {
			t.Fatalf("images=%v: imports = %d, want 1", images, imports)
		}
		if !images && len(s.savedBodies()) != 8 {
			t.Fatalf("every click tells the brain: %d", len(s.savedBodies()))
		}
	}
}

func TestOtherSessionsAreIndependent(t *testing.T) {
	s := newSaveRig(t, false)
	s.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/archive") {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(archive)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	for _, id := range []string{"s1", "s2"} {
		if _, err := s.uc.SaveSession(context.Background(), actor, id); err != nil {
			t.Fatal(err)
		}
	}
	if s.line.count("import") != 2 {
		t.Fatalf("imports = %d", s.line.count("import"))
	}
}

func TestBrainSaveFailingAfterTheImportIsRetriedByTheLeaderWithoutImportingAgain(t *testing.T) {
	s := newSaveRig(t, false)
	s.brainSaveStatus = http.StatusServiceUnavailable
	if _, err := s.save(t); err == nil {
		t.Fatal("the brain refused: the page is told")
	}
	if r, ok := s.saves.row("s1"); !ok || r.ExerciseID != taskID || r.Notified {
		t.Fatalf("the task is stored, not announced: %+v %v", r, ok)
	}
	s.mu.Lock()
	s.brainSaveStatus = 0
	s.mu.Unlock()
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.saves.row("s1"); !r.Notified || s.line.count("import") != 1 {
		t.Fatalf("settle: %+v imports=%d", r, s.line.count("import"))
	}
}

// ── a task with device images: the import runs in the background ───────────────

func running() exerciseUseCase.ImportJobView {
	return exerciseUseCase.ImportJobView{ID: jobID, State: exerciseModel.ImportJobRunning}
}

func done() exerciseUseCase.ImportJobView {
	return exerciseUseCase.ImportJobView{ID: jobID, State: exerciseModel.ImportJobDone, ExerciseIDs: []uuid.UUID{taskID}}
}

func TestSaveWithImagesQueuesTheImportAndReturnsAtOnce(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	d, err := s.save(t)
	if err != nil || d {
		t.Fatalf("queued: done=%v err=%v", d, err)
	}
	if got := s.line.list(); len(got) != 4 || got[0] != "brain:archive" || got[1] != "authorize" || got[2] != "upload" || got[3] != "queue" {
		t.Fatalf("order: %v", got)
	}
	if !bytes.Equal(s.files.body, archive) || s.importer.queued.CreatedBy != userOne || s.importer.queued.OwnerEventID != nil || s.importer.queued.SourceID != fileID {
		t.Fatalf("queued=%+v", s.importer.queued)
	}
	if len(s.savedBodies()) != 0 {
		t.Fatal("the brain is told only when the import is done")
	}
	if r, ok := s.saves.row("s1"); !ok || !r.Pending() || r.JobID != jobID {
		t.Fatalf("the pending job is stored: %+v %v", r, ok)
	}
}

func TestRepeatedSaveWhileTheImportRunsDoesNotQueueAgain(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	for range 3 {
		if d, err := s.save(t); err != nil || d {
			t.Fatalf("done=%v err=%v", d, err)
		}
	}
	if s.line.count("queue") != 1 || s.line.count("brain:archive") != 1 {
		t.Fatalf("one import: %v", s.line.list())
	}
}

func TestLeaderTellsTheBrainWhenTheBackgroundImportIsDone(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	if err := s.uc.SettleSaves(context.Background()); err != nil || len(s.savedBodies()) != 0 {
		t.Fatalf("still running: err=%v saved=%v", err, s.savedBodies())
	}
	s.importer.setJob(done())
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved := s.savedBodies()
	if len(saved) != 1 || bodyID(t, saved[0]) != taskID.String() {
		t.Fatalf("brain save: %v", saved)
	}
	if r, _ := s.saves.row("s1"); r.Pending() || !r.Notified || r.ExerciseID != taskID {
		t.Fatalf("stored save: %+v", r)
	}
	// Settled: the next pass has nothing to do, and a repeated save from the page tells the brain the stored task.
	if err := s.uc.SettleSaves(context.Background()); err != nil || len(s.savedBodies()) != 1 {
		t.Fatalf("settled: %v %v", err, s.savedBodies())
	}
	if d, err := s.save(t); err != nil || !d || len(s.savedBodies()) != 2 || s.line.count("queue") != 1 {
		t.Fatalf("repeat: done=%v err=%v", d, err)
	}
}

func TestRepeatedSaveAfterTheImportFinishedTellsTheBrainWithoutWaitingForTheLeader(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	s.importer.setJob(done())
	if d, err := s.save(t); err != nil || !d {
		t.Fatalf("done=%v err=%v", d, err)
	}
	if saved := s.savedBodies(); len(saved) != 1 || bodyID(t, saved[0]) != taskID.String() || s.line.count("queue") != 1 {
		t.Fatalf("saved=%v", saved)
	}
}

func TestFailedBackgroundImportLeavesTheSaveRetryable(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	s.importer.setJob(exerciseUseCase.ImportJobView{State: exerciseModel.ImportJobFailed, Error: &exerciseModel.ImportJobError{Code: 70995}})
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.saves.row("s1"); ok || len(s.savedBodies()) != 0 {
		t.Fatalf("a failed job clears the save and the brain is not told: saved=%v", s.savedBodies())
	}
	// The page saves again: a new import is queued.
	s.importer.setJob(running())
	if d, err := s.save(t); err != nil || d || s.line.count("queue") != 2 {
		t.Fatalf("retry: done=%v err=%v queues=%d", d, err, s.line.count("queue"))
	}
}

func TestFailedJobSeenByARepeatedSaveStartsAgainAtOnce(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	s.importer.setJob(exerciseUseCase.ImportJobView{State: exerciseModel.ImportJobFailed})
	if d, err := s.save(t); err != nil || d || s.line.count("queue") != 2 {
		t.Fatalf("done=%v err=%v queues=%d", d, err, s.line.count("queue"))
	}
}

func TestSaveWithImagesNeedsImportRights(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.authErr = exerciseModel.ErrExerciseForbidden.Err()
	if _, err := s.save(t); !errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) {
		t.Fatalf("got %v", err)
	}
	if s.line.count("upload") != 0 || s.line.count("queue") != 0 {
		t.Fatal("an unauthorized caller reached the upload or the queue")
	}
	if _, ok := s.saves.row("s1"); ok {
		t.Fatal("nothing is stored")
	}
}

func TestAJobThatIsGoneCountsAsFailed(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	s.importer.mu.Lock()
	s.importer.jobErr = exerciseModel.ErrImportJobNotFound.Err()
	s.importer.mu.Unlock()
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.saves.row("s1"); ok {
		t.Fatal("a vanished job must clear the save")
	}
}
