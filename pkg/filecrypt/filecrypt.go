// Package filecrypt encrypts a byte stream in independent authenticated frames, so a stored file is never held whole in
// memory, a stream is encrypted and decrypted as it passes, and a range of the plain bytes is read by fetching only the
// frames that cover it.
//
// Format of an encrypted object:
//
//	header   8 bytes: "CIBF" + version 1 + 3 zero bytes
//	frame 0  ciphertext of plain bytes [0, FrameSize) + 16 byte AES-256-GCM tag
//	frame 1  ...
//	frame n  the last frame holds what is left (0 to FrameSize plain bytes); an empty file is one empty frame
//
// Every frame is sealed with the one key of the file and the nonce stream(4 bytes, big endian) | frame index (8 bytes,
// big endian). A key is therefore used for one stream only: a caller that keeps several objects under one key (the chunks
// of an upload) gives each its own stream number. The additional data of a frame is one byte, 1 for the last frame and 0
// for any other, so cutting an object short, appending to it or reordering its frames fails to open. The plain size is
// not stored: it follows from the object size (PlainSize), and the caller keeps it in its own record.
package filecrypt

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

const (
	// FrameSize is the number of plain bytes in a full frame.
	FrameSize = 64 << 10
	// TagSize is the authentication tag every frame adds.
	TagSize = 16
	// HeaderSize is the length of the object header.
	HeaderSize = 8
	// KeySize is the length of a file key.
	KeySize = 32
)

var header = [HeaderSize]byte{'C', 'I', 'B', 'F', 1, 0, 0, 0}

var (
	// ErrBadKey: the key is not KeySize bytes.
	ErrBadKey = errors.New("filecrypt: the key must be 32 bytes")
	// ErrNotEncrypted: the object does not start with the header of this format.
	ErrNotEncrypted = errors.New("filecrypt: not an encrypted object")
	// ErrCorrupt: a frame does not open (damaged, cut short, extended, reordered or opened with another key).
	ErrCorrupt = errors.New("filecrypt: the object is damaged or the key is wrong")
	// ErrRange: the requested range lies outside the file.
	ErrRange = errors.New("filecrypt: range outside the file")
)

// NewKey returns a random file key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, err
	}
	return k, nil
}

func frameCount(plain int64) int64 {
	if plain <= 0 {
		return 1
	}
	return (plain + FrameSize - 1) / FrameSize
}

// EncryptedSize is the size of the encrypted object of a file of plain bytes.
func EncryptedSize(plain int64) int64 {
	return HeaderSize + plain + frameCount(plain)*TagSize
}

// PlainSize is the size of the file inside an encrypted object of enc bytes.
func PlainSize(enc int64) (int64, error) {
	body := enc - HeaderSize
	if body < TagSize {
		return 0, ErrCorrupt
	}
	full := body / (FrameSize + TagSize)
	rest := body % (FrameSize + TagSize)
	switch {
	case rest == 0 && full > 0:
		return full * FrameSize, nil
	case rest >= TagSize:
		return full*FrameSize + rest - TagSize, nil
	}
	return 0, ErrCorrupt
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(stream uint32, index uint64) []byte {
	var n [12]byte
	binary.BigEndian.PutUint32(n[:4], stream)
	binary.BigEndian.PutUint64(n[4:], index)
	return n[:]
}

func flag(final bool) []byte {
	if final {
		return []byte{1}
	}
	return []byte{0}
}

// frames cuts a stream into frames of a given size and tells which one is the last.
type frames struct {
	br    *bufio.Reader
	size  int
	index uint64
	done  bool
	// known says the index of the last frame is already known (a range read, which stops before the end of the file),
	// so the end of the stream does not tell which frame is the last.
	known bool
	last  uint64
}

// next reads the next frame into buf (len(buf) == size). It returns the bytes read and whether it is the last frame.
func (f *frames) next(buf []byte) (n int, final bool, err error) {
	if f.done {
		return 0, true, io.EOF
	}
	n, err = io.ReadFull(f.br, buf)
	switch {
	case err == nil && f.known:
		final = f.index == f.last
	case err == nil:
		if _, perr := f.br.Peek(1); perr != nil {
			if errors.Is(perr, io.EOF) {
				final = true
			} else {
				return 0, false, perr
			}
		}
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		final = true
		if f.known && f.index != f.last {
			return 0, false, ErrCorrupt
		}
	default:
		return 0, false, err
	}
	f.done = final
	f.index++
	return n, final, nil
}

type encryptReader struct {
	aead   cipher.AEAD
	stream uint32
	fr     frames
	plain  []byte
	out    []byte
	hdr    bool
	err    error
}

// NewEncryptReader returns the encrypted object of src: the header, then the frames, produced as src is read.
func NewEncryptReader(key []byte, stream uint32, src io.Reader) (io.Reader, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &encryptReader{
		aead: aead, stream: stream, plain: make([]byte, FrameSize),
		fr: frames{br: bufio.NewReaderSize(src, 4096), size: FrameSize},
	}, nil
}

func (e *encryptReader) Read(p []byte) (int, error) {
	for len(e.out) == 0 {
		if e.err != nil {
			return 0, e.err
		}
		if !e.hdr {
			e.hdr = true
			e.out = append(e.out[:0], header[:]...)
			continue
		}
		idx := e.fr.index
		n, final, err := e.fr.next(e.plain)
		if err != nil {
			e.err = err
			continue
		}
		e.out = e.aead.Seal(e.out[:0], nonce(e.stream, idx), e.plain[:n], flag(final))
		if final {
			e.err = io.EOF
		}
	}
	n := copy(p, e.out)
	e.out = e.out[n:]
	return n, nil
}

type decryptReader struct {
	aead   cipher.AEAD
	stream uint32
	fr     frames
	enc    []byte
	out    []byte
	hdr    bool
	err    error
	// skip is how many plain bytes of the first frame are dropped; limit is how many are delivered (negative: all).
	skip  int
	limit int64
	close io.Closer
}

// NewDecryptReader returns the plain bytes of an encrypted object read from src, checking every frame as it passes. A
// damaged or cut object ends with ErrCorrupt after the frames before it were delivered; the caller must treat the
// stream as failed whenever the error is not io.EOF.
func NewDecryptReader(key []byte, stream uint32, src io.Reader) (io.ReadCloser, error) {
	d, err := newDecryptReader(key, stream, src, true)
	if err != nil {
		return nil, err
	}
	if c, ok := src.(io.Closer); ok {
		d.close = c
	}
	return d, nil
}

func newDecryptReader(key []byte, stream uint32, src io.Reader, withHeader bool) (*decryptReader, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &decryptReader{
		aead: aead, stream: stream, enc: make([]byte, FrameSize+TagSize), hdr: !withHeader, limit: -1,
		fr: frames{br: bufio.NewReaderSize(src, 4096), size: FrameSize + TagSize},
	}, nil
}

func (d *decryptReader) Close() error {
	if d.close != nil {
		return d.close.Close()
	}
	return nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.out) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.limit == 0 {
			d.err = io.EOF
			continue
		}
		if !d.hdr {
			var h [HeaderSize]byte
			if _, err := io.ReadFull(d.fr.br, h[:]); err != nil || h != header {
				d.err = ErrNotEncrypted
				continue
			}
			d.hdr = true
			continue
		}
		idx := d.fr.index
		n, final, err := d.fr.next(d.enc)
		if err != nil {
			d.err = err
			continue
		}
		if n < TagSize {
			d.err = ErrCorrupt
			continue
		}
		plain, err := d.aead.Open(d.enc[:0], nonce(d.stream, idx), d.enc[:n], flag(final))
		if err != nil {
			d.err = ErrCorrupt
			continue
		}
		if d.skip > 0 {
			plain = plain[min(d.skip, len(plain)):]
			d.skip = 0
		}
		if d.limit >= 0 && int64(len(plain)) > d.limit {
			plain = plain[:d.limit]
		}
		if d.limit >= 0 {
			d.limit -= int64(len(plain))
		}
		d.out = plain
		if final {
			d.err = io.EOF
		}
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}

// Fetch opens length bytes of the encrypted object starting at offset (a range read of the store).
type Fetch func(offset, length int64) (io.ReadCloser, error)

// OpenRange returns length plain bytes of a file from offset, of a file of plainSize bytes stored encrypted. It asks
// fetch for the frames that cover the range only and checks each of them.
func OpenRange(key []byte, stream uint32, plainSize, offset, length int64, fetch Fetch) (io.ReadCloser, error) {
	if offset < 0 || length < 0 || offset+length > plainSize {
		return nil, ErrRange
	}
	if length == 0 {
		return io.NopCloser(eofReader{}), nil
	}
	total := frameCount(plainSize)
	first := offset / FrameSize
	last := (offset + length - 1) / FrameSize
	start := HeaderSize + first*(FrameSize+TagSize)
	end := min(EncryptedSize(plainSize), HeaderSize+(last+1)*(FrameSize+TagSize))
	rc, err := fetch(start, end-start)
	if err != nil {
		return nil, err
	}
	d, err := newDecryptReader(key, stream, rc, false)
	if err != nil {
		_ = rc.Close()
		return nil, err
	}
	d.fr.index = uint64(first)
	// The last frame of the file is the one that carries the final flag, wherever the range stops.
	d.fr.known, d.fr.last = true, uint64(total-1)
	d.skip = int(offset - first*FrameSize)
	d.limit = length
	d.close = rc
	return d, nil
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }
