package brain

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// The limits of the control plane (brain.proto): a unary call has 30 s, the best-effort calls (StopRun, PostLabFacts) 5 s, a
// stream none; the client pings every 30 s (the brain allows down to 20 s); a unary message is at most 16 MiB and a stream chunk
// at most 1 MiB.
const (
	UnaryTimeout      = 30 * time.Second
	BestEffortTimeout = 5 * time.Second
	KeepaliveTime     = 30 * time.Second
	keepaliveTimeout  = 10 * time.Second
	MaxMessageBytes   = 16 << 20
	MaxChunkBytes     = 1 << 20
	// batchBytes is what one PushData batch is filled to: below MaxMessageBytes by a margin for the framing of the items.
	batchBytes = MaxMessageBytes - 64<<10
)

// ErrAggregateTooLarge: one aggregate alone does not fit a PushData message.
var ErrAggregateTooLarge = errors.New("a statistics aggregate is larger than one message to the brain may be")

// Control is the gRPC client of the brain's machine control plane (pkg/brainapi/v1): runs, opponents, statistics, the task
// archive and the outbox stream. It dials the gRPC endpoint of the stored connection over mutual TLS with the enrolled client
// certificate and the pinned brain CA, the same material as the HTTP client of the chat. It is immutable: a renewed
// certificate is a new Control.
type Control struct {
	conn *grpc.ClientConn
	api  brainapiv1.BrainClient
}

// NewControl builds the client of one gRPC endpoint (host:port). caPEM verifies the brain's server certificate; empty means the
// system roots. It dials lazily: nothing is sent before the first call.
func NewControl(endpoint string, certPEM, keyPEM, caPEM []byte) (*Control, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("client certificate and key: %w", err)
	}
	pool, err := rootPool(caPEM)
	if err != nil {
		return nil, err
	}
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12})
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(creds),
		// Pings only while a call is open: the long streams (run events, the outbox) are what must survive a quiet network.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: KeepaliveTime, Timeout: keepaliveTimeout}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes), grpc.MaxCallSendMsgSize(MaxMessageBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("cyber-brain gRPC endpoint %q: %w", endpoint, err)
	}
	return &Control{conn: conn, api: brainapiv1.NewBrainClient(conn)}, nil
}

// Close ends the connection and every call on it.
func (c *Control) Close() error { return c.conn.Close() }

func unary(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

// successUnless maps the codes the contract calls a success to nil.
func successUnless(err error, success ...codes.Code) error {
	code := Code(err)
	for _, c := range success {
		if code == c {
			return nil
		}
	}
	return err
}

// StartRun starts a run; AlreadyExists (the brain has this run) is a success.
func (c *Control) StartRun(ctx context.Context, req *brainapiv1.StartRunRequest) error {
	ctx, cancel := unary(ctx, UnaryTimeout)
	defer cancel()
	_, err := c.api.StartRun(ctx, req)
	return successUnless(err, codes.AlreadyExists)
}

// File is one file of a run's materials.
type File struct {
	Name, ContentType string
	Data              []byte
}

// UploadMaterials streams the participant's view of a task: the meta first (task.json inline, the total size and the sha256 of
// the files payload: the bytes of every file in order), then every file in chunks of at most MaxChunkBytes, the last chunk of a
// file marked. AlreadyExists (the brain has them for this run) is a success. Like every stream it has no deadline of its own.
func (c *Control) UploadMaterials(ctx context.Context, runID string, taskJSON []byte, files []File) error {
	hash := sha256.New()
	var total uint64
	for _, f := range files {
		hash.Write(f.Data)
		total += uint64(len(f.Data))
	}
	// A client stream has no deadline (the contract): ctx and the keepalive end a call that hangs.
	stream, err := c.api.UploadMaterials(ctx)
	if err != nil {
		return err
	}
	send := func(chunk *brainapiv1.MaterialsChunk) error {
		if sendErr := stream.Send(chunk); sendErr != nil {
			// The status of a refused stream is the one CloseAndRecv returns.
			_, recvErr := stream.CloseAndRecv()
			return errors.Join(sendErr, recvErr)
		}
		return nil
	}
	meta := &brainapiv1.MaterialsMeta{RunId: runID, TaskJson: taskJSON, TotalSize: total, Sha256: hex.EncodeToString(hash.Sum(nil))}
	if err = send(&brainapiv1.MaterialsChunk{Part: &brainapiv1.MaterialsChunk_Meta{Meta: meta}}); err != nil {
		return successUnless(lastStatus(err), codes.AlreadyExists)
	}
	for _, f := range files {
		for off := 0; ; off += MaxChunkBytes {
			end := min(off+MaxChunkBytes, len(f.Data))
			last := end == len(f.Data)
			chunk := &brainapiv1.FileChunk{Name: f.Name, ContentType: f.ContentType, Data: f.Data[off:end], Last: last}
			if err = send(&brainapiv1.MaterialsChunk{Part: &brainapiv1.MaterialsChunk_File{File: chunk}}); err != nil {
				return successUnless(lastStatus(err), codes.AlreadyExists)
			}
			if last {
				break
			}
		}
	}
	_, err = stream.CloseAndRecv()
	return successUnless(err, codes.AlreadyExists)
}

// lastStatus picks the error that carries a gRPC status out of a joined send/receive failure.
func lastStatus(err error) error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		errs := joined.Unwrap()
		for i := len(errs) - 1; i >= 0; i-- {
			if errs[i] != nil && Code(errs[i]) != codes.Unknown {
				return errs[i]
			}
		}
	}
	return err
}

// RunEvents opens the event stream of a run after a sequence number. It has no deadline: ctx ends it. A closed stream is not
// the end of the run (the caller reopens it); an unknown run is NotFound.
func (c *Control) RunEvents(ctx context.Context, runID string, after uint64) (grpc.ServerStreamingClient[brainapiv1.RunEvent], error) {
	return c.api.RunEvents(ctx, &brainapiv1.RunEventsRequest{RunId: runID, AfterSeq: after})
}

// PostVerdict tells the brain whether an answer was right; idempotent by (run_id, answer_seq), so AlreadyExists is a success.
func (c *Control) PostVerdict(ctx context.Context, req *brainapiv1.VerdictRequest) error {
	ctx, cancel := unary(ctx, UnaryTimeout)
	defer cancel()
	_, err := c.api.PostVerdict(ctx, req)
	return successUnless(err, codes.AlreadyExists)
}

// PostLabFacts sends the lab-side facts of a run, best effort within BestEffortTimeout; AlreadyExists is a success.
func (c *Control) PostLabFacts(ctx context.Context, req *brainapiv1.LabFactsRequest) error {
	ctx, cancel := unary(ctx, BestEffortTimeout)
	defer cancel()
	_, err := c.api.PostLabFacts(ctx, req)
	return successUnless(err, codes.AlreadyExists)
}

// StopRun tells the brain to stop a run, best effort within BestEffortTimeout; NotFound (nothing to stop) is a success.
func (c *Control) StopRun(ctx context.Context, runID string) error {
	ctx, cancel := unary(ctx, BestEffortTimeout)
	defer cancel()
	_, err := c.api.StopRun(ctx, &brainapiv1.RunRef{RunId: runID})
	return successUnless(err, codes.NotFound)
}

// GetOpponent asks for the ready-to-deploy spec of an opponent.
func (c *Control) GetOpponent(ctx context.Context, req *brainapiv1.GetOpponentRequest) (*brainapiv1.OpponentSpec, error) {
	ctx, cancel := unary(ctx, UnaryTimeout)
	defer cancel()
	return c.api.GetOpponent(ctx, req)
}

// PushData sends the statistics aggregates (one JSON object each) in as many calls as the message limit needs, and returns the
// items the brain rejected. A failed call returns at once with the rejections so far; an aggregate that alone is above the
// limit fails with ErrAggregateTooLarge before anything is sent.
func (c *Control) PushData(ctx context.Context, aggregates [][]byte) ([]*brainapiv1.ItemRejection, error) {
	var (
		batches [][]*brainapiv1.TaskAggregate
		current []*brainapiv1.TaskAggregate
		size    int
	)
	for _, raw := range aggregates {
		item := &brainapiv1.TaskAggregate{Json: raw}
		n := proto.Size(item) + 8 // the tag and length of the item in the request
		if n > batchBytes {
			return nil, ErrAggregateTooLarge
		}
		if size+n > batchBytes && len(current) > 0 {
			batches, current, size = append(batches, current), nil, 0
		}
		current, size = append(current, item), size+n
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	var rejected []*brainapiv1.ItemRejection
	for _, batch := range batches {
		callCtx, cancel := unary(ctx, UnaryTimeout)
		ack, err := c.api.PushData(callCtx, &brainapiv1.PushDataRequest{Aggregates: batch})
		cancel()
		if err != nil {
			return rejected, err
		}
		rejected = append(rejected, ack.GetRejected()...)
	}
	return rejected, nil
}

// UploadTaskArchive streams a task archive for a provide_task request: the meta {request_id, size, sha256} first, then the bytes
// in chunks of at most MaxChunkBytes. AlreadyExists (delivered before) is a success; a refusal arrives on the outbox instead.
func (c *Control) UploadTaskArchive(ctx context.Context, requestID string, archive []byte) error {
	sum := sha256.Sum256(archive)
	stream, err := c.api.UploadTaskArchive(ctx)
	if err != nil {
		return err
	}
	send := func(chunk *brainapiv1.ArchiveChunk) error {
		if sendErr := stream.Send(chunk); sendErr != nil {
			_, recvErr := stream.CloseAndRecv()
			return lastStatus(errors.Join(sendErr, recvErr))
		}
		return nil
	}
	meta := &brainapiv1.ArchiveMeta{RequestId: requestID, Size: uint64(len(archive)), Sha256: hex.EncodeToString(sum[:])}
	if err = send(&brainapiv1.ArchiveChunk{Part: &brainapiv1.ArchiveChunk_Meta{Meta: meta}}); err != nil {
		return successUnless(err, codes.AlreadyExists)
	}
	for off := 0; off < len(archive); off += MaxChunkBytes {
		end := min(off+MaxChunkBytes, len(archive))
		if err = send(&brainapiv1.ArchiveChunk{Part: &brainapiv1.ArchiveChunk_Data{Data: archive[off:end]}}); err != nil {
			return successUnless(err, codes.AlreadyExists)
		}
	}
	_, err = stream.CloseAndRecv()
	return successUnless(err, codes.AlreadyExists)
}

// Requests opens the outbox: the bidirectional stream of the brain's requests (down) and the platform's answers (up). It has no
// deadline: ctx ends it.
func (c *Control) Requests(ctx context.Context) (grpc.BidiStreamingClient[brainapiv1.RequestAnswer, brainapiv1.ServerFrame], error) {
	return c.api.Requests(ctx)
}
