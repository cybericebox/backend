package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	"github.com/cybericebox/backend/internal/delivery/repository/sessionRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/userRepo"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	dispatchModel "github.com/cybericebox/backend/internal/model/notification/dispatch"
	notificationTypes "github.com/cybericebox/backend/internal/model/notification/types"
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

type keysStub struct {
	mu    sync.Mutex
	holds bool
}

func (k *keysStub) set(v bool) { k.mu.Lock(); k.holds = v; k.mu.Unlock() }
func (k *keysStub) HoldsSecondFactorKey(context.Context, uuid.UUID) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.holds, nil
}

type journalStub struct {
	mu     sync.Mutex
	events []secondFactorModel.JournalEvent
}

func (j *journalStub) RecordFactorEvent(_ context.Context, e secondFactorModel.JournalEvent) error {
	j.mu.Lock()
	j.events = append(j.events, e)
	j.mu.Unlock()
	return nil
}

func (j *journalStub) actions() []secondFactorModel.JournalAction {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]secondFactorModel.JournalAction, 0, len(j.events))
	for _, e := range j.events {
		out = append(out, e.Action)
	}
	return out
}

// mailSpy records the last notice and the channels it was forced to.
type mailSpy struct {
	last   notificationTypes.NotificationPayload
	forced []notificationTypes.NotificationChannel
}

func (m *mailSpy) Notify(_ context.Context, _ uuid.UUID, n notificationTypes.NotificationPayload, opts ...dispatchModel.NotifyOption) error {
	m.last, m.forced = n, dispatchModel.ApplyNotifyOptions(opts).OverrideChannels
	return nil
}

// factorEnv is the auth use case on a real database with one person and one session of theirs.
type factorEnv struct {
	t       *testing.T
	db      *testhelpers.TestDB
	uc      *auth.AuthUseCase
	mail    *mailSpy
	journal *journalStub
	keys    *keysStub
	userID  uuid.UUID
	email   string
	session uuid.UUID
}

func (e *factorEnv) newUC() *auth.AuthUseCase {
	uc := auth.NewAuthUseCase(auth.Dependencies{
		Repo:     e.db.Queries,
		Token:    token.MustNew(token.Config{TokenSignature: "test-signing-key-that-is-long-enough"}),
		Password: password.New(password.Config{HashCost: 4}),
		Notifier: e.mail,
		Sessions: testSessions(e.t),
		Config: config.AuthConfig{
			SessionIdleTTL: time.Hour, TemporalCodeTTL: time.Hour, SignupSetupTokenTTL: time.Hour,
			Hosts: testHosts("example.test"),
		},
	})
	uc.SetSecondFactorCipher(e.cipher("k1:" + factorKeyOld))
	uc.SetSecondFactorKeys(e.keys)
	uc.SetFactorJournal(e.journal)
	uc.SetFactorUnitOfWork(postgres.NewUnitOfWorker[auth.IRepository](postgres.NewUoWFactory(e.db.Pool)))
	return uc
}

func newFactorEnv(t *testing.T, withPassword bool) *factorEnv {
	t.Helper()
	ctx := context.Background()
	e := &factorEnv{t: t, db: testhelpers.SetupTestDB(t), mail: &mailSpy{}, journal: &journalStub{}, keys: &keysStub{}}
	e.uc = e.newUC()
	e.userID, e.email = uuid.Must(uuid.NewV7()), "ann@example.test"
	user := userModel.NewGoogleUser(e.userID, e.email, "Ann", "Lee", time.Now())
	if withPassword {
		hashed, err := password.New(password.Config{HashCost: 4}).Hash(factorPass)
		require.NoError(t, err)
		user.SetPassword(hashed, time.Now())
	}
	_, err := userRepo.New(e.db.Queries).Create(ctx, user)
	require.NoError(t, err)
	s, err := sessionRepo.New(e.db.Queries).Create(ctx, authModel.NewSession(e.userID, authModel.SessionMetadata{}, time.Hour, time.Now()))
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

// code is the authenticator code offset steps from now.
func code(t *testing.T, enrolment auth.TOTPEnrolment, offset int64) string {
	t.Helper()
	c, err := totp.Default.Code(enrolment.Secret, totp.Default.Step(time.Now())+offset)
	require.NoError(t, err)
	return c
}

// enrol sets up the authenticator app as the first factor with the code of the current step.
func (e *factorEnv) enrol(ctx context.Context, pass string) (auth.TOTPEnrolment, []string) {
	e.t.Helper()
	enrolment, err := e.uc.BeginTOTPEnrolment(ctx, e.userID, pass)
	require.NoError(e.t, err)
	codes, err := e.uc.FinishTOTPEnrolment(ctx, e.userID, code(e.t, enrolment, 0))
	require.NoError(e.t, err)
	return enrolment, codes
}

func (e *factorEnv) totpStepUp(enrolment auth.TOTPEnrolment, offset int64) error {
	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(e.t, enrolment, offset)})
	return err
}

func (e *factorEnv) stepUp() secondFactorModel.StepUp {
	s, err := e.uc.StepUpState(context.Background(), e.session)
	require.NoError(e.t, err)
	return s
}

func (e *factorEnv) count(sql string) int {
	var n int
	require.NoError(e.t, e.db.Pool.QueryRow(context.Background(), sql, e.userID).Scan(&n))
	return n
}

func (e *factorEnv) addPasskey(ctx context.Context) (*softAuthenticator, uuid.UUID) {
	e.t.Helper()
	a := newSoftAuthenticator(e.t, passkeyOrigin, passkeyRPID)
	a.userHandle = e.userID.Bytes()
	reg, err := e.uc.BeginPasskeyRegistration(ctx, e.userID, factorPass)
	require.NoError(e.t, err)
	key, err := e.uc.FinishPasskeyRegistration(ctx, e.userID, reg.CeremonyID, "Laptop", a.create(reg.Options))
	require.NoError(e.t, err)
	return a, key.ID
}

func (e *factorEnv) passkeyStepUp(a *softAuthenticator) error {
	ceremony, err := e.uc.BeginPasskeyStepUp(e.ctx(), e.userID)
	require.NoError(e.t, err)
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPasskey, CeremonyID: ceremony.CeremonyID, Credential: a.get(ceremony.Options)})
	return err
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
	require.Equal(t, enrolment.Secret, string(raw))

	status, err := e.uc.SecondFactorStatus(e.ctx(), e.userID)
	require.NoError(t, err)
	require.False(t, status.HasSecondFactor, "an unconfirmed setup is no factor")

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
	require.Contains(t, e.journal.actions(), secondFactorModel.ActionTOTPEnrolled)

	_, err = e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, factorPass)
	wantErr(t, err, secondFactorModel.ErrTOTPAlreadyEnabled)
}

// The first factor clears the step-ups made before it: a password step-up confirms nothing once a factor exists.
func TestTheFirstFactorClearsEarlierStepUps(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	require.NoError(t, err)
	e.enrol(e.ctx(), factorPass)
	require.True(t, e.stepUp().At.IsZero(), "the password step-up is gone")
}

func TestTOTPStepUpRefusesAReusedStepWithoutCountingItAndLocksInTheRow(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass) // takes the current step

	// The just-used code again: a distinct answer, and no failure counted.
	wantErr(t, e.totpStepUp(enrolment, 0), secondFactorModel.ErrTOTPCodeUsed)
	require.Zero(t, e.count(`SELECT failures FROM user_totp WHERE user_id = $1`))

	wantErr(t, e.totpStepUp(enrolment, 3), secondFactorModel.ErrStepUpFailed)
	require.Equal(t, 1, e.count(`SELECT failures FROM user_totp WHERE user_id = $1`))

	require.NoError(t, e.totpStepUp(enrolment, 1))
	require.Zero(t, e.count(`SELECT failures FROM user_totp WHERE user_id = $1`), "a right code resets the count")
	require.Equal(t, secondFactorModel.MethodTOTP, e.stepUp().Method)
	require.True(t, e.stepUp().Fresh(time.Now(), secondFactorModel.StepUpWindow))
	wantErr(t, e.totpStepUp(enrolment, 1), secondFactorModel.ErrTOTPCodeUsed)

	// Five wrong codes lock the authenticator, for every replica: a second use case on the same database is locked too.
	for range 5 {
		wantErr(t, e.totpStepUp(enrolment, 4), secondFactorModel.ErrStepUpFailed)
	}
	other := e.newUC()
	_, err := other.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodTOTP, Code: code(t, enrolment, 0)})
	wantErr(t, err, authModel.ErrAuthTooManyRequests)
	// The password checks have their own lockout: the locked authenticator does not lock them.
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)
}

func TestPasswordStepUpOnlyForAnAccountWithoutAFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	s, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodPassword, s.Method)

	e.enrol(e.ctx(), factorPass)
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)

	for _, m := range []secondFactorModel.Method{"google", "email-code", "sms", secondFactorModel.MethodRecoveryCode} {
		_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: m, Code: "123456"})
		wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)
	}
}

// A Google-only account without a factor confirms with a sign-in of the last 5 minutes; Google itself is no factor.
func TestGoogleOnlyAccountHasNoFactorAndConfirmsWithASignInOfTheLastFiveMinutes(t *testing.T) {
	e := newFactorEnv(t, false)
	has, err := e.uc.HasSecondFactor(context.Background(), e.userID)
	require.NoError(t, err)
	require.False(t, has)

	_, err = e.uc.StepUp(e.signedIn(10*time.Minute), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword})
	wantErr(t, err, authModel.ErrAuthReauthRequired)
	s, err := e.uc.StepUp(e.signedIn(time.Minute), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword})
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodRecentSignIn, s.Method)
}

// With a factor, another one is added only with a step-up made with the existing one: never the password.
func TestASecondFactorIsAddedOnlyWithAStepUpOfTheFirst(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)

	_, err := e.uc.BeginPasskeyRegistration(e.signedIn(0), e.userID, factorPass)
	wantErr(t, err, secondFactorModel.ErrStepUpRequired)

	require.NoError(t, e.totpStepUp(enrolment, 1))
	_, key := e.addPasskey(e.signedIn(0))
	require.NotEqual(t, uuid.Nil, key)
	require.True(t, e.stepUp().At.IsZero(), "adding a factor clears the step-ups")
	require.Contains(t, e.journal.actions(), secondFactorModel.ActionPasskeyAdded)
}

// A further authenticator (after a passkey) needs the factor step-up at both ends and leaves the recovery codes alone.
func TestAFurtherAuthenticatorNeedsAStepUpAndIssuesNoCodes(t *testing.T) {
	e := newFactorEnv(t, true)
	a, _ := e.addPasskey(e.signedIn(0))
	_, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, factorPass)
	wantErr(t, err, secondFactorModel.ErrStepUpRequired)

	require.NoError(t, e.passkeyStepUp(a))
	enrolment, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, "")
	require.NoError(t, err)
	codes, err := e.uc.FinishTOTPEnrolment(e.ctx(), e.userID, code(t, enrolment, 0))
	require.NoError(t, err)
	require.Empty(t, codes, "not the first factor: no new codes")
}

func TestRemovingTheLastFactorNeedsAFactorStepUpKeepsAKeyHolderAndDropsTheCodes(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)
	wantErr(t, e.uc.RemoveTOTP(e.ctx(), e.userID), secondFactorModel.ErrStepUpRequired)

	require.NoError(t, e.totpStepUp(enrolment, 1))
	e.keys.set(true)
	wantErr(t, e.uc.RemoveTOTP(e.ctx(), e.userID), secondFactorModel.ErrLastFactor)

	e.keys.set(false)
	require.NoError(t, e.uc.RemoveTOTP(e.ctx(), e.userID))
	has, err := e.uc.HasSecondFactor(context.Background(), e.userID)
	require.NoError(t, err)
	require.False(t, has)
	require.Zero(t, e.count(`SELECT count(*) FROM user_recovery_codes WHERE user_id = $1`), "the codes go with the last factor")
	require.True(t, e.stepUp().At.IsZero())
	require.Contains(t, e.journal.actions(), secondFactorModel.ActionTOTPRemoved)
	wantErr(t, e.uc.RemoveTOTP(e.ctx(), e.userID), secondFactorModel.ErrTOTPNotEnabled)
}

func TestARecoveryCodeSignsInOnceIsJournaledAndMailedAndAllowsReplacingTheFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	_, codes := e.enrol(e.ctx(), factorPass)

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

	require.Contains(t, e.journal.actions(), secondFactorModel.ActionRecoveryCodeUsed)
	mail, ok := e.mail.last.(notificationPayloads.RecoveryCodeUsedPayload)
	require.True(t, ok, "the owner is mailed: %T", e.mail.last)
	require.Equal(t, "9", mail.CodesLeft)
	require.Equal(t, []notificationTypes.NotificationChannel{notificationTypes.NotificationChannelEmail}, e.mail.forced,
		"no preference turns the notice off")

	// The new session may drop the lost app with its recovery step-up, which is never a second factor.
	pass, err := e.uc.OpenSession(cookie)
	require.NoError(t, err)
	recovered := rbac.ContextWithCurrentUserSession(context.Background(), rbac.Claims{
		UserID: e.userID, SessionID: pass.Ticket.SessionID, Role: rbac.RoleUser, SignedInAt: time.Now().Add(-time.Hour),
	})
	state, err := e.uc.StepUpState(context.Background(), pass.Ticket.SessionID)
	require.NoError(t, err)
	require.Equal(t, secondFactorModel.MethodRecoveryCode, state.Method)
	require.False(t, state.Method.IsSecondFactor())
	require.NoError(t, e.uc.RemoveTOTP(recovered, e.userID))

	// With no factor left the codes are gone and a recovery sign-in is refused; the new app is the first factor again.
	_, _, err = e.uc.SignInWithRecoveryCode(context.Background(), e.email, factorPass, codes[1], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)
	_, err = e.uc.BeginTOTPEnrolment(recovered, e.userID, factorPass)
	require.NoError(t, err)
}

// A recovery code is never the only credential: an account without a password cannot sign in with one.
func TestARecoveryCodeDoesNotSignInAnAccountWithoutAPassword(t *testing.T) {
	e := newFactorEnv(t, false)
	_, codes := e.enrol(e.signedIn(time.Minute), "")
	require.Len(t, codes, secondFactorModel.RecoveryCodeCount)
	_, _, err := e.uc.SignInWithRecoveryCode(context.Background(), e.email, "", codes[0], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)
	require.Equal(t, secondFactorModel.RecoveryCodeCount, e.count(`SELECT count(*) FROM user_recovery_codes WHERE user_id = $1 AND used_at IS NULL`))
}

// The codes are stored as an HMAC under the platform key and keep working after the key is rotated.
func TestRecoveryCodesAreKeyedHashesThatSurviveAKeyRotation(t *testing.T) {
	e := newFactorEnv(t, true)
	_, codes := e.enrol(e.ctx(), factorPass)
	var stored []byte
	require.NoError(t, e.db.Pool.QueryRow(context.Background(), `SELECT code_hash FROM user_recovery_codes WHERE user_id = $1 LIMIT 1`, e.userID).Scan(&stored))
	require.Len(t, stored, 32)

	e.uc.SetSecondFactorCipher(e.cipher("k2:" + factorKeyNew + ",k1:" + factorKeyOld))
	_, _, err := e.uc.SignInWithRecoveryCode(context.Background(), e.email, factorPass, codes[2], "", authModel.SessionMetadata{})
	require.NoError(t, err)
	e.uc.SetSecondFactorCipher(e.cipher("k3:" + strings.Repeat("ab", 32)))
	_, _, err = e.uc.SignInWithRecoveryCode(context.Background(), e.email, factorPass, codes[3], "", authModel.SessionMetadata{})
	wantErr(t, err, secondFactorModel.ErrRecoverySignInFailed)
}

func TestRecoveryCodesAreRegeneratedOnlyWithAFactorAndAStepUp(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.RegenerateRecoveryCodes(e.ctx(), e.userID)
	wantErr(t, err, secondFactorModel.ErrNoSecondFactor)

	enrolment, old := e.enrol(e.ctx(), factorPass)
	_, err = e.uc.RegenerateRecoveryCodes(e.ctx(), e.userID)
	wantErr(t, err, secondFactorModel.ErrStepUpRequired)

	require.NoError(t, e.totpStepUp(enrolment, 1))
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
	require.False(t, e.stepUp().Fresh(time.Now(), secondFactorModel.StepUpWindow))
}

func TestAPasswordChangeClearsTheStepUps(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPassword, Password: factorPass})
	require.NoError(t, err)
	require.NoError(t, e.uc.SetAccountPassword(e.ctx(), e.userID, factorPass, "Another!2"))
	require.True(t, e.stepUp().At.IsZero())
}

// A secret sealed under a platform key that was rotated out is sealed again under the current one on its next use.
func TestTheAuthenticatorSecretMovesToTheCurrentPlatformKey(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)
	rotated := e.cipher("k2:" + factorKeyNew + ",k1:" + factorKeyOld)
	e.uc.SetSecondFactorCipher(rotated)

	require.NoError(t, e.totpStepUp(enrolment, 1))
	var sealed string
	require.NoError(t, e.db.Pool.QueryRow(context.Background(), `SELECT secret_ciphertext FROM user_totp WHERE user_id = $1`, e.userID).Scan(&sealed))
	require.False(t, rotated.NeedsRotation(sealed), "sealed again under k2: %s", sealed[:6])
}

func TestAPasskeyStepUpNeedsUserVerificationAndIsASecondFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	_, err := e.uc.BeginPasskeyStepUp(e.ctx(), e.userID)
	wantErr(t, err, secondFactorModel.ErrStepUpMethodNotAllowed)

	a, _ := e.addPasskey(e.signedIn(0))
	a.uv = false
	wantErr(t, e.passkeyStepUp(a), secondFactorModel.ErrStepUpFailed)

	a.uv = true
	ceremony, err := e.uc.BeginPasskeyStepUp(e.ctx(), e.userID)
	require.NoError(t, err)
	s, err := e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPasskey, CeremonyID: ceremony.CeremonyID, Credential: a.get(ceremony.Options)})
	require.NoError(t, err)
	require.True(t, s.Method.IsSecondFactor())
	require.Contains(t, e.journal.actions(), secondFactorModel.ActionStepUp)

	// The same ceremony does not confirm twice.
	_, err = e.uc.StepUp(e.ctx(), e.userID, auth.StepUpRequest{Method: secondFactorModel.MethodPasskey, CeremonyID: ceremony.CeremonyID, Credential: a.get(ceremony.Options)})
	require.Error(t, err)
}

func TestTheLastPasskeyOfAKeyHolderIsKept(t *testing.T) {
	e := newFactorEnv(t, true)
	a, key := e.addPasskey(e.signedIn(0))

	wantErr(t, e.uc.RemovePasskey(e.ctx(), e.userID, key, factorPass), secondFactorModel.ErrStepUpRequired)
	require.NoError(t, e.passkeyStepUp(a))
	e.keys.set(true)
	wantErr(t, e.uc.RemovePasskey(e.ctx(), e.userID, key, factorPass), secondFactorModel.ErrLastFactor)

	enrolment, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, "") // the passkey step-up confirms the second factor
	require.NoError(t, err)
	_, err = e.uc.FinishTOTPEnrolment(e.ctx(), e.userID, code(t, enrolment, 0))
	require.NoError(t, err)
	require.NoError(t, e.totpStepUp(enrolment, 1))
	require.NoError(t, e.uc.RemovePasskey(e.ctx(), e.userID, key, factorPass))
	require.Contains(t, e.journal.actions(), secondFactorModel.ActionPasskeyRemoved)
}

// Two removals at once of a key holder's two factors: the user row lock lets one through, the other keeps the last.
func TestTwoConcurrentRemovalsKeepTheLastFactor(t *testing.T) {
	e := newFactorEnv(t, true)
	a, key := e.addPasskey(e.signedIn(0))
	require.NoError(t, e.passkeyStepUp(a))
	enrolment, err := e.uc.BeginTOTPEnrolment(e.ctx(), e.userID, "")
	require.NoError(t, err)
	_, err = e.uc.FinishTOTPEnrolment(e.ctx(), e.userID, code(t, enrolment, 0))
	require.NoError(t, err)
	require.NoError(t, e.totpStepUp(enrolment, 1))
	e.keys.set(true)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = e.uc.RemoveTOTP(e.ctx(), e.userID) }()
	go func() { defer wg.Done(); errs[1] = e.uc.RemovePasskey(e.ctx(), e.userID, key, "") }()
	wg.Wait()

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	require.Equal(t, 1, ok, "exactly one removal goes through: %v", errs)
	has, err := e.uc.HasSecondFactor(context.Background(), e.userID)
	require.NoError(t, err)
	require.True(t, has, "the key holder keeps a factor")
}

// Parallel wrong codes cannot slip past the lockout: each check is reserved in the row before the comparison, so no more
// than the configured five are evaluated; the others are refused as locked.
func TestParallelWrongCodesAreBoundedByTheLockout(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)

	const guesses = 20
	errs := make([]error, guesses)
	var wg sync.WaitGroup
	wg.Add(guesses)
	for i := range guesses {
		go func() {
			defer wg.Done()
			errs[i] = e.totpStepUp(enrolment, 4) // never a valid step
		}()
	}
	wg.Wait()

	evaluated, locked := 0, 0
	for _, err := range errs {
		switch {
		case errors.Is(err, secondFactorModel.ErrStepUpFailed.Err()):
			evaluated++
		case errors.Is(err, authModel.ErrAuthTooManyRequests.Err()):
			locked++
		default:
			t.Fatalf("unexpected answer: %v", err)
		}
	}
	require.LessOrEqual(t, evaluated, 5, "at most the threshold of checks is evaluated")
	require.Equal(t, guesses, evaluated+locked)
	require.Equal(t, evaluated, e.count(`SELECT failures FROM user_totp WHERE user_id = $1`))
	// Even the right code is refused while the checks are locked.
	wantErr(t, e.totpStepUp(enrolment, 1), authModel.ErrAuthTooManyRequests)
}

// A huge count neither overflows nor makes the lock longer than its maximum (the exponent is capped).
func TestAHugeFailureCountKeepsTheLockWithinItsMaximum(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)
	_, err := e.db.Pool.Exec(context.Background(),
		`UPDATE user_totp SET failures = 2147483647, last_failure_at = now(), locked_until = NULL WHERE user_id = $1`, e.userID)
	require.NoError(t, err)

	wantErr(t, e.totpStepUp(enrolment, 4), secondFactorModel.ErrStepUpFailed)
	var lockSecs float64
	require.NoError(t, e.db.Pool.QueryRow(context.Background(),
		`SELECT extract(epoch FROM locked_until - now()) FROM user_totp WHERE user_id = $1`, e.userID).Scan(&lockSecs))
	require.Greater(t, lockSecs, 0.0)
	require.LessOrEqual(t, lockSecs, (15*time.Minute).Seconds()+5, "LIMIT_SIGN_IN_LOCK_MAX bounds the lock")
	wantErr(t, e.totpStepUp(enrolment, 1), authModel.ErrAuthTooManyRequests)
}

// Failures are forgotten once both the last check and the end of the last lock are older than the quiet window (twice
// the longest lock, 30 minutes here): a guesser who stopped does not keep the owner locked for good.
func TestOldFailuresDecay(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)
	_, err := e.db.Pool.Exec(context.Background(), `UPDATE user_totp
SET failures = 1000, last_failure_at = now() - interval '1 hour', locked_until = now() - interval '31 minutes'
WHERE user_id = $1`, e.userID)
	require.NoError(t, err)

	wantErr(t, e.totpStepUp(enrolment, 4), secondFactorModel.ErrStepUpFailed)
	require.Equal(t, 1, e.count(`SELECT failures FROM user_totp WHERE user_id = $1`), "the count started again")
	require.Zero(t, e.count(`SELECT count(*) FROM user_totp WHERE user_id = $1 AND locked_until > now()`), "no new lock")
	require.NoError(t, e.totpStepUp(enrolment, 1))
	require.Zero(t, e.count(`SELECT failures FROM user_totp WHERE user_id = $1`))
}

// An attacker who guesses once each time the lock ends stays at the longest lock: waiting out a lock never resets the
// escalation back to the one-minute lock.
func TestGuessingAtEveryLockEndKeepsTheLongestLock(t *testing.T) {
	e := newFactorEnv(t, true)
	enrolment, _ := e.enrol(e.ctx(), factorPass)
	lockSecs := func() float64 {
		var s float64
		require.NoError(t, e.db.Pool.QueryRow(context.Background(),
			`SELECT extract(epoch FROM locked_until - now()) FROM user_totp WHERE user_id = $1`, e.userID).Scan(&s))
		return s
	}
	// Reach the longest lock: five checks lock for 1 min, each further one doubles up to 15 min.
	for range 5 {
		wantErr(t, e.totpStepUp(enrolment, 4), secondFactorModel.ErrStepUpFailed)
	}
	for range 5 {
		// The lock has just ended; the last check was when it began (15 minutes ago at the longest lock).
		_, err := e.db.Pool.Exec(context.Background(), `UPDATE user_totp
SET last_failure_at = now() - interval '15 minutes', locked_until = now() - interval '1 second' WHERE user_id = $1`, e.userID)
		require.NoError(t, err)
		wantErr(t, e.totpStepUp(enrolment, 4), secondFactorModel.ErrStepUpFailed)
	}
	for range 3 {
		_, err := e.db.Pool.Exec(context.Background(), `UPDATE user_totp
SET last_failure_at = now() - interval '15 minutes', locked_until = now() - interval '1 second' WHERE user_id = $1`, e.userID)
		require.NoError(t, err)
		wantErr(t, e.totpStepUp(enrolment, 4), secondFactorModel.ErrStepUpFailed)
		require.Greater(t, lockSecs(), (14 * time.Minute).Seconds(), "still the longest lock, never back to one minute")
	}
}
