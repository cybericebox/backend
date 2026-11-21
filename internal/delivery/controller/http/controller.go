// Package http is the HTTP controller: the router, its global middleware chain and the listeners.
package http

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"github.com/cybericebox/daemon/internal/config"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/errjournal"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/handler"
	_ "github.com/cybericebox/daemon/internal/delivery/controller/http/handler/apidocs"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/middleware"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/protection"
	"github.com/cybericebox/daemon/internal/delivery/controller/http/response"
)

type (
	Controller struct {
		server  *Server
		limiter *middleware.RateLimiter
	}

	IUseCase interface {
		// IUseCase is dependencies for the http handler
		handler.IUseCase
		protection.IUseCase
	}

	Dependencies struct {
		UseCase    IUseCase
		Config     *config.HTTPControllerConfig
		AuthConfig config.AuthConfig
		RateLimit  config.RateLimitConfig
		// Inflight bounds the requests served at once; the internal listener shares these slots.
		Inflight config.InflightConfig
		// ErrorJournal receives 5xx, panics, 403, 429 and the 404 counters; nil captures nothing.
		ErrorJournal errjournal.Sink
	}
)

// hardenRouter sets how the client address is found and what every request and response is held to. No
// forwarding header is trusted (gin gets no trusted proxy, so ClientIP is the connection's own address, the same
// one clientaddr.FromRequest returns): the backend is the public entry and a client must not choose its address
// to dodge the per-address limits. Every body is capped.
func hardenRouter(router *gin.Engine, cfg *config.HTTPControllerConfig) error {
	if err := router.SetTrustedProxies(nil); err != nil {
		return err
	}
	router.Use(middleware.BodyLimit(cfg.MaxBodyBytes), middleware.SecurityHeaders)
	return nil
}

// newRouter is gin.New with the one setting authorization depends on: handlers
// hand their *gin.Context to use cases as the context.Context, and the identity
// (rbac.Claims) lives in the REQUEST context. Without ContextWithFallback the
// use cases never see it: every claims-based decision silently takes the "no
// identity" branch (fail-closed today, fail-open the day a branch is written the
// other way round).
func newRouter() *gin.Engine {
	router := gin.New()
	router.ContextWithFallback = true
	return router
}

func NewController(deps Dependencies) *Controller {
	// create the router
	// gin.New, not gin.Default: logger and recovery come from ForMode, per the
	// gin mode config.SetupLogger set from ENV (and so does the route dump).
	router := newRouter()
	router.Use(middleware.ForMode(gin.Mode())...)
	// Inside the recovery (it records a panic and lets it through) and outside the error handler (the status it
	// reads is the one the client gets).
	router.Use(errjournal.Middleware(deps.ErrorJournal))

	if err := hardenRouter(router, deps.Config); err != nil {
		log.Fatal().Err(err).Msg("Invalid HTTP controller configuration")
	}

	// add global middleware for error handling
	router.Use(response.WithErrorHandler)
	router.Use(middleware.CaptureRequestReceivedAt())

	// build protection middleware and wire it into the handler aggregator
	limiter := middleware.NewRateLimiter(deps.RateLimit)
	prot := protection.New(protection.Dependencies{
		UseCase: deps.UseCase,
		Config:  deps.AuthConfig,
		Limiter: limiter,
	})

	// This service answers on exactly one host, api.<domain> — every other
	// host is routed to a frontend by the edge proxy and never reaches here.
	router.Use(prot.RequireAPIHost)

	// optionally serve the Swagger UI
	if deps.Config.EnableSwaggerDocs {
		// redirect /docs -> /docs/index.html (no trailing slash: distinct path segment, avoids
		// conflicting with the /docs/*any catch-all in gin's radix tree)
		router.GET("/docs", func(c *gin.Context) {
			c.Redirect(http.StatusMovedPermanently, "/docs/index.html")
		})
		router.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// Every frontend calls this API cross-origin (its own subdomain) — CORS
	// (with credentials) must run before any route handling.
	originPolicy := newOriginPolicy(deps)
	router.Use(middleware.HandleCORS(originPolicy), middleware.OriginGuardWith(originPolicy),
		// General request limiter (after CORS, so a 429 stays readable by the page): every cookie-less request
		// here, in its route's anonymous bucket; signed-in ones in the permission gate.
		limiter.Anonymous,
		// In-flight limiter: after CORS (its 503 stays readable by the page) and after the origin guard and the
		// anonymous limit (a refused request never takes a slot); before the handlers and the database.
		middleware.NewInflightLimiter(deps.Inflight, prot.SessionKey).Handle,
		// The body type of a write of unknown length (HTTP/3): it reads the body, so it runs after the limiters.
		middleware.BodyTypeGuard,
		middleware.ContentMiddleware)

	RegisterHealth(router)

	// create handler for routes on current service
	handler.NewAPIHandler(deps.UseCase, prot, deps.AuthConfig).Init(router)

	return &Controller{
		server:  NewServer(&deps.Config.Server, deps.Config.HealthBind, router, deps.AuthConfig.Hosts),
		limiter: limiter,
	}
}

// RegisterHealth adds GET /api/health — a public liveness probe (no auth, no
// DB). Session-aware frontend recovery probes /api/auth/me instead.
func RegisterHealth(router gin.IRouter) {
	router.GET("/api/health", func(ctx *gin.Context) {
		response.AbortWithSuccess(ctx)
	})
}

func (c *Controller) Start() {
	c.server.Start()
}

func (c *Controller) Stop(ctx context.Context) {
	c.server.Stop(ctx)
}
