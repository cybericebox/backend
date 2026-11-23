package exercise

import (
	"context"
	"slices"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// withDossierFiles gives the tools device of every placement that operates devices (a bot defender: defense or
// attack-defense) the files of its dossier (console design 9a.4): /run/eda/consoles.json, /run/eda/credentials.json and, unless
// the topology mode is none, /run/eda/topology.json, root-only, through its bootstrap. The run's topology (after auto-cabling)
// is what they describe; no other tools device of the run is named in them. A dossier is written only when there is something
// to tell: the variant declares a defender block, or the tools device has console lines (a bot defender of a task without either
// starts as it always did, with no launcher and no read of its image's configuration). An attacker's or a grader's tools device
// gets none. The credentials are opened here only for the files, which reach the laboratory as secret bootstrap material: never
// a row or a log.
func (u *ExerciseUseCase) withDossierFiles(variant exerciseModel.Variant, run exerciseModel.Topology, tools []exerciseModel.Device, places []exerciseModel.ToolsPlacement) (exerciseModel.Topology, error) {
	platform := make(map[uuid.UUID]bool, len(tools))
	for _, d := range tools {
		platform[d.ID] = true
	}
	var credentials []exerciseModel.DefenderCredential
	opened := false
	for i, device := range tools {
		if i >= len(places) || len(places[i].Operates) == 0 {
			continue
		}
		if variant.Defender == nil && len(run.ConsoleLinksOf(device)) == 0 {
			continue
		}
		if !opened {
			var err error
			if credentials, err = u.defenderCredentials(variant); err != nil {
				return exerciseModel.Topology{}, err
			}
			opened = true
		}
		dossier := exerciseModel.BotDossier(run, device, places[i].Operates, variant.Defender, credentials)
		var err error
		if run, err = run.WithDossierFiles(device, dossier, platform); err != nil {
			return exerciseModel.Topology{}, err
		}
	}
	return run, nil
}

// deployedDossier is the human defender's dossier kept with a test deploy (console design 9a.3), as the variant is at the
// deploy: nil for a variant without a defender block, and for a run whose tools device took the seat (the human is not at it).
// The secret credentials stay sealed as stored, to this variant.
func deployedDossier(variant exerciseModel.Variant, tools []exerciseModel.ToolsPlacement) *exerciseModel.DeployedDossier {
	if variant.Defender == nil {
		return nil
	}
	if seat := variant.Defender.Seat; seat != "" && slices.ContainsFunc(tools, func(p exerciseModel.ToolsPlacement) bool { return p.Seat == seat }) {
		return nil
	}
	sealed := exerciseModel.CopyDefender(variant.Defender).Credentials
	return &exerciseModel.DeployedDossier{Variant: variant.ID, Dossier: exerciseModel.HumanDossier(variant, sealed)}
}

// DossierView is the dossier of the human defender of a test lab (console design 9a.3): Declared is false when the lab was
// deployed without one (the test lab then shows no dossier). The credentials carry their values.
type DossierView struct {
	Declared bool
	exerciseModel.Dossier
}

// TestDeployDossier is the dossier of the human defender of the owner's test lab, as the lab was deployed (kept with the test
// deploy, so a later edit of the draft never changes it): the console cables of the seat, the forbidden list, the operator
// credentials (the secret ones opened now from the sealed values kept) and the topology mode. It is refused for an event's
// participant preview (event staff are not the task's authors), and the caller's author access to the exercise (the right that
// sees the defender block in a read model) is checked again on every read, so a removed co-author loses it. Only once the lab
// is ready (ErrTestDeployNotReady).
func (u *ExerciseUseCase) TestDeployDossier(ctx context.Context, actor Actor, deployID uuid.UUID) (DossierView, error) {
	status, deploy, err := u.deployTestStatus(ctx, actor.UserID, deployID)
	if err != nil {
		return DossierView{}, err
	}
	if deploy.EventID != nil {
		return DossierView{}, exerciseModel.ErrTestDeployNotFound.Err()
	}
	exerciseID, _, _, err := u.loadVersionVariant(ctx, deploy.VersionID, deploy.VariantID)
	if err != nil {
		return DossierView{}, err
	}
	access, err := u.AuthorizeExercise(ctx, actor, exerciseID, ActionRead)
	if err != nil {
		return DossierView{}, err
	}
	if !access.Full || !access.Flags {
		return DossierView{}, exerciseModel.ErrExerciseForbidden.Err()
	}
	if deploy.Dossier == nil {
		return DossierView{Declared: false, Dossier: exerciseModel.Dossier{
			Links: []exerciseModel.ConsoleLink{}, Forbidden: []string{}, Credentials: []exerciseModel.DefenderCredential{}, Operates: []string{},
			Topology: exerciseModel.DefenderTopologyNone,
		}}, nil
	}
	if deploy.IsStatic() {
		return DossierView{}, exerciseModel.ErrTestDeployStatic.Err()
	}
	if !status.Ready {
		return DossierView{}, exerciseModel.ErrTestDeployNotReady.Err()
	}
	dossier := deploy.Dossier.Dossier
	dossier.Credentials = make([]exerciseModel.DefenderCredential, 0, len(deploy.Dossier.Credentials))
	for _, c := range deploy.Dossier.Credentials {
		if c.Secret && c.Value != "" {
			if err = u.openCredential(deploy.Dossier.Variant, &c, "Failed to decrypt a secret credential"); err != nil {
				return DossierView{}, err
			}
		}
		c.Secret = false
		dossier.Credentials = append(dossier.Credentials, c)
	}
	return DossierView{Declared: true, Dossier: dossier}, nil
}
