package assistant

import "strings"

// SourceEvents is the statistics source of the events. Sources are code: the Academy will be another one, with a rule of
// its own, and each source has its own platform default.
const SourceEvents = "events"

// StatsMode says when the statistics of a source (or of one event) go to the brain.
type StatsMode string

const (
	// StatsModeDefault (an event only): follow the platform default of the source.
	StatsModeDefault StatsMode = "default"
	// StatsModeOff: nothing is sent.
	StatsModeOff StatsMode = "off"
	// StatsModeFinal: the aggregates are sent once the event has ended.
	StatsModeFinal StatsMode = "final"
	// StatsModeContinuous: the aggregates are sent while the event runs.
	StatsModeContinuous StatsMode = "continuous"
)

// Stored values of the modes (0 is "default": no row).
const (
	statsStoredOff        int16 = 1
	statsStoredFinal      int16 = 2
	statsStoredContinuous int16 = 3
)

// ParseStatsMode reads a mode from the API; ok is false for an unknown one.
func ParseStatsMode(s string) (StatsMode, bool) {
	switch m := StatsMode(strings.ToLower(strings.TrimSpace(s))); m {
	case StatsModeDefault, StatsModeOff, StatsModeFinal, StatsModeContinuous:
		return m, true
	}
	return "", false
}

// Stored is the value kept in the database: 0 for default, else 1 off, 2 final, 3 continuous.
func (m StatsMode) Stored() int16 {
	switch m {
	case StatsModeOff:
		return statsStoredOff
	case StatsModeFinal:
		return statsStoredFinal
	case StatsModeContinuous:
		return statsStoredContinuous
	}
	return 0
}

// StatsModeFromStored is the mode of a stored value; anything unknown reads as off (never as collecting).
func StatsModeFromStored(v int16) StatsMode {
	switch v {
	case statsStoredFinal:
		return StatsModeFinal
	case statsStoredContinuous:
		return StatsModeContinuous
	}
	return StatsModeOff
}
