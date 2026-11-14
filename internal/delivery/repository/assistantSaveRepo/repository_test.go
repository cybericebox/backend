package assistantSaveRepo

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

type fake struct {
	row     *postgres.GetAssistantSaveRow
	created int64
	last    postgres.CreateAssistantSaveParams
	list    []postgres.ListUnsettledAssistantSavesRow
}

func (f *fake) GetAssistantSave(context.Context, postgres.GetAssistantSaveParams) (postgres.GetAssistantSaveRow, error) {
	if f.row == nil {
		return postgres.GetAssistantSaveRow{}, pgx.ErrNoRows
	}
	return *f.row, nil
}
func (f *fake) CreateAssistantSave(_ context.Context, p postgres.CreateAssistantSaveParams) (int64, error) {
	f.last = p
	return f.created, nil
}
func (f *fake) SetAssistantSaveExercise(context.Context, postgres.SetAssistantSaveExerciseParams) (int64, error) {
	return 1, nil
}
func (f *fake) SetAssistantSaveNotified(context.Context, postgres.SetAssistantSaveNotifiedParams) (int64, error) {
	return 1, nil
}
func (f *fake) DeleteAssistantSave(context.Context, postgres.DeleteAssistantSaveParams) (int64, error) {
	return 1, nil
}
func (f *fake) ListUnsettledAssistantSaves(context.Context, postgres.ListUnsettledAssistantSavesParams) ([]postgres.ListUnsettledAssistantSavesRow, error) {
	return f.list, nil
}

var (
	author   = uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111")
	exercise = uuid.FromStringOrNil("33333333-3333-4333-8333-333333333333")
	job      = uuid.FromStringOrNil("44444444-4444-4444-8444-444444444444")
)

func TestGetMapsAbsentPendingAndDone(t *testing.T) {
	ctx := context.Background()
	if _, found, err := New(&fake{}).Get(ctx, "s1", author); found || err != nil {
		t.Fatalf("absent: %v %v", found, err)
	}
	pending, found, err := New(&fake{row: &postgres.GetAssistantSaveRow{JobID: uuid.NullUUID{UUID: job, Valid: true}}}).Get(ctx, "s1", author)
	if err != nil || !found || !pending.Pending() || pending.JobID != job {
		t.Fatalf("pending: %+v %v %v", pending, found, err)
	}
	done, _, _ := New(&fake{row: &postgres.GetAssistantSaveRow{ExerciseID: uuid.NullUUID{UUID: exercise, Valid: true}, Notified: true}}).Get(ctx, "s1", author)
	if done.Pending() || done.ExerciseID != exercise || !done.Notified {
		t.Fatalf("done: %+v", done)
	}
}

func TestCreateStoresNullForTheMissingID(t *testing.T) {
	f := &fake{created: 1}
	won, err := New(f).Create(context.Background(), assistantModel.Save{Session: "s1", Author: author, JobID: job}, time.Unix(1, 0))
	if !won || err != nil || f.last.ExerciseID.Valid || !f.last.JobID.Valid || f.last.JobID.UUID != job {
		t.Fatalf("create: %v %v %+v", won, err, f.last)
	}
	if won, _ = New(&fake{}).Create(context.Background(), assistantModel.Save{Session: "s1", Author: author}, time.Unix(1, 0)); won {
		t.Fatal("a row already there must report false")
	}
}

func TestUnsettledMapsRows(t *testing.T) {
	f := &fake{list: []postgres.ListUnsettledAssistantSavesRow{{SessionID: "s1", AuthorID: author, JobID: uuid.NullUUID{UUID: job, Valid: true}}}}
	got, err := New(f).Unsettled(context.Background(), time.Unix(0, 0), 10)
	if err != nil || len(got) != 1 || !got[0].Pending() || got[0].JobID != job {
		t.Fatalf("unsettled: %+v %v", got, err)
	}
}
