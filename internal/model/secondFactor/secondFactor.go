// Package secondFactorModel is the second factor of an account (§6.6 of the access-control design): an authenticator
// app (TOTP) or a passkey with user verification, the one-time recovery codes that back them up, and the step-up, a
// recent confirmation recorded on the session. Google sign-in, email codes and SMS never count as a factor.
package secondFactorModel

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const (
	// StepUpWindow is how long a step-up stays valid (protection.RequireStepUp(StepUpWindow)).
	StepUpWindow = 5 * time.Minute
	// RecoveryCodeCount is how many one-time recovery codes an account gets at a time.
	RecoveryCodeCount = 10
	// TOTPEnrolmentTTL is how long a started authenticator setup waits for its first code.
	TOTPEnrolmentTTL = 10 * time.Minute
	// TOTPSkew is how many 30-second steps either side of now a code may be from (RFC 6238 §5.2: one).
	TOTPSkew = 1
)

// Method is how a step-up (or the session behind it) was confirmed.
type Method string

const (
	MethodPasskey Method = "passkey" // a passkey assertion with user verification
	MethodTOTP    Method = "totp"    // a code of the authenticator app
	// MethodPassword is the password of an account without a second factor.
	MethodPassword Method = "password"
	// MethodRecentSignIn is a sign-in of the last minutes, for an account without a password and without a factor.
	MethodRecentSignIn Method = "recent-sign-in"
	// MethodRecoveryCode marks a session signed in with a recovery code: it may manage the factors (enrol a new one,
	// drop the lost one, get new codes) and nothing that needs a real second factor.
	MethodRecoveryCode Method = "recovery-code"
)

// IsSecondFactor reports the methods that count as a second factor: a passkey with user verification and an
// authenticator app. Nothing else does.
func (m Method) IsSecondFactor() bool { return m == MethodPasskey || m == MethodTOTP }

// ConfirmsFactorChange reports the methods that may confirm a change of the factors of an account that has one: a
// second factor, or a recovery code standing in for a lost one.
func (m Method) ConfirmsFactorChange() bool { return m.IsSecondFactor() || m == MethodRecoveryCode }

// StepUp is the last confirmation made on a session.
type StepUp struct {
	At     time.Time
	Method Method
}

// Fresh reports a step-up made within maxAge before now (and not after it: a clock that runs ahead proves nothing).
func (s StepUp) Fresh(now time.Time, maxAge time.Duration) bool {
	return !s.At.IsZero() && !s.At.After(now) && now.Sub(s.At) <= maxAge
}

// Status is what the account settings show about the second factor.
type Status struct {
	// TOTPEnabled is true once the authenticator app was confirmed with a first code.
	TOTPEnabled     bool
	TOTPConfirmedAt *time.Time
	// Passkeys counts the passkeys that count as a factor (user verification, not marked compromised).
	Passkeys int
	// RecoveryCodesLeft counts the unused recovery codes.
	RecoveryCodesLeft int
	// HasSecondFactor is true with a confirmed authenticator app or a passkey.
	HasSecondFactor bool
	// StepUp is the last confirmation of the current session; nil when there is none.
	StepUp *StepUp
}

// JournalAction is an account-security event the grant journal records (access_grant_log.action).
type JournalAction string

// ActionRecoveryCodeUsed is written when a recovery code signed someone in.
const ActionRecoveryCodeUsed JournalAction = "factor.recovery-used"

// JournalEvent is one row of the grant journal about a factor: the person is both the actor and the subject, at
// platform scope.
type JournalEvent struct {
	Action JournalAction
	UserID uuid.UUID
	At     time.Time
	Reason string
}

// recoveryAlphabet is Crockford's base32: no I, L, O or U, which people misread.
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// recoveryCodeChars is the length of a code without its dashes: 12 characters of 5 bits, 60 bits.
const recoveryCodeChars = 12

// NewRecoveryCodes returns RecoveryCodeCount new codes in the form XXXX-XXXX-XXXX. They are shown once and stored
// only as HashRecoveryCode.
func NewRecoveryCodes() ([]string, error) {
	out := make([]string, 0, RecoveryCodeCount)
	seen := map[string]bool{}
	for len(out) < RecoveryCodeCount {
		raw := make([]byte, recoveryCodeChars)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("recovery codes: read random: %w", err)
		}
		var b strings.Builder
		for i, v := range raw {
			if i > 0 && i%4 == 0 {
				b.WriteByte('-')
			}
			b.WriteByte(recoveryAlphabet[v&0x1f])
		}
		code := b.String()
		if !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out, nil
}

// normalizeRecoveryCode reads a code as people type it: case, spaces and dashes do not matter, and O, I and L read as
// the digits 0 and 1 they look like.
func normalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(code))
	return strings.NewReplacer("O", "0", "I", "1", "L", "1").Replace(code)
}

// HashRecoveryCode is the stored form of a code: SHA-256 over the person's id and the normalized code. A code has 60
// random bits, so a fast hash is enough, and the id makes the same code of two people two different hashes.
func HashRecoveryCode(userID uuid.UUID, code string) []byte {
	h := sha256.New()
	h.Write(userID.Bytes())
	h.Write([]byte(normalizeRecoveryCode(code)))
	return h.Sum(nil)
}
