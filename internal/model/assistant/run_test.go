package assistant

import (
	"encoding/json"
	"testing"
)

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

func TestAccessIsAnOpenUnion(t *testing.T) {
	// A kind the platform has never heard of survives a round trip untouched: no switch rejects it in storage.
	in := `{"visual":{"device":"web","channel":"vnc"},"vpn":{"entry":"[Interface]"}}`
	var a Access
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	if _, ok := a["visual"]; !ok || len(a) != 2 {
		t.Fatalf("%v", a)
	}
	out, err := json.Marshal(a)
	if err != nil || string(out) != in {
		t.Fatalf("%s %v", out, err)
	}
	raw, _ := json.Marshal(VPNAccess("cfg"))
	if string(raw) != `{"vpn":{"entry":"cfg"}}` {
		t.Fatalf("%s", raw)
	}
}

func TestRunStateActive(t *testing.T) {
	for state, want := range map[RunState]bool{RunStarting: true, RunRunning: true, RunClosing: true, RunFinished: false, RunFailed: false, RunStopped: false} {
		if state.Active() != want {
			t.Fatalf("%s", state)
		}
	}
}
