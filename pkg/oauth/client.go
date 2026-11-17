package oauth

import (
	"errors"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	"github.com/cybericebox/daemon/pkg/secret"
)

const (
	stateSubject = "oauth-state"
	stateIssuer  = "oauth"
)

var (
	ErrEmptyStateSignature = errors.New("oauth: state signature cannot be empty")
	ErrInvalidState        = errors.New("oauth: invalid or expired oauth state")
)

// Config holds OAuth provider settings.
type Config struct {
	Google              ClientConfig
	RedirectURLTemplate string // e.g. "https://api.example.com/api/auth/%s/callback"
	// StateKeys are the signing keys of the state, current first (secret.SigningKeys): the first signs, a state with a
	// kid is read by the key of that id, one without is tried against every key.
	StateKeys []secret.SigningKey
	// StateSignature is the single value of the older configuration: with no StateKeys it is the only key and its states
	// carry no kid. Ignored when StateKeys is set.
	StateSignature string
	StateTTL       time.Duration
}

// ClientConfig holds Google OAuth2 app credentials.
type ClientConfig struct {
	ClientID     string
	ClientSecret string
}

// Client manages Google OAuth2 flow.
type Client struct {
	googleCfg *oauth2.Config
	keys      []secret.SigningKey
	stateTTL  time.Duration

	// usedStates remembers the ids of the states that already came back, until
	// they would have expired anyway: a state is single-use, so a captured
	// callback URL cannot be replayed within its lifetime. In memory: with
	// several replicas it holds per replica (the code Google issues is
	// single-use too).
	usedMu     sync.Mutex
	usedStates map[string]time.Time
}

// New constructs a Client, returning an error on invalid config.
func New(cfg Config) (*Client, error) {
	keys := cfg.StateKeys
	if len(keys) == 0 && cfg.StateSignature != "" {
		keys = []secret.SigningKey{{Key: []byte(cfg.StateSignature)}}
	}
	if len(keys) == 0 {
		return nil, ErrEmptyStateSignature
	}
	ttl := cfg.StateTTL
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	return &Client{
		googleCfg:  newGoogleClientConfig(cfg.Google, cfg.RedirectURLTemplate),
		keys:       keys,
		stateTTL:   ttl,
		usedStates: map[string]time.Time{},
	}, nil
}

// ValidateState checks that a state token is signed by this client and not expired.
func (c *Client) ValidateState(state string) error {
	_, err := c.parseStateToken(state)
	return err
}

// ── State JWT helpers ─────────────────────────────────────────────────────────

type stateClaims struct {
	jwt.RegisteredClaims
	// Redirect is the post-auth landing URL, embedded here (rather than carried
	// in a cookie) because it must survive an external round-trip to Google
	// that we do not control — the state token is the only thing guaranteed to
	// come back on the callback.
	Redirect string `json:"redirect,omitempty"`
}

func (c *Client) newStateToken(redirect string) (string, error) {
	now := time.Now()
	claims := stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    stateIssuer,
			Subject:   stateSubject,
			ExpiresAt: jwt.NewNumericDate(now.Add(c.stateTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.Must(uuid.NewV7()).String(),
		},
		Redirect: redirect,
	}
	return secret.SignHS256(claims, c.keys[0])
}

func (c *Client) parseStateToken(tokenStr string) (*stateClaims, error) {
	tok, err := secret.ParseHS256(
		tokenStr,
		&stateClaims{},
		c.keys,
		jwt.WithIssuer(stateIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, ErrInvalidState
	}
	claims, ok := tok.Claims.(*stateClaims)
	if !ok || !tok.Valid || claims.Subject != stateSubject {
		return nil, ErrInvalidState
	}
	return claims, nil
}

// consumeState marks the state as used; false when it was already used.
func (c *Client) consumeState(claims *stateClaims) bool {
	now := time.Now()
	c.usedMu.Lock()
	defer c.usedMu.Unlock()
	for id, expires := range c.usedStates {
		if now.After(expires) {
			delete(c.usedStates, id)
		}
	}
	if _, used := c.usedStates[claims.ID]; used {
		return false
	}
	c.usedStates[claims.ID] = claims.ExpiresAt.Time
	return true
}
