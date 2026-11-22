package exercise

import (
	"errors"
	"testing"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

func TestTheToolsDeviceIsSizedFromItsHintWithinTheDeviceLimits(t *testing.T) {
	u := &ExerciseUseCase{}
	got, err := u.sizeTools(exerciseModel.ToolsPlacement{CPUMillis: 500, MemoryBytes: 2 << 30})
	if err != nil || got.ResourcePreset != "large" {
		t.Fatalf("a hint above the frame is clamped to it: %q %v", got.ResourcePreset, err)
	}
	if got, err = u.sizeTools(exerciseModel.ToolsPlacement{}); err != nil || got.ResourcePreset != "" {
		t.Fatalf("no hint: the default preset: %q %v", got.ResourcePreset, err)
	}
	if _, err = u.sizeTools(exerciseModel.ToolsPlacement{CPUMillis: 4000, MemoryBytes: 16 << 30}); !errors.Is(err, exerciseModel.ErrToolsTooLarge) {
		t.Fatalf("a hint the lab cannot hold at a workable size is refused: %v", err)
	}
}

func TestTheToolsPullCredentialJoinsTheLabsCredentials(t *testing.T) {
	lab := []infraModel.RegistryCredential{{Registry: "ghcr.io", Username: "a", Password: "p"}}
	got, err := withToolsPull(lab, &exerciseModel.ToolsPull{Registry: "https://registry.brain.test", Username: "bot", Password: "short"})
	if err != nil || len(got) != 2 || got[1] != (infraModel.RegistryCredential{Registry: "registry.brain.test", Username: "bot", Password: "short"}) || len(lab) != 1 {
		t.Fatalf("one credential per host, added for this lab only: %v %v", got, err)
	}
	if got, err = withToolsPull(lab, nil); err != nil || len(got) != 1 {
		t.Fatalf("no credential: the lab's own: %v %v", got, err)
	}
	if _, err = withToolsPull(lab, &exerciseModel.ToolsPull{Registry: "ghcr.io", Username: "b", Password: "q"}); !errors.Is(err, exerciseModel.ErrToolsPullConflict) {
		t.Fatalf("another credential for a host the lab pulls from: %v", err)
	}
}
