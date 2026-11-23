package exercise

import (
	"context"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// withDossierFiles gives the tools device of every placement that operates devices (a bot defender: defense or
// attack-defense) the files of its dossier (console design 9a.4): /run/eda/consoles.json, /run/eda/credentials.json and, when
// the dossier shows the topology, /run/eda/topology.json, root-only, through its bootstrap. The run's topology (after
// auto-cabling) is what they describe. An attacker's or a grader's tools device gets none. The credentials are opened here
// only for the files, which reach the laboratory as secret bootstrap material: never a row or a log.
func (u *ExerciseUseCase) withDossierFiles(variant exerciseModel.Variant, run exerciseModel.Topology, tools []exerciseModel.Device, places []exerciseModel.ToolsPlacement) (exerciseModel.Topology, error) {
	var credentials []exerciseModel.DefenderCredential
	opened := false
	for i, device := range tools {
		if i >= len(places) || len(places[i].Operates) == 0 {
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
		if run, err = run.WithDossierFiles(device.ID, dossier); err != nil {
			return exerciseModel.Topology{}, err
		}
	}
	return run, nil
}

// DossierView is the dossier of the human defender of a test lab (console design 9a.3): Declared is false for a variant without
// a defender block (the test lab then shows no dossier). The credentials carry their values.
type DossierView struct {
	Declared bool
	exerciseModel.Dossier
}

// TestDeployDossier is the dossier of the human defender of the owner's test lab, built from the variant as it was deployed
// (a human run deploys the seat as authored): the console cables of the seat, the off-limits devices, the operator credentials
// (secret ones opened) and whether the topology is shown. Only the owner gets it, and only once the lab is ready
// (ErrTestDeployNotReady); a static test has none (ErrTestDeployStatic).
func (u *ExerciseUseCase) TestDeployDossier(ctx context.Context, ownerID, deployID uuid.UUID) (DossierView, error) {
	status, deploy, err := u.deployTestStatus(ctx, ownerID, deployID)
	if err != nil {
		return DossierView{}, err
	}
	variant, err := u.loadVariant(ctx, deploy.VersionID, deploy.VariantID)
	if err != nil {
		return DossierView{}, err
	}
	if variant.Defender == nil {
		return DossierView{Declared: false, Dossier: exerciseModel.HumanDossier(variant, nil)}, nil
	}
	if !variant.Topology.HasLab() {
		return DossierView{}, exerciseModel.ErrTestDeployStatic.Err()
	}
	if !status.Ready {
		return DossierView{}, exerciseModel.ErrTestDeployNotReady.Err()
	}
	credentials, err := u.defenderCredentials(variant)
	if err != nil {
		return DossierView{}, err
	}
	return DossierView{Declared: true, Dossier: exerciseModel.HumanDossier(variant, credentials)}, nil
}
