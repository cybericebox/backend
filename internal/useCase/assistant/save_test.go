package assistant_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain/brainfake"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	mediaModel "github.com/cybericebox/backend/internal/model/media"
	"github.com/cybericebox/backend/internal/model/rbac"
	adminAuditUseCase "github.com/cybericebox/backend/internal/useCase/adminAudit"
	"github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
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
	// during runs while the import is in progress (the row must already be claimed).
	during func()
	// The edit of an existing task (uuid.Nil in editTarget: the archive is a new task).
	editTarget   uuid.UUID
	editErr      error
	previewItems []exerciseUseCase.ArchiveImportPreviewItem
	previewErr   error
	previewIn    exerciseUseCase.PreviewArchiveInput
	commitStart  exerciseUseCase.ArchiveImportStart
	commitErr    error
	commitIn     exerciseUseCase.CommitArchiveInput
}

func (f *fakeImporter) EditedArchiveTarget(context.Context, exerciseUseCase.Actor, []byte, exerciseModel.ArchiveLimits) (uuid.UUID, error) {
	return f.editTarget, f.editErr
}

func (f *fakeImporter) PreviewArchiveImport(_ context.Context, _ exerciseUseCase.Actor, in exerciseUseCase.PreviewArchiveInput) (exerciseUseCase.ArchiveImportPreview, error) {
	f.line.add("preview")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.previewIn = in
	return exerciseUseCase.ArchiveImportPreview{Items: f.previewItems}, f.previewErr
}

func (f *fakeImporter) CommitArchiveImport(_ context.Context, _ exerciseUseCase.Actor, in exerciseUseCase.CommitArchiveInput) (exerciseUseCase.ArchiveImportStart, error) {
	f.line.add("commit")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitIn = in
	return f.commitStart, f.commitErr
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
	if f.during != nil {
		f.during()
	}
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

// memSaves is the save store in memory, with the same claim rules as the table.
type memSaves struct {
	mu   sync.Mutex
	rows map[string]assistantModel.Save
	hash map[string]string
	// completeFails makes the next n Complete calls fail.
	completeFails int
}

func key(session string, author uuid.UUID) string { return session + "/" + author.String() }

func (m *memSaves) Get(_ context.Context, session string, author uuid.UUID) (assistantModel.Save, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key(session, author)]
	return r, ok, nil
}

func (m *memSaves) Claim(_ context.Context, session string, author uuid.UUID, hash string, now, staleBefore time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(session, author)
	if r, ok := m.rows[k]; ok && !(r.State == assistantModel.SaveFailed || (r.State == assistantModel.SavePending && r.JobID == uuid.Nil && r.ClaimedAt.Before(staleBefore))) {
		return false, nil
	}
	m.rows[k] = assistantModel.Save{Session: session, Author: author, State: assistantModel.SavePending, ClaimedAt: now}
	m.hash[k] = hash
	return true, nil
}

func (m *memSaves) update(session string, author uuid.UUID, f func(*assistantModel.Save)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key(session, author)]
	if ok {
		f(&r)
		m.rows[key(session, author)] = r
	}
	return ok
}

func (m *memSaves) SetJob(_ context.Context, session string, author, job uuid.UUID) error {
	m.update(session, author, func(r *assistantModel.Save) {
		if r.State == assistantModel.SavePending {
			r.JobID = job
		}
	})
	return nil
}

func (m *memSaves) Complete(_ context.Context, session string, author, exercise uuid.UUID) (bool, error) {
	m.mu.Lock()
	if m.completeFails > 0 {
		m.completeFails--
		m.mu.Unlock()
		return false, errors.New("database unavailable")
	}
	m.mu.Unlock()
	changed := false
	m.update(session, author, func(r *assistantModel.Save) {
		if r.State == assistantModel.SavePending {
			r.State, r.ExerciseID, changed = assistantModel.SaveDone, exercise, true
		}
	})
	return changed, nil
}

func (m *memSaves) Fail(_ context.Context, session string, author uuid.UUID, code int) error {
	m.update(session, author, func(r *assistantModel.Save) {
		if r.State == assistantModel.SavePending {
			r.State, r.FailureCode = assistantModel.SaveFailed, code
		}
	})
	return nil
}

func (m *memSaves) SetNotified(_ context.Context, session string, author uuid.UUID) error {
	m.update(session, author, func(r *assistantModel.Save) { r.Notified = true })
	return nil
}

func (m *memSaves) Unsettled(context.Context, time.Time, int) ([]assistantModel.Save, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []assistantModel.Save
	for _, r := range m.rows {
		if (r.State == assistantModel.SavePending && r.JobID != uuid.Nil) || ((r.State == assistantModel.SaveDone || r.State == assistantModel.SaveFailed) && !r.Notified) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memSaves) row(session string) (assistantModel.Save, bool) {
	r, ok, _ := m.Get(context.Background(), session, userOne)
	return r, ok
}

// auditLog records the admin audit entries the use case writes.
type auditLog struct {
	mu      sync.Mutex
	entries []adminAuditUseCase.Entry
}

func (a *auditLog) record(_ context.Context, e adminAuditUseCase.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

func (a *auditLog) list() []adminAuditUseCase.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]adminAuditUseCase.Entry(nil), a.entries...)
}

type saveRig struct {
	*rig
	line     *timeline
	importer *fakeImporter
	files    *fakeFiles
	saves    *memSaves
	audit    *auditLog
	mu       sync.Mutex // guards saved and subjects: the brain handler runs on server goroutines
	saved    []string   // bodies of the brain's /save
	subjects []string
	archive  func(w http.ResponseWriter)
	// brainSaveStatus makes the brain's /save fail with this status (0: ok); brainFailedStatus the same for save_failed.
	brainSaveStatus   int
	brainFailedStatus int
	failedBodies      []string
}

func newSaveRig(t *testing.T, images bool) *saveRig {
	t.Helper()
	line := &timeline{}
	imp := &fakeImporter{line: line, start: exerciseUseCase.ArchiveImportStart{Exercises: []exerciseUseCase.ExerciseView{{ID: taskID}}}}
	files := &fakeFiles{line: line}
	s := &saveRig{line: line, importer: imp, files: files, saves: &memSaves{rows: map[string]assistantModel.Save{}, hash: map[string]string{}}, audit: &auditLog{}}
	s.rig = enrolledRig(t, func(d *assistant.Dependencies) {
		d.Saves, d.Audit = s.saves, s.audit.record
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
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/save_failed"):
			line.add("brain:save_failed")
			b, _ := io.ReadAll(req.Body)
			s.mu.Lock()
			s.failedBodies = append(s.failedBodies, string(b))
			status := s.brainFailedStatus
			s.mu.Unlock()
			if status != 0 {
				brainfake.Fail(w, status, "x", "x")
				return
			}
			_, _ = w.Write([]byte("{}"))
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
		if subject != subjectOf(t, s.uc, userOne) {
			t.Fatalf("every brain call carries the caller's subject, got %q", subject)
		}
	}
}

func TestSaveDoesNotTellTheBrainWhenTheImportFails(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	_, err := s.save(t)
	if !errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) {
		t.Fatalf("the import's own error must come back, got %v", err)
	}
	if got := s.line.list(); len(got) != 3 || got[1] != "import" || got[2] != "brain:save_failed" || len(s.savedBodies()) != 0 {
		t.Fatalf("the brain is told of the failure, not of a task: %v", got)
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveFailed || r.ExerciseID != uuid.Nil {
		t.Fatalf("a failed import leaves a failed claim and no task: %+v", r)
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

func TestTheSaveIsClaimedBeforeTheImportRunsAndKeepsTheArchiveHash(t *testing.T) {
	s := newSaveRig(t, false)
	var during assistantModel.Save
	s.importer.during = func() { during, _ = s.saves.row("s1") }
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	if during.State != assistantModel.SavePending || during.ExerciseID != uuid.Nil {
		t.Fatalf("at import time the row must already be pending: %+v", during)
	}
	sum := sha256.Sum256(archive)
	if got := s.saves.hash[key("s1", userOne)]; got != hex.EncodeToString(sum[:]) {
		t.Fatalf("archive hash = %q", got)
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveDone || r.ExerciseID != taskID {
		t.Fatalf("after the import: %+v", r)
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

func TestFailedImportMarksTheSaveFailedAndTheNextSaveClaimsItAgain(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	if _, err := s.save(t); err == nil {
		t.Fatal("expected the import error")
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveFailed {
		t.Fatalf("state = %q", r.State)
	}
	s.importer.err = nil
	if done, err := s.save(t); err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if s.line.count("import") != 2 || len(s.savedBodies()) != 1 {
		t.Fatalf("imports=%d saved=%d", s.line.count("import"), len(s.savedBodies()))
	}
}

func TestSavesOfOneSessionAtOnceImportOnce(t *testing.T) {
	for _, images := range []bool{false, true} {
		s := newSaveRig(t, images)
		s.importer.delay = 50 * time.Millisecond
		s.importer.setJob(running())
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
		if imports := s.line.count("import") + s.line.count("queue"); imports != 1 {
			t.Fatalf("images=%v: imports = %d, want 1", images, imports)
		}
		if !images {
			if got := s.savedBodies(); len(got) != 1 {
				t.Fatalf("the claim's owner tells the brain once: %d", len(got))
			}
			if done, err := s.save(t); err != nil || !done {
				t.Fatalf("a later save tells the brain again: %v %v", done, err)
			}
		}
	}
}

func TestSaveWhileAnotherRequestImportsAnswersNotDoneWithoutImporting(t *testing.T) {
	s := newSaveRig(t, false)
	var second bool
	var err2 error
	s.importer.during = func() { second, err2 = s.save(t) }
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	if err2 != nil || second || s.line.count("import") != 1 {
		t.Fatalf("second: done=%v err=%v imports=%d", second, err2, s.line.count("import"))
	}
}

func TestRecordingTheImportIsRetriedAndASuccessfulRetryCompletesTheSave(t *testing.T) {
	s := newSaveRig(t, false)
	s.saves.completeFails = 2
	if done, err := s.save(t); err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveDone || r.ExerciseID != taskID {
		t.Fatalf("row: %+v", r)
	}
}

func TestWhenRecordingFailsForGoodARetryInsideTheLeaseDoesNotImportAgain(t *testing.T) {
	s := newSaveRig(t, false)
	s.saves.completeFails = 100
	if _, err := s.save(t); err == nil {
		t.Fatal("the page is told the save was not recorded")
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SavePending {
		t.Fatalf("the claim stays pending: %+v", r)
	}
	if done, err := s.save(t); err != nil || done || s.line.count("import") != 1 {
		t.Fatalf("inside the lease: done=%v err=%v imports=%d", done, err, s.line.count("import"))
	}
	// Past the lease the claim is taken over. The import has no idempotency key, so this one imports again.
	s.clock.advance(6 * time.Minute)
	s.saves.mu.Lock()
	s.saves.completeFails = 0
	s.saves.mu.Unlock()
	if done, err := s.save(t); err != nil || !done || s.line.count("import") != 2 {
		t.Fatalf("after the lease: done=%v err=%v imports=%d", done, err, s.line.count("import"))
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
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveDone || !r.Notified || r.ExerciseID != taskID {
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
	if r, ok := s.saves.row("s1"); !ok || r.State != assistantModel.SaveFailed || len(s.savedBodies()) != 0 {
		t.Fatalf("a failed job marks the save failed and the brain is not told: %+v saved=%v", r, s.savedBodies())
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
		t.Fatal("a refused import leaves no claim behind")
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
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveFailed {
		t.Fatalf("a vanished job must fail the save: %+v", r)
	}
}

func TestASavedTaskIsAuditedOnceLikeAnImport(t *testing.T) {
	s := newSaveRig(t, false)
	for range 3 {
		if _, err := s.save(t); err != nil {
			t.Fatal(err)
		}
	}
	entries := s.audit.list()
	if len(entries) != 1 {
		t.Fatalf("one entry for one task, got %d", len(entries))
	}
	e := entries[0]
	if e.ActorID != userOne || e.Target != "exercise:"+taskID.String()+" source:assistant" || e.Method != http.MethodPost || e.ResponseStatus != http.StatusOK ||
		e.Route != "/api/assistant/sessions/:id/save" || e.Permission != string(rbac.PermSelf) {
		t.Fatalf("entry: %+v", e)
	}
}

func TestNoAuditEntryForAFailedImportAndOneWhenTheLeaderFinishesAnImageImport(t *testing.T) {
	failing := newSaveRig(t, false)
	failing.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	_, _ = failing.save(t)
	if len(failing.audit.list()) != 0 {
		t.Fatal("a failed import is not a saved task")
	}

	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	if len(s.audit.list()) != 0 {
		t.Fatal("no task yet")
	}
	s.importer.setJob(done())
	for range 2 {
		if err := s.uc.SettleSaves(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	entries := s.audit.list()
	if len(entries) != 1 || entries[0].ActorID != userOne || !strings.Contains(entries[0].Target, taskID.String()) {
		t.Fatalf("entries: %+v", entries)
	}
}

func TestSavesAtOnceWriteOneAuditEntry(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.delay = 30 * time.Millisecond
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.save(t) }()
	}
	wg.Wait()
	if n := len(s.audit.list()); n != 1 {
		t.Fatalf("audit entries = %d", n)
	}
}

// ── save_failed: the brain is told when an import fails ────────────────────────

func failedBodies(s *saveRig) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.failedBodies...)
}

func codeIn(t *testing.T, body string) int {
	t.Helper()
	var sent map[string]int
	if err := json.Unmarshal([]byte(body), &sent); err != nil || len(sent) != 1 {
		t.Fatalf("save_failed body: %q %v", body, err)
	}
	return sent["code"]
}

func TestAFailedSynchronousImportTellsTheBrainWithTheStableCode(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	if _, err := s.save(t); !errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) {
		t.Fatalf("the import's own error still comes back: %v", err)
	}
	bodies := failedBodies(s)
	if len(bodies) != 1 || codeIn(t, bodies[0]) != exerciseModel.ErrExerciseForbidden.Err().StatusCode().FullCode() {
		t.Fatalf("save_failed: %v", bodies)
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveFailed || !r.Notified || r.FailureCode == 0 {
		t.Fatalf("row: %+v", r)
	}
	if len(s.savedBodies()) != 0 {
		t.Fatal("/save is not called for a failure")
	}
}

func TestAnUntypedImportFailureIsTheFailedImportCode(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = errors.New("zip: not a valid zip file")
	_, _ = s.save(t)
	bodies := failedBodies(s)
	if len(bodies) != 1 || codeIn(t, bodies[0]) != assistantModel.ErrAssistantImportFailed.Err().StatusCode().FullCode() {
		t.Fatalf("save_failed: %v", bodies)
	}
}

func TestSaveFailedThatDoesNotGetThroughIsRetriedByTheLeaderUntilItDoes(t *testing.T) {
	s := newSaveRig(t, false)
	s.brainFailedStatus = http.StatusServiceUnavailable
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	_, _ = s.save(t)
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveFailed || r.Notified {
		t.Fatalf("the failure is stored but not announced: %+v", r)
	}
	if err := s.uc.SettleSaves(context.Background()); err == nil {
		t.Fatal("the brain still refuses: the pass reports it")
	}
	s.mu.Lock()
	s.brainFailedStatus = 0
	s.mu.Unlock()
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.saves.row("s1"); !r.Notified {
		t.Fatalf("announced: %+v", r)
	}
	before := len(failedBodies(s))
	if err := s.uc.SettleSaves(context.Background()); err != nil || len(failedBodies(s)) != before {
		t.Fatalf("a told failure is not told again: %v", err)
	}
	// All attempts carried the same code.
	for _, b := range failedBodies(s) {
		if codeIn(t, b) != exerciseModel.ErrExerciseForbidden.Err().StatusCode().FullCode() {
			t.Fatalf("code changed between attempts: %q", b)
		}
	}
}

func TestSaveFailedAnsweredWith409Or400IsFinalAndNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusBadRequest, http.StatusNotFound} {
		s := newSaveRig(t, false)
		s.brainFailedStatus = status
		s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
		_, _ = s.save(t)
		if r, _ := s.saves.row("s1"); !r.Notified {
			t.Fatalf("status %d: nothing more to tell the brain: %+v", status, r)
		}
		_ = s.uc.SettleSaves(context.Background())
		if n := len(failedBodies(s)); n != 1 {
			t.Fatalf("status %d: asked %d times", status, n)
		}
	}
}

func TestAFailedBackgroundImportTellsTheBrainWithTheJobsCode(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.setJob(running())
	if _, err := s.save(t); err != nil {
		t.Fatal(err)
	}
	if len(failedBodies(s)) != 0 {
		t.Fatal("not failed yet")
	}
	s.importer.setJob(exerciseUseCase.ImportJobView{State: exerciseModel.ImportJobFailed, Error: &exerciseModel.ImportJobError{Code: 70995}})
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	bodies := failedBodies(s)
	if len(bodies) != 1 || codeIn(t, bodies[0]) != 70995 {
		t.Fatalf("save_failed: %v", bodies)
	}
	if r, _ := s.saves.row("s1"); r.State != assistantModel.SaveFailed || !r.Notified || r.FailureCode != 70995 {
		t.Fatalf("row: %+v", r)
	}
	if err := s.uc.SettleSaves(context.Background()); err != nil || len(failedBodies(s)) != 1 {
		t.Fatalf("idempotent: %v %d", err, len(failedBodies(s)))
	}
	if len(s.savedBodies()) != 0 {
		t.Fatal("/save is not called for a failure")
	}
}

func TestAVanishedJobTellsTheBrainTheFailedImportCode(t *testing.T) {
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
	bodies := failedBodies(s)
	if len(bodies) != 1 || codeIn(t, bodies[0]) != assistantModel.ErrAssistantImportFailed.Err().StatusCode().FullCode() {
		t.Fatalf("save_failed: %v", bodies)
	}
}

func TestAPageRetryAfterAFailureClaimsAgainAndAnOldFailureIsNotToldAgain(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	_, _ = s.save(t)
	s.importer.err = nil
	if done, err := s.save(t); err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if err := s.uc.SettleSaves(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(failedBodies(s)) != 1 || len(s.savedBodies()) != 1 {
		t.Fatalf("failed=%d saved=%d", len(failedBodies(s)), len(s.savedBodies()))
	}
}
