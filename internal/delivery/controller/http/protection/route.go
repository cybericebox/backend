package protection

import (
	"github.com/gin-gonic/gin"

	"github.com/cybericebox/backend/internal/authz"
)

// declContextKey holds the authz declaration of the route on the gin context.
const declContextKey = "authz.decl"

// Route declares the keys and the scope source of a route (plan P1b, spec owner decision 1) in front of its legacy
// gate. In P1 the declaration is only recorded on the request (DeclFromContext): legacy, today's gate, still decides
// alone and runs unchanged. A nil legacy is a route without a gate (public flows): the request passes through.
func (p *Protection) Route(decl authz.Decl, legacy gin.HandlerFunc) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Set(declContextKey, decl)
		if legacy != nil {
			legacy(ctx)
		}
	}
}

// DeclFromContext returns the declaration Route recorded for the request.
func DeclFromContext(ctx *gin.Context) (authz.Decl, bool) {
	v, ok := ctx.Get(declContextKey)
	if !ok {
		return authz.Decl{}, false
	}
	d, ok := v.(authz.Decl)
	return d, ok
}
