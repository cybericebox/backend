package auth

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	"github.com/cybericebox/backend/internal/model"
	authModel "github.com/cybericebox/backend/internal/model/auth"
	notificationPayloads "github.com/cybericebox/backend/internal/model/notification/types/payloads"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
)

// issueRecoveryCodes makes a new set of codes, stores their hashes in place of the old set and returns the codes: the
// only time they are ever shown.
func (u *AuthUseCase) issueRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	codes, err := secondFactorModel.NewRecoveryCodes()
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to create recovery codes").Err()
	}
	hashes := make([][]byte, 0, len(codes))
	for _, c := range codes {
		hashes = append(hashes, secondFactorModel.HashRecoveryCode(userID, c))
	}
	if err = u.factors.ReplaceRecoveryCodes(ctx, userID, hashes, time.Now()); err != nil {
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
	return u.issueRecoveryCodes(ctx, userID)
}

// SignInWithRecoveryCode signs in with a one-time recovery code when the factor is lost. An account with a password
// still needs it: the code stands in for the second factor, never for the password. The new session carries a step-up
// of the kind "recovery-code", so the person can enrol a new factor, drop the lost one and get new codes, and nothing
// that needs a real second factor. The use is journaled and mailed to the owner.
//
// Every failure answers the same ErrRecoverySignInFailed, after the same password hashing, and counts against the same
// per-account lockout as the password sign-in.
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

	user, dbErr := u.users.GetByEmail(ctx, account)
	if dbErr != nil && !repositoryTools.IsObjectNotFoundError(dbErr) {
		return "", "", model.ErrPlatform.WithError(dbErr).WithMessage("Failed to get user by email").Err()
	}
	if repositoryTools.IsObjectNotFoundError(dbErr) || !user.HasPassword() {
		// timing-safe: the same bcrypt work whether the account exists or has a password.
		_, _ = u.password.Matches(plainPassword, fakeHashedPassword)
		if repositoryTools.IsObjectNotFoundError(dbErr) {
			return "", "", secondFactorModel.ErrRecoverySignInFailed.WithError(errors.New("recovery sign-in: no such account")).Err()
		}
	} else {
		matches, mErr := u.password.Matches(plainPassword, user.HashedPassword)
		if mErr != nil {
			return "", "", model.ErrPlatform.WithError(mErr).WithMessage("Failed to check password").Err()
		}
		if !matches {
			return "", "", secondFactorModel.ErrRecoverySignInFailed.WithError(errors.New("recovery sign-in: wrong password")).Err()
		}
	}

	now := time.Now()
	used, err := u.factors.UseRecoveryCode(ctx, user.ID, secondFactorModel.HashRecoveryCode(user.ID, code), now)
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
	u.recoveryCodeUsed(ctx, user.ID, user.FirstName, now)
	return cookie, safeRedirect, nil
}

// recoveryCodeUsed writes the journal row and mails the owner. Both are best effort: the sign-in already happened.
func (u *AuthUseCase) recoveryCodeUsed(ctx context.Context, userID uuid.UUID, name string, at time.Time) {
	if err := u.factorJournal.RecordFactorEvent(ctx, secondFactorModel.JournalEvent{
		Action: secondFactorModel.ActionRecoveryCodeUsed, UserID: userID, At: at, Reason: "recovery sign-in",
	}); err != nil {
		log.Error().Err(err).Str("user_id", userID.String()).Msg("Failed to journal a recovery code use")
	}
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
	}); err != nil {
		log.Error().Err(err).Str("user_id", userID.String()).Msg("Failed to notify a recovery code use")
	}
}
