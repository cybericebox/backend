package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/repository/sessionRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/userRepo"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	notificationPayloads "github.com/cybericebox/backend/internal/model/notification/types/payloads"
	"github.com/cybericebox/backend/internal/model/rbac"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
	userModel "github.com/cybericebox/backend/internal/model/user"
	"github.com/cybericebox/backend/internal/secretctx"
	"github.com/cybericebox/backend/internal/testhelpers"
	"github.com/cybericebox/backend/internal/useCase/auth"
	"github.com/cybericebox/backend/pkg/password"
	"github.com/cybericebox/backend/pkg/secret"
	"github.com/cybericebox/backend/pkg/token"
	"github.com/cybericebox/backend/pkg/totp"
)

const (
	factorPass   = "Secret!1"
	factorKeyOld = "000102030405060708090a0b0c0d0e0f000102030405060708090a0b0c0d0e0f"
	factorKeyNew = "101112131415161718191a1b1c1d1e1f101112131415161718191a1b1c1d1e1f"
)

type keysStub struct{ holds bool }

func (k *keysStub) HoldsSecondFactorKey(context.Context, uuid.UUID) (bool, error) {
	return k.holds, nil
}

type journalStub struct {
	events []secondFactorModel.JournalEvent
}

func (j *journalStub) RecordFactorEvent(_ context.Context, e secondFactorModel.JournalEvent) error {
	j.events = append(j.events, e)
	return nil
}

// factorEnv is the auth use case on a real database with one person and one session of theirs.
type factorEnv struct {
	t        *testing.T
	db       *testhelpers.TestDB
	uc       *auth.AuthUseCase
	notifier *fakeNotifier
	journal  *journalStub
	keys     *keysStub
	userID   uuid.UUID
	email    string
	session  uuid.UUID
}

func newFactorEnv(t *testing.T, withPassword bool) *factorEnv {
	t.Helper()
	db := testhelpers.SetupTestDB(t)
	ctx := context.Background()
	pw := password.New(password.Config{HashCost: 4})
	e := &factorEnv{t: t, db: db, notifier: &fakeNotifier{}, journal: &journalStub{}, keys: &keysStub{}}
	e.uc = auth.NewAuthUseCase(auth.Dependencies{
		Repo:     db.Queries,
		Token:    token.MustNew(token.Config{TokenSignature: "test-signing-key-that-is-long-enough"}),
		Password: pw,
		Notifier: e.notifier,
		Sessions: testSessions(t),
		Config: config.AuthConfig{
			SessionIdleTTL: time.Hour, TemporalCodeTTL: time.Hour, SignupSetupTokenTTL: time.Hour,
			Hosts: testHosts("example.test"),
		},
	})
	e.uc.SetSecondFactorCipher(e.cipher("k1:" + factorKeyOld))
	e.uc.SetSecondFactorKeys(e.keys)
	e.uc.SetFactorJournal(e.journal)

	e.userID, e.email = uuid.Must(uuid.NewV7()), "ann@example.test"
	user := userModel.NewGoogleUser(e.userID, e.email, "Ann", "Lee", time.Now())
	if withPassword {
		hashed, err := pw.Hash(factorPass)
		require.NoError(t, err)
		user.SetPassword(hashed, time.Now())
	}
	_, err := userRepo.New(db.Queries).Create(ctx, user)
	require.NoError(t, err)
	s, err := sessionRepo.New(db.Queries).Create(ctx, authModel.NewSession(e.userID, authModel.SessionMetadata{}, time.Hour, time.Now()))
	require.NoError(t, err)
	e.session = s.ID
	return e
}

func (e *factorEnv) cipher(spec string) *secret.Cipher {
	c, err := secret.New(spec)
	require.NoError(e.t, err)
	return c
}

// signedIn is the context of a request of the session, signed in signedInAgo ago.
func (e *factorEnv) signedIn(signedInAgo time.Duration) context.Context {
	return rbac.ContextWithCurrentUserSession(context.Background(), rbac.Claims{
		UserID: e.userID, SessionID: e.session, Role: rbac.RoleUser, SignedInAt: time.Now().Add(-signedInAgo),
	})
}

func (e *factorEnv) ctx() context.Context { return e.signedIn(time.Hour) }

// code is the authenticator code of secret offset steps from now.
func code(t *testing.T, enrolment auth.TOTPEnrolment, offset int64) string {
	t.Helper()
	raw, err := totp.DecodeSecret(enrolment.Secret)
	require.NoError(t, err)
	return totp.Default.Code(raw, totp.Default.Step(time.Now())+offset)
}

// enrol sets up the authenticator app with the code of the current step and returns it and the recovery codes.
func (e *factorEnv) enrol() (auth.TOTPEnrolment, []string) {
	e.t.Helper()
	enrolment, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, factorPass)
	require.NoError(e.t, err)
	codes, err := e.uc.FinishTOTPEnrolment(e.ctx(), e.userID, code(e.t, enrolment, 0))
	require.NoError(e.t, err)
	return enrolment, codes
}

func TestTOTPEnrolmentIsConfirmedByTheFirstCodeAndIssuesRecoveryCodes(t *testing.T) {
	e := newFactorEnv(t, true)

	_, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, "wrong")
	require.Error(t, err, "a stolen session alone cannot start a setup")

	enrolment, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, factorPass)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(enrolment.URI, "otpauth://totp/"), enrolment.URI)
	require.Contains(t, enrolment.URI, "secret="+enrolment.Secret)

	// The secret is sealed at rest, bound to its person.
	var sealed string
	require.NoError(t, e.db.Pool.QueryRow(context.Background(), `SELECT secret_ciphertext FROM user_totp WHERE user_id = $1`, e.userID).Scan(&sealed))
	require.NotContains(t, sealed, enrolment.Secret)
	raw, err := e.cipher("k1:"+factorKeyOld).DecryptWithContext(sealed, secretctx.TOTPSecret(e.userID))
	require.NoError(t, err)
	require.Equal(t, enrolment.Secret, totp.EncodeSecret(raw))

	status, err := e.uc.SecondFactorStatus(e.ctx(), e.userID)
	require.NoError(t, err)
	require.False(t, status.TOTPEnabled, "an unconfirmed setup is no factor")
	require.False(t, status.HasSecondFactor)

	_, err = e.uc.FinishTOTPEnrolment(e.ctx(), e.userID, code(t, enrolment, 5))
	wantErr(t, err, secondFactorModel.ErrTOTPInvalidCode)

	codes, err := e.uc.FinishTOTPEnrolment(e.ctx(), e.userID, code(t, enrolment, 0))
	require.NoError(t, err)
	require.Len(t, codes, secondFactorModel.RecoveryCodeCount)

	status, err = e.uc.SecondFactorStatus(e.ctx(), e.userID)
	require.NoError(t, err)
	require.True(t, status.TOTPEnabled)
	require.True(t, status.HasSecondFactor)
	require.Equal(t, secondFactorModel.RecoveryCodeCount, status.RecoveryCodesLeft)

	_, err = e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, factorPass)
	wantErr(t, err, secondFactorModel.ErrTOTPAlreadyEnabled)
}

func TestTOTPStepUpAcceptsOneStepEitherSideAndRefusesAReusedStep(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol() // takes the current step

	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 0)})
	wantErr(t, err, secondFactorModel.ErrStepUpFailed)
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 2)})
	wantErr(t, err, secondFactorModel.ErrStepUpFailed)

	s, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 1)})
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodTOTP, s.Method)

	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 1)})
	wantErr(t, err, secondFactorModel.ErrStepUpFailed)

	state, err := e.uc.StepUpState(context.Background(), e.session)
	require.NoError(t, err)
	require.True(t, state.Fresh(time.Now(), secondFactorModel.StepUpWindow))
	require.Equal(t, secondFactorModel.MethodTOTP, state.Method)
}

func TestPasswordStepUpOnlyForAnAccountWithoutAFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	s, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodPassword, s.Method)

	e.enrol()
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)

	for _, m := range []secondFactorModel.Method{"google", "email-code", "sms", secondFactorModel.MethodRecoveryCode} {
		_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: m, Code: "123456"})
		wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)
	}
}

// A Google-only account without a factor confirms with a sign-in of the last minutes; Google itself is no factor.
func TestGoogleOnlyAccountHasNoFactorAndConfirmsWithARecentSignIn(t *testing.T) {
	e := newFactorEnv(t, false)
	has, err := e.uc.HasSecondFactor(context.Background(), e.userID)
	require.NoError(t, err)
	require.False(t, has)

	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword})
	wantErr(t, err, authModel.ErrAuthReauthRequired)
	s, err := e.uc.StepUp(e.signedIn(time.Minute), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword})
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodRecentSignIn, s.Method)
}

func TestRemovingTheAuthenticatorNeedsAFactorStepUpAndKeepsTheLastFactorOfAKeyHolder(t *testing.T) {
	e := newFactorEnv(t, true)
	// A password step-up made before the setup does not confirm a factor change afterwards.
	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	require.NoError(t, err)
	enrolment, _ := e.enrol()
	wantErr(t, e.uc.RemoveTOTP(e.ctx(), e.userID), secondFactorModel.ErrStepUpRequired)

	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 1)})
	require.NoError(t, err)
	e.keys.holds = true
	wantErr(t, e.uc.RemoveTOTP(e.ctx(), e.userID), secondFactorModel.ErrLastFactor)

	e.keys.holds = false
	require.NoError(t, e.uc.RemoveTOTP(e.ctx(), e.userID))
	has, err := e.uc.HasSecondFactor(context.Background(), e.userID)
	require.NoError(t, err)
	require.False(t, has)
	wantErr(t, e.uc.RemoveTOTP(e.ctx(), e.userID), secondFactorModel.ErrTOTPNotEnabled)
}

func TestARecoveryCodeSignsInOnceIsJournaledAndMailedAndAllowsANewFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	_, codes := e.enrol()

	// A wrong password does not use the code up.
	_, _, err := e.uc.SignInWithRecoveryCode(context.Background(), e.email, "wrong", codes[0], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)
	_, _, err = e.uc.SignInWithRecoveryCode(context.Background(), "nobody@example.test", factorPass, codes[0], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)

	cookie, _, err := e.uc.SignInWithRecoveryCode(context.Background(), " ANN@example.test", factorPass, strings.ToLower(codes[0]), "", authModel.SessionMetadata{})
	require.NoError(t, err)
	require.NotEmpty(t, cookie)

	_, _, err = e.uc.SignInWithRecoveryCode(context.Background(), e.email, factorPass, codes[0], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)

	require.Len(t, e.journal.events, 1)
	require.Equal(t, secondFactorModel.ActionRecoveryCodeUsed, e.journal.events[0].Action)
	require.Equal(t, e.userID, e.journal.events[0].UserID)
	mail, ok := e.notifier.lastPayload.(notificationPayloads.RecoveryCodeUsedPayload)
	require.True(t, ok, "the owner is mailed: %T", e.notifier.lastPayload)
	require.Equal(t, "9", mail.CodesLeft)

	// The new session may manage the factors with its recovery step-up: drop the lost app and set up a new one.
	pass, err := e.uc.OpenSession(cookie)
	require.NoError(t, err)
	recovered := rbac.ContextWithCurrentUserSession(context.Background(), rbac.Claims{
		UserID: e.userID, SessionID: pass.Ticket.SessionID, Role: rbac.RoleUser, SignedInAt: time.Now().Add(-time.Hour),
	})
	state, err := e.uc.StepUpState(context.Background(), pass.Ticket.SessionID)
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodRecoveryCode, state.Method)
	require.False(t, state.Method.IsSecondFactor(), "a recovery code is never a second factor")
	require.NoError(t, e.uc.RemoveTOTP(recovered, e.userID))
	_, err = e.uc.BeginTOTPEnrolment(recovered, e.userID, "")
	require.NoError(t, err, "the recovery step-up stands in for the password")
}

func TestRecoveryCodesAreRegeneratedOnlyWithAFactorAndAStepUp(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.RegenerateRecoveryCodes(e.ctx(), e.userID)
	wantErr(t, err, secondFactorModel.ErrNoSecondFactor)

	enrolment, old := e.enrol()
	_, err = e.uc.RegenerateRecoveryCodes(e.ctx(), e.userID)
	wantErr(t, err, secondFactorModel.ErrStepUpRequired)

	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 1)})
	require.NoError(t, err)
	fresh, err := e.uc.RegenerateRecoveryCodes(e.ctx(), e.userID)
	require.NoError(t, err)
	require.Len(t, fresh, secondFactorModel.RecoveryCodeCount)

	_, _, err = e.uc.SignInWithRecoveryCode(context.Background(), e.email, factorPass, old[1], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)
	_, _, err = e.uc.SignInWithRecoveryCode(context.Background(), e.email, factorPass, fresh[1], "", authModel.SessionMetadata{})
	require.NoError(t, err)
}

func TestAStepUpOlderThanFiveMinutesIsNotFresh(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	require.NoError(t, err)
	_, err = e.db.Pool.Exec(context.Background(), `UPDATE sessions SET step_up_at = now() - interval '6 minutes' WHERE id = $1`, e.session)
	require.NoError(t, err)
	state, err := e.uc.StepUpState(context.Background(), e.session)
	require.NoError(t, err)
	require.False(t, state.Fresh(time.Now(), secondFactorModel.StepUpWindow))
}

// A secret sealed under a platform key that was rotated out is sealed again under the current one on its next use.
func TestTheAuthenticatorSecretMovesToTheCurrentPlatformKey(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol()
	rotated := e.cipher("k2:" + factorKeyNew + ",k1:" + factorKeyOld)
	e.uc.SetSecondFactorCipher(rotated)

	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 1)})
	require.NoError(t, err)
	var sealed string
	require.NoError(t, e.db.Pool.QueryRow(context.Background(), `SELECT secret_ciphertext FROM user_totp WHERE user_id = $1`, e.userID).Scan(&sealed))
	require.False(t, rotated.NeedsRotation(sealed), "sealed again under k2: %s", sealed[:6])
}

func TestAPasskeyStepUpNeedsUserVerificationAndIsASecondFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.BeginPasskeyStepUp(e.ctx(), e.userID)
	wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)

	a := newSoftAuthenticator(t, passkeyOrigin, passkeyRPID)
	a.userHandle = e.userID.Bytes()
	reg, err := e.uc.BeginPasskeyRegistration(e.signedIn(0), e.userID, factorPass)
	require.NoError(t, err)
	_, err = e.uc.FinishPasskeyRegistration(e.signedIn(0), e.userID, reg.CeremonyID, "Laptop", a.create(reg.Options))
	require.NoError(t, err)

	a.uv = false
	ceremony, err := e.uc.BeginPasskeyStepUp(e.ctx(), e.userID)
	require.NoError(t, err)
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPasskey, CeremonyID: ceremony.CeremonyID, Credential: a.get(ceremony.Options)})
	wantErr(t, err, secondFactorModel.ErrStepUpFailed)

	a.uv = true
	ceremony, err = e.uc.BeginPasskeyStepUp(e.ctx(), e.userID)
	require.NoError(t, err)
	s, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPasskey, CeremonyID: ceremony.CeremonyID, Credential: a.get(ceremony.Options)})
	require.NoError(t, err)
	require.True(t, s.Method.IsSecondFactor())

	// The same ceremony does not confirm twice.
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPasskey, CeremonyID: ceremony.CeremonyID, Credential: a.get(ceremony.Options)})
	require.Error(t, err)
}

func TestTheLastPasskeyOfAKeyHolderIsKept(t *testing.T) {
	e := newFactorEnv(t, true)
	a := newSoftAuthenticator(t, passkeyOrigin, passkeyRPID)
	a.userHandle = e.userID.Bytes()
	reg, err := e.uc.BeginPasskeyRegistration(e.signedIn(0), e.userID, factorPass)
	require.NoError(t, err)
	key, err := e.uc.FinishPasskeyRegistration(e.signedIn(0), e.userID, reg.CeremonyID, "Laptop", a.create(reg.Options))
	require.NoError(t, err)

	e.keys.holds = true
	wantErr(t, e.uc.RemovePasskey(e.ctx(), e.userID, key.ID, factorPass), secondFactorModel.ErrLastFactor)

	e.enrol() // a second factor: the passkey may go
	require.NoError(t, e.uc.RemovePasskey(e.ctx(), e.userID, key.ID, factorPass))
}
