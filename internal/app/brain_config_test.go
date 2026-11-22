package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/useCase"
)

func TestTheBrainSettingsReachTheUseCases(t *testing.T) {
	cfg := &config.Config{}
	cfg.Brain.Endpoint = "brain.example:8099"
	cfg.Brain.GRPCEndpoint = "brain.example:8098"
	cfg.Brain.EnrollmentToken = "token"
	cfg.Brain.CAFile = "/ca.pem"

	var deps useCase.Dependencies
	withBrainConfig(&deps, cfg)

	assert.Equal(t, cfg.Brain, deps.Brain)
}
