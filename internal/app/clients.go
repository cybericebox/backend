package app

import (
	"github.com/rs/zerolog/log"

	"github.com/cybericebox/backend/internal/config"
	"github.com/cybericebox/backend/pkg/oauth"
	"github.com/cybericebox/backend/pkg/password"
	"github.com/cybericebox/backend/pkg/secret"
	"github.com/cybericebox/backend/pkg/storage"
	"github.com/cybericebox/backend/pkg/token"
)

// clients hold the infra wired before the aggregator.
type clients struct {
	oauthClient    *oauth.Client   // nil when OAuth is unconfigured (dev path)
	storageClient  *storage.Client // nil when storage is unconfigured (dev path)
	tokenClient    *token.Client
	passwordClient *password.Client
	exerciseCipher *secret.Cipher // nil when EXERCISE_SECRETS_KEY is unset
	vpnCipher      *secret.Cipher // nil when VPN_SECRETS_KEY is unset
	platformCipher *secret.Cipher // nil when PLATFORM_SECRETS_KEY is unset
	filesCipher    *secret.Cipher // nil when FILES_SECRETS_KEY (or the master key) is unset
}

func setupClients(cfg *config.Config) *clients {
	cls := &clients{}
	var err error
	// SMTP_* is only the bootstrap transport: the mail use case resolves the
	// platform/Event SMTP from the database on every send.
	if cfg.Infrastructure.SMTP.Host == "" || cfg.Infrastructure.SMTP.SenderEmail == "" {
		log.Warn().Msg("SMTP_* unset — email is sent only once platform mail settings are saved in the admin")
	}

	cls.oauthClient, err = oauth.New(
		oauth.Config{
			Google: oauth.ClientConfig{
				ClientID:     cfg.Auth.OAuth.Google.ClientID,
				ClientSecret: cfg.Auth.OAuth.Google.ClientSecret,
			},
			RedirectURLTemplate: cfg.Auth.OAuth.RedirectURLTemplate,
			StateKeys:           cfg.Auth.OAuth.StateKeys,
			StateSignature:      cfg.Auth.OAuth.StateSignature,
			StateTTL:            cfg.Auth.OAuth.StateTTL,
		},
	)
	if err != nil {
		log.Warn().Err(err).Msg("OAuth unconfigured — Google sign-in disabled")
	}

	if cfg.Infrastructure.Storage.Endpoint != "" {
		cls.storageClient, err = storage.New(
			storage.Config{
				Endpoint:  cfg.Infrastructure.Storage.Endpoint,
				AccessKey: cfg.Infrastructure.Storage.AccessKey,
				SecretKey: cfg.Infrastructure.Storage.SecretKey,
				Bucket:    cfg.Infrastructure.Storage.Bucket,
				Region:    cfg.Infrastructure.Storage.Region,
				UseSSL:    cfg.Infrastructure.Storage.UseSSL,
			},
		)
		if err != nil {
			log.Fatal().Err(err).Msg("Storage configured but unreachable")
		}
	} else {
		log.Warn().Msg("Storage unconfigured — avatar upload/serving disabled")
	}

	cls.tokenClient = token.MustNew(
		token.Config{
			Keys:           cfg.Auth.SetupTokenKeys,
			TokenSignature: cfg.Auth.TokenSignature,
			SetupTokenTTL:  cfg.Auth.SetupTokenTTL,
		},
	)

	cls.passwordClient = password.New(password.Config{
		Complexity: password.ComplexityConfig{
			MinLength:            cfg.Auth.Password.MinLength,
			MaxLength:            cfg.Auth.Password.MaxLength,
			MinCapitalLetters:    cfg.Auth.Password.MinCapitalLetters,
			MinSmallLetters:      cfg.Auth.Password.MinSmallLetters,
			MinDigits:            cfg.Auth.Password.MinDigits,
			MinSpecialCharacters: cfg.Auth.Password.MinSpecialCharacters,
		},
	})

	cls.exerciseCipher = mustCipher("EXERCISE_SECRETS_KEY", cfg.Exercise.SecretsKey, "exercise secret env vars")
	cls.filesCipher = mustCipher("FILES_SECRETS_KEY", cfg.Files.SecretsKey, "file encryption (uploads, task archives, avatars, answer files)")
	cls.vpnCipher = mustCipher("VPN_SECRETS_KEY", cfg.VPN.SecretsKey, "VPN config storage (test deploys and event VPN access)")
	cls.platformCipher = mustCipher("PLATFORM_SECRETS_KEY", cfg.Platform.SecretsKey, "platform settings secrets (SMTP password)")

	// Every object the backend stores is encrypted (except public images), so a bucket without the key to wrap file keys
	// would store nothing or, worse, store plain: it does not start.
	if cls.storageClient != nil && cls.filesCipher == nil {
		log.Fatal().Msg("Storage is configured but there is no key for file encryption: set SECRETS_MASTER_KEY (or FILES_SECRETS_KEY)")
	}

	return cls
}

// newCipher builds the cipher of one secret family: nil when its key is unset (the
// feature is then disabled), an error when it is set but is neither one 64-hex-char key nor a keyring.
func newCipher(key string) (*secret.Cipher, error) {
	if key == "" {
		return nil, nil
	}
	return secret.New(key)
}

// mustCipher is newCipher for start-up: a missing key warns and disables only its
// own feature, an invalid one is fatal.
func mustCipher(envName, key, feature string) *secret.Cipher {
	cipher, err := newCipher(key)
	if err != nil {
		log.Fatal().Err(err).Msgf("%s is set but invalid (one 64-hex-char key, or a keyring of id:hex entries)", envName)
	}
	if cipher == nil {
		log.Warn().Msgf("%s unconfigured — %s disabled", envName, feature)
	}
	return cipher
}
