package exercise

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	testDeployRepo "github.com/cybericebox/daemon/internal/delivery/repository/testDeployRepo"
	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	"github.com/cybericebox/daemon/internal/model"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
	labAccessModel "github.com/cybericebox/daemon/internal/model/labAccess"
)

// What the assistant's runs need of a test lab: to know when it is ready, to give a run its own WireGuard client (made
// and revoked per run, never the author's), to judge an answer the run submits, and to learn which task it measures. The
// lab itself is made and removed by DeployVariantTest and DestroyDeployTest, as for any test lab.

// LabClient is a WireGuard client of a run in the lab group of a test lab's owner.
type LabClient struct {
	Name     string
	DeployID uuid.UUID
}

// LabClientSource lists the extra clients of an owner's lab group (the runs that are active). The group policy of a test
// lab names the author's client and these; whoever rewrites the policy must keep them, or a run loses its lab.
type LabClientSource func(ctx context.Context, owner uuid.UUID) []LabClient

// SetLabClientSource wires the source of the run clients after construction; nil: the group has only the author's client.
func (u *ExerciseUseCase) SetLabClientSource(source LabClientSource) { u.labClients = source }

// reconcileTestGroup applies the group policy of a test lab: the author's policy plus the clients of the active runs,
// except those whose lab is skipLab (it is being removed, so a policy that names it would be refused).
func (u *ExerciseUseCase) reconcileTestGroup(ctx context.Context, deploy exerciseModel.TestDeploy, skipLab uuid.UUID, author labAccessModel.ClientPolicy) error {
	return u.infra.ReconcileLabGroupAccess(ctx, deploy.GroupName, u.groupPolicies(ctx, deploy.CreatedBy, skipLab, "", author))
}

func (u *ExerciseUseCase) groupPolicies(ctx context.Context, owner, skipLab uuid.UUID, skipClient string, author labAccessModel.ClientPolicy) []labAccessModel.ClientPolicy {
	policies := []labAccessModel.ClientPolicy{author}
	if u.labClients == nil {
		return policies
	}
	for _, c := range u.labClients(ctx, owner) {
		if c.Name == "" || c.Name == skipClient || c.DeployID == skipLab {
			continue
		}
		policies = append(policies, labAccessModel.ClientPolicy{Name: c.Name, AllowedLabs: []string{testLabName(c.DeployID)}})
	}
	return policies
}

// LabState is what a run needs to know of its test lab.
type LabState struct {
	// Ready: the lab is up and can be reached.
	Ready bool
	// Failed: the agent could not start the lab.
	Failed bool
	// Gone: the lab was removed, expired or never existed.
	Gone bool
}

// TestLabState reads the state of the owner's test lab. A lab that does not exist (any more) is Gone, not an error.
func (u *ExerciseUseCase) TestLabState(ctx context.Context, owner, deployID uuid.UUID) (LabState, error) {
	status, deploy, err := u.deployTestStatus(ctx, owner, deployID)
	if err != nil {
		if errors.Is(err, exerciseModel.ErrTestDeployNotFound.Err()) {
			return LabState{Gone: true}, nil
		}
		return LabState{}, err
	}
	switch {
	case status.Expired || testDeployExpired(deploy, u.timeNow()) || status.Phase == exerciseModel.DeployPhaseRemoving:
		return LabState{Gone: true}, nil
	case status.Phase == exerciseModel.DeployPhaseFailed:
		return LabState{Failed: true}, nil
	}
	return LabState{Ready: status.Ready}, nil
}

// MintLabClient makes a WireGuard client of its own in the group of the owner's test lab, lets it reach that lab only, and
// returns its complete configuration (the key is in it: it is given to the run and never stored here). The client is
// named by the caller and revoked with RevokeLabClient. The caller records the name first, so a crash leaves it known.
func (u *ExerciseUseCase) MintLabClient(ctx context.Context, owner, deployID uuid.UUID, client string) (string, error) {
	deploy, err := u.ownedLab(ctx, owner, deployID)
	if err != nil {
		return "", err
	}
	var config string
	err = u.underOwnerLock(ctx, owner, func(ctx context.Context, repo *testDeployRepo.Repository) (bool, error) {
		// EnsureLabClient replaces a client of that name left by an earlier try, so its config is given again.
		made, err := u.infra.EnsureLabClient(ctx, deploy.GroupName, client)
		if err != nil {
			return false, err
		}
		if made == "" || strings.Contains(made, wgPrivateKeyPlaceholder) {
			return false, errors.New("the laboratory agent returned a VPN config without the private key")
		}
		config = made
		return false, u.refreshTestGroupAccess(ctx, repo, deploy, "")
	})
	if err != nil {
		return "", model.ErrPlatform.WithError(err).WithMessage("Failed to make the run's VPN client").Err()
	}
	return config, nil
}

// RevokeLabClient removes the run's WireGuard client (a client that is gone already is fine) and takes it out of the group
// policy. The removal is what matters: a policy that cannot be rewritten (the group is already gone) is only logged.
func (u *ExerciseUseCase) RevokeLabClient(ctx context.Context, owner, deployID uuid.UUID, client string) error {
	if u.infra == nil || client == "" {
		return nil
	}
	deploy, err := u.testDeploys.GetOwned(ctx, deployID, owner)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			// The lab row is gone, so its group is going or gone with every client in it.
			return nil
		}
		return model.ErrPlatform.WithError(err).WithMessage("Failed to load test deploy").Err()
	}
	if deploy.IsStatic() {
		return nil
	}
	if err = u.infra.DeleteLabClient(ctx, deploy.GroupName, client); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to revoke the run's VPN client").Err()
	}
	if err = u.underOwnerLock(ctx, owner, func(ctx context.Context, repo *testDeployRepo.Repository) (bool, error) {
		return false, u.refreshTestGroupAccess(ctx, repo, deploy, client)
	}); err != nil {
		log.Warn().Err(err).Str("lab_group", deploy.GroupName).Msg("The group policy was not rewritten after a run's client was revoked")
	}
	return nil
}

// refreshTestGroupAccess rewrites the group policy from the owner's active labs and the runs' clients (the owner's lock is held).
func (u *ExerciseUseCase) refreshTestGroupAccess(ctx context.Context, repo *testDeployRepo.Repository, deploy exerciseModel.TestDeploy, skipClient string) error {
	all, err := repo.ListOwned(ctx, deploy.CreatedBy)
	if err != nil {
		return err
	}
	active := u.activeLabs(sameTestGroup(all, deploy.GroupName), u.timeNow())
	author := labAccessModel.ClientPolicy{Name: testClientName(deploy.CreatedBy), AllowedLabs: labNames(active, uuid.Nil)}
	return u.infra.ReconcileLabGroupAccess(ctx, deploy.GroupName, u.groupPolicies(ctx, deploy.CreatedBy, uuid.Nil, skipClient, author))
}

// ownedLab loads the owner's live lab test deploy for a run.
func (u *ExerciseUseCase) ownedLab(ctx context.Context, owner, deployID uuid.UUID) (exerciseModel.TestDeploy, error) {
	if u.infra == nil {
		return exerciseModel.TestDeploy{}, infraModel.ErrInfrastructureUnavailable.Err()
	}
	deploy, err := u.testDeploys.GetOwned(ctx, deployID, owner)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return deploy, exerciseModel.ErrTestDeployNotFound.Err()
		}
		return deploy, model.ErrPlatform.WithError(err).WithMessage("Failed to load test deploy").Err()
	}
	if err = requireLab(deploy); err != nil {
		return deploy, err
	}
	if testDeployExpired(deploy, u.timeNow()) {
		return deploy, exerciseModel.ErrTestDeployNotFound.Err()
	}
	return deploy, nil
}

// JudgeAnswer checks an answer a run submitted for a subtask of the variant the owner's test lab was made from, with the
// checker the author's own flag checks use. The subtask is named by its key, or by its id when it has no key. A subtask
// that is unknown, or has no answer to give (a state of the lab), is simply not correct. The expected value never leaves.
func (u *ExerciseUseCase) JudgeAnswer(ctx context.Context, owner, deployID uuid.UUID, subtaskRef, value string) (bool, error) {
	deploy, err := u.testDeploys.GetOwned(ctx, deployID, owner)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return false, exerciseModel.ErrTestDeployNotFound.Err()
		}
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to load test deploy").Err()
	}
	_, variant, _, err := u.loadVersionVariant(ctx, deploy.VersionID, deploy.VariantID)
	if err != nil {
		return false, err
	}
	for _, task := range variant.Tasks {
		if task.ID.String() == subtaskRef || (task.Key != "" && task.Key == subtaskRef) {
			return u.CheckTestFlag(ctx, owner, deployID, task.ID, value)
		}
	}
	return false, nil
}

// RunTarget is the task a run measures, as the statistics name it: the exercise, the version, and the variants of the
// version as JSON (the fingerprint of its device images is taken from them).
type RunTarget struct {
	ExerciseID uuid.UUID
	VersionID  uuid.UUID
	VariantsJS []byte
	// HasLab: the variant has devices, so a test of it is a lab a run can join (a variant without devices has none).
	HasLab bool
	// Lease is the run length to ask for a lab that lasts at least the wanted time (the longest allowed when none does).
	Lease time.Duration
}

// RunTarget reads the version a variant belongs to and picks the lab length for a run of the wanted time.
func (u *ExerciseUseCase) RunTarget(ctx context.Context, versionID, variantID uuid.UUID, want time.Duration) (RunTarget, error) {
	exerciseID, variant, _, err := u.loadVersionVariant(ctx, versionID, variantID)
	if err != nil {
		return RunTarget{}, err
	}
	version, err := u.exercises.GetVersion(ctx, versionID)
	if err != nil {
		return RunTarget{}, exerciseModel.ErrExerciseVersionNotFound.Err()
	}
	raw, err := json.Marshal(version.Variants)
	if err != nil {
		return RunTarget{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the version").Err()
	}
	allowed, _ := u.TestRunLengths(ctx)
	lease := pickLease(allowed, want)
	return RunTarget{ExerciseID: exerciseID, VersionID: versionID, VariantsJS: raw, HasLab: len(variant.Topology.Devices) > 0, Lease: lease}, nil
}

// pickLease is the shortest allowed length (in minutes) that is at least want, else the longest allowed; zero when none is
// allowed (the default lease is then used).
func pickLease(allowedMinutes []int, want time.Duration) time.Duration {
	var enough, longest time.Duration
	for _, minutes := range allowedMinutes {
		length := time.Duration(minutes) * time.Minute
		longest = max(longest, length)
		if length >= want && (enough == 0 || length < enough) {
			enough = length
		}
	}
	if enough > 0 {
		return enough
	}
	return longest
}
