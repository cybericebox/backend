package brainConnectionRepo

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

type fakeQueries struct {
	seq      int64
	seqErr   error
	lastSeq  postgres.SetBrainOutboxSeqParams
	row      *postgres.BrainConnection
	creates  int64
	updates  int64
	lastSet  postgres.SetBrainConnectionCertificateParams
	lastMake postgres.CreateBrainConnectionParams
}

func (f *fakeQueries) GetBrainConnection(context.Context, string) (postgres.BrainConnection, error) {
	if f.row == nil {
		return postgres.BrainConnection{}, pgx.ErrNoRows
	}
	return *f.row, nil
}

func (f *fakeQueries) CreateBrainConnection(_ context.Context, p postgres.CreateBrainConnectionParams) (int64, error) {
	f.lastMake = p
	return f.creates, nil
}

func (f *fakeQueries) SetBrainConnectionCertificate(_ context.Context, p postgres.SetBrainConnectionCertificateParams) (int64, error) {
	f.lastSet = p
	return f.updates, nil
}

func TestGetReportsAbsenceWithoutError(t *testing.T) {
	r := New(&fakeQueries{})
	_, found, err := r.Get(context.Background(), "brain.test:443")
	if err != nil || found {
		t.Fatalf("absent connection: found=%v err=%v", found, err)
	}
}

func TestGetMapsTheRow(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := New(&fakeQueries{row: &postgres.BrainConnection{Endpoint: "b:1", Tenant: "t", ClientCertPem: "cert", ClientKeyCiphertext: "ct", CaPem: "ca", CertNotAfter: at}})
	c, found, err := r.Get(context.Background(), "b:1")
	want := assistantModel.Connection{Endpoint: "b:1", Tenant: "t", CertPEM: "cert", KeyCiphertext: "ct", CAPEM: "ca", CertNotAfter: at}
	if err != nil || !found || c != want {
		t.Fatalf("got %+v found=%v err=%v", c, found, err)
	}
}

func TestCreateSaysWhetherThisReplicaWon(t *testing.T) {
	won, err := New(&fakeQueries{creates: 1}).Create(context.Background(), assistantModel.Connection{Endpoint: "b:1"})
	if err != nil || !won {
		t.Fatalf("first insert: won=%v err=%v", won, err)
	}
	won, err = New(&fakeQueries{creates: 0}).Create(context.Background(), assistantModel.Connection{Endpoint: "b:1"})
	if err != nil || won {
		t.Fatalf("conflicting insert: won=%v err=%v", won, err)
	}
}

func TestSetCertificateFiltersByEndpointAndTenant(t *testing.T) {
	f := &fakeQueries{updates: 1}
	ok, err := New(f).SetCertificate(context.Background(), "b:1", "tenant", "cert", "ct", "ca", time.Unix(10, 0), time.Unix(5, 0))
	if err != nil || !ok || f.lastSet.Endpoint != "b:1" || f.lastSet.Tenant != "tenant" || f.lastSet.ClientKeyCiphertext != "ct" {
		t.Fatalf("set: ok=%v err=%v params=%+v", ok, err, f.lastSet)
	}
}

func (f *fakeQueries) GetBrainOutboxSeq(context.Context, string) (int64, error) {
	return f.seq, f.seqErr
}

func (f *fakeQueries) SetBrainOutboxSeq(_ context.Context, p postgres.SetBrainOutboxSeqParams) (int64, error) {
	f.lastSeq = p
	return 1, nil
}

func TestOutboxCursor(t *testing.T) {
	if seq, err := New(&fakeQueries{seq: 41}).OutboxSeq(context.Background(), "b:1"); err != nil || seq != 41 {
		t.Fatalf("seq: %d %v", seq, err)
	}
	if seq, err := New(&fakeQueries{seqErr: pgx.ErrNoRows}).OutboxSeq(context.Background(), "b:1"); err != nil || seq != 0 {
		t.Fatalf("no connection reads as 0: %d %v", seq, err)
	}
	f := &fakeQueries{}
	if err := New(f).SetOutboxSeq(context.Background(), "b:1", 42); err != nil || f.lastSeq.OutboxSeq != 42 || f.lastSeq.Endpoint != "b:1" {
		t.Fatalf("set: %v %+v", err, f.lastSeq)
	}
}
