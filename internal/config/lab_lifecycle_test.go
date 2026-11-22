package config

import (
	"testing"
	"time"
)

func TestEventLabRevealTimeoutDefaultAndEnvironment(t *testing.T) {
	t.Setenv("RECAPTCHA_SECRET", "rsecret")
	if got := MustGetConfig().Tunables.EventLabRevealTimeout; got != 5*time.Minute {
		t.Fatalf("default reveal timeout %v, want 5m", got)
	}
	t.Setenv("EVENT_LAB_REVEAL_TIMEOUT", "90s")
	if got := MustGetConfig().Tunables.EventLabRevealTimeout; got != 90*time.Second {
		t.Fatalf("reveal timeout %v, want 90s", got)
	}
	cfg := MustGetConfig().Tunables
	cfg.EventLabRevealTimeout = 0
	if cfg.Validate() == nil {
		t.Fatal("a zero reveal timeout must be refused")
	}
}

func TestLabLifecycleDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("RECAPTCHA_SECRET", "rsecret")
	cfg := MustGetConfig().LabLifecycle
	if cfg.Interval != 2*time.Second || cfg.Batch != 100 || cfg.RetryMin != 10*time.Second || cfg.RetryMax != 5*time.Minute {
		t.Fatal(cfg)
	}
	t.Setenv("EVENT_LAB_LIFECYCLE_INTERVAL", "3s")
	t.Setenv("EVENT_LAB_LIFECYCLE_BATCH", "7")
	t.Setenv("EVENT_LAB_LIFECYCLE_RETRY_MIN", "20s")
	t.Setenv("EVENT_LAB_LIFECYCLE_RETRY_MAX", "2m")
	cfg = MustGetConfig().LabLifecycle
	if cfg.Interval != 3*time.Second || cfg.Batch != 7 || cfg.RetryMin != 20*time.Second || cfg.RetryMax != 2*time.Minute {
		t.Fatal(cfg)
	}
}
