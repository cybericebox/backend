package event

import (
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"

	eventExerciseModel "github.com/cybericebox/backend/internal/model/eventExercise"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	resourcesModel "github.com/cybericebox/backend/internal/model/resources"
)

// A team's group goes to an agent that serves console lines when a planned variant of a task has them.
func TestPlanTaskNotesConsoleLines(t *testing.T) {
	policy := resourcesModel.DefaultPolicy()
	link := eventExerciseModel.EventExercise{ExerciseID: uuid.Must(uuid.NewV7()), VariantMode: eventExerciseModel.VariantModePerTeam}
	router := container("r1", "")
	router.ConsolePorts = []exerciseModel.ConsolePort{{Name: "console0"}}
	plain := planTask(policy, link, []exerciseModel.Variant{variant(false, container("web", ""))}, nil)
	cabled := planTask(policy, link, []exerciseModel.Variant{variant(false, container("web", "")), variant(false, router)}, nil)
	assert.False(t, plain.consoleLines)
	assert.True(t, cabled.consoleLines)
	assert.False(t, planInputs{}.placementNeed().ConsoleLines)
	assert.True(t, planInputs{consoleLines: true}.placementNeed().ConsoleLines)
}

// A team's group goes to an agent that shows the kernel log when a planned variant asks for it.
func TestPlanTaskNotesTheKernelLog(t *testing.T) {
	policy := resourcesModel.DefaultPolicy()
	link := eventExerciseModel.EventExercise{ExerciseID: uuid.Must(uuid.NewV7()), VariantMode: eventExerciseModel.VariantModePerTeam}
	router := container("r1", "")
	router.ConsolePorts = []exerciseModel.ConsolePort{{Name: "console0"}}
	plain := planTask(policy, link, []exerciseModel.Variant{variant(false, router)}, nil)
	router.Profile, router.KernelLog = exerciseModel.NodeProfileMicroVM, true
	logged := planTask(policy, link, []exerciseModel.Variant{variant(false, container("web", "")), variant(false, router)}, nil)
	assert.False(t, plain.kernelLog)
	assert.True(t, logged.kernelLog)
	assert.False(t, planInputs{consoleLines: true}.placementNeed().KernelLog)
	assert.True(t, planInputs{consoleLines: true, kernelLog: true}.placementNeed().KernelLog)
}
