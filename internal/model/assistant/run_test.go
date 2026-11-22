package assistant

import "testing"

func TestGraderRunsNeverCountInStatistics(t *testing.T) {
	if !ExcludeRun(RoleGrader) {
		t.Fatal("a grader run must be left out of the participant statistics")
	}
	for _, role := range []string{"attack", "defense", "attack-defense", "something-new", ""} {
		if !ExcludeRun(role) {
			t.Fatalf("no run is a participant, %q was counted", role)
		}
	}
}

func TestRunStateActive(t *testing.T) {
	for state, want := range map[RunState]bool{RunStarting: true, RunRunning: true, RunClosing: true, RunFinished: false, RunFailed: false, RunStopped: false} {
		if state.Active() != want {
			t.Fatalf("%s", state)
		}
	}
}
