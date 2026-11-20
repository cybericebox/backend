package labaccess

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/golang-jwt/jwt/v5"
)

func testSigningKey(t *testing.T) SigningKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return SigningKey{Tenant: "platform", KeyID: "k-1", Key: key}
}

const (
	webURL     = "https://web-abc123w.challenges.example.com/login"
	consoleURL = "https://db-1-abc123t.challenges.example.com"
)

func testNow() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }

func webSession() Session {
	return Session{Group: "e-0190-t-0191", Client: "p-0192", Lab: "c-1", Device: "web", Kind: KindWeb, AccessURL: webURL}
}

func verify(t *testing.T, sk SigningKey, grant string, at time.Time) (*jwt.Token, claims) {
	t.Helper()
	var got claims
	parsed, err := jwt.ParseWithClaims(grant, &got, func(*jwt.Token) (any, error) { return sk.Key.Public(), nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(sk.Tenant), jwt.WithAudience(Audience),
		jwt.WithTimeFunc(func() time.Time { return at }))
	if err != nil || !parsed.Valid {
		t.Fatalf("the proxy could not verify the grant: %v", err)
	}
	return parsed, got
}

func TestNewRefusesANegativeTTL(t *testing.T) {
	if _, err := New(Config{SessionTTL: -time.Second}); err == nil {
		t.Fatal("a negative ttl must be refused")
	}
}

// Every row of the claims table of the contract (pools-release 2.3.1) as a check on a web grant.
func TestIssueSignsAWebGrantTheProxyCanVerify(t *testing.T) {
	sk := testSigningKey(t)
	issuer, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	session := webSession()
	link, err := issuer.Issue(sk, session, testNow())
	if err != nil {
		t.Fatal(err)
	}

	// The URL is the origin of the device host and the fixed path, with nothing after it.
	parsedURL, err := url.Parse(link.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host != "web-abc123w.challenges.example.com" || parsedURL.Path != AuthPath ||
		parsedURL.RawQuery != "" || parsedURL.Fragment != "" || strings.ContainsAny(link.URL, "?#") || strings.Contains(link.URL, link.Grant) {
		t.Fatalf("link = %s err = %v", link.URL, err)
	}

	parsed, got := verify(t, sk, link.Grant, testNow().Add(10*time.Second))
	if parsed.Header["kid"] != "k-1" || parsed.Header["typ"] != "cib-grant" || parsed.Header["alg"] != "EdDSA" {
		t.Fatalf("header = %v", parsed.Header)
	}
	if got.Issuer != "platform" || got.Subject != session.Client || got.GroupID != session.Group || got.Lab != "c-1" || got.Dev != "web" ||
		got.Host != "web-abc123w.challenges.example.com" || got.Kind != "web" {
		t.Fatalf("claims = %+v", got)
	}
	if !slices.Equal(got.Audience, []string{"laboratory-proxy"}) {
		t.Fatalf("a web grant is meant for the proxy only: %v", got.Audience)
	}
	if got.IssuedAt == nil || got.NotBefore == nil || got.ExpiresAt == nil || !got.IssuedAt.Time.Equal(testNow()) || !got.NotBefore.Time.Equal(testNow()) {
		t.Fatalf("times = %+v", got.RegisteredClaims)
	}
	if want := testNow().Add(DefaultSessionTTL); !got.ExpiresAt.Time.Equal(want) || !link.ExpiresAt.Equal(want) {
		t.Fatalf("exp = %s, link end = %s, want %s", got.ExpiresAt.Time, link.ExpiresAt, want)
	}
}

// Nothing but operator identifiers is in a grant, it is small, and it is refused after exp and with another tenant's key.
func TestAGrantCarriesNoPlatformIdentityAndIsRefusedAfterExpOrWithAnotherKey(t *testing.T) {
	sk := testSigningKey(t)
	issuer, _ := New(Config{})
	link, err := issuer.Issue(sk, webSession(), testNow())
	if err != nil {
		t.Fatal(err)
	}
	_, got := verify(t, sk, link.Grant, testNow())
	// Only operator identifiers: no platform user, event or team id, no session claims, no replay memory.
	raw, _ := json.Marshal(got)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for _, forbidden := range []string{"user_id", "evt", "team", "test", "client", "ver", "sess", "Abs", "jti", "use", "next"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("claim %q must not be in the grant: %s", forbidden, raw)
		}
	}
	if len(link.Grant) > 1024 {
		t.Fatalf("the grant is %d bytes, it must fit a cookie and a form field", len(link.Grant))
	}

	// Refused after exp, and with another tenant's key.
	if _, err = jwt.ParseWithClaims(link.Grant, &claims{}, func(*jwt.Token) (any, error) { return sk.Key.Public(), nil },
		jwt.WithTimeFunc(func() time.Time { return testNow().Add(DefaultSessionTTL + time.Minute) })); err == nil {
		t.Fatal("an expired grant must be refused")
	}
	other := testSigningKey(t)
	if _, err = jwt.ParseWithClaims(link.Grant, &claims{}, func(*jwt.Token) (any, error) { return other.Key.Public(), nil },
		jwt.WithTimeFunc(func() time.Time { return testNow() })); err == nil {
		t.Fatal("a grant must not verify with another tenant's key")
	}
}

// A console grant is meant for the proxy and for device-access, and is bound to the console host.
func TestIssueConsoleGrantHasBothAudiencesAndTheConsoleHost(t *testing.T) {
	sk := testSigningKey(t)
	issuer, _ := New(Config{})
	session := Session{Group: "g", Client: "p-1", Lab: "c-9", Device: "db-1", Kind: KindConsole, AccessURL: consoleURL}
	link, err := issuer.Issue(sk, session, testNow())
	if err != nil {
		t.Fatal(err)
	}
	parsedURL, err := url.Parse(link.URL)
	if err != nil || parsedURL.Host != "db-1-abc123t.challenges.example.com" || parsedURL.Path != AuthPath || strings.ContainsAny(link.URL, "?#") {
		t.Fatalf("link = %s err = %v", link.URL, err)
	}
	_, got := verify(t, sk, link.Grant, testNow())
	if got.Kind != "console" || got.Host != "db-1-abc123t.challenges.example.com" || got.Lab != "c-9" || got.Dev != "db-1" {
		t.Fatalf("claims = %+v", got)
	}
	if !slices.Equal(got.Audience, []string{"laboratory-proxy", "laboratory-device-access"}) {
		t.Fatalf("a console grant is meant for the proxy and device-access: %v", got.Audience)
	}
}

// The kind must agree with the access letter of the host: a web grant for a console host, a console grant for a web host
// and an unknown kind are all refused, as the proxy would refuse them.
func TestIssueRefusesAKindThatDisagreesWithTheHost(t *testing.T) {
	sk := testSigningKey(t)
	issuer, _ := New(Config{})
	web, console := webSession(), webSession()
	web.AccessURL = consoleURL
	console.Kind = KindConsole
	unknown := webSession()
	unknown.Kind = "vnc"
	for name, s := range map[string]Session{"web grant on a console host": web, "console grant on a web host": console, "unknown kind": unknown} {
		if _, err := issuer.Issue(sk, s, testNow()); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	// An empty kind is a web grant.
	implicit := webSession()
	implicit.Kind = ""
	link, err := issuer.Issue(sk, implicit, testNow())
	if err != nil {
		t.Fatal(err)
	}
	if _, got := verify(t, sk, link.Grant, testNow()); got.Kind != "web" {
		t.Fatalf("kind = %q", got.Kind)
	}
}

func TestIssueRefusesAnIncompleteSessionOrKey(t *testing.T) {
	sk := testSigningKey(t)
	issuer, _ := New(Config{})
	for name, mutate := range map[string]func(*Session){
		"no group":   func(s *Session) { s.Group = "" },
		"no client":  func(s *Session) { s.Client = "" },
		"no lab":     func(s *Session) { s.Lab = "" },
		"no device":  func(s *Session) { s.Device = "" },
		"no address": func(s *Session) { s.AccessURL = "" },
	} {
		s := webSession()
		mutate(&s)
		if _, err := issuer.Issue(sk, s, testNow()); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	for name, bad := range map[string]SigningKey{
		"no tenant": {KeyID: "k", Key: sk.Key}, "no key id": {Tenant: "t", Key: sk.Key}, "no key": {Tenant: "t", KeyID: "k"},
	} {
		if _, err := issuer.Issue(bad, webSession(), testNow()); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestIssueLivesUntilTheRequestedEnd(t *testing.T) {
	sk := testSigningKey(t)
	issuer, _ := New(Config{})
	session := webSession()
	end := func(l Link) int64 {
		_, got := verify(t, sk, l.Grant, testNow())
		return got.ExpiresAt.Unix()
	}

	session.ExpiresAt = testNow().Add(72 * time.Hour)
	link, err := issuer.Issue(sk, session, testNow())
	if err != nil {
		t.Fatal(err)
	}
	if !link.ExpiresAt.Equal(session.ExpiresAt) || end(link) != session.ExpiresAt.Unix() {
		t.Fatalf("the grant must last until the event end: %s", link.ExpiresAt)
	}

	// An end that is not in the future falls back to the TTL.
	session.ExpiresAt = testNow().Add(-time.Hour)
	link, _ = issuer.Issue(sk, session, testNow())
	if !link.ExpiresAt.Equal(testNow().Add(DefaultSessionTTL)) || end(link) != testNow().Add(DefaultSessionTTL).Unix() {
		t.Fatalf("fallback ttl expected, got %s", link.ExpiresAt)
	}

	// A configured fallback replaces the default.
	short, _ := New(Config{SessionTTL: 2 * time.Hour})
	link, _ = short.Issue(sk, webSession(), testNow())
	if !link.ExpiresAt.Equal(testNow().Add(2 * time.Hour)) {
		t.Fatalf("configured fallback: %s", link.ExpiresAt)
	}
}

// The agent's limit caps exp - iat, so the proxy accepts what is signed. An end that is sooner keeps itself; with no
// report nothing is capped here.
func TestTheAgentsGrantLimitCapsTheGrant(t *testing.T) {
	sk := testSigningKey(t)
	sk.GrantMaxTTL = time.Hour
	issuer, err := New(Config{SessionTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	link, err := issuer.Issue(sk, webSession(), testNow())
	if err != nil {
		t.Fatal(err)
	}
	_, got := verify(t, sk, link.Grant, testNow())
	if lifetime := got.ExpiresAt.Time.Sub(got.IssuedAt.Time); lifetime != time.Hour || !link.ExpiresAt.Equal(testNow().Add(time.Hour)) {
		t.Fatalf("the grant lives %v, link end %v, want the limit of an hour", lifetime, link.ExpiresAt)
	}

	// An event end beyond the limit is capped too; one before it is kept.
	far := webSession()
	far.ExpiresAt = testNow().Add(10 * time.Hour)
	link, _ = issuer.Issue(sk, far, testNow())
	if !link.ExpiresAt.Equal(testNow().Add(time.Hour)) {
		t.Fatalf("event end beyond the limit: %v", link.ExpiresAt)
	}
	near := webSession()
	near.ExpiresAt = testNow().Add(10 * time.Minute)
	link, _ = issuer.Issue(sk, near, testNow())
	if !link.ExpiresAt.Equal(testNow().Add(10 * time.Minute)) {
		t.Fatalf("event end before the limit: %v", link.ExpiresAt)
	}

	sk.GrantMaxTTL = 0
	link, _ = issuer.Issue(sk, webSession(), testNow())
	if !link.ExpiresAt.Equal(testNow().Add(24 * time.Hour)) {
		t.Fatalf("no report: %v", link.ExpiresAt)
	}
}

// The host claim is the host the grant opens: lower case and without a port, while the URL keeps the origin it must post to.
func TestTheHostClaimIsTheHostOfTheLinkItOpens(t *testing.T) {
	sk := testSigningKey(t)
	issuer, _ := New(Config{})
	for _, access := range []string{"https://web-abc123w.challenges.example.com", "https://Web-abc123w.Challenges.Example.com:8443/app?x=1"} {
		s := webSession()
		s.AccessURL = access
		s.ExpiresAt = testNow().Add(time.Minute)
		link, err := issuer.Issue(sk, s, testNow())
		if err != nil {
			t.Fatal(err)
		}
		_, claim := verify(t, sk, link.Grant, testNow())
		opened, _ := url.Parse(link.URL)
		if claim.Host != "web-abc123w.challenges.example.com" || claim.Host != opened.Hostname() || opened.Path != AuthPath || opened.RawQuery != "" {
			t.Fatalf("host claim %q, link %s", claim.Host, link.URL)
		}
	}
}

// The JSON of a link is PascalCase with exactly these fields: the frontends post Grant to URL.
func TestLinkJSONShape(t *testing.T) {
	link := Link{
		LabID: uuid.Must(uuid.FromString("0192f0a0-0000-7000-8000-000000000c02")), Revision: "3",
		URL: "https://web-abc123w.challenges.example.com/_auth", Grant: "jws", ExpiresAt: testNow(),
	}
	raw, err := json.Marshal(link)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"ExpiresAt", "Grant", "LabID", "Revision", "URL"}) || fields["Grant"] != "jws" {
		t.Fatalf("link JSON = %s", raw)
	}
}
