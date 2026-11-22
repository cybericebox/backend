package imageRegistryUseCase

import (
	"context"
	"strings"
	"unicode/utf8"

	imageRegistryModel "github.com/cybericebox/backend/internal/model/imageRegistry"
	"github.com/cybericebox/backend/pkg/ociregistry"
)

// PinImages fixes the images of an import by digest: for every distinct external reference that names none, one HEAD of
// its manifest (a GET and a hash when the registry sends no usable digest) gives the digest of the manifest or index the
// tag points at now, and the answer maps the reference as written to name:tag@sha256:hex. A reference that already has a
// digest, a platform reference (it is pinned by construction) and an empty image are not looked up and are not in the
// answer. The registry is read anonymously, and with the matching registry secret of the scope's event when that is
// refused, as the availability checks do. One image that cannot be resolved fails the whole call with ErrImagePinFailed
// naming it ("image") and why ("reason"): an import is all or nothing.
func (u *ImageRegistryUseCase) PinImages(ctx context.Context, scope Scope, images []string) (map[string]string, error) {
	pins := map[string]string{}
	for _, image := range images {
		if _, done := pins[image]; done || image == "" || imageRegistryModel.HasDigest(image) {
			continue
		}
		if _, isPlatform := imageRegistryModel.ParsePlatformRef(image); isPlatform {
			continue
		}
		digest, reason := u.resolveExternalDigest(ctx, scope, image)
		if reason != "" {
			return nil, imageRegistryModel.ErrImagePinFailed.WithPublicContext("image", shownImage(image)).WithPublicContext("reason", reason).Err()
		}
		pins[image] = imageRegistryModel.PinnedReference(image, digest)
	}
	return pins, nil
}

// resolveExternalDigest asks the registry of an external reference for the digest of its tag; reason is "" on success.
func (u *ImageRegistryUseCase) resolveExternalDigest(ctx context.Context, scope Scope, image string) (digest, reason string) {
	ref, err := imageRegistryModel.ParseImageRef(image)
	if err != nil || ref.Tag == "" || upperCaseRepository(image) {
		return "", ServiceUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, u.callTimeout)
	defer cancel()
	digest, _, err = readExternal(u, ctx, scope, image, ref, func(hctx context.Context, client *ociregistry.Client) (string, error) {
		return client.ResolveDigest(hctx, ref.Tag)
	})
	if err != nil {
		return "", reasonOfRegistryError(err)
	}
	return digest, ""
}

// maxShownImage bounds the reference named in the error: it is the archive author's text, of any length.
const maxShownImage = 256

// shownImage is image cut to maxShownImage bytes (on a character boundary, "…" marking the cut).
func shownImage(image string) string {
	if len(image) <= maxShownImage {
		return image
	}
	cut := maxShownImage
	for cut > 0 && !utf8.RuneStart(image[cut]) {
		cut--
	}
	return image[:cut] + "…"
}

// upperCaseRepository reports whether the repository path of a reference has an upper case letter. The registry client looks
// the lower-case name up, so such a reference would be pinned and stored as written although no registry or runtime takes it.
func upperCaseRepository(image string) bool {
	name, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	if host, rest, ok := strings.Cut(name, "/"); ok && (strings.ContainsAny(host, ".:") || host == "localhost") {
		name = rest
	}
	return name != strings.ToLower(name)
}
