package assistant

import (
	"encoding/json"
	"time"

	"github.com/gofrs/uuid"
)

// The role of a run: the grader measures a task by working it the way a participant would. The opponents' roles
// (attack, defense, attack-defense) are the manifest's and are not started by the platform yet.
const RoleGrader = "grader"

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
	State      RunState
	// EndState is how a closing run ends ("" otherwise).
	EndState      RunState
	FailureReason string
	// AccessClient is the WireGuard client made for the run ("" until it exists).
	AccessClient string
	LastSeq      int64
	// StreamDownSince is when the brain's event stream stopped being readable (zero while it is).
	StreamDownSince time.Time
	Measurement     json.RawMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
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

// runRoleExcluded lists the roles of a run and whether their runs are left out of the participant statistics. None is
// counted: the grader measures the task and an opponent plays against it, so the figures stay those of people. A role
// has to be listed here to exist.
var runRoleExcluded = map[string]bool{RoleGrader: true, "attack": true, "defense": true, "attack-defense": true}

// ExcludeRun says a run never counts in the participant statistics; a role that is not listed is left out too. The
// statistics sources call it for any run-shaped fact they may read. The events source reads teams only, and a grader's
// lab is a test deploy, which no event owns.
func ExcludeRun(role string) bool {
	excluded, known := runRoleExcluded[role]
	return excluded || !known
}
