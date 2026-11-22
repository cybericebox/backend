package exercise

// consoleCablesCapability is the part of the infrastructure that says whether a laboratory offers console ports and
// console cables.
type consoleCablesCapability interface {
	ConsoleCablesAvailable() bool
}

// ConsoleCablesAvailable reports whether a lab with console ports or console cables can be deployed now: the
// infrastructure says so. Without an infrastructure, or one that does not tell, it is false.
func (u *ExerciseUseCase) ConsoleCablesAvailable() bool {
	capability, ok := u.infra.(consoleCablesCapability)
	return ok && capability.ConsoleCablesAvailable()
}
