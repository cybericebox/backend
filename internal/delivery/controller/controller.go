package controller

import (
	"context"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/controller/http"
	"github.com/cybericebox/backend/internal/delivery/controller/http/errjournal"
)

type (
	Controller struct {
		httpController *http.Controller
	}

	IUseCase interface {
		http.IUseCase
	}

	Dependencies struct {
		UseCase              IUseCase
		HTTPControllerConfig config.HTTPControllerConfig
		AuthConfig           config.AuthConfig
		RateLimit            config.RateLimitConfig
		Inflight             config.InflightConfig
		// ErrorJournal captures HTTP errors; nil captures nothing.
		ErrorJournal errjournal.Sink
	}
)

func NewController(deps Dependencies) *Controller {
	return &Controller{
		httpController: http.NewController(
			http.Dependencies{
				Config:       &deps.HTTPControllerConfig,
				UseCase:      deps.UseCase,
				AuthConfig:   deps.AuthConfig,
				RateLimit:    deps.RateLimit,
				Inflight:     deps.Inflight,
				ErrorJournal: deps.ErrorJournal,
			},
		),
	}
}

func (c *Controller) Start() {
	c.httpController.Start()
}

func (c *Controller) Stop(ctx context.Context) {
	c.httpController.Stop(ctx)
}
