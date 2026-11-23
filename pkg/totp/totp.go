// Package totp implements the one-time passwords of authenticator apps: HOTP (RFC 4226) and TOTP (RFC 6238).
//
// It is a small own implementation on the standard library (crypto/hmac): the algorithm is a few lines, the go.mod has
// no OTP library, and the RFC test vectors (totp_test.go) pin it. Codes are compared in constant time.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 SHA-1 is what every authenticator app uses; HMAC-SHA-1 is not broken
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Algorithm is the HMAC hash of the codes.
type Algorithm int

const (
	SHA1 Algorithm = iota
	SHA256
	SHA512
)

func (a Algorithm) String() string {
	switch a {
	case SHA256:
		return "SHA256"
	case SHA512:
		return "SHA512"
	default:
		return "SHA1"
	}
}

func (a Algorithm) hash() func() hash.Hash {
	switch a {
	case SHA256:
		return sha256.New
	case SHA512:
		return sha512.New
	default:
		return sha1.New
	}
}

// SecretSize is the length of a new secret: 160 bits, the size RFC 4226 recommends for HMAC-SHA-1.
const SecretSize = 20

// Params are the code parameters an authenticator app is set up with.
type Params struct {
	Digits    int
	Period    time.Duration
	Algorithm Algorithm
}

// Default is what every authenticator app understands: 6 digits, 30-second steps, HMAC-SHA-1.
var Default = Params{Digits: 6, Period: 30 * time.Second, Algorithm: SHA1}

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random secret of SecretSize bytes.
func NewSecret() ([]byte, error) {
	secret := make([]byte, SecretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("totp: read random secret: %w", err)
	}
	return secret, nil
}

// EncodeSecret is the base32 form (no padding) people type into an app when they cannot scan the QR code.
func EncodeSecret(secret []byte) string { return encoding.EncodeToString(secret) }

// DecodeSecret reads the base32 form back; case, spaces and padding are ignored.
func DecodeSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(s))
	return encoding.DecodeString(s)
}

// HOTP is the RFC 4226 code of counter: dynamic truncation of HMAC(secret, counter), digits long.
func HOTP(secret []byte, counter uint64, digits int, alg Algorithm) string {
	mac := hmac.New(alg.hash(), secret)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// Step is the RFC 6238 time step of t (Unix time divided by the period).
func (p Params) Step(t time.Time) int64 {
	return t.Unix() / int64(p.Period/time.Second)
}

// Code is the code of step.
func (p Params) Code(secret []byte, step int64) string {
	return HOTP(secret, uint64(step), p.Digits, p.Algorithm) //nolint:gosec // steps before 1970 do not occur
}

// Match checks code against the steps around now (skew steps either side) and returns the earliest matching step
// that is later than after: a step at or before after was already used and is refused, so one code never works twice.
// A code of the wrong length or with anything but digits (spaces aside) never matches. Every candidate step is
// compared, in constant time, so the answer time does not tell which one matched.
func (p Params) Match(secret []byte, code string, now time.Time, skew int, after int64) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != p.Digits {
		return 0, false
	}
	if _, err := strconv.ParseUint(code, 10, 64); err != nil {
		return 0, false
	}
	current := p.Step(now)
	matched, found := int64(0), false
	for d := -int64(skew); d <= int64(skew); d++ {
		step := current + d
		eq := subtle.ConstantTimeCompare([]byte(p.Code(secret, step)), []byte(code)) == 1
		if eq && step > after && !found {
			matched, found = step, true
		}
	}
	return matched, found
}

// URI is the otpauth:// URI (the Key Uri Format of Google Authenticator) an app reads from the QR code.
func URI(issuer, account string, secret []byte, p Params) string {
	q := url.Values{}
	q.Set("secret", EncodeSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", p.Algorithm.String())
	q.Set("digits", strconv.Itoa(p.Digits))
	q.Set("period", strconv.Itoa(int(p.Period/time.Second)))
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	return "otpauth://totp/" + label + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}
