package assistant

import (
	"context"
	"errors"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/assistantRunRepo"
	"github.com/cybericebox/daemon/internal/model"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
)

// The defaults of the budget every opponent run gets when the platform sets none.
const (
	defaultOpponentActions = 100
	defaultOpponentTokens  = 500000
	defaultOpponentSeconds = 1200
)

func (u *UseCase) opponentBudget() *assistantModel.RunBudget {
	b := u.oppBudget
	if b.Actions <= 0 {
		b.Actions = defaultOpponentActions
	}
	if b.Tokens <= 0 {
		b.Tokens = defaultOpponentTokens
	}
	if b.Time <= 0 {
		b.Time = defaultOpponentSeconds
	}
	return &b
}

// TODO(owner-design): whether an opponent run succeeded is OPEN. The attacker is judged by the task's success check over the
// run's window, the defender by that check never firing and the service staying available (the availability prober is a
// later task); how a result affects anything is the platform's decision too. Until then a run stores the brain's events and
// its measurement as they come and the platform judges nothing: no verdict, no score, no statistics.

// toolsPlacement is where an opponent's one tools device sits, and what it is told. This is the single place that decides
// it: an attacker (and an attack-defender, who must be able to reach both) sits on its foothold, a defender on the segment of
// the devices it operates.
//
// TODO(owner-design): the defender's admin credentials for its operates devices belong on the tools host, so the brain gets no
// extra secrets. The manifest holds none today (a device's access entry names a user and a command, never a password or key),
// so nothing is placed; this waits for where those credentials come from.
func toolsPlacement(o exerciseModel.Opponent, run uuid.UUID, trustCA string) exerciseModel.ToolsPlacement {
	place := exerciseModel.ToolsPlacement{Env: exerciseModel.EDAEnv(run.String(), trustCA)}
	if o.Role == exerciseModel.OpponentDefense {
		place.Beside = o.Operates
	} else {
		place.Foothold = o.Foothold
	}
	return place
}

// opponentOf reads the opponent at a 1-based position of the variant's manifest, for an actor who may test the variant.
func (u *UseCase) opponentOf(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) (exerciseUseCase.RunTarget, error) {
	if err := u.runsReady(); err != nil {
		return exerciseUseCase.RunTarget{}, err
	}
	if err := u.labs.AuthorizeTestDeploy(ctx, actor, exerciseID, versionID, variantID); err != nil {
		return exerciseUseCase.RunTarget{}, err
	}
	target, err := u.labs.RunTarget(ctx, versionID, variantID, u.runMax)
	if err != nil {
		return exerciseUseCase.RunTarget{}, err
	}
	if target.ExerciseID != exerciseID || target.VariantID != variantID {
		return exerciseUseCase.RunTarget{}, exerciseModel.ErrExerciseVersionNotFound.Err()
	}
	if index < 1 || index > len(target.Opponents) {
		return exerciseUseCase.RunTarget{}, assistantModel.ErrAssistantOpponentNotFound.Err()
	}
	return target, nil
}

// StartOpponentRun starts the opponent at a 1-based position of the variant's manifest against the author's test lab of the
// variant: the lab is deployed with the platform tools device in it (the same path, limits and authorization as the author's
// own test deploy), and the leader then starts the run at the brain once the lab is ready (and, for an opponent that
// starts after a subtask, once that subtask is solved in the lab). The run is returned as it is, still starting. An
// opponent has at most one active run, a user a few.
func (u *UseCase) StartOpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) (assistantModel.Run, error) {
	target, err := u.opponentOf(ctx, actor, exerciseID, versionID, variantID, index)
	if err != nil {
		return assistantModel.Run{}, err
	}
	if !target.HasLab {
		return assistantModel.Run{}, exerciseModel.ErrTestDeployNoLab.Err()
	}
	opponent := target.Opponents[index-1]
	if err = u.requireOpponentRoom(ctx, actor.UserID, variantID, index); err != nil {
		return assistantModel.Run{}, err
	}
	runID := uuid.Must(uuid.NewV7()) // known before the lab is made: the tools device is told which run it works for
	ca, err := u.brainCA(ctx)
	if err != nil {
		return assistantModel.Run{}, err
	}
	handle, err := u.labs.DeployVariantTestWithTools(ctx, actor.UserID, versionID, variantID, target.Lease, toolsPlacement(opponent, runID, ca))
	if errors.Is(err, exerciseModel.ErrToolsNoImage) || errors.Is(err, exerciseModel.ErrToolsNoVPN) || errors.Is(err, exerciseModel.ErrToolsNoSegment) {
		return assistantModel.Run{}, assistantModel.ErrAssistantOpponentUnavailable.Err()
	}
	if err != nil {
		return assistantModel.Run{}, err
	}
	deployID, err := uuid.FromString(handle.Group)
	if err != nil {
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("The test deploy has no id").Err()
	}
	now := u.now().UTC()
	run := assistantModel.Run{
		ID: runID, ExerciseID: exerciseID, VersionID: versionID, VariantID: variantID, DeployID: deployID,
		Actor: actor.UserID, Role: string(opponent.Role), Opponent: index, State: assistantModel.RunStarting, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(u.runMax),
	}
	if err = u.runs.Create(ctx, run, now); err != nil {
		u.dropLab(ctx, actor.UserID, deployID)
		if errors.Is(err, assistantRunRepo.ErrActive) {
			return assistantModel.Run{}, assistantModel.ErrAssistantRunActive.Err()
		}
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("Failed to save the run").Err()
	}
	return run, nil
}

// requireOpponentRoom refuses a run the opponent or the user has no room for (the unique index is the real guard).
func (u *UseCase) requireOpponentRoom(ctx context.Context, user, variant uuid.UUID, index int) error {
	of, err := u.runs.OpponentsOfVariant(ctx, variant, maxRunsListed)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list the runs").Err()
	}
	for _, r := range of {
		if r.Opponent == index && r.State.Active() {
			return assistantModel.ErrAssistantRunActive.Err()
		}
	}
	n, err := u.runs.CountActiveOf(ctx, user)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to count the runs").Err()
	}
	if n >= u.maxActive {
		return assistantModel.ErrAssistantRunLimit.Err()
	}
	return nil
}

// OpponentRuns lists the runs of one opponent of a variant, newest first.
func (u *UseCase) OpponentRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) ([]assistantModel.Run, error) {
	if _, err := u.opponentOf(ctx, actor, exerciseID, versionID, variantID, index); err != nil {
		return nil, err
	}
	runs, err := u.runs.OpponentsOfVariant(ctx, variantID, maxRunsListed)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to list the runs").Err()
	}
	out := runs[:0]
	for _, r := range runs {
		if isRunOf(r, index) && r.ExerciseID == exerciseID && r.VersionID == versionID {
			out = append(out, r)
		}
	}
	return out, nil
}

// OpponentRun reads one run of an opponent with its judged answers.
func (u *UseCase) OpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, runID uuid.UUID) (RunView, error) {
	run, err := u.loadRunOf(ctx, actor, exerciseID, versionID, variantID, index, runID)
	if err != nil {
		return RunView{}, err
	}
	answers, err := u.runs.Answers(ctx, runID)
	if err != nil {
		return RunView{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the answers of the run").Err()
	}
	return RunView{Run: run, Answers: answers}, nil
}

// StopOpponentRun ends a run of an opponent at the request of the actor; the leader tears it down (the brain is told to
// stop, the access is revoked, the lab and the tools device are destroyed).
func (u *UseCase) StopOpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, runID uuid.UUID) (assistantModel.Run, error) {
	run, err := u.loadRunOf(ctx, actor, exerciseID, versionID, variantID, index, runID)
	if err != nil {
		return assistantModel.Run{}, err
	}
	if _, err = u.runs.Close(ctx, run.ID, assistantModel.RunStopped, "", u.now().UTC()); err != nil {
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("Failed to stop the run").Err()
	}
	run, _, err = u.runs.Get(ctx, runID)
	if err != nil {
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the run").Err()
	}
	return run, nil
}

// OpponentRunEvents is GraderRunEvents for a run of an opponent: the stored events, never the brain's stream.
func (u *UseCase) OpponentRunEvents(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int, runID uuid.UUID) (func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error) {
	if _, err := u.loadRunOf(ctx, actor, exerciseID, versionID, variantID, index, runID); err != nil {
		return nil, err
	}
	return u.runEventReader(runID), nil
}
