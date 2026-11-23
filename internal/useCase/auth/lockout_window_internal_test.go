package auth

import (
	"testing"
	"time"
)

// The quiet window that forgets the TOTP failures is always longer than the longest lock.
func TestTOTPQuietWindowIsLongerThanTheLongestLock(t *testing.T) {
	for _, c := range []struct{ window, lockMax, want time.Duration }{
		{15 * time.Minute, 15 * time.Minute, 30 * time.Minute},
		{time.Hour, 15 * time.Minute, time.Hour},
		{time.Minute, time.Hour, 2 * time.Hour},
	} {
		got := totpQuietWindow(c.window, c.lockMax)
		if got != c.want || got <= c.lockMax {
			t.Errorf("window %s, lock max %s: got %s, want %s (> lock max)", c.window, c.lockMax, got, c.want)
		}
	}
	if w := totpLockout().Window; w <= totpLockout().LockMax {
		t.Fatalf("the configured quiet window %s is not longer than the longest lock %s", w, totpLockout().LockMax)
	}
}
