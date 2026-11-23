package auth

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/repository/secondFactorRepo"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	"github.com/cybericebox/backend/internal/model"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	dispatchModel "github.com/cybericebox/backend/internal/model/notification/dispatch"
	notificationTypes "github.com/cybericebox/backend/internal/model/notification/types"
	notificationPayloads "github.com/cybericebox/backend/internal/model/notification/types/payloads"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
)

// recoveryKeys are the HMAC keys of the recovery codes, one per key of the platform keyring, the current one first.
func (u *AuthUseCase) recoveryKeys() ([][32]byte, error) {
	if err := u.requireFactorCipher(); err != nil {
		return nil, err
	}
	return u.factorCipher.DeriveKeys(secondFactorModel.RecoveryCodeKeyPurpose), nil
}

// issueRecoveryCodes makes a new set of codes, stores their HMACs (current platform key) in place of the old set and
// returns the codes: the only time they are ever shown.
func (u *AuthUseCase) issueRecoveryCodes(ctx context.Context, f *secondFactorRepo.Repository, userID uuid.UUID) ([]string, error) {
	keys, err := u.recoveryKeys()
	if err != nil {
		return nil, err
	}
	codes, err := secondFactorModel.NewRecoveryCodes()
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to create recovery codes").Err()
	}
	hashes := make([][]byte, 0, len(codes))
	for _, c := range codes {
		hashes = append(hashes, secondFactorModel.HashRecoveryCode(keys[0], userID, c))
	}
	if err = f.ReplaceRecoveryCodes(ctx, userID, hashes, time.Now()); err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to store recovery codes").Err()
	}
	return codes, nil
}

// RegenerateRecoveryCodes replaces the recovery codes with a new set. They back up a second factor, so the account needs
// one, and the change needs a fresh step-up made with it (or with a recovery code).
func (u *AuthUseCase) RegenerateRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	has, err := u.HasSecondFactor(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, secondFactorModel.ErrNoSecondFactor.Err()
	}
	if err = u.requireFactorChangeStepUp(ctx, userID); err != nil {
		return nil, err
	}
	return u.issueRecoveryCodes(ctx, u.factors, userID)
}

// SignInWithRecoveryCode signs in with a one-time recovery code when the second factor is lost. A recovery code is
// never the only credential: the account needs a password, and the password is checked first (an account without a
// password resets one by mail). It also needs a second factor, which the code stands in for: an account without one
// has no codes worth anything (they are deleted with the last factor). The new session carries a step-up of the kind
// "recovery-code", so the person can drop the lost factor, enrol a new one and get new codes, and nothing that needs a
// real second factor. The use is journaled and mailed to the owner (a security notice no preference turns off).
//
// Every failure answers the same ErrRecoverySignInFailed, after the same password hashing, and counts against the same
// per-account lockout as the password sign-in. A wrong password never uses a code up.
func (u *AuthUseCase) SignInWithRecoveryCode(
	ctx context.Context,
	emailAddr, plainPassword, code, redirect string,
	meta authModel.SessionMetadata,
) (sessionCookie, safeRedirect string, err error) {
	safeRedirect = u.resolveRedirect(redirect)
	account := normalizeEmail(emailAddr)
	if wait := u.limits.signInAccount.Locked(account); wait > 0 {
		return "", "", tooManyRequests(wait)
	}
	defer func() {
		switch {
		case err == nil:
			u.limits.signInAccount.Reset(account)
		case errors.Is(err, secondFactorModel.ErrRecoverySignInFailed.Err()):
			u.limits.signInAccount.Fail(account)
		}
	}()
	keys, err := u.recoveryKeys()
	if err != nil {
		return "", "", err
	}

	user, dbErr := u.users.GetByEmail(ctx, account)
	if dbErr != nil && !repositoryTools.IsObjectNotFoundError(dbErr) {
		return "", "", model.ErrPlatform.WithError(dbErr).WithMessage("Failed to get user by email").Err()
	}
	if repositoryTools.IsObjectNotFoundError(dbErr) || !user.HasPassword() {
		// timing-safe: the same bcrypt work whether the account exists or has a password.
		_, _ = u.password.Matches(plainPassword, fakeHashedPassword)
		return "", "", secondFactorModel.ErrRecoverySignInFailed.WithError(errors.New("recovery sign-in: no account with a password")).Err()
	}
	matches, mErr := u.password.Matches(plainPassword, user.HashedPassword)
	if mErr != nil {
		return "", "", model.ErrPlatform.WithError(mErr).WithMessage("Failed to check password").Err()
	}
	if !matches {
		return "", "", secondFactorModel.ErrRecoverySignInFailed.WithError(errors.New("recovery sign-in: wrong password")).Err()
	}
	has, err := u.HasSecondFactor(ctx, user.ID)
	if err != nil {
		return "", "", err
	}
	if !has {
		return "", "", secondFactorModel.ErrRecoverySignInFailed.WithError(errors.New("recovery sign-in: the account has no second factor")).Err()
	}

	now := time.Now()
	hashes := make([][]byte, 0, len(keys))
	for _, k := range keys {
		hashes = append(hashes, secondFactorModel.HashRecoveryCode(k, user.ID, code))
	}
	used, err := u.factors.UseRecoveryCode(ctx, user.ID, hashes, now)
	if err != nil {
		return "", "", model.ErrPlatform.WithError(err).WithMessage("Failed to use the recovery code").Err()
	}
	if !used {
		return "", "", secondFactorModel.ErrRecoverySignInFailed.WithError(errors.New("recovery sign-in: unknown or used code")).Err()
	}
	// Only after the credentials matched: revealing "blocked" earlier would let anyone probe account status.
	if err = refuseBlocked(&user); err != nil {
		return "", "", err
	}

	cookie, sessionID, err := u.createSessionWithID(ctx, user.ID, meta)
	if err != nil {
		return "", "", err
	}
	if _, err = u.factors.SetStepUp(ctx, sessionID, user.ID, secondFactorModel.StepUp{At: now, Method: secondFactorModel.MethodRecoveryCode}); err != nil {
		return "", "", model.ErrPlatform.WithError(err).WithMessage("Failed to record the step-up").Err()
	}
	u.recoveryCodeUsed(ctx, user.ID, user.FirstName)
	return cookie, safeRedirect, nil
}

// recoveryCodeUsed writes the journal row and mails the owner. The mail goes whatever the notification preferences say
// (the channel is forced): it is how the owner learns that someone else holds a code. Both are best effort: the sign-in
// already happened.
func (u *AuthUseCase) recoveryCodeUsed(ctx context.Context, userID uuid.UUID, name string) {
	u.journal(ctx, secondFactorModel.ActionRecoveryCodeUsed, userID, "recovery sign-in")
	left, err := u.factors.UnusedRecoveryCodes(ctx, userID)
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("Failed to count the recovery codes left")
	}
	if u.notifier == nil {
		return
	}
	if err = u.notifier.Notify(ctx, userID, notificationPayloads.RecoveryCodeUsedPayload{
		Name:        name,
		CodesLeft:   strconv.Itoa(left),
		SecurityURL: u.cfg.Hosts.IDURL("/profile/"),
	}, dispatchModel.WithOverrideChannels(notificationTypes.NotificationChannelEmail)); err != nil {
		log.Error().Err(err).Str("user_id", userID.String()).Msg("Failed to notify a recovery code use")
	}
}
