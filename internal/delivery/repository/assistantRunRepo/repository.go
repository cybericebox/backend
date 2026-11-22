// Package assistantRunRepo keeps the runs the platform asks cyber-brain for (today the grader's), the brain's events of
// each run verbatim, and the judgement of the answers a run submitted.
package assistantRunRepo

import (
	"context"
	"errors"
	"github.com/cybericebox/backend/pkg/safeconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

type Queries interface {
	CreateAssistantRun(context.Context, postgres.CreateAssistantRunParams) error
	GetAssistantRun(context.Context, uuid.UUID) (postgres.AssistantRun, error)
	GetAssistantLabByRequest(context.Context, string) (postgres.AssistantRun, error)
	SetAssistantRunAnswered(context.Context, postgres.SetAssistantRunAnsweredParams) (int64, error)
	ListAssistantRunsOfVariant(context.Context, postgres.ListAssistantRunsOfVariantParams) ([]postgres.AssistantRun, error)
	ListAssistantOpponentRunsOfVariant(context.Context, postgres.ListAssistantOpponentRunsOfVariantParams) ([]postgres.AssistantRun, error)
	ListActiveAssistantRuns(context.Context, int32) ([]postgres.AssistantRun, error)
	CountActiveAssistantRunsOfActor(context.Context, uuid.UUID) (int64, error)
	CountOtherActiveAssistantRunsOnDeploy(context.Context, postgres.CountOtherActiveAssistantRunsOnDeployParams) (int64, error)
	LockAssistantRunsOfActor(context.Context, uuid.UUID) error
	ListIdleOpponentLabs(context.Context, int32) ([]postgres.ListIdleOpponentLabsRow, error)
	SetAssistantRunAccessClient(context.Context, postgres.SetAssistantRunAccessClientParams) (int64, error)
	SetAssistantRunRunning(context.Context, postgres.SetAssistantRunRunningParams) (int64, error)
	CloseAssistantRun(context.Context, postgres.CloseAssistantRunParams) (int64, error)
	FinishAssistantRun(context.Context, postgres.FinishAssistantRunParams) (int64, error)
	SetAssistantRunMeasurement(context.Context, postgres.SetAssistantRunMeasurementParams) (int64, error)
	SetAssistantRunStreamDown(context.Context, postgres.SetAssistantRunStreamDownParams) (int64, error)
	SetAssistantRunLabReady(context.Context, postgres.SetAssistantRunLabReadyParams) (int64, error)
	SetAssistantRunLabActivity(context.Context, postgres.SetAssistantRunLabActivityParams) (int64, error)
	AdvanceAssistantRunSeq(context.Context, postgres.AdvanceAssistantRunSeqParams) (int64, error)
	InsertAssistantRunEvent(context.Context, postgres.InsertAssistantRunEventParams) (int64, error)
	ListAssistantRunEvents(context.Context, postgres.ListAssistantRunEventsParams) ([]postgres.ListAssistantRunEventsRow, error)
	InsertAssistantRunAnswer(context.Context, postgres.InsertAssistantRunAnswerParams) (int64, error)
	GetAssistantRunAnswer(context.Context, postgres.GetAssistantRunAnswerParams) (postgres.GetAssistantRunAnswerRow, error)
	SetAssistantRunAnswerSent(context.Context, postgres.SetAssistantRunAnswerSentParams) (int64, error)
	ListAssistantRunAnswers(context.Context, uuid.UUID) ([]postgres.ListAssistantRunAnswersRow, error)
	ListUnsentAssistantRunAnswers(context.Context, uuid.UUID) ([]postgres.ListUnsentAssistantRunAnswersRow, error)
	ListRecentAssistantRunAnswers(context.Context, postgres.ListRecentAssistantRunAnswersParams) ([]postgres.ListRecentAssistantRunAnswersRow, error)
	GetAssistantSubjectUser(context.Context, uuid.UUID) (uuid.UUID, error)
}

type Repository struct {
	q Queries
	// uow opens the transaction CreateAll inserts in; nil (a repository over fakes): CreateAll runs its steps without one.
	uow postgres.IUnitOfWorker[Queries]
}

func New(q Queries) *Repository { return &Repository{q: q} }

// WithUnitOfWork gives the repository the transactions CreateAll needs to be atomic.
func (r *Repository) WithUnitOfWork(uow postgres.IUnitOfWorker[Queries]) *Repository {
	r.uow = uow
	return r
}

var (
	// ErrActive: the variant already has an active run (the partial unique index).
	ErrActive = errors.New("the variant already has an active run")
	// ErrLimit: the user would have more active runs than the cap.
	ErrLimit = errors.New("the user has the most active runs they may have")
)

func timeOf(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

func toRun(r postgres.AssistantRun) assistantModel.Run {
	return assistantModel.Run{
		ID: r.ID, ExerciseID: r.ExerciseID, VersionID: r.VersionID, VariantID: r.VariantID, DeployID: r.DeployID, Actor: r.ActorID,
		Role: r.Role, Opponent: int(r.Opponent), ToolsIndex: int(r.ToolsIndex), State: assistantModel.RunState(r.State), EndState: assistantModel.RunState(r.EndState), FailureReason: r.FailureReason,
		AccessClient: r.AccessClient, RequestID: r.RequestID, Answered: r.Answered, SpecID: r.SpecID, LastSeq: r.LastSeq, StreamDownSince: timeOf(r.StreamDownSince), Measurement: r.Measurement,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, StartedAt: timeOf(r.StartedAt), FinishedAt: timeOf(r.FinishedAt), LabReadyAt: timeOf(r.LabReadyAt), LabActivity: r.LabActivity, ExpiresAt: r.ExpiresAt,
	}
}

func toRuns(rows []postgres.AssistantRun) []assistantModel.Run {
	out := make([]assistantModel.Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRun(r))
	}
	return out
}

// Create stores a run that is starting. ErrActive when the variant already has an active one.
func (r *Repository) Create(ctx context.Context, run assistantModel.Run, now time.Time) error {
	return create(ctx, r.q, run, now)
}

// CreateAll stores the runs of one request (all of one actor), starting, as one step: under a lock of the actor the count of
// their active runs is checked against maxActive (ErrLimit when the runs do not fit; maxActive <= 0: no cap) and every run is
// inserted, in one transaction. So two requests at once cannot both pass the cap, and the runs of a request become visible
// together (no driver sees one of them before the others exist). ErrActive when one of them loses the race for its variant or
// opponent; then none is stored.
func (r *Repository) CreateAll(ctx context.Context, runs []assistantModel.Run, maxActive int, now time.Time) error {
	if len(runs) == 0 {
		return nil
	}
	actor := runs[0].Actor
	for _, run := range runs[1:] {
		if run.Actor != actor {
			return errors.New("the runs of one request belong to one actor")
		}
	}
	q, save := r.q, func() error { return nil }
	if r.uow != nil {
		txCtx, txQ, unit, err := r.uow.UnitOfWork(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = unit.Restore() }()
		ctx, q, save = txCtx, txQ, unit.Save
	}
	if err := q.LockAssistantRunsOfActor(ctx, actor); err != nil {
		return err
	}
	if maxActive > 0 {
		n, err := q.CountActiveAssistantRunsOfActor(ctx, actor)
		if err != nil {
			return err
		}
		if int(n)+len(runs) > maxActive {
			return ErrLimit
		}
	}
	for _, run := range runs {
		if err := create(ctx, q, run, now); err != nil {
			return err
		}
	}
	return save()
}

func create(ctx context.Context, q Queries, run assistantModel.Run, now time.Time) error {
	err := q.CreateAssistantRun(ctx, postgres.CreateAssistantRunParams{
		ID: run.ID, ExerciseID: run.ExerciseID, VersionID: run.VersionID, VariantID: run.VariantID, DeployID: run.DeployID,
		ActorID: run.Actor, Role: run.Role, Opponent: safeconv.Int32(run.Opponent), ToolsIndex: safeconv.Int32(run.ToolsIndex), RequestID: run.RequestID, SpecID: run.SpecID, Now: now, ExpiresAt: run.ExpiresAt,
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgerrcode.UniqueViolation {
		return ErrActive
	}
	return err
}

// IdleOpponentLabs are the test labs of opponent runs that still exist although none of their runs is active (a teardown that
// was cut short), at most limit of them.
func (r *Repository) IdleOpponentLabs(ctx context.Context, limit int) ([]assistantModel.RunLab, error) {
	rows, err := r.q.ListIdleOpponentLabs(ctx, safeconv.Int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.RunLab, 0, len(rows))
	for _, row := range rows {
		out = append(out, assistantModel.RunLab{DeployID: row.DeployID, Owner: row.ActorID})
	}
	return out, nil
}

// Get reads a run; found is false when there is none.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (assistantModel.Run, bool, error) {
	row, err := r.q.GetAssistantRun(ctx, id)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return assistantModel.Run{}, false, nil
		}
		return assistantModel.Run{}, false, err
	}
	return toRun(row), true, nil
}

// LabOfRequest finds the lab kept for an outbox request; found is false when there is none.
func (r *Repository) LabOfRequest(ctx context.Context, requestID string) (assistantModel.Run, bool, error) {
	row, err := r.q.GetAssistantLabByRequest(ctx, requestID)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return assistantModel.Run{}, false, nil
		}
		return assistantModel.Run{}, false, err
	}
	return toRun(row), true, nil
}

// SetAnswered records that the brain was answered the request the lab was made for.
func (r *Repository) SetAnswered(ctx context.Context, id uuid.UUID, now time.Time) error {
	_, err := r.q.SetAssistantRunAnswered(ctx, postgres.SetAssistantRunAnsweredParams{ID: id, Now: now})
	return err
}

// OfVariant lists the runs of a variant, newest first.
func (r *Repository) OfVariant(ctx context.Context, variant uuid.UUID, limit int) ([]assistantModel.Run, error) {
	rows, err := r.q.ListAssistantRunsOfVariant(ctx, postgres.ListAssistantRunsOfVariantParams{VariantID: variant, MaxRows: int32(limit)}) //nolint:gosec // a small page size
	if err != nil {
		return nil, err
	}
	return toRuns(rows), nil
}

// OpponentsOfVariant lists the opponent runs of a variant, newest first.
func (r *Repository) OpponentsOfVariant(ctx context.Context, variant uuid.UUID, limit int) ([]assistantModel.Run, error) {
	rows, err := r.q.ListAssistantOpponentRunsOfVariant(ctx, postgres.ListAssistantOpponentRunsOfVariantParams{VariantID: variant, MaxRows: int32(limit)}) //nolint:gosec // a small page size
	if err != nil {
		return nil, err
	}
	return toRuns(rows), nil
}

// Active lists the runs that are starting, running or closing.
func (r *Repository) Active(ctx context.Context, limit int) ([]assistantModel.Run, error) {
	rows, err := r.q.ListActiveAssistantRuns(ctx, int32(limit)) //nolint:gosec // a small page size
	if err != nil {
		return nil, err
	}
	return toRuns(rows), nil
}

// CountActiveOf is how many active runs the user has.
func (r *Repository) CountActiveOf(ctx context.Context, actor uuid.UUID) (int, error) {
	n, err := r.q.CountActiveAssistantRunsOfActor(ctx, actor)
	return int(n), err
}

// OthersActiveOnLab is how many active runs other than except work in the lab of a test deploy (the runs of several opponents
// share one lab).
func (r *Repository) OthersActiveOnLab(ctx context.Context, deploy, except uuid.UUID) (int, error) {
	n, err := r.q.CountOtherActiveAssistantRunsOnDeploy(ctx, postgres.CountOtherActiveAssistantRunsOnDeployParams{DeployID: deploy, ID: except})
	return int(n), err
}

// SetAccessClient records the WireGuard client made for a starting run.
func (r *Repository) SetAccessClient(ctx context.Context, id uuid.UUID, client string, now time.Time) error {
	_, err := r.q.SetAssistantRunAccessClient(ctx, postgres.SetAssistantRunAccessClientParams{ID: id, AccessClient: client, Now: now})
	return err
}

// SetRunning marks a starting run as accepted by the brain; false when it was not starting.
func (r *Repository) SetRunning(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	n, err := r.q.SetAssistantRunRunning(ctx, postgres.SetAssistantRunRunningParams{ID: id, Now: now})
	return n > 0, err
}

// Close decides how a starting or running run ends (it becomes closing); false when it was already decided.
func (r *Repository) Close(ctx context.Context, id uuid.UUID, end assistantModel.RunState, reason string, now time.Time) (bool, error) {
	n, err := r.q.CloseAssistantRun(ctx, postgres.CloseAssistantRunParams{ID: id, EndState: string(end), FailureReason: reason, Now: now})
	return n > 0, err
}

// Finish ends a closing run once its teardown is done.
func (r *Repository) Finish(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	n, err := r.q.FinishAssistantRun(ctx, postgres.FinishAssistantRunParams{ID: id, Now: now})
	return n > 0, err
}

// SetMeasurement stores the final aggregate of the run, opaque.
func (r *Repository) SetMeasurement(ctx context.Context, id uuid.UUID, measurement []byte, now time.Time) error {
	_, err := r.q.SetAssistantRunMeasurement(ctx, postgres.SetAssistantRunMeasurementParams{ID: id, Measurement: measurement, Now: now})
	return err
}

// SetLabReady records the moment the run's lab was first seen ready; a later call keeps the first.
func (r *Repository) SetLabReady(ctx context.Context, id uuid.UUID, now time.Time) error {
	_, err := r.q.SetAssistantRunLabReady(ctx, postgres.SetAssistantRunLabReadyParams{ID: id, Now: now})
	return err
}

// SetLabActivity records the activity of the run's lab (a RunLabActivity as JSON); the first decision stays.
func (r *Repository) SetLabActivity(ctx context.Context, id uuid.UUID, activity []byte, now time.Time) error {
	_, err := r.q.SetAssistantRunLabActivity(ctx, postgres.SetAssistantRunLabActivityParams{ID: id, LabActivity: activity, Now: now})
	return err
}

// SetStreamDown records since when the event stream cannot be read; the zero time clears it.
func (r *Repository) SetStreamDown(ctx context.Context, id uuid.UUID, since, now time.Time) error {
	ts := pgtype.Timestamptz{}
	if !since.IsZero() {
		ts = pgtype.Timestamptz{Time: since, Valid: true}
	}
	_, err := r.q.SetAssistantRunStreamDown(ctx, postgres.SetAssistantRunStreamDownParams{ID: id, Since: ts, Now: now})
	return err
}

// AppendEvent stores one brain frame verbatim and moves the run's last sequence number to it. A frame stored already
// (a resumed stream) is ignored; it reports whether the frame was new.
func (r *Repository) AppendEvent(ctx context.Context, id uuid.UUID, e assistantModel.RunEvent, now time.Time) (bool, error) {
	n, err := r.q.InsertAssistantRunEvent(ctx, postgres.InsertAssistantRunEventParams{RunID: id, Seq: e.Seq, Type: e.Type, Data: e.Data, Now: now})
	if err != nil {
		return false, err
	}
	if _, err = r.q.AdvanceAssistantRunSeq(ctx, postgres.AdvanceAssistantRunSeqParams{ID: id, Seq: e.Seq}); err != nil {
		return false, err
	}
	return n > 0, nil
}

// Events are the stored frames after a sequence number, in order.
func (r *Repository) Events(ctx context.Context, id uuid.UUID, after int64, limit int) ([]assistantModel.RunEvent, error) {
	rows, err := r.q.ListAssistantRunEvents(ctx, postgres.ListAssistantRunEventsParams{RunID: id, After: after, MaxRows: int32(limit)}) //nolint:gosec // a page size
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.RunEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, assistantModel.RunEvent{Seq: row.Seq, Type: row.Type, Data: row.Data, At: row.CreatedAt})
	}
	return out, nil
}

// AddAnswer stores the judgement of the answer event seq; false when that answer was judged already (the first
// judgement stays).
func (r *Repository) AddAnswer(ctx context.Context, id uuid.UUID, seq int64, subtask string, correct bool, now time.Time) (bool, error) {
	n, err := r.q.InsertAssistantRunAnswer(ctx, postgres.InsertAssistantRunAnswerParams{RunID: id, Seq: seq, SubtaskRef: subtask, Correct: correct, Now: now})
	return n > 0, err
}

// Answer reads the judgement of the answer event seq; found is false when it was not judged.
func (r *Repository) Answer(ctx context.Context, id uuid.UUID, seq int64) (assistantModel.RunAnswer, bool, error) {
	row, err := r.q.GetAssistantRunAnswer(ctx, postgres.GetAssistantRunAnswerParams{RunID: id, Seq: seq})
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return assistantModel.RunAnswer{}, false, nil
		}
		return assistantModel.RunAnswer{}, false, err
	}
	return assistantModel.RunAnswer{Seq: row.Seq, SubtaskRef: row.SubtaskRef, Correct: row.Correct, At: row.JudgedAt, VerdictSent: row.VerdictSent}, true, nil
}

// SetAnswerSent records that the brain was told the verdict of the answer.
func (r *Repository) SetAnswerSent(ctx context.Context, id uuid.UUID, seq int64) error {
	_, err := r.q.SetAssistantRunAnswerSent(ctx, postgres.SetAssistantRunAnswerSentParams{RunID: id, Seq: seq})
	return err
}

// Answers are all the judged answers of the run, in order.
func (r *Repository) Answers(ctx context.Context, id uuid.UUID) ([]assistantModel.RunAnswer, error) {
	rows, err := r.q.ListAssistantRunAnswers(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.RunAnswer, 0, len(rows))
	for _, row := range rows {
		out = append(out, assistantModel.RunAnswer{Seq: row.Seq, SubtaskRef: row.SubtaskRef, Correct: row.Correct, At: row.JudgedAt, VerdictSent: row.VerdictSent})
	}
	return out, nil
}

// Unsent are the judged answers whose verdict the brain was not told yet.
func (r *Repository) Unsent(ctx context.Context, id uuid.UUID) ([]assistantModel.RunAnswer, error) {
	rows, err := r.q.ListUnsentAssistantRunAnswers(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.RunAnswer, 0, len(rows))
	for _, row := range rows {
		out = append(out, assistantModel.RunAnswer{Seq: row.Seq, SubtaskRef: row.SubtaskRef, Correct: row.Correct, At: row.JudgedAt})
	}
	return out, nil
}

// RecentAnswers are the times of the answers of the run since a moment, oldest first, with the subtask each was for.
func (r *Repository) RecentAnswers(ctx context.Context, id uuid.UUID, since time.Time) ([]assistantModel.RunAnswer, error) {
	rows, err := r.q.ListRecentAssistantRunAnswers(ctx, postgres.ListRecentAssistantRunAnswersParams{RunID: id, Since: since})
	if err != nil {
		return nil, err
	}
	out := make([]assistantModel.RunAnswer, 0, len(rows))
	for _, row := range rows {
		out = append(out, assistantModel.RunAnswer{SubtaskRef: row.SubtaskRef, At: row.JudgedAt})
	}
	return out, nil
}

// UserOfSubject is the user a brain pseudonym belongs to; found is false for an unknown one.
func (r *Repository) UserOfSubject(ctx context.Context, subject uuid.UUID) (uuid.UUID, bool, error) {
	user, err := r.q.GetAssistantSubjectUser(ctx, subject)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return user, true, nil
}
