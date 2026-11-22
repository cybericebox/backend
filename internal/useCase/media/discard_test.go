package media_test

import (
	"context"
	"testing"

	"github.com/gofrs/uuid"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	media "github.com/cybericebox/backend/internal/useCase/media"
)

// A stored file nobody uses is removed at once by the one who stored it: the row goes (only an unreferenced file of that owner),
// the blob is left to the collector.
func TestDiscardFile_RemovesTheOwnersUnreferencedFile(t *testing.T) {
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	uc := media.NewMediaUseCase(media.Dependencies{Repo: q, Storage: newFakeStorage(), FilesSealer: testSealer(t), Config: config.MediaConfig{MaxUploadBytes: 1}})
	id, owner := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q.EXPECT().DeleteUnreferencedFile(gomock.Any(), postgres.DeleteUnreferencedFileParams{ID: id, CreatedBy: uuid.NullUUID{UUID: owner, Valid: true}}).Return(int64(1), nil)
	if err := uc.DiscardFile(context.Background(), id, owner); err != nil {
		t.Fatalf("DiscardFile: %v", err)
	}
}
