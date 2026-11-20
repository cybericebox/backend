package secretkeys

import (
	"fmt"
	"time"

	"github.com/cybericebox/daemon/pkg/secret"
)

// PurposeKind says how `keys check` can speak about what a derived purpose key seals.
type PurposeKind int

const (
	// ScannedPurpose: the daemon reads every stored value sealed by the purpose (it is a Family).
	ScannedPurpose PurposeKind = iota
	// LifetimeBoundPurpose: what it seals is not stored by us but lives for a bounded time; the rule is a time.
	LifetimeBoundPurpose
	// NotCoveredPurpose: the daemon can say nothing about it.
	NotCoveredPurpose
)

// Lifetimes are the configured times that bound what lives outside the database.
type Lifetimes struct {
	SessionIdle      time.Duration // SESSION_IDLE_TTL
	SessionAbsolute  time.Duration // SESSION_ABSOLUTE_TTL
	EventRenderGrant time.Duration // the longest life of an event-render grant
	SetupToken       time.Duration // the longest life of a setup link: the larger of SETUP_TOKEN_TTL and SIGNUP_SETUP_TOKEN_TTL
	OAuthState       time.Duration // OAUTH_STATE_TTL
}

// PurposeRule is what `keys check` says about one purpose.
type PurposeRule struct {
	Kind   PurposeKind
	Family Family // ScannedPurpose only
	// Rule is the sentence an operator acts on, for a lifetime-bound or not covered purpose.
	Rule func(Lifetimes) string
}

// PurposeRules is the one table of what is scanned, what is bound by a lifetime and what is not covered. Every purpose
// the key derivation knows (secret.AllPurposes) must be here; a test fails when one is missing, so a purpose added later
// cannot be forgotten by `keys check`.
var PurposeRules = map[secret.Purpose]PurposeRule{
	secret.PurposePlatform: {Kind: ScannedPurpose, Family: FamilyPlatform},
	secret.PurposeVPN:      {Kind: ScannedPurpose, Family: FamilyVPN},
	secret.PurposeExercise: {Kind: ScannedPurpose, Family: FamilyExercise},
	secret.PurposeFiles:    {Kind: ScannedPurpose, Family: FamilyFiles},
	secret.PurposeSession: {Kind: LifetimeBoundPurpose, Rule: func(l Lifetimes) string {
		return fmt.Sprintf("the session cookie key: a cookie sealed by it is valid for at most SESSION_IDLE_TTL=%s after it was issued "+
			"(a cookie in use is sealed again with the current key); SESSION_ABSOLUTE_TTL=%s ends a session however it is used; "+
			"the event-render signing keys it seeds sign grants of at most %s. It is retirable when %s have passed since EVERY "+
			"instance loaded the new master key (use %s to be sure no user has to sign in again). The daemon cannot know when "+
			"the instances switched",
			l.SessionIdle, l.SessionAbsolute, l.EventRenderGrant, l.SessionIdle, l.SessionAbsolute)
	}},
	secret.PurposeJWTSetup: {Kind: LifetimeBoundPurpose, Rule: func(l Lifetimes) string {
		return fmt.Sprintf("the signature of setup and invitation links: nothing is stored sealed with it, so the daemon does not scan "+
			"it; a link signed by it is valid for at most %s (the longer of SETUP_TOKEN_TTL and SIGNUP_SETUP_TOKEN_TTL). "+
			"It is retirable after %s since EVERY instance loaded the new master key (a link mailed earlier then expires; the "+
			"old explicit JWT_TOKEN_SIGNATURE, if still set, is read the same way). The daemon cannot know when the instances switched",
			l.SetupToken, l.SetupToken)
	}},
	secret.PurposeOAuthState: {Kind: LifetimeBoundPurpose, Rule: func(l Lifetimes) string {
		return fmt.Sprintf("the state of a Google sign-in: nothing is stored sealed with it; a state is valid for at most "+
			"OAUTH_STATE_TTL=%s. It is retirable after %s since EVERY instance loaded the new master key (a sign-in started "+
			"before then fails and is started again). The daemon cannot know when the instances switched", l.OAuthState, l.OAuthState)
	}},
}
