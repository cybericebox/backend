// Package labaccess signs the lab access tokens of the laboratory L7 proxy: the
// handoff links. The platform sets no cookie on the lab domain (the API and the
// labs live on different domains): the task page asks for a link per device and
// opens https://<device>-<code>.<base>/_auth?t=<jwt>. The proxy verifies the
// EdDSA (Ed25519) signature offline, then sets its own cookie and redirects to
// the site root. The proxy holds only the public keys of the tenant: every
// infrastructure agent has its own tenant and its own signing key, so a token
// is signed with the key of the agent that holds the lab group (iss = the tenant
// name, kid = the key id, aud = laboratory-proxy).
//
// A token carries what the operator knows and nothing else: the lab group
// (group_id, also the namespace), the LabGroupClient of that group (client, the
// same object that is the VPN peer), the device host label the link is for
// (host), the end of the session the proxy cookie gets (sess: the event's
// effective finish plus a buffer, or a test deploy's lease end; DefaultSessionTTL
// is the fallback), and iat, exp and a version. The token itself lives about a
// minute (LAB_ACCESS_TOKEN_TTL, five at most) and is stateless: nothing is
// remembered about it on either side. The proxy authorizes every request by the
// group access policy, so blocking a client refuses a still valid session.
// Clients are created by the platform when a team is formed or a member is
// added, never while issuing a link.
package labaccess

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/gofrs/uuid"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// DefaultSessionTTL is the default session length when the caller names no end (LAB_SESSION_TTL; an
	// event without a finish).
	DefaultSessionTTL = 24 * time.Hour
	// DefaultTokenTTL is how long an access token can be opened.
	DefaultTokenTTL = time.Minute
	// AuthPath is the proxy path that consumes a handoff link.
	AuthPath = "/_auth"
)

// Audience is the token's aud: the proxy accepts only tokens meant for it.
const Audience = "laboratory-proxy"

// GRPCAudience is the aud of the long-lived token of a run's grpc access. It differs from Audience on purpose: the 60 s
// handoff token is accepted at /_auth (it buys a browser cookie), a run's token must never be.
const GRPCAudience = "laboratory-proxy-grpc"

// The claims that mark a grpc token next to its audience.
const (
	TokenUseGRPC = "grpc"
	KindGRPC     = "grpc"
)

// Config sets how long a token can be opened.
type Config struct {
	// TokenTTL is how long a token can be opened (default one minute). The proxy of each agent states
	// its own limit (SigningKey.MaxTokenTTL), which caps it.
	TokenTTL time.Duration
	// SessionTTL is the session length when the caller names no end; zero means DefaultSessionTTL.
	SessionTTL time.Duration
}

// SigningKey is the key of one tenant that signs its access tokens: the tenant name is the issuer, KeyID
// the key id the proxy picks the verifying key by (the token's kid header).
type SigningKey struct {
	Tenant string
	KeyID  string
	Key    ed25519.PrivateKey
	// MaxTokenTTL and MaxSessionTTL are what the tenant's proxy accepts (its agent reports them): the
	// longest link and the longest session. Zero = not reported, no cap is applied here.
	MaxTokenTTL   time.Duration
	MaxSessionTTL time.Duration
}

// Session is who the link is for, in the operator's own terms: a LabGroup and
// one LabGroupClient of that group. The client is the same object that is the
// participant's VPN peer, so blocking it in the group access policy blocks the
// VPN and the web together. The platform knows which event, team or test deploy
// a group belongs to and which user a client is; the token does not say.
type Session struct {
	// Group is the lab group name (e-<event>-t-<team> or t-<deploy>), which is
	// also its namespace.
	Group string
	// Client is the LabGroupClient name inside the group (p-<user id>).
	Client string
	// AccessURL is the device URL from the lab status (https://<device>-<code>.<base>/...).
	// The link is built on its origin; its first host label is the token's host.
	AccessURL string
	// Next is where the proxy sends the browser after the handoff, the value of the `next` query key of the link (a path with
	// its query, for example the console page of a device: see ConsoleSession). Empty: the proxy's default.
	Next string
	// ExpiresAt is the wanted end of the session (the event's effective finish
	// plus a buffer, or a test deploy's lease end). Zero, or a time that is not in
	// the future, means the fallback TTL.
	ExpiresAt time.Time
}

// Link is a signed handoff link.
type Link struct {
	LabID    uuid.UUID
	Revision string
	// URL is the full link to open in the browser.
	URL string
	// Token is the signed JWT inside the link.
	Token string
	// ExpiresAt is the end of the session the proxy will grant.
	ExpiresAt time.Time
}

// claims: iss = the tenant, sub = the LabGroupClient, aud = laboratory-proxy, iat/nbf/exp and
// group_id (the lab group id), host (<device>-<code>) and sess (the unix end of the session).
type claims struct {
	GroupID string `json:"group_id"`
	Host    string `json:"host"`
	Session int64  `json:"sess"`
	jwt.RegisteredClaims
}

type Issuer struct {
	ttl        time.Duration
	sessionTTL time.Duration
}

// New returns an issuer; the signing key is given per token.
func New(cfg Config) (*Issuer, error) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.SessionTTL < 0 || cfg.TokenTTL < 0 {
		return nil, errors.New("labaccess: the session and the token ttl must be positive")
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = DefaultTokenTTL
	}
	return &Issuer{ttl: cfg.TokenTTL, sessionTTL: cfg.SessionTTL}, nil
}

// TokenTTL is how long a link can be opened.
func (i *Issuer) TokenTTL() time.Duration { return i.ttl }

// SessionTTL is the session length when the caller names no end.
func (i *Issuer) SessionTTL() time.Duration { return i.sessionTTL }

// Issue signs an access link for one device with the tenant's key, valid for the token TTL from now.
func (i *Issuer) Issue(sk SigningKey, s Session, now time.Time) (Link, error) {
	if sk.Tenant == "" || sk.KeyID == "" || len(sk.Key) != ed25519.PrivateKeySize {
		return Link{}, errors.New("labaccess: no signing key")
	}
	if s.Group == "" || s.Client == "" {
		return Link{}, errors.New("labaccess: incomplete session")
	}
	origin, err := url.Parse(s.AccessURL)
	if err != nil || origin.Scheme == "" || origin.Hostname() == "" {
		return Link{}, errors.New("labaccess: the device has no web address")
	}
	host := strings.SplitN(origin.Hostname(), ".", 2)[0]
	end := now.Add(i.sessionTTL)
	if s.ExpiresAt.After(now) {
		end = s.ExpiresAt
	}
	// The proxy cuts a session at its own limit: say so in the link instead of promising more.
	if sk.MaxSessionTTL > 0 && end.After(now.Add(sk.MaxSessionTTL)) {
		end = now.Add(sk.MaxSessionTTL)
	}
	ttl := i.ttl
	if sk.MaxTokenTTL > 0 {
		ttl = min(ttl, sk.MaxTokenTTL)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims{
		GroupID: s.Group, Host: host, Session: end.Unix(),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: sk.Tenant, Subject: s.Client, Audience: jwt.ClaimStrings{Audience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	})
	token.Header["kid"] = sk.KeyID
	signed, err := token.SignedString(sk.Key)
	if err != nil {
		return Link{}, fmt.Errorf("sign lab access token: %w", err)
	}
	query := url.Values{"t": {signed}}
	if s.Next != "" {
		query.Set("next", s.Next)
	}
	link := url.URL{Scheme: origin.Scheme, Host: origin.Host, Path: AuthPath, RawQuery: query.Encode()}
	return Link{URL: link.String(), Token: signed, ExpiresAt: end}, nil
}

// Bearer is the token an automated actor presents to the laboratory proxy, in the metadata header `x-lab-access`, to reach
// one device's grpc access. It is signed like a handoff link's (EdDSA by the tenant's access key) but has its own audience
// (laboratory-proxy-grpc) and token_use/kind grpc, so it cannot be used at /_auth; it names the run it is for and lives for
// the whole run instead of one minute.
type Bearer struct {
	// Group is the lab group of the device, Client the per-run client inside it (the token's subject).
	Group, Client string
	// AccessURL is the device's grpc address from the lab status; its first host label is the token's host.
	AccessURL string
	// Run is the run the token is for (claim `run`).
	Run string
	// ExpiresAt is the end of the token: the end of the run's window plus a grace.
	ExpiresAt time.Time
}

// BearerToken is a signed token with the address to dial and when it ends.
type BearerToken struct {
	// URL is the device's grpc address (the AccessURL as given), Token the signed JWT.
	URL       string
	Token     string
	ExpiresAt time.Time
}

type bearerClaims struct {
	TokenUse string `json:"token_use"`
	Kind     string `json:"kind"`
	GroupID  string `json:"group_id"`
	Host     string `json:"host"`
	Run      string `json:"run"`
	jwt.RegisteredClaims
}

// IssueBearer signs a run's bearer token. The end is capped by what the tenant's proxy keeps a session (when it reports
// one); a token that would end before it starts is refused.
func (i *Issuer) IssueBearer(sk SigningKey, b Bearer, now time.Time) (BearerToken, error) {
	if sk.Tenant == "" || sk.KeyID == "" || len(sk.Key) != ed25519.PrivateKeySize {
		return BearerToken{}, errors.New("labaccess: no signing key")
	}
	if b.Group == "" || b.Client == "" || b.Run == "" {
		return BearerToken{}, errors.New("labaccess: incomplete bearer")
	}
	origin, err := url.Parse(b.AccessURL)
	if err != nil || origin.Hostname() == "" {
		return BearerToken{}, errors.New("labaccess: the device has no grpc address")
	}
	end := b.ExpiresAt
	if sk.MaxSessionTTL > 0 && end.After(now.Add(sk.MaxSessionTTL)) {
		end = now.Add(sk.MaxSessionTTL)
	}
	if !end.After(now) {
		return BearerToken{}, errors.New("labaccess: the bearer would end before it starts")
	}
	host := strings.SplitN(origin.Hostname(), ".", 2)[0]
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, bearerClaims{
		TokenUse: TokenUseGRPC, Kind: KindGRPC, GroupID: b.Group, Host: host, Run: b.Run,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: sk.Tenant, Subject: b.Client, Audience: jwt.ClaimStrings{GRPCAudience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(end),
		},
	})
	token.Header["kid"] = sk.KeyID
	signed, err := token.SignedString(sk.Key)
	if err != nil {
		return BearerToken{}, fmt.Errorf("sign lab bearer token: %w", err)
	}
	return BearerToken{URL: b.AccessURL, Token: signed, ExpiresAt: end}, nil
}
