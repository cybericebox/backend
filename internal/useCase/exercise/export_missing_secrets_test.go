package exercise

import exerciseModel "github.com/cybericebox/backend/internal/model/exercise"

// FirstSecretValueForTest exposes firstSecretValue to the external tests.
func FirstSecretValueForTest(versions []exerciseModel.ExerciseVersion) string {
	return firstSecretValue(versions)
}
