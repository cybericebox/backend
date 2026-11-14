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
// task, and when the save was claimed.
type Save struct {
	Session    string
	Author     uuid.UUID
	State      SaveState
	ExerciseID uuid.UUID
	JobID      uuid.UUID
	Notified   bool
	ClaimedAt  time.Time
}

// Pending says the task does not exist yet.
func (s Save) Pending() bool { return s.State == SavePending }
