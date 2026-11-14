package assistant

import (
	"crypto/hkdf"
	"crypto/sha256"
)

// hkdfKey is HKDF-SHA256 (RFC 5869) of the secret with the fixed info and no salt, 32 bytes.
func hkdfKey(secret []byte, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, nil, info, 32)
}
