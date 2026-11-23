package totp_test

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"

	"github.com/cybericebox/backend/pkg/totp"
)

func b32(seed string) string { return base32.StdEncoding.EncodeToString([]byte(seed)) }

// RFC 4226, Appendix D: HOTP values of the secret "12345678901234567890" for the counters 0 to 9 (a TOTP step is an
// HOTP counter).
func TestCodesMatchRFC4226Vectors(t *testing.T) {
	secret := b32("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, code := range want {
		got, err := totp.Default.Code(secret, int64(counter))
		if err != nil || got != code {
			t.Errorf("counter %d: got %s (%v), want %s", counter, got, err, code)
		}
	}
}

// RFC 6238, Appendix B: TOTP values (8 digits, 30 s steps) for SHA-1, SHA-256 and SHA-512.
func TestCodesMatchRFC6238Vectors(t *testing.T) {
	seeds := map[otp.Algorithm]string{
		otp.AlgorithmSHA1:   b32("12345678901234567890"),
		otp.AlgorithmSHA256: b32("12345678901234567890123456789012"),
		otp.AlgorithmSHA512: b32("1234567890123456789012345678901234567890123456789012345678901234"),
	}
	vectors := []struct {
		unix int64
		alg  otp.Algorithm
		code string
	}{
		{59, otp.AlgorithmSHA1, "94287082"}, {59, otp.AlgorithmSHA256, "46119246"}, {59, otp.AlgorithmSHA512, "90693936"},
		{1111111109, otp.AlgorithmSHA1, "07081804"}, {1111111109, otp.AlgorithmSHA256, "68084774"}, {1111111109, otp.AlgorithmSHA512, "25091201"},
		{1111111111, otp.AlgorithmSHA1, "14050471"}, {1111111111, otp.AlgorithmSHA256, "67062674"}, {1111111111, otp.AlgorithmSHA512, "99943326"},
		{1234567890, otp.AlgorithmSHA1, "89005924"}, {1234567890, otp.AlgorithmSHA256, "91819424"}, {1234567890, otp.AlgorithmSHA512, "93441116"},
		{2000000000, otp.AlgorithmSHA1, "69279037"}, {2000000000, otp.AlgorithmSHA256, "90698825"}, {2000000000, otp.AlgorithmSHA512, "38618901"},
		{20000000000, otp.AlgorithmSHA1, "65353130"}, {20000000000, otp.AlgorithmSHA256, "77737706"}, {20000000000, otp.AlgorithmSHA512, "47863826"},
	}
	for _, v := range vectors {
		p := totp.Params{Digits: otp.DigitsEight, Period: 30 * time.Second, Algorithm: v.alg}
		now := time.Unix(v.unix, 0)
		got, err := p.Code(seeds[v.alg], p.Step(now))
		if err != nil || got != v.code {
			t.Errorf("TOTP(%d, %v) = %s (%v), want %s", v.unix, v.alg, got, err, v.code)
		}
		// The wrapper's Match accepts the vector at its own time.
		if _, ok := p.Match(seeds[v.alg], v.code, now, 1, 0); !ok {
			t.Errorf("Match refuses the vector %d %v", v.unix, v.alg)
		}
	}
}

var rfcSecret = b32("12345678901234567890")

func code(t *testing.T, step int64) string {
	t.Helper()
	c, err := totp.Default.Code(rfcSecret, step)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMatchAcceptsOneStepEitherSideOnly(t *testing.T) {
	now := time.Unix(1111111111, 0)
	step := totp.Default.Step(now)
	for _, d := range []int64{-1, 0, 1} {
		got, ok := totp.Default.Match(rfcSecret, code(t, step+d), now, 1, 0)
		if !ok || got != step+d {
			t.Errorf("offset %d: got step %d ok %v, want step %d", d, got, ok, step+d)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := totp.Default.Match(rfcSecret, code(t, step+d), now, 1, 0); ok {
			t.Errorf("offset %d must be refused", d)
		}
	}
}

func TestMatchRefusesAStepNotAfterTheLastUsedOne(t *testing.T) {
	now := time.Unix(1111111111, 0)
	step := totp.Default.Step(now)
	if _, ok := totp.Default.Match(rfcSecret, code(t, step), now, 1, step); ok {
		t.Fatal("a code of the last used step must be refused (reuse)")
	}
	if _, ok := totp.Default.Match(rfcSecret, code(t, step-1), now, 1, step); ok {
		t.Fatal("a code older than the last used step must be refused")
	}
	if got, ok := totp.Default.Match(rfcSecret, code(t, step+1), now, 1, step); !ok || got != step+1 {
		t.Fatal("the next step after the last used one is accepted")
	}
}

func TestMatchRefusesMalformedCodes(t *testing.T) {
	now := time.Unix(1111111111, 0)
	for _, c := range []string{"", "12345", "1234567", "12a456", "１２３４５６"} {
		if _, ok := totp.Default.Match(rfcSecret, c, now, 1, 0); ok {
			t.Errorf("code %q must be refused", c)
		}
	}
	c := code(t, totp.Default.Step(now))
	if _, ok := totp.Default.Match(rfcSecret, c[:3]+" "+c[3:], now, 1, 0); !ok {
		t.Error("a code with a space in the middle is accepted")
	}
}

func TestNewKeyHasASecretOfSecretSizeAndAnOtpauthURI(t *testing.T) {
	k, err := totp.Default.New("Cyber ICE Box", "ann@example.test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(k.Secret)
	if err != nil || len(raw) != totp.SecretSize {
		t.Fatalf("secret %q: %d bytes (%v), want %d", k.Secret, len(raw), err, totp.SecretSize)
	}
	u, err := url.Parse(k.URI)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("uri %q", k.URI)
	}
	q := u.Query()
	if q.Get("secret") != k.Secret || q.Get("issuer") != "Cyber ICE Box" || q.Get("digits") != "6" || q.Get("period") != "30" ||
		!strings.EqualFold(q.Get("algorithm"), "SHA1") {
		t.Fatalf("uri %q has the wrong parameters", k.URI)
	}
	if !strings.Contains(u.Path, "ann@example.test") {
		t.Fatalf("uri %q lacks the account", k.URI)
	}
	other, _ := totp.Default.New("Cyber ICE Box", "ann@example.test")
	if other.Secret == k.Secret {
		t.Fatal("two keys share a secret")
	}
}
