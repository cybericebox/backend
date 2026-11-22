package exercise

import (
	"encoding/json"
	"fmt"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// forbiddenOpponentKeys are what the platform alone decides about an opponent: its image, its toolset, the keys it
// works with and its budget. An author that names one of them is refused, with the field named.
var forbiddenOpponentKeys = []string{
	"image", "images", "toolset", "tools", "toolbox", "key", "keys", "private_key", "ssh_key", "token", "secret", "model", "access",
	"budget", "actions", "tokens", "time", "time_limit", "max_actions", "max_tokens",
}

// checkOpponentKeys refuses, before the strict decode, an opponent that names a field of the platform. The error is
// ErrOpponentInvalid (reason forbidden_field) with the field path under the variant and, as every document error, the
// path of the variant. Any other unknown key is refused by the strict decode that follows.
func checkOpponentKeys(payload []byte) error {
	var doc struct {
		Versions []struct {
			Variants []struct {
				Opponents []map[string]json.RawMessage `json:"opponents"`
			} `json:"variants"`
		} `json:"versions"`
	}
	if json.Unmarshal(payload, &doc) != nil {
		return nil
	}
	for vi, version := range doc.Versions {
		for xi, variant := range version.Variants {
			for oi, opponent := range variant.Opponents {
				for _, key := range forbiddenOpponentKeys {
					if _, named := opponent[key]; !named {
						continue
					}
					return withArchivePath(
						exerciseModel.ErrOpponentInvalid.
							WithPublicContext("field", fmt.Sprintf("opponents[%d].%s", oi+1, key)).
							WithPublicContext("reason", exerciseModel.OpponentReasonForbiddenField).Err(),
						fmt.Sprintf("versions[%d].variants[%d]", vi+1, xi+1),
					)
				}
			}
		}
	}
	return nil
}
