package exercise_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	imageRegistryModel "github.com/cybericebox/daemon/internal/model/imageRegistry"
	exercise "github.com/cybericebox/daemon/internal/useCase/exercise"
	imageRegistryUseCase "github.com/cybericebox/daemon/internal/useCase/imageRegistry"
)

// draftImages is the image registry of the editor: the scope an exercise is read in, and the digest lookup.
type draftImages struct {
	scope  imageRegistryUseCase.Scope
	asked  [][]string
	refuse string
}

func (d *draftImages) ExerciseImageScope(context.Context, uuid.UUID) (imageRegistryUseCase.Scope, error) {
	return d.scope, nil
}

func (d *draftImages) PrepareDeployImages(_ context.Context, _ imageRegistryUseCase.Scope, topo exerciseModel.Topology) (imageRegistryUseCase.DeployImages, error) {
	return imageRegistryUseCase.DeployImages{Topology: topo}, nil
}

func (d *draftImages) PinImages(_ context.Context, _ imageRegistryUseCase.Scope, images []string) (map[string]string, error) {
	d.asked = append(d.asked, images)
	pins := map[string]string{}
	for _, image := range images {
		if image == d.refuse {
			return nil, imageRegistryModel.ErrImagePinFailed.WithPublicContext("image", image).WithPublicContext("reason", "not_found").Err()
		}
		pins[image] = imageRegistryModel.PinnedReference(image, fakeDigest(image))
	}
	return pins, nil
}

func pinnedWith(image, digestHex string) string {
	return image + "@sha256:" + strings.Repeat(digestHex, 64)
}

func withImages(v exerciseModel.Variant, device string) exerciseModel.Variant {
	v.Topology.Devices[0].Image = device
	return v
}

// Saving a draft fixes the typed registry references by digest (devices and own images of a linked variant), keeps a
// platform reference and a reference that has a digest, and reads registries in the scope of the exercise.
func TestSaveDraftPinsTypedImages(t *testing.T) {
	s := newLinkedSave(t, nil)
	event := uuid.Must(uuid.NewV7())
	images := &draftImages{scope: imageRegistryUseCase.Scope{EventID: &event}}
	s.uc.SetImageAccess(images)
	platform := "platform:web@sha256:" + strings.Repeat("c", 64)
	base := withImages(saveVariant(saveBaseID, "T", "", "ICE{a}", ""), "nginx:1.27")
	base.Topology.Devices = append(base.Topology.Devices, exerciseModel.Device{ID: saveThirdID, Name: "db", Type: exerciseModel.DeviceTypeContainer, Image: platform})
	linked := linkTo(saveVariant(saveLinkedID, "", "", "ICE{b}", ""), saveBaseID)
	linked.ImageOverrides = []exerciseModel.ImageOverride{{DeviceID: saveDeviceID, Image: "redis"}}

	written := s.save(t, exercise.SaveDraftInput{Variants: []exerciseModel.Variant{base, linked}})

	require.Equal(t, "nginx:1.27@"+fakeDigest("nginx:1.27"), written[0].Topology.Devices[0].Image)
	require.Equal(t, platform, written[0].Topology.Devices[1].Image)
	require.Equal(t, "redis:latest@"+fakeDigest("redis"), written[1].ImageOverrides[0].Image)
	require.Equal(t, "redis:latest@"+fakeDigest("redis"), written[1].Topology.Devices[0].Image)
	require.Len(t, images.asked, 1, "one lookup call for the whole save")
	require.ElementsMatch(t, []string{"nginx:1.27", "redis"}, images.asked[0])
}

// An autosave that sends the typed tag back again, for an image the stored draft already fixed under the same name and
// tag, keeps the stored digest and asks no registry; a different tag is looked up.
func TestSaveDraftReusesTheStoredPinOfTheSameTag(t *testing.T) {
	stored := pinnedWith("nginx:1.27", "9")
	s := newLinkedSave(t, []exerciseModel.Variant{withImages(saveVariant(saveBaseID, "T", "", "ICE{a}", ""), stored)})
	images := &draftImages{}
	s.uc.SetImageAccess(images)

	written := s.save(t, exercise.SaveDraftInput{Variants: []exerciseModel.Variant{withImages(saveVariant(saveBaseID, "T", "", "ICE{a}", ""), "nginx:1.27")}})
	require.Equal(t, stored, written[0].Topology.Devices[0].Image)
	require.Empty(t, images.asked)

	written = s.save(t, exercise.SaveDraftInput{Variants: []exerciseModel.Variant{withImages(saveVariant(saveBaseID, "T", "", "ICE{a}", ""), "nginx:1.28")}})
	require.Equal(t, "nginx:1.28@"+fakeDigest("nginx:1.28"), written[0].Topology.Devices[0].Image)
	require.Len(t, images.asked, 1)
}

// An image the registry cannot resolve refuses the save with the error naming the image, the variant, the device and the
// field, so the editor shows it next to the image field; nothing is stored.
func TestSaveDraftPinFailureIsAFieldError(t *testing.T) {
	s := newLinkedSave(t, nil)
	s.uc.SetImageAccess(&draftImages{refuse: "nope:1"})
	_, err := s.uc.SaveDraft(context.Background(), saveExerciseID, exercise.SaveDraftInput{
		Variants: []exerciseModel.Variant{withImages(saveVariant(saveBaseID, "T", "", "ICE{a}", ""), "nope:1")},
	})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	var carrier interface{ PublicContext() map[string]any }
	require.ErrorAs(t, err, &carrier)
	require.Equal(t, map[string]any{"image": "nope:1", "reason": "not_found", "variant": 1, "device": saveDevice, "field": "image"}, carrier.PublicContext())
	require.False(t, s.called, "nothing is stored")
}

// The single-archive import pins like the others, before anything is created.
func TestImportExerciseArchivePinsAndFailsAsAWhole(t *testing.T) {
	f := newImportFixture(t)
	f.expectCreation(t)
	f.withPinning(t)
	archive := zipOf(t, map[string][]byte{"exercise.json": imageTask("Single", [2]string{"web", "nginx:1.27"}, [2]string{"db", "postgres"})})

	_, err := f.uc.ImportExerciseArchive(context.Background(), exercise.ImportExerciseInput{Archive: archive, CreatedBy: importer})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"web": "nginx:1.27@" + fakeDigest("nginx:1.27"), "db": "postgres:latest@" + fakeDigest("postgres")}, f.writtenImages(t))

	g := newImportFixture(t)
	g.expectCreation(t)
	g.withPinning(t).refuse = "postgres"
	_, err = g.uc.ImportExerciseArchive(context.Background(), exercise.ImportExerciseInput{Archive: archive, CreatedBy: importer})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	require.Empty(t, g.created, "nothing is created")
}
