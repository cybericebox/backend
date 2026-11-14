package assistantSaveRepo

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

type fake struct {
	row   *postgres.GetAssistantSaveRow
	rows  int64
	claim postgres.ClaimAssistantSaveParams
	list  []postgres.ListUnsettledAssistantSavesRow
}

func (f *fake) GetAssistantSave(context.Context, postgres.GetAssistantSaveParams) (postgres.GetAssistantSaveRow, error) {
	if f.row == nil {
		return postgres.GetAssistantSaveRow{}, pgx.ErrNoRows
	}
	return *f.row, nil
}
func (f *fake) ClaimAssistantSave(_ context.Context, p postgres.ClaimAssistantSaveParams) (int64, error) {
	f.claim = p
	return f.rows, nil
}
func (f *fake) SetAssistantSaveJob(context.Context, postgres.SetAssistantSaveJobParams) (int64, error) {
	return f.rows, nil
}
func (f *fake) CompleteAssistantSave(context.Context, postgres.CompleteAssistantSaveParams) (int64, error) {
	return f.rows, nil
}
func (f *fake) FailAssistantSave(context.Context, postgres.FailAssistantSaveParams) (int64, error) {
	return f.rows, nil
}
func (f *fake) SetAssistantSaveNotified(context.Context, postgres.SetAssistantSaveNotifiedParams) (int64, error) {
	return f.rows, nil
}
func (f *fake) ListUnsettledAssistantSaves(context.Context, postgres.ListUnsettledAssistantSavesParams) ([]postgres.ListUnsettledAssistantSavesRow, error) {
	return f.list, nil
}

var (
	author   = uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111")
	exercise = uuid.FromStringOrNil("33333333-3333-4333-8333-333333333333")
	job      = uuid.FromStringOrNil("44444444-4444-4444-8444-444444444444")
	claimed  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

func TestGetMapsAbsentPendingAndDone(t *testing.T) {
	ctx := context.Background()
	if _, found, err := New(&fake{}).Get(ctx, "s1", author); found || err != nil {
		t.Fatalf("absent: %v %v", found, err)
	}
	at := pgtype.Timestamptz{Time: claimed, Valid: true}
	pending, found, err := New(&fake{row: &postgres.GetAssistantSaveRow{State: "pending", JobID: uuid.NullUUID{UUID: job, Valid: true}, ClaimedAt: at}}).Get(ctx, "s1", author)
	if err != nil || !found || !pending.Pending() || pending.JobID != job || pending.ExerciseID != uuid.Nil || !pending.ClaimedAt.Equal(claimed) {
		t.Fatalf("pending: %+v %v %v", pending, found, err)
	}
	done, _, _ := New(&fake{row: &postgres.GetAssistantSaveRow{State: "done", ExerciseID: uuid.NullUUID{UUID: exercise, Valid: true}, Notified: true}}).Get(ctx, "s1", author)
	if done.State != assistantModel.SaveDone || done.ExerciseID != exercise || !done.Notified {
		t.Fatalf("done: %+v", done)
	}
}

func TestClaimSaysWhetherTheCallerOwnsIt(t *testing.T) {
	f := &fake{rows: 1}
	won, err := New(f).Claim(context.Background(), "s1", author, "abc", claimed, claimed.Add(-time.Minute))
	if !won || err != nil || f.claim.ArchiveHash != "abc" || !f.claim.StaleBefore.Equal(claimed.Add(-time.Minute)) {
		t.Fatalf("claim: %v %v %+v", won, err, f.claim)
	}
	if won, _ = New(&fake{}).Claim(context.Background(), "s1", author, "abc", claimed, claimed); won {
		t.Fatal("0 rows means another request holds the claim")
	}
}

func TestCompleteReportsWhetherTheSaveWasPending(t *testing.T) {
	if ok, _ := New(&fake{rows: 1}).Complete(context.Background(), "s1", author, exercise); !ok {
		t.Fatal("pending save completes")
	}
	if ok, _ := New(&fake{}).Complete(context.Background(), "s1", author, exercise); ok {
		t.Fatal("a save that is not pending is left alone")
	}
}

func TestUnsettledMapsRows(t *testing.T) {
	f := &fake{list: []postgres.ListUnsettledAssistantSavesRow{{SessionID: "s1", AuthorID: author, State: "pending", JobID: uuid.NullUUID{UUID: job, Valid: true}}}}
	got, err := New(f).Unsettled(context.Background(), time.Unix(0, 0), 10)
	if err != nil || len(got) != 1 || !got[0].Pending() || got[0].JobID != job {
		t.Fatalf("unsettled: %+v %v", got, err)
	}
}
