package exercise

import (
	"context"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// FirstSecretValueForTest exposes firstSecretValue to the external tests.
func FirstSecretValueForTest(versions []exerciseModel.ExerciseVersion) string {
	return firstSecretValue(versions)
}

// ValidateImportContentForTest runs the write-free content validation of an import (the one save → ImportRawArchive does)
// on a parsed archive, for the external tests.
func ValidateImportContentForTest(ctx context.Context, u *ExerciseUseCase, parsed *ParsedExerciseArchive, protected bool) error {
	return u.validateImportContent(ctx, parsed, protected)
}
