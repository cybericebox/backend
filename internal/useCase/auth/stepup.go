package auth

import (
	"context"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/model"
	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
	temporalCodeModel "github.com/cybericebox/backend/internal/model/temporalCode"
)

// StepUpRequest is one confirmation: a passkey assertion (CeremonyID + Credential, from BeginPasskeyStepUp), a code of
// the authenticator app (Code), or the password of an account without a second factor (Password; an account without a
// password then needs a sign-in of the last minutes instead). Google sign-in, email codes and SMS are not methods.
type StepUpRequest struct {
	Method     secondFactorModel.Method
	Code       string
	Password   string
	CeremonyID string
	Credential []byte
}

// passkeyStepUpCeremony is the stored server half of a passkey step-up: it belongs to one person and one session.
type passkeyStepUpCeremony struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Session   webauthn.SessionData
}

// usablePasskeys are the passkeys of the person that can confirm (not marked compromised).
func (u *AuthUseCase) usablePasskeys(ctx context.Context, userID uuid.UUID) ([]webauthn.Credential, error) {
	keys, err := u.passkeys.ListByUser(ctx, userID)
	if err != nil {
		return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to list passkeys").Err()
	}
	creds := make([]webauthn.Credential, 0, len(keys))
	for _, k := range keys {
		if k.CompromisedAt == nil && k.UserVerified {
			creds = append(creds, toCredential(k))
		}
	}
	return creds, nil
}

// BeginPasskeyStepUp starts a passkey confirmation of the signed-in person: the browser asks the authenticator for one
// of the person's own passkeys, with user verification required.
func (u *AuthUseCase) BeginPasskeyStepUp(ctx context.Context, userID uuid.UUID) (PasskeyCeremony, error) {
	if err := u.requirePasskeys(); err != nil {
		return PasskeyCeremony{}, err
	}
	sessionID, err := currentSession(ctx)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	creds, err := u.usablePasskeys(ctx, userID)
	if err != nil {
		return PasskeyCeremony{}, err
	}
	if len(creds) == 0 {
		return PasskeyCeremony{}, secondFactorModel.ErrStepUpMethodNotAllowed.WithError(errors.New("no passkey to confirm with")).Err()
	}
	user, err := u.users.GetByID(ctx, userID)
	if err != nil {
		return PasskeyCeremony{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get user").Err()
	}
	assertion, session, err := u.webauthn.BeginLogin(
		passkeyUser{user: user, creds: creds},
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return PasskeyCeremony{}, model.ErrPlatform.WithError(err).WithMessage("Failed to begin the passkey confirmation").Err()
	}
	return u.passkeyCeremonyOf(ctx, temporalCodeModel.PasskeyStepUpCodeType,
		passkeyStepUpCeremony{UserID: userID, SessionID: sessionID, Session: *session}, assertion)
}

// StepUp confirms the signed-in person on this session and records when and how (sessions.step_up_at), for
// secondFactorModel.StepUpWindow. The password is accepted only from an account without a second factor: who has one
// confirms with it.
func (u *AuthUseCase) StepUp(ctx context.Context, userID uuid.UUID, req StepUpRequest) (secondFactorModel.StepUp, error) {
	sessionID, err := currentSession(ctx)
	if err != nil {
		return secondFactorModel.StepUp{}, err
	}
	key := userID.String()
	if wait := u.limits.secondFactor.Locked(key); wait > 0 {
		return secondFactorModel.StepUp{}, tooManyRequests(wait)
	}
	method, err := u.confirmStepUp(ctx, userID, sessionID, req)
	if err != nil {
		if errors.Is(err, secondFactorModel.ErrStepUpFailed.Err()) {
			u.limits.secondFactor.Fail(key)
		}
		return secondFactorModel.StepUp{}, err
	}
	u.limits.secondFactor.Reset(key)
	s := secondFactorModel.StepUp{At: time.Now(), Method: method}
	found, err := u.factors.SetStepUp(ctx, sessionID, userID, s)
	if err != nil {
		return secondFactorModel.StepUp{}, model.ErrPlatform.WithError(err).WithMessage("Failed to record the step-up").Err()
	}
	if !found {
		return secondFactorModel.StepUp{}, secondFactorModel.ErrStepUpFailed.WithError(errors.New("step-up: the session is gone")).Err()
	}
	return s, nil
}

func (u *AuthUseCase) confirmStepUp(
	ctx context.Context,
	userID, sessionID uuid.UUID,
	req StepUpRequest,
) (secondFactorModel.Method, error) {
	switch req.Method {
	case secondFactorModel.MethodTOTP:
		ok, err := u.verifyTOTP(ctx, userID, req.Code)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", secondFactorModel.ErrStepUpFailed.WithError(errors.New("step-up: wrong or reused authenticator code")).Err()
		}
		return secondFactorModel.MethodTOTP, nil
	case secondFactorModel.MethodPasskey:
		if err := u.verifyPasskeyStepUp(ctx, userID, sessionID, req); err != nil {
			return "", err
		}
		return secondFactorModel.MethodPasskey, nil
	case secondFactorModel.MethodPassword:
		has, err := u.HasSecondFactor(ctx, userID)
		if err != nil {
			return "", err
		}
		if has {
			return "", secondFactorModel.ErrStepUpMethodNotAllowed.WithError(errors.New("step-up: the account has a second factor")).Err()
		}
		user, err := u.users.GetByID(ctx, userID)
		if err != nil {
			return "", model.ErrPlatform.WithError(err).WithMessage("Failed to get user").Err()
		}
		// The password (its own lockout), or a sign-in of the last minutes for an account without one.
		if err = u.reauthenticate(ctx, userID, req.Password); err != nil {
			return "", err
		}
		if user.HasPassword() {
			return secondFactorModel.MethodPassword, nil
		}
		return secondFactorModel.MethodRecentSignIn, nil
	}
	return "", secondFactorModel.ErrStepUpMethodNotAllowed.WithError(errors.New("step-up: unknown method")).Err()
}

// verifyPasskeyStepUp checks the assertion against the ceremony of this person and session: one of their own passkeys,
// user verification proven, the counter advancing.
func (u *AuthUseCase) verifyPasskeyStepUp(ctx context.Context, userID, sessionID uuid.UUID, req StepUpRequest) error {
	if err := u.requirePasskeys(); err != nil {
		return err
	}
	var ceremony passkeyStepUpCeremony
	if err := u.consumePasskeyCeremony(ctx, req.CeremonyID, temporalCodeModel.PasskeyStepUpCodeType, &ceremony); err != nil {
		return err
	}
	if ceremony.UserID != userID || ceremony.SessionID != sessionID {
		return secondFactorModel.ErrStepUpFailed.WithError(errors.New("step-up: ceremony of another person or session")).Err()
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		return secondFactorModel.ErrStepUpFailed.WithError(err).Err()
	}
	creds, err := u.usablePasskeys(ctx, userID)
	if err != nil {
		return err
	}
	user, err := u.users.GetByID(ctx, userID)
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to get user").Err()
	}
	verified, err := u.webauthn.ValidateLogin(passkeyUser{user: user, creds: creds}, ceremony.Session, parsed)
	if err != nil {
		return secondFactorModel.ErrStepUpFailed.WithError(err).Err()
	}
	if !verified.Flags.UserVerified {
		return secondFactorModel.ErrStepUpFailed.WithError(errors.New("step-up: user verification missing")).Err()
	}
	stored, err := u.passkeys.GetByCredentialID(ctx, verified.ID)
	if err != nil || stored.UserID != userID {
		return secondFactorModel.ErrStepUpFailed.WithError(errors.New("step-up: the key is not the person's")).Err()
	}
	if err = u.recordPasskeyUse(ctx, stored, verified); err != nil {
		return secondFactorModel.ErrStepUpFailed.WithError(err).Err()
	}
	return nil
}
