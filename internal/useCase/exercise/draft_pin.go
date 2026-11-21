package exercise

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/uuid"

	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	imageRegistryModel "github.com/cybericebox/daemon/internal/model/imageRegistry"
	imageRegistryUseCase "github.com/cybericebox/daemon/internal/useCase/imageRegistry"
	appErr "github.com/cybericebox/daemon/pkg/err"
)

// draftPinBudget is the time the registry lookups of one draft save may take together.
const draftPinBudget = 30 * time.Second

// unpinnedKey is name:tag of a reference with the digest cut off ("nginx" is "nginx:latest").
func unpinnedKey(image string) string {
	return strings.TrimSuffix(imageRegistryModel.PinnedReference(strings.SplitN(image, "@", 2)[0], ""), "@")
}

// pinDraftImages fixes the registry references of a draft being saved by digest, as an import does. A reference the stored
// working copy already holds fixed under the same name and tag takes that stored value without asking a registry (an
// autosave sends the same text again and again; a tag moved in the registry meanwhile changes nothing). Every other
// reference without a digest is looked up once, together, under one deadline, in the scope of the exercise. A reference
// that cannot be resolved refuses the save: ErrImagePinFailed with the image, the reason and, as a field error, the
// variant (1-based), the device name and field "image". Without an image registry that can pin, nothing is done.
func (u *ExerciseUseCase) pinDraftImages(ctx context.Context, exerciseID uuid.UUID, variants []exerciseModel.Variant, stored []exerciseModel.Variant) error {
	pinner := u.imagePinner()
	if pinner == nil {
		return nil
	}
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
			*image = pinned
			return
		}
		if !slices.Contains(images, *image) {
			images = append(images, *image)
		}
	})
	if len(images) == 0 {
		return nil
	}
	scope := imageRegistryUseCase.Scope{}
	if u.images != nil {
		var err error
		if scope, err = u.images.ExerciseImageScope(ctx, exerciseID); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, draftPinBudget)
	defer cancel()
	pins, err := pinner.PinImages(ctx, scope, images)
	if err != nil {
		return withImageField(err, variants)
	}
	eachVariantImage(variants, func(image *string) {
		if pinned, found := pins[*image]; found {
			*image = pinned
		}
	})
	return nil
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
