package event

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatusParam(t *testing.T) {
	st, err := parseStatusParam("2")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.EqualValues(t, 2, *st)

	// 4294967297 is 1<<32+1: truncated to 32 bits it would silently filter by status 1.
	for _, bad := range []string{"4294967297", "2147483648", "-2147483649", "x", "1.5"} {
		st, err := parseStatusParam(bad)
		assert.Error(t, err, bad)
		assert.Nil(t, st, bad)
	}
}
