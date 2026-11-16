package exercise

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/exerciseRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/model/rbac"
	"github.com/cybericebox/backend/pkg/secret"
)

// editedFixture is a task with two secret variables on one device and a plain one, stored sealed as the platform stores them.
func editedFixture(t *testing.T, cipher *secret.Cipher) (exerciseModel.ExerciseVersion, uuid.UUID) {
	t.Helper()
	version, _ := summaryVersion(t)
	version.Status = exerciseModel.VersionStatusDraft
	version.Variants[0].Tasks[0].Attachments = nil
	exerciseID := freshID()
	version.ExerciseID, version.ID = exerciseID, freshID()
	env := []exerciseModel.EnvVar{{Name: "MODE", Value: "prod"}}
	for name, value := range map[string]string{"DB_PASSWORD": "scenario password", "API_KEY": "per-build key"} {
		sealed, err := cipher.EncryptWithContext([]byte(value), EnvSecretContext(version.Variants[0].ID, "web", name))
		require.NoError(t, err)
		env = append(env, exerciseModel.EnvVar{Name: name, Value: sealed, Secret: true, Origin: freshID().String()})
	}
	version.Variants[0].Topology.Devices[0].Bootstrap.Env = env
	return version, exerciseID
}

func editUseCase(t *testing.T, version exerciseModel.ExerciseVersion, cipher *secret.Cipher) *ExerciseUseCase {
	t.Helper()
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	variants, err := json.Marshal(version.Variants)
	require.NoError(t, err)
	q.EXPECT().GetExerciseByID(gomock.Any(), version.ExerciseID).Return(postgres.Exercise{ID: version.ExerciseID, Name: version.Name, Tags: version.Tags}, nil).AnyTimes()
	q.EXPECT().ListExerciseVersions(gomock.Any(), version.ExerciseID).Return([]postgres.ExerciseVersion{{
		ID: version.ID, ExerciseID: version.ExerciseID, Status: string(version.Status), Variants: variants,
	}}, nil).AnyTimes()
	return &ExerciseUseCase{exercises: exerciseRepo.New(q), cipher: cipher}
}

func openedEnv(t *testing.T, cipher *secret.Cipher, variant exerciseModel.Variant) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, env := range variant.Topology.Devices[0].Bootstrap.Env {
		if env.Secret && env.Value != "" {
			plain, err := cipher.DecryptWithContext(env.Value, EnvSecretContext(variant.ID, "web", env.Name))
			require.NoError(t, err)
			out[env.Name] = string(plain)
		} else {
			out[env.Name] = env.Value
		}
	}
	return out
}

// The edit export keeps no secret value: each secret variable is there by name with an empty value (the flag stays), a plain
// variable keeps its value. What the brain can read holds no secret.
func TestEditExportDeliversEverySecretByNameWithAnEmptyValue(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("11", 32))
	require.NoError(t, err)
	stored, _ := editedFixture(t, cipher)
	u := editUseCase(t, stored, cipher)
	archive, err := u.ExportTaskVersionForEdit(context.Background(), Actor{UserID: freshID(), Role: rbac.RoleAdmin}, stored.ExerciseID, stored.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(archive), "scenario password")
	parsed, err := ParseExerciseArchive(archive, "", DefaultLimits())
	require.NoError(t, err)
	byName := map[string]exerciseModel.EnvVar{}
	for _, e := range parsed.Versions[0].Variants[0].Topology.Devices[0].Bootstrap.Env {
		byName[e.Name] = e
	}
	assert.Equal(t, exerciseModel.EnvVar{Name: "MODE", Value: "prod"}, byName["MODE"])
	for _, name := range []string{"DB_PASSWORD", "API_KEY"} {
		assert.Equal(t, exerciseModel.EnvVar{Name: name, Secret: true}, byName[name])
	}
}

// Export for edit -> the edit (a changed flag, a new plain and a new secret variable, one secret set anew) -> the import of the
// update: the stored secrets are back where the edit left them empty, the edit is applied, a value typed in the edit is taken as
// typed, and a new secret without a value stays empty with a warning that names it.
func TestEditExportThenUpdateImportKeepsSecretsAndAppliesTheEdit(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("22", 32))
	require.NoError(t, err)
	stored, _ := editedFixture(t, cipher)
	u := editUseCase(t, stored, cipher)
	archive, err := u.ExportTaskVersionForEdit(context.Background(), Actor{UserID: freshID(), Role: rbac.RoleAdmin}, stored.ExerciseID, stored.ID)
	require.NoError(t, err)

	edited, err := ParseExerciseArchive(archive, "", DefaultLimits())
	require.NoError(t, err)
	version := edited.Versions[0]
	version.Variants[0].Tasks[0].Check = exerciseModel.FlagCheck("ICE{edited}")
	device := &version.Variants[0].Topology.Devices[0]
	device.Bootstrap.Env = append(device.Bootstrap.Env,
		exerciseModel.EnvVar{Name: "DEBUG", Value: "0"},
		exerciseModel.EnvVar{Name: "NEW_SECRET", Secret: true})
	for i := range device.Bootstrap.Env {
		if device.Bootstrap.Env[i].Name == "API_KEY" {
			device.Bootstrap.Env[i].Value = "typed in the edit"
		}
	}

	_, _, err = importIdentity(&version, stored)
	require.NoError(t, err)
	require.Equal(t, stored.Variants[0].ID, version.Variants[0].ID, "the edited variant is the stored one")
	warnings, err := u.restoreKeptSecrets(&version, stored)
	require.NoError(t, err)
	require.NoError(t, u.encryptImportedSecrets([]exerciseModel.ExerciseVersion{version}))

	assert.Equal(t, map[string]string{
		"MODE": "prod", "DEBUG": "0", "DB_PASSWORD": "scenario password", "API_KEY": "typed in the edit", "NEW_SECRET": "",
	}, openedEnv(t, cipher, version.Variants[0]))
	assert.Equal(t, exerciseModel.FlagCheck("ICE{edited}"), version.Variants[0].Tasks[0].Check, "the edit is applied")
	assert.Equal(t, []exerciseModel.ImportWarning{{Code: exerciseModel.ImportWarnSecretMissing, Context: map[string]any{"device": "web", "variable": "NEW_SECRET"}}}, warnings)
	assert.Equal(t, "scenario password", openedEnv(t, cipher, stored.Variants[0])["DB_PASSWORD"], "the stored version is not touched")
}

// A secret of a device the edit renamed has no stored counterpart (nothing matches by device name): it stays empty with a
// warning, never another device's value. Without the key to open the stored values the restore is refused, not skipped.
func TestKeptSecretsAreFoundOnlyByDeviceAndNameAndNeedTheKey(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("33", 32))
	require.NoError(t, err)
	stored, _ := editedFixture(t, cipher)
	u := editUseCase(t, stored, cipher)
	archive, err := u.ExportTaskVersionForEdit(context.Background(), Actor{UserID: freshID(), Role: rbac.RoleAdmin}, stored.ExerciseID, stored.ID)
	require.NoError(t, err)
	parse := func() exerciseModel.ExerciseVersion {
		parsed, parseErr := ParseExerciseArchive(archive, "", DefaultLimits())
		require.NoError(t, parseErr)
		version := parsed.Versions[0]
		_, _, parseErr = importIdentity(&version, stored)
		require.NoError(t, parseErr)
		return version
	}
	t.Run("another device name", func(t *testing.T) {
		version := parse()
		version.Variants[0].Topology.Devices[0].Name = "renamed"
		warnings, restoreErr := u.restoreKeptSecrets(&version, stored)
		require.NoError(t, restoreErr)
		assert.Len(t, warnings, 2)
		for _, env := range version.Variants[0].Topology.Devices[0].Bootstrap.Env {
			if env.Secret {
				assert.Empty(t, env.Value)
			}
		}
	})
	t.Run("no key to open the stored value", func(t *testing.T) {
		version := parse()
		_, restoreErr := (&ExerciseUseCase{}).restoreKeptSecrets(&version, stored)
		require.ErrorIs(t, restoreErr, exerciseModel.ErrSecretsNotConfigured.Err())
	})
}
