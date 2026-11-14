package useCase

import (
	"context"
	"net/http"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	assistantUseCase "github.com/cybericebox/daemon/internal/useCase/assistant"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

// AssistantProxy passes one call of the authoring chat to cyber-brain as the user (the HTTP handler's port).
func (u *UseCase) AssistantProxy(ctx context.Context, user uuid.UUID, call assistantUseCase.Call) (*http.Response, error) {
	return u.Assistant.Proxy(ctx, user, call)
}

// AssistantSaveSession imports the finished task of a chat session into the catalog and tells the brain (done), or
// queues the background import of a task with device images (not done yet).
func (u *UseCase) AssistantSaveSession(ctx context.Context, actor exerciseUseCase.Actor, sessionID string) (bool, error) {
	return u.Assistant.SaveSession(ctx, actor, sessionID)
}

// The statistics collection settings (platform admins; the HTTP handler's port).

func (u *UseCase) AssistantStatsDefaults(ctx context.Context) ([]assistantUseCase.SourceMode, error) {
	return u.Assistant.StatsDefaults(ctx)
}

func (u *UseCase) AssistantSetStatsDefault(ctx context.Context, source string, mode assistantModel.StatsMode) error {
	return u.Assistant.SetStatsDefault(ctx, source, mode)
}

func (u *UseCase) AssistantEventStats(ctx context.Context, event uuid.UUID) (assistantUseCase.EventStatsMode, error) {
	return u.Assistant.EventStats(ctx, event)
}

func (u *UseCase) AssistantSetEventStats(ctx context.Context, event uuid.UUID, mode assistantModel.StatsMode) error {
	return u.Assistant.SetEventStats(ctx, event, mode)
}

// The grader runs of a variant (the HTTP handler's port): authorized like the variant's test deploy.

func (u *UseCase) StartGraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) (assistantModel.Run, error) {
	return u.Assistant.StartGraderRun(ctx, actor, exerciseID, versionID, variantID)
}

func (u *UseCase) GraderRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) ([]assistantModel.Run, error) {
	return u.Assistant.GraderRuns(ctx, actor, exerciseID, versionID, variantID)
}

func (u *UseCase) GraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (assistantUseCase.RunView, error) {
	return u.Assistant.GraderRun(ctx, actor, exerciseID, versionID, variantID, runID)
}

func (u *UseCase) StopGraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (assistantModel.Run, error) {
	return u.Assistant.StopGraderRun(ctx, actor, exerciseID, versionID, variantID, runID)
}

func (u *UseCase) GraderRunEvents(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error) {
	return u.Assistant.GraderRunEvents(ctx, actor, exerciseID, versionID, variantID, runID)
}
