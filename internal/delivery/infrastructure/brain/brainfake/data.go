package brainfake

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// Data is the statistics ingest of the fake (PushData): what each call carried, and the tasks it rejects.
type Data struct {
	mu       sync.Mutex
	requests []*brainapiv1.PushDataRequest
	reject   map[string]string // task_ref -> reason
	failNext []codes.Code
}

// NewData makes an empty ingest (New makes one for every fake).
func NewData() *Data { return &Data{reject: map[string]string{}} }

// Reject makes every aggregate of the task rejected with the reason.
func (d *Data) Reject(taskRef, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reject[taskRef] = reason
}

// FailNext makes the next calls answer these codes, one each.
func (d *Data) FailNext(codes ...codes.Code) { d.mu.Lock(); defer d.mu.Unlock(); d.failNext = codes }

// Calls are the aggregates (the JSON of each TaskAggregate) of every accepted call.
func (d *Data) Calls() [][]json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][]json.RawMessage, 0, len(d.requests))
	for _, req := range d.requests {
		var call []json.RawMessage
		for _, a := range req.GetAggregates() {
			call = append(call, json.RawMessage(a.GetJson()))
		}
		out = append(out, call)
	}
	return out
}

// Requests are the accepted PushData requests.
func (d *Data) Requests() []*brainapiv1.PushDataRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*brainapiv1.PushDataRequest, 0, len(d.requests))
	for _, r := range d.requests {
		out = append(out, proto.CloneOf(r))
	}
	return out
}

func (d *Data) push(req *brainapiv1.PushDataRequest) (*brainapiv1.PushDataAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.failNext) > 0 {
		code := d.failNext[0]
		d.failNext = d.failNext[1:]
		return nil, fail(code)
	}
	d.requests = append(d.requests, req)
	ack := &brainapiv1.PushDataAck{}
	for _, a := range req.GetAggregates() {
		var key struct {
			TaskRef string `json:"task_ref"`
			Version string `json:"version"`
		}
		if json.Unmarshal(a.GetJson(), &key) != nil {
			ack.Rejected = append(ack.Rejected, &brainapiv1.ItemRejection{Reason: "not JSON"})
			continue
		}
		if reason, ok := d.reject[key.TaskRef]; ok {
			ack.Rejected = append(ack.Rejected, &brainapiv1.ItemRejection{TaskRef: key.TaskRef, Version: key.Version, Reason: reason})
		}
	}
	return ack, nil
}

// Archives is the task archive upload of the fake (UploadTaskArchive). A stored archive also closes its outbox request, the way
// the brain does.
type Archives struct {
	mu       sync.Mutex
	uploads  map[string]*Archive
	failNext []codes.Code
	onStored func(requestID string)
}

// Archive is one stored upload: the meta, the bytes and the largest chunk.
type Archive struct {
	Meta     *brainapiv1.ArchiveMeta
	Data     []byte
	MaxChunk int
}

// NewArchives makes an empty archive store (New makes one for every fake).
func NewArchives() *Archives { return &Archives{uploads: map[string]*Archive{}} }

// FailNext makes the next uploads answer these codes, one each.
func (a *Archives) FailNext(codes ...codes.Code) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failNext = codes
}

// Uploaded is the archive stored for a request (nil: none).
func (a *Archives) Uploaded(requestID string) *Archive {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.uploads[requestID]
}

func (a *Archives) upload(stream grpc.ClientStreamingServer[brainapiv1.ArchiveChunk, brainapiv1.UploadAck]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	meta := first.GetMeta()
	if meta == nil || meta.GetRequestId() == "" {
		return fail(codes.InvalidArgument)
	}
	got := &Archive{Meta: meta}
	var buf bytes.Buffer
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		if chunk.GetMeta() != nil {
			return fail(codes.InvalidArgument)
		}
		got.MaxChunk = max(got.MaxChunk, len(chunk.GetData()))
		buf.Write(chunk.GetData())
	}
	got.Data = buf.Bytes()
	sum := sha256.Sum256(got.Data)
	if uint64(len(got.Data)) != meta.GetSize() || hex.EncodeToString(sum[:]) != meta.GetSha256() {
		return fail(codes.InvalidArgument)
	}
	a.mu.Lock()
	if len(a.failNext) > 0 {
		code := a.failNext[0]
		a.failNext = a.failNext[1:]
		a.mu.Unlock()
		return fail(code)
	}
	if _, ok := a.uploads[meta.GetRequestId()]; ok {
		a.mu.Unlock()
		return fail(codes.AlreadyExists)
	}
	a.uploads[meta.GetRequestId()] = got
	onStored := a.onStored
	a.mu.Unlock()
	if onStored != nil {
		onStored(meta.GetRequestId())
	}
	return stream.SendAndClose(&brainapiv1.UploadAck{Stored: true})
}

// Opponents is GetOpponent of the fake: a test sets the answer; without one the call is Unimplemented (a brain that has no
// opponent specs yet).
type Opponents struct {
	mu       sync.Mutex
	answer   func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error)
	requests []*brainapiv1.GetOpponentRequest
}

// Answer sets what GetOpponent answers.
func (o *Opponents) Answer(fn func(*brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answer = fn
}

// Requests are the GetOpponent requests that came.
func (o *Opponents) Requests() []*brainapiv1.GetOpponentRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*brainapiv1.GetOpponentRequest(nil), o.requests...)
}

func (o *Opponents) get(req *brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
	o.mu.Lock()
	o.requests = append(o.requests, req)
	answer := o.answer
	o.mu.Unlock()
	if answer == nil {
		return nil, fail(codes.Unimplemented)
	}
	return answer(req)
}
