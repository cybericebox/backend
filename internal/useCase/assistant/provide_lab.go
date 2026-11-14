package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	userModel "github.com/cybericebox/daemon/internal/model/user"
	exerciseUseCase "github.com/cybericebox/daemon/internal/useCase/exercise"
	pkgerr "github.com/cybericebox/daemon/pkg/err"
)

// More refusals of an outbox request, next to the ones in outbox.go.
const (
	codeUnknownSubject = "unknown_subject"
	codeTaskNotFound   = "task_not_found"
	codeImageMismatch  = "image_mismatch"
	codeNoLab          = "no_lab"
	codeLabUnavailable = "lab_unavailable"
	codeLabNotReady    = "lab_not_ready"
	codeUnavailable    = "unavailable"
	codeForbidden      = "forbidden"
)

// labLeaseCap bounds how long a lab the brain asked for is followed; the lab's own lease ends it much sooner.
const labLeaseCap = 24 * time.Hour

type provideLabRequest struct {
	Subject string `json:"subject"`
	Source  struct {
		Draft json.RawMessage `json:"draft"`
		Task  *struct {
			TaskRef string `json:"task_ref"`
			Version string `json:"version"`
			Image   string `json:"image"`
		} `json:"task"`
	} `json:"source"`
}

// UserLookup reads a user, with the role the authorization needs.
type UserLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (userModel.User, error)
}

// answerLater is what a handler returns when it keeps the request and answers it itself later: the request is persisted as
// claimed, so the cursor moves on and the stream goes on reading.
var answerLater = &AnswerError{Code: "answer_later"}

// provideLab claims provide_lab: a test lab of a task pinned to a version and an image, deployed as the user the subject
// belongs to, after the same authorization as that user's own test deploy and under their limits and lease. It only starts
// the lab and keeps the request as a lab row; the leader's driver waits for the lab, makes the WireGuard client the brain
// joins it with and answers {device_ref, access:{vpn:{entry}}} (or the refusal if the lab fails). The outbox is never held:
// a request seen again after a restart finds its row and goes on waiting. A lab from a draft is not provided yet.
func (u *UseCase) provideLab(ctx context.Context, req OutboxRequest) (any, *AnswerError) {
	return u.provideTaskLab(ctx, req, false)
}

// provideTaskLab is provideLab, and with tools the lab of provide_device for the tools device: the task's lab with the
// platform tools device in it (role tools; the answer carries the eda access of that device instead of the VPN one).
func (u *UseCase) provideTaskLab(ctx context.Context, req OutboxRequest, tools bool) (any, *AnswerError) {
	var in provideLabRequest
	if err := json.Unmarshal(req.Raw, &in); err != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "the request could not be read"}
	}
	if len(in.Source.Draft) > 0 && string(in.Source.Draft) != "null" {
		return nil, &AnswerError{Code: codeNotSupportedYet, Message: "a lab from a draft is not supported yet"}
	}
	if in.Source.Task == nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "the request names no task"}
	}
	version, versionErr := uuid.FromString(in.Source.Task.Version)
	exercise, exerciseErr := uuid.FromString(in.Source.Task.TaskRef)
	subject, subjectErr := uuid.FromString(in.Subject)
	if versionErr != nil || exerciseErr != nil || subjectErr != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "task_ref, version and subject must be ids"}
	}
	if u.runsReady() != nil || u.users == nil {
		return nil, &AnswerError{Code: codeUnavailable, Message: "labs are not available"}
	}
	internal := &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	if _, claimed, err := u.runs.LabOfRequest(ctx, req.ID); err != nil {
		log.Error().Err(err).Msg("Reading the lab of a request failed")
		return nil, internal
	} else if claimed {
		return nil, answerLater // seen again: its lab is already being waited for
	}
	user, found, err := u.runs.UserOfSubject(ctx, subject)
	if err != nil {
		log.Error().Err(err).Msg("Reading the user of a subject failed")
		return nil, internal
	}
	if !found {
		return nil, &AnswerError{Code: codeUnknownSubject, Message: "no user belongs to this subject"}
	}
	target, err := u.labs.RunTarget(ctx, version, uuid.Nil, 0)
	if err != nil || target.ExerciseID != exercise {
		return nil, &AnswerError{Code: codeTaskNotFound, Message: "no such task version"}
	}
	if refusal := u.authorizeLab(ctx, user, exercise, version, target.VariantID); refusal != nil {
		return nil, refusal
	}
	if image, _ := imageAndKind(target.VariantsJS); image != in.Source.Task.Image {
		return nil, &AnswerError{Code: codeImageMismatch, Message: "the task version has other images than the request names"}
	}
	if !target.HasLab {
		return nil, &AnswerError{Code: codeNoLab, Message: "the task has no lab"}
	}
	n, err := u.runs.CountActiveOf(ctx, user)
	if err != nil || n >= u.maxActive {
		return nil, &AnswerError{Code: codeLabUnavailable, Message: "the user has too many labs or runs"}
	}
	rowID := uuid.Must(uuid.NewV7())
	role := assistantModel.RoleLab
	var handle exerciseModel.DeployHandle
	if tools {
		role = assistantModel.RoleTools
		ca, caErr := u.brainCA(ctx)
		if caErr != nil {
			return nil, &AnswerError{Code: codeUnavailable, Message: "labs are not available"}
		}
		handle, err = u.labs.DeployVariantTestWithTools(ctx, user, version, target.VariantID, 0, exerciseModel.ToolsPlacement{Env: exerciseModel.EDAEnv(rowID.String(), ca)})
	} else {
		handle, err = u.labs.DeployVariantTest(ctx, user, version, target.VariantID, 0)
	}
	if errors.Is(err, exerciseModel.ErrToolsNoImage) || errors.Is(err, exerciseModel.ErrToolsNoVPN) {
		return nil, &AnswerError{Code: codeNotSupportedYet, Message: "the tools device is not available for this lab"}
	}
	if err != nil {
		log.Warn().Err(err).Msg("A lab the brain asked for was refused")
		return nil, &AnswerError{Code: codeLabUnavailable, Message: "the lab could not be provided"}
	}
	deployID, err := uuid.FromString(handle.Group)
	if err != nil {
		return nil, internal
	}
	now := u.now().UTC()
	lab := assistantModel.Run{
		ID: rowID, ExerciseID: exercise, VersionID: version, VariantID: target.VariantID, DeployID: deployID, Actor: user,
		Role: role, RequestID: req.ID, State: assistantModel.RunStarting, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(labLeaseCap),
	}
	if err = u.runs.Create(ctx, lab, now); err != nil {
		u.dropLab(ctx, user, deployID)
		if _, claimed, readErr := u.runs.LabOfRequest(ctx, req.ID); readErr == nil && claimed {
			return nil, answerLater // another delivery of the same request claimed it first
		}
		return nil, internal
	}
	return nil, answerLater
}

// authorizeLab applies the policy of the user's own test deploy of the task. A refusal (or a task the user may not see) is
// forbidden; a failure of the platform is not.
func (u *UseCase) authorizeLab(ctx context.Context, user, exercise, version, variant uuid.UUID) *AnswerError {
	forbidden := &AnswerError{Code: codeForbidden, Message: "the user may not run a lab of this task"}
	account, err := u.users.GetByID(ctx, user)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return forbidden
		}
		log.Error().Err(err).Msg("Reading the user of a lab request failed")
		return &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	}
	if account.IsBlocked() || account.IsDeleted() {
		return forbidden
	}
	actor := exerciseUseCase.Actor{UserID: user, Role: account.Role}
	err = u.labs.AuthorizeTestDeploy(ctx, actor, exercise, version, variant)
	if err == nil {
		return nil
	}
	var coded pkgerr.Error
	if errors.As(err, &coded) {
		switch coded.StatusCode().HTTPCode() {
		case http.StatusForbidden, http.StatusNotFound:
			return forbidden
		}
	}
	log.Error().Err(err).Msg("Authorizing a lab request failed")
	return &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
}

// labRefusal is the refusal a lab that did not come up is answered with.
func labRefusal(reason string) *AnswerError {
	if reason == assistantModel.FailureLabNotReady {
		return &AnswerError{Code: codeLabNotReady, Message: "the lab was not ready in time"}
	}
	return &AnswerError{Code: codeLabUnavailable, Message: "the lab did not start"}
}
