package exercise

import (
	"fmt"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/model"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/secretctx"
)

// The secret operator credentials of a variant's defender block (console design 9a.1) are stored like secret env values: sealed
// at rest, a blank secret on a save keeps the stored value, blank in every read model, plaintext only in a password-protected
// archive and in the dossier of the moment. A credential is kept by (the variant it is sealed to, device, port, user); a linked
// variant takes its base's block as it is, so its credentials are sealed to the base (Variant.SealVariant). A copy of a
// variant does not take the values: they are missing on the copy until the author enters them (MissingSecrets).

// DefenderSecretContext binds the ciphertext of a secret credential to its variant, device, port and user.
func DefenderSecretContext(variantID uuid.UUID, c exerciseModel.DefenderCredential) []byte {
	return secretctx.ExerciseDefenderCredential(variantID, c.Device, c.Port, c.User)
}

func defenderKey(variantID uuid.UUID, c exerciseModel.DefenderCredential) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s", variantID, c.Device, c.Port, c.User)
}

// eachSecretCredential calls fn for every secret credential of the variants, with the variant it is sealed to.
func eachSecretCredential(variants []exerciseModel.Variant, fn func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error) error {
	for vi := range variants {
		if variants[vi].Defender == nil {
			continue
		}
		seal := variants[vi].SealVariant()
		for ci := range variants[vi].Defender.Credentials {
			if c := &variants[vi].Defender.Credentials[ci]; c.Secret {
				if err := fn(seal, c); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// submittedDefenderSecrets records the secret credentials that arrived with a value (plaintext), before the merge.
func submittedDefenderSecrets(variants []exerciseModel.Variant) map[string]bool {
	out := map[string]bool{}
	_ = eachSecretCredential(variants, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value != "" {
			out[defenderKey(seal, *c)] = true
		}
		return nil
	})
	return out
}

// mergeKeptDefenderSecrets fills a blank secret credential with the ciphertext the stored draft has for it.
func mergeKeptDefenderSecrets(incoming, current []exerciseModel.Variant) {
	kept := map[string]string{}
	_ = eachSecretCredential(current, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value != "" {
			kept[defenderKey(seal, *c)] = c.Value
		}
		return nil
	})
	_ = eachSecretCredential(incoming, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value == "" {
			c.Value = kept[defenderKey(seal, *c)]
		}
		return nil
	})
}

// encryptDefenderSecrets seals the secret credentials submitted in this save; merged values are ciphertext already.
func (u *ExerciseUseCase) encryptDefenderSecrets(variants []exerciseModel.Variant, submitted map[string]bool) error {
	return eachSecretCredential(variants, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value == "" || !submitted[defenderKey(seal, *c)] {
			return nil
		}
		return u.sealCredential(seal, c, "Failed to encrypt a secret credential")
	})
}

func (u *ExerciseUseCase) sealCredential(seal uuid.UUID, c *exerciseModel.DefenderCredential, failure string) error {
	if u.cipher == nil {
		return exerciseModel.ErrSecretsNotConfigured.Err()
	}
	ct, err := u.cipher.EncryptWithContext([]byte(c.Value), DefenderSecretContext(seal, *c))
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage(failure).Err()
	}
	c.Value = ct
	return nil
}

func (u *ExerciseUseCase) openCredential(seal uuid.UUID, c *exerciseModel.DefenderCredential, failure string) error {
	if u.cipher == nil {
		return exerciseModel.ErrSecretsNotConfigured.Err()
	}
	plain, err := u.cipher.DecryptWithContext(c.Value, DefenderSecretContext(seal, *c))
	if err != nil {
		return model.ErrPlatform.WithError(err).WithMessage(failure).Err()
	}
	c.Value = string(plain)
	return nil
}

// decryptExportedDefenderSecrets opens the secret credentials of an export copy (a password-protected archive only).
func (u *ExerciseUseCase) decryptExportedDefenderSecrets(variants []exerciseModel.Variant) error {
	return eachSecretCredential(variants, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value == "" {
			return nil
		}
		return u.openCredential(seal, c, "Failed to decrypt a secret credential for export")
	})
}

// encryptImportedDefenderSecrets seals the secret credentials of imported variants before they are stored.
func (u *ExerciseUseCase) encryptImportedDefenderSecrets(variants []exerciseModel.Variant) error {
	return eachSecretCredential(variants, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value == "" {
			return nil
		}
		return u.sealCredential(seal, c, "Failed to encrypt an imported secret credential")
	})
}

// restoreKeptDefenderSecrets puts the stored value (opened) back where an edited version left a secret credential blank.
func (u *ExerciseUseCase) restoreKeptDefenderSecrets(imported []exerciseModel.Variant, stored []exerciseModel.Variant) error {
	kept := map[string]exerciseModel.DefenderCredential{}
	seals := map[string]uuid.UUID{}
	_ = eachSecretCredential(stored, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		if c.Value != "" {
			key := defenderKey(seal, *c)
			kept[key], seals[key] = *c, seal
		}
		return nil
	})
	return eachSecretCredential(imported, func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error {
		key := defenderKey(seal, *c)
		found, ok := kept[key]
		if c.Value != "" || !ok {
			return nil
		}
		if err := u.openCredential(seals[key], &found, "Failed to open a kept secret credential"); err != nil {
			return err
		}
		c.Value = found.Value
		return nil
	})
}

// defenderCredentials are the credentials of the variant's defender block with the secret values opened: for the dossier of
// the moment only, never stored or logged. None without a block.
func (u *ExerciseUseCase) defenderCredentials(v exerciseModel.Variant) ([]exerciseModel.DefenderCredential, error) {
	if v.Defender == nil {
		return []exerciseModel.DefenderCredential{}, nil
	}
	out := make([]exerciseModel.DefenderCredential, 0, len(v.Defender.Credentials))
	seal := v.SealVariant()
	for _, c := range v.Defender.Credentials {
		if c.Secret && c.Value != "" {
			if err := u.openCredential(seal, &c, "Failed to decrypt a secret credential"); err != nil {
				return nil, err
			}
		}
		c.Secret = false
		out = append(out, c)
	}
	return out, nil
}

// maskDefender is the defender block of a read model: a deep copy with every secret value blank.
func maskDefender(d *exerciseModel.Defender) *exerciseModel.Defender {
	out := exerciseModel.CopyDefender(d)
	if out == nil {
		return nil
	}
	for i := range out.Credentials {
		if out.Credentials[i].Secret {
			out.Credentials[i].Value = ""
		}
	}
	return out
}
