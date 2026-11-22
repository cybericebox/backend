package assistant_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	userModel "github.com/cybericebox/backend/internal/model/user"
	"github.com/cybericebox/backend/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

// fakeTasks is the exercise use case's export for edit: it knows one task with one version and says who may edit it.
type fakeTasks struct {
	mu      sync.Mutex
	archive []byte
	err     error
	asked   []fakeTaskAsk
}

type fakeTaskAsk struct {
	Actor             exerciseUseCase.Actor
	Exercise, Version uuid.UUID
}

func (f *fakeTasks) ExportTaskVersionForEdit(_ context.Context, actor exerciseUseCase.Actor, id, versionID uuid.UUID) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, fakeTaskAsk{actor, id, versionID})
	if f.err != nil {
		return nil, f.err
	}
	return f.archive, nil
}

func (f *fakeTasks) askedCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.asked) }

func newTaskRig(t *testing.T, tasks *fakeTasks) *provideRig {
	t.Helper()
	return newProvideRig(t, func(d *assistant.Dependencies) { d.Tasks = tasks })
}

func (p *provideRig) taskFrame(seq int64, id string, subject, exercise, version string) outboxFrame {
	return outboxFrame{seq: seq, event: "provide_task", data: fmt.Sprintf(`{"request_id":%q,"type":"provide_task","subject":%q,"task_ref":%q,"version":%q}`, id, subject, exercise, version)}
}

func (p *provideRig) ask(id string) outboxFrame {
	return p.taskFrame(1, id, p.subject.String(), runExercise.String(), runVersion.String())
}

func TestProvideTaskDeliversTheArchiveOfAnEditorAsAnUploadAndAnswers(t *testing.T) {
	tasks := &fakeTasks{archive: []byte("PK-the-archive")}
	p := newTaskRig(t, tasks)
	p.users.users[runActor.UserID] = userModel.User{ID: runActor.UserID, Role: rbac.RoleAdmin, Status: userModel.UserStatusActive}
	p.brain.frames = []outboxFrame{p.ask("t1")}
	p.serve(t)
	answer := p.answeredOnce(t, "t1")
	require.Contains(t, answer, `"task_uploaded":true`)
	require.NotContains(t, answer, "error")

	// UploadTaskArchive: the meta {request_id, size, sha256} first, then the bytes
	uploads := p.brain.uploaded("t1")
	require.Len(t, uploads, 1)
	sum := sha256.Sum256([]byte("PK-the-archive"))
	assert.Equal(t, "t1", uploads[0].Meta.GetRequestId())
	assert.Equal(t, uint64(len("PK-the-archive")), uploads[0].Meta.GetSize())
	assert.Equal(t, hex.EncodeToString(sum[:]), uploads[0].Meta.GetSha256())
	assert.Equal(t, []byte("PK-the-archive"), uploads[0].Data)
	assert.Empty(t, p.brain.fake.Outbox.Pending(), "a stored archive also closes the request at the brain")

	// it is authorized as the user the subject belongs to, for that task and version
	require.Len(t, tasks.asked, 1)
	assert.Equal(t, exerciseUseCase.Actor{UserID: runActor.UserID, Role: rbac.RoleAdmin}, tasks.asked[0].Actor)
	assert.Equal(t, runExercise, tasks.asked[0].Exercise)
	assert.Equal(t, runVersion, tasks.asked[0].Version)
}

// What the exercise use case refuses is what the brain is told, never the archive: forbidden for a user who may not edit the
// task, not_found for a task or a version that does not exist (or is not of that task).
func TestProvideTaskRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		code string
	}{
		"the user may not edit the task": {exerciseModel.ErrExerciseForbidden.Err(), "forbidden"},
		"there is no such task":          {exerciseModel.ErrExerciseNotFound.Err(), "not_found"},
		"the version is not of the task": {exerciseModel.ErrExerciseVersionNotFound.Err(), "not_found"},
		"the platform fails":             {errors.New("database down"), "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &fakeTasks{archive: []byte("never sent"), err: c.err}
			p := newTaskRig(t, tasks)
			p.brain.frames = []outboxFrame{p.ask("t1")}
			p.serve(t)
			assert.Equal(t, c.code, refusalCode(t, p.answeredOnce(t, "t1")))
			assert.Empty(t, p.brain.uploaded("t1"), "an archive is never uploaded for a refused request")
		})
	}
}

func TestProvideTaskNeverAsksTheExerciseUseCaseForAnUnknownOrBlockedUser(t *testing.T) {
	for name, prep := range map[string]func(p *provideRig){
		"the user is blocked": func(p *provideRig) {
			p.users.users[runActor.UserID] = userModel.User{ID: runActor.UserID, Status: userModel.UserStatusBlocked}
		},
		"the user is gone": func(p *provideRig) { delete(p.users.users, runActor.UserID) },
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &fakeTasks{archive: []byte("x")}
			p := newTaskRig(t, tasks)
			prep(p)
			p.brain.frames = []outboxFrame{p.ask("t1")}
			p.serve(t)
			assert.Equal(t, "forbidden", refusalCode(t, p.answeredOnce(t, "t1")))
			assert.Zero(t, tasks.askedCount())
		})
	}
	t.Run("a subject that belongs to nobody", func(t *testing.T) {
		tasks := &fakeTasks{archive: []byte("x")}
		p := newTaskRig(t, tasks)
		p.brain.frames = []outboxFrame{p.taskFrame(1, "t1", uuid.Must(uuid.NewV7()).String(), runExercise.String(), runVersion.String())}
		p.serve(t)
		assert.Equal(t, "unknown_subject", refusalCode(t, p.answeredOnce(t, "t1")))
		assert.Zero(t, tasks.askedCount())
	})
	t.Run("ids that are no ids", func(t *testing.T) {
		tasks := &fakeTasks{archive: []byte("x")}
		p := newTaskRig(t, tasks)
		p.brain.frames = []outboxFrame{p.taskFrame(1, "t1", p.subject.String(), "not-an-id", runVersion.String())}
		p.serve(t)
		assert.Equal(t, "invalid_request", refusalCode(t, p.answeredOnce(t, "t1")))
		assert.Zero(t, tasks.askedCount())
	})
}

func TestProvideTaskWithoutATaskSourceIsUnavailable(t *testing.T) {
	p := newProvideRig(t, nil)
	p.brain.frames = []outboxFrame{p.ask("t1")}
	p.serve(t)
	assert.Equal(t, "unavailable", refusalCode(t, p.answeredOnce(t, "t1")))
}

// Unavailable is retried; AlreadyExists (the brain has the archive already, a request seen again after a restart) counts as
// delivered; any other refusal ends in upload_failed and nothing else is claimed.
func TestProvideTaskUploadOutcomes(t *testing.T) {
	for name, c := range map[string]struct {
		codes    []codes.Code
		uploaded int
		code     string // empty: answered with the result
	}{
		"unavailable then success":  {[]codes.Code{codes.Unavailable}, 1, ""},
		"rate limited then success": {[]codes.Code{codes.ResourceExhausted}, 1, ""},
		"the brain has it already":  {[]codes.Code{codes.AlreadyExists}, 0, ""},
		"a final refusal":           {[]codes.Code{codes.InvalidArgument}, 0, "upload_failed"},
		"an internal error":         {[]codes.Code{codes.Internal}, 0, "upload_failed"},
		"unavailable for good":      {[]codes.Code{codes.Unavailable, codes.Unavailable, codes.Unavailable}, 0, "upload_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newTaskRig(t, &fakeTasks{archive: []byte("zip")})
			p.brain.fake.Archives.FailNext(c.codes...)
			p.brain.frames = []outboxFrame{p.ask("t1")}
			p.serve(t)
			answer := p.answeredOnce(t, "t1")
			if c.code == "" {
				assert.Contains(t, answer, `"task_uploaded":true`)
			} else {
				assert.Equal(t, c.code, refusalCode(t, answer))
			}
			assert.Len(t, p.brain.uploaded("t1"), c.uploaded)
		})
	}
}

// Nothing is sent unasked: with no request in the outbox no archive is exported or uploaded.
func TestProvideTaskIsNeverUnsolicited(t *testing.T) {
	tasks := &fakeTasks{archive: []byte("zip")}
	p := newTaskRig(t, tasks)
	p.serve(t)
	time.Sleep(60 * time.Millisecond)
	assert.Zero(t, tasks.askedCount())
	assert.Empty(t, p.brain.uploaded("t1"))
}
