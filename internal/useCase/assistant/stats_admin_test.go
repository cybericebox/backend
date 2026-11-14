package assistant_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	assistantModel "github.com/cybericebox/daemon/internal/model/assistant"
	"github.com/cybericebox/daemon/internal/useCase/assistant"
)

type memModes struct {
	mu       sync.Mutex
	defaults map[string]assistantModel.StatsMode
	events   map[uuid.UUID]assistantModel.StatsMode
	known    map[uuid.UUID]bool
}

func newMemModes(events ...uuid.UUID) *memModes {
	m := &memModes{defaults: map[string]assistantModel.StatsMode{}, events: map[uuid.UUID]assistantModel.StatsMode{}, known: map[uuid.UUID]bool{}}
	for _, e := range events {
		m.known[e] = true
	}
	return m
}

func (m *memModes) Defaults(context.Context) (map[string]assistantModel.StatsMode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]assistantModel.StatsMode{}
	for k, v := range m.defaults {
		out[k] = v
	}
	return out, nil
}

func (m *memModes) SetDefault(_ context.Context, source string, mode assistantModel.StatsMode, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaults[source] = mode
	return nil
}

func (m *memModes) EventMode(_ context.Context, e uuid.UUID) (assistantModel.StatsMode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode, ok := m.events[e]; ok {
		return mode, nil
	}
	return assistantModel.StatsModeDefault, nil
}

func (m *memModes) SetEventMode(_ context.Context, e uuid.UUID, mode assistantModel.StatsMode, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.known[e] {
		return false, nil
	}
	if mode == assistantModel.StatsModeDefault {
		delete(m.events, e)
	} else {
		m.events[e] = mode
	}
	return true, nil
}

var eventOne = uuid.FromStringOrNil("eeeeeeee-0000-4000-8000-000000000001")

func adminUC(modes *memModes) *assistant.UseCase {
	return assistant.New(assistant.Dependencies{Stats: assistant.MultiSource{&fakeStats{}}, StatsAdmin: modes})
}

func TestEveryKnownSourceIsListedOffUntilSet(t *testing.T) {
	modes := newMemModes()
	uc := adminUC(modes)
	list, err := uc.StatsDefaults(context.Background())
	if err != nil || len(list) != 1 || list[0].Source != "events" || list[0].Mode != assistantModel.StatsModeOff {
		t.Fatalf("%v %+v", err, list)
	}
	if err = uc.SetStatsDefault(context.Background(), "events", assistantModel.StatsModeFinal); err != nil {
		t.Fatal(err)
	}
	if list, _ = uc.StatsDefaults(context.Background()); list[0].Mode != assistantModel.StatsModeFinal {
		t.Fatalf("%+v", list)
	}
}

func TestADefaultCannotBeSetForAnUnknownSourceOrToDefault(t *testing.T) {
	uc := adminUC(newMemModes())
	for _, c := range []struct {
		source string
		mode   assistantModel.StatsMode
	}{{"academy", assistantModel.StatsModeFinal}, {"events", assistantModel.StatsModeDefault}, {"events", "bogus"}} {
		if err := uc.SetStatsDefault(context.Background(), c.source, c.mode); !errors.Is(err, assistantModel.ErrAssistantStatsInvalid.Err()) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestAnEventFollowsThePlatformDefaultUntilItHasItsOwnMode(t *testing.T) {
	modes := newMemModes(eventOne)
	uc := adminUC(modes)
	ctx := context.Background()
	got, err := uc.EventStats(ctx, eventOne)
	if err != nil || got.Mode != assistantModel.StatsModeDefault || got.Effective != assistantModel.StatsModeOff {
		t.Fatalf("fresh event, default off: %v %+v", err, got)
	}
	_ = uc.SetStatsDefault(ctx, "events", assistantModel.StatsModeContinuous)
	if got, _ = uc.EventStats(ctx, eventOne); got.Effective != assistantModel.StatsModeContinuous || got.Mode != assistantModel.StatsModeDefault {
		t.Fatalf("follows the platform: %+v", got)
	}
	if err = uc.SetEventStats(ctx, eventOne, assistantModel.StatsModeOff); err != nil {
		t.Fatal(err)
	}
	if got, _ = uc.EventStats(ctx, eventOne); got.Mode != assistantModel.StatsModeOff || got.Effective != assistantModel.StatsModeOff {
		t.Fatalf("its own mode wins: %+v", got)
	}
	if err = uc.SetEventStats(ctx, eventOne, assistantModel.StatsModeDefault); err != nil {
		t.Fatal(err)
	}
	if got, _ = uc.EventStats(ctx, eventOne); got.Mode != assistantModel.StatsModeDefault || got.Effective != assistantModel.StatsModeContinuous {
		t.Fatalf("back to the platform default: %+v", got)
	}
}

func TestSettingTheModeOfAMissingEventOrWithABadModeIsRefused(t *testing.T) {
	uc := adminUC(newMemModes(eventOne))
	missing := uuid.FromStringOrNil("eeeeeeee-0000-4000-8000-0000000000ff")
	if err := uc.SetEventStats(context.Background(), missing, assistantModel.StatsModeFinal); !errors.Is(err, assistantModel.ErrAssistantStatsEventNotFound.Err()) {
		t.Fatalf("missing event: %v", err)
	}
	if err := uc.SetEventStats(context.Background(), eventOne, "bogus"); !errors.Is(err, assistantModel.ErrAssistantStatsInvalid.Err()) {
		t.Fatalf("bad mode: %v", err)
	}
}

func TestModesParseFromTheAPI(t *testing.T) {
	for in, want := range map[string]assistantModel.StatsMode{"off": "off", " Final ": "final", "CONTINUOUS": "continuous", "default": "default"} {
		if got, ok := assistantModel.ParseStatsMode(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	if _, ok := assistantModel.ParseStatsMode("sometimes"); ok {
		t.Fatal("an unknown mode was accepted")
	}
	// An unknown stored value never reads as collecting.
	if assistantModel.StatsModeFromStored(0) != assistantModel.StatsModeOff || assistantModel.StatsModeFromStored(9) != assistantModel.StatsModeOff {
		t.Fatal("unknown stored modes must read as off")
	}
}
