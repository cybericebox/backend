package assistantRunRepo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

// fake overrides what a test needs; every other query would panic on the nil interface, which a test must not reach.
type fake struct {
	Queries
	createErr error
	run       *postgres.AssistantRun
	down      postgres.SetAssistantRunStreamDownParams
	insertN   int64
	advanced  int
}

func (f *fake) CreateAssistantRun(context.Context, postgres.CreateAssistantRunParams) error {
	return f.createErr
}
func (f *fake) GetAssistantRun(context.Context, uuid.UUID) (postgres.AssistantRun, error) {
	if f.run == nil {
		return postgres.AssistantRun{}, pgx.ErrNoRows
	}
	return *f.run, nil
}
func (f *fake) SetAssistantRunStreamDown(_ context.Context, p postgres.SetAssistantRunStreamDownParams) (int64, error) {
	f.down = p
	return 1, nil
}
func (f *fake) InsertAssistantRunEvent(context.Context, postgres.InsertAssistantRunEventParams) (int64, error) {
	return f.insertN, nil
}
func (f *fake) AdvanceAssistantRunSeq(context.Context, postgres.AdvanceAssistantRunSeqParams) (int64, error) {
	f.advanced++
	return 1, nil
}

func TestCreateMapsTheActiveRunIndexToErrActive(t *testing.T) {
	f := &fake{createErr: &pgconn.PgError{Code: pgerrcode.UniqueViolation}}
	if err := New(f).Create(context.Background(), assistantModel.Run{}, time.Now()); !errors.Is(err, ErrActive) {
		t.Fatalf("a second active run of the variant must be ErrActive, got %v", err)
	}
	other := errors.New("down")
	f.createErr = other
	if err := New(f).Create(context.Background(), assistantModel.Run{}, time.Now()); !errors.Is(err, other) {
		t.Fatalf("any other failure passes through, got %v", err)
	}
}

func TestGetMapsARunAndMissingIsNotAnError(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	now := time.Unix(100, 0).UTC()
	f := &fake{run: &postgres.AssistantRun{
		ID: id, State: "closing", EndState: "failed", FailureReason: "brain_unreachable", LastSeq: 7,
		Measurement: []byte(`{"a":1}`), StreamDownSince: pgtype.Timestamptz{Time: now, Valid: true}, LabReadyAt: pgtype.Timestamptz{Time: now, Valid: true}, ExpiresAt: now,
	}}
	got, found, err := New(f).Get(context.Background(), id)
	if err != nil || !found || got.State != assistantModel.RunClosing || got.EndState != assistantModel.RunFailed || got.LastSeq != 7 ||
		got.FailureReason != "brain_unreachable" || string(got.Measurement) != `{"a":1}` || !got.StreamDownSince.Equal(now) || !got.LabReadyAt.Equal(now) || !got.StartedAt.IsZero() {
		t.Fatalf("%v %v %+v", err, found, got)
	}
	f.run = nil
	if _, found, err = New(f).Get(context.Background(), id); err != nil || found {
		t.Fatalf("no row is not found, not an error: %v %v", err, found)
	}
}

func TestStreamDownZeroClearsAndAppendReportsNewFrames(t *testing.T) {
	f := &fake{insertN: 1}
	r := New(f)
	if err := r.SetStreamDown(context.Background(), uuid.Nil, time.Time{}, time.Now()); err != nil || f.down.Since.Valid {
		t.Fatalf("a zero time must clear the column: %v %+v", err, f.down)
	}
	isNew, err := r.AppendEvent(context.Background(), uuid.Nil, assistantModel.RunEvent{Seq: 3}, time.Now())
	if err != nil || !isNew || f.advanced != 1 {
		t.Fatalf("%v %v %d", err, isNew, f.advanced)
	}
	f.insertN = 0
	if isNew, err = r.AppendEvent(context.Background(), uuid.Nil, assistantModel.RunEvent{Seq: 3}, time.Now()); err != nil || isNew {
		t.Fatalf("a frame seen again is not new: %v %v", err, isNew)
	}
}
