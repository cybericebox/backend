package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/repository/assistantRunRepo"
	"github.com/cybericebox/backend/internal/model"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
	labActivityUseCase "github.com/cybericebox/backend/internal/useCase/labActivity"
)

const (
	// DefaultRunMaxDuration is the longest a run lasts when the deployment says nothing (BRAIN_RUN_MAX_DURATION).
	DefaultRunMaxDuration = 30 * time.Minute
	// DefaultRunGrace is how long an unreachable brain is waited for before the run fails (BRAIN_RUN_GRACE).
	DefaultRunGrace = 60 * time.Second
	// DefaultMaxActiveRuns is how many runs one user may have active at once; every run holds a test lab, which has its
	// own limit as well.
	DefaultMaxActiveRuns = 2
	// maxRunsListed is how many runs of a variant are listed.
	maxRunsListed = 20
)

type (
	// RunStore keeps the runs, their events and the judged answers (assistantRunRepo.Repository).
	RunStore interface {
		Create(ctx context.Context, run assistantModel.Run, now time.Time) error
		Get(ctx context.Context, id uuid.UUID) (assistantModel.Run, bool, error)
		LabOfRequest(ctx context.Context, requestID string) (assistantModel.Run, bool, error)
		SetAnswered(ctx context.Context, id uuid.UUID, now time.Time) error
		OfVariant(ctx context.Context, variant uuid.UUID, limit int) ([]assistantModel.Run, error)
		OpponentsOfVariant(ctx context.Context, variant uuid.UUID, limit int) ([]assistantModel.Run, error)
		Active(ctx context.Context, limit int) ([]assistantModel.Run, error)
		CountActiveOf(ctx context.Context, actor uuid.UUID) (int, error)
		// OthersActiveOnLab is how many active runs other than except work in the lab of a test deploy.
		OthersActiveOnLab(ctx context.Context, deploy, except uuid.UUID) (int, error)
		SetAccessClient(ctx context.Context, id uuid.UUID, client string, now time.Time) error
		SetRunning(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
		Close(ctx context.Context, id uuid.UUID, end assistantModel.RunState, reason string, now time.Time) (bool, error)
		Finish(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
		SetMeasurement(ctx context.Context, id uuid.UUID, measurement []byte, now time.Time) error
		SetStreamDown(ctx context.Context, id uuid.UUID, since, now time.Time) error
		// SetLabReady records the first moment the run's lab was seen ready (a later call keeps the first).
		SetLabReady(ctx context.Context, id uuid.UUID, now time.Time) error
		// SetLabActivity records the activity of the run's lab as JSON (the first decision stays).
		SetLabActivity(ctx context.Context, id uuid.UUID, activity []byte, now time.Time) error
		AppendEvent(ctx context.Context, id uuid.UUID, e assistantModel.RunEvent, now time.Time) (bool, error)
		Events(ctx context.Context, id uuid.UUID, after int64, limit int) ([]assistantModel.RunEvent, error)
		AddAnswer(ctx context.Context, id uuid.UUID, seq int64, subtask string, correct bool, now time.Time) (bool, error)
		Answer(ctx context.Context, id uuid.UUID, seq int64) (assistantModel.RunAnswer, bool, error)
		SetAnswerSent(ctx context.Context, id uuid.UUID, seq int64) error
		Answers(ctx context.Context, id uuid.UUID) ([]assistantModel.RunAnswer, error)
		Unsent(ctx context.Context, id uuid.UUID) ([]assistantModel.RunAnswer, error)
		RecentAnswers(ctx context.Context, id uuid.UUID, since time.Time) ([]assistantModel.RunAnswer, error)
		UserOfSubject(ctx context.Context, subject uuid.UUID) (uuid.UUID, bool, error)
	}

	// LabRunner is what a run takes from the exercise use case: the test lab it works against and the checks of it. The lab
	// is deployed and destroyed through the same paths as an author's own test lab.
	LabRunner interface {
		AuthorizeTestDeploy(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) error
		DeployVariantTest(ctx context.Context, owner, versionID, variantID uuid.UUID, length time.Duration) (exerciseModel.DeployHandle, error)
		DeployVariantTestWithTools(ctx context.Context, owner, versionID, variantID uuid.UUID, length time.Duration, places []exerciseModel.ToolsPlacement) (exerciseModel.DeployHandle, error)
		DestroyDeployTest(ctx context.Context, owner, deployID uuid.UUID) error
		TestLabState(ctx context.Context, owner, deployID uuid.UUID) (exerciseUseCase.LabState, error)
		MintLabClient(ctx context.Context, owner, deployID uuid.UUID, client string) (string, error)
		RevokeLabClient(ctx context.Context, owner, deployID uuid.UUID, client string) error
		JudgeAnswer(ctx context.Context, owner, deployID uuid.UUID, subtaskRef, value string) (bool, error)
		RunTarget(ctx context.Context, versionID, variantID uuid.UUID, want time.Duration) (exerciseUseCase.RunTarget, error)
		RunMaterials(ctx context.Context, owner, deployID uuid.UUID) (exerciseUseCase.Materials, error)
		RunStateTasks(ctx context.Context, owner, deployID uuid.UUID) ([]exerciseUseCase.StateSubtask, error)
		ControlAddress(ctx context.Context, owner, deployID uuid.UUID, index int) (string, error)
		RunSolvedSubtasks(ctx context.Context, owner, deployID uuid.UUID) (map[string]bool, error)
		// RunLabRef is the lab group and the lab of the run's test lab, as the laboratory names them.
		RunLabRef(ctx context.Context, owner, deployID uuid.UUID) (group, lab string, err error)
	}

	// LabActivity is the laboratory's activity reports (nil: not reported). Refresh pulls a group now; OfLab is one lab as one VPN
	// client saw it, nil while nothing is reported.
	LabActivity interface {
		Refresh(ctx context.Context, group string) error
		OfLab(ctx context.Context, group, lab, client string) (*labActivityUseCase.LabFigures, error)
	}

	// RunView is a run with the answers it submitted and how they were judged.
	RunView struct {
		Run     assistantModel.Run
		Answers []assistantModel.RunAnswer
	}
)

func (u *UseCase) runsReady() error {
	if !u.Enabled() || u.runs == nil || u.labs == nil {
		return assistantModel.ErrAssistantUnavailable.Err()
	}
	return nil
}

// StartGraderRun starts a grader run on a variant: the variant's test lab is deployed as the actor (the same path, limits
// and authorization as their own test deploy), and the leader then starts the run at the brain once the lab is ready. The
// run is returned as it is, still starting. A variant has at most one active run, a user a few. A variant without devices
// has no lab (its test deploy is only a row with the pinned answers): the run gets materials and answers only.
func (u *UseCase) StartGraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) (assistantModel.Run, error) {
	if err := u.runsReady(); err != nil {
		return assistantModel.Run{}, err
	}
	if err := u.labs.AuthorizeTestDeploy(ctx, actor, exerciseID, versionID, variantID); err != nil {
		return assistantModel.Run{}, err
	}
	target, err := u.labs.RunTarget(ctx, versionID, variantID, u.runMax)
	if err != nil {
		return assistantModel.Run{}, err
	}
	if target.ExerciseID != exerciseID {
		return assistantModel.Run{}, exerciseModel.ErrExerciseVersionNotFound.Err()
	}
	if err = u.requireRunRoom(ctx, actor.UserID, variantID); err != nil {
		return assistantModel.Run{}, err
	}
	handle, err := u.labs.DeployVariantTest(ctx, actor.UserID, versionID, variantID, target.Lease)
	if err != nil {
		return assistantModel.Run{}, err
	}
	deployID, err := uuid.FromString(handle.Group)
	if err != nil {
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("The test deploy has no id").Err()
	}
	now := u.now().UTC()
	run := assistantModel.Run{
		ID: uuid.Must(uuid.NewV7()), ExerciseID: exerciseID, VersionID: versionID, VariantID: variantID, DeployID: deployID,
		Actor: actor.UserID, Role: assistantModel.RoleGrader, State: assistantModel.RunStarting, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(u.runMax),
	}
	if err = u.runs.Create(ctx, run, now); err != nil {
		// The lab was made for nothing: take it down again, whatever the cause.
		u.dropLab(ctx, actor.UserID, deployID)
		if errors.Is(err, assistantRunRepo.ErrActive) {
			return assistantModel.Run{}, assistantModel.ErrAssistantRunActive.Err()
		}
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("Failed to save the run").Err()
	}
	return run, nil
}

// requireRunRoom refuses a run the variant or the user has no room for. The unique index is the real guard against two
// runs of a variant; this check only saves a lab from being made for a run that cannot start.
func (u *UseCase) requireRunRoom(ctx context.Context, user, variant uuid.UUID) error {
	of, err := u.runs.OfVariant(ctx, variant, maxRunsListed)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to list the runs").Err()
	}
	for _, r := range of {
		if r.Role == assistantModel.RoleGrader && r.State.Active() {
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

// dropLab destroys a test lab made for a run that was never saved; the failure is logged (the lab's own lease ends it
// anyway).
func (u *UseCase) dropLab(ctx context.Context, owner, deployID uuid.UUID) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := u.labs.DestroyDeployTest(cleanup, owner, deployID); err != nil {
		log.Warn().Err(err).Msg("The lab of a run that did not start was not removed; its lease will end it")
	}
}

// loadRun authorizes the actor for the variant like for its test deploy and returns the run if it belongs to that variant.
func (u *UseCase) loadRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (assistantModel.Run, error) {
	return u.loadRunOf(ctx, actor, exerciseID, versionID, variantID, 0, runID)
}

// loadRunOf is loadRun for the grader runs (opponent 0) or the runs of one opponent of the variant (its 1-based position).
func (u *UseCase) loadRunOf(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID, opponent int, runID uuid.UUID) (assistantModel.Run, error) {
	if err := u.runsReady(); err != nil {
		return assistantModel.Run{}, err
	}
	if err := u.labs.AuthorizeTestDeploy(ctx, actor, exerciseID, versionID, variantID); err != nil {
		return assistantModel.Run{}, err
	}
	run, found, err := u.runs.Get(ctx, runID)
	if err != nil {
		return assistantModel.Run{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the run").Err()
	}
	if !found || !isRunOf(run, opponent) || run.ExerciseID != exerciseID || run.VariantID != variantID || run.VersionID != versionID {
		return assistantModel.Run{}, assistantModel.ErrAssistantRunNotFound.Err()
	}
	return run, nil
}

// isRunOf says a row is a grader run (opponent 0) or a run of that opponent. A lab the brain asked for is neither.
func isRunOf(run assistantModel.Run, opponent int) bool {
	if opponent == 0 {
		return run.Role == assistantModel.RoleGrader && run.Opponent == 0
	}
	return run.Opponent == opponent && !assistantModel.IsLabRole(run.Role) && run.Role != assistantModel.RoleGrader
}

// GraderRuns lists the runs of a variant, newest first.
func (u *UseCase) GraderRuns(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID uuid.UUID) ([]assistantModel.Run, error) {
	if err := u.runsReady(); err != nil {
		return nil, err
	}
	if err := u.labs.AuthorizeTestDeploy(ctx, actor, exerciseID, versionID, variantID); err != nil {
		return nil, err
	}
	runs, err := u.runs.OfVariant(ctx, variantID, maxRunsListed)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to list the runs").Err()
	}
	out := runs[:0]
	for _, r := range runs {
		if r.Role == assistantModel.RoleGrader && r.ExerciseID == exerciseID && r.VersionID == versionID {
			out = append(out, r)
		}
	}
	return out, nil
}

// GraderRun reads one run with its judged answers.
func (u *UseCase) GraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (RunView, error) {
	run, err := u.loadRun(ctx, actor, exerciseID, versionID, variantID, runID)
	if err != nil {
		return RunView{}, err
	}
	answers, err := u.runs.Answers(ctx, runID)
	if err != nil {
		return RunView{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the answers of the run").Err()
	}
	return RunView{Run: run, Answers: answers}, nil
}

// StopGraderRun ends a run at the request of the actor: it is torn down (the brain is told to stop, the access is
// revoked, the lab is destroyed) by the leader. Stopping a run that is ending or over changes nothing.
func (u *UseCase) StopGraderRun(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (assistantModel.Run, error) {
	run, err := u.loadRun(ctx, actor, exerciseID, versionID, variantID, runID)
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

// GraderRunEvents authorizes the actor once and returns the reader of the run's stored events: the frames after a
// sequence number, and the state the run is in. The page's stream is served from this storage, never from the brain.
func (u *UseCase) GraderRunEvents(ctx context.Context, actor exerciseUseCase.Actor, exerciseID, versionID, variantID, runID uuid.UUID) (func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error), error) {
	if _, err := u.loadRun(ctx, actor, exerciseID, versionID, variantID, runID); err != nil {
		return nil, err
	}
	return u.runEventReader(runID), nil
}

// runEventReader reads the stored events of a run (the caller has authorized the actor for it).
func (u *UseCase) runEventReader(runID uuid.UUID) func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error) {
	return func(ctx context.Context, after int64, limit int) ([]assistantModel.RunEvent, assistantModel.RunState, error) {
		// The state is read first: a run that is over by then has all its events stored, so the page may stop after them.
		run, found, err := u.runs.Get(ctx, runID)
		if err != nil {
			return nil, "", model.ErrPlatform.WithError(err).WithMessage("Failed to read the run").Err()
		}
		if !found {
			return nil, "", assistantModel.ErrAssistantRunNotFound.Err()
		}
		events, err := u.runs.Events(ctx, runID, after, limit)
		if err != nil {
			return nil, "", model.ErrPlatform.WithError(err).WithMessage("Failed to read the events of the run").Err()
		}
		return events, run.State, nil
	}
}

// isBotRun says a row is a bot's: an opponent run or the lab of a tools device. Its client reaches the control gateway only.
func isBotRun(r assistantModel.Run) bool { return r.Opponent > 0 || r.Role == assistantModel.RoleTools }

// LabClients lists the WireGuard clients of the active runs of an owner, for the group policy of their test labs.
func (u *UseCase) LabClients(ctx context.Context, owner uuid.UUID) []exerciseUseCase.LabClient {
	if u.runs == nil {
		return nil
	}
	runs, err := u.runs.Active(ctx, 200)
	if err != nil {
		log.Warn().Err(err).Msg("The active runs could not be listed for a group policy")
		return nil
	}
	var out []exerciseUseCase.LabClient
	for _, r := range runs {
		if r.Actor == owner && r.AccessClient != "" {
			out = append(out, exerciseUseCase.LabClient{Name: r.AccessClient, DeployID: r.DeployID, Control: isBotRun(r), ToolsIndex: r.ToolsIndex})
		}
	}
	return out
}
