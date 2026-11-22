package assistant

import (
	"net/http"

	"github.com/cybericebox/backend/internal/model"
	"github.com/cybericebox/backend/pkg/err"
)

// next free detail code: 18

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
)
