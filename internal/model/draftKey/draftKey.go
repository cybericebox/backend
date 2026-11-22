// Package draftKeyModel describes the keys the catalog editor seals its unsaved drafts with in the browser. One key per
// person and browser: the browser holds only the id of its key, in an httpOnly cookie, and asks the backend for the
// key itself each time it opens a draft. A key nobody used for TTL is deleted, and signing out deletes the key of that
// browser, so a draft left in the storage of a shared computer cannot be opened after either.
package draftKeyModel

import (
	"regexp"
	"time"
)

const (
	// CookieName is the cookie that names the key of this browser.
	CookieName = "cib_draft_key"
	// CookiePath limits the cookie to the API.
	CookiePath = "/api"
	// TTL is how long a key lives after its last use; the cookie lives as long and is refreshed with every use.
	TTL = 48 * time.Hour
	// KeySize is the length of a key in bytes (AES-256).
	KeySize = 32
	// IDSize is the length of the random part of a key id in bytes; the id is its unpadded base64url form.
	IDSize = 32
	// MaxPerUser bounds the keys one person keeps at a time (one per browser); the least recently used go first.
	MaxPerUser = 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidID tells whether s has the form of a key id (43 characters of base64url). Anything else is never looked up.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Key is a draft key and its id.
type Key struct {
	ID  string
	Key []byte
}
