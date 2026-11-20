package media

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cybericebox/daemon/pkg/filecrypt"
)

// ErrFilesKeyMissing: an object is stored encrypted but the daemon has no key of the files purpose.
var ErrFilesKeyMissing = errors.New("media: the files key is not configured")

// object is one stored object and how to open it: where it lies, and (when it is encrypted) the key that was wrapped for it,
// the context the wrapping is bound to, and the stream number of its frames. size is the plain size, needed to read a
// range of an encrypted object.
type object struct {
	key     string
	wrapped string // empty: the object is plain
	ctx     []byte
	stream  uint32
	size    int64
}

// wrap seals a file key with the files purpose key, bound to ctx.
func (u *MediaUseCase) wrap(fileKey, ctx []byte) (string, error) {
	if !u.sealerConfigured {
		return "", ErrFilesKeyMissing
	}
	return u.sealer.EncryptWithContext(fileKey, ctx)
}

// unwrap opens a wrapped file key.
func (u *MediaUseCase) unwrap(wrapped string, ctx []byte) ([]byte, error) {
	if !u.sealerConfigured {
		return nil, ErrFilesKeyMissing
	}
	return u.sealer.DecryptWithContext(wrapped, ctx)
}

// encryptedPut stores r (plainSize bytes, or -1 when unknown) under key, encrypted with fileKey as the given stream.
func (u *MediaUseCase) encryptedPut(ctx context.Context, key string, fileKey []byte, stream uint32, r io.Reader, plainSize int64, contentType string) error {
	enc, err := filecrypt.NewEncryptReader(fileKey, stream, r)
	if err != nil {
		return err
	}
	size := int64(-1)
	if plainSize >= 0 {
		size = filecrypt.EncryptedSize(plainSize)
	}
	return u.storage.Put(ctx, key, enc, size, contentType)
}

// open streams the plain bytes of an object. The caller closes the reader.
func (u *MediaUseCase) open(ctx context.Context, o object) (io.ReadCloser, error) {
	rc, _, err := u.storage.Get(ctx, o.key)
	if err != nil {
		return nil, err
	}
	if o.wrapped == "" {
		return rc, nil
	}
	fileKey, err := u.unwrap(o.wrapped, o.ctx)
	if err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("unwrap the key of %s: %w", o.key, err)
	}
	dec, err := filecrypt.NewDecryptReader(fileKey, o.stream, rc)
	if err != nil {
		_ = rc.Close()
		return nil, err
	}
	return dec, nil
}

// openRange streams length plain bytes of an object from offset, asking the store for the covering frames only.
func (u *MediaUseCase) openRange(ctx context.Context, o object, offset, length int64) (io.ReadCloser, error) {
	if o.wrapped == "" {
		return u.storage.GetRange(ctx, o.key, offset, length)
	}
	fileKey, err := u.unwrap(o.wrapped, o.ctx)
	if err != nil {
		return nil, fmt.Errorf("unwrap the key of %s: %w", o.key, err)
	}
	return filecrypt.OpenRange(fileKey, o.stream, o.size, offset, length, func(off, n int64) (io.ReadCloser, error) {
		return u.storage.GetRange(ctx, o.key, off, n)
	})
}
