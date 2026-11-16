package exercise

import (
	"bytes"
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
// typed, and a new secret without a value stays empty: a missing value of the version.
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
	require.NoError(t, u.restoreKeptSecrets(&version, stored))
	require.NoError(t, u.encryptImportedSecrets([]exerciseModel.ExerciseVersion{version}))

	assert.Equal(t, map[string]string{
		"MODE": "prod", "DEBUG": "0", "DB_PASSWORD": "scenario password", "API_KEY": "typed in the edit", "NEW_SECRET": "",
	}, openedEnv(t, cipher, version.Variants[0]))
	assert.Equal(t, exerciseModel.FlagCheck("ICE{edited}"), version.Variants[0].Tasks[0].Check, "the edit is applied")
	assert.Equal(t, []exerciseModel.MissingSecret{{Variant: 1, Device: "web", Key: "NEW_SECRET"}}, exerciseModel.MissingSecrets(version.Variants), "the new secret is a missing value of the version")
	assert.Equal(t, "scenario password", openedEnv(t, cipher, stored.Variants[0])["DB_PASSWORD"], "the stored version is not touched")
}

// A secret of a device the edit renamed has no stored counterpart (nothing matches by device name): it stays empty
// (a missing value), never another device's value. Without the key to open the stored values the restore is refused, not skipped.
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
		require.NoError(t, u.restoreKeptSecrets(&version, stored))
		assert.Len(t, exerciseModel.MissingSecrets(version.Variants), 2, "both secrets of the renamed device are missing")
		for _, env := range version.Variants[0].Topology.Devices[0].Bootstrap.Env {
			if env.Secret {
				assert.Empty(t, env.Value)
			}
		}
	})
	t.Run("no key to open the stored value", func(t *testing.T) {
		version := parse()
		restoreErr := (&ExerciseUseCase{}).restoreKeptSecrets(&version, stored)
		require.ErrorIs(t, restoreErr, exerciseModel.ErrSecretsNotConfigured.Err())
	})
}

// An archive holds a secret value or only declares secrets: only the first is refused for a task the brain made.
func TestArchiveCarriesSecretValue(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("44", 32))
	require.NoError(t, err)
	stored, _ := editedFixture(t, cipher)
	u := editUseCase(t, stored, cipher)
	archive, err := u.ExportTaskVersionForEdit(context.Background(), Actor{UserID: freshID(), Role: rbac.RoleAdmin}, stored.ExerciseID, stored.ID)
	require.NoError(t, err)
	carries, err := u.ArchiveCarriesSecretValue(archive, DefaultLimits())
	require.NoError(t, err)
	assert.False(t, carries, "an edit export declares its secrets without values")

	withValue, err := u.ExportExerciseArchive(context.Background(), stored.ExerciseID, ExportOptions{IncludeSecrets: true, Password: "pw"}, DefaultLimits())
	require.NoError(t, err)
	_, err = u.ArchiveCarriesSecretValue(withValue, DefaultLimits())
	require.Error(t, err, "a protected archive is not one the brain makes: it does not even open without its password")

	_, err = u.ArchiveCarriesSecretValue([]byte("not a zip"), DefaultLimits())
	require.Error(t, err)
}

// Which task an archive is the edit of: the one its document names when the caller may edit it; otherwise none, and the archive is
// a new task (never an error, never an update of a task the caller cannot edit).
func TestEditedArchiveTarget(t *testing.T) {
	cipher, err := secret.New(strings.Repeat("55", 32))
	require.NoError(t, err)
	stored, _ := editedFixture(t, cipher)
	u := editUseCase(t, stored, cipher)
	admin := Actor{UserID: freshID(), Role: rbac.RoleAdmin}
	archive, err := u.ExportTaskVersionForEdit(context.Background(), admin, stored.ExerciseID, stored.ID)
	require.NoError(t, err)

	target, err := u.EditedArchiveTarget(context.Background(), admin, archive, DefaultLimits())
	require.NoError(t, err)
	assert.Equal(t, stored.ExerciseID, target, "an editor of the task: it is an edit of it")

	nobody := Actor{UserID: freshID(), Role: rbac.RoleUser}
	target, err = u.EditedArchiveTarget(context.Background(), nobody, archive, DefaultLimits())
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, target, "a user who may not edit the task: a new task")

	files, err := ReadArchiveV1(bytes.NewReader(archive), DefaultLimits())
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(files["exercise.json"], &doc))
	delete(doc["exercise"].(map[string]any), "id")
	files["exercise.json"], err = json.Marshal(doc)
	require.NoError(t, err)
	anonymous, err := BuildArchiveV1(files)
	require.NoError(t, err)
	target, err = u.EditedArchiveTarget(context.Background(), admin, anonymous, DefaultLimits())
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, target, "no id: a new task")
}
