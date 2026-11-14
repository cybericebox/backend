package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// PostJSON sends one JSON body to the brain and discards the answer body; a non-2xx answer is an *APIError.
func (c *Client) PostJSON(ctx context.Context, path string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, Request{
		Method: http.MethodPost, Path: path, Header: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}},
		Body: bytes.NewReader(raw), ContentLength: int64(len(raw)),
	})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Stream opens the event stream at path resumed after a sequence number and calls fn for every frame until the stream
// ends (nil), fn fails (its error) or the read breaks. The caller decides what a clean end means: a closed stream is
// not the end of a run.
func (c *Client) Stream(ctx context.Context, path string, after int64, fn func(Frame) error) error {
	resp, err := c.Do(ctx, Request{
		Method: http.MethodGet, Path: path, Query: url.Values{"after": {strconv.FormatInt(after, 10)}},
		Header: http.Header{"Accept": {"text/event-stream"}},
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return ReadFrames(resp.Body, fn)
}
