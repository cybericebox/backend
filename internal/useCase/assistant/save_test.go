package assistant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

type fakeImporter struct {
	line    *timeline
	start   exerciseUseCase.ArchiveImportStart
	err     error
	actor   exerciseUseCase.Actor
	input   exerciseUseCase.ImportExerciseInput
	authErr error
	queued  exerciseUseCase.QueueImportInput
	jobs    []exerciseUseCase.ImportJobView // answers in order, the last one repeats
	polls   int
}

func (f *fakeImporter) AuthorizeImport(context.Context, exerciseUseCase.Actor, *uuid.UUID) error {
	f.line.add("authorize")
	return f.authErr
}

func (f *fakeImporter) ArchiveLimits(context.Context) exerciseModel.ArchiveLimits {
	return exerciseModel.ArchiveLimits{ArchiveBytes: 1 << 20}
}

func (f *fakeImporter) ImportRawArchive(_ context.Context, actor exerciseUseCase.Actor, in exerciseUseCase.ImportExerciseInput) (exerciseUseCase.ArchiveImportStart, error) {
	f.line.add("import")
	f.actor, f.input = actor, in
	return f.start, f.err
}

func (f *fakeImporter) QueueArchiveImport(_ context.Context, in exerciseUseCase.QueueImportInput) (uuid.UUID, error) {
	f.line.add("queue")
	f.queued = in
	return jobID, nil
}

func (f *fakeImporter) ArchiveImportJob(_ context.Context, id, user uuid.UUID) (exerciseUseCase.ImportJobView, error) {
	f.line.add("poll")
	i := min(f.polls, len(f.jobs)-1)
	f.polls++
	return f.jobs[i], nil
}

type fakeFiles struct {
	line *timeline
	body []byte
}

func (f *fakeFiles) UploadFile(_ context.Context, name, contentType string, r io.Reader, _ uuid.UUID) (mediaModel.File, error) {
	f.line.add("upload")
	f.body, _ = io.ReadAll(r)
	return mediaModel.File{ID: fileID}, nil
}

type saveRig struct {
	*rig
	line     *timeline
	importer *fakeImporter
	files    *fakeFiles
	saved    []string // bodies of the brain's /save
	subjects []string
	archive  func(w http.ResponseWriter)
}

func newSaveRig(t *testing.T, images bool) *saveRig {
	t.Helper()
	line := &timeline{}
	imp := &fakeImporter{line: line, start: exerciseUseCase.ArchiveImportStart{Exercises: []exerciseUseCase.ExerciseView{{ID: taskID}}}}
	files := &fakeFiles{line: line}
	s := &saveRig{line: line, importer: imp, files: files}
	s.rig = enrolledRig(t, func(d *assistant.Dependencies) {
		d.Importer, d.Files = imp, files
		d.NamesImages = func([]byte, exerciseModel.ArchiveLimits) bool { return images }
		d.ImportPoll, d.ImportWait = time.Millisecond, 200*time.Millisecond
	})
	s.archive = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive)
	}
	s.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.subjects = append(s.subjects, req.Header.Get(assistant.SubjectHeader))
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/assistant/sessions/s1/archive":
			line.add("brain:archive")
			s.archive(w)
		case req.Method == http.MethodPost && req.URL.Path == "/v1/assistant/sessions/s1/save":
			line.add("brain:save")
			b, _ := io.ReadAll(req.Body)
			s.saved = append(s.saved, string(b))
			_, _ = w.Write([]byte("{}"))
		default:
			brainfake.Fail(w, http.StatusNotFound, "not_found", "no route")
		}
	})
	return s
}

var actor = exerciseUseCase.Actor{UserID: userOne, Role: rbac.Role("")}

func TestSaveFetchesImportsThenTellsTheBrainTheExerciseID(t *testing.T) {
	s := newSaveRig(t, false)
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); err != nil {
		t.Fatal(err)
	}
	if got := s.line.list(); len(got) != 3 || got[0] != "brain:archive" || got[1] != "import" || got[2] != "brain:save" {
		t.Fatalf("order: %v", got)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(s.saved[0]), &sent); err != nil || sent["ExerciseID"] != taskID.String() {
		t.Fatalf("brain save body = %q", s.saved[0])
	}
	if !bytes.Equal(s.importer.input.Archive, archive) || s.importer.input.CreatedBy != userOne || s.importer.actor != actor || s.importer.input.OwnerEventID != nil {
		t.Fatalf("the archive must be imported as the caller into the catalog: %+v", s.importer.input)
	}
	for _, subject := range s.subjects {
		if subject != s.uc.Subject(userOne) {
			t.Fatalf("every brain call carries the caller's subject, got %q", subject)
		}
	}
}

func TestSaveDoesNotTellTheBrainWhenTheImportFails(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = exerciseModel.ErrExerciseForbidden.Err()
	err := s.uc.SaveSession(context.Background(), actor, "s1")
	if !errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) {
		t.Fatalf("the import's own error must come back, got %v", err)
	}
	if got := s.line.list(); len(got) != 2 || got[1] != "import" {
		t.Fatalf("the brain must not be told: %v", got)
	}
	if len(s.saved) != 0 {
		t.Fatal("brain /save was called")
	}
}

func TestSaveWithABrokenArchiveIsAFailedImportNotAnInternalError(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.err = errors.New("zip: not a valid zip file")
	err := s.uc.SaveSession(context.Background(), actor, "s1")
	if err == nil || len(s.saved) != 0 {
		t.Fatalf("err=%v saved=%v", err, s.saved)
	}
}

func TestSaveWithoutAFinishedArchiveStopsBeforeImporting(t *testing.T) {
	s := newSaveRig(t, false)
	s.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		brainfake.Fail(w, http.StatusConflict, "no_archive", "the session has no archive yet")
	})
	err := s.uc.SaveSession(context.Background(), actor, "s1")
	if !errors.Is(err, assistantModel.ErrAssistantNotReady.Err()) {
		t.Fatalf("got %v", err)
	}
	for _, e := range s.line.list() {
		if e == "import" {
			t.Fatal("nothing may be imported without an archive")
		}
	}
}

func TestSaveRefusesAnArchiveLargerThanTheImportLimit(t *testing.T) {
	s := newSaveRig(t, false)
	s.archive = func(w http.ResponseWriter) { _, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20)) }
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); err == nil {
		t.Fatal("an oversized archive was accepted")
	}
	for _, e := range s.line.list() {
		if e == "import" || e == "brain:save" {
			t.Fatalf("unexpected step %q", e)
		}
	}
}

func TestSaveTwoTasksInOneArchiveIsAFailedImport(t *testing.T) {
	s := newSaveRig(t, false)
	s.importer.start = exerciseUseCase.ArchiveImportStart{Exercises: []exerciseUseCase.ExerciseView{{ID: taskID}, {ID: jobID}}}
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); !errors.Is(err, assistantModel.ErrAssistantImportFailed.Err()) || len(s.saved) != 0 {
		t.Fatalf("got %v saved=%v", err, s.saved)
	}
}

func running() exerciseUseCase.ImportJobView {
	return exerciseUseCase.ImportJobView{ID: jobID, State: exerciseModel.ImportJobRunning}
}

func TestSaveWithImagesWaitsForTheBackgroundImportThenTellsTheBrain(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.jobs = []exerciseUseCase.ImportJobView{running(), running(), {ID: jobID, State: exerciseModel.ImportJobDone, ExerciseIDs: []uuid.UUID{taskID}}}
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); err != nil {
		t.Fatal(err)
	}
	got := s.line.list()
	if got[0] != "brain:archive" || got[1] != "authorize" || got[2] != "upload" || got[3] != "queue" || got[len(got)-1] != "brain:save" {
		t.Fatalf("order: %v", got)
	}
	if s.importer.polls != 3 || !bytes.Equal(s.files.body, archive) || s.importer.queued.CreatedBy != userOne || s.importer.queued.OwnerEventID != nil || s.importer.queued.SourceID != fileID {
		t.Fatalf("polls=%d queued=%+v", s.importer.polls, s.importer.queued)
	}
	var sent map[string]string
	_ = json.Unmarshal([]byte(s.saved[0]), &sent)
	if sent["ExerciseID"] != taskID.String() {
		t.Fatalf("brain save body = %q", s.saved[0])
	}
}

func TestSaveWithImagesFailedJobDoesNotTellTheBrain(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.jobs = []exerciseUseCase.ImportJobView{{ID: jobID, State: exerciseModel.ImportJobFailed, Error: &exerciseModel.ImportJobError{Code: 70995}}}
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); !errors.Is(err, assistantModel.ErrAssistantImportFailed.Err()) || len(s.saved) != 0 {
		t.Fatalf("got %v saved=%v", err, s.saved)
	}
}

func TestSaveWithImagesStillRunningAfterTheWaitIsPendingAndNotTold(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.jobs = []exerciseUseCase.ImportJobView{running()}
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); !errors.Is(err, assistantModel.ErrAssistantImportPending.Err()) || len(s.saved) != 0 {
		t.Fatalf("got %v saved=%v", err, s.saved)
	}
}

func TestSaveWithImagesNeedsImportRights(t *testing.T) {
	s := newSaveRig(t, true)
	s.importer.authErr = exerciseModel.ErrExerciseForbidden.Err()
	if err := s.uc.SaveSession(context.Background(), actor, "s1"); !errors.Is(err, exerciseModel.ErrExerciseForbidden.Err()) {
		t.Fatalf("got %v", err)
	}
	for _, e := range s.line.list() {
		if e == "upload" || e == "queue" {
			t.Fatalf("unauthorized caller reached %q", e)
		}
	}
}
