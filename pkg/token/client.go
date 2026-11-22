package token

import (
	"errors"
	"time"

	"github.com/cybericebox/backend/pkg/secret"
)

const issuer = "id"

var (
	ErrInvalidToken = errors.New("token: invalid or malformed token")
	ErrEmptySignKey = errors.New("token: signing key cannot be empty")
)

// Config holds the secret needed by the token client.
type Config struct {
	// Keys are the signing keys, current first (secret.SigningKeys): the first signs, a token with a kid is read by the key
	// of that id, one without is tried against every key.
	Keys []secret.SigningKey
	// TokenSignature is the single value of the older configuration: with no Keys it is the only key, and its tokens carry
	// no kid. Ignored when Keys is set.
	TokenSignature string
	Issuer         string
	// SetupTokenTTL is the life of a setup link (SETUP_TOKEN_TTL); zero means SetupTokenTTL.
	SetupTokenTTL time.Duration
}

// Client signs and verifies JWTs.
type Client struct {
	keys     []secret.SigningKey
	issuer   string
	setupTTL time.Duration
}

// New constructs a Client, returning an error on invalid config.
func New(cfg Config) (*Client, error) {
	keys := cfg.Keys
	if len(keys) == 0 && cfg.TokenSignature != "" {
		keys = []secret.SigningKey{{Key: []byte(cfg.TokenSignature)}}
	}
	for _, k := range keys {
		if len(k.Key) == 0 {
			return nil, ErrEmptySignKey
		}
	}
	if len(keys) == 0 {
		return nil, ErrEmptySignKey
	}
	if cfg.SetupTokenTTL < 0 {
		return nil, errors.New("token: the setup token ttl cannot be negative")
	}
	if cfg.SetupTokenTTL == 0 {
		cfg.SetupTokenTTL = SetupTokenTTL
	}
	return &Client{keys: keys, issuer: cfg.Issuer, setupTTL: cfg.SetupTokenTTL}, nil
}

// MustNew is New but panics on error — safe to call at startup.
func MustNew(cfg Config) *Client {
	c, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return c
}
