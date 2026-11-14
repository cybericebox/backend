package assistant

import "time"

// TaskRef names the task a statistic is about, with the same keys the brain's runs carry: the exercise id, the exercise
// version id the event pinned, and a fingerprint of the version's device images ("" when it has none).
type TaskRef struct {
	TaskRef string `json:"task_ref"`
	Version string `json:"version"`
	Image   string `json:"image"`
}

// TimeStats is the distribution of the time participants took to solve a task, in seconds.
type TimeStats struct {
	Median float64 `json:"median"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
}

// HintBucket says how many teams opened this many hints of the task.
type HintBucket struct {
	Hints int   `json:"hints"`
	Teams int64 `json:"teams"`
}

// Aggregate is what the platform tells the brain about one task: figures over all teams, never a row of one person or
// one team. AsOf is the moment the figures were taken; they are cumulative up to it, so a later push replaces an earlier
// one.
type Aggregate struct {
	TaskRef
	Solves    int64        `json:"solves"`
	Attempts  int64        `json:"attempts"`
	SolveRate float64      `json:"solve_rate"`
	Time      TimeStats    `json:"time"`
	HintsUsed []HintBucket `json:"hints_used"`
	Modality  string       `json:"modality"`
	AsOf      time.Time    `json:"as_of"`
}

// Unavailable stands in the answer of a pull request for a task whose statistics are not collected: the code says so, so
// that "off" is not mistaken for "no data yet".
type Unavailable struct {
	TaskRef
	Error string `json:"error"`
}
