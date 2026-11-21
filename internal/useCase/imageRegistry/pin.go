package imageRegistryUseCase

import (
	"context"

	imageRegistryModel "github.com/cybericebox/daemon/internal/model/imageRegistry"
	"github.com/cybericebox/daemon/pkg/ociregistry"
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
			return nil, imageRegistryModel.ErrImagePinFailed.WithPublicContext("image", image).WithPublicContext("reason", reason).Err()
		}
		pins[image] = imageRegistryModel.PinnedReference(image, digest)
	}
	return pins, nil
}

// resolveExternalDigest asks the registry of an external reference for the digest of its tag; reason is "" on success.
func (u *ImageRegistryUseCase) resolveExternalDigest(ctx context.Context, scope Scope, image string) (digest, reason string) {
	ref, err := imageRegistryModel.ParseImageRef(image)
	if err != nil || ref.Tag == "" {
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
