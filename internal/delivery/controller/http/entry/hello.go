// Package entry is the optional transparent proxy by TLS server name on the public HTTPS port: it peeks the
// ClientHello, serves the backend's own API host itself and passes the TLS stream of the platform's frontend
// hosts, untouched, to fixed upstreams. It never terminates TLS for a frontend and never chooses an upstream from
// anything the client sends: the target comes from a table built at start from DOMAIN and the configuration.
package entry

import (
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/cryptobyte"
)

// The peek is bounded by the TLS record limit: a plaintext record is at most 2^14 bytes, and the whole
// ClientHello must sit in the first record (browsers send it in one; a hello split over records is refused).
const (
	recordHeaderLen  = 5
	maxRecordLen     = 1 << 14
	recordTypeHandsh = 0x16
	handshakeClient  = 0x01
	extServerName    = 0
	nameTypeHost     = 0
	maxHostNameLen   = 253
)

// Why a connection was refused before any byte reached a service.
var (
	ErrNotTLS         = errors.New("entry: not a TLS handshake record")
	ErrTooLarge       = errors.New("entry: handshake record larger than the TLS limit")
	ErrNotClientHello = errors.New("entry: first handshake message is not a ClientHello")
	ErrFragmented     = errors.New("entry: ClientHello does not fit the first record")
	ErrMalformed      = errors.New("entry: malformed ClientHello")
	ErrNoServerName   = errors.New("entry: no server name")
	ErrBadServerName  = errors.New("entry: invalid server name")
)

// ReadClientHello reads exactly one TLS record from r and returns the server name of the ClientHello in it, in
// lower case, plus every byte read (to replay to whoever serves the connection). It reads at most
// recordHeaderLen+maxRecordLen bytes and allocates the body only after the record header is valid. It does not
// bound time: the caller sets a deadline on the connection.
func ReadClientHello(r io.Reader) (serverName string, raw []byte, err error) {
	var header [recordHeaderLen]byte
	n, err := io.ReadFull(r, header[:])
	if err != nil {
		return "", header[:n], err
	}
	if header[0] != recordTypeHandsh || header[1] != 3 || header[2] > 4 {
		return "", header[:], ErrNotTLS
	}
	length := int(header[3])<<8 | int(header[4])
	if length > maxRecordLen {
		return "", header[:], ErrTooLarge
	}
	if length < 4 {
		return "", header[:], ErrMalformed
	}
	raw = make([]byte, recordHeaderLen+length)
	copy(raw, header[:])
	n, err = io.ReadFull(r, raw[recordHeaderLen:])
	if err != nil {
		return "", raw[:recordHeaderLen+n], err
	}
	serverName, err = parseServerName(raw[recordHeaderLen:])
	return serverName, raw, err
}

// parseServerName reads the handshake message in body: a ClientHello with exactly one server_name extension that
// carries exactly one host_name. A duplicate extension is refused (Go's own TLS server refuses it too, so the two
// parsers cannot disagree on which name counts).
func parseServerName(body []byte) (string, error) {
	msg := cryptobyte.String(body)
	var msgType uint8
	var hello cryptobyte.String
	if !msg.ReadUint8(&msgType) {
		return "", ErrMalformed
	}
	if msgType != handshakeClient {
		return "", ErrNotClientHello
	}
	var declared uint32
	if !msg.ReadUint24(&declared) {
		return "", ErrMalformed
	}
	if int(declared) > len(msg) {
		return "", ErrFragmented
	}
	hello = msg[:declared]
	var legacyVersion uint16
	var random, sessionID, suites, compression cryptobyte.String
	if !hello.ReadUint16(&legacyVersion) || !hello.ReadBytes((*[]byte)(&random), 32) ||
		!hello.ReadUint8LengthPrefixed(&sessionID) || !hello.ReadUint16LengthPrefixed(&suites) ||
		!hello.ReadUint8LengthPrefixed(&compression) {
		return "", ErrMalformed
	}
	if hello.Empty() {
		return "", ErrNoServerName // a hello without extensions
	}
	var extensions cryptobyte.String
	if !hello.ReadUint16LengthPrefixed(&extensions) || !hello.Empty() {
		return "", ErrMalformed
	}
	var name string
	found := false
	for !extensions.Empty() {
		var extType uint16
		var data cryptobyte.String
		if !extensions.ReadUint16(&extType) || !extensions.ReadUint16LengthPrefixed(&data) {
			return "", ErrMalformed
		}
		if extType != extServerName {
			continue
		}
		if found {
			return "", ErrMalformed
		}
		found = true
		var list cryptobyte.String
		if !data.ReadUint16LengthPrefixed(&list) || !data.Empty() || list.Empty() {
			return "", ErrMalformed
		}
		var nameType uint8
		var host cryptobyte.String
		if !list.ReadUint8(&nameType) || nameType != nameTypeHost || !list.ReadUint16LengthPrefixed(&host) || !list.Empty() {
			return "", ErrMalformed
		}
		name = string(host)
	}
	if !found {
		return "", ErrNoServerName
	}
	return normalizeServerName(name)
}

// normalizeServerName lower-cases a DNS host name and refuses everything that is not one: empty, too long, a
// trailing dot, an address literal, or any byte outside letters, digits, '-' and '.', with no empty or
// over-long label.
func normalizeServerName(name string) (string, error) {
	if name == "" {
		return "", ErrNoServerName
	}
	if len(name) > maxHostNameLen {
		return "", ErrBadServerName
	}
	name = strings.ToLower(name)
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrBadServerName
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", ErrBadServerName
			}
		}
	}
	return name, nil
}
