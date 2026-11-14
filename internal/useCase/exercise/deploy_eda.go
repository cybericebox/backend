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

// IBearerSessions signs the bearer token of a run for one device's grpc access (satisfied by the same session issuer that
// signs the handoff links of ITestSessions, with the same tenant access key).
type IBearerSessions interface {
	IssueBearer(ctx context.Context, b labaccess.Bearer, now time.Time) (labaccess.BearerToken, error)
}

// EDAAccess is how an automated actor reaches a device out of band: the address of the device's grpc access (through the
// laboratory's proxy) and the bearer token to present there. The token is a secret: it is handed to the run and never logged.
type EDAAccess struct {
	URL       string
	Token     string
	ExpiresAt time.Time
}

// MintEDAAccess gives a run the grpc access of one device of the owner's test lab. The address is the one the laboratory
// reports for the device's grpc entry in the lab status; the token names the lab group, the device's host, the per-run client
// and the run, and ends at `until` (the end of the run's window plus a grace).
func (u *ExerciseUseCase) MintEDAAccess(ctx context.Context, owner, deployID uuid.UUID, device, client, run string, until time.Time) (EDAAccess, error) {
	deploy, err := u.ownedLab(ctx, owner, deployID)
	if err != nil {
		return EDAAccess{}, err
	}
	signer, ok := u.sessions.(IBearerSessions)
	if !ok || u.sessions == nil {
		return EDAAccess{}, infraModel.ErrInfrastructureUnavailable.Err()
	}
	status, err := u.infra.LabStatus(ctx, deploy.GroupName, deploy.LabName)
	if err != nil {
		return EDAAccess{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read deploy status").Err()
	}
	address, found := status.EntryURL(device, exerciseModel.AccessGRPC, 0)
	if !found {
		return EDAAccess{}, exerciseModel.ErrTestDeployNoGRPC.Err()
	}
	token, err := signer.IssueBearer(ctx, labaccess.Bearer{Group: deploy.GroupName, Client: client, AccessURL: address, Run: run, ExpiresAt: until}, u.timeNow())
	if err != nil {
		return EDAAccess{}, model.ErrPlatform.WithError(err).WithMessage("Failed to sign the run's access token").Err()
	}
	return EDAAccess{URL: token.URL, Token: token.Token, ExpiresAt: token.ExpiresAt}, nil
}
