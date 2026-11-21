package exercise

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/model"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
	imageRegistryModel "github.com/cybericebox/daemon/internal/model/imageRegistry"
	imageRegistryUseCase "github.com/cybericebox/daemon/internal/useCase/imageRegistry"
	appErr "github.com/cybericebox/daemon/pkg/err"
	"github.com/cybericebox/daemon/pkg/ociregistry"
	yzip "github.com/yeka/zip"
)

// Device images inside a task archive (`image: {file: images/web.tar}`). Such an archive is read where it lies
// (an io.ReaderAt), never whole: the documents first, which name the image files; every other entry is read into
// memory as before (same limits), while the image files stay in the zip and are streamed from it twice each (index,
// then push). Image files do not count towards the unpacked total; each is at most MaxImageArchiveBytes, and all of
// them together too. The import is all or nothing: every task is parsed and checked, every image indexed and pushed,
// and only then are the tasks created.

// imageFileMarker starts the image a device holds while its image file is not pushed yet; the rest is the file's
// archive path. A reference in the document may not start with it.
const imageFileMarker = exerciseModel.PendingArchiveImagePrefix

// archiveImageFile is one device image file of the archive, left in the zip.
type archiveImageFile struct {
	// Name is the entry's path in the archive.
	Name string
	// Size is its unpacked size (the zip header's; the reader stops at it).
	Size int64
	open func() (io.ReadCloser, error)
	// checksum is the hex sha256 manifest.json lists for it ("" when it is not listed).
	checksum string
}

// ParsedArchiveImage is a device image file a task's document names: Device is the first device naming it (the
// platform image name), Marker what the devices hold until the push.
type ParsedArchiveImage struct {
	Path   string
	Device string
	Marker string
	file   *archiveImageFile
}

// zipEntry is one entry of a zip opened over an io.ReaderAt, plain or protected.
type zipEntry struct {
	raw          string
	mode         fs.FileMode
	uncompressed uint64
	compressed   uint64
	encrypted    bool
	// open reads the entry; stream (for image files) checks a protected entry's authentication code at its end
	// instead of buffering the whole entry first.
	open func(stream bool) (io.ReadCloser, error)
}

// openZipAt opens the zip over r: plain, or AES-protected when its entries are encrypted and a password is given
// (protected tells which). An encrypted archive without a password fails like the plain read of it.
func openZipAt(r io.ReaderAt, size int64, password string, lim exerciseModel.ArchiveLimits) ([]zipEntry, bool, error) {
	zbr := newOpenBudgetReaderAt(r, lim.Entries)
	zr, err := zip.NewReader(zbr, size)
	if refused := zbr.finish(); refused != nil {
		return nil, false, refused
	}
	if err != nil {
		return nil, false, err
	}
	if err = checkEntryCount(len(zr.File), lim); err != nil {
		return nil, false, err
	}
	encrypted := false
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 {
			encrypted = true
		}
	}
	if !encrypted {
		entries := make([]zipEntry, 0, len(zr.File))
		for _, f := range zr.File {
			entries = append(entries, zipEntry{
				raw: f.Name, mode: f.Mode(), uncompressed: f.UncompressedSize64, compressed: f.CompressedSize64,
				open: func(bool) (io.ReadCloser, error) { return f.Open() },
			})
		}
		return entries, false, nil
	}
	if password == "" {
		return nil, false, exerciseModel.ErrArchivePasswordRequired.Err()
	}
	ybr := newOpenBudgetReaderAt(r, lim.Entries)
	yr, err := yzip.NewReader(ybr, size)
	if refused := ybr.finish(); refused != nil {
		return nil, false, refused
	}
	if err != nil {
		return nil, false, err
	}
	entries := make([]zipEntry, 0, len(yr.File))
	for _, f := range yr.File {
		f.SetPassword(password)
		entries = append(entries, zipEntry{
			raw: f.Name, mode: f.Mode(), uncompressed: f.UncompressedSize64, compressed: f.CompressedSize64, encrypted: f.IsEncrypted(),
			open: func(stream bool) (io.ReadCloser, error) {
				f.DeferAuth = stream
				return f.Open()
			},
		})
	}
	return entries, true, nil
}

// archiveRead is an archive read with its image files left in the zip.
type archiveRead struct {
	// files are every entry but the image files (NFC names), manifest.json checked and removed.
	files map[string][]byte
	// images are the image files by archive path.
	images    map[string]*archiveImageFile
	protected bool
}

func isDocumentEntry(name string) bool {
	base := path.Base(name)
	return (base == "exercise.json" || base == "exercise.yaml") && !isArchiveLitter(name)
}

// checkedEntries applies the entry rules to every entry (symlinks, unsafe or repeated paths, the entry count) and
// returns the files by NFC name in archive order.
func checkedEntries(entries []zipEntry, protected bool, lim exerciseModel.ArchiveLimits) ([]string, map[string]zipEntry, error) {
	if err := checkEntryCount(len(entries), lim); err != nil {
		return nil, nil, err
	}
	seen := map[string][]byte{}
	byName := map[string]zipEntry{}
	var names []string
	for _, e := range entries {
		name, skip, err := checkArchiveEntry(e.raw, e.mode, seen)
		if err != nil {
			return nil, nil, err
		}
		if skip {
			continue
		}
		if protected && !e.encrypted {
			return nil, nil, errUnencryptedEntry
		}
		seen[name], byName[name] = nil, e
		names = append(names, name)
	}
	return names, byName, nil
}

// readEntry reads one non-image entry within the budget.
func readEntry(budget *archiveBudget, count int, e zipEntry) ([]byte, error) {
	if err := budget.check(count, e.uncompressed, e.compressed); err != nil {
		return nil, err
	}
	rc, err := e.open(false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return budget.read(rc)
}

// readDocuments reads the exercise.json / exercise.yaml entries.
func readDocuments(names []string, byName map[string]zipEntry, count int, budget *archiveBudget) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range names {
		if !isDocumentEntry(name) {
			continue
		}
		body, err := readEntry(budget, count, byName[name])
		if err != nil {
			return nil, err
		}
		files[name] = body
	}
	return files, nil
}

// documentImageFiles is the set of archive paths the documents name as device image files, and whether any device
// names one at all. A link that is not a safe path inside its folder is left out of the set: the parse names it.
func documentImageFiles(documents map[string][]byte) (map[string]bool, bool, error) {
	out := map[string]bool{}
	named := false
	names := sortedArchiveNames(documents)
	for _, name := range names {
		doc, err := decodeDocument(name, documents[name])
		if err != nil {
			return nil, false, err
		}
		prefix := path.Dir(name)
		for _, version := range doc.Versions {
			for _, variant := range version.Variants {
				var files []string
				for _, device := range variant.Topology.Devices {
					files = append(files, device.Image.File)
				}
				for _, image := range variant.DeviceImages {
					files = append(files, image.File)
				}
				for _, file := range files {
					if file == "" {
						continue
					}
					named = true
					clean, linkErr := checkLinkPath(file)
					if linkErr != nil {
						continue
					}
					if prefix != "." {
						clean = prefix + "/" + clean
					}
					out[clean] = true
				}
			}
		}
	}
	return out, named, nil
}

// namesImageFiles reads only the documents of an opened archive and tells whether any device names an image file.
func namesImageFiles(entries []zipEntry, protected bool, lim exerciseModel.ArchiveLimits) (bool, error) {
	names, byName, err := checkedEntries(entries, protected, lim)
	if err != nil {
		return false, err
	}
	documents, err := readDocuments(names, byName, len(entries), newArchiveBudget(lim))
	if err != nil {
		return false, err
	}
	_, named, err := documentImageFiles(documents)
	return named, err
}

// ArchiveNamesImages tells whether a task archive (bytes, the password when it is protected) names any device image
// file; such an archive is imported in the background. A broken archive answers false: the synchronous import names
// what is wrong with it.
func ArchiveNamesImages(contents []byte, password string, lim exerciseModel.ArchiveLimits) bool {
	entries, protected, err := openZipAt(bytes.NewReader(contents), int64(len(contents)), password, lim)
	if err != nil {
		return false
	}
	named, err := namesImageFiles(entries, protected, lim)
	return err == nil && named
}

// readArchiveEntries reads an opened archive with its image files left in the zip.
func readArchiveEntries(entries []zipEntry, protected bool, lim exerciseModel.ArchiveLimits) (archiveRead, error) {
	names, byName, err := checkedEntries(entries, protected, lim)
	if err != nil {
		return archiveRead{}, err
	}
	budget := newArchiveBudget(lim)
	files, err := readDocuments(names, byName, len(entries), budget)
	if err != nil {
		return archiveRead{}, err
	}
	imageNames, _, err := documentImageFiles(files)
	if err != nil {
		return archiveRead{}, err
	}
	images := map[string]*archiveImageFile{}
	var imageTotal int64
	for _, name := range names {
		if _, done := files[name]; done {
			continue
		}
		e := byName[name]
		if !imageNames[name] {
			body, readErr := readEntry(budget, len(entries), e)
			if readErr != nil {
				return archiveRead{}, readErr
			}
			files[name] = body
			continue
		}
		size := int64(e.uncompressed)
		if e.uncompressed > uint64(imageRegistryModel.MaxImageArchiveBytes) {
			return archiveRead{}, archiveTooLarge("an image file is over " + gib(imageRegistryModel.MaxImageArchiveBytes) + " unpacked")
		}
		if imageTotal += size; imageTotal > imageRegistryModel.MaxImageArchiveBytes {
			return archiveRead{}, archiveTooLarge("the image files are over " + gib(imageRegistryModel.MaxImageArchiveBytes) + " unpacked together")
		}
		images[name] = imageFileOf(name, size, e.open)
	}
	files, err = validateArchiveManifestWith(files, images)
	if err != nil {
		return archiveRead{}, err
	}
	return archiveRead{files: files, images: images, protected: protected}, nil
}

// imageFileOf is an image entry left in the zip, read through sizedEntryReader.
func imageFileOf(name string, size int64, open func(stream bool) (io.ReadCloser, error)) *archiveImageFile {
	return &archiveImageFile{Name: name, Size: size, open: func() (io.ReadCloser, error) {
		rc, err := open(true)
		if err != nil {
			if !isTyped(err) && IsArchiveFormatError(err) {
				return nil, fmt.Errorf("%w: %w", ociregistry.ErrSourceCorrupt, err)
			}
			return nil, err
		}
		return &sizedEntryReader{r: rc, size: size}, nil
	}}
}

func gib(n int64) string { return fmt.Sprintf("%d GiB", n>>30) }

// parseArchiveRead parses every task folder of an archive read with its image files.
func parseArchiveRead(read archiveRead) ([]ParsedExerciseArchive, error) {
	folders, err := splitTaskFolders(read.files)
	if err != nil {
		return nil, err
	}
	tasks := make([]ParsedExerciseArchive, 0, len(folders))
	for _, folder := range folders {
		images := map[string]*archiveImageFile{}
		for name, image := range read.images {
			if folder.Prefix == "" {
				images[name] = image
			} else if rel, ok := strings.CutPrefix(name, folder.Prefix+"/"); ok {
				images[rel] = image
			}
		}
		parsed, parseErr := parseTaskFolder(folder.Files, images)
		if parseErr != nil {
			return nil, parseErr
		}
		// Every version was checked while parsing; the import brings one of them, as a draft.
		parsed.keepOneVersionAsDraft()
		parsed.ItemKey = importItemKey(folder.Prefix)
		tasks = append(tasks, parsed)
	}
	return tasks, nil
}

// pushArchiveImages indexes every image file the tasks name (each once), then pushes each; it returns the platform
// reference per marker. An image is named after the first device naming it.
func (u *ExerciseUseCase) pushArchiveImages(ctx context.Context, tasks []ParsedExerciseArchive, createdBy uuid.UUID, progress *importProgress) (map[string]string, error) {
	var images []ParsedArchiveImage
	seen := map[string]bool{}
	for _, task := range tasks {
		for _, image := range task.Images {
			if !seen[image.Marker] {
				seen[image.Marker] = true
				images = append(images, image)
			}
		}
	}
	if len(images) == 0 {
		return nil, nil
	}
	if u.archiveImages == nil {
		return nil, model.ErrPlatform.WithMessage("Image import is not wired").Err()
	}
	// The registry not configured: the upload refusal, before any image is read.
	if err := u.archiveImages.RequireImageUploads(ctx); err != nil {
		return nil, err
	}
	var totalBytes int64
	for _, image := range images {
		totalBytes += image.file.Size
	}
	progress.images(ctx, len(images), totalBytes)
	indexed := make([]*ociregistry.ImageArchive, len(images))
	for i, image := range images {
		archive, err := u.archiveImages.IndexImageArchive(image.file.open, image.file.Size)
		if err != nil {
			return nil, withImageFile(err, image.file.Name)
		}
		if image.file.checksum != "" && archive.SHA256 != image.file.checksum {
			return nil, archiveChecksumMismatch(image.file.Name)
		}
		indexed[i] = archive
	}
	refs := make(map[string]string, len(images))
	for i, image := range images {
		res, err := u.archiveImages.PushImageArchive(ctx, imageRegistryUseCase.PushArchiveInput{UploadedBy: createdBy, Name: image.Device, Archive: indexed[i]})
		if err != nil {
			return nil, withImageFile(err, image.file.Name)
		}
		refs[image.Marker] = res.Reference
		progress.imagePushed(ctx, image.file.Size)
	}
	return refs, nil
}

// imagePinner is the optional part of the image registry that fixes external image references by digest at import
// (imageRegistryUseCase.ImageRegistryUseCase.PinImages). Without it an import stores references as written.
type imagePinner interface {
	PinImages(ctx context.Context, scope imageRegistryUseCase.Scope, images []string) (map[string]string, error)
}

var _ imagePinner = (*imageRegistryUseCase.ImageRegistryUseCase)(nil)

// importPinBudget is the time all the registry lookups of one import may take together.
const importPinBudget = 3 * time.Minute

// imagePinner is the image registry that fixes references by digest: the one wired for archive imports, else the one
// the editor uses; nil when none can (narrow constructions), and references are then stored as written.
func (u *ExerciseUseCase) imagePinner() imagePinner {
	if pinner, ok := u.archiveImages.(imagePinner); ok {
		return pinner
	}
	if pinner, ok := u.images.(imagePinner); ok {
		return pinner
	}
	return nil
}

// importImageScope is the credential scope an import reads registries in: the event it goes into, else, for an update,
// the owner of the exercise it updates (the editor's rule), else the public one of the catalog.
func (u *ExerciseUseCase) importImageScope(ctx context.Context, ownerEventID *uuid.UUID, target ImportTarget) (imageRegistryUseCase.Scope, error) {
	if target.Mode == "update" && target.ExerciseID != uuid.Nil && u.images != nil {
		return u.images.ExerciseImageScope(ctx, target.ExerciseID)
	}
	if ownerEventID != nil {
		id := *ownerEventID
		return imageRegistryUseCase.Scope{EventID: &id}, nil
	}
	return imageRegistryUseCase.Scope{}, nil
}

// pinImportImages fixes the external images of the versions by digest: every device image and own image of a linked
// variant that is a registry reference without a digest becomes name:tag@sha256:hex, looked up once at this moment. Platform
// references (pushed archive images, pinned by construction) and references that name a digest are left alone. An image the
// registry cannot resolve fails the import (ErrImagePinFailed naming it), before anything is created.
func (u *ExerciseUseCase) pinImportImages(ctx context.Context, deadline time.Time, scope imageRegistryUseCase.Scope, versions []exerciseModel.ExerciseVersion) error {
	pinner := u.imagePinner()
	if pinner == nil {
		return nil
	}
	var images []string
	eachImage(versions, func(image *string) {
		if isExternalImage(*image) && !imageRegistryModel.HasDigest(*image) {
			images = append(images, *image)
		}
	})
	if len(images) == 0 {
		return nil
	}
	// One deadline for the whole import: an archive of many images on a slow registry cannot hold the import for minutes.
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	pins, err := pinner.PinImages(ctx, scope, images)
	if err != nil {
		return err
	}
	eachImage(versions, func(image *string) {
		if pinned, found := pins[*image]; found {
			*image = pinned
		}
	})
	return nil
}

// isExternalImage tells a registry reference from an empty image, a platform reference and a pending archive image.
func isExternalImage(image string) bool {
	if image == "" || strings.HasPrefix(image, imageFileMarker) || strings.HasPrefix(image, "platform:") {
		return false
	}
	return true
}

// eachImage calls fn with every device image and every own image of a linked variant of the versions.
func eachImage(versions []exerciseModel.ExerciseVersion, fn func(image *string)) {
	for vi := range versions {
		eachVariantImage(versions[vi].Variants, fn)
	}
}

// eachVariantImage calls fn with every device image and every own image of the variants.
func eachVariantImage(variants []exerciseModel.Variant, fn func(image *string)) {
	for xi := range variants {
		variant := &variants[xi]
		for di := range variant.Topology.Devices {
			fn(&variant.Topology.Devices[di].Image)
		}
		for oi := range variant.ImageOverrides {
			fn(&variant.ImageOverrides[oi].Image)
		}
	}
}

// withImageFile adds the archive path of the image file an error is about (public context "file").
func withImageFile(failure error, file string) error {
	var creator appErr.ErrorCreator
	if !errors.As(failure, &creator) {
		return failure
	}
	return creator.WithPublicContext("file", file).Err()
}

// replaceImageMarkers gives every device and every own image of a linked variant holding an image file marker the
// pushed image's reference.
func replaceImageMarkers(versions []exerciseModel.ExerciseVersion, refs map[string]string) error {
	for vi := range versions {
		for xi := range versions[vi].Variants {
			variant := &versions[vi].Variants[xi]
			images := make([]*string, 0, len(variant.Topology.Devices)+len(variant.ImageOverrides))
			for di := range variant.Topology.Devices {
				images = append(images, &variant.Topology.Devices[di].Image)
			}
			for oi := range variant.ImageOverrides {
				images = append(images, &variant.ImageOverrides[oi].Image)
			}
			for _, image := range images {
				if !strings.HasPrefix(*image, imageFileMarker) {
					continue
				}
				ref, ok := refs[*image]
				if !ok {
					return model.ErrPlatform.WithMessage("An image file of the archive was not pushed").Err()
				}
				*image = ref
			}
		}
	}
	return nil
}
