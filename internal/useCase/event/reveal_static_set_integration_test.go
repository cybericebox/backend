package event_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A reveal barrier waits for a ready lab of every cohort team, so an exercise without labs must never get one:
// it would never open and only adds noise to every barrier query and stand view (lt1028).
func TestPreparationFreezesNoRevealBarrierForAStaticExercise(t *testing.T) {
	f := newStandFixture(t)
	f.uc.SetLifecycleControls(true)
	static, infra := f.setOf(t, f.staticChallenge), f.setOf(t, f.infraOne)
	f.pass(t)
	require.Positive(t, f.count(t, `SELECT count(*) FROM team_challenges WHERE event_challenge_id=$1`, f.staticChallenge), "the static exercise must be prepared")
	require.Zero(t, f.count(t, `SELECT count(*) FROM event_lab_reveal_barriers WHERE event_exercise_id=$1`, static), "an exercise without labs has no barrier")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM event_lab_reveal_barriers WHERE event_exercise_id=$1`, infra), "an exercise with a lab keeps its barrier")
}

// Without infrastructure an exercise with a lab is never shown with its lab either: no barrier for it.
func TestPreparationFreezesNoRevealBarrierWithoutInfrastructure(t *testing.T) {
	f := newStandFixture(t)
	_, err := f.db.Pool.Exec(context.Background(), `UPDATE events SET infrastructure_allowed = false WHERE id = $1`, f.eventID)
	require.NoError(t, err)
	f.uc.SetLifecycleControls(true)
	f.pass(t)
	require.Zero(t, f.count(t, `SELECT count(*) FROM event_lab_reveal_barriers WHERE event_id=$1`, f.eventID))
}
