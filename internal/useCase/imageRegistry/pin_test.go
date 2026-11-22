package imageRegistryUseCase

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	imageRegistryModel "github.com/cybericebox/backend/internal/model/imageRegistry"
)

func pinContext(t *testing.T, err error) map[string]any {
	t.Helper()
	var carrier interface{ PublicContext() map[string]any }
	require.ErrorAs(t, err, &carrier)
	return carrier.PublicContext()
}

// hexOf is a 64-character hex string of one digit.
func hexOf(digit string) string { return strings.Repeat(digit, 64) }

// A tag is fixed to the digest the registry serves now, the human tag kept: name:tag@sha256.
func TestPinImagesFixesATagToItsDigest(t *testing.T) {
	ctx := context.Background()
	f := newExternalFixture(t, "")
	digest := pushConfigImage(t, publicClient(f.hub.host(), "acme/app", "", ""), configJSON("linux", "amd64", "app"), "1")

	pins, err := f.uc.PinImages(ctx, Scope{}, []string{"registry.example/acme/app:1", "registry.example/acme/app:1"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"registry.example/acme/app:1": "registry.example/acme/app:1@" + digest}, pins)
	require.Equal(t, 1, f.hub.headCount(), "one request per distinct image")

	// The tag is moved later: the pin made before does not change, a new import pins the new image.
	moved := pushConfigImage(t, publicClient(f.hub.host(), "acme/app", "", ""), configJSON("linux", "amd64", "other"), "1")
	require.NotEqual(t, digest, moved)
	again, err := f.uc.PinImages(ctx, Scope{}, []string{"registry.example/acme/app:1"})
	require.NoError(t, err)
	require.Equal(t, "registry.example/acme/app:1@"+moved, again["registry.example/acme/app:1"])
}

// A reference that has a digest, a platform reference and an empty image are not looked up and not in the answer.
func TestPinImagesSkipsWhatIsAlreadyFixed(t *testing.T) {
	f := newExternalFixture(t, "")
	pinned := "registry.example/acme/app:1@sha256:" + hexOf("b")
	pins, err := f.uc.PinImages(context.Background(), Scope{}, []string{pinned, "platform:web@sha256:" + hexOf("c"), ""})
	require.NoError(t, err)
	require.Empty(t, pins)
	require.Zero(t, f.hub.headCount())
}

func TestPinImagesRefusesAnImageItCannotResolve(t *testing.T) {
	ctx := context.Background()
	f := newExternalFixture(t, "")
	_, err := f.uc.PinImages(ctx, Scope{}, []string{"registry.example/acme/missing:1"})
	require.Error(t, err)
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	require.Equal(t, "registry.example/acme/missing:1", pinContext(t, err)["image"])
	require.Equal(t, ServiceNotFound, pinContext(t, err)["reason"])

	f.hub.srv.Close()
	_, err = f.uc.PinImages(ctx, Scope{}, []string{"registry.example/acme/app:1"})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	require.Equal(t, ServiceUnreachable, pinContext(t, err)["reason"])
}

// A private image is refused in the catalog (no credential) and pinned with the event's own registry secret.
func TestPinImagesUsesTheEventSecretOfAPrivateImage(t *testing.T) {
	ctx := context.Background()
	f := newExternalFixture(t, "bob")
	digest := pushConfigImage(t, publicClient(f.hub.host(), "acme/app", "bob", secretToken), configJSON("linux", "amd64", "x"), "1")
	f.q.EXPECT().ListEventRegistrySecrets(gomock.Any(), eventA).Return([]postgres.EventRegistrySecret{
		f.row(t, secID, "registry.example", "acme", secretToken),
	}, nil).AnyTimes()
	image := "registry.example/acme/app:1"

	_, err := f.uc.PinImages(ctx, Scope{}, []string{image})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	require.Equal(t, ServiceUnauthorized, pinContext(t, err)["reason"])

	pins, err := f.uc.PinImages(ctx, eventScope(eventA), []string{image})
	require.NoError(t, err)
	require.Equal(t, image+"@"+digest, pins[image])
}

// The reference is the author's: the default client never speaks plain http or dials an internal address.
func TestPinImagesDoesNotDialInternalAddresses(t *testing.T) {
	f := newAccessFixture(t, true, func(d *Dependencies) { d.NewExternalClient = DefaultExternalClient })
	loopback := newTestRegistry(t, "", "")
	pushConfigImage(t, publicClient(loopback.host(), "acme/app", "", ""), configJSON("linux", "amd64", "app"), "1")

	_, err := f.uc.PinImages(context.Background(), Scope{}, []string{loopback.host() + "/acme/app:1"})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	require.Zero(t, loopback.headCount())
}

// The registry secret of an event goes only to the registry host (and repository prefix) it was stored for: an archive naming
// another host, or another repository of the same host, is looked up anonymously and its server never sees a credential.
func TestPinImagesSendsTheEventSecretOnlyWhereItWasStored(t *testing.T) {
	ctx := context.Background()
	f := newExternalFixture(t, "bob")
	var mu sync.Mutex
	var seen []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="other"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(other.Close)
	f.hosts["other.example"] = strings.TrimPrefix(other.URL, "http://")
	pushConfigImage(t, publicClient(f.hub.host(), "acme/app", "bob", secretToken), configJSON("linux", "amd64", "x"), "1")
	pushConfigImage(t, publicClient(f.hub.host(), "elsewhere/app", "bob", secretToken), configJSON("linux", "amd64", "x"), "1")
	f.q.EXPECT().ListEventRegistrySecrets(gomock.Any(), eventA).Return([]postgres.EventRegistrySecret{
		f.row(t, secID, "registry.example", "acme", secretToken),
	}, nil).AnyTimes()

	for _, image := range []string{
		"other.example/acme/app:1",         // another host with the same repository path
		"registry.example/elsewhere/app:1", // the secret's host, outside its repository prefix
		"registry.example:443/acme/app:1",  // the same name with a port is another registry
		"other.example/registry.example/acme/app:1",
	} {
		_, err := f.uc.PinImages(ctx, eventScope(eventA), []string{image})
		require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err(), image)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, auth := range seen {
		require.Empty(t, auth, "no credential reaches a host the secret was not stored for")
	}
}

// A reference whose repository has upper case letters is no reference a registry or a runtime takes: it is refused, not looked up
// under its lower-case name and stored as written. A very long reference is named in the error cut short.
func TestPinImagesRefusesUpperCaseRepositoriesAndCutsLongNames(t *testing.T) {
	ctx := context.Background()
	f := newExternalFixture(t, "")
	pushConfigImage(t, publicClient(f.hub.host(), "acme/app", "", ""), configJSON("linux", "amd64", "x"), "1")

	_, err := f.uc.PinImages(ctx, Scope{}, []string{"registry.example/Acme/App:1"})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	require.Equal(t, ServiceUnsupported, pinContext(t, err)["reason"])
	require.Zero(t, f.hub.headCount())

	long := "registry.example/acme/" + strings.Repeat("x", 5000) + ":1"
	_, err = f.uc.PinImages(ctx, Scope{}, []string{long})
	require.ErrorIs(t, err, imageRegistryModel.ErrImagePinFailed.Err())
	shown, _ := pinContext(t, err)["image"].(string)
	require.NotEmpty(t, shown)
	require.LessOrEqual(t, len(shown), 300)
	require.True(t, strings.HasPrefix(long, strings.TrimSuffix(shown, "…")))
}
