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

// Distribution is a figure over teams as three quartiles. Which figure, and over which teams, is said where it is used.
type Distribution struct {
	P25    float64 `json:"p25"`
	Median float64 `json:"median"`
	P75    float64 `json:"p75"`
}

// StateCheck is the figure of one state-checked subtask (no answer is submitted for it, so this is the only human-time
// signal): how many teams passed it, and the seconds from the lab being ready to the pass over those teams.
type StateCheck struct {
	SubtaskRef  string       `json:"subtask_ref"`
	TeamsPassed int64        `json:"teams_passed"`
	TimeToPassS Distribution `json:"time_to_pass_s"`
}

// LabHealth is the noise filter: how the labs of the task's teams behaved. ReadyTimeS is over the team-instances whose lab
// came up (nil when none did); RestartsPerTeam is the mean number of lab restarts per team-instance; BrokenShare the
// share of team-instances that hit a lab failure.
type LabHealth struct {
	ReadyTimeS      *Distribution `json:"ready_time_s"`
	RestartsPerTeam float64       `json:"restarts_per_team"`
	BrokenShare     float64       `json:"broken_share"`
}

// TeamStateCheck is one state-checked subtask of one team: the seconds from the lab being ready to the pass, absent while
// the check did not pass.
type TeamStateCheck struct {
	SubtaskRef  string   `json:"subtask_ref"`
	TimeToPassS *float64 `json:"time_to_pass_s,omitempty"`
}

// TeamRecord is the anonymous row of one engaged team. It carries no team, user or address. TeamRef is a random id that is
// the same for the team across the tasks of ONE event and different in every other event (never derived from the team id);
// the records of a task are shuffled. The effort facts and EgressUsed are present when the laboratory reports them.
type TeamRecord struct {
	TeamRef          string           `json:"team_ref,omitempty"`
	Solved           bool             `json:"solved"`
	SolveTimeS       *float64         `json:"solve_time_s,omitempty"`
	WrongSubmissions int64            `json:"wrong_submissions"`
	HintsUsed        int64            `json:"hints_used"`
	StateChecks      []TeamStateCheck `json:"state_checks"`
	VPNBytes         *float64         `json:"vpn_bytes,omitempty"`
	DevicesTouched   *float64         `json:"devices_touched,omitempty"`
	ConsoleMinutes   *float64         `json:"console_minutes,omitempty"`
	EgressUsed       *bool            `json:"egress_used,omitempty"`
}

// HintBucket says how many teams opened this many hints of the task.
type HintBucket struct {
	Hints int   `json:"hints"`
	Teams int64 `json:"teams"`
}

// Aggregate is what the platform tells the brain about one task: figures over all teams, never a row of one person or
// one team. Source names where they come from ("events"): the brain gets one entry per (source, task_ref, version,
// image) and decides itself what to do with several. AsOf is the moment the figures were taken; they are cumulative up
// to it, so a later push replaces an earlier one.
type Aggregate struct {
	Source string `json:"source"`
	TaskRef
	Solves    int64        `json:"solves"`
	Attempts  int64        `json:"attempts"`
	SolveRate float64      `json:"solve_rate"`
	Time      TimeStats    `json:"time"`
	HintsUsed []HintBucket `json:"hints_used"`
	TaskKind  string       `json:"task_kind"`
	AsOf      time.Time    `json:"as_of"`

	// TeamsEngaged are the teams that opened or tried the task: the denominator SolveRate is read against.
	TeamsEngaged int64 `json:"teams_engaged"`
	// StateChecks is absent for a task without state-checked subtasks.
	StateChecks []StateCheck `json:"state_checks,omitempty"`
	// WrongSubmissionsPerTeam is over the solving teams (wrong answers before the solve); absent for a state-only task or
	// when nobody solved. WrongSubmissionsPerNonsolver is over the teams that did not solve (their total wrong answers).
	WrongSubmissionsPerTeam      *Distribution `json:"wrong_submissions_per_team,omitempty"`
	WrongSubmissionsPerNonsolver *Distribution `json:"wrong_submissions_per_nonsolver,omitempty"`
	// EgressEnabled: the topology has an Internet gateway.
	EgressEnabled bool `json:"egress_enabled"`
	// LabHealth is absent for a task without a lab.
	LabHealth *LabHealth `json:"lab_health,omitempty"`
	// The figures below come from the laboratory's activity reports; null (no figure) while it does not report them. null is
	// "not reported yet", never "none".
	EgressShare           *float64      `json:"egress_share"`
	VPNBytesPerTeam       *Distribution `json:"vpn_bytes_per_team"`
	DevicesTouchedPerTeam *Distribution `json:"devices_touched_per_team"`
	ConsoleMinutesPerTeam *Distribution `json:"console_minutes_per_team"`
	// TeamRecords is one anonymous record per engaged team, shuffled.
	TeamRecords []TeamRecord `json:"team_records"`
}

// Unavailable stands in the answer of a pull request for a task whose statistics are not collected: the code says so, so
// that "off" is not mistaken for "no data yet".
type Unavailable struct {
	TaskRef
	Error string `json:"error"`
}

// RunLabActivity is what the laboratory's activity reports said of a grader run's lab, kept on the run until its lab facts are
// sent. Reported false: nothing is reported (the reports are off, or the lab is unknown to them).
type RunLabActivity struct {
	Reported       bool     `json:"reported"`
	EgressUsed     bool     `json:"egress_used"`
	VPNBytes       *float64 `json:"vpn_bytes"`
	DevicesTouched *float64 `json:"devices_touched"`
	ConsoleMinutes float64  `json:"console_minutes"`
}
