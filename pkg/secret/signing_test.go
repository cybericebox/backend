package secret_test

import (
	"crypto/hkdf"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/cybericebox/backend/pkg/secret"
)

const (
	signMasterA = "m1:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	signMasterB = "m2:202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	signOld     = "an-old-explicit-signing-value-of-32-bytes!"
)

func TestSigningKeys_FromTheMasterKeyring(t *testing.T) {
	keys, err := secret.SigningKeys(signMasterB+","+signMasterA, "", secret.PurposeJWTSetup)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != "d-m2" || keys[1].ID != "d-m1" {
		t.Fatalf("the first master version is current: %+v", keys)
	}
	want, err := hkdf.Key(sha256.New, mustHex(t, strings.TrimPrefix(signMasterB, "m2:")), nil, "cybericebox/secrets/v1/jwt-setup", 32)
	if err != nil {
		t.Fatal(err)
	}
	if string(keys[0].Key) != string(want) {
		t.Fatal("the key is HKDF-SHA256 of the master version with the label of the purpose")
	}
}

func TestSigningKeys_PurposesAreSeparated(t *testing.T) {
	jwtKeys, _ := secret.SigningKeys(signMasterA, "", secret.PurposeJWTSetup)
	oauthKeys, _ := secret.SigningKeys(signMasterA, "", secret.PurposeOAuthState)
	sessionLike, _ := secret.Resolve(signMasterA, "", secret.PurposeSession, false)
	if string(jwtKeys[0].Key) == string(oauthKeys[0].Key) {
		t.Fatal("two purposes must not share a key")
	}
	if strings.Contains(sessionLike, string(jwtKeys[0].Key)) {
		t.Fatal("the session key is not a signing key")
	}
}

func TestSigningKeys_ExplicitValueOnlyVerifiesNextToAMaster(t *testing.T) {
	keys, err := secret.SigningKeys(signMasterA, signOld, secret.PurposeJWTSetup)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != "d-m1" || keys[1].ID != secret.ExplicitSigningKeyID || string(keys[1].Key) != signOld {
		t.Fatalf("the derived key signs, the explicit value stays as the last, verify-only key: %+v", keys)
	}
}

func TestSigningKeys_WithoutAMasterTheExplicitValueWorksAsToday(t *testing.T) {
	keys, err := secret.SigningKeys("", signOld, secret.PurposeJWTSetup)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != "" || string(keys[0].Key) != signOld {
		t.Fatalf("one key without an id (tokens carry no kid, as before): %+v", keys)
	}
	none, err := secret.SigningKeys("", "", secret.PurposeJWTSetup)
	if err != nil || len(none) != 0 {
		t.Fatalf("nothing configured is no keys, not an error: %v %v", none, err)
	}
}

func TestSigningKeys_BadMasterIsAnErrorWithoutTheValue(t *testing.T) {
	_, err := secret.SigningKeys("m1:not-hex-but-secret-looking-text", "", secret.PurposeJWTSetup)
	if err == nil || strings.Contains(err.Error(), "secret-looking") {
		t.Fatalf("an error that does not echo the value: %v", err)
	}
}

func TestParseHS256_PicksByKidAndTriesAllWithoutOne(t *testing.T) {
	keys, _ := secret.SigningKeys(signMasterB+","+signMasterA, signOld, secret.PurposeJWTSetup)
	claims := func() *jwt.RegisteredClaims {
		return &jwt.RegisteredClaims{Issuer: "id", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	}
	// signed by the current key: carries its kid
	signed, err := secret.SignHS256(claims(), keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if kid := headerKid(t, signed); kid != "d-m2" {
		t.Fatalf("kid %q", kid)
	}
	if _, perr := secret.ParseHS256(signed, &jwt.RegisteredClaims{}, keys); perr != nil {
		t.Fatalf("current key: %v", perr)
	}
	// a kid that is not in the ring is refused (the other keys are not tried)
	if _, perr := secret.ParseHS256(signed, &jwt.RegisteredClaims{}, keys[1:]); perr == nil {
		t.Fatal("a token whose kid names a key that is not loaded must fail")
	}
	// an old token without a kid, signed by the explicit value: every key is tried
	old, err := secret.SignHS256(claims(), secret.SigningKey{Key: []byte(signOld)})
	if err != nil {
		t.Fatal(err)
	}
	if kid := headerKid(t, old); kid != "" {
		t.Fatalf("a key without an id signs without a kid, got %q", kid)
	}
	if _, perr := secret.ParseHS256(old, &jwt.RegisteredClaims{}, keys); perr != nil {
		t.Fatalf("an old token without a kid: %v", perr)
	}
	// another value does not verify
	other, _ := secret.SignHS256(claims(), secret.SigningKey{Key: []byte("some-other-value-that-is-long-enough!!")})
	if _, perr := secret.ParseHS256(other, &jwt.RegisteredClaims{}, keys); perr == nil {
		t.Fatal("a token of another key must fail")
	}
	// only HS256 is accepted
	none := jwt.NewWithClaims(jwt.SigningMethodNone, claims())
	forged, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, perr := secret.ParseHS256(forged, &jwt.RegisteredClaims{}, keys); perr == nil {
		t.Fatal("alg none must fail")
	}
}

func headerKid(t *testing.T, signed string) string {
	t.Helper()
	tok, _, err := jwt.NewParser().ParseUnverified(signed, &jwt.RegisteredClaims{})
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := tok.Header["kid"].(string)
	return kid
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, len(s)/2)
	for i := range b {
		var v byte
		for _, c := range s[2*i : 2*i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			default:
				v |= byte(c-'a') + 10
			}
		}
		b[i] = v
	}
	return b
}
