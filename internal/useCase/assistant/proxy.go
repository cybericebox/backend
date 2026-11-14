package assistant

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/daemon/internal/delivery/infrastructure/brain"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
)

// SubjectHeader names the opaque user the brain binds a chat session to.
const SubjectHeader = "X-Cib-Subject"

// Call is one request to the brain on behalf of a signed-in user.
type Call struct {
	Method string
	// Path is the brain path, starting with /v1/assistant/.
	Path   string
	Query  url.Values
	Header http.Header
	Body   io.Reader
	// ContentLength is the body length, -1 when unknown.
	ContentLength int64
}

// Proxy sends the call to the brain as the user and returns the 2xx response with its body open (the caller closes
// it). Every other outcome is a typed error: the platform's own credentials never produce a 401 for the page.
func (u *UseCase) Proxy(ctx context.Context, user uuid.UUID, call Call) (*http.Response, error) {
	subject := u.Subject(user)
	if subject == "" {
		return nil, assistantModel.ErrAssistantUnavailable.Err()
	}
	c, err := u.client(ctx)
	if err != nil {
		return nil, err
	}
	header := call.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Set(SubjectHeader, subject)
	resp, err := c.Do(ctx, brain.Request{
		Method: call.Method, Path: call.Path, Query: call.Query, Header: header, Body: call.Body, ContentLength: call.ContentLength,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return resp, nil
}

// mapError turns a brain failure into the platform's typed error by status, never by the text of the message.
func mapError(err error) error {
	var api *brain.APIError
	switch {
	case errors.As(err, &api):
		return mapAPIError(api)
	case errors.Is(err, brain.ErrUnreachable):
		return assistantModel.ErrAssistantBrainUnreachable.WithError(err).Err()
	default:
		return assistantModel.ErrAssistantUpstream.WithError(err).Err()
	}
}

// mapAPIError maps the status of a brain error envelope. The stable `error` code refines a status only where the
// platform has a meaning for it (none yet in phase 1); a status without a typed meaning is the generic upstream error.
func mapAPIError(e *brain.APIError) error {
	switch e.Status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return assistantModel.ErrAssistantRejected.Err()
	case http.StatusForbidden:
		return assistantModel.ErrAssistantForbidden.Err()
	case http.StatusNotFound:
		return assistantModel.ErrAssistantNotFound.Err()
	case http.StatusConflict:
		return assistantModel.ErrAssistantNotReady.Err()
	case http.StatusGone:
		return assistantModel.ErrAssistantGone.Err()
	}
	// 401 means the brain refused the platform's own certificate or subject: a deployment fault, not a sign-in for the
	// person (which would send them to the sign-in page), so it is an upstream failure like a 5xx or an unknown status.
	log.Warn().Int("status", e.Status).Str("code", e.Code).Msg("Cyber-brain answered with an error that has no typed meaning")
	return assistantModel.ErrAssistantUpstream.WithError(e).Err()
}
