package auth

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/repository/passkeyRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	"github.com/cybericebox/backend/internal/delivery/repository/secondFactorRepo"
	"github.com/cybericebox/backend/internal/limits"
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

// SetSecondFactorCipher sets the platform key (PLATFORM_SECRETS_KEY): it seals the authenticator secrets at rest and
// keys the recovery-code HMAC. Without it no authenticator app or recovery code works.
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

// SetFactorUnitOfWork gives the factor changes their transaction: the user row is locked, so the last-factor guard and
// the removal are one decision. Without it (isolated tests) the changes run without one.
func (u *AuthUseCase) SetFactorUnitOfWork(unit postgres.IUnitOfWorker[IRepository]) {
	u.factorUnit = unit
}

// TOTPEnrolment is what the person needs to add the account to an authenticator app: the URI the QR code carries and
// the same secret in base32 for typing in.
type TOTPEnrolment struct {
	Secret string
	URI    string
}

func (u *AuthUseCase) requireFactorCipher() error {
	if u.factorCipher == nil {
		return model.ErrPlatform.WithMessage("The second factor is not configured (PLATFORM_SECRETS_KEY)").Err()
	}
	return nil
}

// totpLockout is the persistent lockout of the authenticator codes: the sign-in failure limits (LIMIT_SIGN_IN_*, the
// failure window included).
func totpLockout() secondFactorRepo.Lockout {
	c := limits.Get()
	return secondFactorRepo.Lockout{
		MaxFailures: c.SignInMaxFailures, LockBase: c.SignInLockBase, LockMax: c.SignInLockMax, Window: c.SignInFailureWindow,
	}
}

// currentSession is the session of the request; a request without one cannot confirm anything.
func currentSession(ctx context.Context) (uuid.UUID, error) {
	claims, ok := rbac.CurrentUserSessionFromContext(ctx)
	if !ok || claims.SessionID == uuid.Nil {
		return uuid.Nil, authModel.ErrAuthReauthRequired.Err()
	}
	return claims.SessionID, nil
}

func (u *AuthUseCase) journal(ctx context.Context, action secondFactorModel.JournalAction, userID uuid.UUID, reason string) {
	if err := u.factorJournal.RecordFactorEvent(ctx, secondFactorModel.JournalEvent{
		Action: action, UserID: userID, At: time.Now(), Reason: reason,
	}); err != nil {
		log.Error().Err(err).Str("action", string(action)).Str("user_id", userID.String()).Msg("Failed to journal a second-factor event")
	}
}

// factorTx is the data a factor change works on: in the transaction of inFactorChange when there is one.
type factorTx struct {
	factors  *secondFactorRepo.Repository
	passkeys *passkeyRepo.Repository
}

// inFactorChange runs a change of the factors of the person in one transaction with their user row locked (SELECT ...
// FOR UPDATE): two concurrent removals cannot both pass the last-factor guard.
func (u *AuthUseCase) inFactorChange(ctx context.Context, userID uuid.UUID, change func(context.Context, factorTx) error) error {
	if u.factorUnit == nil {
		return change(ctx, factorTx{factors: u.factors, passkeys: u.passkeys})
	}
	txCtx, repo, unit, err := u.factorUnit.UnitOfWork(ctx)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to begin the factor change").Err()
	}
	defer unit.Restore()
	tx := factorTx{factors: secondFactorRepo.New(repo), passkeys: passkeyRepo.New(repo)}
	if err = tx.factors.LockUser(txCtx, userID); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to lock the account").Err()
	}
	if err = change(txCtx, tx); err != nil {
		return err
	}
	if err = unit.Save(); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to save the factor change").Err()
	}
	return nil
}

// HasSecondFactor reports a confirmed authenticator app or a passkey that counts (user verification, not compromised).
// Google sign-in, email codes and SMS never count. The access evaluator asks it for keys with the second_factor flag.
func (u *AuthUseCase) HasSecondFactor(ctx context.Context, userID uuid.UUID) (bool, error) {
	n, err := countFactors(ctx, u.factors, userID)
	return n > 0, err
}

// countFactors counts the second factors of the person: every passkey that counts, plus one for a confirmed
// authenticator app.
func countFactors(ctx context.Context, f *secondFactorRepo.Repository, userID uuid.UUID) (int, error) {
	passkeys, err := f.FactorPasskeys(ctx, userID)
	if err != nil {
		return 0, model.ErrPlatform.WithError(err).WithMessage("Failed to count passkeys").Err()
	}
	t, found, err := f.GetTOTP(ctx, userID)
	if err != nil {
		return 0, model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if found && t.ConfirmedAt != nil {
		passkeys++
	}
	return passkeys, nil
}

// guardLastFactor refuses removing a factor that is the last one of a holder of a second-factor key. Removing
// something that is no factor (a compromised passkey) is never refused here.
func (u *AuthUseCase) guardLastFactor(ctx context.Context, tx factorTx, userID uuid.UUID, removingFactor bool, why string) error {
	if !removingFactor {
		return nil
	}
	holds, err := u.factorKeys.HoldsSecondFactorKey(ctx, userID)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to check the keys of the account").Err()
	}
	if !holds {
		return nil
	}
	n, err := countFactors(ctx, tx.factors, userID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return secondFactorModel.ErrLastFactor.WithError(errors.New(why)).Err()
	}
	return nil
}

// afterFactorChange runs in the transaction of every factor change: the recovery codes go with the last factor (they
// back up a factor, they are no credential of their own), and every step-up of the person is forgotten, so a
// confirmation made before the change (with the password, or with the factor that went) confirms nothing after it.
func afterFactorChange(ctx context.Context, tx factorTx, userID uuid.UUID) error {
	n, err := countFactors(ctx, tx.factors, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		if err = tx.factors.DeleteRecoveryCodes(ctx, userID); err != nil {
			return model.ErrPlatform.WithError(err).WithMessage("Failed to delete the recovery codes").Err()
		}
	}
	if err = tx.factors.ClearStepUps(ctx, userID); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to clear the step-ups").Err()
	}
	return nil
}

// stepUpOf is the last step-up of a session.
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

// requireFactorChangeStepUp is the confirmation a change of the factors of an account that has one needs: a step-up on
// this session in the last StepUpWindow, made with an existing factor (or a recovery code standing in for a lost one).
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

// confirmNewFactor is the confirmation that adds a factor. An account that has one confirms with it
// (requireFactorChangeStepUp); only the first factor may be confirmed with a fresh step-up of any kind, the password,
// or for an account without a password a sign-in of the last RecentSignInWindow.
func (u *AuthUseCase) confirmNewFactor(ctx context.Context, userID uuid.UUID, currentPassword string) error {
	has, err := u.HasSecondFactor(ctx, userID)
	if err != nil {
		return err
	}
	if has {
		return u.requireFactorChangeStepUp(ctx, userID)
	}
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
	return u.reauthenticateWithin(ctx, userID, currentPassword, secondFactorModel.RecentSignInWindow)
}

// BeginTOTPEnrolment starts adding an authenticator app: a new secret is sealed and stored unconfirmed, and its URI is
// returned for the QR code. A setup started earlier and never confirmed is replaced; a confirmed app is refused.
func (u *AuthUseCase) BeginTOTPEnrolment(ctx context.Context, userID uuid.UUID, currentPassword string) (TOTPEnrolment, error) {
	if err := u.requireFactorCipher(); err != nil {
		return TOTPEnrolment{}, err
	}
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if found && t.ConfirmedAt != nil {
		return TOTPEnrolment{}, secondFactorModel.ErrTOTPAlreadyEnabled.Err()
	}
	if err = u.confirmNewFactor(ctx, userID, currentPassword); err != nil {
		return TOTPEnrolment{}, err
	}
	user, err := u.users.GetByID(ctx, userID)
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get user").Err()
	}
	key, err := totp.Default.New(relyingPartyName, user.Email)
	if err != nil {
		return TOTPEnrolment{}, model.ErrPlatform.WithError(err).WithMessage("Failed to create the authenticator secret").Err()
	}
	sealed, err := u.factorCipher.EncryptWithContext([]byte(key.Secret), secretctx.TOTPSecret(userID))
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
	return TOTPEnrolment{Secret: key.Secret, URI: key.URI}, nil
}

// openTOTP opens the sealed secret of a stored authenticator.
func (u *AuthUseCase) openTOTP(userID uuid.UUID, t secondFactorRepo.TOTP) (string, error) {
	raw, err := u.factorCipher.DecryptWithContext(t.SealedSecret, secretctx.TOTPSecret(userID))
	if err != nil {
		return "", model.ErrPlatform.WithError(err).WithMessage("Failed to open the authenticator secret").Err()
	}
	return string(raw), nil
}

// reserveTOTPAttempt counts one code check in the user_totp row before the code is compared (one conditional UPDATE),
// so parallel guesses cannot all slip past the lockout: at most the configured number of checks are evaluated before
// the checks lock, on every replica. While they are locked it answers 429 with the wait. A right code resets the count.
func (u *AuthUseCase) reserveTOTPAttempt(ctx context.Context, userID uuid.UUID) error {
	reserved, err := u.factors.ReserveTOTPAttempt(ctx, userID, totpLockout(), time.Now())
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to count the authenticator check").Err()
	}
	if reserved {
		return nil
	}
	wait := totpLockout().LockBase
	if t, found, gErr := u.factors.GetTOTP(ctx, userID); gErr == nil && found && t.LockedUntil != nil {
		wait = time.Until(*t.LockedUntil)
	}
	return tooManyRequests(wait)
}

// refundTOTPAttempt gives the reserved check back: a right code of a step already used is no guess.
func (u *AuthUseCase) refundTOTPAttempt(ctx context.Context, userID uuid.UUID) {
	if err := u.factors.RefundTOTPAttempt(ctx, userID, totpLockout()); err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("Failed to give an authenticator check back")
	}
}

// FinishTOTPEnrolment confirms the setup with the first code of the app. When the app is the first factor of the
// account it also issues the recovery codes (shown once, replacing any older set); a further factor leaves the codes
// as they are (POST /auth/recovery-codes makes new ones) and returns none. An account that already has a factor needs
// a step-up made with it here too, not only at the start.
func (u *AuthUseCase) FinishTOTPEnrolment(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	if err := u.requireFactorCipher(); err != nil {
		return nil, err
	}
	if _, err := currentSession(ctx); err != nil {
		return nil, err
	}
	has, err := u.HasSecondFactor(ctx, userID)
	if err != nil {
		return nil, err
	}
	if has {
		if err = u.requireFactorChangeStepUp(ctx, userID); err != nil {
			return nil, err
		}
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
	secretB32, err := u.openTOTP(userID, t)
	if err != nil {
		return nil, err
	}
	if err = u.reserveTOTPAttempt(ctx, userID); err != nil {
		return nil, err
	}
	now := time.Now()
	step, ok := totp.Default.Match(secretB32, code, now, secondFactorModel.TOTPSkew, 0)
	if !ok {
		return nil, secondFactorModel.ErrTOTPInvalidCode.Err()
	}
	var codes []string
	err = u.inFactorChange(ctx, userID, func(ctx context.Context, tx factorTx) error {
		n, cErr := countFactors(ctx, tx.factors, userID)
		if cErr != nil {
			return cErr
		}
		confirmed, cErr := tx.factors.Confirm(ctx, userID, t.SealedSecret, step, now)
		if cErr != nil {
			return model.ErrPlatform.WithError(cErr).WithMessage("Failed to confirm the authenticator").Err()
		}
		if !confirmed {
			return secondFactorModel.ErrTOTPEnrolmentExpired.WithError(errors.New("totp setup changed meanwhile")).Err()
		}
		if n == 0 {
			if codes, cErr = u.issueRecoveryCodes(ctx, tx.factors, userID); cErr != nil {
				return cErr
			}
		}
		return afterFactorChange(ctx, tx, userID)
	})
	if err != nil {
		return nil, err
	}
	u.journal(ctx, secondFactorModel.ActionTOTPEnrolled, userID, "")
	return codes, nil
}

// RemoveTOTP removes the authenticator app. It needs a fresh step-up made with a factor (or a recovery code) and is
// refused when it is the last factor of a holder of a second-factor key; with the last factor the recovery codes go.
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
	err = u.inFactorChange(ctx, userID, func(ctx context.Context, tx factorTx) error {
		if gErr := u.guardLastFactor(ctx, tx, userID, true, "totp-remove: the last second factor of a holder of a second-factor key"); gErr != nil {
			return gErr
		}
		removed, dErr := tx.factors.DeleteTOTP(ctx, userID, true)
		if dErr != nil {
			return model.ErrPlatform.WithError(dErr).WithMessage("Failed to remove the authenticator").Err()
		}
		if !removed {
			return secondFactorModel.ErrTOTPNotEnabled.Err()
		}
		return afterFactorChange(ctx, tx, userID)
	})
	if err != nil {
		return err
	}
	u.journal(ctx, secondFactorModel.ActionTOTPRemoved, userID, "")
	return nil
}

// verifyTOTP checks a code of the confirmed authenticator app and takes its step, so the same code (or an older one)
// never works again. Every check is reserved in the row before the comparison (the lockout holds across replicas and
// under parallel guesses); a right code of a step already used is ErrTOTPCodeUsed and gives its check back (a double
// submit). A secret sealed under an older platform key is sealed
// again under the current one.
func (u *AuthUseCase) verifyTOTP(ctx context.Context, userID uuid.UUID, code string) error {
	if err := u.requireFactorCipher(); err != nil {
		return err
	}
	t, found, err := u.factors.GetTOTP(ctx, userID)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to read the authenticator").Err()
	}
	if !found || t.ConfirmedAt == nil {
		return secondFactorModel.ErrStepUpMethodNotAllowed.WithError(errors.New("no confirmed authenticator")).Err()
	}
	secretB32, err := u.openTOTP(userID, t)
	if err != nil {
		return err
	}
	if err = u.reserveTOTPAttempt(ctx, userID); err != nil {
		return err
	}
	now := time.Now()
	step, ok := totp.Default.Match(secretB32, code, now, secondFactorModel.TOTPSkew, t.LastStep)
	if !ok {
		if used, ok := totp.Default.Match(secretB32, code, now, secondFactorModel.TOTPSkew, 0); ok && used <= t.LastStep {
			u.refundTOTPAttempt(ctx, userID)
			return secondFactorModel.ErrTOTPCodeUsed.Err()
		}
		return secondFactorModel.ErrStepUpFailed.WithError(errors.New("step-up: wrong authenticator code")).Err()
	}
	taken, err := u.factors.UseStep(ctx, userID, step)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to record the authenticator code").Err()
	}
	if !taken {
		u.refundTOTPAttempt(ctx, userID)
		return secondFactorModel.ErrTOTPCodeUsed.WithError(errors.New("the step was taken by a concurrent request")).Err()
	}
	u.resealTOTP(ctx, userID, t.SealedSecret, []byte(secretB32))
	return nil
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
