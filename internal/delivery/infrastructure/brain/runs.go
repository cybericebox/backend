package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
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

// Part is one part of a multipart upload.
type Part struct {
	// Field is the form field name; Filename and ContentType describe the content.
	Field, Filename, ContentType string
	Data                         []byte
}

// RequestedWith is the non-empty marker header of an upload: the brain's CSRF guard asks for it.
const RequestedWith = "cybericebox-daemon"

// PostMultipart sends the parts as one multipart/form-data body (held in memory: the caller has capped it), with the
// marker header the brain's uploads ask for; a non-2xx answer is an *APIError.
func (c *Client) PostMultipart(ctx context.Context, path string, parts []Part) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.Field, p.Filename))
		contentType := p.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		header.Set("Content-Type", contentType)
		part, err := w.CreatePart(header)
		if err != nil {
			return err
		}
		if _, err = part.Write(p.Data); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	resp, err := c.Do(ctx, Request{
		Method: http.MethodPost, Path: path, Body: bytes.NewReader(body.Bytes()), ContentLength: int64(body.Len()),
		Header: http.Header{"Content-Type": {w.FormDataContentType()}, "Accept": {"application/json"}, "X-Requested-With": {RequestedWith}},
	})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
