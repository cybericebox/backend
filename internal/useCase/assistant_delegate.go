package useCase

import (
	"context"
	"net/http"

	"github.com/gofrs/uuid"

	assistantUseCase "github.com/cybericebox/daemon/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

// AssistantProxy passes one call of the authoring chat to cyber-brain as the user (the HTTP handler's port).
func (u *UseCase) AssistantProxy(ctx context.Context, user uuid.UUID, call assistantUseCase.Call) (*http.Response, error) {
	return u.Assistant.Proxy(ctx, user, call)
}

// AssistantSaveSession imports the finished task of a chat session into the catalog and tells the brain.
func (u *UseCase) AssistantSaveSession(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) error {
	return u.Assistant.SaveSession(ctx, actor, sessionID)
}
