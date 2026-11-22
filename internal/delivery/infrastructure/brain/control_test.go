package brain_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain"
	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain/brainfake"
	"github.com/cybericebox/backend/pkg/agentcrypto"
	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// control enrolls at the fake over HTTP and opens the gRPC control client with the issued certificate and the pinned CA.
func control(t *testing.T, f *brainfake.Fake) *brain.Control {
	t.Helper()
	key, err := agentcrypto.NewClientKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := brain.Enroll(context.Background(), f.Endpoint(), f.CAPEM, brainfake.Token, key.CSRPEM)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	c, err := brain.NewControl(f.GRPCEndpoint(), []byte(out.CertPEM), []byte(key.KeyPEM), []byte(out.CAPEM))
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func withRetryInfo(t *testing.T, code codes.Code, delay time.Duration) error {
	t.Helper()
	st, err := status.New(code, "slow down").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(delay)})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestRetryRetriesOnlyUnavailableResourceExhaustedAndTheNetwork(t *testing.T) {
	for _, tc := range []struct {
		err      error
		attempts int
	}{
		{status.Error(codes.Unavailable, "down"), 3},
		{status.Error(codes.ResourceExhausted, "busy"), 3},
		{status.Error(codes.DeadlineExceeded, "no answer in time"), 3},
		{errors.New("connection reset by peer"), 3},
		{status.Error(codes.InvalidArgument, "bad"), 1},
		{status.Error(codes.Internal, "bug"), 1},
		{status.Error(codes.PermissionDenied, "no"), 1},
		{status.Error(codes.NotFound, "gone"), 1},
		{status.Error(codes.FailedPrecondition, "not ready"), 1},
	} {
		n := 0
		err := brain.Retry(context.Background(), 3, time.Millisecond, func(context.Context) error { n++; return tc.err })
		if n != tc.attempts || err == nil {
			t.Errorf("%v: %d attempts (want %d), err %v", tc.err, n, tc.attempts, err)
		}
	}
	n := 0
	if err := brain.Retry(context.Background(), 3, time.Millisecond, func(context.Context) error {
		n++
		if n < 3 {
			return status.Error(codes.Unavailable, "down")
		}
		return nil
	}); err != nil || n != 3 {
		t.Fatalf("a success after two transient failures: %d %v", n, err)
	}
}

func TestRetryHonoursRetryInfo(t *testing.T) {
	started := time.Now()
	n := 0
	err := brain.Retry(context.Background(), 2, time.Millisecond, func(context.Context) error {
		n++
		if n == 1 {
			return withRetryInfo(t, codes.ResourceExhausted, 80*time.Millisecond)
		}
		return nil
	})
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if waited := time.Since(started); waited < 80*time.Millisecond {
		t.Fatalf("the retry must wait the delay the brain asked for: %v", waited)
	}
	if d, ok := brain.RetryDelay(withRetryInfo(t, codes.ResourceExhausted, time.Second)); !ok || d != time.Second {
		t.Fatalf("RetryDelay: %v %v", d, ok)
	}
}

func TestReasonIsReadFromErrorInfo(t *testing.T) {
	st, err := status.New(codes.NotFound, "no such run").WithDetails(&errdetails.ErrorInfo{Reason: "gone", Domain: "brain"})
	if err != nil {
		t.Fatal(err)
	}
	if got := brain.Reason(st.Err()); got != "gone" {
		t.Fatalf("reason = %q", got)
	}
	if got := brain.Reason(status.Error(codes.NotFound, "plain")); got != "" {
		t.Fatalf("no ErrorInfo: %q", got)
	}
	if brain.Code(errors.New("x")) != codes.Unknown || brain.Code(nil) != codes.OK {
		t.Fatal("Code of a plain error and of nil")
	}
}

func TestControlGoesOverMutualTLSAndARepeatedStartIsASuccess(t *testing.T) {
	f := brainfake.New()
	t.Cleanup(f.Close)
	c := control(t, f)
	req := &brainapiv1.StartRunRequest{RunId: "run-1", Role: "grader", Access: &brainapiv1.Access{Kind: &brainapiv1.Access_None_{None: &brainapiv1.Access_None{}}}}
	if err := c.StartRun(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// The brain answers AlreadyExists for a run it has: idempotent success.
	f.Runs.SetStartStatus(codes.AlreadyExists)
	if err := c.StartRun(context.Background(), req); err != nil {
		t.Fatalf("AlreadyExists is success: %v", err)
	}
	f.Runs.SetStartStatus(codes.InvalidArgument)
	if err := c.StartRun(context.Background(), req); brain.Code(err) != codes.InvalidArgument {
		t.Fatalf("a refusal keeps its code: %v", err)
	}
}

func TestControlWithACertificateOfAnotherCAIsRefused(t *testing.T) {
	f := brainfake.New()
	t.Cleanup(f.Close)
	other := brainfake.New()
	t.Cleanup(other.Close)
	key, _ := agentcrypto.NewClientKey()
	out, err := brain.Enroll(context.Background(), other.Endpoint(), other.CAPEM, brainfake.Token, key.CSRPEM)
	if err != nil {
		t.Fatal(err)
	}
	c, err := brain.NewControl(f.GRPCEndpoint(), []byte(out.CertPEM), []byte(key.KeyPEM), f.CAPEM)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	err = c.StartRun(context.Background(), &brainapiv1.StartRunRequest{RunId: "x"})
	if err == nil || len(f.Runs.Started()) != 0 {
		t.Fatalf("a foreign certificate must not reach the brain: %v", err)
	}
}

func TestStopRunTreatsNotFoundAsSuccess(t *testing.T) {
	f := brainfake.New()
	t.Cleanup(f.Close)
	c := control(t, f)
	f.Runs.SetStopStatus(codes.NotFound)
	if err := c.StopRun(context.Background(), "run-1"); err != nil {
		t.Fatalf("NotFound = already stopped: %v", err)
	}
	f.Runs.SetStopStatus(codes.Unavailable)
	if err := c.StopRun(context.Background(), "run-1"); brain.Code(err) != codes.Unavailable {
		t.Fatalf("%v", err)
	}
}

func TestUploadMaterialsSendsTheMetaFirstAndTheFilesInChunksOfAtMostOneMiB(t *testing.T) {
	f := brainfake.New()
	t.Cleanup(f.Close)
	c := control(t, f)
	big := bytes.Repeat([]byte("a"), 2*brain.MaxChunkBytes+17)
	files := []brain.File{{Name: "big.bin", ContentType: "application/octet-stream", Data: big}, {Name: "empty.txt", ContentType: "text/plain"}, {Name: "notes.txt", ContentType: "text/plain", Data: []byte("hi")}}
	if err := c.UploadMaterials(context.Background(), "run-1", []byte(`{"title":"t"}`), files); err != nil {
		t.Fatal(err)
	}
	got := f.Runs.Materials("run-1")
	if len(got) != 1 {
		t.Fatalf("%d uploads", len(got))
	}
	up := got[0]
	payload := append(append([]byte(nil), big...), []byte("hi")...)
	sum := sha256.Sum256(payload)
	if string(up.Meta.GetTaskJson()) != `{"title":"t"}` || up.Meta.GetTotalSize() != uint64(len(payload)) || up.Meta.GetSha256() != hex.EncodeToString(sum[:]) {
		t.Fatalf("meta: %v", up.Meta)
	}
	if len(up.Files) != 3 || !bytes.Equal(up.Files[0].Data, big) || len(up.Files[1].Data) != 0 || string(up.Files[2].Data) != "hi" || up.Files[1].Name != "empty.txt" {
		t.Fatalf("files: %+v", up.Files)
	}
	if up.MaxChunk > brain.MaxChunkBytes || up.Chunks != 5 {
		t.Fatalf("three chunks of the big file, one of each other, none above 1 MiB: %d chunks, max %d", up.Chunks, up.MaxChunk)
	}
	f.Runs.FailMaterials(codes.AlreadyExists)
	if err := c.UploadMaterials(context.Background(), "run-1", []byte("{}"), nil); err != nil {
		t.Fatalf("AlreadyExists = the brain has them: %v", err)
	}
}

func TestUploadTaskArchiveSendsSizeAndHashThenTheBytesAndARepeatIsDelivered(t *testing.T) {
	f := brainfake.New()
	t.Cleanup(f.Close)
	c := control(t, f)
	archive := bytes.Repeat([]byte("zip"), brain.MaxChunkBytes)
	if err := c.UploadTaskArchive(context.Background(), "req-1", archive); err != nil {
		t.Fatal(err)
	}
	got := f.Archives.Uploaded("req-1")
	sum := sha256.Sum256(archive)
	if got == nil || !bytes.Equal(got.Data, archive) || got.Meta.GetSize() != uint64(len(archive)) || got.Meta.GetSha256() != hex.EncodeToString(sum[:]) || got.MaxChunk > brain.MaxChunkBytes {
		t.Fatalf("%+v", got)
	}
	if err := c.UploadTaskArchive(context.Background(), "req-1", archive); err != nil {
		t.Fatalf("a repeat (AlreadyExists) is delivered: %v", err)
	}
}

func TestPushDataSplitsTheBatchesUnderTheMessageLimitAndReturnsTheRejections(t *testing.T) {
	f := brainfake.New()
	t.Cleanup(f.Close)
	c := control(t, f)
	item := func(ref string, size int) []byte {
		return []byte(`{"task_ref":"` + ref + `","pad":"` + strings.Repeat("x", size) + `"}`)
	}
	// Three aggregates of 6 MiB do not fit one 16 MiB message: two calls.
	aggregates := [][]byte{item("a", 6<<20), item("b", 6<<20), item("c", 6<<20)}
	f.Data.Reject("b", "unknown task")
	rejected, err := c.PushData(context.Background(), aggregates)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.Data.Calls()
	if len(calls) != 2 || len(calls[0])+len(calls[1]) != 3 {
		t.Fatalf("batches: %d", len(calls))
	}
	if len(rejected) != 1 || rejected[0].GetTaskRef() != "b" || rejected[0].GetReason() != "unknown task" {
		t.Fatalf("rejections: %v", rejected)
	}
	// One aggregate above the limit cannot be sent at all.
	if _, err = c.PushData(context.Background(), [][]byte{item("huge", brain.MaxMessageBytes)}); !errors.Is(err, brain.ErrAggregateTooLarge) {
		t.Fatalf("an aggregate above the limit: %v", err)
	}
}
