package media

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/gofrs/uuid"
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/delivery/repository/keyRotationRepo"
	"github.com/cybericebox/backend/internal/model"
	mediaModel "github.com/cybericebox/backend/internal/model/media"
	"github.com/cybericebox/backend/pkg/secret"
)

// LegacyAvatarsSource is the name of this pass in the progress table that `keys status` prints.
const LegacyAvatarsSource = "users.legacy_avatars_to_media"

const legacyUsersBatch = 200

// EncryptLegacyObjects is what the periodic job runs: the plain blobs, then the legacy avatars.
func (u *MediaUseCase) EncryptLegacyObjects(ctx context.Context) error {
	if _, err := u.EncryptLegacyBlobs(ctx); err != nil {
		return err
	}
	_, err := u.MoveLegacyAvatars(ctx)
	return err
}

// MoveLegacyAvatars moves the avatars that were stored directly under avatars/<user id> (plain, no database record) into
// the media store, which encrypts them. The backend may not list the bucket, so it walks the users and probes the key of
// each. It runs in the background, never from a request:
//   - a deleted user's legacy object is removed, never adopted;
//   - for a live user the encrypted copy is stored first, then the reference is published in one statement that locks the
//     user row and holds only while the legacy avatar is still the user's current one (the user exists, the picture is set,
//     no reference yet), and only then is the legacy object removed. A user who removed the avatar or the account meanwhile
//     does not get the photo back, and the copy is left unreferenced for the media GC;
//   - until a user's object is moved, the read path serves it plain as before.
func (u *MediaUseCase) MoveLegacyAvatars(ctx context.Context) (secret.Progress, error) {
	started := time.Now()
	p := secret.Progress{Family: keyRotationRepo.FamilyFiles, Source: LegacyAvatarsSource, KeyID: u.currentKeyID()}
	if !u.storageConfigured || !u.sealerConfigured || u.legacyAvatarsDone.Load() {
		return p, nil
	}
	save := func(state string) {
		p.UpdatedAt = time.Now()
		if u.progress != nil {
			if err := u.progress.SaveProgress(context.WithoutCancel(ctx), p, state, started); err != nil {
				log.Warn().Msg("media: the progress of the legacy avatar pass could not be written")
			}
		}
	}
	var after uuid.UUID
	for {
		users, err := u.files.ListUsers(ctx, after, legacyUsersBatch)
		if err != nil {
			return p, model.ErrPlatform.WithError(err).WithMessage("Failed to list users").Err()
		}
		if len(users) == 0 {
			break
		}
		for _, user := range users {
			after = user.ID
			if ctx.Err() != nil {
				save(keyRotationRepo.StateInterrupted)
				return p, ctx.Err()
			}
			moved, err := u.moveLegacyAvatar(ctx, user.ID, user.Deleted)
			switch {
			case err != nil:
				p.Total++
				p.Failed++
				log.Warn().Err(err).Str("user", user.ID.String()).Msg("media: a legacy avatar could not be moved, it stays as it is")
			case moved:
				p.Total++
				p.Done++
			}
		}
	}
	if p.Total == 0 {
		u.legacyAvatarsDone.Store(true)
		return p, nil
	}
	if p.Failed > 0 {
		save(keyRotationRepo.StateFinishedWithFailures)
		return p, nil
	}
	u.legacyAvatarsDone.Store(true)
	save(keyRotationRepo.StateFinished)
	return p, nil
}

// moveLegacyAvatar handles one user; moved is false when the user had no legacy object.
func (u *MediaUseCase) moveLegacyAvatar(ctx context.Context, userID uuid.UUID, deleted bool) (moved bool, err error) {
	key := mediaModel.LegacyAvatarKey(userID)
	exists, err := u.storage.Stat(ctx, key)
	if err != nil || !exists {
		return false, err
	}
	if deleted {
		return true, u.storage.Remove(ctx, key)
	}
	rc, contentType, err := u.storage.Get(ctx, key)
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(rc, mediaModel.MaxLegacyAvatarBytes+1))
	_ = rc.Close()
	if err != nil {
		return false, err
	}
	if len(data) > mediaModel.MaxLegacyAvatarBytes {
		return false, io.ErrShortBuffer
	}
	file, err := u.upload(ctx, "avatar", contentType, bytes.NewReader(data), userID, mediaModel.MaxLegacyAvatarBytes, false)
	if err != nil {
		return false, err
	}
	// Publish first, in one statement that locks the user row and re-checks that the legacy avatar is still the user's current
	// one (the user exists, the picture is set, no reference yet). No retry: a failed statement leaves everything as it is
	// for the next pass, and a refusal is final. The legacy object goes only after this, so a crash in between leaves a
	// duplicate that the next pass cleans (its adopt is refused, the reference stands), never a photo that comes back.
	if _, err = u.files.AdoptLegacyAvatar(ctx, userID, file.ID); err != nil {
		return false, err
	}
	// Adopted or refused, the legacy object is not wanted any more. A refused adopt leaves the new file unreferenced, which
	// is what makes it eligible for the media GC.
	if err = u.storage.Remove(ctx, key); err != nil {
		return true, err
	}
	return true, nil
}
