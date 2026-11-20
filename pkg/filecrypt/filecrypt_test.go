package filecrypt

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

var testKey = bytes.Repeat([]byte{0x42}, KeySize)

// pattern gives n bytes of a fixed, position dependent content, so a misplaced frame or offset shows.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func encrypt(t *testing.T, key []byte, salt byte, plain []byte) []byte {
	t.Helper()
	r, err := newEncryptReader(key, [SaltSize]byte(bytes.Repeat([]byte{salt}, SaltSize)), bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decrypt(key []byte, enc []byte) ([]byte, error) {
	r, err := NewDecryptReader(key, bytes.NewReader(enc))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

var sizes = []int{0, 1, 15, FrameSize - 1, FrameSize, FrameSize + 1, 2 * FrameSize, 3*FrameSize + 7}

func TestRoundTripAndSizes(t *testing.T) {
	for _, n := range sizes {
		plain := pattern(n)
		enc := encrypt(t, testKey, 3, plain)
		if int64(len(enc)) != EncryptedSize(int64(n)) {
			t.Errorf("size %d: encrypted length %d, EncryptedSize %d", n, len(enc), EncryptedSize(int64(n)))
		}
		if got, err := PlainSize(int64(len(enc))); err != nil || got != int64(n) {
			t.Errorf("size %d: PlainSize = %d, %v", n, got, err)
		}
		got, err := decrypt(testKey, enc)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("size %d: round trip failed: %v", n, err)
		}
	}
}

func TestRoundTripWithSlowReaders(t *testing.T) {
	plain := pattern(2*FrameSize + 5)
	r, err := NewEncryptReader(testKey, iotest.OneByteReader(bytes.NewReader(plain)))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := io.ReadAll(iotest.OneByteReader(r))
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecryptReader(testKey, iotest.HalfReader(bytes.NewReader(enc)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(d)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("slow readers: %v", err)
	}
}

// A fixed vector freezes the format: the same key and plain bytes must always give the same object.
func TestFixedVector(t *testing.T) {
	enc := encrypt(t, testKey, 1, []byte("cybericebox"))
	if len(enc) != HeaderSize+11+TagSize {
		t.Fatalf("length %d", len(enc))
	}
	if hex.EncodeToString(enc[:HeaderSize]) != "4349424602000000"+strings.Repeat("01", SaltSize) {
		t.Fatalf("header %x", enc[:HeaderSize])
	}
	if got, err := decrypt(testKey, enc); err != nil || string(got) != "cybericebox" {
		t.Fatalf("decrypt: %q %v", got, err)
	}
	if hex.EncodeToString(enc) != fixedVector {
		t.Fatalf("the format changed: %x", enc)
	}
}

func TestSizesRejectImpossible(t *testing.T) {
	for _, enc := range []int64{0, HeaderSize, HeaderSize + TagSize - 1, HeaderSize + FrameSize + TagSize + 3} {
		if _, err := PlainSize(enc); err == nil {
			t.Errorf("PlainSize(%d) accepted", enc)
		}
	}
}

func TestTamperIsDetected(t *testing.T) {
	plain := pattern(2*FrameSize + 100)
	enc := encrypt(t, testKey, 0, plain)
	frame := FrameSize + TagSize

	cases := map[string][]byte{
		"truncated at a frame":  enc[:HeaderSize+frame],
		"truncated in the tail": enc[:len(enc)-1],
		"appended byte":         append(append([]byte{}, enc...), 0),
		"flipped bit":           flip(enc, HeaderSize+5),
		"swapped frames":        swap(enc, HeaderSize, HeaderSize+frame, frame),
		"only header":           enc[:HeaderSize],
		"flipped tag of last":   flip(enc, len(enc)-1),
		"first frame removed":   append(append([]byte{}, enc[:HeaderSize]...), enc[HeaderSize+frame:]...),
	}
	for name, bad := range cases {
		if _, err := decrypt(testKey, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func flip(b []byte, at int) []byte {
	c := append([]byte{}, b...)
	c[at] ^= 1
	return c
}

func swap(b []byte, a, c, n int) []byte {
	out := append([]byte{}, b...)
	copy(out[a:a+n], b[c:c+n])
	copy(out[c:c+n], b[a:a+n])
	return out
}

func TestWrongKeyAndFormat(t *testing.T) {
	enc := encrypt(t, testKey, 5, pattern(100))
	other := bytes.Repeat([]byte{0x43}, KeySize)
	if _, err := decrypt(other, enc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := decrypt(testKey, pattern(100)); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("plain data: %v", err)
	}
	if _, err := NewEncryptReader(testKey[:5], bytes.NewReader(nil)); !errors.Is(err, ErrBadKey) {
		t.Errorf("short key: %v", err)
	}
}

// Writing the same bytes twice with the same file key (a chunk sent again, two requests for the same chunk) must never share a
// key and nonce: each write draws its own salt, so the frame keys and the ciphertexts differ.
func TestEveryWriteHasItsOwnNonceSpace(t *testing.T) {
	plain := pattern(3000)
	write := func() []byte {
		r, err := NewEncryptReader(testKey, bytes.NewReader(plain))
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b := write(), write()
	if bytes.Equal(a[8:HeaderSize], b[8:HeaderSize]) {
		t.Fatal("two writes drew the same salt")
	}
	if bytes.Equal(a[HeaderSize:], b[HeaderSize:]) {
		t.Fatal("the same file key and plain bytes gave the same frame: the nonce space is shared")
	}
	for _, o := range [][]byte{a, b} {
		if got, err := decrypt(testKey, o); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip: %v", err)
		}
	}
	// And the same salt does give the same object: nothing but the salt varies.
	if !bytes.Equal(encrypt(t, testKey, 9, plain), encrypt(t, testKey, 9, plain)) {
		t.Fatal("the format is not deterministic for a given salt")
	}
}

// The frame key is HKDF-SHA256(file key, salt, info); the expected value was computed with an independent HKDF (Python hmac).
func TestFrameKeyDerivation(t *testing.T) {
	frameKey, err := hkdf.Key(sha256.New, testKey, bytes.Repeat([]byte{1}, SaltSize), frameKeyInfo, KeySize)
	if err != nil || hex.EncodeToString(frameKey) != "115fff4784b35032ee2aef5765ad793d578c78988c12b07a05eee4ac2f3f3324" {
		t.Fatalf("frame key %x %v", frameKey, err)
	}
}

// bytesFetch serves ranges of an in-memory object and records what was asked.
type bytesFetch struct {
	obj   []byte
	asked [][2]int64
}

func (b *bytesFetch) fetch(offset, length int64) (io.ReadCloser, error) {
	b.asked = append(b.asked, [2]int64{offset, length})
	return io.NopCloser(bytes.NewReader(b.obj[offset : offset+length])), nil
}

func TestOpenRange(t *testing.T) {
	plain := pattern(3*FrameSize + 7)
	size := int64(len(plain))
	cases := []struct {
		name           string
		offset, length int64
	}{
		{"start", 0, 10},
		{"inside one frame", 100, 200},
		{"frame boundary", FrameSize - 5, 10},
		{"exactly one frame", FrameSize, FrameSize},
		{"across three frames", FrameSize - 1, FrameSize + 2},
		{"last bytes", size - 3, 3},
		{"whole file", 0, size},
		{"zero length", 50, 0},
		{"inside the short last frame", 3*FrameSize + 2, 4},
	}
	for _, c := range cases {
		src := &bytesFetch{obj: encrypt(t, testKey, 9, plain)}
		rc, err := OpenRange(testKey, size, c.offset, c.length, src.fetch)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil || !bytes.Equal(got, plain[c.offset:c.offset+c.length]) {
			t.Errorf("%s: wrong bytes (%v)", c.name, err)
		}
	}
}

// A range read fetches only the frames that cover it, never the whole object.
func TestOpenRangeFetchesOnlyNeededFrames(t *testing.T) {
	plain := pattern(10 * FrameSize)
	src := &bytesFetch{obj: encrypt(t, testKey, 0, plain)}
	rc, err := OpenRange(testKey, int64(len(plain)), 3*FrameSize+1, 10, src.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	// One read of the header (the salt), one of the covering frame.
	if len(src.asked) != 2 || src.asked[0] != [2]int64{0, HeaderSize} || src.asked[1][0] != HeaderSize+3*(FrameSize+TagSize) || src.asked[1][1] != FrameSize+TagSize {
		t.Fatalf("asked %v", src.asked)
	}
}

func TestOpenRangeDetectsDamageAndBadRanges(t *testing.T) {
	plain := pattern(2*FrameSize + 3)
	enc := encrypt(t, testKey, 0, plain)
	size := int64(len(plain))
	bad := &bytesFetch{obj: flip(enc, HeaderSize+FrameSize+TagSize+2)}
	rc, err := OpenRange(testKey, size, FrameSize, 5, bad.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("damaged frame: %v", err)
	}
	// A frame that was the last of the file is not accepted as a middle frame of a longer one, nor the reverse.
	short := &bytesFetch{obj: enc}
	for _, r := range [][2]int64{{-1, 1}, {0, size + 1}, {size, 1}, {1, -1}} {
		if _, err = OpenRange(testKey, size, r[0], r[1], short.fetch); !errors.Is(err, ErrRange) {
			t.Errorf("range %v: %v", r, err)
		}
	}
	// A plain size that is too large for the object (cut short) fails on the missing frame.
	cut := &bytesFetch{obj: enc[:len(enc)-5]}
	rc, err = OpenRange(testKey, size, 2*FrameSize, 3, func(o, l int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(cut.obj[o:])), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); err == nil {
		t.Error("a cut object was accepted")
	}
}

// fixedVector freezes version 2 (key "42"*32, salt "01"*16, plain "cybericebox"); only the frame key derivation is independently checked.
const fixedVector = "434942460200000001010101010101010101010101010101ec854736779ad3e2c5186af79451fd165d62813332afb22245b226"
