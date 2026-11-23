package totp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cybericebox/backend/pkg/totp"
)

// RFC 4226, Appendix D: HOTP values of the secret "12345678901234567890" for the counters 0 to 9.
func TestHOTPMatchesRFC4226Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, code := range want {
		if got := totp.HOTP(secret, uint64(counter), 6, totp.SHA1); got != code {
			t.Errorf("HOTP(counter %d) = %s, want %s", counter, got, code)
		}
	}
}

// RFC 6238, Appendix B: TOTP values (8 digits, 30 s steps) for SHA-1, SHA-256 and SHA-512.
func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	seeds := map[totp.Algorithm][]byte{
		totp.SHA1:   []byte("12345678901234567890"),
		totp.SHA256: []byte("12345678901234567890123456789012"),
		totp.SHA512: []byte("1234567890123456789012345678901234567890123456789012345678901234"),
	}
	vectors := []struct {
		unix int64
		alg  totp.Algorithm
		code string
	}{
		{59, totp.SHA1, "94287082"}, {59, totp.SHA256, "46119246"}, {59, totp.SHA512, "90693936"},
		{1111111109, totp.SHA1, "07081804"}, {1111111109, totp.SHA256, "68084774"}, {1111111109, totp.SHA512, "25091201"},
		{1111111111, totp.SHA1, "14050471"}, {1111111111, totp.SHA256, "67062674"}, {1111111111, totp.SHA512, "99943326"},
		{1234567890, totp.SHA1, "89005924"}, {1234567890, totp.SHA256, "91819424"}, {1234567890, totp.SHA512, "93441116"},
		{2000000000, totp.SHA1, "69279037"}, {2000000000, totp.SHA256, "90698825"}, {2000000000, totp.SHA512, "38618901"},
		{20000000000, totp.SHA1, "65353130"}, {20000000000, totp.SHA256, "77737706"}, {20000000000, totp.SHA512, "47863826"},
	}
	for _, v := range vectors {
		p := totp.Params{Digits: 8, Period: 30 * time.Second, Algorithm: v.alg}
		step := p.Step(time.Unix(v.unix, 0))
		if got := p.Code(seeds[v.alg], step); got != v.code {
			t.Errorf("TOTP(%d, %v) = %s, want %s", v.unix, v.alg, got, v.code)
		}
	}
}

func TestMatchAcceptsOneStepEitherSideOnly(t *testing.T) {
	secret := []byte("12345678901234567890")
	p := totp.Default
	now := time.Unix(1111111111, 0)
	step := p.Step(now)
	for _, d := range []int64{-1, 0, 1} {
		got, ok := p.Match(secret, p.Code(secret, step+d), now, 1, 0)
		if !ok || got != step+d {
			t.Errorf("offset %d: got step %d ok %v, want step %d", d, got, ok, step+d)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := p.Match(secret, p.Code(secret, step+d), now, 1, 0); ok {
			t.Errorf("offset %d must be refused", d)
		}
	}
}

func TestMatchRefusesAStepNotAfterTheLastUsedOne(t *testing.T) {
	secret := []byte("12345678901234567890")
	p := totp.Default
	now := time.Unix(1111111111, 0)
	step := p.Step(now)
	code := p.Code(secret, step)
	if _, ok := p.Match(secret, code, now, 1, step); ok {
		t.Fatal("a code of the last used step must be refused (reuse)")
	}
	if _, ok := p.Match(secret, p.Code(secret, step-1), now, 1, step); ok {
		t.Fatal("a code older than the last used step must be refused")
	}
	if got, ok := p.Match(secret, p.Code(secret, step+1), now, 1, step); !ok || got != step+1 {
		t.Fatal("the next step after the last used one is accepted")
	}
}

func TestMatchRefusesMalformedCodes(t *testing.T) {
	secret := []byte("12345678901234567890")
	p := totp.Default
	now := time.Unix(1111111111, 0)
	for _, code := range []string{"", "12345", "1234567", "12a456", "１２３４５６"} {
		if _, ok := p.Match(secret, code, now, 1, 0); ok {
			t.Errorf("code %q must be refused", code)
		}
	}
	// Spaces people type between the halves are ignored.
	c := p.Code(secret, p.Step(now))
	if _, ok := p.Match(secret, c[:3]+" "+c[3:], now, 1, 0); !ok {
		t.Error("a code with a space in the middle is accepted")
	}
}

func TestSecretRoundTripAndURI(t *testing.T) {
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != totp.SecretSize {
		t.Fatalf("secret is %d bytes, want %d", len(secret), totp.SecretSize)
	}
	enc := totp.EncodeSecret(secret)
	if strings.Contains(enc, "=") {
		t.Fatalf("encoded secret %q has padding", enc)
	}
	back, err := totp.DecodeSecret(strings.ToLower(enc[:4]) + " " + enc[4:])
	if err != nil || string(back) != string(secret) {
		t.Fatalf("decode: %v", err)
	}
	uri := totp.URI("Cyber ICE Box", "ann@example.test", secret, totp.Default)
	for _, part := range []string{"otpauth://totp/Cyber%20ICE%20Box:ann@example.test?", "secret=" + enc, "issuer=Cyber%20ICE%20Box", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, part) {
			t.Errorf("uri %q lacks %q", uri, part)
		}
	}
}
