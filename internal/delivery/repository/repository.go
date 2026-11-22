package repository

import (
	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
)

type (
	Repository struct {
		*postgres.PostgresRepository
	}

	Dependencies struct {
		PostgresConfig *config.PostgresConfig
	}
)

func NewRepository(deps Dependencies) *Repository {
	return &Repository{
		postgres.NewRepository(postgres.Dependencies{Config: deps.PostgresConfig}),
	}
}

func (r *Repository) Close() {
	r.PostgresRepository.Close()
}
