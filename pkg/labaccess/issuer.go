// Package labaccess signs the access grants of the laboratory: the proof that lets one user open one device
// of one lab, on the web (the L7 proxy) or as a console (the L7 proxy and the device-access service).
//
// A grant is one compact JWS, EdDSA (Ed25519), signed with the key of the tenant (every infrastructure
// agent is a tenant with its own key; the laboratory holds only the public keys). The platform returns it
// next to a query-free URL (https://<host>/_auth) and the frontend posts it in a form, so it never appears
// in a URL, a history entry or a referrer. The proxy verifies the signature offline and the grant itself is
// the session: there is no refresh, no sliding and no replay memory. Opening the device again fetches a new
// grant.
//
// A grant carries what the operator knows and nothing else: iss = the tenant, kid = the key id, sub = the
// LabGroupClient of the user (the same object that is the VPN peer), group_id = the lab group (also the
// namespace), lab, dev (the topology device name), host (the full access host the grant is bound to), kind
// (web or console), aud and iat, nbf, exp. No platform user, event or team id is in it. The proxy authorizes
// every request by the group access policy as well, so blocking a client refuses a still valid grant.
// Clients are created by the platform when a team is formed or a member is added, never while issuing.
//
// exp is the end of the access the platform wants (the event's effective finish plus a buffer, or a test
// deploy's lease end; the configured session TTL is the fallback), never later than the limit the agent
// reports (ProxyFeature.grant_max_ttl_seconds), so the proxy accepts what is signed.
package labaccess

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// DefaultSessionTTL is the default grant length when the caller names no end (LAB_SESSION_TTL; an
	// event without a finish).
	DefaultSessionTTL = 24 * time.Hour
	// AuthPath is the path the form with the grant is posted to, on the device host.
	AuthPath = "/_auth"
	// GrantType is the typ header of a grant.
	GrantType = "cib-grant"
)

// The audiences of a grant: the proxy accepts only grants meant for it; a console grant is also meant for
// the device-access service, which must not accept a web grant.
const (
	Audience             = "laboratory-proxy"
	DeviceAccessAudience = "laboratory-device-access"
)

// Kind is what the grant opens.
type Kind string

const (
	// KindWeb opens a web device on its own host, the proxy keeps the grant as a cookie.
	KindWeb Kind = "web"
	// KindConsole opens the console of a device on its console host.
	KindConsole Kind = "console"
)

// consoleHostLetter is the last character of the first label of a console host: the access kind of
// <device>-<code><letter> (the laboratory's names.AccessKindTerminal).
const consoleHostLetter = 't'

// Config sets the fallback length of a grant.
type Config struct {
	// SessionTTL is the grant length when the caller names no end; zero means DefaultSessionTTL.
	SessionTTL time.Duration
}

// SigningKey is the key of one tenant that signs its grants: the tenant name is the issuer, KeyID the key id
// the proxy picks the verifying key by (the grant's kid header).
type SigningKey struct {
	Tenant string
	KeyID  string
	Key    ed25519.PrivateKey
	// GrantMaxTTL is the longest exp - iat the tenant's proxy and device-access accept (its agent reports
	// it). Zero = not reported, no cap is applied here.
	GrantMaxTTL time.Duration
}

// Session is who the grant is for and what it opens, in the operator's own terms: a LabGroup and one
// LabGroupClient of that group (the same object that is the participant's VPN peer, so blocking it in the
// group access policy blocks the VPN and the web together), one device of one lab. The platform knows which
// event, team or test deploy a group belongs to and which user a client is; the grant does not say.
type Session struct {
	// Group is the lab group name (e-<event>-t-<team> or t-<deploy>), which is also its namespace.
	Group string
	// Client is the LabGroupClient name inside the group (p-<user id>).
	Client string
	// Lab is the lab name inside the group.
	Lab string
	// Device is the topology device name.
	Device string
	// Kind is what the grant opens; empty means KindWeb.
	Kind Kind
	// AccessURL is the device URL from the lab status (https://<device>-<code><letter>.<base>/...). The
	// grant is bound to its host and the post goes to its origin.
	AccessURL string
	// ExpiresAt is the wanted end of the grant (the event's effective finish plus a buffer, or a test
	// deploy's lease end). Zero, or a time that is not in the future, means the fallback TTL.
	ExpiresAt time.Time
}

// Link is what the platform hands to the frontend: where to post and what to post.
type Link struct {
	LabID    uuid.UUID `json:"LabID"`
	Revision string    `json:"Revision"`
	// URL is https://<host>/_auth, never with a query or a fragment: the grant must not be in a URL.
	URL string `json:"URL"`
	// Grant is the signed JWS, posted in the form field `grant`.
	Grant string `json:"Grant"`
	// ExpiresAt is the end of the grant (its exp).
	ExpiresAt time.Time `json:"ExpiresAt"`
}

// claims: iss = the tenant, sub = the LabGroupClient, aud, iat/nbf/exp and group_id (the lab group id), lab,
// dev, host (the full host of the device access) and kind.
type claims struct {
	GroupID string `json:"group_id"`
	Lab     string `json:"lab"`
	Dev     string `json:"dev"`
	Host    string `json:"host"`
	Kind    string `json:"kind"`
	jwt.RegisteredClaims
}

type Issuer struct {
	sessionTTL time.Duration
}

// New returns an issuer; the signing key is given per grant.
func New(cfg Config) (*Issuer, error) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.SessionTTL < 0 {
		return nil, errors.New("labaccess: the session ttl must be positive")
	}
	return &Issuer{sessionTTL: cfg.SessionTTL}, nil
}

// SessionTTL is the grant length when the caller names no end.
func (i *Issuer) SessionTTL() time.Duration { return i.sessionTTL }

// hostLetter is the access kind character of an access host: the last character of its first label.
func hostLetter(host string) byte {
	label, _, _ := strings.Cut(host, ".")
	if label == "" {
		return 0
	}
	return label[len(label)-1]
}

// Issue signs a grant for one device with the tenant's key, valid from now.
func (i *Issuer) Issue(sk SigningKey, s Session, now time.Time) (Link, error) {
	if sk.Tenant == "" || sk.KeyID == "" || len(sk.Key) != ed25519.PrivateKeySize {
		return Link{}, errors.New("labaccess: no signing key")
	}
	if s.Group == "" || s.Client == "" || s.Lab == "" || s.Device == "" {
		return Link{}, errors.New("labaccess: incomplete session")
	}
	kind := s.Kind
	if kind == "" {
		kind = KindWeb
	}
	if kind != KindWeb && kind != KindConsole {
		return Link{}, fmt.Errorf("labaccess: unknown kind %q", kind)
	}
	origin, err := url.Parse(s.AccessURL)
	if err != nil || origin.Scheme == "" || origin.Hostname() == "" {
		return Link{}, errors.New("labaccess: the device has no web address")
	}
	host := strings.ToLower(origin.Hostname()) // the full host of the device access: the proxy requires exact equality
	// The kind must agree with the access letter of the host, as the proxy checks.
	if isConsoleHost := hostLetter(host) == consoleHostLetter; isConsoleHost != (kind == KindConsole) {
		return Link{}, errors.New("labaccess: the kind does not agree with the access host")
	}
	end := now.Add(i.sessionTTL)
	if s.ExpiresAt.After(now) {
		end = s.ExpiresAt
	}
	// The proxy refuses a longer grant: say so in the grant instead of promising more.
	if sk.GrantMaxTTL > 0 && end.After(now.Add(sk.GrantMaxTTL)) {
		end = now.Add(sk.GrantMaxTTL)
	}
	aud := jwt.ClaimStrings{Audience}
	if kind == KindConsole {
		aud = jwt.ClaimStrings{Audience, DeviceAccessAudience}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims{
		GroupID: s.Group, Lab: s.Lab, Dev: s.Device, Host: host, Kind: string(kind),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: sk.Tenant, Subject: s.Client, Audience: aud,
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(end),
		},
	})
	token.Header["kid"] = sk.KeyID
	token.Header["typ"] = GrantType
	signed, err := token.SignedString(sk.Key)
	if err != nil {
		return Link{}, fmt.Errorf("sign lab access grant: %w", err)
	}
	// The URL carries no query and no fragment: the grant is posted, never put in an address.
	link := url.URL{Scheme: origin.Scheme, Host: strings.ToLower(origin.Host), Path: AuthPath}
	return Link{URL: link.String(), Grant: signed, ExpiresAt: end.Truncate(time.Second)}, nil
}
