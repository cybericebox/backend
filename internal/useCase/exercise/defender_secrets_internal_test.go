package exercise

import (
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/pkg/secret"
)

func defenderVariant(id uuid.UUID, value string) exerciseModel.Variant {
	return exerciseModel.Variant{ID: id, Defender: &exerciseModel.Defender{Credentials: []exerciseModel.DefenderCredential{
		{Device: "r1", Port: "console0", User: "admin", Value: value, Secret: true},
		{Device: "web", User: "root", Value: "plain"},
	}}}
}

// A secret credential follows the rules of a secret env value: sealed on save to its variant, kept by a blank save, blank in a
// read model, opened for the dossier; a linked variant or a copy takes its source's value sealed under its own id.
func TestDefenderCredentialSecrets(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("66", 32))
	require.NoError(t, err)
	u := &ExerciseUseCase{cipher: cipher}
	base := uuid.Must(uuid.NewV7())

	saved := []exerciseModel.Variant{defenderVariant(base, "s3cret")}
	require.NoError(t, u.encryptDefenderSecrets(saved, submittedDefenderSecrets(saved)))
	sealed := saved[0].Defender.Credentials[0].Value
	require.NotEqual(t, "s3cret", sealed)
	require.Equal(t, "plain", saved[0].Defender.Credentials[1].Value, "a plain value stays as it is")

	next := []exerciseModel.Variant{defenderVariant(base, "")}
	submitted := submittedDefenderSecrets(next)
	mergeKeptDefenderSecrets(next, saved)
	require.NoError(t, u.encryptDefenderSecrets(next, submitted))
	require.Equal(t, sealed, next[0].Defender.Credentials[0].Value, "a blank secret keeps the stored ciphertext")

	masked := maskSecrets(next)
	require.Empty(t, masked[0].Defender.Credentials[0].Value)
	require.Equal(t, sealed, next[0].Defender.Credentials[0].Value, "the stored variant is not changed")

	opened, err := u.defenderCredentials(next[0])
	require.NoError(t, err)
	require.Equal(t, []exerciseModel.DefenderCredential{
		{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"},
		{Device: "web", User: "root", Value: "plain"},
	}, opened)

	// A linked variant (its block is the base's, blank on the way in) and a copy take the value under their own ids.
	linked := defenderVariant(uuid.Must(uuid.NewV7()), "")
	linked.LinkedTo = uuid.NullUUID{UUID: base, Valid: true}
	copied := defenderVariant(uuid.Must(uuid.NewV7()), "")
	variants := []exerciseModel.Variant{next[0], linked, copied}
	inherited := map[string]bool{}
	require.NoError(t, u.inheritSecrets(variants, map[int]uuid.UUID{1: base, 2: base}, inherited))
	require.NoError(t, u.encryptDefenderSecrets(variants, inherited))
	for _, v := range variants[1:] {
		got, openErr := u.defenderCredentials(v)
		require.NoError(t, openErr)
		require.Equal(t, "s3cret", got[0].Value)
		require.NotEqual(t, sealed, v.Defender.Credentials[0].Value, "sealed under the variant's own id")
	}

	moved := next[0]
	moved.Defender = exerciseModel.CopyDefender(moved.Defender)
	moved.Defender.Credentials[0].User = "root"
	_, err = u.defenderCredentials(moved)
	require.Error(t, err, "a value moved to another credential does not open")

	// The export opens the values (a password-protected archive), the import seals them again; an unprotected import refuses one.
	versions := []exerciseModel.ExerciseVersion{{Variants: []exerciseModel.Variant{{ID: base, Defender: exerciseModel.CopyDefender(next[0].Defender)}}}}
	require.NoError(t, u.decryptExportedSecrets(versions))
	require.Equal(t, "s3cret", versions[0].Variants[0].Defender.Credentials[0].Value)
	require.Equal(t, "versions[1].variants[1].defender.credentials[1]", firstSecretValue(versions), "an unprotected import refuses it")
	require.NoError(t, u.encryptImportedSecrets(versions))
	again, err := u.defenderCredentials(versions[0].Variants[0])
	require.NoError(t, err)
	require.Equal(t, "s3cret", again[0].Value)

	// An edited version that leaves the secret blank gets the stored value back.
	edited := exerciseModel.ExerciseVersion{Variants: []exerciseModel.Variant{defenderVariant(base, "")}}
	require.NoError(t, u.restoreKeptSecrets(&edited, exerciseModel.ExerciseVersion{Variants: next}))
	require.Equal(t, "s3cret", edited.Variants[0].Defender.Credentials[0].Value)
}

// A tools device that operates devices gets the dossier files, with the credentials opened, when the variant declares a
// defender block or the device has console lines; an attacker's never.
func TestWithDossierFiles(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("77", 32))
	require.NoError(t, err)
	u := &ExerciseUseCase{cipher: cipher}
	variant := defenderVariant(uuid.Must(uuid.NewV7()), "s3cret")
	stored := []exerciseModel.Variant{variant}
	require.NoError(t, u.encryptDefenderSecrets(stored, submittedDefenderSecrets(stored)))
	variant = stored[0]

	defender := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "tools", Type: exerciseModel.DeviceTypeNode}
	attacker := exerciseModel.Device{ID: uuid.Must(uuid.NewV7()), Name: "tools-2", Type: exerciseModel.DeviceTypeNode}
	run := exerciseModel.Topology{Devices: []exerciseModel.Device{defender, attacker}}
	places := []exerciseModel.ToolsPlacement{{Operates: []string{"r1"}}, {Foothold: "lan"}}
	out, err := u.withDossierFiles(variant, run, []exerciseModel.Device{defender, attacker}, places)
	require.NoError(t, err)
	paths := func(d exerciseModel.Device) []string {
		var out []string
		for _, f := range d.Bootstrap.Files {
			out = append(out, f.Path)
		}
		return out
	}
	require.Equal(t, []string{exerciseModel.DossierConsolesPath, exerciseModel.DossierCredentialsPath, exerciseModel.DossierTopologyPath}, paths(out.Devices[0]))
	require.Contains(t, out.Devices[0].Bootstrap.Files[1].Content, "s3cret")
	require.NotContains(t, out.Devices[0].Bootstrap.Files[2].Content, "tools-2", "no other tools device is named")
	require.Empty(t, paths(out.Devices[1]), "an attacker's tools device gets no dossier")
	require.Empty(t, run.Devices[0].Bootstrap.Files, "the input is not changed")

	out, err = u.withDossierFiles(exerciseModel.Variant{}, run, []exerciseModel.Device{defender, attacker}, places)
	require.NoError(t, err)
	require.Empty(t, paths(out.Devices[0]), "no block and no console lines: nothing to tell, no launcher")
}

// The deployed dossier is the variant as it is at the deploy, sealed values kept; none without a block or when a bot takes the
// seat.
func TestDeployedDossier(t *testing.T) {
	v := defenderVariant(uuid.Must(uuid.NewV7()), "ciphertext")
	v.Defender.Seat = "pc1"
	got := deployedDossier(v, nil)
	require.NotNil(t, got)
	require.Equal(t, v.ID, got.Variant)
	require.Equal(t, "ciphertext", got.Credentials[0].Value)
	require.True(t, got.Credentials[0].Secret)
	v.Defender.Credentials[0].Value = "changed later"
	require.Equal(t, "ciphertext", got.Credentials[0].Value, "a copy: a later edit does not reach it")
	require.Nil(t, deployedDossier(v, []exerciseModel.ToolsPlacement{{Seat: "pc1"}}))
	require.Nil(t, deployedDossier(exerciseModel.Variant{}, nil))
}
