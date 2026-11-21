package exercise_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	imageRegistryModel "github.com/cybericebox/daemon/internal/model/imageRegistry"
	jobsModel "github.com/cybericebox/daemon/internal/model/jobs"
	mediaModel "github.com/cybericebox/daemon/internal/model/media"
	exercise "github.com/cybericebox/daemon/internal/useCase/exercise"
	imageRegistryUseCase "github.com/cybericebox/daemon/internal/useCase/imageRegistry"
)

// pinningImages is the image registry of an import that also fixes external references by digest: a reference without a
// digest gets the digest "sha256:" + 64 hex made of its length, so every test reads it without a registry.
type pinningImages struct {
	*fakeImages
	scopes []imageRegistryUseCase.Scope
	asked  [][]string
	// deadlines are the context deadlines the lookups ran under; unbounded is set when one had none.
	deadlines []time.Time
	unbounded bool
	// refuse names an image the registry cannot resolve.
	refuse string
}

func fakeDigest(image string) string {
	return "sha256:" + strings.Repeat(string(rune('a'+len(image)%6)), 64)
}

func (p *pinningImages) PinImages(ctx context.Context, scope imageRegistryUseCase.Scope, images []string) (map[string]string, error) {
	deadline, bounded := ctx.Deadline()
	p.deadlines = append(p.deadlines, deadline)
	p.unbounded = p.unbounded || !bounded
	p.scopes = append(p.scopes, scope)
	p.asked = append(p.asked, images)
	pins := map[string]string{}
	for _, image := range images {
		if image == p.refuse {
			return nil, imageRegistryModel.ErrImagePinFailed.WithPublicContext("image", image).WithPublicContext("reason", "not_found").Err()
		}
		if !imageRegistryModel.HasDigest(image) {
			pins[image] = imageRegistryModel.PinnedReference(image, fakeDigest(image))
		}
	}
	return pins, nil
}

func (f *importFixture) withPinning(t *testing.T) *pinningImages {
	t.Helper()
	p := &pinningImages{fakeImages: f.images}
	f.uc.SetArchiveImport(exercise.ArchiveImportDependencies{
		Images: p, Sources: f.sources, Sealer: f.cipher,
		Queue: func(_ context.Context, args jobsModel.ExerciseImportArgs) error {
			f.queued = append(f.queued, args)
			return nil
		},
	})
	return p
}

// An archive with images files and references: the references are stored with their digest (name:tag@sha256), a
// reference that already has one and a pushed platform image stay as they are, and only the first kind is looked up.
func TestImportArchivePinsExternalImages(t *testing.T) {
	f := newImportFixture(t)
	f.expectCreation(t)
	pinning := f.withPinning(t)
	already := "ghcr.io/acme/app:2@sha256:" + strings.Repeat("9", 64)
	f.sources.stage(archiveUpID, importer, zipOf(t, map[string][]byte{
		"exercise.json": imageTask("Pins", [2]string{"web", "images/web.tar"}, [2]string{"proxy", "nginx:1.27"},
			[2]string{"proxy-copy", "nginx:1.27"}, [2]string{"app", already}, [2]string{"db", "postgres"}),
		"images/web.tar": webImageTar,
	}), mediaModel.UploadPurposeExerciseArchive)

	view := f.startAndRun(t)
	require.Equal(t, exerciseModel.ImportJobDone, view.State, "error %+v", view.Error)
	require.Equal(t, map[string]string{
		"web":        webReference(),
		"proxy":      "nginx:1.27@" + fakeDigest("nginx:1.27"),
		"proxy-copy": "nginx:1.27@" + fakeDigest("nginx:1.27"),
		"app":        already,
		"db":         "postgres:latest@" + fakeDigest("postgres"),
	}, f.writtenImages(t))
	require.Len(t, pinning.asked, 1)
	require.ElementsMatch(t, []string{"nginx:1.27", "nginx:1.27", "postgres"}, pinning.asked[0], "neither the pushed platform image nor the one with a digest is asked about")
	require.Equal(t, []imageRegistryUseCase.Scope{{}}, pinning.scopes, "a catalog import reads registries in the public scope")
}

// An image that cannot be resolved fails the whole import, names the image, and creates nothing.
func TestImportArchivePinFailureCreatesNothing(t *testing.T) {
	f := newImportFixture(t)
	pinning := f.withPinning(t)
	pinning.refuse = "ghcr.io/acme/gone:1"
	f.sources.stage(archiveUpID, importer, zipOf(t, map[string][]byte{
		"exercise.json":  imageTask("Pins", [2]string{"web", "images/web.tar"}, [2]string{"gone", "ghcr.io/acme/gone:1"}),
		"images/web.tar": webImageTar,
	}), mediaModel.UploadPurposeExerciseArchive)

	view := f.startAndRun(t)
	require.Equal(t, exerciseModel.ImportJobFailed, view.State)
	require.Empty(t, view.ExerciseIDs)
	require.Empty(t, f.created)
	want := imageRegistryModel.ErrImagePinFailed.Err().StatusCode()
	require.Equal(t, want.FullCode(), view.Error.Code)
	require.Equal(t, "ghcr.io/acme/gone:1", view.Error.Context["image"])
	require.Equal(t, "not_found", view.Error.Context["reason"])
}

// An archive without image files is imported at once and pinned the same way, in the scope of the event it goes to.
func TestImportUploadedArchiveWithoutImageFilesIsPinned(t *testing.T) {
	f := newImportFixture(t)
	f.expectCreation(t)
	pinning := f.withPinning(t)
	f.sources.stage(archiveUpID, importer, zipOf(t, map[string][]byte{"exercise.json": imageTask("Plain", [2]string{"web", "nginx:1.27"})}), mediaModel.UploadPurposeExerciseArchive)
	event := uuid.Must(uuid.NewV7())
	start, err := f.uc.ImportUploadedArchive(context.Background(), exercise.ImportUploadInput{UploadID: archiveUpID, CreatedBy: importer, OwnerEventID: &event})
	require.NoError(t, err)
	require.Len(t, start.Exercises, 1)
	require.Equal(t, map[string]string{"web": "nginx:1.27@" + fakeDigest("nginx:1.27")}, f.writtenImages(t))
	require.Equal(t, []imageRegistryUseCase.Scope{{EventID: &event}}, pinning.scopes)
}

// The lookups of one import share a deadline, so many images on a slow registry cannot hold the import for long.
func TestImportArchiveBoundsThePinLookups(t *testing.T) {
	f := newImportFixture(t)
	f.expectCreation(t)
	pinning := f.withPinning(t)
	f.sources.stage(archiveUpID, importer, zipOf(t, map[string][]byte{"exercise.json": imageTask("Plain", [2]string{"web", "nginx:1.27"})}), mediaModel.UploadPurposeExerciseArchive)
	_, err := f.uc.ImportUploadedArchive(context.Background(), exercise.ImportUploadInput{UploadID: archiveUpID, CreatedBy: importer})
	require.NoError(t, err)
	require.False(t, pinning.unbounded, "a lookup with no deadline")
	require.Len(t, pinning.deadlines, 1)
	require.WithinDuration(t, time.Now().Add(3*time.Minute), pinning.deadlines[0], 10*time.Second)
}

// An image that cannot be resolved stops the import before anything is pushed or uploaded: not an image file of the archive, not
// an attachment of an earlier task.
func TestImportArchivePinFailureLeavesNothingPushedOrUploaded(t *testing.T) {
	f := newImportFixture(t)
	pinning := f.withPinning(t)
	pinning.refuse = "ghcr.io/acme/gone:1"
	doc := func(name, image string) []byte {
		return []byte("schema: cib-exercise/1.0\nexercise:\n  name: " + name + "\nversions:\n  - status: draft\n    variants:\n      - tasks:\n          - name: t\n" +
			"            attachments:\n              - {file: files/a.bin}\n        topology:\n          devices:\n            - {name: web, type: container, image: " + image + "}\n")
	}
	f.sources.stage(archiveUpID, importer, zipOf(t, map[string][]byte{
		"a/exercise.yaml": doc("First", "{file: images/web.tar}"), "a/files/a.bin": []byte("packets"), "a/images/web.tar": webImageTar,
		"b/exercise.yaml": doc("Second", "ghcr.io/acme/gone:1"), "b/files/a.bin": []byte("other"),
	}), mediaModel.UploadPurposeExerciseArchive)

	view := f.startAndRun(t)
	require.Equal(t, exerciseModel.ImportJobFailed, view.State)
	require.Equal(t, "ghcr.io/acme/gone:1", view.Error.Context["image"])
	require.Empty(t, f.images.pushes, "the image file of the first task was pushed before the second image was looked up")
	require.Empty(t, f.media.uploads, "the attachment of the first task was uploaded before the second image was looked up")
}
