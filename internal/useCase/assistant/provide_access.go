package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
)

// The refusals of provide_access.
const (
	// codeRunNotFound: no run or tools lab of this platform has that id (or it is not one a device is driven in).
	codeRunNotFound = "run_not_found"
	// codeRunEnded: the run or the lab is over (or ending): nothing is driven any more.
	codeRunEnded = "run_ended"
	// codeNoDevice: the lab has no device with a grpc access (any more).
	codeNoDevice = "no_device"
)

// provideAccess answers provide_access {run_id} (or {device_ref} of a tools lab): a fresh handoff, valid for 60 s, that the
// brain exchanges at the proxy for the byte tunnel to the grpc access of the run's tools device. It may be asked for as often
// as the run needs. Only a run or a tools lab of this platform that is still active, whose lab has a device with a grpc access,
// is served; the handoff names that device's host alone. The handoff is a secret: it is in the answer only, never in a log.
func (u *UseCase) provideAccess(ctx context.Context, req OutboxRequest) (any, *AnswerError) {
	var in struct {
		RunID     string `json:"run_id"`
		DeviceRef string `json:"device_ref"`
	}
	if err := json.Unmarshal(req.Raw, &in); err != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "the request could not be read"}
	}
	raw := in.RunID
	if raw == "" {
		raw = in.DeviceRef
	}
	id, err := uuid.FromString(raw)
	if err != nil {
		return nil, &AnswerError{Code: codeInvalidRequest, Message: "run_id or device_ref must be an id"}
	}
	if u.runsReady() != nil {
		return nil, &AnswerError{Code: codeUnavailable, Message: "labs are not available"}
	}
	internal := &AnswerError{Code: codeInternal, Message: "the platform could not handle the request"}
	run, found, err := u.runs.Get(ctx, id)
	if err != nil {
		log.Error().Err(err).Msg("Reading the run of an access request failed")
		return nil, internal
	}
	// A grader run, and a lab made for a VPN, have no device driven out of band.
	if !found || run.Role == assistantModel.RoleGrader || run.Role == assistantModel.RoleLab || (run.Role != assistantModel.RoleTools && run.Opponent == 0) {
		return nil, &AnswerError{Code: codeRunNotFound, Message: "no such run"}
	}
	now := u.now()
	if (run.State != assistantModel.RunStarting && run.State != assistantModel.RunRunning) || !now.Before(run.ExpiresAt) {
		return nil, &AnswerError{Code: codeRunEnded, Message: "the run is over"}
	}
	// The session the handoff opens ends with the run's window (plus the grace); a tools lab has no window of its own but the
	// longest a run lasts.
	until := run.ExpiresAt
	if run.Role == assistantModel.RoleTools {
		until = now.Add(u.runMax)
		if until.After(run.ExpiresAt) {
			until = run.ExpiresAt
		}
	}
	handoff, err := u.labs.TunnelHandoff(ctx, run.Actor, run.DeployID, "", until.Add(u.runGrace))
	switch {
	case err == nil:
	case errors.Is(err, exerciseModel.ErrTestDeployNoGRPC.Err()):
		return nil, &AnswerError{Code: codeNoDevice, Message: "the lab has no device to drive"}
	case errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()):
		return nil, &AnswerError{Code: codeRunEnded, Message: "the run is over"}
	default:
		log.Error().Err(err).Msg("Signing a tunnel handoff failed")
		return nil, internal
	}
	return map[string]any{"handoff": handoff.Token, "url": handoff.URL, "expires_at": handoff.ExpiresAt.UTC().Format(time.RFC3339)}, nil
}
