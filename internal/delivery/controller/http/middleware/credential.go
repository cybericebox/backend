package middleware

import "github.com/gin-gonic/gin"

// credentialKey holds the credentials a layer checked for the request, by scope source ("service(render)").
const credentialKey = "authz.credential"

// MarkCredentialChecked records that the credential of a service or bearer route passed its layer (the render secret,
// a live-screen link). A route declared with that scope source (authz.ServiceRoute, authz.BearerRoute) has no user
// gate: the enforcement (plan P2) admits it only when its credential layer left this mark.
func MarkCredentialChecked(ctx *gin.Context, scope string) {
	checked, _ := ctx.Get(credentialKey)
	set, _ := checked.(map[string]bool)
	if set == nil {
		set = map[string]bool{}
	}
	set[scope] = true
	ctx.Set(credentialKey, set)
}

// CredentialChecked reports whether a layer marked the credential of scope as checked for the request.
func CredentialChecked(ctx *gin.Context, scope string) bool {
	checked, _ := ctx.Get(credentialKey)
	set, _ := checked.(map[string]bool)
	return set[scope]
}
