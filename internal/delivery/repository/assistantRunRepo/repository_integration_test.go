package assistantRunRepo_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/delivery/repository/assistantRunRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	"github.com/cybericebox/backend/internal/delivery/repository/userRepo"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	userModel "github.com/cybericebox/backend/internal/model/user"
	"github.com/cybericebox/backend/internal/testhelpers"
)

func runsRepo(t *testing.T) (*assistantRunRepo.Repository, *testhelpers.TestDB) {
	t.Helper()
	db := testhelpers.SetupTestDB(t)
	uow := postgres.NewUnitOfWorker[assistantRunRepo.Queries](postgres.NewUoWFactory(db.Pool))
	return assistantRunRepo.New(db.Queries).WithUnitOfWork(uow), db
}

func seedUser(t *testing.T, db *testhelpers.TestDB, email string) uuid.UUID {
	t.Helper()
	u, err := userRepo.New(db.Queries).Create(context.Background(), userModel.NewIncompleteUser(uuid.Must(uuid.NewV7()), email, time.Now().UTC()))
	require.NoError(t, err)
	return u.ID
}

func opponentRun(actor, variant, deploy uuid.UUID, opponent, index int, now time.Time) assistantModel.Run {
	return assistantModel.Run{
		ID: uuid.Must(uuid.NewV7()), ExerciseID: uuid.Must(uuid.NewV7()), VersionID: uuid.Must(uuid.NewV7()), VariantID: variant, DeployID: deploy,
		Actor: actor, Role: "attack", Opponent: opponent, ToolsIndex: index, State: assistantModel.RunStarting, ExpiresAt: now.Add(time.Hour),
	}
}

// Requests of one user that start runs at the same moment cannot pass the cap of active runs together: the check and the insert
// of every run of one request are one transaction under a lock of the user.
func TestCreateAllHoldsTheCapUnderConcurrentRequests(t *testing.T) {
	repo, db := runsRepo(t)
	ctx := context.Background()
	actor := seedUser(t, db, "cap@example.com")
	now := time.Now().UTC()

	const requests, maxActive = 8, 2
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		limited int
	)
	start := make(chan struct{})
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			run := opponentRun(actor, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), 1, 0, now)
			err := repo.CreateAll(ctx, []assistantModel.Run{run}, maxActive, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, assistantRunRepo.ErrLimit):
				limited++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	n, err := repo.CountActiveOf(ctx, actor)
	require.NoError(t, err)
	require.Equal(t, maxActive, n, "never more active runs than the cap")
	require.Equal(t, maxActive, created)
	require.Equal(t, requests-maxActive, limited)
}

// The runs of one request are inserted together or not at all: one that loses the race for its opponent (or does not fit the cap)
// leaves none of the others behind, and the ones inserted become visible together.
func TestCreateAllInsertsTheRunsOfOneRequestTogether(t *testing.T) {
	repo, db := runsRepo(t)
	ctx := context.Background()
	actor := seedUser(t, db, "batch@example.com")
	now := time.Now().UTC()
	variant, deploy := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	a, b := opponentRun(actor, variant, deploy, 1, 0, now), opponentRun(actor, variant, deploy, 2, 1, now)
	require.NoError(t, repo.CreateAll(ctx, []assistantModel.Run{a, b}, 10, now))
	others, err := repo.OthersActiveOnLab(ctx, deploy, a.ID)
	require.NoError(t, err)
	require.Equal(t, 1, others)

	// opponent 2 of the variant is active already: the new opponent 3 is not inserted either
	c, dup := opponentRun(actor, variant, uuid.Must(uuid.NewV7()), 3, 0, now), opponentRun(actor, variant, uuid.Nil, 2, 1, now)
	dup.DeployID = c.DeployID
	err = repo.CreateAll(ctx, []assistantModel.Run{c, dup}, 10, now)
	require.ErrorIs(t, err, assistantRunRepo.ErrActive)
	_, found, err := repo.Get(ctx, c.ID)
	require.NoError(t, err)
	require.False(t, found, "nothing of a refused request is kept")

	// two more do not fit a cap of three
	d, e := opponentRun(actor, variant, uuid.Nil, 4, 0, now), opponentRun(actor, variant, uuid.Nil, 5, 1, now)
	require.ErrorIs(t, repo.CreateAll(ctx, []assistantModel.Run{d, e}, 3, now), assistantRunRepo.ErrLimit)
	n, err := repo.CountActiveOf(ctx, actor)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

// The leader finds the labs of opponent runs that are still there although none of their runs is active.
func TestIdleOpponentLabs(t *testing.T) {
	repo, db := runsRepo(t)
	ctx := context.Background()
	actor := seedUser(t, db, "idle@example.com")
	now := time.Now().UTC()

	conn, err := db.Pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	// The test deploys reference an exercise version; the foreign keys are not what is tested here.
	_, err = conn.Exec(ctx, "SET session_replication_role = replica")
	require.NoError(t, err)
	present := func(deploy uuid.UUID) {
		t.Helper()
		_, execErr := conn.Exec(ctx, `INSERT INTO exercise_test_deployments (id, group_name, lab_name, version_id, variant_id, created_by, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, deploy, "g-"+deploy.String(), "l-"+deploy.String(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), actor, now)
		require.NoError(t, execErr)
	}
	end := func(runs ...assistantModel.Run) {
		t.Helper()
		for _, r := range runs {
			_, closeErr := repo.Close(ctx, r.ID, assistantModel.RunFinished, "", now)
			require.NoError(t, closeErr)
			_, finishErr := repo.Finish(ctx, r.ID, now)
			require.NoError(t, finishErr)
		}
	}
	idle, playing, gone, grader := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, deploy := range []uuid.UUID{idle, playing, grader} {
		present(deploy)
	}
	v1, v2, v3 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	i1, i2 := opponentRun(actor, v1, idle, 1, 0, now), opponentRun(actor, v1, idle, 2, 1, now)
	p1, p2 := opponentRun(actor, v2, playing, 1, 0, now), opponentRun(actor, v2, playing, 2, 1, now)
	g1 := opponentRun(actor, v3, gone, 1, 0, now)
	gr := opponentRun(actor, v3, grader, 0, 0, now)
	gr.Role = assistantModel.RoleGrader
	for _, batch := range [][]assistantModel.Run{{i1, i2}, {p1, p2}, {g1}, {gr}} {
		require.NoError(t, repo.CreateAll(ctx, batch, 100, now))
	}
	end(i1, i2, p1, g1, gr)

	labs, err := repo.IdleOpponentLabs(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []assistantModel.RunLab{{DeployID: idle, Owner: actor}}, labs,
		"only the lab whose runs all ended and which is still there; never a lab a run still plays in, a lab that is gone, or a grader's")
}
