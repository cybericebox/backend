package exercise

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
)

func pinVariantOf(images ...string) exerciseModel.Variant {
	var v exerciseModel.Variant
	for _, image := range images {
		v.Topology.Devices = append(v.Topology.Devices, exerciseModel.Device{Image: image})
	}
	return v
}

func pinImagesOf(v exerciseModel.Variant) []string {
	var out []string
	for _, d := range v.Topology.Devices {
		out = append(out, d.Image)
	}
	return out
}

// A stored image fixed at import (name:tag@sha256) equals the archive's unpinned reference of the same name and tag, so
// importing the same archive again is "unchanged"; a different tag, or a digest the archive names itself, still differs.
func TestAlignUnpinnedImages(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	other := "@sha256:" + strings.Repeat("b", 64)
	platform := "platform:web@sha256:" + strings.Repeat("c", 64)
	source := []exerciseModel.Variant{pinVariantOf("nginx:1.27", "postgres", "app:2", "app:3"+other, "cache:1", platform)}
	target := []exerciseModel.Variant{pinVariantOf("nginx:1.27"+digest, "postgres:latest"+digest, "app:1"+digest, "app:3"+digest, "cache:1", platform)}
	alignUnpinnedImages(source, target)
	require.Equal(t, []string{"nginx:1.27", "postgres", "app:1" + digest, "app:3" + digest, "cache:1", platform}, pinImagesOf(target[0]),
		"only a stored pin of the same name and tag is dropped")
	require.Equal(t, []string{"nginx:1.27", "postgres", "app:2", "app:3" + other, "cache:1", platform}, pinImagesOf(source[0]), "the archive side never changes")
}

func TestAlignUnpinnedImagesOfOwnImages(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	source := []exerciseModel.Variant{{ImageOverrides: []exerciseModel.ImageOverride{{Image: "web:1"}}}}
	target := []exerciseModel.Variant{{ImageOverrides: []exerciseModel.ImageOverride{{Image: "web:1" + digest}}}}
	alignUnpinnedImages(source, target)
	require.Equal(t, "web:1", target[0].ImageOverrides[0].Image)
}
