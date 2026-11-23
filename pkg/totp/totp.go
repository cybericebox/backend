// Package totp is a thin wrapper over github.com/pquerna/otp (its totp and hotp packages) with the two rules the
// platform adds: a code is checked against one step either side of now, and a step at or before the last used one is
// refused, so one code never works twice (RFC 6238 §5.2). The library does the HMAC, the base32 secret, the code
// comparison (constant time) and the otpauth:// URI; the RFC test vectors in totp_test.go pin the result.
package totp

import (
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

// SecretSize is the length of a new secret in bytes: 160 bits, the size RFC 4226 recommends for HMAC-SHA-1.
const SecretSize = 20

// Params are the code parameters an authenticator app is set up with.
type Params struct {
	Digits    otp.Digits
	Period    time.Duration
	Algorithm otp.Algorithm
}

// Default is what every authenticator app understands: 6 digits, 30-second steps, HMAC-SHA-1.
var Default = Params{Digits: otp.DigitsSix, Period: 30 * time.Second, Algorithm: otp.AlgorithmSHA1}

// Key is a new secret: Secret in base32 (what is stored, sealed, and what people type into an app when they cannot
// scan the QR code) and URI, the otpauth:// URI the QR code carries.
type Key struct {
	Secret string
	URI    string
}

func (p Params) seconds() uint { return uint(p.Period / time.Second) }

// New makes a random secret of SecretSize bytes for account at issuer.
func (p Params) New(issuer, account string) (Key, error) {
	k, err := totp.Generate(totp.GenerateOpts{
		Issuer: issuer, AccountName: account, Period: p.seconds(), SecretSize: SecretSize,
		Digits: p.Digits, Algorithm: p.Algorithm,
	})
	if err != nil {
		return Key{}, err
	}
	return Key{Secret: k.Secret(), URI: k.URL()}, nil
}

// Step is the RFC 6238 time step of t (Unix time divided by the period).
func (p Params) Step(t time.Time) int64 { return t.Unix() / int64(p.seconds()) }

// Code is the code of step for the base32 secret.
func (p Params) Code(secret string, step int64) (string, error) {
	return hotp.GenerateCodeCustom(secret, uint64(step), p.hotpOpts()) //nolint:gosec // steps before 1970 do not occur
}

func (p Params) hotpOpts() hotp.ValidateOpts {
	return hotp.ValidateOpts{Digits: p.Digits, Algorithm: p.Algorithm}
}

// Match checks code against the steps around now (skew steps either side) and returns the earliest matching step that
// is later than after: a step at or before after was already used and is refused. Spaces people type between the
// halves are ignored; anything else that is not the code never matches.
func (p Params) Match(secret, code string, now time.Time, skew int, after int64) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	current := p.Step(now)
	for d := -int64(skew); d <= int64(skew); d++ {
		step := current + d
		if step <= after {
			continue
		}
		ok, err := hotp.ValidateCustom(code, uint64(step), secret, p.hotpOpts()) //nolint:gosec // see Code
		if err == nil && ok {
			return step, true
		}
	}
	return 0, false
}
