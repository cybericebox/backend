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
	lastGRPC postgres.SetBrainConnectionGRPCEndpointParams
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

func (f *fakeQueries) SetBrainConnectionGRPCEndpoint(_ context.Context, p postgres.SetBrainConnectionGRPCEndpointParams) (int64, error) {
	f.lastGRPC = p
	return f.updates, nil
}

func TestTheGRPCEndpointIsStoredAndRead(t *testing.T) {
	f := &fakeQueries{updates: 1, row: &postgres.BrainConnection{Endpoint: "b:1", GrpcEndpoint: "b:2"}}
	c, _, err := New(f).Get(context.Background(), "b:1")
	if err != nil || c.GRPCEndpoint != "b:2" {
		t.Fatalf("read: %+v %v", c, err)
	}
	if _, err = New(f).Create(context.Background(), assistantModel.Connection{Endpoint: "b:1", GRPCEndpoint: "b:2"}); err != nil || f.lastMake.GrpcEndpoint != "b:2" {
		t.Fatalf("create: %+v %v", f.lastMake, err)
	}
	ok, err := New(f).SetGRPCEndpoint(context.Background(), "b:1", "b:3", time.Unix(5, 0))
	if err != nil || !ok || f.lastGRPC.Endpoint != "b:1" || f.lastGRPC.GrpcEndpoint != "b:3" || !f.lastGRPC.UpdatedAt.Equal(time.Unix(5, 0)) {
		t.Fatalf("set: %v %v %+v", ok, err, f.lastGRPC)
	}
}
