package exercise

import (
	"fmt"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/model"
	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	"github.com/cybericebox/backend/internal/secretctx"
)

// The secret operator credentials of a variant's defender block (console design 9a.1) follow exactly the rules of a secret env
// value on every path (confirmed with the brain session, which holds the exercisedoc): entered in the editor and sealed at rest
// to their variant; a blank secret on a save keeps the stored value; a linked variant or a copy takes its source's value, sealed
// under its own id (inheritSecrets); blank in every read model, in the edit export and in every export except a
// password-protected archive; an import refuses a non-empty secret value except from a password-protected archive
// (firstSecretValue); a missing value refuses publishing (MissingSecrets). A credential is kept by (variant, device, port,
// user), which is unique in a variant (credential_repeated).

// DefenderSecretContext binds the ciphertext of a secret credential to its variant, device, port and user.
func DefenderSecretContext(variantID uuid.UUID, c exerciseModel.DefenderCredential) []byte {
	return secretctx.ExerciseDefenderCredential(variantID, c.Device, c.Port, c.User)
}

// defenderKey is the key of a secret credential in the maps of a save (the same maps as the secret env values: the prefix keeps
// the two key spaces apart).
func defenderKey(variantID uuid.UUID, c exerciseModel.DefenderCredential) string {
	return fmt.Sprintf("defender\x00%s\x00%s\x00%s\x00%s", variantID, c.Device, c.Port, c.User)
}

// eachSecretCredential calls fn for every secret credential of the variants, with the variant it is sealed to.
func eachSecretCredential(variants []exerciseModel.Variant, fn func(seal uuid.UUID, c *exerciseModel.DefenderCredential) error) error {
	for vi := range variants {
		if variants[vi].Defender == nil {
			continue
		}
		seal := variants[vi].ID
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

// inheritDefenderSecrets gives a linked variant or a copy its source's secret credentials, the rule of inheritVariantSecrets for
// env values: a target credential still blank (or, for a linked variant, every one not typed in this save) takes the source's
// value of the same (device, port, user) as plaintext and is marked submitted, so it is sealed under the target's id; a linked
// variant keeps its own stored value while it opens to the base's, and is cleared when the base has none.
func (u *ExerciseUseCase) inheritDefenderSecrets(target *exerciseModel.Variant, source exerciseModel.Variant, linked bool, submitted map[string]bool) error {
	if target.Defender == nil {
		return nil
	}
	for ci := range target.Defender.Credentials {
		c := &target.Defender.Credentials[ci]
		if !c.Secret {
			continue
		}
		targetKey := defenderKey(target.ID, *c)
		if c.Value != "" && (!linked || submitted[targetKey]) {
			continue
		}
		sourceValue := ""
		if source.Defender != nil {
			for _, s := range source.Defender.Credentials {
				if s.Secret && s.Device == c.Device && s.Port == c.Port && s.User == c.User {
					sourceValue = s.Value
				}
			}
		}
		if sourceValue == "" {
			if linked {
				c.Value = ""
			}
			continue
		}
		plain := exerciseModel.DefenderCredential{Device: c.Device, Port: c.Port, User: c.User, Value: sourceValue}
		if !submitted[defenderKey(source.ID, plain)] {
			if err := u.openCredential(source.ID, &plain, "Failed to decrypt the secret credential of the source variant"); err != nil {
				return err
			}
		}
		if c.Value != "" && u.cipher != nil {
			own := *c
			if u.openCredential(target.ID, &own, "") == nil && own.Value == plain.Value {
				continue
			}
		}
		c.Value = plain.Value
		submitted[targetKey] = true
	}
	return nil
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
	seal := v.ID
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
