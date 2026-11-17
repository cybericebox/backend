package secret

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// signingKeyLen is the length of a derived signing key: an HMAC-SHA256 key as long as its hash.
const signingKeyLen = 32

// ExplicitSigningKeyID names the key an installation set by hand (the old JWT_TOKEN_SIGNATURE and OAUTH_STATE_SIGNATURE)
// when it stays next to a master key. It only verifies: the tokens already issued with it keep working until they expire.
const ExplicitSigningKeyID = "explicit"

// SigningKey is one HMAC key of a stateless signing purpose (a token that is signed, not stored). ID is what the token
// names in its kid header; an empty ID is a key whose tokens carry no kid (the single value of the older configuration).
type SigningKey struct {
	ID  string
	Key []byte
}

// SigningKeys returns the keys of a signing purpose, current first.
//
//   - With a master key (masterSpec, the form New accepts) each master version gives the key HKDF-SHA256(master, no salt,
//     KDFLabel(purpose)) with the id "d-<master id>", the first version being current, like the keys of Resolve. Nothing
//     derived is stored: it is computed again at every start.
//   - explicit is the value an installation set before the purpose moved under the master key. Next to a master key it is
//     appended as a verify-only key (ExplicitSigningKeyID), so the tokens issued with it stay valid until they expire.
//   - Without a master key the explicit value is the only key, with no id: tokens are signed and read exactly as before.
//   - Nothing set gives no keys and no error: the caller says what that means.
func SigningKeys(masterSpec, explicit string, purpose Purpose) ([]SigningKey, error) {
	if strings.TrimSpace(masterSpec) == "" {
		if explicit == "" {
			return nil, nil
		}
		return []SigningKey{{Key: []byte(explicit)}}, nil
	}
	master, err := New(masterSpec)
	if err != nil {
		return nil, fmt.Errorf("secret: master key: %w", err)
	}
	defer master.Destroy()
	keys := make([]SigningKey, 0, len(master.keys)+1)
	for _, k := range master.keys {
		if len(k.id) > maxMasterIDLen {
			return nil, fmt.Errorf("secret: master key id %q is longer than %d characters", k.id, maxMasterIDLen)
		}
		dk, derr := hkdf.Key(sha256.New, k.key[:], nil, KDFLabel(purpose), signingKeyLen)
		if derr != nil {
			return nil, derr
		}
		keys = append(keys, SigningKey{ID: derivedPrefix + k.id, Key: dk})
	}
	if explicit != "" {
		keys = append(keys, SigningKey{ID: ExplicitSigningKeyID, Key: []byte(explicit)})
	}
	return keys, nil
}

// ErrNoSigningKey is returned when a token would be signed or read without a key.
var ErrNoSigningKey = errors.New("secret: no signing key")

// SignHS256 signs claims with key (HS256). A key with an id puts it in the kid header.
func SignHS256(claims jwt.Claims, key SigningKey) (string, error) {
	if len(key.Key) == 0 {
		return "", ErrNoSigningKey
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	if key.ID != "" {
		tok.Header["kid"] = key.ID
	}
	return tok.SignedString(key.Key)
}

// ParseHS256 verifies a token and fills claims. A token with a kid is read with the key of that id only (an id that is not
// loaded fails); a token without one is tried against every key, current first. Only HS256 is accepted. The error says
// nothing about which key was tried.
func ParseHS256(tokenStr string, claims jwt.Claims, keys []SigningKey, opts ...jwt.ParserOption) (*jwt.Token, error) {
	if len(keys) == 0 {
		return nil, ErrNoSigningKey
	}
	unverified, _, err := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"})).ParseUnverified(tokenStr, &jwt.RegisteredClaims{})
	if err != nil {
		return nil, err
	}
	candidates := keys
	if kid, has := unverified.Header["kid"]; has {
		id, ok := kid.(string)
		if !ok || id == "" {
			return nil, errors.New("secret: malformed kid")
		}
		candidates = nil
		for _, k := range keys {
			if k.ID == id {
				candidates = append(candidates, k)
			}
		}
		if len(candidates) == 0 {
			return nil, errors.New("secret: the key of this token is not loaded")
		}
	}
	options := append([]jwt.ParserOption{jwt.WithValidMethods([]string{"HS256"})}, opts...)
	var lastErr error
	for _, k := range candidates {
		tok, err := jwt.ParseWithClaims(tokenStr, claims, func(*jwt.Token) (any, error) { return k.Key, nil }, options...)
		if err == nil {
			return tok, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
