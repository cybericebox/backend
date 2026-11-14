package assistant

import (
	"time"

	"github.com/gofrs/uuid"
)

// Save is what the platform keeps of one save of a finished session by one author: the catalog task it was imported as
// (Nil while a background import still runs), that import's job, and whether the brain was told the task.
type Save struct {
	Session    string
	Author     uuid.UUID
	ExerciseID uuid.UUID
	JobID      uuid.UUID
	Notified   bool
	CreatedAt  time.Time
}

// Pending says the background import has not produced the task yet.
func (s Save) Pending() bool { return s.ExerciseID == uuid.Nil }
