package imageRegistryUseCase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// The start configuration of a tools image is read from its registry with the brain's pull credential, and nothing is stored:
// the credential is the run's. Without the credential a private image stays unknown.
func TestToolsImageServiceReadsWithTheBrainCredential(t *testing.T) {
	ctx := context.Background()
	f := newExternalFixture(t, "brain")
	pushConfigImage(t, publicClient(f.hub.host(), "eda/defender", "brain", secretToken), configJSON("linux", "amd64", "eda"), "1")

	service, known := f.uc.ToolsImageService(ctx, "registry.example/eda/defender:1", &exerciseModel.ToolsPull{Registry: "registry.example", Username: "brain", Password: secretToken})
	require.True(t, known)
	require.Equal(t, "eda", service.User)
	require.Equal(t, []string{"/app"}, service.Entrypoint)
	require.Empty(t, f.store.rows, "nothing is stored")

	// A credential for another registry is not sent: the private image is read anonymously and stays unknown.
	_, known = f.uc.ToolsImageService(ctx, "registry.example/eda/defender:1", &exerciseModel.ToolsPull{Registry: "other.example", Username: "brain", Password: secretToken})
	require.False(t, known, "the credential goes only to its own registry")
	_, known = f.uc.ToolsImageService(ctx, "registry.example/eda/defender:1", &exerciseModel.ToolsPull{Registry: "https://registry.example", Username: "brain", Password: secretToken})
	require.True(t, known, "the registry is normalised as the lab's pull secret is")

	_, known = f.uc.ToolsImageService(ctx, "registry.example/eda/defender:1", nil)
	require.False(t, known, "a private image without the credential")
	_, known = f.uc.ToolsImageService(ctx, "not a reference", nil)
	require.False(t, known)
}
