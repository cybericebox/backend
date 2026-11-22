package exercise

import (
	"testing"
	"time"
)

func TestPickLease(t *testing.T) {
	allowed := []int{15, 30, 60}
	for want, expect := range map[time.Duration]time.Duration{
		10 * time.Minute: 15 * time.Minute, 15 * time.Minute: 15 * time.Minute, 20 * time.Minute: 30 * time.Minute, 90 * time.Minute: 60 * time.Minute,
	} {
		if got := pickLease(allowed, want); got != expect {
			t.Errorf("want %v: got %v, expected %v", want, got, expect)
		}
	}
	if got := pickLease(nil, time.Hour); got != 0 {
		t.Errorf("no allowed length leaves the default lease: %v", got)
	}
}
