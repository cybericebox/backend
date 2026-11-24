package assistant

import (
	"context"
	"testing"

	brainapiv1 "github.com/cybericebox/backend/pkg/brainapi/v1"
)

// The brain sends a request again on the stream that replaced the one the answer went up (and was lost) on, while the
// first handling has not freed its slot yet: that resend is skipped as a duplicate, so the handling has to answer again on
// the new stream, or nothing ever answers the request.
func TestAResendOnANewerStreamThanTheAnswerWentUpOnMakesTheHandlerAnswerAgain(t *testing.T) {
	var o outboxStream
	first, second := new(int), new(int)
	send := func(*brainapiv1.RequestAnswer) error { return nil }
	o.openAs(first, send, 4)
	if dup, err := o.acquire(context.Background(), "r1", first); dup || err != nil {
		t.Fatalf("first handling: %v %v", dup, err)
	}
	_, sentOn, err := o.sendAnswer(&brainapiv1.RequestAnswer{RequestId: "r1"})
	if err != nil || sentOn != first {
		t.Fatalf("the answer goes up the first stream: %v %v", sentOn, err)
	}
	// the stream is replaced and the brain sends the request again, before the handling is over
	o.close(first)
	o.openAs(second, send, 4)
	if dup, err := o.acquire(context.Background(), "r1", second); !dup || err != nil {
		t.Fatalf("a request being handled is a duplicate: %v %v", dup, err)
	}
	if !o.finish("r1", sentOn, false) {
		t.Fatal("the answer went up a stream the brain has left: answer again")
	}
	_, sentOn, err = o.sendAnswer(&brainapiv1.RequestAnswer{RequestId: "r1"})
	if err != nil || sentOn != second {
		t.Fatalf("the second answer goes up the new stream: %v %v", sentOn, err)
	}
	if o.finish("r1", sentOn, false) {
		t.Fatal("answered on the stream of the resend: done")
	}
	if dup, _ := o.acquire(context.Background(), "r1", second); dup {
		t.Fatal("the slot is free after the handling is done")
	}
}

// A resend on the very stream the answer went up changes nothing: one answer.
func TestAResendOnTheStreamOfTheAnswerIsNotAnsweredTwice(t *testing.T) {
	var o outboxStream
	s := new(int)
	o.openAs(s, func(*brainapiv1.RequestAnswer) error { return nil }, 4)
	if _, err := o.acquire(context.Background(), "r1", s); err != nil {
		t.Fatal(err)
	}
	_, sentOn, _ := o.sendAnswer(&brainapiv1.RequestAnswer{RequestId: "r1"})
	if dup, _ := o.acquire(context.Background(), "r1", s); !dup {
		t.Fatal("duplicate")
	}
	if o.finish("r1", sentOn, false) {
		t.Fatal("no second answer on the same stream")
	}
}

// A failed answer with a resend skipped meanwhile is tried again: nothing else will answer it.
func TestAFailedAnswerWithASkippedResendIsTriedAgain(t *testing.T) {
	var o outboxStream
	s := new(int)
	o.openAs(s, func(*brainapiv1.RequestAnswer) error { return nil }, 4)
	if _, err := o.acquire(context.Background(), "r1", s); err != nil {
		t.Fatal(err)
	}
	if dup, _ := o.acquire(context.Background(), "r1", s); !dup {
		t.Fatal("duplicate")
	}
	if !o.finish("r1", nil, true) {
		t.Fatal("the answer failed and the resend was skipped: try again")
	}
	if o.finish("r1", nil, true) {
		t.Fatal("tried once more only when another resend was skipped")
	}
}
