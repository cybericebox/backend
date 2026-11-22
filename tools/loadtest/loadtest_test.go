package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	eventConfigModel "github.com/cybericebox/backend/internal/model/eventConfig"
	pkgerr "github.com/cybericebox/backend/pkg/err"
)

func TestPercentileNearestRank(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[float64]time.Duration{50: 50 * time.Millisecond, 95: 95 * time.Millisecond, 99: 99 * time.Millisecond, 100: 100 * time.Millisecond} {
		if got := percentile(d, p); got != want {
			t.Errorf("p%.0f = %s, want %s", p, got, want)
		}
	}
	if percentile(nil, 95) != 0 {
		t.Error("an empty sample has no percentile")
	}
}

func TestFlagsAreValidated(t *testing.T) {
	base := []string{"--domain", "stand.test", "--event-slug", "lt", "--creds", "c.json"}
	if _, err := parseFlags(base); err == nil || !strings.Contains(err.Error(), "confirm-domain") {
		t.Fatalf("a run without --confirm-domain must be refused, got %v", err)
	}
	if _, err := parseFlags(append(base, "--confirm-domain", "other.test")); err == nil {
		t.Fatal("a wrong --confirm-domain must be refused")
	}
	cfg, err := parseFlags(append(base, "--confirm-domain", "stand.test", "--members", "6", "--api-url", "http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.membersMin != 6 || cfg.apiHost != "api.stand.test" || cfg.eventOrigin() != "https://lt.stand.test" || cfg.idOrigin() != "https://id.stand.test" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err = parseFlags(append(base, "--dry-run", "--stage", "bogus")); err == nil {
		t.Fatal("an unknown stage must be refused")
	}
}

func TestPlanIsDeterministicAndBounded(t *testing.T) {
	cfg := config{teams: 14, membersMin: 10, membersMax: 15, seed: 7}
	accounts := make([]account, 300)
	for i := range accounts {
		accounts[i] = account{N: i + 1, Email: fmt.Sprintf("u%d", i)}
	}
	size := func(seed uint64) []int {
		cfg.seed = seed
		teams, err := plan(cfg, accounts, &runner{cfg: cfg})
		if err != nil {
			t.Fatal(err)
		}
		var out []int
		for _, tm := range teams {
			out = append(out, len(tm.members))
			if len(tm.members) < 10 || len(tm.members) > 15 || !tm.members[0].captain || tm.members[1].captain {
				t.Fatalf("team %s: %d members", tm.name, len(tm.members))
			}
		}
		return out
	}
	if a, b := size(7), size(7); fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("the same seed must give the same layout")
	}
	if fmt.Sprint(size(7)) == fmt.Sprint(size(8)) {
		t.Fatal("another seed should change the layout")
	}
	cfg.membersMin, cfg.membersMax, cfg.accountOffset = 15, 15, 100
	if _, err := plan(cfg, accounts, &runner{cfg: cfg}); err == nil {
		t.Fatal("too few accounts must be reported")
	}
}

// fakeStand answers like the platform for the endpoints the tool uses.
type fakeStand struct {
	mu        sync.Mutex
	teams     map[string]string // cookie -> team name
	codes     map[string]string // join code -> team name
	byName    map[string]string
	labReady  time.Time
	solved    map[string]bool // team|challenge
	startTime time.Time
	t         *testing.T
	badOrigin int
	// resultsDenied, when set, is the platform code the results endpoints answer with 403 (results hidden, participants only).
	resultsDenied int
}

const (
	fakeEvent = "11111111-1111-1111-1111-111111111111"
	chStatic  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	chLab     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func (f *fakeStand) handler() http.Handler {
	reply := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Status": map[string]any{"Code": 0, "Message": "ok"}, "Data": data})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		cookie, _ := r.Cookie("__Host-session")
		who := ""
		if cookie != nil {
			who = cookie.Value
		}
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/events") && !strings.HasPrefix(r.Header.Get("Origin"), "https://lt.stand.test") {
			f.badOrigin++
		}
		if who == "" && !strings.Contains(path, "live-screen") && !strings.HasSuffix(path, "/results") {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		team := f.teams[who]
		if f.resultsDenied != 0 && (strings.HasSuffix(path, "/results") || strings.HasSuffix(path, "/results/changes")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"Status": map[string]any{"Code": f.resultsDenied, "Message": "denied"}})
			return
		}
		switch {
		case path == "/api/auth/me":
			reply(w, map[string]any{})
		case path == "/api/events/self/info":
			reply(w, map[string]any{"EventID": fakeEvent, "StartTime": f.startTime.Format(time.RFC3339Nano), "FinishTime": f.startTime.Add(time.Hour).Format(time.RFC3339Nano)})
		case path == "/api/events/self/join":
			reply(w, map[string]any{})
		case path == "/api/events/self/join/info":
			reply(w, map[string]any{})
		case path == "/api/events/"+fakeEvent+"/teams" && r.Method == "POST":
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.teams[who] = body.Name
			f.codes["code-"+body.Name] = body.Name
			reply(w, nil)
		case path == "/api/events/"+fakeEvent+"/teams/mine":
			reply(w, map[string]any{"JoinCode": "code-" + team})
		case path == "/api/events/"+fakeEvent+"/teams/join":
			var body struct{ JoinCode string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, ok := f.codes[body.JoinCode]
			if !ok {
				http.Error(w, "bad code", http.StatusBadRequest)
				return
			}
			f.teams[who] = name
			reply(w, nil)
		case strings.HasSuffix(path, "/teams/mine/form"):
			reply(w, nil)
		case strings.HasSuffix(path, "/teams/challenges/mine"):
			solved := func(id string) *string {
				if f.solved[team+"|"+id] {
					s := "2026-01-01T00:00:00Z"
					return &s
				}
				return nil
			}
			reply(w, map[string]any{"Challenges": []map[string]any{
				{"EventChallengeID": chStatic, "Infrastructure": false, "SolvedAt": solved(chStatic), "Snapshot": map[string]string{"name": "Static task"}},
				{"EventChallengeID": chLab, "Infrastructure": true, "SolvedAt": solved(chLab), "Snapshot": map[string]string{"name": "Lab task"}},
			}})
		case strings.HasSuffix(path, "/lab") && strings.Contains(path, chLab):
			phase := "Provisioning"
			if time.Now().After(f.labReady) {
				phase = "Ready"
			}
			reply(w, map[string]any{"Phase": phase, "Ready": phase == "Ready"})
		case strings.HasSuffix(path, "/lab/link"):
			reply(w, map[string]any{"URL": "https://x"})
		case strings.HasSuffix(path, "/labs/stand"):
			reply(w, map[string]any{"Status": "ready", "VPNReady": time.Now().After(f.labReady)})
		case strings.HasSuffix(path, "/labs/vpn"):
			reply(w, map[string]any{"Config": "[Interface]\nPrivateKey = x"})
		case strings.HasSuffix(path, "/open"), strings.HasSuffix(path, "/solves"):
			reply(w, map[string]any{})
		case strings.HasSuffix(path, "/submit"):
			if r.Header.Get("Idempotency-Key") == "" {
				http.Error(w, "no key", http.StatusBadRequest)
				return
			}
			var body struct{ Answer string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := strings.Split(path, "/")[6]
			correct := body.Answer == "flag{real-"+id+"}"
			if correct {
				f.solved[team+"|"+id] = true
			}
			reply(w, map[string]any{"Correct": correct})
		case strings.HasSuffix(path, "/results"):
			reply(w, map[string]any{"Revision": 5})
		case strings.HasSuffix(path, "/results/changes"):
			reply(w, map[string]any{"Revision": 5, "Changes": []any{}})
		case strings.Contains(path, "live-screen"):
			reply(w, map[string]any{})
		default:
			f.t.Errorf("unexpected request %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	})
}

func newFakeStand(t *testing.T) *fakeStand {
	return &fakeStand{t: t, teams: map[string]string{}, codes: map[string]string{}, solved: map[string]bool{},
		labReady: time.Now().Add(400 * time.Millisecond), startTime: time.Now().Add(300 * time.Millisecond)}
}

// runAgainst runs the whole scenario against the fake stand and returns the JSON report and the text output.
func runAgainst(t *testing.T, stand *fakeStand) (Report, string) {
	t.Helper()
	server := httptest.NewServer(stand.handler())
	defer server.Close()

	dir := t.TempDir()
	var accounts []account
	for i := 1; i <= 6; i++ {
		accounts = append(accounts, account{N: i, Email: fmt.Sprintf("lt-x-%03d@loadtest.invalid", i), Cookie: fmt.Sprintf("cookie-%d", i)})
	}
	credsRaw, _ := json.Marshal(map[string]any{"accounts": accounts})
	creds := filepath.Join(dir, "creds.json")
	flags := filepath.Join(dir, "flags.json")
	_ = os.WriteFile(creds, credsRaw, 0o600)
	flagRaw, _ := json.Marshal(map[string]string{"Static task": "flag{real-" + chStatic + "}"})
	_ = os.WriteFile(flags, flagRaw, 0o600)
	reportPath := filepath.Join(dir, "report.json")

	cfg, err := parseFlags([]string{"--domain", "stand.test", "--confirm-domain", "stand.test", "--event-slug", "lt", "--creds", creds, "--flags", flags,
		"--api-url", server.URL, "--teams", "2", "--members", "3", "--ramp-up", "100ms", "--duration", "4s", "--prewarm", "200ms",
		"--think-min", "20ms", "--think-max", "60ms", "--lab-poll", "80ms", "--results-poll", "100ms", "--warm-poll", "100ms",
		"--correct-ratio", "1", "--live-token", "tok", "--live-viewers", "1", "--report", reportPath})
	if err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if err = execute(context.Background(), cfg, &out, &logs); err != nil {
		t.Fatalf("execute: %v\n%s", err, logs.String())
	}

	var report Report
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report, out.String() + logs.String()
}

// TestWholeRunAgainstAFakeStand drives two teams through the full flow and checks that every
// step of the scenario was exercised and reported.
func TestWholeRunAgainstAFakeStand(t *testing.T) {
	stand := newFakeStand(t)
	report, out := runAgainst(t, stand)
	seen := map[string]EndpointReport{}
	for _, e := range report.Endpoints {
		seen[e.Endpoint] = e
	}
	for _, want := range []string{
		"GET /api/auth/me", "GET /api/events/self/info", "POST /api/events/self/join", "POST /api/events/{id}/teams", "POST /api/events/{id}/teams/join",
		"POST /api/events/{id}/teams/mine/form", "GET /api/events/{id}/teams/challenges/mine", "GET /api/events/{id}/teams/challenges/{id}/lab",
		"GET /api/events/{id}/teams/labs/vpn", "POST /api/events/{id}/teams/challenges/{id}/submit", "GET /api/events/{id}/results",
		"GET /api/events/{id}/results/changes", "GET /api/events/self/live-screen",
	} {
		if seen[want].Count == 0 {
			t.Errorf("endpoint %q was never called; got %v", want, keys(seen))
		}
	}
	if report.Errors != 0 {
		t.Errorf("errors = %d, want 0\n%s", report.Errors, out)
	}
	if report.Labs.Labs != 2 || report.Labs.Ready != 2 || report.Labs.P50s <= 0 {
		t.Errorf("labs = %+v, want 2 ready labs with a start time", report.Labs)
	}
	if report.Notes["flag accepted"] == 0 || report.Notes["vpn config fetched"] != 6 {
		t.Errorf("notes = %v", report.Notes)
	}
	if stand.badOrigin != 0 {
		t.Errorf("%d event requests carried the wrong Origin", stand.badOrigin)
	}
	if !strings.Contains(out, "p95 ms") || !strings.Contains(out, "Labs: 2 tracked, 2 ready") {
		t.Errorf("text report incomplete:\n%s", out)
	}
}

func resultsRow(report Report) EndpointReport {
	for _, e := range report.Endpoints {
		if e.Endpoint == "GET /api/events/{id}/results" {
			return e
		}
	}
	return EndpointReport{}
}

// A hidden scoreboard answers 403 (results hidden) by design; the scoreboard polls must not count it as a
// failure, or a run on such an event reports errors and trips the abort rule.
func TestHiddenScoreboardIsNotAFailure(t *testing.T) {
	stand := newFakeStand(t)
	stand.resultsDenied = resultsHiddenCode
	report, out := runAgainst(t, stand)
	results := resultsRow(report)
	if results.Count == 0 || results.Status[http.StatusForbidden] == 0 {
		t.Fatalf("the scoreboard was never asked or never hidden: %+v", results)
	}
	if results.ErrorRate != 0 || report.Errors != 0 {
		t.Errorf("hidden scoreboard counted as failure: results %+v, errors %d\n%s", results, report.Errors, out)
	}
}

// Any other 403 of the results endpoints (participants only: the join failed) stays an error.
func TestOtherResultsDenialIsAFailure(t *testing.T) {
	stand := newFakeStand(t)
	stand.resultsDenied = resultsHiddenCode + 1
	report, _ := runAgainst(t, stand)
	if results := resultsRow(report); results.Status[http.StatusForbidden] == 0 || results.ErrorRate == 0 {
		t.Fatalf("a participants-only 403 must count as an error: %+v", results)
	}
}

// resultsHiddenCode is the wire code of the platform's results-hidden error.
func TestResultsHiddenCodeMatchesThePlatform(t *testing.T) {
	var coded interface{ StatusCode() pkgerr.StatusCode }
	if !errors.As(eventConfigModel.ErrResultsHidden.Err(), &coded) {
		t.Fatal("ErrResultsHidden has no status code")
	}
	if got := coded.StatusCode().FullCode(); got != resultsHiddenCode {
		t.Fatalf("ErrResultsHidden is %d on the wire, the tool expects %d", got, resultsHiddenCode)
	}
}

func keys(m map[string]EndpointReport) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAbortRuleStopsAFailingRun(t *testing.T) {
	m := newMetrics(time.Now())
	for i := 0; i < 300; i++ {
		m.observe("GET /x", 503, time.Millisecond, false)
	}
	requests, errs, _ := m.window(time.Minute)
	if requests != 300 || errs != 300 {
		t.Fatalf("window = %d %d", requests, errs)
	}
	m.observe("GET /y", 409, time.Millisecond, true)
	if _, errs, _ = m.window(time.Minute); errs != 300 {
		t.Fatal("an expected 4xx must not count as an error")
	}
}

// The sealed ticket is base64 with «+», «/» and «=»; the daemon reads the cookie query-unescaped, so the
// header must carry the escaped form or the ticket is garbage there.
func TestSessionCookieIsEscapedOnTheWire(t *testing.T) {
	const sealed = "ab+cd/ef=="
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(sessionCookie)
		got = c.Value
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: url.QueryEscape(sealed)})
		_, _ = w.Write([]byte(`{"Status":{"Code":0},"Data":{}}`))
	}))
	defer server.Close()
	a := newAPI(server.URL, "", false, newMetrics(time.Now()), 1)
	res := a.do(context.Background(), request{Method: "GET", Path: "/x", Cookie: sealed})
	if url.QueryEscape(sealed) != got {
		t.Fatalf("wire value = %q, want %q", got, url.QueryEscape(sealed))
	}
	if back := cookieFrom(res.Header); back != sealed {
		t.Fatalf("cookieFrom = %q, want %q", back, sealed)
	}
}

func TestManualAccountsAreNeverUsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(path, []byte(`{"accounts":[{"n":1,"email":"a","cookie":"c"},{"n":2,"email":"m","manual":true}]}`), 0o600)
	accounts, err := readCreds(path)
	if err != nil || len(accounts) != 1 || accounts[0].Email != "a" {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
}
