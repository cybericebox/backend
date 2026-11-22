package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/model"
	draftKeyModel "github.com/cybericebox/backend/internal/model/draftKey"
	"github.com/cybericebox/backend/internal/secretctx"
	"github.com/cybericebox/backend/pkg/secret"
)

// SetDraftKeyCipher sets the key the draft keys are sealed with at rest (PLATFORM_SECRETS_KEY). Without it no draft
// key is handed out.
func (u *AuthUseCase) SetDraftKeyCipher(cipher *secret.Cipher) { u.draftCipher = cipher }

// DraftKey returns the draft key of this person and browser: the live key named by cookieID when it is this person's,
// otherwise a new one. Keys of the person that nobody used for the key lifetime are deleted first.
func (u *AuthUseCase) DraftKey(ctx context.Context, userID uuid.UUID, cookieID string) (draftKeyModel.Key, error) {
	if u.draftCipher == nil {
		return draftKeyModel.Key{}, model.ErrPlatform.WithMessage("Draft keys are not configured (PLATFORM_SECRETS_KEY)").Err()
	}
	now := time.Now()
	staleBefore := now.Add(-draftKeyModel.TTL)
	if err := u.draftKeys.DeleteStaleOf(ctx, userID, staleBefore); err != nil {
		return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to delete stale draft keys").Err()
	}
	if draftKeyModel.ValidID(cookieID) {
		sealed, found, err := u.draftKeys.Touch(ctx, cookieID, userID, now, staleBefore)
		if err != nil {
			return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read the draft key").Err()
		}
		if found {
			key, derr := u.draftCipher.DecryptWithContext(sealed, secretctx.DraftKey(userID, cookieID))
			if derr == nil && len(key) == draftKeyModel.KeySize {
				u.resealDraftKey(ctx, userID, cookieID, sealed, key)
				return draftKeyModel.Key{ID: cookieID, Key: key}, nil
			}
			// A key that does not open is of no use to anyone: replaced by a new one.
			log.Warn().Err(derr).Msg("A stored draft key does not open; it is replaced")
			if err = u.draftKeys.Delete(ctx, cookieID, userID); err != nil {
				return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to delete the draft key").Err()
			}
		}
	}
	return u.newDraftKey(ctx, userID, now)
}

// resealDraftKey moves a key sealed by an older platform key to the current one: a key in use never expires, so
// without this it would hold the old key forever (keys check counts it). A failure is logged: the key is still
// returned and the next read tries again.
func (u *AuthUseCase) resealDraftKey(ctx context.Context, userID uuid.UUID, id, sealed string, key []byte) {
	if !u.draftCipher.NeedsRotation(sealed) {
		return
	}
	fresh, err := u.draftCipher.EncryptWithContext(key, secretctx.DraftKey(userID, id))
	if err == nil {
		err = u.draftKeys.Reseal(ctx, id, userID, sealed, fresh)
	}
	if err != nil {
		log.Warn().Err(err).Msg("Failed to seal a draft key again under the current platform key")
	}
}

func (u *AuthUseCase) newDraftKey(ctx context.Context, userID uuid.UUID, now time.Time) (draftKeyModel.Key, error) {
	rawID := make([]byte, draftKeyModel.IDSize)
	key := make([]byte, draftKeyModel.KeySize)
	if _, err := rand.Read(rawID); err != nil {
		return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to create a draft key").Err()
	}
	if _, err := rand.Read(key); err != nil {
		return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to create a draft key").Err()
	}
	id := base64.RawURLEncoding.EncodeToString(rawID)
	sealed, err := u.draftCipher.EncryptWithContext(key, secretctx.DraftKey(userID, id))
	if err != nil {
		return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to seal the draft key").Err()
	}
	if err = u.draftKeys.Create(ctx, id, userID, sealed, now, draftKeyModel.MaxPerUser); err != nil {
		return draftKeyModel.Key{}, model.ErrPlatform.WithError(err).WithMessage("Failed to store the draft key").Err()
	}
	return draftKeyModel.Key{ID: id, Key: key}, nil
}

// ForgetDraftKey deletes the key of this browser (sign-out). A cookie that names no key of this person deletes nothing.
func (u *AuthUseCase) ForgetDraftKey(ctx context.Context, userID uuid.UUID, cookieID string) error {
	if !draftKeyModel.ValidID(cookieID) {
		return nil
	}
	if err := u.draftKeys.Delete(ctx, cookieID, userID); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to delete the draft key").Err()
	}
	return nil
}

// ForgetOtherDraftKeys deletes the keys of every other browser of the person (the other sessions were signed out).
func (u *AuthUseCase) ForgetOtherDraftKeys(ctx context.Context, userID uuid.UUID, cookieID string) error {
	var err error
	if draftKeyModel.ValidID(cookieID) {
		err = u.draftKeys.DeleteAllExcept(ctx, userID, cookieID)
	} else {
		err = u.draftKeys.DeleteAll(ctx, userID)
	}
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to delete the draft keys").Err()
	}
	return nil
}

// forgetAllDraftKeys deletes every key of the person (signed out everywhere, blocked, deleted).
func (u *AuthUseCase) forgetAllDraftKeys(ctx context.Context, userID uuid.UUID) error {
	if err := u.draftKeys.DeleteAll(ctx, userID); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to delete the draft keys").Err()
	}
	return nil
}

// CleanupStaleDraftKeys deletes every key nobody used for the key lifetime (the periodic draft_key_gc job).
func (u *AuthUseCase) CleanupStaleDraftKeys(ctx context.Context) error {
	n, err := u.draftKeys.DeleteStale(ctx, time.Now().Add(-draftKeyModel.TTL))
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to delete stale draft keys").Err()
	}
	if n > 0 {
		log.Debug().Int64("deleted", n).Msg("Stale draft keys deleted")
	}
	return nil
}
