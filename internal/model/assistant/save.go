package assistant

import (
	"time"

	"github.com/gofrs/uuid"
)

// SaveState is where a save of a finished session stands.
type SaveState string

const (
	// SavePending: claimed by a request, or its background import is running.
	SavePending SaveState = "pending"
	// SaveDone: the task exists in the catalog.
	SaveDone SaveState = "done"
	// SaveFailed: the import failed; the next save claims the row again.
	SaveFailed SaveState = "failed"
)

// Save is what the platform keeps of one save of a finished session by one author: its state, the catalog task (Nil
// until done), the background import job (Nil for an import made inside the request), whether the brain was told the
// outcome (the task, or the failure), when the save was claimed and, for a failed one, the error code.
type Save struct {
	Session    string
	Author     uuid.UUID
	State      SaveState
	ExerciseID uuid.UUID
	JobID      uuid.UUID
	Notified   bool
	ClaimedAt  time.Time
	// FailureCode is the stable error code (the catalog's FullCode) a failed save ended with.
	FailureCode int
}

// Pending says the task does not exist yet.
func (s Save) Pending() bool { return s.State == SavePending }
