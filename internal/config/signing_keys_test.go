package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cybericebox/daemon/pkg/secret"
)

const (
	signMaster   = "m2:" + dirKeyB + ",m1:" + dirKeyA
	signExplicit = "an-old-explicit-signing-value-of-32-bytes!"
)

// The setup-link and OAuth-state signatures come from the master key: one key per purpose, the first master version signs.
func TestMasterKeyGivesTheSigningPurposesTheirKeys(t *testing.T) {
	var c Config
	c.Keys.SecretsMasterKey = signMaster
	require.NoError(t, c.ResolveKeys())
	require.Len(t, c.Auth.SetupTokenKeys, 2)
	require.Len(t, c.Auth.OAuth.StateKeys, 2)
	require.Equal(t, "d-m2", c.Auth.SetupTokenKeys[0].ID)
	require.Equal(t, "d-m1", c.Auth.OAuth.StateKeys[1].ID)
	require.NotEqual(t, c.Auth.SetupTokenKeys[0].Key, c.Auth.OAuth.StateKeys[0].Key, "one key per purpose")

	var again Config
	again.Keys.SecretsMasterKey = signMaster
	require.NoError(t, again.ResolveKeys())
	require.Equal(t, c.Auth.SetupTokenKeys, again.Auth.SetupTokenKeys, "the same keys after a restart: nothing is stored")
}

// An installation that still sets the old variables keeps the tokens it already mailed valid.
func TestExplicitSigningValuesStayAsVerifyOnlyKeys(t *testing.T) {
	var c Config
	c.Keys.SecretsMasterKey = signMaster
	c.Auth.TokenSignature = signExplicit
	c.Auth.OAuth.StateSignature = signExplicit + "-state"
	require.NoError(t, c.ResolveKeys())
	require.Equal(t, []string{"d-m2", "d-m1", secret.ExplicitSigningKeyID}, signingIDs(c.Auth.SetupTokenKeys))
	require.Equal(t, []string{"d-m2", "d-m1", secret.ExplicitSigningKeyID}, signingIDs(c.Auth.OAuth.StateKeys))
	require.Equal(t, signExplicit, string(c.Auth.SetupTokenKeys[2].Key))
}

func TestWithoutAMasterKeyTheExplicitValueIsTheOnlyKey(t *testing.T) {
	var c Config
	c.Auth.TokenSignature = signExplicit
	require.NoError(t, c.ResolveKeys())
	require.Equal(t, []secret.SigningKey{{Key: []byte(signExplicit)}}, c.Auth.SetupTokenKeys)
	require.Empty(t, c.Auth.OAuth.StateKeys)
}

func TestSigningKeysAreNotChangedByAFailedResolve(t *testing.T) {
	var c Config
	c.Keys.SecretsMasterKey = signMaster
	c.Exercise.SecretsKeyDir = t.TempDir() + "/missing"
	require.Error(t, c.ResolveKeys())
	require.Empty(t, c.Auth.SetupTokenKeys)
}

func signingIDs(keys []secret.SigningKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.ID
	}
	return out
}

func TestWeaknessesKnowTheMasterKey(t *testing.T) {
	derived := []secret.SigningKey{{ID: "d-m1", Key: make([]byte, 32)}}
	c := AuthConfig{SetupTokenKeys: derived, OAuth: OAuthConfig{StateKeys: derived}, Recaptcha: RecaptchaConfig{Score: 0.5}}
	c.OAuth.Google.ClientID = "id"
	require.Empty(t, c.Weaknesses(), "keys from the master key need no JWT_TOKEN_SIGNATURE or OAUTH_STATE_SIGNATURE")

	weak := c
	weak.TokenSignature = "short" // an explicit value that is still accepted must be strong
	require.NotEmpty(t, weak.Weaknesses())

	none := AuthConfig{Recaptcha: RecaptchaConfig{Score: 0.5}}
	require.NotEmpty(t, none.Weaknesses(), "no master key and no JWT_TOKEN_SIGNATURE")
}

func TestRenderSecretNextValidation(t *testing.T) {
	valid := AuthConfig{
		SessionEncryptionKey: dirKeyA, RenderSecret: strings.Repeat("r", 32),
		SessionIdleTTL: 12 * time.Hour, SessionAbsoluteTTL: 168 * time.Hour, SessionRevocationStaleAfter: 30 * time.Second,
	}
	require.NoError(t, valid.ValidateSession(), "RENDER_SECRET_NEXT is optional")
	next := valid
	next.RenderSecretNext = strings.Repeat("n", 32)
	require.NoError(t, next.ValidateSession())
	short := valid
	short.RenderSecretNext = "short"
	require.ErrorContains(t, short.ValidateSession(), "RENDER_SECRET_NEXT")
	same := valid
	same.RenderSecretNext = valid.RenderSecret
	require.ErrorContains(t, same.ValidateSession(), "RENDER_SECRET_NEXT")
}
