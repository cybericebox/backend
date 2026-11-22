package assistant_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	assistantHandler "github.com/cybericebox/backend/internal/delivery/controller/http/handler/assistant"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	"github.com/cybericebox/backend/internal/delivery/controller/http/sse"
	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain/brainfake"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	mediaModel "github.com/cybericebox/backend/internal/model/media"
	"github.com/cybericebox/backend/internal/model/rbac"
	assistantUseCase "github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
	pkgerr "github.com/cybericebox/backend/pkg/err"
	"github.com/cybericebox/backend/pkg/secret"
)

const cipherKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	author   = uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111")
	stranger = uuid.FromStringOrNil("22222222-2222-4222-8222-222222222222")
	taskID   = uuid.FromStringOrNil("33333333-3333-4333-8333-333333333333")
)

// memStore is the connection store in memory.
type memStore struct {
	mu   sync.Mutex
	conn map[string]assistantModel.Connection
}

func (s *memStore) Get(_ context.Context, e string) (assistantModel.Connection, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conn[e]
	return c, ok, nil
}

func (s *memStore) Create(_ context.Context, c assistantModel.Connection) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn[c.Endpoint] = c
	return true, nil
}

func (s *memStore) SetGRPCEndpoint(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s *memStore) SetCertificate(context.Context, string, string, string, string, string, time.Time, time.Time) (bool, error) {
	return false, nil
}

type saves struct {
	mu   sync.Mutex
	rows map[string]assistantModel.Save
}

func (m *saves) Get(_ context.Context, s string, a uuid.UUID) (assistantModel.Save, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[s+a.String()]
	return r, ok, nil
}

func (m *saves) Claim(_ context.Context, s string, a uuid.UUID, _ string, now, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[s+a.String()] = assistantModel.Save{Session: s, Author: a, State: assistantModel.SavePending, ClaimedAt: now}
	return true, nil
}

func (m *saves) update(s string, a uuid.UUID, f func(*assistantModel.Save)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[s+a.String()]
	f(&r)
	m.rows[s+a.String()] = r
}

func (m *saves) SetJob(_ context.Context, s string, a, job uuid.UUID) error {
	m.update(s, a, func(r *assistantModel.Save) { r.JobID = job })
	return nil
}

func (m *saves) Complete(_ context.Context, s string, a, e uuid.UUID) (bool, error) {
	m.update(s, a, func(r *assistantModel.Save) { r.State, r.ExerciseID = assistantModel.SaveDone, e })
	return true, nil
}

func (m *saves) Fail(_ context.Context, s string, a uuid.UUID, _ int) error {
	m.update(s, a, func(r *assistantModel.Save) { r.State = assistantModel.SaveFailed })
	return nil
}

func (m *saves) SetNotified(_ context.Context, s string, a uuid.UUID) error {
	m.update(s, a, func(r *assistantModel.Save) { r.Notified = true })
	return nil
}

func (m *saves) Unsettled(context.Context, time.Time, int) ([]assistantModel.Save, error) {
	return nil, nil
}

// importer records the import of the save route.
type importer struct {
	mu      sync.Mutex
	err     error
	imports int
	queues  int
}

func (i *importer) AuthorizeImport(context.Context, exerciseUseCase.Actor, *uuid.UUID) error {
	return nil
}
func (i *importer) ArchiveLimits(context.Context) exerciseModel.ArchiveLimits {
	return exerciseModel.ArchiveLimits{ArchiveBytes: 1 << 20}
}
func (i *importer) ImportRawArchive(context.Context, exerciseUseCase.Actor, exerciseUseCase.ImportExerciseInput) (exerciseUseCase.ArchiveImportStart, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.imports++
	return exerciseUseCase.ArchiveImportStart{Exercises: []exerciseUseCase.ExerciseView{{ID: taskID}}}, i.err
}
func (i *importer) QueueArchiveImport(context.Context, exerciseUseCase.QueueImportInput) (uuid.UUID, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.queues++
	return taskID, nil
}
func (i *importer) ArchiveImportJob(context.Context, uuid.UUID, uuid.UUID) (exerciseUseCase.ImportJobView, error) {
	return exerciseUseCase.ImportJobView{State: exerciseModel.ImportJobRunning}, nil
}

// The edit of an existing task is not exercised here: every archive of these tests is a new task.
func (i *importer) EditedArchiveTarget(context.Context, exerciseUseCase.Actor, []byte, exerciseModel.ArchiveLimits) (exerciseUseCase.EditTarget, error) {
	return exerciseUseCase.EditTarget{}, nil
}
func (i *importer) StaleDraftError(context.Context, uuid.UUID) error {
	return exerciseModel.ErrDraftModified.Err()
}
func (i *importer) ArchiveCarriesSecretValue([]byte, exerciseModel.ArchiveLimits) (bool, error) {
	return false, nil
}
func (i *importer) PreviewArchiveImport(context.Context, exerciseUseCase.Actor, exerciseUseCase.PreviewArchiveInput) (exerciseUseCase.ArchiveImportPreview, error) {
	return exerciseUseCase.ArchiveImportPreview{}, nil
}
func (i *importer) CommitArchiveImport(context.Context, exerciseUseCase.Actor, exerciseUseCase.CommitArchiveInput) (exerciseUseCase.ArchiveImportStart, error) {
	return exerciseUseCase.ArchiveImportStart{}, nil
}

type subjects struct {
	mu   sync.Mutex
	rows map[uuid.UUID]uuid.UUID
}

func (m *subjects) Get(_ context.Context, u uuid.UUID) (uuid.UUID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[u]
	return s, ok, nil
}

func (m *subjects) Create(_ context.Context, u, s uuid.UUID, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[u]; ok {
		return false, nil
	}
	m.rows[u] = s
	return true, nil
}

func subjectOf(t *testing.T, uc *assistantUseCase.UseCase, user uuid.UUID) string {
	t.Helper()
	s, err := uc.Subject(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type files struct{}

func (files) UploadFile(context.Context, string, string, io.Reader, uuid.UUID) (mediaModel.File, error) {
	return mediaModel.File{ID: taskID}, nil
}

func (files) DiscardFile(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (files) GetFile(_ context.Context, id uuid.UUID) (mediaModel.File, error) {
	return mediaModel.File{ID: id}, nil
}

// port is the handler's use case port over the real assistant use case. Only the author may author.
type port struct{ *assistantUseCase.UseCase }

func (p port) RequireAuthor(_ context.Context, a exerciseUseCase.Actor) error {
	if a.UserID != author {
		return exerciseModel.ErrExerciseForbidden.Err()
	}
	return nil
}
func (p port) AssistantProxy(ctx context.Context, u uuid.UUID, c assistantUseCase.Call) (*http.Response, error) {
	return p.Proxy(ctx, u, c)
}
func (p port) AssistantSaveSession(ctx context.Context, a exerciseUseCase.Actor, id string) (bool, error) {
	return p.SaveSession(ctx, a, id)
}

// protector is the sign-in gate: X-Test-User names the signed-in person, none is a 401 like the real one.
type protector struct{}

func (protector) RequirePermission(rbac.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("X-Test-User")
		id, err := uuid.FromString(raw)
		if err != nil {
			c.Header("X-Sign-In-URL", "https://id.example.test/sign-in")
			response.AbortWithUnauthenticated(c)
			return
		}
		c.Request = c.Request.WithContext(rbac.ContextWithCurrentUserSession(c.Request.Context(), rbac.Claims{UserID: id}))
	}
}

type rig struct {
	fake *brainfake.Fake
	imp  *importer
	uc   *assistantUseCase.UseCase
	api  *httptest.Server
}

func newRig(t *testing.T) *rig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := brainfake.New()
	t.Cleanup(f.Close)
	caFile := filepath.Join(t.TempDir(), "brain-ca.crt")
	if err := os.WriteFile(caFile, f.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New(cipherKey)
	if err != nil {
		t.Fatal(err)
	}
	imp := &importer{}
	uc := assistantUseCase.New(assistantUseCase.Dependencies{
		Store: &memStore{conn: map[string]assistantModel.Connection{}}, Cipher: cipher, Importer: imp, Files: files{},
		NamesImages: func(a []byte, _ exerciseModel.ArchiveLimits) bool { return bytes.Equal(a, []byte("PK-with-images")) },
		Saves:       &saves{rows: map[string]assistantModel.Save{}},
		Subjects:    &subjects{rows: map[uuid.UUID]uuid.UUID{}},
		Config:      assistantUseCase.Config{Endpoint: f.Endpoint(), GRPCEndpoint: f.GRPCEndpoint(), EnrollmentToken: brainfake.Token, CAFile: caFile},
	})
	if _, err = uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(response.WithErrorHandler)
	assistantHandler.New(port{uc}, protector{}).Init(engine.Group("api"))
	api := httptest.NewServer(engine)
	t.Cleanup(api.Close)
	return &rig{fake: f, imp: imp, uc: uc, api: api}
}

func (r *rig) do(t *testing.T, user uuid.UUID, method, path string, body io.Reader, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, r.api.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// envelope is what the page unwraps: {Status: {Code, Message}, Data}.
type envelope struct {
	Status struct {
		Code    int
		Message string
	}
	Data json.RawMessage
}

// successCode is the Status.Code of every successful platform answer.
var successCode = pkgerr.StatusCodeSuccess.FullCode()

func decodeEnvelope(t *testing.T, resp *http.Response) envelope {
	t.Helper()
	var got envelope
	if err := json.Unmarshal([]byte(read(t, resp)), &got); err != nil {
		t.Fatalf("the answer is not the platform envelope: %v", err)
	}
	return got
}

func read(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRoutesNeedASignedInAuthor(t *testing.T) {
	r := newRig(t)
	calls := 0
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = w.Write([]byte("{}")) })

	anon := r.do(t, uuid.Nil, http.MethodGet, "/api/assistant/ideas", nil, nil)
	if anon.StatusCode != http.StatusUnauthorized || anon.Header.Get("X-Sign-In-URL") == "" {
		t.Fatalf("anonymous: %d sign-in=%q", anon.StatusCode, anon.Header.Get("X-Sign-In-URL"))
	}
	other := r.do(t, stranger, http.MethodGet, "/api/assistant/ideas", nil, nil)
	if other.StatusCode != http.StatusForbidden {
		t.Fatalf("a person who may not author: %d", other.StatusCode)
	}
	if calls != 0 {
		t.Fatal("the brain must not be reached by a caller who may not use the assistant")
	}
}

func TestJSONRoutesGoThroughWithTheSubjectAndTheAnswerComesBack(t *testing.T) {
	r := newRig(t)
	type seen struct{ method, path, ctype, subject, body string }
	var got []seen
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		got = append(got, seen{req.Method, req.URL.Path, req.Header.Get("Content-Type"), req.Header.Get(assistantUseCase.SubjectHeader), string(b)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ID":"s1","extra":"kept"}`))
	})
	json := map[string]string{"Content-Type": "application/json", assistantUseCase.SubjectHeader: "spoofed"}

	resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions", strings.NewReader(`{"ExerciseID":"e","VersionID":"v"}`), json)
	answer := decodeEnvelope(t, resp)
	if resp.StatusCode != 200 || answer.Status.Code != successCode || string(answer.Data) != `{"ID":"s1","extra":"kept"}` || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("create: %d %+v %s", resp.StatusCode, answer.Status, answer.Data)
	}
	r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/messages", strings.NewReader(`{"Text":"hi"}`), json)
	r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/start", nil, nil)
	r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/stop", strings.NewReader(`{}`), json)
	r.do(t, author, http.MethodDelete, "/api/assistant/sessions/s1", nil, nil)
	r.do(t, author, http.MethodGet, "/api/assistant/ideas", nil, nil)
	r.do(t, author, http.MethodDelete, "/api/assistant/sessions/s1/attachments/a-1", nil, nil)

	want := []seen{
		{"POST", "/v1/assistant/sessions", "application/json", "", `{"ExerciseID":"e","VersionID":"v"}`},
		{"POST", "/v1/assistant/sessions/s1/messages", "application/json", "", `{"Text":"hi"}`},
		{"POST", "/v1/assistant/sessions/s1/start", "application/json", "", `{}`},
		{"POST", "/v1/assistant/sessions/s1/stop", "application/json", "", `{}`},
		{"DELETE", "/v1/assistant/sessions/s1", "", "", ""},
		{"GET", "/v1/assistant/ideas", "", "", ""},
		{"DELETE", "/v1/assistant/sessions/s1/attachments/a-1", "", "", ""},
	}
	subject := subjectOf(t, r.uc, author)
	if len(got) != len(want) {
		t.Fatalf("calls: %+v", got)
	}
	for i, w := range want {
		w.subject = subject
		if got[i] != w {
			t.Errorf("call %d: got %+v want %+v", i, got[i], w)
		}
	}
	if subject == "" || strings.Contains(subject, author.String()) {
		t.Fatalf("subject %q", subject)
	}
}

func TestAttachmentUploadStreamsTheMultipartBodyAndMarksItForTheCSRFGuard(t *testing.T) {
	r := newRig(t)
	var gotCT, gotXRW, gotFile, gotName string
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotCT, gotXRW = req.Header.Get("Content-Type"), req.Header.Get("X-Requested-With")
		if err := req.ParseMultipartForm(1 << 20); err == nil {
			f, h, _ := req.FormFile("file")
			b, _ := io.ReadAll(f)
			gotFile, gotName = string(b), h.Filename
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ID":"a-1"}`))
	})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "notes.txt")
	_, _ = part.Write([]byte("file contents"))
	_ = mw.Close()

	resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/attachments", &body, map[string]string{"Content-Type": mw.FormDataContentType()})
	if got := decodeEnvelope(t, resp); resp.StatusCode != 200 || string(got.Data) != `{"ID":"a-1"}` {
		t.Fatalf("upload: %d %s", resp.StatusCode, got.Data)
	}
	if mt, _, err := mime.ParseMediaType(gotCT); err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type at the brain: %q", gotCT)
	}
	if gotXRW == "" || gotFile != "file contents" || gotName != "notes.txt" {
		t.Fatalf("x-requested-with=%q file=%q name=%q", gotXRW, gotFile, gotName)
	}
}

func TestArchiveIsStreamedWithTheBrainsFileName(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="task.cybericebox.zip"`)
		_, _ = w.Write([]byte("PK-bytes"))
	})
	resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/archive", nil, nil)
	if resp.StatusCode != 200 || read(t, resp) != "PK-bytes" ||
		resp.Header.Get("Content-Type") != "application/zip" || resp.Header.Get("Content-Disposition") != `attachment; filename=task.cybericebox.zip` {
		t.Fatalf("archive: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestBrainErrorsBecomeTypedStatusesAndNeverA401(t *testing.T) {
	r := newRig(t)
	status := 0
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		brainfake.Fail(w, status, "x", "never shown")
	})
	for brainStatus, want := range map[int]int{
		400: 400, 404: 404, 409: 409, 410: 410, 422: 400,
		401: 502, 500: 502, 503: 502, 418: 502,
	} {
		status = brainStatus
		resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/start", nil, nil)
		if resp.StatusCode != want {
			t.Errorf("brain %d: page got %d, want %d", brainStatus, resp.StatusCode, want)
		}
		if body := read(t, resp); strings.Contains(body, "never shown") {
			t.Errorf("brain %d: the brain's own text reached the page: %s", brainStatus, body)
		}
	}
	r.fake.Server.Close()
	if resp := r.do(t, author, http.MethodGet, "/api/assistant/ideas", nil, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unreachable brain: %d", resp.StatusCode)
	}
}

func TestACraftedSessionIDNeverReachesTheBrain(t *testing.T) {
	r := newRig(t)
	calls := 0
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++ })
	for _, id := range []string{"a$b", "%2e%2e", "a%20b", ".hidden"} {
		resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/"+id+"/start", nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("id %q: %d", id, resp.StatusCode)
		}
	}
	if calls != 0 {
		t.Fatalf("%d crafted ids reached the brain", calls)
	}
}

func TestSaveImportsThenTellsTheBrain(t *testing.T) {
	r := newRig(t)
	var order []string
	var saveBody string
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/archive"):
			order = append(order, "archive")
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK-task"))
		case strings.HasSuffix(req.URL.Path, "/save"):
			order = append(order, "save")
			b, _ := io.ReadAll(req.Body)
			saveBody = string(b)
			_, _ = w.Write([]byte("{}"))
		}
	})
	resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/save", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 || strings.Join(order, ",") != "archive,save" || r.imp.imports != 1 || !strings.Contains(saveBody, taskID.String()) {
		t.Fatalf("save: %d order=%v imports=%d body=%q", resp.StatusCode, order, r.imp.imports, saveBody)
	}
}

func TestSaveWithAFailedImportAnswersTheImportsErrorAndSkipsTheBrain(t *testing.T) {
	r := newRig(t)
	r.imp.err = exerciseModel.ErrExerciseForbidden.Err()
	saved := false
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/save") {
			saved = true
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK-task"))
	})
	resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/save", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusForbidden || saved {
		t.Fatalf("save with a failed import: %d brain told=%v", resp.StatusCode, saved)
	}
}

// ── the event stream ────────────────────────────────────────────────────────

func TestEventsRelayEveryFrameVerbatimIncludingUnknownTypesAndPassAfterThrough(t *testing.T) {
	r := newRig(t)
	frames := "id: 7\nevent: message\ndata: {\"type\":\"message\",\"role\":\"assistant\",\"text\":\"hi\"}\n\n" +
		": a comment frame\n\n" +
		"id: 8\nevent: brand_new_type\ndata: {\"type\":\"brand_new_type\",\"x\":1}\n\n" +
		"id: 9\nevent: delta\ndata: {\"type\":\"delta\",\n" + "data: \"text\":\"multi\"}\n\n"
	var afterAtBrain, acceptAtBrain, subject string
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		afterAtBrain, acceptAtBrain, subject = req.URL.Query().Get("after"), req.Header.Get("Accept"), req.Header.Get(assistantUseCase.SubjectHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(frames))
	})
	resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/events?after=6", nil, nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %v", resp.StatusCode, resp.Header)
	}
	if got := read(t, resp); got != frames {
		t.Fatalf("frames changed on the way:\n got: %q\nwant: %q", got, frames)
	}
	if afterAtBrain != "6" || acceptAtBrain != "text/event-stream" || subject != subjectOf(t, r.uc, author) {
		t.Fatalf("after=%q accept=%q subject=%q", afterAtBrain, acceptAtBrain, subject)
	}
}

func TestEventsRejectABadAfterAndRespectThePerUserStreamLimit(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("id: 1\nevent: status\ndata: {}\n\n"))
	})
	for _, bad := range []string{"abc", "-1", "1.5", "99999999999999999999999"} {
		if resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/events?after="+bad, nil, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("after=%q: %d", bad, resp.StatusCode)
		}
	}
	var releases []func()
	for {
		release, ok := sse.Streams.Acquire("assistant:"+author.String(), 8)
		if !ok {
			break
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	if resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/events", nil, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over the stream limit: %d", resp.StatusCode)
	}
	// Another person is not affected.
	other := assistantStreamFree(t, r)
	if !other {
		t.Fatal("the limit is per user")
	}
}

func assistantStreamFree(t *testing.T, r *rig) bool {
	t.Helper()
	release, ok := sse.Streams.Acquire("assistant:"+stranger.String(), 8)
	if ok {
		release()
	}
	return ok
}

func TestEventsEndWhenTheClientLeaves(t *testing.T) {
	r := newRig(t)
	brainSawClose := make(chan struct{})
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 1\nevent: status\ndata: {\"type\":\"status\",\"status\":\"talking\"}\n\n"))
		w.(http.Flusher).Flush()
		<-req.Context().Done() // a quiet stream that only ends when the daemon lets go of it
		close(brainSawClose)
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.api.URL+"/api/assistant/sessions/s1/events", nil)
	req.Header.Set("X-Test-User", author.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "id: 1\n" {
		t.Fatalf("first line %q %v", line, err)
	}
	cancel()
	select {
	case <-brainSawClose:
	case <-time.After(5 * time.Second):
		t.Fatal("the brain connection stayed open after the client left")
	}
}

func TestSaveOfATaskWithImagesAnswers202AndDoesNotTellTheBrainYet(t *testing.T) {
	r := newRig(t)
	saved := false
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/save") {
			saved = true
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK-with-images"))
	})
	resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/save", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusAccepted || saved || r.imp.queues != 1 || r.imp.imports != 0 {
		t.Fatalf("status=%d told=%v queues=%d imports=%d", resp.StatusCode, saved, r.imp.queues, r.imp.imports)
	}
}

// ── what the brain says about content types is not trusted ──────────────────

func TestAJSONRouteAnswerOfAnotherTypeIsNotRelayed(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/assistant/ideas"},
		{http.MethodPost, "/api/assistant/sessions"},
		{http.MethodPost, "/api/assistant/sessions/s1/attachments"},
	} {
		resp := r.do(t, author, route.method, route.path, strings.NewReader("{}"), map[string]string{"Content-Type": "application/json"})
		body := read(t, resp)
		if resp.StatusCode != http.StatusBadGateway || strings.Contains(body, "<script>") || strings.Contains(resp.Header.Get("Content-Type"), "html") {
			t.Errorf("%s %s: %d %q %q", route.method, route.path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
}

func TestJSONAnswersCarryTheFixedTypeAndNosniff(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8; x=1")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	resp := r.do(t, author, http.MethodGet, "/api/assistant/ideas", nil, nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}

func TestArchiveTypeAndDispositionAreRebuiltNeverCopied(t *testing.T) {
	r := newRig(t)
	for header, want := range map[string]string{
		`inline; filename="a.html"`:                    `attachment; filename=a.html`,
		`attachment; filename="../../etc/pass wd.zip"`: `attachment; filename=pass-wd.zip`,
		`attachment; filename="x\y<b>.zip"`:            `attachment; filename=y-b-.zip`,
		`attachment`:                                   `attachment; filename=task.zip`,
		`garbage;;;`:                                   `attachment; filename=task.zip`,
		``:                                             `attachment; filename=task.zip`,
	} {
		r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			if header != "" {
				w.Header().Set("Content-Disposition", header)
			}
			_, _ = w.Write([]byte("PK"))
		})
		resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/archive", nil, nil)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
			resp.Header.Get("Content-Disposition") != want {
			t.Errorf("brain %q: %d type=%q disposition=%q want %q", header, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"), want)
		}
	}
}

func TestArchiveOfAnotherTypeIsNotRelayed(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	})
	resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/archive", nil, nil)
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(read(t, resp), "<script>") {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestEventsOfAnotherTypeAreNotRelayedAndStreamsAreNosniff(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	})
	resp := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/events", nil, nil)
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(read(t, resp), "<script>") {
		t.Fatalf("status %d", resp.StatusCode)
	}
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte("id: 1\nevent: status\ndata: {}\n\n"))
	})
	ok := r.do(t, author, http.MethodGet, "/api/assistant/sessions/s1/events", nil, nil)
	if ok.StatusCode != 200 || !strings.HasPrefix(ok.Header.Get("Content-Type"), "text/event-stream") || ok.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", ok.StatusCode, ok.Header)
	}
}

func TestEverySuccessfulJSONRouteAnswersInTheEnvelopeWithTheBrainsPayloadInData(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Ideas":[{"ID":"i1","Title":"t"}]}`))
	})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/assistant/ideas"},
		{http.MethodPost, "/api/assistant/sessions"},
		{http.MethodPost, "/api/assistant/sessions/s1/messages"},
		{http.MethodPost, "/api/assistant/sessions/s1/start"},
		{http.MethodPost, "/api/assistant/sessions/s1/stop"},
		{http.MethodDelete, "/api/assistant/sessions/s1"},
		{http.MethodDelete, "/api/assistant/sessions/s1/attachments/a-1"},
	} {
		resp := r.do(t, author, route.method, route.path, strings.NewReader("{}"), map[string]string{"Content-Type": "application/json"})
		got := decodeEnvelope(t, resp)
		if resp.StatusCode != 200 || got.Status.Code != successCode || string(got.Data) != `{"Ideas":[{"ID":"i1","Title":"t"}]}` {
			t.Errorf("%s %s: %d %+v %s", route.method, route.path, resp.StatusCode, got.Status, got.Data)
		}
	}
}

func TestAnEmptySuccessIsTheSameAsOurOtherEmptySuccesses(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	resp := r.do(t, author, http.MethodDelete, "/api/assistant/sessions/s1", nil, nil)
	got := decodeEnvelope(t, resp)
	if resp.StatusCode != 200 || got.Status.Code != successCode || (len(got.Data) != 0 && string(got.Data) != "null") {
		t.Fatalf("%d %+v %s", resp.StatusCode, got.Status, got.Data)
	}
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if resp = r.do(t, author, http.MethodDelete, "/api/assistant/sessions/s1", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("204: %d", resp.StatusCode)
	}
}

func TestAJSONAnswerThatIsNotJSONIsNotRelayed(t *testing.T) {
	r := newRig(t)
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	})
	if resp := r.do(t, author, http.MethodGet, "/api/assistant/ideas", nil, nil); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestErrorsAreOurEnvelopeWithTheCatalogCodeAndNeverTheBrainsBody(t *testing.T) {
	r := newRig(t)
	status := 0
	r.fake.Assistant = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"secret_code","message":"brain private text","extra":"leak"}`))
	})
	for brainStatus, want := range map[int]pkgerr.StatusCode{
		400: assistantModel.ErrAssistantRejected.Err().StatusCode(),
		403: assistantModel.ErrAssistantForbidden.Err().StatusCode(),
		404: assistantModel.ErrAssistantNotFound.Err().StatusCode(),
		409: assistantModel.ErrAssistantNotReady.Err().StatusCode(),
		410: assistantModel.ErrAssistantGone.Err().StatusCode(),
		401: assistantModel.ErrAssistantUpstream.Err().StatusCode(),
		500: assistantModel.ErrAssistantUpstream.Err().StatusCode(),
	} {
		status = brainStatus
		resp := r.do(t, author, http.MethodPost, "/api/assistant/sessions/s1/start", nil, nil)
		raw := read(t, resp)
		var got envelope
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("brain %d: not an envelope: %q", brainStatus, raw)
		}
		if got.Status.Code != want.FullCode() || resp.StatusCode != want.HTTPCode() {
			t.Errorf("brain %d: code %d http %d, want code %d http %d", brainStatus, got.Status.Code, resp.StatusCode, want.FullCode(), want.HTTPCode())
		}
		if strings.Contains(raw, "secret_code") || strings.Contains(raw, "brain private text") || strings.Contains(raw, "leak") {
			t.Errorf("brain %d: the brain's body reached the page: %s", brainStatus, raw)
		}
	}
}
