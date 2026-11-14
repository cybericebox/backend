package brain

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// maxFrameLine caps one line of an event stream.
const maxFrameLine = 1 << 20

// Frame is one server-sent event of the brain: `id:` (the sequence number), `event:` (the type) and the `data:` lines
// joined by a newline.
type Frame struct {
	ID    string
	Event string
	Data  []byte
}

// ReadFrames reads an event stream and calls fn for every complete frame that carries data (a comment or a keep-alive
// without data is skipped). It returns nil when the stream ends cleanly, fn's error when fn fails, or the read error.
func ReadFrames(r io.Reader, fn func(Frame) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrameLine)
	var (
		id, event string
		data      [][]byte
	)
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), "\r")
		if len(line) == 0 {
			// Like the SSE standard: a frame whose data is empty is not dispatched.
			if joined := bytes.Join(data, []byte("\n")); len(joined) > 0 {
				if err := fn(Frame{ID: id, Event: event, Data: joined}); err != nil {
					return err
				}
			}
			id, event, data = "", "", nil
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(string(line), ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			id = value
		case "event":
			event = value
		case "data":
			data = append(data, []byte(value))
		}
	}
	return sc.Err()
}
