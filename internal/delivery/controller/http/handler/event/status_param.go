package event

import (
	"math"
	"strconv"

	participantModel "github.com/cybericebox/backend/internal/model/participant"
)

// parseStatusParam reads the status filter of a participant list. A number outside the 32 bits of a status is
// refused: cut down to 32 bits it would filter by some other status without a word.
func parseStatusParam(raw string) (*participantModel.Status, error) {
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return nil, err
	}
	if n < math.MinInt32 || n > math.MaxInt32 {
		return nil, strconv.ErrRange
	}
	st := participantModel.Status(n)
	return &st, nil
}
