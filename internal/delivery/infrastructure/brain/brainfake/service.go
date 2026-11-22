package brainfake

import (
	"bytes"
	"context"
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// service is the gRPC side of the fake: every call goes to the part of the fake that owns it.
type service struct {
	brainapiv1.UnimplementedBrainServer
	f *Fake
}

func (s *service) UploadMaterials(stream grpc.ClientStreamingServer[brainapiv1.MaterialsChunk, emptypb.Empty]) error {
	return s.f.Runs.upload(stream)
}

func (s *service) StartRun(_ context.Context, req *brainapiv1.StartRunRequest) (*brainapiv1.RunRef, error) {
	return s.f.Runs.start(req)
}

func (s *service) RunEvents(req *brainapiv1.RunEventsRequest, stream grpc.ServerStreamingServer[brainapiv1.RunEvent]) error {
	return s.f.Runs.events(req, stream)
}

func (s *service) PostVerdict(_ context.Context, req *brainapiv1.VerdictRequest) (*emptypb.Empty, error) {
	return s.f.Runs.verdict(req)
}

func (s *service) PostLabFacts(_ context.Context, req *brainapiv1.LabFactsRequest) (*emptypb.Empty, error) {
	return s.f.Runs.postLabFacts(req)
}

func (s *service) StopRun(_ context.Context, req *brainapiv1.RunRef) (*emptypb.Empty, error) {
	return s.f.Runs.stop(req)
}

func (s *service) GetOpponent(_ context.Context, req *brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
	return s.f.Opponents.get(req)
}

func (s *service) PushData(_ context.Context, req *brainapiv1.PushDataRequest) (*brainapiv1.PushDataAck, error) {
	return s.f.Data.push(req)
}

func (s *service) UploadTaskArchive(stream grpc.ClientStreamingServer[brainapiv1.ArchiveChunk, brainapiv1.UploadAck]) error {
	return s.f.Archives.upload(stream)
}

func (s *service) Requests(stream grpc.BidiStreamingServer[brainapiv1.RequestAnswer, brainapiv1.ServerFrame]) error {
	return s.f.Outbox.serve(stream)
}

// fail is the error a scripted refusal answers with.
func fail(code codes.Code) error { return status.Error(code, "scripted by the test: "+code.String()) }

// JSON is a message as proto-JSON with the proto field names and every field present (zero values too) except an unset
// optional field, compacted: what the tests read of what the platform sent.
func JSON(m proto.Message) json.RawMessage {
	return marshal(protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}, m)
}

// WireJSON is a message as proto-JSON with the proto field names and only the fields that are set (what proto3 puts on the
// wire), compacted: the form of the fixtures of testdata/brain/wire.
func WireJSON(m proto.Message) json.RawMessage {
	return marshal(protojson.MarshalOptions{UseProtoNames: true}, m)
}

func marshal(o protojson.MarshalOptions, m proto.Message) json.RawMessage {
	raw, err := o.Marshal(m)
	if err != nil {
		panic(err)
	}
	// protojson varies its whitespace on purpose: the compact form is stable.
	var out bytes.Buffer
	if err = json.Compact(&out, raw); err != nil {
		panic(err)
	}
	return out.Bytes()
}
