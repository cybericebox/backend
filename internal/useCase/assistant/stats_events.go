package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/assistantStatsRepo"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// ParticipantFilter says which participants refused that their data is used. Consent comes later; today nobody is
// excluded. An excluded participant drops out of every count: the team they are in is left out of the aggregates
// entirely, the solve_rate denominator included.
type ParticipantFilter interface {
	Excluded(ctx context.Context) ([]uuid.UUID, error)
}

// NoExclusion excludes nobody.
type NoExclusion struct{}

func (NoExclusion) Excluded(context.Context) ([]uuid.UUID, error) { return nil, nil }

// LabActivitySource gives the laboratory's activity reports (VPN bytes, devices touched, console minutes, egress use) of
// team-instances. It is the seam for the laboratory's activity API: today nothing implements it, so EventsSource leaves
// those figures null ("not reported yet"). An instance the laboratory has no report for is simply absent from the answer; a
// nil figure inside an Activity is not reported either.
// EgressUsed is per lab: "the lab had any Internet egress". The laboratory attributes egress to a lab only (best-effort, by source
// address), never to a device or a user, so nothing here maps it to either; in events a team has its own lab, so per lab is per team. Only figures of the instances asked for may be returned.
type LabActivitySource interface {
	Activity(ctx context.Context, instances []assistantStatsRepo.InstanceKey) (map[assistantStatsRepo.InstanceKey]assistantStatsRepo.Activity, error)
}

// EventStatsStore is what the events source reads.
type EventStatsStore interface {
	Defaults(ctx context.Context) (map[string]assistantModel.StatsMode, error)
	EnabledTasks(ctx context.Context, platformDefault assistantModel.StatsMode, exercises []uuid.UUID) ([]assistantStatsRepo.Task, error)
	TeamFacts(ctx context.Context, platformDefault assistantModel.StatsMode, asOf time.Time, excluded, exercises []uuid.UUID) ([]assistantStatsRepo.Fact, error)
	VersionVariants(ctx context.Context, versions []uuid.UUID) (map[uuid.UUID][]byte, error)
}

// EventsSource is the statistics source of the events. A task is reported from the events whose mode (their own, else the
// platform default of the source) is continuous, or final once the event has ended. Hidden teams and moderators never
// count; a participant filter (consent) leaves teams out. A grader's run is not a team: its lab is a test deploy, which
// no event owns, and the facts below are read from event teams only, so a run cannot enter them. assistantModel.ExcludeRun
// states that for every run role, and the test of this source pins that the runs' tables are never read here.
type EventsSource struct {
	Store     EventStatsStore
	Exclusion ParticipantFilter
	// Activity is the laboratory's activity reports; nil: none are reported.
	Activity LabActivitySource
	// Shuffle orders the team records of a task (rand.Shuffle when nil); tests set it to a fixed permutation.
	Shuffle func(n int, swap func(i, j int))
}

// Name is the source's key in the platform defaults.
func (EventsSource) Name() string { return assistantModel.SourceEvents }

func (s EventsSource) platformDefault(ctx context.Context) (assistantModel.StatsMode, error) {
	defaults, err := s.Store.Defaults(ctx)
	if err != nil {
		return "", err
	}
	if mode, ok := defaults[assistantModel.SourceEvents]; ok {
		return mode, nil
	}
	return assistantModel.StatsModeOff, nil
}

func exerciseIDs(refs []assistantModel.TaskRef) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, ref := range refs {
		if id, err := uuid.FromString(ref.TaskRef); err == nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// CollectionOn: for each ref, whether an event that uses that task and version has collection on.
func (s EventsSource) CollectionOn(ctx context.Context, refs []assistantModel.TaskRef) (map[assistantModel.TaskRef]bool, error) {
	out := make(map[assistantModel.TaskRef]bool, len(refs))
	ids := exerciseIDs(refs)
	if len(ids) == 0 {
		return out, nil
	}
	platformDefault, err := s.platformDefault(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.Store.EnabledTasks(ctx, platformDefault, ids)
	if err != nil {
		return nil, err
	}
	enabled := map[[2]string]bool{}
	for _, t := range tasks {
		enabled[[2]string{t.ExerciseID.String(), t.VersionID.String()}] = true
	}
	for _, ref := range refs {
		id, e1 := uuid.FromString(ref.TaskRef)
		version, e2 := uuid.FromString(ref.Version)
		out[ref] = e1 == nil && e2 == nil && enabled[[2]string{id.String(), version.String()}]
	}
	return out, nil
}

// Aggregates are the cumulative figures of the due events as of asOf. refs nil: every task; else only those.
func (s EventsSource) Aggregates(ctx context.Context, refs []assistantModel.TaskRef, asOf time.Time) ([]assistantModel.Aggregate, error) {
	platformDefault, err := s.platformDefault(ctx)
	if err != nil {
		return nil, err
	}
	var excluded []uuid.UUID
	if s.Exclusion != nil {
		if excluded, err = s.Exclusion.Excluded(ctx); err != nil {
			return nil, err
		}
	}
	var ids []uuid.UUID
	if refs != nil {
		if ids = exerciseIDs(refs); len(ids) == 0 {
			return nil, nil
		}
	}
	facts, err := s.Store.TeamFacts(ctx, platformDefault, asOf, excluded, ids)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return nil, nil
	}
	versions := make([]uuid.UUID, 0, len(facts))
	seen := map[uuid.UUID]bool{}
	for _, f := range facts {
		if !seen[f.VersionID] {
			seen[f.VersionID] = true
			versions = append(versions, f.VersionID)
		}
	}
	variants, err := s.Store.VersionVariants(ctx, versions)
	if err != nil {
		return nil, err
	}
	if s.Activity != nil {
		keys := make([]assistantStatsRepo.InstanceKey, 0, len(facts))
		for _, f := range facts {
			keys = append(keys, f.Instance)
		}
		reports, activityErr := s.Activity.Activity(ctx, keys)
		if activityErr != nil {
			return nil, activityErr
		}
		for i := range facts {
			if a, ok := reports[facts[i].Instance]; ok {
				facts[i].Activity = &a
			}
		}
	}
	all := BuildAggregatesWith(facts, variants, asOf, s.Shuffle)
	for i := range all {
		all[i].Source = assistantModel.SourceEvents
	}
	if refs == nil {
		return all, nil
	}
	want := make(map[assistantModel.TaskRef]bool, len(refs))
	for _, ref := range refs {
		want[ref] = true
	}
	out := all[:0]
	for _, a := range all {
		if want[a.TaskRef] {
			out = append(out, a)
		}
	}
	return out, nil
}

// BuildAggregates turns the per-team facts into one aggregate per (task, version, image), shuffling the team records.
func BuildAggregates(facts []assistantStatsRepo.Fact, variants map[uuid.UUID][]byte, asOf time.Time) []assistantModel.Aggregate {
	return BuildAggregatesWith(facts, variants, asOf, nil)
}

// BuildAggregatesWith is BuildAggregates with the shuffle of the team records given (nil: a random one). solves are the
// teams that solved every subtask; solve_rate is solves over the teams that engaged with the task (teams_engaged); the time
// figures are over the solving teams; hints_used is how many teams used how many hints. The wrong submissions are over the
// solving teams (before their solve) and, apart, over the teams that did not solve. The lab figures are over the teams whose
// task has a lab; the state checks over the teams that have the subtask. The effort figures and egress_share are over the
// teams the laboratory reported, and stay null when none were. Nothing of a team stays in the result except its record: no
// id, no key, only the random team_ref of the team in its event; the records are shuffled.
func BuildAggregatesWith(facts []assistantStatsRepo.Fact, variants map[uuid.UUID][]byte, asOf time.Time, shuffle func(n int, swap func(i, j int))) []assistantModel.Aggregate {
	if shuffle == nil {
		shuffle = rand.Shuffle
	}
	type acc struct {
		ref      assistantModel.TaskRef
		kind     string
		egress   bool
		tried    int64
		solves   int64
		attempts int64
		times    []float64
		hints    map[int64]int64
		facts    []assistantStatsRepo.Fact
	}
	groups := map[assistantModel.TaskRef]*acc{}
	for _, f := range facts {
		image, kind := imageAndKind(variants[f.VersionID])
		ref := assistantModel.TaskRef{TaskRef: f.ExerciseID.String(), Version: f.VersionID.String(), Image: image}
		g := groups[ref]
		if g == nil {
			g = &acc{ref: ref, kind: kind, egress: egressEnabled(variants[f.VersionID]), hints: map[int64]int64{}}
			groups[ref] = g
		}
		g.tried++
		g.attempts += f.Attempts
		g.hints[f.Hints]++
		g.facts = append(g.facts, f)
		if solvedAll(f) {
			g.solves++
			if f.HasSolveSeconds {
				g.times = append(g.times, f.SolveSeconds)
			}
		}
	}
	out := make([]assistantModel.Aggregate, 0, len(groups))
	for _, g := range groups {
		a := assistantModel.Aggregate{
			TaskRef: g.ref, Solves: g.solves, Attempts: g.attempts, TaskKind: g.kind, AsOf: asOf,
			Time:          assistantModel.TimeStats{Median: percentile(g.times, 0.5), P25: percentile(g.times, 0.25), P75: percentile(g.times, 0.75)},
			TeamsEngaged:  g.tried,
			EgressEnabled: g.egress,
		}
		if g.tried > 0 {
			a.SolveRate = float64(g.solves) / float64(g.tried)
		}
		counts := make([]int64, 0, len(g.hints))
		for n := range g.hints {
			counts = append(counts, n)
		}
		slices.Sort(counts)
		for _, n := range counts {
			a.HintsUsed = append(a.HintsUsed, assistantModel.HintBucket{Hints: int(n), Teams: g.hints[n]})
		}
		fillLabSignals(&a, g.facts)
		a.TeamRecords = teamRecords(g.facts, shuffle)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TaskRef.TaskRef != out[j].TaskRef.TaskRef {
			return out[i].TaskRef.TaskRef < out[j].TaskRef.TaskRef
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Image < out[j].Image
	})
	return out
}

func solvedAll(f assistantStatsRepo.Fact) bool {
	return f.Subtasks > 0 && f.SolvedSubtasks >= f.Subtasks
}

// distribution is the quartiles of values; nil for no values (there is nothing to say, not zero).
func distribution(values []float64) *assistantModel.Distribution {
	if len(values) == 0 {
		return nil
	}
	return &assistantModel.Distribution{P25: percentile(values, 0.25), Median: percentile(values, 0.5), P75: percentile(values, 0.75)}
}

// fillLabSignals sets the wrong-submission, state-check, lab-health and activity figures of one task from its teams' facts.
func fillLabSignals(a *assistantModel.Aggregate, facts []assistantStatsRepo.Fact) {
	var wrongSolvers, wrongOthers, ready []float64
	var restarts float64
	var labTeams, broken int64
	stateOnly := len(facts) > 0
	type stateAcc struct {
		passed int64
		times  []float64
	}
	states := map[string]*stateAcc{}
	var stateOrder []string
	var egressReported, egressUsed int64
	var vpn, devices, console []float64
	for _, f := range facts {
		if solvedAll(f) {
			wrongSolvers = append(wrongSolvers, float64(f.WrongSubmissions))
		} else {
			wrongOthers = append(wrongOthers, float64(f.WrongSubmissions))
		}
		if len(f.StateChecks) == 0 || int64(len(f.StateChecks)) < f.Subtasks {
			stateOnly = false
		}
		for _, c := range f.StateChecks {
			s := states[c.SubtaskRef]
			if s == nil {
				s = &stateAcc{}
				states[c.SubtaskRef] = s
				stateOrder = append(stateOrder, c.SubtaskRef)
			}
			if c.Passed {
				s.passed++
				s.times = append(s.times, c.PassSeconds)
			}
		}
		if f.Lab != nil {
			labTeams++
			restarts += float64(f.Lab.Restarts)
			if f.Lab.Broken {
				broken++
			}
			if f.Lab.HasReady {
				ready = append(ready, f.Lab.ReadySeconds)
			}
		}
		if act := f.Activity; act != nil {
			if act.EgressUsed != nil {
				egressReported++
				if *act.EgressUsed {
					egressUsed++
				}
			}
			if act.VPNBytes != nil {
				vpn = append(vpn, *act.VPNBytes)
			}
			if act.DevicesTouched != nil {
				devices = append(devices, *act.DevicesTouched)
			}
			if act.ConsoleMinutes != nil {
				console = append(console, *act.ConsoleMinutes)
			}
		}
	}
	if !stateOnly {
		a.WrongSubmissionsPerTeam = distribution(wrongSolvers)
		a.WrongSubmissionsPerNonsolver = distribution(wrongOthers)
	}
	slices.Sort(stateOrder)
	for _, ref := range stateOrder {
		s := states[ref]
		sc := assistantModel.StateCheck{SubtaskRef: ref, TeamsPassed: s.passed}
		if d := distribution(s.times); d != nil {
			sc.TimeToPassS = *d
		}
		a.StateChecks = append(a.StateChecks, sc)
	}
	if labTeams > 0 {
		a.LabHealth = &assistantModel.LabHealth{
			ReadyTimeS: distribution(ready), RestartsPerTeam: restarts / float64(labTeams), BrokenShare: float64(broken) / float64(labTeams),
		}
	}
	if egressReported > 0 {
		share := float64(egressUsed) / float64(egressReported)
		a.EgressShare = &share
	}
	a.VPNBytesPerTeam, a.DevicesTouchedPerTeam, a.ConsoleMinutesPerTeam = distribution(vpn), distribution(devices), distribution(console)
}

// teamRecords are the anonymous per-team rows of a task, in a shuffled order.
func teamRecords(facts []assistantStatsRepo.Fact, shuffle func(n int, swap func(i, j int))) []assistantModel.TeamRecord {
	records := make([]assistantModel.TeamRecord, 0, len(facts))
	for _, f := range facts {
		r := assistantModel.TeamRecord{
			TeamRef: f.TeamRef, Solved: solvedAll(f), WrongSubmissions: f.WrongSubmissions, HintsUsed: f.Hints,
			StateChecks: []assistantModel.TeamStateCheck{},
		}
		if r.Solved && f.HasSolveSeconds {
			t := f.SolveSeconds
			r.SolveTimeS = &t
		}
		for _, c := range f.StateChecks {
			sc := assistantModel.TeamStateCheck{SubtaskRef: c.SubtaskRef}
			if c.Passed {
				t := c.PassSeconds
				sc.TimeToPassS = &t
			}
			r.StateChecks = append(r.StateChecks, sc)
		}
		if act := f.Activity; act != nil {
			r.VPNBytes, r.DevicesTouched, r.ConsoleMinutes, r.EgressUsed = act.VPNBytes, act.DevicesTouched, act.ConsoleMinutes, act.EgressUsed
		}
		records = append(records, r)
	}
	shuffle(len(records), func(i, j int) { records[i], records[j] = records[j], records[i] })
	return records
}

// percentile is the continuous percentile (linear between the two nearest values, like percentile_cont); 0 for no data.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	pos := p * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	return sorted[lower] + (sorted[upper]-sorted[lower])*(pos-float64(lower))
}

// imageAndKind reads the variants of a version: the fingerprint of its device images (sha256 over the sorted,
// distinct image references; empty when it has none) and its kind (static without an image, dynamic with one).
func imageAndKind(variants []byte) (image, kind string) {
	var list []struct {
		Topology struct {
			Devices []struct {
				Image string `json:"image"`
			} `json:"devices"`
		} `json:"topology"`
	}
	if json.Unmarshal(variants, &list) != nil {
		return "", "static"
	}
	set := map[string]bool{}
	for _, v := range list {
		for _, d := range v.Topology.Devices {
			if ref := strings.TrimSpace(d.Image); ref != "" {
				set[ref] = true
			}
		}
	}
	if len(set) == 0 {
		return "", "static"
	}
	refs := make([]string, 0, len(set))
	for ref := range set {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	sum := sha256.Sum256([]byte(strings.Join(refs, "\n")))
	return hex.EncodeToString(sum[:]), "dynamic"
}

// egressEnabled says whether the topology has an Internet gateway in any variant (the task's lab can reach the Internet).
func egressEnabled(variants []byte) bool {
	var list []struct {
		Topology struct {
			Devices []struct {
				Type string `json:"type"`
			} `json:"devices"`
		} `json:"topology"`
	}
	if json.Unmarshal(variants, &list) != nil {
		return false
	}
	for _, v := range list {
		for _, d := range v.Topology.Devices {
			if exerciseModel.DeviceType(d.Type) == exerciseModel.DeviceTypeInternetGateway {
				return true
			}
		}
	}
	return false
}
