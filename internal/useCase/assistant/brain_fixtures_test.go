package assistant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/daemon/internal/delivery/repository/assistantStatsRepo"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
)

// The wire samples of the brain's local end-to-end runs live in <repo>/testdata/brain/wire (see its README). Every file is made
// here by the daemon's own code (the aggregate builder, the PushStatistics marshalling, the run driver), so a sample cannot drift
// from what the daemon really sends. Regenerate with:
//
//	go test ./internal/useCase/assistant -run TestBrainFixtures -update-brain-fixtures
var updateBrainFixtures = flag.Bool("update-brain-fixtures", false, "rewrite the generated files of testdata/brain")

const brainFixtures = "../../../testdata/brain"

var fixtureUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// stableIDs gives every distinct uuid of a document a fixed one, in order of first appearance.
func stableIDs(doc []byte) []byte {
	seen := map[string]string{}
	return fixtureUUID.ReplaceAllFunc(doc, func(id []byte) []byte {
		key := string(bytes.ToLower(id))
		if fixed, ok := seen[key]; ok {
			return []byte(fixed)
		}
		n := len(seen) + 1
		fixed := "00000000-0000-4000-8000-" + string(append(bytes.Repeat([]byte("0"), 10), byte('0'+n/10), byte('0'+n%10)))
		seen[key] = fixed
		return []byte(fixed)
	})
}

// checkGolden compares a JSON document (raw bytes as sent on the wire) with its file, pretty printed with the key order kept, or
// rewrites the file with -update-brain-fixtures.
func checkGolden(t *testing.T, rel string, raw []byte) {
	t.Helper()
	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, stableIDs(raw), "", "  "))
	pretty.WriteByte('\n')
	path := filepath.Join(brainFixtures, rel)
	if *updateBrainFixtures {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, pretty.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing fixture %s: run go test ./internal/useCase/assistant -run TestBrainFixtures -update-brain-fixtures", rel)
	require.Equal(t, string(want), pretty.String(), "fixture %s is out of date: run go test ./internal/useCase/assistant -run TestBrainFixtures -update-brain-fixtures", rel)
}

// ── PushData ───────────────────────────────────────────────────────────────

var (
	fxAsOf     = time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	fxEventA   = uuid.FromStringOrNil("e0000000-0000-4000-8000-00000000000a")
	fxEventB   = uuid.FromStringOrNil("e0000000-0000-4000-8000-00000000000b")
	fxWebTask  = uuid.FromStringOrNil("a1000000-0000-4000-8000-000000000001")
	fxWebVer   = uuid.FromStringOrNil("b1000000-0000-4000-8000-000000000001")
	fxStatic1  = uuid.FromStringOrNil("a1000000-0000-4000-8000-000000000002")
	fxStaticV1 = uuid.FromStringOrNil("b1000000-0000-4000-8000-000000000002")
	fxStatic2  = uuid.FromStringOrNil("a1000000-0000-4000-8000-000000000003")
	fxStaticV2 = uuid.FromStringOrNil("b1000000-0000-4000-8000-000000000003")
	fxStateTsk = uuid.FromStringOrNil("a1000000-0000-4000-8000-000000000004")
	fxStateVer = uuid.FromStringOrNil("b1000000-0000-4000-8000-000000000004")
)

// A web task whose lab has an Internet gateway, as the variants of its version say.
const fxWebVariants = `[{"topology":{"devices":[{"type":"node","image":"nginx:1.27"},{"type":"vpn_gateway"},{"type":"internet_gateway"}]}}]`

// A task whose lab has devices and no Internet gateway.
const fxStateVariants = `[{"topology":{"devices":[{"type":"node","image":"debian:12"},{"type":"vpn_gateway"}]}}]`

func f64(v float64) *float64 { return &v }
func yes() *bool             { v := true; return &v }
func no() *bool              { v := false; return &v }

// fxTeam makes one team's facts of one task. ref is the random team_ref the team has in its event (here fixed so the sample is
// stable); the instance key is the daemon's own and never leaves it.
func fxTeam(ex, ver, event uuid.UUID, n byte, ref string, subtasks, solved, wrong, hints int64, solveSeconds *float64) assistantStatsRepo.Fact {
	f := assistantStatsRepo.Fact{
		Task: assistantStatsRepo.Task{ExerciseID: ex, VersionID: ver}, Subtasks: subtasks, SolvedSubtasks: solved, Attempts: solved + wrong, Hints: hints,
		WrongSubmissions: wrong, TeamRef: ref,
		Instance: assistantStatsRepo.InstanceKey{EventID: event, TeamID: uuid.FromStringOrNil("77000000-0000-4000-8000-0000000000" + string(rune('0'+n/10)) + string(rune('0'+n%10))), EventExerciseID: ex},
	}
	if solveSeconds != nil {
		f.SolveSeconds, f.HasSolveSeconds = *solveSeconds, true
	}
	return f
}

func fxLab(f assistantStatsRepo.Fact, ready float64, hasReady bool, restarts int64, broken bool) assistantStatsRepo.Fact {
	f.Lab = &assistantStatsRepo.LabFact{ReadySeconds: ready, HasReady: hasReady, Restarts: restarts, Broken: broken}
	return f
}

// rotate is a fixed shuffle (the daemon shuffles at random; a sample must not move).
func rotate(n int, swap func(i, j int)) {
	for i := 1; i < n; i++ {
		swap(0, i)
	}
}

func pushBody(t *testing.T, aggregates []assistantModel.Aggregate) []byte {
	t.Helper()
	r, b := newPushRig(t, &fakeStats{all: aggregates})
	require.NoError(t, r.uc.PushStatistics(context.Background()))
	require.Len(t, b.bodies, 1)
	return []byte(b.bodies[0])
}

func buildAggregates(t *testing.T, facts []assistantStatsRepo.Fact, variants map[uuid.UUID][]byte, activity assistant.LabActivitySource) []assistantModel.Aggregate {
	t.Helper()
	store := &fakeEventStore{
		defaults: map[string]assistantModel.StatsMode{"events": assistantModel.StatsModeContinuous}, facts: facts, variants: variants,
	}
	got, err := assistant.EventsSource{Store: store, Activity: activity, Shuffle: rotate}.Aggregates(context.Background(), nil, fxAsOf)
	require.NoError(t, err)
	return got
}

// A dynamic web task from two events: every figure measured. Two events give the same task two sets of team_refs, and a team has
// a different team_ref in every event (the same one across the tasks of one event). The effort figures come from the laboratory's
// activity reports; one team (B1) and the team that never got a lab (B2) have none.
func TestBrainFixturesPushDataWebDynamic(t *testing.T) {
	const a1, a2, a3, b1, b2 = 1, 2, 3, 4, 5
	facts := []assistantStatsRepo.Fact{
		fxLab(fxTeam(fxWebTask, fxWebVer, fxEventA, a1, "5f0c2a7e9b1d4c3a8e6f0b2d7c91a4e3", 1, 1, 2, 1, f64(1800)), 38, true, 0, false),
		fxLab(fxTeam(fxWebTask, fxWebVer, fxEventA, a2, "c4b8e1d02f7a46c9a3d5e8f1b0627c94", 1, 1, 5, 0, f64(2700)), 41, true, 1, false),
		fxLab(fxTeam(fxWebTask, fxWebVer, fxEventA, a3, "17ad93f5c2e04b8d9a61f3c7e5b28d40", 1, 0, 9, 2, nil), 55, true, 2, true),
		fxLab(fxTeam(fxWebTask, fxWebVer, fxEventB, b1, "e92b6d04a8c15f3790bd2e64c1a7f853", 1, 1, 0, 0, f64(3300)), 36, true, 0, false),
		fxLab(fxTeam(fxWebTask, fxWebVer, fxEventB, b2, "3a7f1c85e0d249b6a8c3f5710e9d26b4", 1, 0, 3, 0, nil), 0, false, 0, true),
	}
	key := func(f assistantStatsRepo.Fact) assistantStatsRepo.InstanceKey { return f.Instance }
	activity := fixedActivity{
		key(facts[0]): {EgressUsed: yes(), VPNBytes: f64(52428800), DevicesTouched: f64(3), ConsoleMinutes: f64(12.5)},
		key(facts[1]): {EgressUsed: no(), VPNBytes: f64(8000000), DevicesTouched: f64(2), ConsoleMinutes: f64(0)},
		key(facts[2]): {EgressUsed: yes(), VPNBytes: f64(1200000), DevicesTouched: f64(1), ConsoleMinutes: f64(4)},
	}
	aggregates := buildAggregates(t, facts, map[uuid.UUID][]byte{fxWebVer: []byte(fxWebVariants)}, activity)
	require.Len(t, aggregates, 1)
	a := aggregates[0]
	assert.True(t, a.EgressEnabled)
	require.NotNil(t, a.EgressShare)
	assert.InDelta(t, 2.0/3.0, *a.EgressShare, 1e-9)
	require.NotNil(t, a.LabHealth)
	assert.Equal(t, 0.4, a.LabHealth.BrokenShare)
	refs := map[string]bool{}
	for _, r := range a.TeamRecords {
		refs[r.TeamRef] = true
	}
	assert.Len(t, refs, 5, "five distinct opaque refs, none a platform id")
	checkGolden(t, "wire/push_data_web_dynamic.json", pushBody(t, aggregates))
}

// Static answer tasks: no lab, so no lab_health and egress disabled; the laboratory reports nothing, so every effort figure and
// egress_share are null (not reported), never zero. A second aggregate that nobody solved shows the empty distributions.
func TestBrainFixturesPushDataStaticAnswers(t *testing.T) {
	facts := []assistantStatsRepo.Fact{
		fxTeam(fxStatic1, fxStaticV1, fxEventA, 1, "5f0c2a7e9b1d4c3a8e6f0b2d7c91a4e3", 2, 2, 1, 0, f64(900)),
		fxTeam(fxStatic1, fxStaticV1, fxEventA, 2, "c4b8e1d02f7a46c9a3d5e8f1b0627c94", 2, 2, 4, 1, f64(1500)),
		fxTeam(fxStatic1, fxStaticV1, fxEventA, 3, "17ad93f5c2e04b8d9a61f3c7e5b28d40", 2, 1, 7, 3, nil),
		fxTeam(fxStatic1, fxStaticV1, fxEventB, 4, "e92b6d04a8c15f3790bd2e64c1a7f853", 2, 0, 0, 0, nil),
		fxTeam(fxStatic2, fxStaticV2, fxEventA, 1, "5f0c2a7e9b1d4c3a8e6f0b2d7c91a4e3", 1, 0, 6, 2, nil),
		fxTeam(fxStatic2, fxStaticV2, fxEventA, 2, "c4b8e1d02f7a46c9a3d5e8f1b0627c94", 1, 0, 0, 0, nil),
	}
	aggregates := buildAggregates(t, facts, nil, nil)
	require.Len(t, aggregates, 2)
	for _, a := range aggregates {
		assert.Nil(t, a.EgressShare)
		assert.Nil(t, a.VPNBytesPerTeam)
		assert.Nil(t, a.LabHealth)
		assert.False(t, a.EgressEnabled)
		assert.Equal(t, "static", a.TaskKind)
	}
	checkGolden(t, "wire/push_data_static_answers.json", pushBody(t, aggregates))
}

// A task with state-checked subtasks: the time from the lab being ready to each pass. Events refuse such tasks today (the Academy
// has them), so this is the shape the Academy source will send: source "academy". The laboratory does not report egress here.
func TestBrainFixturesPushDataStateChecks(t *testing.T) {
	check := func(f assistantStatsRepo.Fact, passes ...*float64) assistantStatsRepo.Fact {
		for i, p := range passes {
			c := assistantStatsRepo.StateCheckFact{SubtaskRef: []string{"web_up", "config_ok"}[i]}
			if p != nil {
				c.Passed, c.PassSeconds = true, *p
			}
			f.StateChecks = append(f.StateChecks, c)
		}
		return f
	}
	solved := func(f assistantStatsRepo.Fact, at float64) assistantStatsRepo.Fact {
		f.SolvedSubtasks, f.SolveSeconds, f.HasSolveSeconds = 2, at, true
		return f
	}
	partly := func(f assistantStatsRepo.Fact) assistantStatsRepo.Fact { f.SolvedSubtasks = 1; return f }
	t1 := fxLab(fxTeam(fxStateTsk, fxStateVer, fxEventA, 1, "5f0c2a7e9b1d4c3a8e6f0b2d7c91a4e3", 2, 0, 0, 0, nil), 30, true, 0, false)
	t2 := fxLab(fxTeam(fxStateTsk, fxStateVer, fxEventA, 2, "c4b8e1d02f7a46c9a3d5e8f1b0627c94", 2, 0, 0, 1, nil), 44, true, 0, false)
	t3 := fxLab(fxTeam(fxStateTsk, fxStateVer, fxEventA, 3, "17ad93f5c2e04b8d9a61f3c7e5b28d40", 2, 0, 0, 0, nil), 33, true, 1, false)
	facts := []assistantStatsRepo.Fact{
		solved(check(t1, f64(240), f64(900)), 1140),
		partly(check(t2, f64(600), nil)),
		check(t3, nil, nil),
	}
	aggregates := buildAggregates(t, facts, map[uuid.UUID][]byte{fxStateVer: []byte(fxStateVariants)}, nil)
	require.Len(t, aggregates, 1)
	a := aggregates[0]
	require.Len(t, a.StateChecks, 2)
	passed := map[string]int64{}
	for _, c := range a.StateChecks {
		passed[c.SubtaskRef] = c.TeamsPassed
	}
	assert.Equal(t, map[string]int64{"web_up": 2, "config_ok": 1}, passed)
	assert.Nil(t, a.WrongSubmissionsPerTeam, "no answer is submitted for a state-only task")
	a.Source = "academy"
	checkGolden(t, "wire/push_data_state_checks.json", pushBody(t, []assistantModel.Aggregate{a}))
}

// ── lab_facts ──────────────────────────────────────────────────────────────

func TestBrainFixturesLabFacts(t *testing.T) {
	reported, err := json.Marshal(assistantModel.LabFacts{ReadyTimeS: 41.7, Restarts: 0, EgressEnabled: true, EgressUsed: no(), VPNBytes: f64(1500000), DevicesTouched: f64(2), ConsoleMinutes: f64(0)})
	require.NoError(t, err)
	checkGolden(t, "wire/lab_facts.json", reported)
	// A laboratory that keeps no activity reports (or no device_touch events): null, never zero.
	notReported, err := json.Marshal(assistantModel.LabFacts{ReadyTimeS: 41.7, Restarts: 0, EgressEnabled: true})
	require.NoError(t, err)
	checkGolden(t, "wire/lab_facts_not_reported.json", notReported)
}

// ── StartRun ───────────────────────────────────────────────────────────────

// A grader: the participant's view of a dynamic task over its own VPN client (no addr), no band, no budget.
func TestBrainFixturesStartRunGrader(t *testing.T) {
	r := newRunRig(t, nil)
	r.start(t)
	r.drive(t)
	eventually(t, "StartRun", func() bool { return len(r.brn.Started()) == 1 })
	body := []byte(r.brn.Started()[0])
	var got struct {
		Role   string                     `json:"role"`
		Access map[string]json.RawMessage `json:"access"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "grader", got.Role)
	assert.Contains(t, got.Access, "vpn")
	assert.NotContains(t, got.Access, "addr")
	checkGolden(t, "wire/start_run_grader.json", body)
}

// An opponent: the attacker of the opponent-lab task. Its tools device is on the control gateway, reached over the run's VPN at
// the one address in access.addr. It carries the manifest's band, goal and foothold, and the platform's budget.
func TestBrainFixturesStartRunOpponent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(brainFixtures, "tasks/opponent-lab/opponents.json"))
	require.NoError(t, err, "generated by the exercise tests: go test ./internal/useCase/exercise -run TestBrainFixtures -update-brain-fixtures")
	var manifest []exerciseModel.Opponent
	require.NoError(t, json.Unmarshal(raw, &manifest))
	require.Len(t, manifest, 2)
	r := opponentRig(t, manifest...)
	run := r.tryOpponent(t, 1)
	assert.Equal(t, "attack", run.Role)
	r.drive(t)
	eventually(t, "StartRun", func() bool { return len(r.brn.Started()) == 1 })
	body := []byte(r.brn.Started()[0])
	var got struct {
		Foothold string                     `json:"foothold"`
		Budget   assistantModel.RunBudget   `json:"budget"`
		Access   map[string]json.RawMessage `json:"access"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "sw1", got.Foothold)
	assert.Contains(t, got.Access, "vpn")
	assert.Contains(t, got.Access, "addr")
	assert.NotZero(t, got.Budget.Actions)
	checkGolden(t, "wire/start_run_opponent.json", body)
}
