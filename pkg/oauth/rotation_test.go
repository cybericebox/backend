package oauth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/cybericebox/daemon/pkg/oauth"
	"github.com/cybericebox/daemon/pkg/secret"
)

const (
	stateMasterOld = "m1:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	stateMasterNew = "m2:202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	stateExplicit  = "an-old-explicit-state-value-of-32-bytes!!"
)

func keyedClient(t *testing.T, master, explicit string) *oauth.Client {
	t.Helper()
	keys, err := secret.SigningKeys(master, explicit, secret.PurposeOAuthState)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.StateSignature = ""
	cfg.StateKeys = keys
	c, err := oauth.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func stateKid(t *testing.T, state string) string {
	t.Helper()
	tok, _, err := jwt.NewParser().ParseUnverified(state, &jwt.RegisteredClaims{})
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := tok.Header["kid"].(string)
	return kid
}

func TestStateRotation_SignedByTheCurrentKeyWithItsKid(t *testing.T) {
	c := keyedClient(t, stateMasterNew+","+stateMasterOld, "")
	_, state, _ := c.GetGoogleLoginURL("", "")
	if kid := stateKid(t, state); kid != "d-m2" {
		t.Fatalf("kid %q", kid)
	}
	if err := c.ValidateState(state); err != nil {
		t.Fatal(err)
	}
}

func TestStateRotation_AFlowStartedBeforeTheSwitchFinishesAfterIt(t *testing.T) {
	before := keyedClient(t, stateMasterOld, "")
	_, state, _ := before.GetGoogleLoginURL("", "")
	after := keyedClient(t, stateMasterNew+","+stateMasterOld, "")
	if err := after.ValidateState(state); err != nil {
		t.Fatalf("the old master version is still in the ring: %v", err)
	}
	if err := keyedClient(t, stateMasterNew, "").ValidateState(state); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("a dropped master version must not verify: %v", err)
	}
}

func TestStateRotation_AnOldStateWithoutAKidIsAcceptedThroughTheExplicitKey(t *testing.T) {
	legacy := mustClient(t) // single StateSignature value, no kid
	cfg := defaultConfig()
	cfg.StateSignature = ""
	keys, _ := secret.SigningKeys(stateMasterOld, cfg2Explicit(), secret.PurposeOAuthState)
	cfg.StateKeys = keys
	moved, newErr := oauth.New(cfg)
	if newErr != nil {
		t.Fatal(newErr)
	}
	_, state, _ := legacy.GetGoogleLoginURL("", "")
	if stateKid(t, state) != "" {
		t.Fatal("the single value signs without a kid")
	}
	if err := moved.ValidateState(state); err != nil {
		t.Fatalf("explicit key verifies: %v", err)
	}
}

func cfg2Explicit() string { return defaultConfig().StateSignature }

func TestStateRotation_WithoutAMasterKeyNothingChanges(t *testing.T) {
	c := keyedClient(t, "", stateExplicit)
	_, state, _ := c.GetGoogleLoginURL("", "")
	if stateKid(t, state) != "" || strings.Count(state, ".") != 2 {
		t.Fatal("a state without a kid")
	}
	cfg := defaultConfig()
	cfg.StateSignature = stateExplicit
	plain, _ := oauth.New(cfg)
	if err := plain.ValidateState(state); err != nil {
		t.Fatal(err)
	}
}
