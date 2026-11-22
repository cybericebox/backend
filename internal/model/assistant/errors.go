package assistant

import (
	"net/http"

	"github.com/cybericebox/backend/internal/model"
	"github.com/cybericebox/backend/pkg/err"
)

// next free detail code: 26

var (
	// ErrAssistantUnavailable: the assistant is off (no BRAIN_ENDPOINT), not enrolled yet, or the platform secrets
	// key that seals its private key is not configured.
	ErrAssistantUnavailable = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The authoring assistant is not available").
				WithDetailCode(1).
				WithHTTPCode(http.StatusServiceUnavailable)

	// ErrAssistantBrainUnreachable: the brain did not answer (network, TLS, timeout). Transient.
	ErrAssistantBrainUnreachable = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The authoring assistant service is unreachable").
					WithDetailCode(2).
					WithHTTPCode(http.StatusServiceUnavailable)

	// ErrAssistantUpstream: the brain answered with a failure the platform has no typed meaning for (a 5xx, an
	// unknown code, a refusal of the platform's own credentials).
	ErrAssistantUpstream = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The authoring assistant service failed").
				WithDetailCode(3).
				WithHTTPCode(http.StatusBadGateway)

	// ErrAssistantRejected: the brain refused the request as malformed or not acceptable (400, 415, 422).
	ErrAssistantRejected = err.ErrInvalidData.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The authoring assistant did not accept the request").
				WithDetailCode(4)

	// ErrAssistantNotFound: the session or the attachment is unknown to the brain (404).
	ErrAssistantNotFound = err.ErrObjectNotFound.WithObjectCode(model.AssistantObjectCode).
				WithMessage("Assistant session not found").
				WithDetailCode(5)

	// ErrAssistantNotReady: the session is not in the right state for the call (409), for example it has no
	// archive yet.
	ErrAssistantNotReady = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The assistant session is not in the right state for this action").
				WithDetailCode(6)

	// ErrAssistantGone: the session is gone for good (410), for example it expired.
	ErrAssistantGone = err.ErrObjectNotFound.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The assistant session is over").
				WithDetailCode(7).
				WithHTTPCode(http.StatusGone)

	// ErrAssistantForbidden: the brain refused the call for this user (403).
	ErrAssistantForbidden = err.ErrForbidden.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The assistant refused the request").
				WithDetailCode(8)

	// ErrAssistantImportFailed: the background import of the finished task failed.
	ErrAssistantImportFailed = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The finished task could not be imported").
					WithDetailCode(9)

	// ErrAssistantImportPending: the background import of the finished task is still running; it keeps running,
	// and saving can be asked for again.
	ErrAssistantImportPending = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The finished task is still being imported").
					WithDetailCode(10)

	// ErrAssistantStatsInvalid: an unknown statistics source or mode.
	ErrAssistantStatsInvalid = err.ErrInvalidData.WithObjectCode(model.AssistantObjectCode).
					WithMessage("Unknown statistics source or mode").
					WithDetailCode(11)

	// ErrAssistantStatsEventNotFound: the event whose statistics mode is set does not exist.
	ErrAssistantStatsEventNotFound = err.ErrObjectNotFound.WithObjectCode(model.AssistantObjectCode).
					WithMessage("Event not found").
					WithDetailCode(12)

	// ErrAssistantRunNotFound: the run does not exist (or belongs to another variant).
	ErrAssistantRunNotFound = err.ErrObjectNotFound.WithObjectCode(model.AssistantObjectCode).
				WithMessage("Assistant run not found").
				WithDetailCode(13)

	// ErrAssistantRunActive: the variant already has an active run; a variant has at most one.
	ErrAssistantRunActive = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The variant already has an active run").
				WithDetailCode(14)

	// ErrAssistantRunLimit: the user already has the most active runs they may have.
	ErrAssistantRunLimit = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
				WithMessage("Too many active runs").
				WithDetailCode(15)

	// ErrAssistantOpponentNotFound: the variant has no opponent at that position of its manifest.
	ErrAssistantOpponentNotFound = err.ErrObjectNotFound.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The variant has no such opponent").
					WithDetailCode(16)

	// ErrAssistantOpponentUnavailable: the platform cannot run an opponent here (no tools image is configured, or the lab
	// has no VPN network with DHCP for the tools device).
	ErrAssistantOpponentUnavailable = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("Opponents cannot be run in this lab").
					WithDetailCode(17).
					WithHTTPCode(http.StatusServiceUnavailable)

	// ErrAssistantSecretValue: the finished task carries the value of a secret variable. The brain never sets secret values (they are
	// entered in the catalog), so such an archive is refused, for a new task and for an edited one alike.
	ErrAssistantSecretValue = err.ErrInvalidData.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The finished task carries a secret value, which the assistant never sets").
				WithDetailCode(18)

	// ErrAssistantEditForbidden: the finished task is the edit of a task the user may no longer edit (or whose flags they may not
	// see). It is refused, never saved as a new task.
	ErrAssistantEditForbidden = err.ErrForbidden.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The task this edit belongs to can no longer be edited by you").
					WithDetailCode(19)

	// ErrAssistantEditGone: the finished task is the edit of a task that no longer exists. It is refused, never saved as a new task.
	ErrAssistantEditGone = err.ErrObjectNotFound.WithObjectCode(model.AssistantObjectCode).
				WithMessage("The task this edit belongs to no longer exists").
				WithDetailCode(20)

	// ErrAssistantEditArchived: the finished task is the edit of a task that was archived since. It is refused, never saved as a
	// new task.
	ErrAssistantEditArchived = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The task this edit belongs to is archived").
					WithDetailCode(21)

	// ErrAssistantOpponentTooLarge: the tools device the brain sized for the opponent does not fit the lab's device limits at a
	// workable size, so the run is refused instead of running starved.
	ErrAssistantOpponentTooLarge = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The opponent does not fit the device limits of the lab").
					WithDetailCode(22)

	// ErrAssistantOpponentSpecInvalid: the brain answered GetOpponent with a spec the platform cannot deploy (no image, an
	// input it cannot fill, an expired pull credential).
	ErrAssistantOpponentSpecInvalid = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The brain gave an opponent the platform cannot deploy").
					WithDetailCode(23).
					WithHTTPCode(http.StatusBadGateway)

	// ErrAssistantOpponentControlName: the task has a device of its own named like a control gateway the platform adds for the
	// opponents' tools devices ("ctl", "ctl-2" ... "ctl-8"), so the opponents cannot be given their control. Renaming the
	// device fixes it (newer versions refuse such a name when they are saved).
	ErrAssistantOpponentControlName = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
					WithMessage("The task has a device named like a control gateway the platform adds for opponents").
					WithDetailCode(24)

	// ErrAssistantOpponentGatewayLimit: every opponent's tools device gets a control gateway of its own, and with the task's own
	// gateways they would be more than a lab may have (limit: the most gateways of a lab). Fewer opponents at once fit.
	ErrAssistantOpponentGatewayLimit = err.ErrConflict.WithObjectCode(model.AssistantObjectCode).
						WithMessage("The lab cannot hold a control gateway for every opponent: a lab has a limit of gateways").
						WithDetailCode(25)
)
