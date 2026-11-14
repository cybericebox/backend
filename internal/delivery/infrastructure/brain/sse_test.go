package brain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain"
)

func TestReadFramesParsesIDEventAndMultilineData(t *testing.T) {
	stream := ": keep-alive\n\nid: 7\nevent: provide_data\ndata: {\"a\":\ndata: 1}\n\nevent: heartbeat\ndata: {}\n\nid: 9\r\nevent: x\r\ndata: y\r\n\r\ndata\n\nid: 10\nevent: unfinished\ndata: z"
	var got []brain.Frame
	if err := brain.ReadFrames(strings.NewReader(stream), func(f brain.Frame) error { got = append(got, f); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("frames: %+v", got)
	}
	if got[0].ID != "7" || got[0].Event != "provide_data" || string(got[0].Data) != "{\"a\":\n1}" {
		t.Fatalf("first: %+v", got[0])
	}
	if got[1].ID != "" || got[1].Event != "heartbeat" {
		t.Fatalf("a frame without id is passed on for the caller to skip: %+v", got[1])
	}
	if got[2].ID != "9" || got[2].Event != "x" || string(got[2].Data) != "y" {
		t.Fatalf("CRLF frame: %+v", got[2])
	}
}

func TestReadFramesStopsWithTheCallbackError(t *testing.T) {
	boom := errors.New("boom")
	err := brain.ReadFrames(strings.NewReader("data: a\n\ndata: b\n\n"), func(brain.Frame) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
