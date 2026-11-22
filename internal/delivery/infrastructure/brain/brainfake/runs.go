package brainfake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// Runs is the run side of the fake brain: StartRun, UploadMaterials, RunEvents, PostVerdict, PostLabFacts and StopRun. A test
// scripts the events of a run with Emit and reads what the platform sent.
type Runs struct {
	mu           sync.Mutex
	startStatus  codes.Code // when not OK, StartRun answers it
	eventsStatus codes.Code // when not OK, RunEvents answers it (the brain is down, or NotFound)

	started      []*brainapiv1.StartRunRequest
	stops        []string
	materials    map[string][]Materials
	materialsErr []codes.Code // codes the next materials uploads answer with (then accepted)
	order        []string     // the calls in the order they came: "materials:<id>", "start:<id>"
	verdicts     map[string][]*brainapiv1.VerdictRequest
	frames       map[string][]*brainapiv1.RunEvent
	opened       map[string]int // event stream connections per run
	afters       map[string][]uint64
	stopStatus   codes.Code // when not OK, StopRun answers it
	vFail        int        // how many of the next verdicts are refused with vStatus
	vStatus      codes.Code
	labFacts     map[string][]*brainapiv1.LabFactsRequest
	lfFail       int // how many of the next lab_facts calls are refused with lfStatus
	lfStatus     codes.Code
	replay       []string // runs whose next stream starts from the first event
	closeAll     bool
	wake         chan struct{}
}

// NewRuns makes an empty run side (New makes one for every fake).
func NewRuns() *Runs {
	return &Runs{verdicts: map[string][]*brainapiv1.VerdictRequest{}, labFacts: map[string][]*brainapiv1.LabFactsRequest{}, materials: map[string][]Materials{}, frames: map[string][]*brainapiv1.RunEvent{}, opened: map[string]int{}, afters: map[string][]uint64{}, wake: make(chan struct{})}
}

// Emit adds an event to the run's stream and returns its sequence number.
func (r *Runs) Emit(runID, typ, data string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := uint64(len(r.frames[runID]) + 1)
	r.frames[runID] = append(r.frames[runID], &brainapiv1.RunEvent{Seq: seq, Type: typ, Data: []byte(data)})
	r.wakeLocked()
	return seq
}

// EmitAt adds an event with a chosen sequence number (to test gaps, a resume and duplicates).
func (r *Runs) EmitAt(runID string, seq uint64, typ, data string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames[runID] = append(r.frames[runID], &brainapiv1.RunEvent{Seq: seq, Type: typ, Data: []byte(data)})
	r.wakeLocked()
}

// Replay makes the next stream of the run send every event again from the first, whatever after_seq it asks for (a brain that
// does not filter: the platform must drop what it stored already).
func (r *Runs) Replay(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replay = append(r.replay, runID)
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

// SetEventsStatus makes the event stream answer this code (OK: normal).
func (r *Runs) SetEventsStatus(code codes.Code) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventsStatus = code
}

// SetStartStatus makes StartRun answer this code (OK: normal).
func (r *Runs) SetStartStatus(code codes.Code) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startStatus = code
}

// SetStopStatus makes StopRun answer this code (OK: normal); the call is still counted in Stops.
func (r *Runs) SetStopStatus(code codes.Code) { r.mu.Lock(); defer r.mu.Unlock(); r.stopStatus = code }

// FailVerdicts makes the next n verdict calls answer this code (they are not recorded).
func (r *Runs) FailVerdicts(n int, code codes.Code) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vFail, r.vStatus = n, code
}

// FailLabFacts makes the next n lab facts calls answer this code (they are not recorded).
func (r *Runs) FailLabFacts(n int, code codes.Code) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lfFail, r.lfStatus = n, code
}

// FailMaterials makes the next uploads answer these codes, one each (then they are accepted).
func (r *Runs) FailMaterials(codes ...codes.Code) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.materialsErr = codes
}

// Started are the accepted StartRun requests as JSON (see JSON).
func (r *Runs) Started() []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]json.RawMessage, 0, len(r.started))
	for _, s := range r.started {
		out = append(out, JSON(s))
	}
	return out
}

// StartedRequests are the accepted StartRun requests.
func (r *Runs) StartedRequests() []*brainapiv1.StartRunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*brainapiv1.StartRunRequest, 0, len(r.started))
	for _, s := range r.started {
		out = append(out, proto.CloneOf(s))
	}
	return out
}

// Stops are the run ids StopRun was called for.
func (r *Runs) Stops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stops...)
}

// Verdicts are the verdicts sent for a run as JSON.
func (r *Runs) Verdicts(runID string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]json.RawMessage, 0, len(r.verdicts[runID]))
	for _, v := range r.verdicts[runID] {
		out = append(out, JSON(v))
	}
	return out
}

// VerdictRequests are the verdicts sent for a run.
func (r *Runs) VerdictRequests(runID string) []*brainapiv1.VerdictRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*brainapiv1.VerdictRequest(nil), r.verdicts[runID]...)
}

// LabFacts are the lab facts sent for a run as JSON.
func (r *Runs) LabFacts(runID string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]json.RawMessage, 0, len(r.labFacts[runID]))
	for _, v := range r.labFacts[runID] {
		out = append(out, JSON(v))
	}
	return out
}

// LabFactsRequests are the lab facts sent for a run.
func (r *Runs) LabFactsRequests(runID string) []*brainapiv1.LabFactsRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*brainapiv1.LabFactsRequest(nil), r.labFacts[runID]...)
}

// Afters are the after_seq values the run's event stream was opened with, in order.
func (r *Runs) Afters(runID string) []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.afters[runID]...)
}

// Opened is how many times the run's event stream was opened.
func (r *Runs) Opened(runID string) int { r.mu.Lock(); defer r.mu.Unlock(); return r.opened[runID] }

// Materials is one accepted materials upload: the meta and the files put together from their chunks, with the number of chunks
// and the largest one.
type Materials struct {
	Meta     *brainapiv1.MaterialsMeta
	Files    []UploadedFile
	Chunks   int
	MaxChunk int
}

// UploadedFile is one file of a materials upload.
type UploadedFile struct {
	Name, ContentType string
	Data              []byte
}

// Materials are the accepted uploads of a run.
func (r *Runs) Materials(runID string) []Materials {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Materials(nil), r.materials[runID]...)
}

// Order is the accepted materials uploads and starts in the order they came ("materials:<id>", "start:<id>").
func (r *Runs) Order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func (r *Runs) upload(stream grpc.ClientStreamingServer[brainapiv1.MaterialsChunk, emptypb.Empty]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	meta := first.GetMeta()
	if meta == nil || meta.GetRunId() == "" {
		return fail(codes.InvalidArgument)
	}
	up := Materials{Meta: meta}
	hash := sha256.New()
	var total uint64
	open := false
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		f := chunk.GetFile()
		if f == nil {
			return fail(codes.InvalidArgument) // a second meta
		}
		up.Chunks++
		up.MaxChunk = max(up.MaxChunk, len(f.GetData()))
		if !open {
			up.Files = append(up.Files, UploadedFile{Name: f.GetName(), ContentType: f.GetContentType()})
			open = true
		}
		last := &up.Files[len(up.Files)-1]
		last.Data = append(last.Data, f.GetData()...)
		hash.Write(f.GetData())
		total += uint64(len(f.GetData()))
		if f.GetLast() {
			open = false
		}
	}
	if open || total != meta.GetTotalSize() || hex.EncodeToString(hash.Sum(nil)) != meta.GetSha256() {
		return fail(codes.InvalidArgument)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.materialsErr) > 0 {
		code := r.materialsErr[0]
		r.materialsErr = r.materialsErr[1:]
		return fail(code)
	}
	r.materials[meta.GetRunId()] = append(r.materials[meta.GetRunId()], up)
	r.order = append(r.order, "materials:"+meta.GetRunId())
	return stream.SendAndClose(&emptypb.Empty{})
}

func (r *Runs) start(req *brainapiv1.StartRunRequest) (*brainapiv1.RunRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startStatus != codes.OK {
		return nil, fail(r.startStatus)
	}
	r.started = append(r.started, req)
	r.order = append(r.order, "start:"+req.GetRunId())
	return &brainapiv1.RunRef{RunId: req.GetRunId()}, nil
}

func (r *Runs) stop(req *brainapiv1.RunRef) (*emptypb.Empty, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops = append(r.stops, req.GetRunId())
	if r.stopStatus != codes.OK {
		return nil, fail(r.stopStatus)
	}
	return &emptypb.Empty{}, nil
}

func (r *Runs) verdict(req *brainapiv1.VerdictRequest) (*emptypb.Empty, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vFail > 0 {
		r.vFail--
		return nil, fail(r.vStatus)
	}
	r.verdicts[req.GetRunId()] = append(r.verdicts[req.GetRunId()], req)
	return &emptypb.Empty{}, nil
}

func (r *Runs) postLabFacts(req *brainapiv1.LabFactsRequest) (*emptypb.Empty, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lfFail > 0 {
		r.lfFail--
		return nil, fail(r.lfStatus)
	}
	r.labFacts[req.GetRunId()] = append(r.labFacts[req.GetRunId()], req)
	return &emptypb.Empty{}, nil
}

func (r *Runs) events(req *brainapiv1.RunEventsRequest, stream grpc.ServerStreamingServer[brainapiv1.RunEvent]) error {
	runID, after := req.GetRunId(), req.GetAfterSeq()
	r.mu.Lock()
	if r.eventsStatus != codes.OK {
		code := r.eventsStatus
		r.mu.Unlock()
		return fail(code)
	}
	r.opened[runID]++
	r.afters[runID] = append(r.afters[runID], after)
	for i, id := range r.replay {
		if id == runID {
			r.replay = append(r.replay[:i], r.replay[i+1:]...)
			after = 0
			break
		}
	}
	r.mu.Unlock()
	sent := 0
	for {
		r.mu.Lock()
		var next []*brainapiv1.RunEvent
		for _, f := range r.frames[runID][sent:] {
			next = append(next, f)
		}
		sent = len(r.frames[runID])
		closing, wake := r.closeAll, r.wake
		r.mu.Unlock()
		finished := false
		for _, f := range next {
			if f.GetSeq() <= after {
				continue
			}
			if err := stream.Send(f); err != nil {
				return err
			}
			finished = finished || f.GetType() == "run_finished"
		}
		if finished || closing {
			return nil
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-wake:
		}
	}
}
