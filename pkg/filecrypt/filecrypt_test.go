package filecrypt

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
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

func encrypt(t *testing.T, key []byte, stream uint32, plain []byte) []byte {
	t.Helper()
	r, err := NewEncryptReader(key, stream, bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decrypt(key []byte, stream uint32, enc []byte) ([]byte, error) {
	r, err := NewDecryptReader(key, stream, bytes.NewReader(enc))
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
		got, err := decrypt(testKey, 3, enc)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("size %d: round trip failed: %v", n, err)
		}
	}
}

func TestRoundTripWithSlowReaders(t *testing.T) {
	plain := pattern(2*FrameSize + 5)
	r, err := NewEncryptReader(testKey, 0, iotest.OneByteReader(bytes.NewReader(plain)))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := io.ReadAll(iotest.OneByteReader(r))
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecryptReader(testKey, 0, iotest.HalfReader(bytes.NewReader(enc)))
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
	if hex.EncodeToString(enc[:HeaderSize]) != "4349424601000000" {
		t.Fatalf("header %x", enc[:HeaderSize])
	}
	if got, err := decrypt(testKey, 1, enc); err != nil || string(got) != "cybericebox" {
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
		if _, err := decrypt(testKey, 0, bad); err == nil {
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

func TestWrongKeyStreamAndFormat(t *testing.T) {
	enc := encrypt(t, testKey, 5, pattern(100))
	other := bytes.Repeat([]byte{0x43}, KeySize)
	if _, err := decrypt(other, 5, enc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := decrypt(testKey, 6, enc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong stream: %v", err)
	}
	if _, err := decrypt(testKey, 5, pattern(100)); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("plain data: %v", err)
	}
	if _, err := NewEncryptReader(testKey[:5], 0, bytes.NewReader(nil)); !errors.Is(err, ErrBadKey) {
		t.Errorf("short key: %v", err)
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
		rc, err := OpenRange(testKey, 9, size, c.offset, c.length, src.fetch)
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
	rc, err := OpenRange(testKey, 0, int64(len(plain)), 3*FrameSize+1, 10, src.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if len(src.asked) != 1 || src.asked[0][0] != HeaderSize+3*(FrameSize+TagSize) || src.asked[0][1] != FrameSize+TagSize {
		t.Fatalf("asked %v", src.asked)
	}
}

func TestOpenRangeDetectsDamageAndBadRanges(t *testing.T) {
	plain := pattern(2*FrameSize + 3)
	enc := encrypt(t, testKey, 0, plain)
	size := int64(len(plain))
	bad := &bytesFetch{obj: flip(enc, HeaderSize+FrameSize+TagSize+2)}
	rc, err := OpenRange(testKey, 0, size, FrameSize, 5, bad.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("damaged frame: %v", err)
	}
	// A frame that was the last of the file is not accepted as a middle frame of a longer one, nor the reverse.
	short := &bytesFetch{obj: enc}
	for _, r := range [][2]int64{{-1, 1}, {0, size + 1}, {size, 1}, {1, -1}} {
		if _, err = OpenRange(testKey, 0, size, r[0], r[1], short.fetch); !errors.Is(err, ErrRange) {
			t.Errorf("range %v: %v", r, err)
		}
	}
	// A plain size that is too large for the object (cut short) fails on the missing frame.
	cut := &bytesFetch{obj: enc[:len(enc)-5]}
	rc, err = OpenRange(testKey, 0, size, 2*FrameSize, 3, func(o, l int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(cut.obj[o:])), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(rc); err == nil {
		t.Error("a cut object was accepted")
	}
}

const fixedVector = "4349424601000000e83f59061b2b9655ab9ce9cfc48913227bce44d04c5a48b536f03f"
