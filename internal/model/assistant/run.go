package assistant

import (
	"encoding/json"
	"maps"
	"time"

	"github.com/gofrs/uuid"
)

// The roles of a run row. The grader measures a task by working it the way a participant would. A lab is not a run of
// the brain: it is a test lab the brain asked for (provide_lab), kept as a row so that the VPN client made for it is
// revoked when the lab is gone. The opponents' roles (attack, defense, attack-defense) are the manifest's and are not
// started by the platform yet.
const (
	RoleGrader = "grader"
	RoleLab    = "lab"
	// RoleTools is the lab of a provide_device request for the platform tools device: the task's lab with the tools device
	// in it, kept as a row like a RoleLab one (its client and lab are revoked and destroyed with it).
	RoleTools = "tools"
)

// IsLabRole says a row is a lab the brain asked for (provide_lab or provide_device), not a run of the brain.
func IsLabRole(role string) bool { return role == RoleLab || role == RoleTools }

// RunState is where a run stands. starting: the lab is being deployed and the run is not accepted by the brain yet;
// running: its events are read; closing: the outcome is decided and the lab and the access are being torn down;
// finished, failed, stopped: over.
type RunState string

const (
	RunStarting RunState = "starting"
	RunRunning  RunState = "running"
	RunClosing  RunState = "closing"
	RunFinished RunState = "finished"
	RunFailed   RunState = "failed"
	RunStopped  RunState = "stopped"
)

// Active says the run still holds a lab (or is about to).
func (s RunState) Active() bool { return s == RunStarting || s == RunRunning || s == RunClosing }

// The reasons a run failed, in Run.FailureReason. The brain's own failure keeps the status it sent ("run_" + status).
const (
	FailureBrainUnreachable = "brain_unreachable"
	FailureLabFailed        = "lab_failed"
	FailureLabNotReady      = "lab_not_ready"
	FailureStartRefused     = "start_refused"
	FailureTimeout          = "max_duration"
	FailureBrainFailed      = "brain_failed"
	FailureInternal         = "internal"
)

// Run is one run the platform asked the brain for: its test lab (DeployID, owned by Actor), its state, the brain's last
// event sequence number that was stored and the final measurement (the brain's last aggregate, opaque).
type Run struct {
	ID         uuid.UUID
	ExerciseID uuid.UUID
	VersionID  uuid.UUID
	VariantID  uuid.UUID
	DeployID   uuid.UUID
	Actor      uuid.UUID
	Role       string
	// Opponent is the 1-based position of the opponent in the variant's manifest for an opponent run, 0 for any other run.
	Opponent int
	State    RunState
	// EndState is how a closing run ends ("" otherwise).
	EndState      RunState
	FailureReason string
	// AccessClient is the WireGuard client made for the run ("" until it exists).
	AccessClient string
	// RequestID is the outbox request a lab (RoleLab) was made for, Answered whether the brain got its answer.
	RequestID string
	Answered  bool
	LastSeq   int64
	// StreamDownSince is when the brain's event stream stopped being readable (zero while it is).
	StreamDownSince time.Time
	Measurement     json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	// LabReadyAt is when the run's test lab was first seen ready (zero until then).
	LabReadyAt time.Time
	// LabActivity is the activity of the run's lab, read before it was destroyed: nil while undecided, else a RunLabActivity.
	LabActivity json.RawMessage
	// ExpiresAt is the longest the run may last.
	ExpiresAt time.Time
}

// RunEvent is one frame of the brain's event stream of a run, kept verbatim: Seq is the frame id, Data the data line(s).
type RunEvent struct {
	Seq  int64
	Type string
	Data string
	At   time.Time
}

// RunAnswer is the judgement of one answer a run submitted. The submitted value is not kept. VerdictSent says the brain
// was told.
type RunAnswer struct {
	Seq         int64
	SubtaskRef  string
	Correct     bool
	At          time.Time
	VerdictSent bool
}

// Access is how a run reaches the lab: an open union keyed by kind, so a new kind needs no change in the storage or the
// transport. Today ssh, http and vpn; the value of a kind is that kind's own object, passed through untouched.
type Access map[string]json.RawMessage

// VPNAccess is the access of a run that joins the lab's network as a participant: entry is the full WireGuard
// configuration text.
func VPNAccess(entry string) Access {
	raw, _ := json.Marshal(struct {
		Entry string `json:"entry"`
	}{Entry: entry})
	return Access{"vpn": raw}
}

// WithAddr adds the address of the runner of the tools device on its control gateway (host:port) to an access: the brain joins
// the control gateway with the entry and dials that address. It is a key of the open union next to the entry, never a credential.
func (a Access) WithAddr(addr string) Access {
	out := maps.Clone(a)
	if out == nil {
		out = Access{}
	}
	raw, _ := json.Marshal(addr)
	out["addr"] = raw
	return out
}

// NoAccess is the access of a run that joins no lab: a task with nothing to reach (only its materials and answers). It is
// one more kind of the open union, written {"none": {}}.
func NoAccess() Access { return Access{"none": json.RawMessage(`{}`)} }

// runRoleExcluded lists the roles of a run and whether their runs are left out of the participant statistics. None is
// counted: the grader measures the task and an opponent plays against it, so the figures stay those of people. A role
// has to be listed here to exist.
var runRoleExcluded = map[string]bool{RoleGrader: true, RoleLab: true, RoleTools: true, "attack": true, "defense": true, "attack-defense": true}

// ExcludeRun says a run never counts in the participant statistics; a role that is not listed is left out too. The
// statistics sources call it for any run-shaped fact they may read. The events source reads teams only, and a grader's
// lab is a test deploy, which no event owns.
func ExcludeRun(role string) bool {
	excluded, known := runRoleExcluded[role]
	return excluded || !known
}

// StartRun is the body of `POST /v1/runs` for a run the platform originates: the grader has no band and no budget (the
// brain applies its own reference configuration) and no goal. Access is the one entry point; Start is "immediate" or
// {"after": "<ref>"}.
//
// An opponent run also carries what its manifest entry says (Band, Goal, Operates, Brief) and the platform's budget; the
// grader sends none of them.
type StartRun struct {
	RunID    string          `json:"run_id"`
	Role     string          `json:"role"`
	TaskRef  string          `json:"task_ref"`
	Version  string          `json:"version"`
	Image    string          `json:"image"`
	Access   Access          `json:"access"`
	Start    json.RawMessage `json:"start"`
	Band     int             `json:"band,omitempty"`
	Goal     []string        `json:"goal,omitempty"`
	Operates []string        `json:"operates,omitempty"`
	Foothold string          `json:"foothold,omitempty"`
	Brief    string          `json:"brief,omitempty"`
	Budget   *RunBudget      `json:"budget,omitempty"`
}

// RunBudget is what an opponent may spend: actions and tokens, and a time in whole seconds (omitted when zero).
type RunBudget struct {
	Actions int `json:"actions"`
	Tokens  int `json:"tokens"`
	Time    int `json:"time,omitempty"`
}

// Verdict is the body of `POST /v1/runs/{id}/verdict`: whether the answer was right and when it was judged. Never the
// expected value.
type Verdict struct {
	SubtaskRef string `json:"subtask_ref"`
	Correct    bool   `json:"correct"`
	At         string `json:"at"`
}
