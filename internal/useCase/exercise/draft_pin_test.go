package exercise_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"

	"github.com/cybericebox/daemon/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/daemon/internal/delivery/repository/postgres/mocks"
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
	require.Equal(t, [][]string{{"nginx:1.27"}, {"redis"}}, images.asked, "each distinct image is looked up once, on its own")
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

// A draft save never fails because of a registry: an image that cannot be resolved (half typed, unknown, refused, the
// registry down) is stored as typed while the others are still fixed.
func TestSaveDraftKeepsAnUnresolvableImageAsTyped(t *testing.T) {
	s := newLinkedSave(t, nil)
	images := &draftImages{refuse: "ngin"}
	s.uc.SetImageAccess(images)
	base := withImages(saveVariant(saveBaseID, "T", "", "ICE{a}", ""), "ngin")
	base.Topology.Devices = append(base.Topology.Devices, exerciseModel.Device{ID: saveThirdID, Name: "db", Type: exerciseModel.DeviceTypeContainer, Image: "redis:7"})

	written := s.save(t, exercise.SaveDraftInput{Variants: []exerciseModel.Variant{base}})
	require.Equal(t, "ngin", written[0].Topology.Devices[0].Image)
	require.Equal(t, "redis:7@"+fakeDigest("redis:7"), written[0].Topology.Devices[1].Image)
	require.True(t, s.called)
}

// publishFixture is an exercise with a stored draft of one container device whose image is image; PublishDraft reads it.
type publishFixture struct {
	uc        *exercise.ExerciseUseCase
	images    *draftImages
	upserted  [][]byte
	published []byte
}

func newPublishFixture(t *testing.T, image string) *publishFixture {
	t.Helper()
	q := postgresMocks.NewMockQuerier(gomock.NewController(t))
	f := &publishFixture{images: &draftImages{}}
	f.uc = newUC(q, newFakeMedia())
	f.uc.SetImageAccess(f.images)
	exID, draftID := saveExerciseID, saveDraftID
	variants := []exerciseModel.Variant{{ID: saveBaseID, Tasks: []exerciseModel.Task{{
		ID: saveTaskID, Name: "Valid static task", Difficulty: exerciseModel.DifficultyEasy,
		Description: json.RawMessage(`{"root":{"type":"text","text":"Solve this task"}}`), Check: exerciseModel.FlagCheck("ICE{x}"),
	}}, Topology: exerciseModel.Topology{Devices: []exerciseModel.Device{{ID: saveDeviceID, Name: saveDevice, Type: exerciseModel.DeviceTypeContainer, Image: image}}}}}
	stored := mustMarshal(t, variants)
	q.EXPECT().GetExerciseByID(gomock.Any(), exID).Return(postgres.Exercise{ID: exID, Name: "Published name"}, nil).AnyTimes()
	q.EXPECT().GetDraftVersion(gomock.Any(), exID).DoAndReturn(func(context.Context, uuid.UUID) (postgres.ExerciseVersion, error) {
		return postgres.ExerciseVersion{ID: draftID, ExerciseID: exID, Status: "draft", Name: "Published name", Variants: stored}, nil
	}).AnyTimes()
	q.EXPECT().UpsertExerciseDraft(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, arg postgres.UpsertExerciseDraftParams) (postgres.UpsertExerciseDraftRow, error) {
			f.upserted = append(f.upserted, arg.Variants)
			stored = arg.Variants
			return postgres.UpsertExerciseDraftRow{ID: draftID, ExerciseID: exID, Status: "draft", Name: "Published name", Variants: arg.Variants}, nil
		}).AnyTimes()
	q.EXPECT().PublishValidatedExerciseDraft(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, arg postgres.PublishValidatedExerciseDraftParams) (postgres.PublishValidatedExerciseDraftRow, error) {
			f.published = arg.ExpectedVariants
			return postgres.PublishValidatedExerciseDraftRow{ID: arg.ExpectedID, ExerciseID: arg.ExerciseID, Status: "published", Name: arg.ExpectedName, Variants: arg.ExpectedVariants}, nil
		}).AnyTimes()
	return f
}

// A draft saved with an image that cannot be resolved is refused at publish, with the field error next to the image.
func TestPublishRefusesAnImageThatCannotBeResolved(t *testing.T) {
	f := newPublishFixture(t, "nope:1")
	f.images.refuse = "nope:1"
	_, err := f.uc.PublishDraft(context.Background(), saveExerciseID)
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	var carrier interface{ PublicContext() map[string]any }
	require.ErrorAs(t, err, &carrier)
	require.Equal(t, map[string]any{"image": "nope:1", "reason": "not_found", "variant": 1, "device": saveDevice, "field": "image"}, carrier.PublicContext())
	require.Empty(t, f.upserted)
	require.Nil(t, f.published, "nothing is published")
}

// A draft whose tag resolves is published fixed by digest: the pinned draft is stored first and is what is published,
// and no published version holds a bare tag.
func TestPublishStoresThePinnedImage(t *testing.T) {
	f := newPublishFixture(t, "nginx:1.27")
	_, err := f.uc.PublishDraft(context.Background(), saveExerciseID)
	require.NoError(t, err)
	require.Len(t, f.upserted, 1)
	var published []exerciseModel.Variant
	require.NoError(t, json.Unmarshal(f.published, &published))
	require.Equal(t, "nginx:1.27@"+fakeDigest("nginx:1.27"), published[0].Topology.Devices[0].Image)
	require.JSONEq(t, string(f.upserted[0]), string(f.published))
	requireNoBareTags(t, f.published)
}

// A draft already holding pinned and platform images is published as it is: nothing is looked up or rewritten.
func TestPublishOfAPinnedDraftAsksNoRegistry(t *testing.T) {
	f := newPublishFixture(t, pinnedWith("nginx:1.27", "9"))
	_, err := f.uc.PublishDraft(context.Background(), saveExerciseID)
	require.NoError(t, err)
	require.Empty(t, f.images.asked)
	require.Empty(t, f.upserted)
	requireNoBareTags(t, f.published)
}

// requireNoBareTags asserts that every device image and own image of the stored variant JSON is a platform reference or
// carries a digest.
func requireNoBareTags(t *testing.T, raw []byte) {
	t.Helper()
	var variants []exerciseModel.Variant
	require.NoError(t, json.Unmarshal(raw, &variants))
	for _, variant := range variants {
		for _, device := range variant.Topology.Devices {
			require.True(t, device.Image == "" || strings.HasPrefix(device.Image, "platform:") || imageRegistryModel.HasDigest(device.Image), "bare tag %q", device.Image)
		}
		for _, override := range variant.ImageOverrides {
			require.True(t, strings.HasPrefix(override.Image, "platform:") || imageRegistryModel.HasDigest(override.Image), "bare tag %q", override.Image)
		}
	}
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
