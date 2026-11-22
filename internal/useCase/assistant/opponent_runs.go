package assistant

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain"
	"github.com/cybericebox/backend/internal/delivery/repository/assistantRunRepo"
	"github.com/cybericebox/backend/internal/model"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// The defaults of the budget every opponent run gets when the platform sets none.
const (
	defaultOpponentActions = 100
	defaultOpponentTokens  = 500000
	defaultOpponentSeconds = 1200
)

func (u *UseCase) opponentBudget() *brainapiv1.Budget {
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
	return &brainapiv1.Budget{Actions: uint32(b.Actions), Tokens: uint64(b.Tokens), Seconds: uint32(b.Time)} //nolint:gosec // positive, from the config
}

// TODO(owner-design): whether an opponent run succeeded is OPEN. The attacker is judged by the task's success check over the
// run's window, the defender by that check never firing and the service staying available (the availability prober is a
// later task); how a result affects anything is the platform's decision too. Until then a run stores the brain's events and
// its measurement as they come and the platform judges nothing: no verdict, no score, no statistics.

// toolsPlacement is where an opponent's own tools device sits, and what it is told. This is the single place that decides
// it: an attacker (and an attack-defender, who must be able to reach both) sits on its foothold, a defender on the segment of
// the devices it operates.
//
// opponentSpec asks the brain for the opponent (GetOpponent) and turns its spec into the placement of the tools device: the
// image and the env as the brain gave them, the size as a hint, the short-lived pull credential (kept in memory only, for this
// deploy). ok is false when the brain has no specs (Unimplemented): the caller then deploys the fallback tools image of the
// platform (BRAIN_TOOLS_IMAGE). The inputs StartRun carries are fixed (the run's VPN client, the runner address, the manifest's
// foothold), so an attacker or attack-defender without a foothold is refused before the brain is asked; a pull credential must
// still be valid (one that expired on the way is fetched once more; one without expires_at is reusable).
func (u *UseCase) opponentSpec(ctx context.Context, runID uuid.UUID, o exerciseModel.Opponent, exerciseID, versionID uuid.UUID, place exerciseModel.ToolsPlacement) (exerciseModel.ToolsPlacement, string, bool, error) {
	if o.Role != exerciseModel.OpponentDefense && o.Foothold == "" {
		return place, "", false, assistantModel.ErrAssistantOpponentSpecInvalid.WithMessage("The opponent needs a foothold the manifest does not name").Err()
	}
	c, err := u.control(ctx)
	if err != nil {
		return place, "", false, err
	}
	req := &brainapiv1.GetOpponentRequest{RunId: runID.String(), Role: string(o.Role), Band: uint32(max(o.Band, 0)), TaskRef: exerciseID.String(), Version: versionID.String()} //nolint:gosec // 1..6
	var spec *brainapiv1.OpponentSpec
	for attempt := 1; ; attempt++ {
		if spec, err = c.GetOpponent(ctx, req); err != nil {
			switch brain.Code(err) {
			case codes.Unimplemented:
				return place, "", false, nil
			case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
				return place, "", false, assistantModel.ErrAssistantBrainUnreachable.WithError(err).Err()
			}
			log.Warn().Str("code", brain.Code(err).String()).Str("reason", brain.Reason(err)).Msg("The brain refused an opponent")
			return place, "", false, assistantModel.ErrAssistantOpponentUnavailable.WithError(err).Err()
		}
		pull := spec.GetDeploy().GetPull()
		if pull == nil || pull.GetExpiresAt() == nil || u.now().Before(pull.GetExpiresAt().AsTime()) || attempt >= 2 {
			break
		}
	}
	deploy := spec.GetDeploy()
	if deploy.GetImage() == "" || spec.GetSpecId() == "" {
		return place, "", false, assistantModel.ErrAssistantOpponentSpecInvalid.WithMessage("The opponent spec has no image or no id").Err()
	}
	if pull := deploy.GetPull(); pull != nil {
		if pull.GetExpiresAt() != nil && !u.now().Before(pull.GetExpiresAt().AsTime()) {
			return place, "", false, assistantModel.ErrAssistantOpponentSpecInvalid.WithMessage("The pull credential of the opponent image has expired").Err()
		}
		place.Pull = &exerciseModel.ToolsPull{Registry: pull.GetRegistry(), Username: pull.GetUsername(), Password: pull.GetPassword()}
	}
	place.Image = deploy.GetImage()
	place.Env = make([]exerciseModel.EnvVar, 0, len(deploy.GetEnv()))
	for _, name := range slices.Sorted(maps.Keys(deploy.GetEnv())) {
		place.Env = append(place.Env, exerciseModel.EnvVar{Name: name, Value: deploy.GetEnv()[name]})
	}
	place.CPUMillis, place.MemoryBytes = int64(deploy.GetResources().GetCpuMillis()), int64(deploy.GetResources().GetMemBytes()) //nolint:gosec // sizes
	return place, spec.GetSpecId(), true, nil
}

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

// opponentTarget reads the task of a variant for an actor who may test the variant.
func (u *UseCase) opponentTarget(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) (exerciseUseCase.RunTarget, error) {
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
	return target, nil
}

// opponentOf reads the opponent at a 1-based position of the variant's manifest, for an actor who may test the variant.
func (u *UseCase) opponentOf(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) (exerciseUseCase.RunTarget, error) {
	target, err := u.opponentTarget(ctx, actor, exerciseID, versionID, variantID)
	if err != nil {
		return exerciseUseCase.RunTarget{}, err
	}
	if index < 1 || index > len(target.Opponents) {
		return exerciseUseCase.RunTarget{}, assistantModel.ErrAssistantOpponentNotFound.Err()
	}
	return target, nil
}

// StartOpponentRun starts the opponent at a 1-based position of the variant's manifest against the author's test lab of the
// variant: StartOpponentRuns with that one opponent.
func (u *UseCase) StartOpponentRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, index int) (assistantModel.Run, error) {
	runs, err := u.StartOpponentRuns(ctx, actor, exerciseID, versionID, variantID, []int{index})
	if err != nil {
		return assistantModel.Run{}, err
	}
	return runs[0], nil
}

// StartOpponentRuns starts several opponents of the variant's manifest (their 1-based positions) together in one test lab of
// the variant: the lab is deployed once with a tools device per opponent (the same path, limits and authorization as the
// author's own test deploy), and every opponent gets a run of its own: its own GetOpponent spec, its own device (the run's
// ToolsIndex is the opponent's position in indexes), its own VPN client that reaches only that device, its own StartRun. The
// leader starts each run at the brain once the lab is ready (and, for an opponent that starts after a subtask, once that
// subtask is solved in the lab); the lab is destroyed when the last of its runs ends, a run that ends earlier revokes only its
// own client. The runs are returned in the order of indexes, still starting. An empty list, a repeated or unknown position and
// more than MaxOpponents are refused; an opponent has at most one active run, a user a few (each run counts).
func (u *UseCase) StartOpponentRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, indexes []int) ([]assistantModel.Run, error) {
	target, err := u.opponentTarget(ctx, actor, exerciseID, versionID, variantID)
	if err != nil {
		return nil, err
	}
	if len(indexes) == 0 || len(indexes) > exerciseModel.MaxOpponents {
		return nil, assistantModel.ErrAssistantOpponentNotFound.Err()
	}
	seen := make(map[int]bool, len(indexes))
	for _, index := range indexes {
		if index < 1 || index > len(target.Opponents) || seen[index] {
			return nil, assistantModel.ErrAssistantOpponentNotFound.Err()
		}
		seen[index] = true
	}
	if !target.HasLab {
		return nil, exerciseModel.ErrTestDeployNoLab.Err()
	}
	if err = u.requireOpponentRoom(ctx, actor.UserID, variantID, indexes); err != nil {
		return nil, err
	}
	ca, err := u.brainCA(ctx)
	if err != nil {
		return nil, err
	}
	runIDs := make([]uuid.UUID, len(indexes))
	specIDs := make([]string, len(indexes))
	places := make([]exerciseModel.ToolsPlacement, len(indexes))
	for i, index := range indexes {
		opponent := target.Opponents[index-1]
		runIDs[i] = uuid.Must(uuid.NewV7()) // known before the lab is made: the tools device is told which run it works for
		// The brain says what to deploy (GetOpponent); a brain without opponent specs leaves the platform's fallback tools image.
		if places[i], specIDs[i], _, err = u.opponentSpec(ctx, runIDs[i], opponent, exerciseID, versionID, toolsPlacement(opponent, runIDs[i], ca)); err != nil {
			return nil, err
		}
	}
	handle, err := u.labs.DeployVariantTestWithTools(ctx, actor.UserID, versionID, variantID, target.Lease, places)
	for i := range places {
		places[i].Pull = nil // the credential is the lab's now: it goes nowhere else
	}
	switch {
	case errors.Is(err, exerciseModel.ErrToolsTooLarge):
		return nil, assistantModel.ErrAssistantOpponentTooLarge.Err()
	case errors.Is(err, exerciseModel.ErrToolsNoImage) || errors.Is(err, exerciseModel.ErrToolsNoVPN) || errors.Is(err, exerciseModel.ErrToolsNoSegment) ||
		errors.Is(err, exerciseModel.ErrToolsPullConflict) || errors.Is(err, exerciseModel.ErrToolsTooMany):
		return nil, assistantModel.ErrAssistantOpponentUnavailable.Err()
	case err != nil:
		return nil, err
	}
	deployID, err := uuid.FromString(handle.Group)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("The test deploy has no id").Err()
	}
	now := u.now().UTC()
	runs := make([]assistantModel.Run, 0, len(indexes))
	for i, index := range indexes {
		run := assistantModel.Run{
			ID: runIDs[i], ExerciseID: exerciseID, VersionID: versionID, VariantID: variantID, DeployID: deployID, Actor: actor.UserID,
			Role: string(target.Opponents[index-1].Role), Opponent: index, ToolsIndex: i, SpecID: specIDs[i], State: assistantModel.RunStarting,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(u.runMax),
		}
		if err = u.runs.Create(ctx, run, now); err != nil {
			// The lab was made for runs that cannot all start: the ones saved already are ended, the lab is taken down.
			u.abandonRuns(ctx, runs)
			u.dropLab(ctx, actor.UserID, deployID)
			if errors.Is(err, assistantRunRepo.ErrActive) {
				return nil, assistantModel.ErrAssistantRunActive.Err()
			}
			return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to save the run").Err()
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// abandonRuns ends runs that were saved for a lab that is taken down before they started; the leader finishes them.
func (u *UseCase) abandonRuns(ctx context.Context, runs []assistantModel.Run) {
	for _, run := range runs {
		if _, err := u.runs.Close(context.WithoutCancel(ctx), run.ID, assistantModel.RunFailed, assistantModel.FailureStartRefused, u.now().UTC()); err != nil {
			log.Warn().Err(err).Msg("A run of a lab that was taken down was not ended; its window will end it")
		}
	}
}

// requireOpponentRoom refuses runs the opponents or the user have no room for (the unique index is the real guard).
func (u *UseCase) requireOpponentRoom(ctx context.Context, user, variant uuid.UUID, indexes []int) error {
	of, err := u.runs.OpponentsOfVariant(ctx, variant, maxRunsListed)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list the runs").Err()
	}
	for _, r := range of {
		if slices.Contains(indexes, r.Opponent) && r.State.Active() {
			return assistantModel.ErrAssistantRunActive.Err()
		}
	}
	n, err := u.runs.CountActiveOf(ctx, user)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to count the runs").Err()
	}
	if n+len(indexes) > u.maxActive {
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
// stop, the run's access is revoked, and the lab with its tools devices is destroyed once no other run plays in it).
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
