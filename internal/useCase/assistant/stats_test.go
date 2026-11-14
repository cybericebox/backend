package assistant_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
)

var (
	refA = assistantModel.TaskRef{TaskRef: "ex-a", Version: "v1", Image: "img-a"}
	refB = assistantModel.TaskRef{TaskRef: "ex-b", Version: "v1", Image: ""}
)

type fakeStats struct {
	mu         sync.Mutex
	on         map[assistantModel.TaskRef]bool
	all        []assistantModel.Aggregate
	asked      [][]assistantModel.TaskRef
	allCalls   int
	aggregates []assistantModel.Aggregate
}

func aggregate(ref assistantModel.TaskRef) assistantModel.Aggregate {
	return assistantModel.Aggregate{
		TaskRef: ref, Solves: 7, Attempts: 30, SolveRate: 0.35, Time: assistantModel.TimeStats{Median: 600, P25: 300, P75: 1200},
		HintsUsed: []assistantModel.HintBucket{{Hints: 0, Teams: 12}, {Hints: 1, Teams: 5}}, Modality: "web",
		AsOf: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC),
	}
}

func (f *fakeStats) Aggregates(_ context.Context, refs []assistantModel.TaskRef, _ time.Time) ([]assistantModel.Aggregate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if refs == nil {
		f.allCalls++
		return f.all, nil
	}
	f.asked = append(f.asked, refs)
	var out []assistantModel.Aggregate
	for _, r := range refs {
		out = append(out, aggregate(r))
	}
	return out, nil
}

func (f *fakeStats) CollectionOn(_ context.Context, refs []assistantModel.TaskRef) (map[assistantModel.TaskRef]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[assistantModel.TaskRef]bool{}
	for _, r := range refs {
		out[r] = f.on[r]
	}
	return out, nil
}

func provideDataFrame(seq int64, id string, refs ...assistantModel.TaskRef) outboxFrame {
	raw, _ := json.Marshal(map[string]any{"request_id": id, "type": "provide_data", "task_refs": refs})
	return outboxFrame{seq: seq, event: "provide_data", data: string(raw)}
}

func newStatsRig(t *testing.T, stats *fakeStats, frames ...outboxFrame) *outboxRig {
	t.Helper()
	cursor := &memCursor{}
	r := newRig(t, func(d *assistant.Dependencies) {
		d.Cursor, d.Stats = cursor, stats
		d.WatchMin, d.WatchMax, d.AnswerPause = 5*time.Millisecond, 20*time.Millisecond, time.Millisecond
	})
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := &outboxBrain{frames: frames}
	r.fake.Assistant = b.handler()
	return &outboxRig{rig: r, brain: b, cursor: cursor}
}

func TestProvideDataAnswersWithTheAggregatesOfTheRequestedTasks(t *testing.T) {
	stats := &fakeStats{on: map[assistantModel.TaskRef]bool{refA: true, refB: true}}
	o := newStatsRig(t, stats, provideDataFrame(1, "d1", refA, refB))
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	var got struct {
		Result struct{ Aggregates []assistantModel.Aggregate }
	}
	if err := json.Unmarshal([]byte(o.brain.answered("d1")[0]), &got); err != nil || len(got.Result.Aggregates) != 2 {
		t.Fatalf("answer: %v %v", o.brain.answered("d1"), err)
	}
	if got.Result.Aggregates[0].TaskRef != refA || got.Result.Aggregates[0].Solves != 7 || got.Result.Aggregates[0].Time.P75 != 1200 {
		t.Fatalf("aggregate: %+v", got.Result.Aggregates[0])
	}
}

func TestProvideDataWithCollectionOffIsCollectionDisabledNotAnEmptyResult(t *testing.T) {
	stats := &fakeStats{on: map[assistantModel.TaskRef]bool{}}
	o := newStatsRig(t, stats, provideDataFrame(1, "d1", refA, refB))
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	if code := refusalCode(t, o.brain.answered("d1")[0]); code != "collection_disabled" {
		t.Fatalf("code = %q", code)
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	if len(stats.asked) != 0 {
		t.Fatal("no aggregates are read for tasks whose collection is off")
	}
}

func TestProvideDataForSomeTasksReturnsOnlyThoseWithCollectionOn(t *testing.T) {
	stats := &fakeStats{on: map[assistantModel.TaskRef]bool{refB: true}}
	o := newStatsRig(t, stats, provideDataFrame(1, "d1", refA, refB))
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	var got struct {
		Result struct{ Aggregates []assistantModel.Aggregate }
	}
	_ = json.Unmarshal([]byte(o.brain.answered("d1")[0]), &got)
	if len(got.Result.Aggregates) != 1 || got.Result.Aggregates[0].TaskRef != refB {
		t.Fatalf("answer: %v", o.brain.answered("d1"))
	}
}

func TestProvideDataWithoutTaskRefsIsAnInvalidRequest(t *testing.T) {
	o := newStatsRig(t, &fakeStats{}, provideDataFrame(1, "d1"))
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	if code := refusalCode(t, o.brain.answered("d1")[0]); code != "invalid_request" {
		t.Fatalf("code = %q", code)
	}
}

func TestWithoutAStatsSourceCollectionIsDisabled(t *testing.T) {
	o := newOutboxRig(t, provideDataFrame(1, "d1", refA))
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	if code := refusalCode(t, o.brain.answered("d1")[0]); code != "collection_disabled" {
		t.Fatalf("code = %q", code)
	}
}

// ── PushData ────────────────────────────────────────────────────────────────

type dataBrain struct {
	mu      sync.Mutex
	bodies  []string
	status  int
	handler http.Handler
}

func (b *dataBrain) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/v1/data" || req.Method != http.MethodPost {
		http.NotFound(w, req)
		return
	}
	body, _ := io.ReadAll(req.Body)
	b.mu.Lock()
	b.bodies = append(b.bodies, string(body))
	status := b.status
	b.mu.Unlock()
	if status != 0 {
		http.Error(w, "x", status)
		return
	}
	_, _ = w.Write([]byte("{}"))
}

func newPushRig(t *testing.T, stats *fakeStats) (*rig, *dataBrain) {
	t.Helper()
	r := newRig(t, func(d *assistant.Dependencies) { d.Stats = stats })
	if _, err := r.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := &dataBrain{}
	r.fake.Assistant = b
	return r, b
}

func TestPushSendsTheAggregatesInBatches(t *testing.T) {
	stats := &fakeStats{}
	for i := range 1100 {
		stats.all = append(stats.all, aggregate(assistantModel.TaskRef{TaskRef: fmt.Sprintf("ex-%d", i), Version: "v", Image: ""}))
	}
	r, b := newPushRig(t, stats)
	if err := r.uc.PushStatistics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.bodies) != 3 {
		t.Fatalf("1100 aggregates in batches of 500: %d calls", len(b.bodies))
	}
	total := 0
	for _, body := range b.bodies {
		var in struct{ Aggregates []assistantModel.Aggregate }
		if err := json.Unmarshal([]byte(body), &in); err != nil {
			t.Fatal(err)
		}
		total += len(in.Aggregates)
	}
	if total != 1100 {
		t.Fatalf("total %d", total)
	}
}

func TestPushWithNothingToSendOrNoSourceSendsNothing(t *testing.T) {
	r, b := newPushRig(t, &fakeStats{})
	if err := r.uc.PushStatistics(context.Background()); err != nil || len(b.bodies) != 0 {
		t.Fatalf("empty: %v %d", err, len(b.bodies))
	}
	r2 := newRig(t, nil)
	if _, err := r2.uc.BootstrapBrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	b2 := &dataBrain{}
	r2.fake.Assistant = b2
	if err := r2.uc.PushStatistics(context.Background()); err != nil || len(b2.bodies) != 0 {
		t.Fatalf("no source: %v %d", err, len(b2.bodies))
	}
}

func TestPushReportsAFailingBrain(t *testing.T) {
	stats := &fakeStats{all: []assistantModel.Aggregate{aggregate(refA)}}
	r, b := newPushRig(t, stats)
	b.status = http.StatusServiceUnavailable
	if err := r.uc.PushStatistics(context.Background()); err == nil {
		t.Fatal("a refused push must be reported so the next run tries again")
	}
}

// ── only aggregates leave the daemon ────────────────────────────────────────

// allowedKeys is every key a statistics payload may carry. None names or identifies a user, a team or an address.
var allowedKeys = map[string]bool{
	"aggregates": true, "result": true,
	"task_ref": true, "version": true, "image": true, "solves": true, "attempts": true, "solve_rate": true,
	"time": true, "median": true, "p25": true, "p75": true, "hints_used": true, "hints": true, "teams": true,
	"modality": true, "as_of": true,
}

var identifying = regexp.MustCompile(`(?i)user|team_id|team_name|member|captain|participant|email|name|ip|addr|client|session|account|login|login`)

func collectKeys(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			into[k] = true
			collectKeys(child, into)
		}
	case []any:
		for _, child := range x {
			collectKeys(child, into)
		}
	}
}

func checkKeys(t *testing.T, where string, payload []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	keys := map[string]bool{}
	collectKeys(v, keys)
	var bad []string
	for k := range keys {
		if !allowedKeys[k] {
			bad = append(bad, k)
		}
		// "teams" is a count of teams in a hint bucket, the one allowed word; any other identifying key is a leak.
		if k != "teams" && k != "time" && k != "image" && identifying.MatchString(k) {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("%s carries keys that are not aggregate figures: %v", where, bad)
	}
}

func TestStatisticsPayloadsCarryOnlyAggregateFields(t *testing.T) {
	stats := &fakeStats{on: map[assistantModel.TaskRef]bool{refA: true}, all: []assistantModel.Aggregate{aggregate(refA)}}

	o := newStatsRig(t, stats, provideDataFrame(1, "d1", refA))
	o.run(t)
	eventually(t, "answered", func() bool { return o.cursor.get() == 1 })
	checkKeys(t, "ProvideData answer", []byte(o.brain.answered("d1")[0]))

	r, b := newPushRig(t, stats)
	if err := r.uc.PushStatistics(context.Background()); err != nil || len(b.bodies) != 1 {
		t.Fatalf("push: %v %d", err, len(b.bodies))
	}
	checkKeys(t, "PushData", []byte(b.bodies[0]))
	if strings.Contains(b.bodies[0], "11111111-1111") {
		t.Fatal("a user id is in the payload")
	}
}

// The model itself has no field that could hold a person, a team or an address: a new field must be added to this list
// on purpose.
func TestTheAggregateTypeHasOnlyTheDocumentedFields(t *testing.T) {
	var tags []string
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Slice || typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			if f.Anonymous {
				walk(f.Type)
				continue
			}
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			tags = append(tags, tag)
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(assistantModel.Aggregate{}))
	for _, tag := range tags {
		if !allowedKeys[tag] || tag == "aggregates" || tag == "result" {
			t.Errorf("the aggregate has the field %q", tag)
		}
	}
	if len(tags) != 15 {
		t.Errorf("fields: %v", tags)
	}
}
