package brainfake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Runs is the run side of the fake brain: `POST /v1/runs`, `POST /v1/runs/{id}/stop`, `POST /v1/runs/{id}/verdict` and
// `GET /v1/runs/{id}/events?after=`. A test scripts the events of a run with Emit and reads what the platform sent.
type Runs struct {
	mu sync.Mutex
	// StartStatus, when not 0, is the status StartRun answers with an error envelope.
	StartStatus int
	// EventsStatus, when not 0, is the status the event stream answers with an error envelope (the brain is down).
	EventsStatus int

	started      []json.RawMessage
	stops        []string
	materials    map[string][]Upload
	materialsErr []int    // statuses the next materials uploads answer with (then accepted)
	order        []string // the calls in the order they came: "materials:<id>", "start:<id>"
	verdicts     map[string][]json.RawMessage
	frames       map[string][]frame
	opened       map[string]int // event stream connections per run
	afters       map[string][]int64
	vFail        int // how many of the next verdicts are refused with vStatus
	vStatus      int
	closeAll     bool
	wake         chan struct{}
}

type frame struct {
	seq   int64
	event string
	data  string
}

// NewRuns makes an empty run side; set it as Fake.Runs.
func NewRuns() *Runs {
	return &Runs{verdicts: map[string][]json.RawMessage{}, materials: map[string][]Upload{}, frames: map[string][]frame{}, opened: map[string]int{}, afters: map[string][]int64{}, wake: make(chan struct{})}
}

// Emit adds an event to the run's stream and returns its sequence number.
func (r *Runs) Emit(runID, event, data string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := int64(len(r.frames[runID]) + 1)
	r.frames[runID] = append(r.frames[runID], frame{seq: seq, event: event, data: data})
	r.wakeLocked()
	return seq
}

// EmitAt adds an event with a chosen sequence number (to test gaps and a resume).
func (r *Runs) EmitAt(runID string, seq int64, event, data string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames[runID] = append(r.frames[runID], frame{seq: seq, event: event, data: data})
	r.wakeLocked()
}

func (r *Runs) wakeLocked() {
	close(r.wake)
	r.wake = make(chan struct{})
}

// DropStreams ends every open event stream cleanly (a network blip); the platform reconnects.
func (r *Runs) DropStreams() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeAll = true
	r.wakeLocked()
}

// AllowStreams lets event streams stay open again after DropStreams.
func (r *Runs) AllowStreams() { r.mu.Lock(); defer r.mu.Unlock(); r.closeAll = false }

// SetEventsStatus makes the event stream answer this status (0: normal).
func (r *Runs) SetEventsStatus(status int) { r.mu.Lock(); defer r.mu.Unlock(); r.EventsStatus = status }

// SetStartStatus makes StartRun answer this status (0: normal).
func (r *Runs) SetStartStatus(status int) { r.mu.Lock(); defer r.mu.Unlock(); r.StartStatus = status }

// Started are the bodies of the accepted StartRun calls.
func (r *Runs) Started() []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]json.RawMessage(nil), r.started...)
}

// Stops are the run ids StopRun was called for.
func (r *Runs) Stops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stops...)
}

// Verdicts are the verdict bodies sent for a run.
func (r *Runs) Verdicts(runID string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]json.RawMessage(nil), r.verdicts[runID]...)
}

// Afters are the `after` values the run's event stream was opened with, in order.
func (r *Runs) Afters(runID string) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.afters[runID]...)
}

// FailVerdicts makes the next n verdict calls answer this status with an error envelope (they are not recorded).
func (r *Runs) FailVerdicts(n, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vFail, r.vStatus = n, status
}

// Opened is how many times the run's event stream was opened.
func (r *Runs) Opened(runID string) int { r.mu.Lock(); defer r.mu.Unlock(); return r.opened[runID] }

// ServeHTTP handles the /v1/runs routes.
func (r *Runs) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rest := strings.TrimPrefix(req.URL.Path, "/v1/runs")
	rest = strings.Trim(rest, "/")
	switch {
	case rest == "" && req.Method == http.MethodPost:
		r.start(w, req)
	case strings.HasSuffix(rest, "/materials") && req.Method == http.MethodPost:
		r.upload(w, req, strings.TrimSuffix(rest, "/materials"))
	case strings.HasSuffix(rest, "/stop") && req.Method == http.MethodPost:
		r.mu.Lock()
		r.stops = append(r.stops, strings.TrimSuffix(rest, "/stop"))
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(rest, "/verdict") && req.Method == http.MethodPost:
		id := strings.TrimSuffix(rest, "/verdict")
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		if r.vFail > 0 {
			r.vFail--
			status := r.vStatus
			r.mu.Unlock()
			Fail(w, status, "unavailable", "try again")
			return
		}
		r.verdicts[id] = append(r.verdicts[id], body)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(rest, "/events") && req.Method == http.MethodGet:
		r.events(w, req, strings.TrimSuffix(rest, "/events"))
	default:
		Fail(w, http.StatusNotFound, "not_found", "no such route")
	}
}

func (r *Runs) start(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	status := r.StartStatus
	if status == 0 {
		r.started = append(r.started, body)
		var probe struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(body, &probe)
		r.order = append(r.order, "start:"+probe.RunID)
	}
	r.mu.Unlock()
	if status != 0 {
		Fail(w, status, "start_failed", "the run was refused")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{}`))
}

func (r *Runs) events(w http.ResponseWriter, req *http.Request, runID string) {
	after, _ := strconv.ParseInt(req.URL.Query().Get("after"), 10, 64)
	r.mu.Lock()
	status := r.EventsStatus
	if status == 0 {
		r.opened[runID]++
		r.afters[runID] = append(r.afters[runID], after)
	}
	r.mu.Unlock()
	if status != 0 {
		Fail(w, status, "unavailable", "the brain is down")
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		r.mu.Lock()
		var next []frame
		for _, f := range r.frames[runID] {
			if f.seq > after {
				next = append(next, f)
			}
		}
		closing, wake := r.closeAll, r.wake
		r.mu.Unlock()
		finished := false
		for _, f := range next {
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.seq, f.event, f.data)
			after = f.seq
			finished = finished || f.event == "run_finished"
		}
		if flusher != nil {
			flusher.Flush()
		}
		if finished || closing {
			return
		}
		select {
		case <-req.Context().Done():
			return
		case <-wake:
		}
	}
}

// Upload is one accepted materials upload: the raw body with the headers that matter.
type Upload struct {
	Body          []byte
	ContentType   string
	RequestedWith string
}

// FailMaterials makes the next uploads answer these statuses with an error envelope, one each (then they are accepted).
func (r *Runs) FailMaterials(statuses ...int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.materialsErr = statuses
}

// Materials are the accepted uploads of a run.
func (r *Runs) Materials(runID string) []Upload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Upload(nil), r.materials[runID]...)
}

// Order is the accepted materials uploads and starts in the order they came ("materials:<id>", "start:<id>").
func (r *Runs) Order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func (r *Runs) upload(w http.ResponseWriter, req *http.Request, runID string) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	if len(r.materialsErr) > 0 {
		status := r.materialsErr[0]
		r.materialsErr = r.materialsErr[1:]
		r.mu.Unlock()
		Fail(w, status, "upload_failed", "the upload was not accepted")
		return
	}
	if req.Header.Get("X-Requested-With") == "" {
		r.mu.Unlock()
		Fail(w, http.StatusForbidden, "csrf", "the marker header is missing")
		return
	}
	r.materials[runID] = append(r.materials[runID], Upload{Body: body, ContentType: req.Header.Get("Content-Type"), RequestedWith: req.Header.Get("X-Requested-With")})
	r.order = append(r.order, "materials:"+runID)
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}
