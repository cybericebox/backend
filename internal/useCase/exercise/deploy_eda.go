package exercise

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/model"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
	"github.com/cybericebox/daemon/pkg/labaccess"
)

// EDAAddress is where an automated actor reaches a device out of band: the address of the device's grpc access (the host the
// laboratory's proxy serves it on). It carries no credential: the actor gets a fresh handoff for it when it asks (TunnelHandoff).
// With an empty device the lab's one device with a grpc access is meant (the tools device of a run's lab).
func (u *ExerciseUseCase) EDAAddress(ctx context.Context, owner, deployID uuid.UUID, device string) (string, error) {
	_, address, err := u.edaDevice(ctx, owner, deployID, device)
	return address, err
}

func (u *ExerciseUseCase) edaDevice(ctx context.Context, owner, deployID uuid.UUID, device string) (exerciseModel.TestDeploy, string, error) {
	deploy, err := u.ownedLab(ctx, owner, deployID)
	if err != nil {
		return deploy, "", err
	}
	status, err := u.infra.LabStatus(ctx, deploy.GroupName, deploy.LabName)
	if err != nil {
		return deploy, "", model.ErrPlatform.WithError(err).WithMessage("Failed to read deploy status").Err()
	}
	if device == "" {
		device, _ = status.GRPCDevice()
	}
	address, found := status.EntryURL(device, exerciseModel.AccessGRPC, 0)
	if !found {
		return deploy, "", exerciseModel.ErrTestDeployNoGRPC.Err()
	}
	return deploy, address, nil
}

// EDAHandoff is a handoff for the proxy's tunnel to a device's grpc access: the address, the signed handoff (60 s) and when the
// handoff ends. The handoff is a secret: it is handed to the one who asked and never logged.
type EDAHandoff struct {
	URL       string
	Token     string
	ExpiresAt time.Time
}

// TunnelHandoff signs a fresh handoff for the tunnel to the grpc access of the owner's test lab's device, through the same
// path as the handoff links of the browser: the host claim is the full host of the device's access, the claim use is tunnel,
// the subject is the author's own client of the group (a bot is no lab client; the group policy that lets the author reach
// their lab lets the tunnel in). The session it opens lasts until `until` (the end of the run's window plus a grace).
func (u *ExerciseUseCase) TunnelHandoff(ctx context.Context, owner, deployID uuid.UUID, device string, until time.Time) (EDAHandoff, error) {
	deploy, address, err := u.edaDevice(ctx, owner, deployID, device)
	if err != nil {
		return EDAHandoff{}, err
	}
	if u.sessions == nil {
		return EDAHandoff{}, infraModel.ErrInfrastructureUnavailable.Err()
	}
	link, err := u.sessions.Issue(ctx, labaccess.Session{Group: deploy.GroupName, Client: testClientName(owner), AccessURL: address, Use: labaccess.UseTunnel, ExpiresAt: until}, u.timeNow())
	if err != nil {
		return EDAHandoff{}, model.ErrPlatform.WithError(err).WithMessage("Failed to sign the tunnel handoff").Err()
	}
	return EDAHandoff{URL: address, Token: link.Token, ExpiresAt: link.TokenExpiresAt}, nil
}
