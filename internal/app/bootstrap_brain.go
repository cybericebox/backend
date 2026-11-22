package app

import (
	"context"
	"errors"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/config"
	assistantModel "github.com/cybericebox/backend/internal/model/assistant"
)

// brainBootstrapper is the part of the assistant use case the startup needs.
type brainBootstrapper interface {
	BootstrapBrain(ctx context.Context) (bool, error)
}

// bootstrapBrain enrolls the platform at the cyber-brain named by BRAIN_ENDPOINT with BRAIN_ENROLLMENT_TOKEN when no
// connection for that endpoint is stored yet. The token is not used again. A failure is a clear log, never a reason to
// stop the daemon: the assistant routes answer 503 until the connection exists.
func bootstrapBrain(ctx context.Context, cfg config.BrainConfig, brain brainBootstrapper) {
	if cfg.Endpoint == "" {
		return
	}
	if strings.TrimSpace(cfg.GRPCEndpoint) == "" {
		log.Warn().Msg("BRAIN_GRPC_ENDPOINT is empty and no gRPC endpoint is stored with the connection: the assistant chat works, the runs, the outbox and the statistics stay off")
	}
	enrolled, err := brain.BootstrapBrain(ctx)
	switch {
	case errors.Is(err, assistantModel.ErrAssistantUnavailable.Err()):
		log.Warn().Msg("BRAIN_ENDPOINT is set but PLATFORM_SECRETS_KEY is not: the authoring assistant stays off")
	case err != nil:
		log.Error().Err(err).Str("endpoint", cfg.Endpoint).Msg("Cyber-brain enrollment failed: check BRAIN_ENROLLMENT_TOKEN (one-time, expires), BRAIN_CA_FILE and the endpoint; the daemon starts, the authoring assistant answers 503")
	case enrolled:
		log.Info().Str("endpoint", cfg.Endpoint).Msg("Enrolled at cyber-brain from the configuration")
	case strings.TrimSpace(cfg.EnrollmentToken) != "":
		log.Warn().Str("endpoint", cfg.Endpoint).Msg("BRAIN_ENROLLMENT_TOKEN is ignored: the connection is already stored; remove the token from the configuration")
	}
}
