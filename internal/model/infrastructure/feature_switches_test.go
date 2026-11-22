package infrastructure_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
)

func TestRequireLabOptionsNamesTheSwitchesThatAreOff(t *testing.T) {
	off := false
	f := infraModel.AgentFeatures{
		VPN: &infraModel.VPNFeature{Enabled: &off}, Internet: &infraModel.InternetFeature{},
		Access:            &infraModel.AccessFeature{},
		AllowedRegistries: &infraModel.AllowedRegistriesFeature{Registries: []string{"docker.io"}},
	}
	err := f.RequireLabOptions(exerciseModel.LabNeeds{
		VPN: true, Internet: true, HTTP: true, Terminal: true, RDP: true, VNC: true, Registries: []string{"docker.io", "gcr.io"},
	})
	require.ErrorIs(t, err, exerciseModel.ErrLabOptionUnavailable.Err())
	for _, want := range []string{"vpn", "internet", "http", "terminal", "rdp", "vnc", "registry gcr.io"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "registry docker.io")
	assert.True(t, infraModel.IsPlanningError(err))
}

func TestRequireLabOptionsLetsWhatTheClusterServesThrough(t *testing.T) {
	on := true
	needs := exerciseModel.LabNeeds{VPN: true, Internet: true, HTTP: true, RDP: true, VNC: true, Registries: []string{"gcr.io"}}
	for name, f := range map[string]infraModel.AgentFeatures{
		"all on": {
			VPN: &infraModel.VPNFeature{Enabled: &on}, Internet: &infraModel.InternetFeature{Enabled: true},
			Access:            &infraModel.AccessFeature{HTTP: true, RDP: true, VNC: true},
			AllowedRegistries: &infraModel.AllowedRegistriesFeature{Any: true},
		},
		"the listed registry": {
			Access:            &infraModel.AccessFeature{HTTP: true, RDP: true, VNC: true},
			AllowedRegistries: &infraModel.AllowedRegistriesFeature{Registries: []string{"gcr.io"}},
		},
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, f.RequireLabOptions(needs)) })
	}
	// An older agent reports none of the 2.8 fields: nothing is held against it except the accesses it never reported.
	older := infraModel.AgentFeatures{Access: &infraModel.AccessFeature{HTTP: true, RDP: true, VNC: true}}
	require.NoError(t, older.RequireLabOptions(needs))
}
