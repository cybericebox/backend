package assistantSubjectRepo

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
)

type fake struct {
	subject *uuid.UUID
	rows    int64
}

func (f *fake) GetAssistantSubject(context.Context, uuid.UUID) (uuid.UUID, error) {
	if f.subject == nil {
		return uuid.Nil, pgx.ErrNoRows
	}
	return *f.subject, nil
}

func (f *fake) CreateAssistantSubject(context.Context, postgres.CreateAssistantSubjectParams) (int64, error) {
	return f.rows, nil
}

func TestGetAndCreate(t *testing.T) {
	user := uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111")
	subject := uuid.FromStringOrNil("66666666-6666-4666-8666-666666666666")
	if _, found, err := New(&fake{}).Get(context.Background(), user); found || err != nil {
		t.Fatalf("absent: %v %v", found, err)
	}
	if got, found, err := New(&fake{subject: &subject}).Get(context.Background(), user); !found || err != nil || got != subject {
		t.Fatalf("present: %v %v %v", got, found, err)
	}
	if won, err := New(&fake{rows: 1}).Create(context.Background(), user, subject, time.Unix(1, 0)); !won || err != nil {
		t.Fatalf("create: %v %v", won, err)
	}
	if won, _ := New(&fake{}).Create(context.Background(), user, subject, time.Unix(1, 0)); won {
		t.Fatal("an existing pseudonym stays")
	}
}
