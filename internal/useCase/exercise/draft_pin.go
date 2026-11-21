package exercise

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/uuid"

	repositoryTools "github.com/cybericebox/daemon/internal/delivery/repository/tools"
	"github.com/cybericebox/daemon/internal/model"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	imageRegistryModel "github.com/cybericebox/daemon/internal/model/imageRegistry"
	imageRegistryUseCase "github.com/cybericebox/daemon/internal/useCase/imageRegistry"
	appErr "github.com/cybericebox/daemon/pkg/err"
)

// draftPinBudget is the time the registry lookups of one draft save, or of the pinning a publish does, may take together.
const draftPinBudget = 10 * time.Second

// unpinnedKey is name:tag of a reference with the digest cut off ("nginx" is "nginx:latest").
func unpinnedKey(image string) string {
	return strings.TrimSuffix(imageRegistryModel.PinnedReference(strings.SplitN(image, "@", 2)[0], ""), "@")
}

// pinImages fixes the registry references of the variants by digest, in place, and tells whether any changed. A reference
// the stored working copy already holds fixed under the same name and tag takes that stored value without asking a registry
// (an autosave sends the same text again and again; a tag moved in the registry meanwhile changes nothing). The rest are
// looked up under one deadline in the scope of the exercise.
//
// Not strict (a draft save, which must never fail because of a registry): each image is looked up on its own, and one that
// cannot be resolved for any reason (not found, refused, unreachable, the deadline) stays as typed. Strict (a publish): the
// lookups are one all-or-nothing call, and a reference that cannot be resolved is ErrImagePinFailed with the image, the
// reason and, as a field error, the variant (1-based), the device name and field "image". Without an image registry that
// can pin, nothing is done.
func (u *ExerciseUseCase) pinImages(ctx context.Context, exerciseID uuid.UUID, variants, stored []exerciseModel.Variant, strict bool) (bool, error) {
	pinner := u.imagePinner()
	if pinner == nil {
		return false, nil
	}
	changed := false
	storedPins := map[string]string{}
	eachVariantImage(stored, func(image *string) {
		if isExternalImage(*image) && imageRegistryModel.HasDigest(*image) {
			storedPins[unpinnedKey(*image)] = *image
		}
	})
	var images []string
	eachVariantImage(variants, func(image *string) {
		if !isExternalImage(*image) || imageRegistryModel.HasDigest(*image) {
			return
		}
		if pinned, found := storedPins[unpinnedKey(*image)]; found {
			*image, changed = pinned, true
			return
		}
		if !slices.Contains(images, *image) {
			images = append(images, *image)
		}
	})
	if len(images) == 0 {
		return changed, nil
	}
	scope := imageRegistryUseCase.Scope{}
	if u.images != nil {
		var err error
		if scope, err = u.images.ExerciseImageScope(ctx, exerciseID); err != nil {
			return changed, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, draftPinBudget)
	defer cancel()
	pins := map[string]string{}
	if strict {
		var err error
		if pins, err = pinner.PinImages(ctx, scope, images); err != nil {
			return changed, withImageField(err, variants)
		}
	} else {
		for _, image := range images {
			if one, err := pinner.PinImages(ctx, scope, []string{image}); err == nil {
				maps.Copy(pins, one)
			}
		}
	}
	eachVariantImage(variants, func(image *string) {
		if pinned, found := pins[*image]; found {
			*image, changed = pinned, true
		}
	})
	return changed, nil
}

// pinDraftImages is the pinning of a draft save (not strict).
func (u *ExerciseUseCase) pinDraftImages(ctx context.Context, exerciseID uuid.UUID, variants, stored []exerciseModel.Variant) {
	_, _ = u.pinImages(ctx, exerciseID, variants, stored, false)
}

// pinDraftForPublish fixes every still-unpinned registry reference of the draft about to be published, so a published
// version never holds a bare tag. The lookups come first, outside any transaction and all-or-nothing (one that cannot be
// resolved refuses the publish with the field error). When something changed, the pinned draft is stored under the
// catalog lock, only if the draft is still exactly what was read, at the same revision (else ErrExerciseModified), and
// the stored row is what is published. The write goes through the revision check like every writer of the working copy;
// it keeps the last saver (pinning is not an edit of its own).
func (u *ExerciseUseCase) pinDraftForPublish(ctx context.Context, exerciseID uuid.UUID, draft exerciseModel.ExerciseVersion) (exerciseModel.ExerciseVersion, error) {
	if u.imagePinner() == nil {
		return draft, nil
	}
	pinned, err := copyImportVersion(draft)
	if err != nil {
		return draft, err
	}
	changed, err := u.pinImages(ctx, exerciseID, pinned.Variants, nil, true)
	if err != nil || !changed {
		return draft, err
	}
	ctx, session, q, unit, err := u.beginCatalog(ctx)
	if err != nil {
		return draft, err
	}
	defer unit.Restore()
	if _, err = q.LockExerciseForImport(ctx, exerciseID); err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return draft, exerciseModel.ErrExerciseNotFound.Err()
		}
		return draft, model.ErrPlatform.WithError(err).WithMessage("Failed to lock draft").Err()
	}
	latest, err := session.exercises.GetDraft(ctx, exerciseID)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return draft, exerciseModel.ErrNoDraft.Err()
		}
		return draft, model.ErrPlatform.WithError(err).WithMessage("Failed to get draft").Err()
	}
	if latest.ID != draft.ID || !latest.UpdatedAt.Equal(draft.UpdatedAt) || !reflect.DeepEqual(latest.Variants, draft.Variants) {
		return draft, exerciseModel.ErrExerciseModified.Err()
	}
	saved, err := session.exercises.UpsertDraft(ctx, exerciseID, uuid.Must(uuid.NewV7()), pinned, time.Now(), draft.UpdatedBy, draft.UpdatedAt)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			// Zero rows is the revision check: the working copy was saved, discarded or published meanwhile.
			return draft, exerciseModel.ErrExerciseModified.Err()
		}
		return draft, model.ErrPlatform.WithError(err).WithMessage("Failed to save the pinned draft").Err()
	}
	if err = unit.Save(); err != nil {
		return draft, model.ErrPlatform.WithError(err).WithMessage("Failed to save the pinned draft").Err()
	}
	return saved, nil
}

// withImageField adds the position of the first device or own image holding the image a pin failure names (public
// context variant, device, field), so the editor can show the error next to that field.
func withImageField(failure error, variants []exerciseModel.Variant) error {
	var creator appErr.ErrorCreator
	var carrier interface{ PublicContext() map[string]any }
	if !errors.As(failure, &creator) || !errors.As(failure, &carrier) {
		return failure
	}
	image, _ := carrier.PublicContext()["image"].(string)
	for vi, variant := range variants {
		names := map[uuid.UUID]string{}
		for _, device := range variant.Topology.Devices {
			names[device.ID] = device.Name
			if device.Image == image {
				return creator.WithPublicContext("variant", vi+1).WithPublicContext("device", device.Name).WithPublicContext("field", "image").Err()
			}
		}
		for _, override := range variant.ImageOverrides {
			if override.Image == image {
				return creator.WithPublicContext("variant", vi+1).WithPublicContext("device", names[override.DeviceID]).WithPublicContext("field", "image").Err()
			}
		}
	}
	return failure
}
