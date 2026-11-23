package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/eventAnswerFileRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/eventRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	"github.com/cybericebox/backend/internal/delivery/repository/userRepo"
	eventFormModel "github.com/cybericebox/backend/internal/model/eventForm"
	userModel "github.com/cybericebox/backend/internal/model/user"
	"github.com/cybericebox/backend/internal/testhelpers"
)

// Answer files against the real schema: upload keeps the blob referenced,
// saving an answer attaches its file and releases the one it replaced, and
// retention removes unused uploads and orphaned references.
func TestEventAnswerFilesLifecycle(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", sql, err)
		}
		return n
	}
	user, err := userRepo.New(db.Queries).Create(ctx, userModel.NewIncompleteUser(uuid.Must(uuid.NewV7()), "answer-files@test.test", now))
	if err != nil {
		t.Fatal(err)
	}
	e := mustCreateEvent(t, eventRepo.New(db.Queries), "answerfiles", "Answer files", now, now.Add(30*24*time.Hour), user.ID, now)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO file_blobs (content_hash, size_bytes, ref_count, created_at) VALUES ('hash', 4, 3, $1)`, now); err != nil {
		t.Fatal(err)
	}
	repo := eventAnswerFileRepo.New(db.Queries)
	upload := func(createdAt time.Time) uuid.UUID {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		if _, err := db.Pool.Exec(ctx, `INSERT INTO files (id, name, content_type, size_bytes, content_hash, created_at) VALUES ($1, 'cv.pdf', 'application/pdf', 4, 'hash', $2)`, id, createdAt); err != nil {
			t.Fatal(err)
		}
		if err := repo.Create(ctx, eventFormModel.NewAnswerFile(id, e.ID, eventFormModel.AnswerScopeParticipant, "cv", "cv.pdf", 4, "application/pdf", user.ID, createdAt)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first, second, unused := upload(now), upload(now), upload(now.Add(-48*time.Hour))
	if n := count(`SELECT count(*) FROM file_references WHERE ref_type = 'event_answer_file'`); n != 3 {
		t.Fatalf("every upload must be referenced, got %d", n)
	}

	if err = repo.Attach(ctx, e.ID, eventFormModel.AnswerScopeParticipant, user.ID, []uuid.UUID{first}, now); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, first)
	if err != nil || got.AttachedAt == nil || got.OwnerID.UUID != user.ID {
		t.Fatalf("attached file = %+v, %v", got, err)
	}
	// A new answer replaces the file: the old one is released.
	if err = repo.Attach(ctx, e.ID, eventFormModel.AnswerScopeParticipant, user.ID, []uuid.UUID{second}, now); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM event_answer_files WHERE file_id = $1`, first); n != 0 {
		t.Fatal("the replaced file must be released")
	}
	if n := count(`SELECT count(*) FROM file_references WHERE ref_id = $1`, first); n != 0 {
		t.Fatal("the replaced file must lose its reference")
	}
	// The upload caps count only what waits for an answer: the attached file is not counted, the waiting upload
	// is; another question has none of its own waiting, another person nothing at all.
	usage, err := repo.Usage(ctx, e.ID, user.ID, eventFormModel.AnswerScopeParticipant, "cv")
	if err != nil || usage != (eventAnswerFileRepo.Usage{PendingInField: 1, EventPendingBytes: 4, UserPendingBytes: 4}) {
		t.Fatalf("usage = %+v, %v", usage, err)
	}
	if usage, err = repo.Usage(ctx, e.ID, user.ID, eventFormModel.AnswerScopeParticipant, "photo"); err != nil || usage != (eventAnswerFileRepo.Usage{EventPendingBytes: 4, UserPendingBytes: 4}) {
		t.Fatalf("usage of another question = %+v, %v", usage, err)
	}
	if usage, err = repo.Usage(ctx, e.ID, uuid.Must(uuid.NewV7()), eventFormModel.AnswerScopeParticipant, "cv"); err != nil || usage != (eventAnswerFileRepo.Usage{}) {
		t.Fatalf("usage of another person = %+v, %v", usage, err)
	}

	// An orphaned reference (its row removed with a team or an account).
	orphan := uuid.Must(uuid.NewV7())
	if _, err = db.Pool.Exec(ctx, `INSERT INTO files (id, name, content_hash, created_at) VALUES ($1, 'x', 'hash', $2)`, orphan, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO file_references (ref_type, ref_id, file_id) VALUES ('event_answer_file', $1, $1)`, orphan); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Queries.PurgeEventAnswerFiles(ctx, postgres.PurgeEventAnswerFilesParams{EndedBefore: now.Add(-365 * 24 * time.Hour), PendingBefore: now.Add(-24 * time.Hour), BatchSize: 100}); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM event_answer_files WHERE file_id = $1`, unused); n != 0 {
		t.Fatal("an upload unused past its period must be removed")
	}
	if n := count(`SELECT count(*) FROM file_references WHERE ref_type = 'event_answer_file'`); n != 1 {
		t.Fatalf("only the attached file keeps its reference, got %d", n)
	}
	if n := count(`SELECT count(*) FROM event_answer_files WHERE file_id = $1`, second); n != 1 {
		t.Fatal("the attached file of a running event must stay")
	}
}

// The upload caps hold under concurrency: CreateWithinCaps takes the person's lock in its transaction and re-reads
// their usage, so parallel uploads never store more than the caps allow, whatever the checks before saw.
func TestEventAnswerFilesCapsHoldUnderConcurrentUploads(t *testing.T) {
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	user, err := userRepo.New(db.Queries).Create(ctx, userModel.NewIncompleteUser(uuid.Must(uuid.NewV7()), "answer-caps@test.test", now))
	if err != nil {
		t.Fatal(err)
	}
	e := mustCreateEvent(t, eventRepo.New(db.Queries), "answercaps", "Answer caps", now, now.Add(30*24*time.Hour), user.ID, now)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO file_blobs (content_hash, size_bytes, ref_count, created_at) VALUES ('caps', 10, 60, $1)`, now); err != nil {
		t.Fatal(err)
	}
	other := mustCreateEvent(t, eventRepo.New(db.Queries), "answercaps2", "Answer caps 2", now, now.Add(30*24*time.Hour), user.ID, now)
	race := func(eventID uuid.UUID, field string, n int, caps eventAnswerFileRepo.Caps) int {
		t.Helper()
		var (
			wg     sync.WaitGroup
			mu     sync.Mutex
			stored int
		)
		start := make(chan struct{})
		for range n {
			id := uuid.Must(uuid.NewV7())
			if _, err := db.Pool.Exec(ctx, `INSERT INTO files (id, name, content_type, size_bytes, content_hash, created_at) VALUES ($1, 'cv.pdf', 'application/pdf', 10, 'caps', $2)`, id, now); err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				tx, txErr := db.Pool.Begin(ctx)
				if txErr != nil {
					t.Error(txErr)
					return
				}
				defer func() { _ = tx.Rollback(ctx) }()
				repo := eventAnswerFileRepo.New(db.Queries.WithTx(tx))
				_, ok, createErr := repo.CreateWithinCaps(ctx, eventFormModel.NewAnswerFile(id, eventID, eventFormModel.AnswerScopeParticipant, field, "cv.pdf", 10, "application/pdf", user.ID, now), caps)
				if createErr != nil {
					t.Error(createErr)
					return
				}
				if commitErr := tx.Commit(ctx); commitErr != nil {
					t.Error(commitErr)
					return
				}
				if ok {
					mu.Lock()
					stored++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		return stored
	}
	if stored := race(e.ID, "cv", 12, eventAnswerFileRepo.Caps{PendingPerField: 3, EventPendingBytes: 1000, UserPendingBytes: 1000}); stored != 3 {
		t.Fatalf("pending cap 3: %d uploads stored", stored)
	}
	// 30 bytes wait in the three uploads of "cv"; an event budget of 80 leaves room for five more files of 10 bytes.
	if stored := race(e.ID, "photo", 12, eventAnswerFileRepo.Caps{PendingPerField: 100, EventPendingBytes: 80, UserPendingBytes: 1000}); stored != 5 {
		t.Fatalf("event byte budget: %d uploads stored", stored)
	}
	// 80 bytes wait in the first event; a budget of 120 for all events leaves room for four in another event.
	if stored := race(other.ID, "cv", 12, eventAnswerFileRepo.Caps{PendingPerField: 100, EventPendingBytes: 1000, UserPendingBytes: 120}); stored != 4 {
		t.Fatalf("budget of all events: %d uploads stored", stored)
	}
	var rows int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM event_answer_files WHERE uploaded_by = $1`, user.ID).Scan(&rows); err != nil || rows != 12 {
		t.Fatalf("rows = %d, %v", rows, err)
	}
}
