package app

import (
	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/useCase"
)

// withBrainConfig hands the cyber-brain settings (BRAIN_*) to the use cases. Without them the assistant use case sees no
// endpoint: the enrollment is skipped and the chat, the runs, the outbox and the statistics stay off.
func withBrainConfig(deps *useCase.Dependencies, cfg *config.Config) {
	deps.Brain = cfg.Brain
}
