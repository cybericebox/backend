package brain

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The error model of the brain's control plane (brain.proto): the platform maps on the gRPC code and on the stable machine
// reason of google.rpc.ErrorInfo, never on the message. AlreadyExists is an idempotent success where the contract says so (the
// callers below decide); only Unavailable, ResourceExhausted (with its RetryInfo) and a failure of the network are retried.

// Code is the gRPC code of an error: OK for nil, Unknown for an error that carries no status.
func Code(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

// Reason is the machine reason of the error's google.rpc.ErrorInfo ("" when it has none).
func Reason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, isInfo := d.(*errdetails.ErrorInfo); isInfo {
			return info.GetReason()
		}
	}
	return ""
}

// RetryDelay is the delay the brain asked for in the error's google.rpc.RetryInfo.
func RetryDelay(err error) (time.Duration, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return 0, false
	}
	for _, d := range st.Details() {
		if info, isInfo := d.(*errdetails.RetryInfo); isInfo && info.GetRetryDelay() != nil {
			return info.GetRetryDelay().AsDuration(), true
		}
	}
	return 0, false
}

// noRetryErr marks an error Retry must not try again, whatever its code (the caller decides about it itself).
type noRetryErr struct{ error }

func (e noRetryErr) Unwrap() error { return e.error }

// GRPCStatus keeps the status of the wrapped error visible to status.FromError and Code.
func (e noRetryErr) GRPCStatus() *status.Status {
	st, _ := status.FromError(e.error)
	return st
}

// NoRetry marks err so that Retry returns it at once (nil stays nil).
func NoRetry(err error) error {
	if err == nil {
		return nil
	}
	return noRetryErr{err}
}

// Retryable says a call may be tried again: the brain is unavailable or rate limits the platform, or the network failed (no
// status, or our own deadline ran out before an answer). A cancelled context is never retried.
func Retryable(err error) bool {
	var marked noRetryErr
	if err == nil || errors.Is(err, context.Canceled) || errors.As(err, &marked) {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		// No status: the call never reached the brain or its answer was lost (a reset, our own deadline before the headers).
		return true
	}
	switch st.Code() {
	case codes.Unavailable, codes.ResourceExhausted, codes.DeadlineExceeded:
		return true
	}
	return false
}

// Retry calls fn up to attempts times while its error is Retryable, with a doubling pause (±20 % jitter) that a RetryInfo of the
// brain replaces. It returns the last error, or ctx's when ctx ends first.
func Retry(ctx context.Context, attempts int, pause time.Duration, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(ctx); err == nil || !Retryable(err) || attempt >= attempts {
			var marked noRetryErr
			if errors.As(err, &marked) {
				return marked.error
			}
			return err
		}
		wait := time.Duration(float64(pause) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // jitter, not a secret
		if asked, ok := RetryDelay(err); ok {
			wait = asked
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(wait):
		}
		pause *= 2
	}
}
