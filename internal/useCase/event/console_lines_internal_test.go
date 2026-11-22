package event

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	eventLabModel "github.com/cybericebox/backend/internal/model/eventLab"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// consoleInfra is an infrastructure that only answers the console-line question.
type consoleInfra struct {
	Infrastructure
	asked int
}

func (c *consoleInfra) RequireConsoleLines(topo exerciseModel.Topology) error {
	c.asked++
	if topo.UsesConsoleLines() {
		return exerciseModel.ErrConsoleUnavailable.Err()
	}
	return nil
}

// The pre-flight of a stand asks for the console lines whatever the snapshot mode, before anything else.
func TestValidateLabPreparation_RefusesConsoleLinesEarly(t *testing.T) {
	infra := &consoleInfra{}
	u := &EventUseCase{infra: infra}
	topo := exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "pc", Type: exerciseModel.DeviceTypeNode, SerialPorts: []exerciseModel.SerialPort{{Name: "serial0"}}}}}
	err := u.validateLabPreparation(context.Background(), "g", eventLabModel.Policy{}, topo)
	require.ErrorIs(t, err, exerciseModel.ErrConsoleUnavailable.Err())
	plain := exerciseModel.Topology{Devices: []exerciseModel.Device{{Name: "pc", Type: exerciseModel.DeviceTypeNode}}}
	_ = u.validateLabPreparation(context.Background(), "g", eventLabModel.Policy{}, plain) // the snapshot rules answer the rest
	require.Equal(t, 2, infra.asked, "asked for every topology")
}
