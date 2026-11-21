package safeconv

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInt32ClampsInsteadOfWrapping(t *testing.T) {
	assert.EqualValues(t, 0, Int32(0))
	assert.EqualValues(t, -5, Int32(-5))
	assert.EqualValues(t, math.MaxInt32, Int32(math.MaxInt32))
	if math.MaxInt > math.MaxInt32 { // 64-bit: values beyond int32 exist
		big := int64(math.MaxInt32) + 1
		assert.EqualValues(t, math.MaxInt32, Int32(int(big)))
		assert.EqualValues(t, math.MaxInt32, Int32(int(int64(1)<<32+1)), "must not wrap to 1")
		assert.EqualValues(t, math.MinInt32, Int32(int(-big-1)))
	}
}
