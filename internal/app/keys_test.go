package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/useCase/secretkeys"
	"github.com/cybericebox/backend/pkg/secret"
)

type fakeChecker struct {
	report secretkeys.Report
	err    error
	asked  int
	opened bool
}

func (f *fakeChecker) Check(_ context.Context, open bool) (secretkeys.Report, error) {
	f.asked++
	f.opened = open
	return f.report, f.err
}

func lostKey() secretkeys.Report {
	return secretkeys.Report{Columns: []secretkeys.Column{{
		Name: "event_registry_secrets.token_ciphertext", Total: 1, Problems: 1,
		ProblemRefs: []secretkeys.Problem{{Kind: secretkeys.KeyNotLoaded, KeyID: "k0", Ref: "row"}},
	}}}
}

func TestGuardModesOnALostKey(t *testing.T) {
	ctx := context.Background()

	c := &fakeChecker{report: lostKey()}
	require.NoError(t, guardSecrets(ctx, config.SecretsGuardWarn, c), "warn starts")
	require.Equal(t, 1, c.asked)
	require.False(t, c.opened, "the guard compares key ids, it opens nothing")

	err := guardSecrets(ctx, config.SecretsGuardRefuse, &fakeChecker{report: lostKey()})
	require.ErrorIs(t, err, ErrSecretKeysLost)

	off := &fakeChecker{report: lostKey()}
	require.NoError(t, guardSecrets(ctx, config.SecretsGuardOff, off))
	require.Zero(t, off.asked, "off does not even read")
}

func TestGuardPassesWhenEveryKeyIsLoaded(t *testing.T) {
	for _, mode := range []string{config.SecretsGuardWarn, config.SecretsGuardRefuse} {
		require.NoError(t, guardSecrets(context.Background(), mode, &fakeChecker{}), mode)
	}
}

// A scan that cannot run is not a lost key: warn goes on, refuse stops because it cannot promise the keys are there.
func TestGuardWhenTheScanFails(t *testing.T) {
	boom := errors.New("database down")
	require.NoError(t, guardSecrets(context.Background(), config.SecretsGuardWarn, &fakeChecker{err: boom}))
	require.ErrorIs(t, guardSecrets(context.Background(), config.SecretsGuardRefuse, &fakeChecker{err: boom}), boom)
}

type totpFlag struct {
	stored bool
	err    error
}

func (f totpFlag) AnyTOTP(context.Context) (bool, error) { return f.stored, f.err }

// Stored authenticator secrets and no platform key: the daemon does not start. Nothing stored, or the key set: it does.
func TestGuardTOTPKey(t *testing.T) {
	ctx := context.Background()
	require.Error(t, guardTOTPKey(ctx, totpFlag{stored: true}, nil))
	require.NoError(t, guardTOTPKey(ctx, totpFlag{}, nil))
	require.Error(t, guardTOTPKey(ctx, totpFlag{err: errors.New("db down")}, nil))
	key, err := secret.New("k1:" + strings.Repeat("a1", 32))
	require.NoError(t, err)
	require.NoError(t, guardTOTPKey(ctx, totpFlag{stored: true}, key))
}
