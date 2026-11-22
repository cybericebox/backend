package testhelpers

import "github.com/cybericebox/backend/pkg/secret"

// filesKeyHex is a fixed key of the files purpose for tests (not a secret; it protects nothing).
const filesKeyHex = "f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f00"

// FilesSealer is the keyring of the files purpose that tests give the media use case, so uploads are encrypted as in production.
func FilesSealer() *secret.Cipher {
	c, err := secret.New("t1:" + filesKeyHex)
	if err != nil {
		panic(err)
	}
	return c
}
