package assistantSaveRepo

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
)

type fake struct {
	id      *uuid.UUID
	created int64
	last    postgres.CreateAssistantSaveParams
}

func (f *fake) GetAssistantSave(context.Context, postgres.GetAssistantSaveParams) (uuid.UUID, error) {
	if f.id == nil {
		return uuid.Nil, pgx.ErrNoRows
	}
	return *f.id, nil
}

func (f *fake) CreateAssistantSave(_ context.Context, p postgres.CreateAssistantSaveParams) (int64, error) {
	f.last = p
	return f.created, nil
}

func TestGetAndPut(t *testing.T) {
	author := uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111")
	exercise := uuid.FromStringOrNil("33333333-3333-4333-8333-333333333333")
	if _, found, err := New(&fake{}).Get(context.Background(), "s1", author); found || err != nil {
		t.Fatalf("absent: %v %v", found, err)
	}
	if id, found, err := New(&fake{id: &exercise}).Get(context.Background(), "s1", author); !found || err != nil || id != exercise {
		t.Fatalf("present: %v %v %v", id, found, err)
	}
	f := &fake{created: 1}
	if won, err := New(f).Put(context.Background(), "s1", author, exercise, time.Unix(1, 0)); !won || err != nil || f.last.SessionID != "s1" || f.last.AuthorID != author {
		t.Fatalf("put: %v %v %+v", won, err, f.last)
	}
	if won, _ := New(&fake{}).Put(context.Background(), "s1", author, exercise, time.Unix(1, 0)); won {
		t.Fatal("a row already there must report false")
	}
}
