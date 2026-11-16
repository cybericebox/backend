package exerciseModel_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
)

func versionWithEnv(env ...exerciseModel.EnvVar) exerciseModel.ExerciseVersion {
	variant := validVariant()
	variant.Topology.Devices = append(variant.Topology.Devices, exerciseModel.Device{
		Name: "db", Type: exerciseModel.DeviceTypeContainer, Image: "postgres:16", Bootstrap: exerciseModel.Bootstrap{Env: env},
	})
	return exerciseModel.ExerciseVersion{Variants: []exerciseModel.Variant{variant}}
}

// A secret variable with no value is a state, not an error: the draft is legal, the version lists what is missing (variant,
// device, key), and a plain empty variable or a secret that has a value is not missing.
func TestMissingSecretsListsEverySecretWithoutAValue(t *testing.T) {
	version := versionWithEnv(
		exerciseModel.EnvVar{Name: "DB_PASSWORD", Secret: true},
		exerciseModel.EnvVar{Name: "API_KEY", Secret: true, Value: "sealed"},
		exerciseModel.EnvVar{Name: "MODE"},
		exerciseModel.EnvVar{Name: "TOKEN", Secret: true},
	)
	require.NoError(t, version.ValidateStructure(), "an empty secret is legal in a draft")
	assert.Equal(t, []exerciseModel.MissingSecret{{Variant: 1, Device: "db", Key: "DB_PASSWORD"}, {Variant: 1, Device: "db", Key: "TOKEN"}}, version.MissingSecrets())
	kept := versionWithEnv(exerciseModel.EnvVar{Name: "A", Secret: true, Value: "x"})
	assert.Empty(t, kept.MissingSecrets())
}

// Publishing is refused while a value is missing, with the list in the error; once the values are entered it is not.
func TestPublishIsRefusedWhileASecretValueIsMissing(t *testing.T) {
	version := versionWithEnv(exerciseModel.EnvVar{Name: "DB_PASSWORD", Secret: true})
	plain := versionWithEnv()
	require.NoError(t, plain.ValidateForPublish(), "the fixture is publishable by itself")

	err := version.ValidateForPublish()
	require.Error(t, err)
	require.True(t, errors.Is(err, exerciseModel.ErrSecretValueMissing.Err()), "got %v", err)

	version.Variants[0].Topology.Devices[0].Bootstrap.Env[0].Value = "sealed value"
	assert.NoError(t, version.ValidateForPublish(), "entering the value clears the state")
}
