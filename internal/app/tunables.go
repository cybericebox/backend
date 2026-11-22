package app

import (
	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/internal/delivery/controller/http/download"
	"github.com/cybericebox/backend/internal/delivery/controller/http/sse"
	challengeAttempt "github.com/cybericebox/backend/internal/model/challengeAttempt"
	eventConfigModel "github.com/cybericebox/backend/internal/model/eventConfig"
	eventContentModel "github.com/cybericebox/backend/internal/model/eventContent"
	eventLabModel "github.com/cybericebox/backend/internal/model/eventLab"
	eventStandModel "github.com/cybericebox/backend/internal/model/eventStand"
	mailModel "github.com/cybericebox/backend/internal/model/mail"
	mediaModel "github.com/cybericebox/backend/internal/model/media"
	authUseCase "github.com/cybericebox/backend/internal/useCase/auth"
	eventUseCase "github.com/cybericebox/backend/internal/useCase/event"
	mailUseCase "github.com/cybericebox/backend/internal/useCase/mail"
	emailUseCase "github.com/cybericebox/backend/internal/useCase/notification/channels/email"
)

// applyTunables hands the operator-set limits and timings to the packages that own them. It runs once
// at start, before anything serves.
func applyTunables(t config.TunablesConfig) {
	eventStandModel.DeployTimeout = t.EventStandDeployTimeout
	eventStandModel.RevealTimeout = t.EventLabRevealTimeout
	sse.SetMaxLifetime(t.SSEMaxLifetime)
	download.SetLongOperationTimeout(t.LongOperationTimeout)
	mediaModel.LongOperationTimeout = t.LongOperationTimeout
	eventContentModel.LiveScreenLinkMaxTTL = t.LiveScreenLinkMaxTTL
	eventConfigModel.SetDefaultMaxTeamSize(t.EventDefaultMaxTeamSize)
	p := eventLabModel.Policy{SnapshotMode: t.EventLabSnapshotMode, RetentionMinutes: t.EventLabRetentionMinutes}
	if t.EventLabMaxActive > 0 {
		n := t.EventLabMaxActive
		p.MaxActiveLabsPerTeam = &n
	}
	eventLabModel.SetDefaultPolicy(p)

	mailUseCase.ConfigureLimiter(mailUseCase.LimiterTimings{
		MaxRateWait: t.MailMaxRateWait, QuotaRetryAfter: t.MailQuotaRetryAfter,
		QuotaRecheck: t.MailQuotaRecheck, QuotaWindow: t.MailQuotaWindow,
	})
	mailModel.ConfigureLimitCaps(t.MailMaxPerSecondLimit, t.MailDailyQuotaLimit)

	authUseCase.MaxAvatarBytes = t.AvatarMaxBytes
	challengeAttempt.MaxAnswerBytes = t.FlagAnswerMaxBytes
	mediaModel.MaxImagePixels = t.ImageMaxPixels
	eventUseCase.ConfigureUploadLimits(eventUseCase.UploadLimits{
		Logo: int64(t.EventLogoMaxBytes), PreviewPicture: int64(t.EventPreviewPictureMaxBytes),
		ContentImage: int64(t.EventContentImageMaxBytes), LiveLogo: int64(t.LiveLogoMaxBytes),
	})
	emailUseCase.ConfigureTemplateImages(emailUseCase.TemplateImageLimits{
		UploadBytes: t.EmailImageUploadMaxBytes, Bytes: t.EmailImageMaxBytes,
		Width: t.EmailImageMaxWidth, Pixels: t.EmailImageMaxPixels,
	})
}
