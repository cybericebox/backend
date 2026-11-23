package exercise

import (
	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	"github.com/cybericebox/backend/internal/model/rbac"
	exerciseUseCase "github.com/cybericebox/backend/internal/useCase/exercise"
)

type (
	// dossierResponse is the dossier of the human defender of a test lab (console design 9a.3). Declared is false when the
	// variant has no defender block (no dossier tab); the lists are then empty. Links are the console cables of the seat, in
	// the order of its serial ports; Forbidden are the devices the defender must not console into; Credentials are the operator
	// credentials with their values (show and copy controls). Forbidden is off_limits when the task names any, else every
	// device with a console port the defender does not operate.
	dossierResponse struct {
		Declared      bool                        `json:"Declared"`
		Links         []dossierLinkResponse       `json:"Links"`
		Forbidden     []string                    `json:"Forbidden"`
		Credentials   []dossierCredentialResponse `json:"Credentials"`
		// Topology is full (the schema view of the whole lab), operates (only Operates and their links) or none.
		Topology string `json:"Topology"`
		// Operates are the devices the defender operates (the subset of Topology operates).
		Operates []string `json:"Operates"`
	}

	// dossierLinkResponse is one cabled line of the seat: SerialPort and its device Path (/dev/ttyCON0), the Device and its
	// ConsolePort at the other end, and ConsoleKind: login (a login prompt), program (Program is the base name of the command,
	// such as vtysh) or native (an emulator's own console).
	dossierLinkResponse struct {
		SerialPort  string `json:"SerialPort"`
		Path        string `json:"Path"`
		Device      string `json:"Device"`
		ConsolePort string `json:"ConsolePort"`
		ConsoleKind string `json:"ConsoleKind"`
		Program     string `json:"Program"`
	}

	// dossierCredentialResponse is one operator credential: a device, optionally a console port, a user and the value.
	dossierCredentialResponse struct {
		Device string `json:"Device"`
		Port   string `json:"Port"`
		User   string `json:"User"`
		Value  string `json:"Value"`
	}
)

func dossierToResponse(v exerciseUseCase.DossierView) dossierResponse {
	out := dossierResponse{
		Declared: v.Declared, Topology: string(v.Topology), Operates: append([]string{}, v.Operates...), Links: []dossierLinkResponse{},
		Forbidden: append([]string{}, v.Forbidden...), Credentials: []dossierCredentialResponse{},
	}
	for _, l := range v.Links {
		out.Links = append(out.Links, dossierLinkResponse{
			SerialPort: l.SerialPort, Path: l.Path, Device: l.Device, ConsolePort: l.ConsolePort, ConsoleKind: string(l.Kind), Program: l.Program,
		})
	}
	for _, c := range v.Credentials {
		out.Credentials = append(out.Credentials, dossierCredentialResponse{Device: c.Device, Port: c.Port, User: c.User, Value: c.Value})
	}
	return out
}

// deployDossier godoc
// @Summary  The dossier of the human defender of a test lab
// @Description  The console map of the defender's seat, the forbidden devices, the operator credentials (with their values) and the topology mode (full, operates, none), as the lab was deployed. The owner of the test lab with author access to the exercise only (checked on every read), never an event preview, once the lab is ready (409 otherwise). Declared false: the lab was deployed without a defender block. Never cached.
// @Tags     exercises
// @Produce  json
// @Param    deployID  path  string  true  "test deployment id"
// @Success  200  {object}  response.Response{data=dossierResponse}
// @Failure  400  {object}  response.Response
// @Failure  404  {object}  response.Response
// @Failure  409  {object}  response.Response
// @Router   /exercises/deploys/{deployID}/dossier [get]
func (h *Handler) deployDossier(ctx *gin.Context) {
	claims, ok := rbac.CurrentUserSessionFromContext(ctx.Request.Context())
	if !ok {
		response.AbortWithUnauthenticated(ctx)
		return
	}
	deployID, err := uuid.FromString(ctx.Param("deployID"))
	if err != nil {
		response.AbortWithBadRequest(ctx)
		return
	}
	// The dossier carries operator credentials.
	ctx.Header("Cache-Control", "private, no-store")
	view, err := h.useCase.TestDeployDossier(ctx, exerciseUseCase.Actor{UserID: claims.UserID, Role: claims.Role}, deployID)
	if err != nil {
		response.AbortWithError(ctx, err)
		return
	}
	response.AbortWithData(ctx, dossierToResponse(view))
}
