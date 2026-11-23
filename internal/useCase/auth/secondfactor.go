package auth

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/model"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	"github.com/cybericebox/backend/internal/model/rbac"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
	"github.com/cybericebox/backend/internal/secretctx"
	"github.com/cybericebox/backend/pkg/secret"
	"github.com/cybericebox/backend/pkg/totp"
)

// ISecondFactorKeys says whether a person holds a key with the second_factor flag (access.*, roles.manage, the staff
// keys; §6.6 of the access-control design) at any scope. While they hold one, their last factor cannot be removed. The
// key catalogue and the grants (plan P1/P2) implement it; until they land noSecondFactorKeys answers "no" for everyone,
// so nothing is refused yet.
type ISecondFactorKeys interface {
	HoldsSecondFactorKey(ctx context.Context, userID uuid.UUID) (bool, error)
}

// IFactorJournal writes the account-security events of the second factor to the grant journal (access_grant_log, plan
// P2). Until the journal exists the use case logs them (logFactorJournal).
type IFactorJournal interface {
	RecordFactorEvent(ctx context.Context, e secondFactorModel.JournalEvent) error
}

type noSecondFactorKeys struct{}

func (noSecondFactorKeys) HoldsSecondFactorKey(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

type logFactorJournal struct{}

func (logFactorJournal) RecordFactorEvent(_ context.Context, e secondFactorModel.JournalEvent) error {
	log.Info().Str("action", string(e.Action)).Str("user_id", e.UserID.String()).Time("at", e.At).
		Str("reason", e.Reason).Msg("Second factor journal event")
	return nil
}

// SetSecondFactorCipher sets the key the authenticator secrets are sealed with at rest (PLATFORM_SECRETS_KEY). Without
// it no authenticator app can be set up or used.
func (u *AuthUseCase) SetSecondFactorCipher(cipher *secret.Cipher) { u.factorCipher = cipher }

// SetSecondFactorKeys plugs in the check of the second-factor keys (plan P2); nil keeps the stub.
func (u *AuthUseCase) SetSecondFactorKeys(keys ISecondFactorKeys) {
	if keys == nil {
		keys = noSecondFactorKeys{}
	}
	u.factorKeys = keys
}

// SetFactorJournal plugs in the grant journal (plan P2); nil keeps the log.
func (u *AuthUseCase) SetFactorJournal(j IFactorJournal) {
	if j == nil {
		j = logFactorJournal{}
	}
	u.factorJournal = j
}

// TOTPEnrolment is what the person needs to add the account to an authenticator app: the URI the QR code carries and
// the same secret in base32 for typing in.
type TOTPEnrolment struct {
	Secret string
	URI    string
}

func (u *AuthUseCase) requireFactorCipher() error {
	if u.factorCipher == nil {
		return model.ErrPlatform.WithMessage("Authenticator apps are not configured (PLATFORM_SECRETS_KEY)").Err()
	}
	return nil
}

// currentSession is the session of the request; a request without one cannot confirm anything.
func currentSession(ctx context.Context) (uuid.UUID, error) {
	claims, ok := rbac.CurrentUserSessionFromContext(ctx)
	if !ok || claims.SessionID == uuid.Nil {
		return uuid.Nil, authModel.ErrAuthReauthRequired.Err()
	}
	return claims.SessionID, nil
}

// HasSecondFactor reports a confirmed authenticator app or a passkey that counts (user verification, not compromised).
// Google sign-in, email codes and SMS never count. The access evaluator asks it for keys with the second_factor flag.
func (u *AuthUseCase) HasSecondFactor(ctx context.Context, userID uuid.UUID) (bool, error) {
	n, err := u.countFactors(ctx, userID)
	return n > 0, err
}

// countFactors counts the second factors of the person: every passkey that counts, plus one for a confirmed
// authenticator app.
func (u *AuthUseCase) countFactors(ctx context.Context, userID uuid.UUID) (int, error) {
	passkeys, err := u.factors.FactorPasskeys(ctx, userID)
	if err != nil {
		return 0, model.ErrPlatform.WithError(err).WithMessage("Failed to count passkeys").Err()
	}
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return 0, model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if found && t.ConfirmedAt != nil {
		passkeys++
	}
	return passkeys, nil
}

// guardLastFactor refuses a removal that would leave a holder of a second-factor key with no factor at all.
func (u *AuthUseCase) guardLastFactor(ctx context.Context, userID uuid.UUID, why string) error {
	holds, err := u.factorKeys.HoldsSecondFactorKey(ctx, userID)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to check the keys of the account").Err()
	}
	if !holds {
		return nil
	}
	n, err := u.countFactors(ctx, userID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return secondFactorModel.ErrLastFactor.WithError(errors.New(why)).Err()
	}
	return nil
}

// stepUpOf is the last step-up of the current session.
func (u *AuthUseCase) stepUpOf(ctx context.Context, sessionID uuid.UUID) (secondFactorModel.StepUp, error) {
	s, err := u.factors.StepUp(ctx, sessionID)
	if err != nil {
		return secondFactorModel.StepUp{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the step-up").Err()
	}
	return s, nil
}

// StepUpState is the last step-up of a session (protection.RequireStepUp).
func (u *AuthUseCase) StepUpState(ctx context.Context, sessionID uuid.UUID) (secondFactorModel.StepUp, error) {
	return u.stepUpOf(ctx, sessionID)
}

// requireFactorChangeStepUp is the confirmation a change of the factors needs: a step-up on this session in the last
// StepUpWindow, made with a second factor or a recovery code when the account has a second factor.
func (u *AuthUseCase) requireFactorChangeStepUp(ctx context.Context, userID uuid.UUID) error {
	sessionID, err := currentSession(ctx)
	if err != nil {
		return err
	}
	s, err := u.stepUpOf(ctx, sessionID)
	if err != nil {
		return err
	}
	if !s.Fresh(time.Now(), secondFactorModel.StepUpWindow) {
		return secondFactorModel.ErrStepUpRequired.Err()
	}
	has, err := u.HasSecondFactor(ctx, userID)
	if err != nil {
		return err
	}
	if has && !s.Method.ConfirmsFactorChange() {
		return secondFactorModel.ErrStepUpRequired.WithError(errors.New("the step-up was not made with a second factor")).Err()
	}
	return nil
}

// confirmOwnerForEnrolment lets a person start a new factor: a fresh step-up of this session (any method; a recovery-code
// sign-in counts, so a lost factor can be replaced) or, as for adding a passkey, the password (a recent sign-in for an
// account without one).
func (u *AuthUseCase) confirmOwnerForEnrolment(ctx context.Context, userID uuid.UUID, currentPassword string) error {
	sessionID, err := currentSession(ctx)
	if err != nil {
		return err
	}
	s, err := u.stepUpOf(ctx, sessionID)
	if err != nil {
		return err
	}
	if s.Fresh(time.Now(), secondFactorModel.StepUpWindow) {
		return nil
	}
	return u.reauthenticate(ctx, userID, currentPassword)
}

// BeginTOTPEnrolment starts adding an authenticator app: a new secret is sealed and stored unconfirmed, and its URI is
// returned for the QR code. A setup started earlier and never confirmed is replaced; a confirmed app is refused.
func (u *AuthUseCase) BeginTOTPEnrolment(ctx context.Context, userID uuid.UUID, currentPassword string) (TOTPEnrolment, error) {
	if err := u.requireFactorCipher(); err != nil {
		return TOTPEnrolment{}, err
	}
	if err := u.confirmOwnerForEnrolment(ctx, userID, currentPassword); err != nil {
		return TOTPEnrolment{}, err
	}
	user, err := u.users.GetByID(ctx, userID)
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get user").Err()
	}
	raw, err := totp.NewSecret()
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to create the authenticator secret").Err()
	}
	sealed, err := u.factorCipher.EncryptWithContext(raw, secretctx.TOTPSecret(userID))
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to seal the authenticator secret").Err()
	}
	started, err := u.factors.Start(ctx, userID, sealed, time.Now())
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to store the authenticator secret").Err()
	}
	if !started {
		return TOTPEnrolment{}, secondFactorModel.ErrTOTPAlreadyEnabled.Err()
	}
	return TOTPEnrolment{
		Secret: totp.EncodeSecret(raw),
		URI:    totp.URI(relyingPartyName, user.Email, raw, totp.Default),
	}, nil
}

// FinishTOTPEnrolment confirms the setup with the first code of the app and issues a new set of recovery codes (shown
// once; they replace any older set).
func (u *AuthUseCase) FinishTOTPEnrolment(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	if err := u.requireFactorCipher(); err != nil {
		return nil, err
	}
	if _, err := currentSession(ctx); err != nil {
		return nil, err
	}
	key := userID.String()
	if wait := u.limits.secondFactor.Locked(key); wait > 0 {
		return nil, tooManyRequests(wait)
	}
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	switch {
	case !found:
		return nil, secondFactorModel.ErrTOTPEnrolmentExpired.Err()
	case t.ConfirmedAt != nil:
		return nil, secondFactorModel.ErrTOTPAlreadyEnabled.Err()
	case time.Since(t.CreatedAt) > secondFactorModel.TOTPEnrolmentTTL:
		return nil, secondFactorModel.ErrTOTPEnrolmentExpired.WithError(errors.New("totp setup is too old")).Err()
	}
	raw, err := u.factorCipher.DecryptWithContext(t.SealedSecret, secretctx.TOTPSecret(userID))
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to open the authenticator secret").Err()
	}
	now := time.Now()
	step, ok := totp.Default.Match(raw, code, now, secondFactorModel.TOTPSkew, 0)
	if !ok {
		u.limits.secondFactor.Fail(key)
		return nil, secondFactorModel.ErrTOTPInvalidCode.Err()
	}
	confirmed, err := u.factors.Confirm(ctx, userID, t.SealedSecret, step, now)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to confirm the authenticator").Err()
	}
	if !confirmed {
		return nil, secondFactorModel.ErrTOTPEnrolmentExpired.WithError(errors.New("totp setup changed meanwhile")).Err()
	}
	u.limits.secondFactor.Reset(key)
	return u.issueRecoveryCodes(ctx, userID)
}

// RemoveTOTP removes the authenticator app. It needs a fresh step-up (with a second factor or a recovery code) and is
// refused when it is the last factor of a holder of a second-factor key.
func (u *AuthUseCase) RemoveTOTP(ctx context.Context, userID uuid.UUID) error {
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if !found || t.ConfirmedAt == nil {
		return secondFactorModel.ErrTOTPNotEnabled.Err()
	}
	if err = u.requireFactorChangeStepUp(ctx, userID); err != nil {
		return err
	}
	if err = u.guardLastFactor(ctx, userID, "totp-remove: the last second factor of a holder of a second-factor key"); err != nil {
		return err
	}
	removed, err := u.factors.DeleteTOTP(ctx, userID, true)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to remove the authenticator").Err()
	}
	if !removed {
		return secondFactorModel.ErrTOTPNotEnabled.Err()
	}
	return nil
}

// verifyTOTP checks a code of the confirmed authenticator app and takes its step, so the same code (or an older one)
// never works again. A secret sealed under an older platform key is sealed again under the current one.
func (u *AuthUseCase) verifyTOTP(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	if err := u.requireFactorCipher(); err != nil {
		return false, err
	}
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if !found || t.ConfirmedAt == nil {
		return false, secondFactorModel.ErrStepUpMethodNotAllowed.WithError(errors.New("no confirmed authenticator")).Err()
	}
	raw, err := u.factorCipher.DecryptWithContext(t.SealedSecret, secretctx.TOTPSecret(userID))
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to open the authenticator secret").Err()
	}
	step, ok := totp.Default.Match(raw, code, time.Now(), secondFactorModel.TOTPSkew, t.LastStep)
	if !ok {
		return false, nil
	}
	taken, err := u.factors.UseStep(ctx, userID, step)
	if err != nil {
		return false, model.ErrPlatform.WithError(err).WithMessage("Failed to record the authenticator code").Err()
	}
	if taken {
		u.resealTOTP(ctx, userID, t.SealedSecret, raw)
	}
	return taken, nil
}

// resealTOTP moves a secret sealed by an older platform key to the current one (keys check counts it). A failure is
// logged; the next use tries again.
func (u *AuthUseCase) resealTOTP(ctx context.Context, userID uuid.UUID, sealed string, raw []byte) {
	if !u.factorCipher.NeedsRotation(sealed) {
		return
	}
	fresh, err := u.factorCipher.EncryptWithContext(raw, secretctx.TOTPSecret(userID))
	if err == nil {
		err = u.factors.Reseal(ctx, userID, sealed, fresh)
	}
	if err != nil {
		log.Warn().Err(err).Msg("Failed to seal an authenticator secret again under the current platform key")
	}
}

// SecondFactorStatus is what the security settings show: the authenticator, the passkeys that count, the recovery
// codes left and the step-up of the current session.
func (u *AuthUseCase) SecondFactorStatus(ctx context.Context, userID uuid.UUID) (secondFactorModel.Status, error) {
	var st secondFactorModel.Status
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return st, model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if found && t.ConfirmedAt != nil {
		st.TOTPEnabled, st.TOTPConfirmedAt = true, t.ConfirmedAt
	}
	if st.Passkeys, err = u.factors.FactorPasskeys(ctx, userID); err != nil {
		return st, model.ErrPlatform.WithError(err).WithMessage("Failed to count passkeys").Err()
	}
	if st.RecoveryCodesLeft, err = u.factors.UnusedRecoveryCodes(ctx, userID); err != nil {
		return st, model.ErrPlatform.WithError(err).WithMessage("Failed to count recovery codes").Err()
	}
	st.HasSecondFactor = st.TOTPEnabled || st.Passkeys > 0
	if sessionID, sErr := currentSession(ctx); sErr == nil {
		s, sErr := u.stepUpOf(ctx, sessionID)
		if sErr != nil {
			return st, sErr
		}
		if !s.At.IsZero() {
			st.StepUp = &s
		}
	}
	return st, nil
}
