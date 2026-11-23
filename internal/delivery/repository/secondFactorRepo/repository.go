// Package secondFactorRepo stores the second factor of an account: the sealed authenticator secret, the hashed
// recovery codes and the step-up of a session.
package secondFactorRepo

import (
	"context"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
)

// Queries is the narrow slice of the sqlc Querier this repository needs.
type Queries interface {
	GetUserTOTP(ctx context.Context, userID uuid.UUID) (postgres.UserTotp, error)
	StartUserTOTP(ctx context.Context, arg postgres.StartUserTOTPParams) (int64, error)
	ConfirmUserTOTP(ctx context.Context, arg postgres.ConfirmUserTOTPParams) (int64, error)
	UseUserTOTPStep(ctx context.Context, arg postgres.UseUserTOTPStepParams) (int64, error)
	ResealUserTOTP(ctx context.Context, arg postgres.ResealUserTOTPParams) (int64, error)
	ReserveUserTOTPAttempt(ctx context.Context, arg postgres.ReserveUserTOTPAttemptParams) (int32, error)
	RefundUserTOTPAttempt(ctx context.Context, arg postgres.RefundUserTOTPAttemptParams) error
	ClearUserStepUps(ctx context.Context, userID uuid.UUID) (int64, error)
	LockUserForFactorChange(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	UserTOTPExists(ctx context.Context) (bool, error)
	DeleteUserTOTP(ctx context.Context, arg postgres.DeleteUserTOTPParams) (int64, error)
	ReplaceUserRecoveryCodes(ctx context.Context, arg postgres.ReplaceUserRecoveryCodesParams) (int64, error)
	UseUserRecoveryCode(ctx context.Context, arg postgres.UseUserRecoveryCodeParams) (int64, error)
	CountUnusedUserRecoveryCodes(ctx context.Context, userID uuid.UUID) (int64, error)
	DeleteUserRecoveryCodes(ctx context.Context, userID uuid.UUID) (int64, error)
	CountUserFactorPasskeys(ctx context.Context, userID uuid.UUID) (int64, error)
	SetSessionStepUp(ctx context.Context, arg postgres.SetSessionStepUpParams) (int64, error)
	GetSessionStepUp(ctx context.Context, id uuid.UUID) (postgres.GetSessionStepUpRow, error)
}

type Repository struct{ q Queries }

func New(q Queries) *Repository { return &Repository{q: q} }

// TOTP is the stored authenticator of a person.
type TOTP struct {
	SealedSecret string
	CreatedAt    time.Time
	// ConfirmedAt is nil while the setup waits for its first code.
	ConfirmedAt *time.Time
	// LastStep is the time step of the last accepted code (0: none yet).
	LastStep int64
	// Failures counts the wrong codes since the last right one; LockedUntil stops the checks until then.
	Failures    int
	LockedUntil *time.Time
}

// GetTOTP returns the authenticator of the person; found is false when there is none.
func (r *Repository) GetTOTP(ctx context.Context, userID uuid.UUID) (t TOTP, found bool, err error) {
	row, err := r.q.GetUserTOTP(ctx, userID)
	if repositoryTools.IsObjectNotFoundError(err) {
		return TOTP{}, false, nil
	}
	if err != nil {
		return TOTP{}, false, err
	}
	t = TOTP{SealedSecret: row.SecretCiphertext, CreatedAt: row.CreatedAt, LastStep: row.LastStep.Int64}
	t.Failures = int(row.Failures)
	if row.ConfirmedAt.Valid {
		at := row.ConfirmedAt.Time
		t.ConfirmedAt = &at
	}
	if row.LockedUntil.Valid {
		at := row.LockedUntil.Time
		t.LockedUntil = &at
	}
	return t, true, nil
}

// Start stores a setup that waits for its first code; started is false when the person has a confirmed authenticator.
func (r *Repository) Start(ctx context.Context, userID uuid.UUID, sealed string, at time.Time) (started bool, err error) {
	n, err := r.q.StartUserTOTP(ctx, postgres.StartUserTOTPParams{UserID: userID, SecretCiphertext: sealed, CreatedAt: at})
	return n == 1, err
}

// Confirm confirms the setup that holds sealed with the step of its first code; false when the setup changed or went.
func (r *Repository) Confirm(ctx context.Context, userID uuid.UUID, sealed string, step int64, at time.Time) (bool, error) {
	n, err := r.q.ConfirmUserTOTP(ctx, postgres.ConfirmUserTOTPParams{
		UserID: userID, SecretCiphertext: sealed, LastStep: step, ConfirmedAt: at,
	})
	return n == 1, err
}

// UseStep takes the step of an accepted code; false when that step or a later one was used already.
func (r *Repository) UseStep(ctx context.Context, userID uuid.UUID, step int64) (bool, error) {
	n, err := r.q.UseUserTOTPStep(ctx, postgres.UseUserTOTPStepParams{UserID: userID, Step: step})
	return n == 1, err
}

// Reseal replaces the sealed secret old with sealed (the same secret under the current platform key).
func (r *Repository) Reseal(ctx context.Context, userID uuid.UUID, old, sealed string) error {
	_, err := r.q.ResealUserTOTP(ctx, postgres.ResealUserTOTPParams{UserID: userID, OldCiphertext: old, SecretCiphertext: sealed})
	return err
}

// DeleteTOTP removes the authenticator (confirmedOnly: only a confirmed one); false when there was none.
func (r *Repository) DeleteTOTP(ctx context.Context, userID uuid.UUID, confirmedOnly bool) (bool, error) {
	n, err := r.q.DeleteUserTOTP(ctx, postgres.DeleteUserTOTPParams{UserID: userID, ConfirmedOnly: confirmedOnly})
	return n > 0, err
}

// ReplaceRecoveryCodes stores the hashes of a new set of codes in place of every old one.
func (r *Repository) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes [][]byte, at time.Time) error {
	_, err := r.q.ReplaceUserRecoveryCodes(ctx, postgres.ReplaceUserRecoveryCodesParams{UserID: userID, CodeHashes: hashes, CreatedAt: at})
	return err
}

// UseRecoveryCode takes one unused code that has one of hashes (the code under each key of the keyring) once; false
// for an unknown or used one.
func (r *Repository) UseRecoveryCode(ctx context.Context, userID uuid.UUID, hashes [][]byte, at time.Time) (bool, error) {
	n, err := r.q.UseUserRecoveryCode(ctx, postgres.UseUserRecoveryCodeParams{UserID: userID, CodeHashes: hashes, UsedAt: at})
	return n == 1, err
}

// DeleteRecoveryCodes removes every recovery code of the person (the last factor went).
func (r *Repository) DeleteRecoveryCodes(ctx context.Context, userID uuid.UUID) error {
	_, err := r.q.DeleteUserRecoveryCodes(ctx, userID)
	return err
}

// Lockout is the failure rule of the authenticator codes: from MaxFailures on, a lock of LockBase doubling up to LockMax.
type Lockout struct {
	MaxFailures       int
	LockBase, LockMax time.Duration
}

// ReserveTOTPAttempt counts one code check before the code is compared; reserved is false while the checks are locked.
func (r *Repository) ReserveTOTPAttempt(ctx context.Context, userID uuid.UUID, l Lockout, now time.Time) (reserved bool, err error) {
	_, err = r.q.ReserveUserTOTPAttempt(ctx, postgres.ReserveUserTOTPAttemptParams{
		UserID: userID, MaxFailures: int32(l.MaxFailures), Now: now, //nolint:gosec // a small configured count
		LockBaseSecs: l.LockBase.Seconds(), LockMaxSecs: l.LockMax.Seconds(),
	})
	if repositoryTools.IsObjectNotFoundError(err) {
		return false, nil
	}
	return err == nil, err
}

// RefundTOTPAttempt gives a reserved attempt back.
func (r *Repository) RefundTOTPAttempt(ctx context.Context, userID uuid.UUID, l Lockout) error {
	return r.q.RefundUserTOTPAttempt(ctx, postgres.RefundUserTOTPAttemptParams{UserID: userID, MaxFailures: int32(l.MaxFailures)}) //nolint:gosec // see above
}

// ClearStepUps forgets the step-ups of every session of the person.
func (r *Repository) ClearStepUps(ctx context.Context, userID uuid.UUID) error {
	_, err := r.q.ClearUserStepUps(ctx, userID)
	return err
}

// LockUser holds the row of the person until the transaction ends, so two factor changes do not both pass the
// last-factor guard.
func (r *Repository) LockUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.q.LockUserForFactorChange(ctx, userID)
	return err
}

// AnyTOTP reports whether any authenticator secret is stored (the start-up check of the platform key).
func (r *Repository) AnyTOTP(ctx context.Context) (bool, error) { return r.q.UserTOTPExists(ctx) }

// UnusedRecoveryCodes counts the codes the person can still use.
func (r *Repository) UnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := r.q.CountUnusedUserRecoveryCodes(ctx, userID)
	return int(n), err
}

// FactorPasskeys counts the passkeys that count as a second factor.
func (r *Repository) FactorPasskeys(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := r.q.CountUserFactorPasskeys(ctx, userID)
	return int(n), err
}

// DeleteAll removes the authenticator and the recovery codes of the person (account removal).
func (r *Repository) DeleteAll(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.q.DeleteUserTOTP(ctx, postgres.DeleteUserTOTPParams{UserID: userID}); err != nil {
		return err
	}
	_, err := r.q.DeleteUserRecoveryCodes(ctx, userID)
	return err
}

// SetStepUp records a step-up on the session of the person; false when the session is gone.
func (r *Repository) SetStepUp(ctx context.Context, sessionID, userID uuid.UUID, s secondFactorModel.StepUp) (bool, error) {
	n, err := r.q.SetSessionStepUp(ctx, postgres.SetSessionStepUpParams{
		ID: sessionID, UserID: userID, StepUpAt: s.At, StepUpMethod: string(s.Method),
	})
	return n == 1, err
}

// StepUp returns the last step-up of the session (zero when there is none or the session is gone).
func (r *Repository) StepUp(ctx context.Context, sessionID uuid.UUID) (secondFactorModel.StepUp, error) {
	row, err := r.q.GetSessionStepUp(ctx, sessionID)
	if repositoryTools.IsObjectNotFoundError(err) {
		return secondFactorModel.StepUp{}, nil
	}
	if err != nil || !row.StepUpAt.Valid {
		return secondFactorModel.StepUp{}, err
	}
	return secondFactorModel.StepUp{At: row.StepUpAt.Time, Method: secondFactorModel.Method(row.StepUpMethod.String)}, nil
}
