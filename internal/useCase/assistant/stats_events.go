package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/delivery/repository/assistantStatsRepo"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
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
	all := BuildAggregates(facts, variants, asOf)
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

// BuildAggregates turns the per-team facts into one aggregate per (task, version, image). solves are the teams that
// solved every subtask; solve_rate is solves over the teams that engaged with the task; the time figures are over the
// solving teams; hints_used is how many teams used how many hints. Nothing of a team stays in the result.
func BuildAggregates(facts []assistantStatsRepo.Fact, variants map[uuid.UUID][]byte, asOf time.Time) []assistantModel.Aggregate {
	type acc struct {
		ref      assistantModel.TaskRef
		kind     string
		tried    int64
		solves   int64
		attempts int64
		times    []float64
		hints    map[int64]int64
	}
	groups := map[assistantModel.TaskRef]*acc{}
	for _, f := range facts {
		image, kind := imageAndKind(variants[f.VersionID])
		ref := assistantModel.TaskRef{TaskRef: f.ExerciseID.String(), Version: f.VersionID.String(), Image: image}
		g := groups[ref]
		if g == nil {
			g = &acc{ref: ref, kind: kind, hints: map[int64]int64{}}
			groups[ref] = g
		}
		g.tried++
		g.attempts += f.Attempts
		g.hints[f.Hints]++
		if f.Subtasks > 0 && f.SolvedSubtasks >= f.Subtasks {
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
			Time: assistantModel.TimeStats{Median: percentile(g.times, 0.5), P25: percentile(g.times, 0.25), P75: percentile(g.times, 0.75)},
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
