package usertext

import "fmt"

// ImportedNameSuffix is appended to the name of a task imported again under a taken name.
func ImportedNameSuffix(attempt int) string {
	return fmt.Sprintf(" (імпорт %d)", attempt)
}
