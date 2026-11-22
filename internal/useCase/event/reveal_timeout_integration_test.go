package event_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/delivery/repository/eventLabRevealRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/teamChallengeRepo"
	inboxUseCase "github.com/cybericebox/backend/internal/useCase/notification/inbox"
)

// revealTimeoutFixture is a frozen all_ready cohort (Blue and Red) where only Blue's lab is exactly Ready, and the
// set's team challenges wait for the barrier (readiness 1).
func revealTimeoutFixture(t *testing.T) (*standFixture, sharedSet, *eventLabRevealRepo.Repository, time.Time) {
	t.Helper()
	f, set, _, _ := prepareLifecycle(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := f.db.Pool.Exec(ctx, `UPDATE event_team_labs SET observed_revision=0 WHERE event_id=$1`, f.eventID)
	require.NoError(t, err)
	_, err = f.db.Pool.Exec(ctx, `UPDATE event_team_labs SET observed_revision=desired_revision WHERE event_exercise_id=$1 AND event_team_id=$2`, set.exerciseID, f.blueID)
	require.NoError(t, err)
	_, err = f.db.Pool.Exec(ctx, `UPDATE team_challenges SET readiness=1 WHERE event_challenge_id=ANY($1::uuid[])`, set.challenges)
	require.NoError(t, err)
	repo := eventLabRevealRepo.New(f.db.Queries)
	cohort, err := repo.Freeze(ctx, f.eventID, set.exerciseID, now)
	require.NoError(t, err)
	require.Len(t, cohort, 2)
	require.NoError(t, repo.OpenReady(ctx, f.eventID, now))
	barrier, err := repo.Barrier(ctx, f.eventID, set.exerciseID)
	require.NoError(t, err)
	require.False(t, barrier.OpenedAt.Valid, "Red is not ready: the barrier waits")
	return f, set, repo, now
}

// The reveal timeout: timeout after the barrier would have opened, it opens for the teams whose labs are Ready; a stuck
// team stays closed until its own lab is Ready, then gets the tasks alone. The barrier opened this way names the stuck
// teams once.
func TestRevealBarrierTimeoutOpensForReadyTeamsAndKeepsStuckTeamsClosed(t *testing.T) {
	f, set, repo, now := revealTimeoutFixture(t)
	ctx := context.Background()

	early, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(4*time.Minute), 5*time.Minute)
	require.NoError(t, err)
	require.Empty(t, early, "the timeout has not passed")
	barrier, err := repo.Barrier(ctx, f.eventID, set.exerciseID)
	require.NoError(t, err)
	require.False(t, barrier.OpenedAt.Valid)

	opened, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(6*time.Minute), 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, opened, 1)
	require.Equal(t, set.exerciseID, opened[0].EventExerciseID)
	require.Equal(t, []uuid.UUID{f.redID}, opened[0].StuckTeamIDs)
	require.Equal(t, []string{"Red"}, opened[0].StuckTeamNames)
	require.NotEmpty(t, opened[0].TaskName)
	barrier, err = repo.Barrier(ctx, f.eventID, set.exerciseID)
	require.NoError(t, err)
	require.True(t, barrier.OpenedAt.Valid)

	again, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(7*time.Minute), 5*time.Minute)
	require.NoError(t, err)
	require.Empty(t, again, "an opened barrier is reported once")

	challenges := teamChallengeRepo.NewWithRevealBarriers(f.db.Queries)
	_, err = challenges.PublishAvailable(ctx, f.eventID, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, f.readiness(t, f.blueID, set.challenges[0]), "the ready team gets the tasks")
	require.EqualValues(t, 1, f.readiness(t, f.redID, set.challenges[0]), "the stuck team stays closed")

	_, err = f.db.Pool.Exec(ctx, `UPDATE event_team_labs SET observed_revision=desired_revision WHERE event_exercise_id=$1 AND event_team_id=$2`, set.exerciseID, f.redID)
	require.NoError(t, err)
	_, err = challenges.PublishAvailable(ctx, f.eventID, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, f.readiness(t, f.redID, set.challenges[0]), "the stuck team opens on its own once its lab is Ready")
}

// Every team ready before the timeout: the ordinary barrier opens and no timeout alarm is raised.
func TestRevealBarrierTimeoutIgnoresAnOpenedBarrier(t *testing.T) {
	f, set, repo, now := revealTimeoutFixture(t)
	ctx := context.Background()
	_, err := f.db.Pool.Exec(ctx, `UPDATE event_team_labs SET observed_revision=desired_revision WHERE event_exercise_id=$1 AND event_team_id=$2`, set.exerciseID, f.redID)
	require.NoError(t, err)
	require.NoError(t, repo.OpenReady(ctx, f.eventID, now))
	opened, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(time.Hour), 5*time.Minute)
	require.NoError(t, err)
	require.Empty(t, opened)
}

type revealAlarms struct {
	mu   sync.Mutex
	sent []inboxUseCase.RevealTimeout
}

func (a *revealAlarms) RevealTimedOut(_ context.Context, alarm inboxUseCase.RevealTimeout) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, alarm)
	return nil
}

// The stand pass opens a timed-out barrier and raises one alarm to the organizers naming the stuck teams.
func TestRevealTimeoutPassRaisesOneAlarmToTheOrganizers(t *testing.T) {
	f, set, repo, now := revealTimeoutFixture(t)
	ctx := context.Background()
	// The barrier would have opened ten minutes ago.
	f.shiftLifecycle(t, -10*time.Minute, time.Hour)
	_, err := f.db.Pool.Exec(ctx, `UPDATE event_lab_reveal_barriers SET created_at=$2 WHERE event_exercise_id=$1`, set.exerciseID, now.Add(-10*time.Minute))
	require.NoError(t, err)
	alarms := &revealAlarms{}
	f.uc.SetRevealTimeoutInbox(alarms)
	f.uc.SetLifecycleControls(true)
	_ = f.uc.ReconcileEventStands(ctx) // the stand pass may report waits of the stuck lab; the reveal step still runs

	barrier, err := repo.Barrier(ctx, f.eventID, set.exerciseID)
	require.NoError(t, err)
	require.True(t, barrier.OpenedAt.Valid)
	alarms.mu.Lock()
	require.Len(t, alarms.sent, 1)
	alarm := alarms.sent[0]
	alarms.mu.Unlock()
	require.Equal(t, f.eventID, alarm.EventID)
	require.Equal(t, set.exerciseID, alarm.EventExerciseID)
	require.Equal(t, []string{"Red"}, alarm.StuckTeams)
	require.Equal(t, 5*time.Minute, alarm.Timeout)

	_ = f.uc.ReconcileEventStands(ctx)
	alarms.mu.Lock()
	defer alarms.mu.Unlock()
	require.Len(t, alarms.sent, 1, "one alarm per barrier")
}
