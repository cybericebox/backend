package secondFactorModel

import (
	"github.com/cybericebox/backend/pkg/err"

	"github.com/cybericebox/backend/internal/model"
)

// Error-code convention: see internal/model/auth/errors.go. Enforced by `mise run lint:errors`.
//
// SecondFactorObjectCode — next free detail code: 12.
//
//nolint:gochecknoglobals,mnd // the error registry: one var per code, parsed by tools/errorcatalog
var (
	// ErrStepUpRequired (STEP_UP_REQUIRED, HTTP 401) answers an action that needs a confirmation (a step-up) made on this
	// session in the last minutes, or one made with a second factor. The client confirms (POST /api/auth/step-up) and
	// repeats the request; it is not a dead session, so the sign-in URL is not advertised and the cookie stays.
	// multi-site: every action that needs a step-up returns it.
	ErrStepUpRequired = err.ErrUnauthenticated.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("Confirm it is you to continue").WithDetailCode(1)
	// ErrTOTPInvalidCode: the code of the authenticator app is wrong, outside the time window or already used.
	// multi-site (category A): the client must not tell these apart.
	ErrTOTPInvalidCode = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("Invalid authenticator code").WithDetailCode(2)
	ErrTOTPAlreadyEnabled = err.ErrObjectExists.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("An authenticator app is already set up").WithDetailCode(3)
	ErrTOTPNotEnabled = err.ErrObjectNotFound.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("No authenticator app is set up").WithDetailCode(4)
	// ErrTOTPEnrolmentExpired: there is no setup to confirm (never started, too old, or replaced by a newer one).
	ErrTOTPEnrolmentExpired = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("Authenticator setup expired, start again").WithDetailCode(5)
	// ErrLastFactor: removing this factor would leave no second factor to a holder of a key that needs one.
	// multi-site: the passkey and the authenticator removals both guard it.
	ErrLastFactor = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
			WithMessage("Your access needs a second factor: add another one before removing this one").WithDetailCode(6)
	// ErrRecoverySignInFailed (category A, security-indistinguishable): an unknown address, a wrong password, a wrong
	// or used recovery code. The reason goes to the logs via WithError.
	ErrRecoverySignInFailed = err.ErrUnauthenticated.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("Recovery sign-in failed").WithDetailCode(7)
	// ErrStepUpFailed (category A): the confirmation did not pass (a wrong code, a passkey answer that did not verify).
	ErrStepUpFailed = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
			WithMessage("Confirmation failed").WithDetailCode(8)
	// ErrStepUpMethodNotAllowed: the confirmation method cannot be used by this account: the password when the account
	// has a second factor, a passkey or an authenticator app it does not have, or an unknown method.
	ErrStepUpMethodNotAllowed = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
					WithMessage("Confirm with your passkey or authenticator app").WithDetailCode(9)
	// ErrNoSecondFactor: recovery codes back up a second factor, so the account needs one first.
	ErrNoSecondFactor = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
				WithMessage("Set up a passkey or an authenticator app first").WithDetailCode(10)
	// ErrTOTPCodeUsed: the code is right but its time step was used already (a double submit, or a copied code). It
	// does not count as a failure; the person waits for the next code.
	ErrTOTPCodeUsed = err.ErrInvalidData.WithObjectCode(model.SecondFactorObjectCode).
			WithMessage("This code was already used, wait for the next one").WithDetailCode(11)
)
