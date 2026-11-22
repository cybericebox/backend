package event_test

import (
	"context"
	"errors"
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
	makeLabReady(t, f, set, f.blueID)
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

func makeLabReady(t *testing.T, f *standFixture, set sharedSet, team uuid.UUID) {
	t.Helper()
	_, err := f.db.Pool.Exec(context.Background(), `UPDATE event_team_labs SET observed_revision=desired_revision WHERE event_exercise_id=$1 AND event_team_id=$2`, set.exerciseID, team)
	require.NoError(t, err)
}

func publishReveals(t *testing.T, f *standFixture) {
	t.Helper()
	_, err := teamChallengeRepo.NewWithRevealBarriers(f.db.Queries).PublishAvailable(context.Background(), f.eventID, false)
	require.NoError(t, err)
}

// The reveal timeout: timeout after the barrier would have opened, it opens for the teams whose labs are Ready; a stuck
// team stays closed until its own lab is Ready, then gets the tasks alone. The barrier records that its alarm is due.
func TestRevealBarrierTimeoutOpensForReadyTeamsAndKeepsStuckTeamsClosed(t *testing.T) {
	f, set, repo, now := revealTimeoutFixture(t)
	ctx := context.Background()

	n, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(4*time.Minute), 5*time.Minute)
	require.NoError(t, err)
	require.Zero(t, n, "the timeout has not passed")
	pending, err := repo.PendingTimeoutAlarms(ctx, f.eventID)
	require.NoError(t, err)
	require.Empty(t, pending)

	n, err = repo.OpenTimedOut(ctx, f.eventID, now.Add(6*time.Minute), 5*time.Minute)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	barrier, err := repo.Barrier(ctx, f.eventID, set.exerciseID)
	require.NoError(t, err)
	require.True(t, barrier.OpenedAt.Valid)
	n, err = repo.OpenTimedOut(ctx, f.eventID, now.Add(7*time.Minute), 5*time.Minute)
	require.NoError(t, err)
	require.Zero(t, n, "an opened barrier opens once")

	publishReveals(t, f)
	require.EqualValues(t, 2, f.readiness(t, f.blueID, set.challenges[0]), "the ready team gets the tasks")
	require.EqualValues(t, 1, f.readiness(t, f.redID, set.challenges[0]), "the stuck team stays closed")

	pending, err = repo.PendingTimeoutAlarms(ctx, f.eventID)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the alarm is due until it is marked sent")
	require.Equal(t, set.exerciseID, pending[0].EventExerciseID)
	require.Equal(t, []uuid.UUID{f.redID}, pending[0].StuckTeamIDs)
	require.Equal(t, []string{"Red"}, pending[0].StuckTeamNames)
	require.NotEmpty(t, pending[0].TaskName)

	makeLabReady(t, f, set, f.redID)
	publishReveals(t, f)
	require.EqualValues(t, 2, f.readiness(t, f.redID, set.challenges[0]), "the stuck team opens on its own once its lab is Ready")
	pending, err = repo.PendingTimeoutAlarms(ctx, f.eventID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Empty(t, pending[0].StuckTeamNames, "a team that got the tasks is never named")

	require.NoError(t, repo.MarkTimeoutAlarmSent(ctx, set.exerciseID, pending[0].Revision, now.Add(8*time.Minute)))
	pending, err = repo.PendingTimeoutAlarms(ctx, f.eventID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// After a timeout a team gets the tasks only when its lab passes the strict test the barrier uses, and until then the
// alarm names it: a lab that is Running and observed but not materialized (or of another definition, or with a
// binding not ready) is not Ready.
func TestRevealTimeoutUsesTheStrictReadyTestForTasksAndAlarm(t *testing.T) {
	breaks := map[string]func(f *standFixture, set sharedSet) (string, []any){
		"not materialized": func(f *standFixture, set sharedSet) (string, []any) {
			return `UPDATE event_team_labs SET materialized=false WHERE event_exercise_id=$1 AND event_team_id=$2`, []any{set.exerciseID, f.redID}
		},
		"another definition": func(f *standFixture, set sharedSet) (string, []any) {
			return `UPDATE event_team_labs SET definition_version_id=gen_random_uuid() WHERE event_exercise_id=$1 AND event_team_id=$2`, []any{set.exerciseID, f.redID}
		},
		// The binding of another task of the lab: the binding of the first task itself stays ready.
		"a binding not ready": func(f *standFixture, set sharedSet) (string, []any) {
			return `UPDATE lab_bindings b SET readiness=0 FROM event_team_labs l WHERE b.lab_id=l.id AND l.event_exercise_id=$1 AND l.event_team_id=$2 AND b.event_challenge_id=$3`, []any{set.exerciseID, f.redID, set.challenges[1]}
		},
		"bindings of an old lab generation": func(f *standFixture, set sharedSet) (string, []any) {
			return `UPDATE event_team_labs SET generation=generation+1 WHERE event_exercise_id=$1 AND event_team_id=$2`, []any{set.exerciseID, f.redID}
		},
	}
	for name, breakLab := range breaks {
		t.Run(name, func(t *testing.T) {
			f, set, repo, now := revealTimeoutFixture(t)
			ctx := context.Background()
			makeLabReady(t, f, set, f.redID)
			query, args := breakLab(f, set)
			_, err := f.db.Pool.Exec(ctx, query, args...)
			require.NoError(t, err)
			n, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(6*time.Minute), 5*time.Minute)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			publishReveals(t, f)
			require.EqualValues(t, 2, f.readiness(t, f.blueID, set.challenges[0]))
			require.EqualValues(t, 1, f.readiness(t, f.redID, set.challenges[0]), "no task before the lab is truly Ready")
			pending, err := repo.PendingTimeoutAlarms(ctx, f.eventID)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			require.Equal(t, []string{"Red"}, pending[0].StuckTeamNames, "the alarm names the team that did not get the tasks")
		})
	}
}

// Every team ready before the timeout: the ordinary barrier opens and no timeout alarm is due.
func TestRevealBarrierTimeoutIgnoresAnOpenedBarrier(t *testing.T) {
	f, set, repo, now := revealTimeoutFixture(t)
	ctx := context.Background()
	makeLabReady(t, f, set, f.redID)
	require.NoError(t, repo.OpenReady(ctx, f.eventID, now))
	n, err := repo.OpenTimedOut(ctx, f.eventID, now.Add(time.Hour), 5*time.Minute)
	require.NoError(t, err)
	require.Zero(t, n)
	pending, err := repo.PendingTimeoutAlarms(ctx, f.eventID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

type revealAlarms struct {
	mu   sync.Mutex
	fail bool
	sent []inboxUseCase.RevealTimeout
}

func (a *revealAlarms) RevealTimedOut(_ context.Context, alarm inboxUseCase.RevealTimeout) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return errors.New("the dispatcher is down")
	}
	a.sent = append(a.sent, alarm)
	return nil
}

func (a *revealAlarms) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sent)
}

// The stand pass opens a timed-out barrier and raises one alarm to the organizers naming the stuck teams. An alarm
// that fails is kept due and sent by a later pass (at least once); a sent one is never sent again.
func TestRevealTimeoutPassRaisesTheAlarmAtLeastOnceAndOnlyOnce(t *testing.T) {
	f, set, repo, now := revealTimeoutFixture(t)
	ctx := context.Background()
	// The barrier would have opened ten minutes ago.
	f.shiftLifecycle(t, -10*time.Minute, time.Hour)
	_, err := f.db.Pool.Exec(ctx, `UPDATE event_lab_reveal_barriers SET created_at=$2 WHERE event_exercise_id=$1`, set.exerciseID, now.Add(-10*time.Minute))
	require.NoError(t, err)
	alarms := &revealAlarms{fail: true}
	f.uc.SetRevealTimeoutInbox(alarms)
	f.uc.SetLifecycleControls(true)
	_ = f.uc.ReconcileEventStands(ctx) // the alarm fails: the pass reports it, the barrier is open anyway

	barrier, err := repo.Barrier(ctx, f.eventID, set.exerciseID)
	require.NoError(t, err)
	require.True(t, barrier.OpenedAt.Valid)
	require.Zero(t, alarms.count())
	pending, err := repo.PendingTimeoutAlarms(ctx, f.eventID)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed alarm stays due")

	alarms.mu.Lock()
	alarms.fail = false
	alarms.mu.Unlock()
	_ = f.uc.ReconcileEventStands(ctx)
	require.Equal(t, 1, alarms.count(), "the next pass sends it")
	alarm := alarms.sent[0]
	require.Equal(t, f.eventID, alarm.EventID)
	require.Equal(t, set.exerciseID, alarm.EventExerciseID)
	require.Equal(t, []string{"Red"}, alarm.StuckTeams)
	require.Equal(t, 5*time.Minute, alarm.Timeout)

	_ = f.uc.ReconcileEventStands(ctx)
	require.Equal(t, 1, alarms.count(), "one alarm per barrier")
}
