package token_test

import (
	"testing"

	"github.com/gofrs/uuid"
	"github.com/golang-jwt/jwt/v5"

	"github.com/cybericebox/backend/pkg/secret"
	"github.com/cybericebox/backend/pkg/token"
)

const (
	rotMasterOld = "m1:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	rotMasterNew = "m2:202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	rotExplicit  = "an-old-explicit-signing-value-of-32-bytes!"
)

func ringClient(t *testing.T, master, explicit string) *token.Client {
	t.Helper()
	keys, err := secret.SigningKeys(master, explicit, secret.PurposeJWTSetup)
	if err != nil {
		t.Fatal(err)
	}
	c, err := token.New(token.Config{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func kidOf(t *testing.T, signed string) string {
	t.Helper()
	tok, _, err := jwt.NewParser().ParseUnverified(signed, &jwt.RegisteredClaims{})
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := tok.Header["kid"].(string)
	return kid
}

func TestRotation_NewTokensCarryTheKidOfTheCurrentKey(t *testing.T) {
	c := ringClient(t, rotMasterNew+","+rotMasterOld, "")
	signed, err := c.GenerateSetupToken(uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	if kid := kidOf(t, signed); kid != "d-m2" {
		t.Fatalf("kid %q", kid)
	}
}

func TestRotation_ATokenOfTheOldMasterVersionStillParsesUntilItIsDropped(t *testing.T) {
	user := uuid.Must(uuid.NewV7())
	before := ringClient(t, rotMasterOld, "")
	signed, err := before.GenerateSetupToken(user)
	if err != nil {
		t.Fatal(err)
	}
	both := ringClient(t, rotMasterNew+","+rotMasterOld, "")
	if got, perr := both.ParseSetupToken(signed); perr != nil || got != user {
		t.Fatalf("the old master version is still in the ring: %v", perr)
	}
	onlyNew := ringClient(t, rotMasterNew, "")
	if _, perr := onlyNew.ParseSetupToken(signed); perr == nil {
		t.Fatal("after the old master version is dropped its tokens must fail")
	}
}

func TestRotation_ALinkMailedBeforeTheMoveKeepsWorking(t *testing.T) {
	user := uuid.Must(uuid.NewV7())
	// issued by the installation that still had only JWT_TOKEN_SIGNATURE: no kid
	legacy := token.MustNew(token.Config{TokenSignature: rotExplicit})
	signed, err := legacy.GenerateSetupToken(user)
	if err != nil {
		t.Fatal(err)
	}
	if kid := kidOf(t, signed); kid != "" {
		t.Fatalf("the single explicit value signs without a kid, got %q", kid)
	}
	moved := ringClient(t, rotMasterOld, rotExplicit)
	if got, perr := moved.ParseSetupToken(signed); perr != nil || got != user {
		t.Fatalf("the explicit value is a verify-only key: %v", perr)
	}
	fresh, err := moved.GenerateSetupToken(user)
	if err != nil {
		t.Fatal(err)
	}
	if kid := kidOf(t, fresh); kid != "d-m1" {
		t.Fatalf("new tokens are signed by the derived key, kid %q", kid)
	}
	if _, perr := legacy.ParseSetupToken(fresh); perr == nil {
		t.Fatal("the explicit value must not verify a token of the derived key")
	}
}

func TestRotation_WithoutAMasterKeyNothingChanges(t *testing.T) {
	c := ringClient(t, "", rotExplicit)
	signed, err := c.GenerateSetupToken(uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	if kid := kidOf(t, signed); kid != "" {
		t.Fatalf("no kid without a master key, got %q", kid)
	}
	plain := token.MustNew(token.Config{TokenSignature: rotExplicit})
	if _, perr := plain.ParseSetupToken(signed); perr != nil {
		t.Fatalf("the same bytes as the single-value client: %v", perr)
	}
}

func TestRotation_KeysWinOverTheShorthand(t *testing.T) {
	keys, _ := secret.SigningKeys(rotMasterOld, "", secret.PurposeJWTSetup)
	c, err := token.New(token.Config{Keys: keys, TokenSignature: rotExplicit})
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := c.GenerateSetupToken(uuid.Must(uuid.NewV7()))
	if kid := kidOf(t, signed); kid != "d-m1" {
		t.Fatalf("kid %q", kid)
	}
}
