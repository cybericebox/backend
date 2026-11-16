package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/infrastructure/brain"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
	pkgerr "github.com/cybericebox/backend/pkg/err"
)

// The refusals of provide_task, as the brain's spec names them.
const (
	codeNotFound     = "not_found"
	codeUploadFailed = "upload_failed"
)

// archiveAttempts is how often the upload of a task archive is tried when the brain answers a server error.
const archiveAttempts = 3

// TaskSource gives the import archive of one version of a task to a user who may edit it (the exercise use case).
type TaskSource interface {
	ExportTaskVersionForEdit(ctx context.Context, actor exerciseUseCase.Actor, id, versionID uuid.UUID) ([]byte, error)
}

// provideTask answers provide_task: the brain asks for the source of an existing task so it can edit it (the Teacher's edit
// flow). The daemon delivers the task version's import archive (the admin export's format, one version, secrets masked) as an
// upload to `POST /v1/requests/{request_id}/archive` (multipart, part `archive`), then answers {uploaded: true}. It is
// authorized against the user the subject belongs to like provide_lab: the user must be allowed to edit the task, else
// `forbidden`; an unknown task or a version that is not of that task is `not_found`. Nothing is ever sent unasked.
func (u *UseCase) provideTask(ctx context.Context, req OutboxRequest) (any, *AnswerError) {
	var in struct {
		Subject string `json:"subject"`
		TaskRef string `json:"task_ref"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(req.Raw, &in); err != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "the request could not be read"}
	}
	exercise, exerciseErr := uuid.FromString(in.TaskRef)
	version, versionErr := uuid.FromString(in.Version)
	subject, subjectErr := uuid.FromString(in.Subject)
	if exerciseErr != nil || versionErr != nil || subjectErr != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "task_ref, version and subject must be ids"}
	}
	if u.tasks == nil || u.runs == nil || u.users == nil {
		return nil, &AnswerError{Code: codeUnavailable, Message: "task sources are not available"}
	}
	internal := &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	user, found, err := u.runs.UserOfSubject(ctx, subject)
	if err != nil {
		log.Error().Err(err).Msg("Reading the user of a subject failed")
		return nil, internal
	}
	if !found {
		return nil, &AnswerError{Code: codeUnknownSubject, Message: "no user belongs to this subject"}
	}
	forbidden := &AnswerError{Code: codeForbidden, Message: "the user may not edit this task"}
	account, err := u.users.GetByID(ctx, user)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return nil, forbidden
		}
		log.Error().Err(err).Msg("Reading the user of a task request failed")
		return nil, internal
	}
	if account.IsBlocked() || account.IsDeleted() {
		return nil, forbidden
	}
	archive, err := u.tasks.ExportTaskVersionForEdit(ctx, exerciseUseCase.Actor{UserID: user, Role: account.Role}, exercise, version)
	if err != nil {
		var coded pkgerr.Error
		if errors.As(err, &coded) {
			switch coded.StatusCode().HTTPCode() {
			case http.StatusForbidden:
				return nil, forbidden
			case http.StatusNotFound:
				return nil, &AnswerError{Code: codeNotFound, Message: "no such task version"}
			}
		}
		log.Error().Err(err).Msg("Exporting a task for the brain failed")
		return nil, internal
	}
	name := strings.ToLower(in.TaskRef) + ".cybericebox.zip"
	if err = u.uploadArchive(ctx, req.ID, name, archive); err != nil {
		log.Warn().Err(err).Msg("The task archive was not delivered to the brain")
		return nil, &AnswerError{Code: codeUploadFailed, Message: "the task archive could not be delivered"}
	}
	return map[string]any{"uploaded": true}, nil
}

// uploadArchive sends the archive of a request to the brain. A server error is retried; 409 means the brain has it already (the
// request is seen again after a restart); any other refusal is final.
func (u *UseCase) uploadArchive(ctx context.Context, requestID, name string, archive []byte) error {
	c, err := u.client(ctx)
	if err != nil {
		return err
	}
	parts := []brain.Part{{Field: "archive", Filename: name, ContentType: "application/zip", Data: archive}}
	pause := u.answerPause
	for attempt := 1; ; attempt++ {
		err = c.PostMultipart(ctx, "/v1/requests/"+requestID+"/archive", parts)
		var api *brain.APIError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &api) && api.Status == http.StatusConflict:
			return nil
		case errors.As(err, &api) && api.Status < http.StatusInternalServerError && api.Status != http.StatusTooManyRequests:
			return err
		}
		if attempt >= archiveAttempts || !sleep(ctx, pause) {
			return err
		}
		pause *= 2
	}
}
