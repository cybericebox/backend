// Package safeconv narrows integers with an explicit range check, so a value that does not fit is clamped
// instead of wrapping around to another number.
package safeconv

import "math"

// Int32 is n limited to the range of int32.
func Int32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}
