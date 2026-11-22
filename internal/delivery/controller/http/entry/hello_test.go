package entry

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// helloBuilder assembles a ClientHello record from fixed bytes (random and session id are zeros).
type helloOpts struct {
	extensions  []byte // raw extension block content; nil builds one server_name with names
	names       []string
	noExtBlock  bool
	msgType     byte
	recordType  byte
	declaredAdd int // added to the handshake length field
}

func u16(n int) []byte { return []byte{byte(n >> 8), byte(n)} }

func sniExt(names ...string) []byte {
	var list []byte
	for _, n := range names {
		list = append(list, 0)
		list = append(list, u16(len(n))...)
		list = append(list, n...)
	}
	data := append(u16(len(list)), list...)
	ext := append(u16(0), u16(len(data))...)
	return append(ext, data...)
}

func otherExt() []byte { return append(u16(0x0017), u16(0)...) } // extended_master_secret

func buildHello(o helloOpts) []byte {
	if o.msgType == 0 {
		o.msgType = 1
	}
	if o.recordType == 0 {
		o.recordType = 0x16
	}
	body := []byte{3, 3}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0)          // session id
	body = append(body, u16(2)...)  // cipher suites
	body = append(body, 0x13, 0x01) // TLS_AES_128_GCM_SHA256
	body = append(body, 1, 0)       // compression
	if !o.noExtBlock {
		ext := o.extensions
		if ext == nil {
			ext = append(otherExt(), sniExt(o.names...)...)
		}
		body = append(body, u16(len(ext))...)
		body = append(body, ext...)
	}
	n := len(body) + o.declaredAdd
	msg := append([]byte{o.msgType, byte(n >> 16), byte(n >> 8), byte(n)}, body...)
	rec := []byte{o.recordType, 3, 1}
	rec = append(rec, u16(len(msg))...)
	return append(rec, msg...)
}

func TestReadClientHello(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
		err  error
	}{
		{"plain name", buildHello(helloOpts{names: []string{"api.example.test"}}), "api.example.test", nil},
		{"upper case is lowered", buildHello(helloOpts{names: []string{"API.Example.TEST"}}), "api.example.test", nil},
		{"no server_name extension", buildHello(helloOpts{extensions: otherExt()}), "", ErrNoServerName},
		{"no extension block", buildHello(helloOpts{noExtBlock: true}), "", ErrNoServerName},
		{"empty name", buildHello(helloOpts{names: []string{""}}), "", ErrNoServerName},
		{"two names in the list", buildHello(helloOpts{names: []string{"a.example.test", "b.example.test"}}), "", ErrMalformed},
		{"duplicate extension", buildHello(helloOpts{extensions: append(sniExt("a.example.test"), sniExt("b.example.test")...)}), "", ErrMalformed},
		{"trailing dot", buildHello(helloOpts{names: []string{"api.example.test."}}), "", ErrBadServerName},
		{"address literal", buildHello(helloOpts{names: []string{"203.0.113.9"}}), "", nil},
		{"underscore", buildHello(helloOpts{names: []string{"a_b.example.test"}}), "", ErrBadServerName},
		{"nul byte", buildHello(helloOpts{names: []string{"a\x00.example.test"}}), "", ErrBadServerName},
		{"empty label", buildHello(helloOpts{names: []string{"a..example.test"}}), "", ErrBadServerName},
		{"too long", buildHello(helloOpts{names: []string{strings.Repeat("a.", 130) + "test"}}), "", ErrBadServerName},
		{"label over 63", buildHello(helloOpts{names: []string{strings.Repeat("a", 64) + ".test"}}), "", ErrBadServerName},
		{"not a hello", buildHello(helloOpts{msgType: 2, names: []string{"api.example.test"}}), "", ErrNotClientHello},
		{"not a handshake record", buildHello(helloOpts{recordType: 0x17, names: []string{"api.example.test"}}), "", ErrNotTLS},
		{"plain HTTP", []byte("GET / HTTP/1.1\r\nHost: api.example.test\r\n\r\n"), "", ErrNotTLS},
		{"hello longer than its record", buildHello(helloOpts{declaredAdd: 10, names: []string{"api.example.test"}}), "", ErrFragmented},
		{"record over the TLS limit", []byte{0x16, 3, 1, 0x40, 0x01}, "", ErrTooLarge},
		{"zero length record", []byte{0x16, 3, 1, 0, 0}, "", ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, raw, err := ReadClientHello(bytes.NewReader(c.in))
			if c.name == "address literal" {
				// digits and dots are valid label bytes: the policy refuses it, as it matches no host.
				if err != nil || got != "203.0.113.9" {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if !errors.Is(err, c.err) && !(c.err == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if got != c.want {
				t.Fatalf("name = %q, want %q", got, c.want)
			}
			if err == nil && !bytes.Equal(raw, c.in) {
				t.Fatal("the bytes read are not returned whole for replay")
			}
		})
	}
}

func TestReadClientHelloTruncatedInputEnds(t *testing.T) {
	full := buildHello(helloOpts{names: []string{"api.example.test"}})
	for _, cut := range []int{0, 3, 5, 20, len(full) - 1} {
		_, _, err := ReadClientHello(bytes.NewReader(full[:cut]))
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("cut %d: %v", cut, err)
		}
	}
}

// countingReader serves an endless stream and counts what was taken.
type countingReader struct {
	head []byte
	n    int
}

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		if r.n < len(r.head) {
			p[i] = r.head[r.n]
		} else {
			p[i] = 0
		}
		r.n++
	}
	return len(p), nil
}

func TestReadClientHelloNeverReadsMoreThanOneRecord(t *testing.T) {
	// A record that claims the maximum and an endless stream behind it: at most header + 16 KiB are taken.
	r := &countingReader{head: []byte{0x16, 3, 1, 0x40, 0x00, 1, 0xff, 0xff, 0xff}}
	_, _, err := ReadClientHello(r)
	if err == nil {
		t.Fatal("garbage accepted")
	}
	if r.n > recordHeaderLen+maxRecordLen {
		t.Fatalf("read %d bytes", r.n)
	}
	// Over the limit: only the header is read.
	r = &countingReader{head: []byte{0x16, 3, 1, 0x40, 0x01}}
	if _, _, err = ReadClientHello(r); !errors.Is(err, ErrTooLarge) || r.n != recordHeaderLen {
		t.Fatalf("err=%v read=%d", err, r.n)
	}
	// Not TLS: only the header is read.
	r = &countingReader{head: []byte("GET /")}
	if _, _, err = ReadClientHello(r); !errors.Is(err, ErrNotTLS) || r.n != recordHeaderLen {
		t.Fatalf("err=%v read=%d", err, r.n)
	}
}
