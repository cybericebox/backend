package assistant

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
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

// provideLab answers provide_lab: a test lab of a task pinned to a version and an image, deployed as the user the subject
// belongs to, under that user's own limits and with the same lease as their test labs. The result is the lab and a
// WireGuard client of its own (a participant's access, revoked when the lab is gone). A lab from a draft is not provided
// yet. The brain is a trusted tenant: the task is named by it and not authorized against the user.
func (u *UseCase) provideLab(ctx context.Context, req OutboxRequest) (any, *AnswerError) {
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
	if u.runsReady() != nil {
		return nil, &AnswerError{Code: codeUnavailable, Message: "labs are not available"}
	}
	user, found, err := u.runs.UserOfSubject(ctx, subject)
	if err != nil {
		log.Error().Err(err).Msg("Reading the user of a subject failed")
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	}
	if !found {
		return nil, &AnswerError{Code: codeUnknownSubject, Message: "no user belongs to this subject"}
	}
	target, err := u.labs.RunTarget(ctx, version, uuid.Nil, 0)
	if err != nil || target.ExerciseID != exercise {
		return nil, &AnswerError{Code: codeTaskNotFound, Message: "no such task version"}
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
	handle, err := u.labs.DeployVariantTest(ctx, user, version, target.VariantID, 0)
	if err != nil {
		log.Warn().Err(err).Msg("A lab the brain asked for was refused")
		return nil, &AnswerError{Code: codeLabUnavailable, Message: "the lab could not be provided"}
	}
	deployID, err := uuid.FromString(handle.Group)
	if err != nil {
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	}
	now := u.now().UTC()
	lab := assistantModel.Run{
		ID: uuid.Must(uuid.NewV7()), ExerciseID: exercise, VersionID: version, VariantID: target.VariantID, DeployID: deployID, Actor: user,
		Role: assistantModel.RoleLab, State: assistantModel.RunStarting, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(labLeaseCap),
	}
	if err = u.runs.Create(ctx, lab, now); err != nil {
		u.dropLab(ctx, user, deployID)
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	}
	access, refusal := u.readyLabAccess(ctx, lab)
	if refusal != nil {
		// The row is closed as failed: the leader's driver revokes the client and destroys the lab.
		if _, closeErr := u.runs.Close(ctx, lab.ID, assistantModel.RunFailed, refusal.Code, u.now().UTC()); closeErr != nil {
			log.Error().Err(closeErr).Msg("Closing a refused lab failed")
		}
		return nil, refusal
	}
	return map[string]any{"device_ref": deployID.String(), "access": access}, nil
}

// readyLabAccess waits for the lab and makes the client the brain joins it with.
func (u *UseCase) readyLabAccess(ctx context.Context, lab assistantModel.Run) (assistantModel.Access, *AnswerError) {
	deadline := u.now().Add(labReadyTimeout)
	for {
		state, err := u.labs.TestLabState(ctx, lab.Actor, lab.DeployID)
		switch {
		case err != nil:
			log.Warn().Err(err).Msg("Reading the lab the brain asked for failed")
		case state.Gone || state.Failed:
			return nil, &AnswerError{Code: codeLabUnavailable, Message: "the lab did not start"}
		case state.Ready:
			return u.mintLabAccess(ctx, lab)
		}
		if !u.now().Before(deadline) {
			return nil, &AnswerError{Code: codeLabNotReady, Message: "the lab was not ready in time"}
		}
		if !sleep(ctx, u.runPoll) {
			return nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
		}
	}
}

func (u *UseCase) mintLabAccess(ctx context.Context, lab assistantModel.Run) (assistantModel.Access, *AnswerError) {
	client := runClientName(lab.ID)
	// Recorded before it is made, so a crash leaves a client that is known and gets revoked.
	if err := u.runs.SetAccessClient(ctx, lab.ID, client, u.now().UTC()); err != nil {
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	}
	config, err := u.labs.MintLabClient(ctx, lab.Actor, lab.DeployID, client)
	if err != nil {
		log.Warn().Err(err).Msg("Making the VPN client of a lab the brain asked for failed")
		return nil, &AnswerError{Code: codeLabUnavailable, Message: "the lab's access could not be made"}
	}
	if _, err = u.runs.SetRunning(ctx, lab.ID, u.now().UTC()); err != nil {
		return nil, &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	}
	return assistantModel.VPNAccess(config), nil
}
