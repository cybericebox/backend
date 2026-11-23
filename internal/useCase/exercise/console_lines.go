package exercise

// consoleCablesCapability is the part of the infrastructure that says whether a laboratory offers console ports and
// console cables.
type consoleCablesCapability interface {
	ConsoleCablesAvailable() bool
}

// kernelLogCapability is the part of the infrastructure that says whether a laboratory puts the kernel log on a console port.
type kernelLogCapability interface {
	KernelLogAvailable() bool
}

// KernelLogAvailable reports whether a lab whose devices ask for the kernel log on a console port can be deployed now (agent
// contract 3.2). Without an infrastructure, or one that does not tell, it is false.
func (u *ExerciseUseCase) KernelLogAvailable() bool {
	capability, ok := u.infra.(kernelLogCapability)
	return ok && capability.KernelLogAvailable()
}

// ConsoleCablesAvailable reports whether a lab with console ports or console cables can be deployed now: the
// infrastructure says so. Without an infrastructure, or one that does not tell, it is false.
func (u *ExerciseUseCase) ConsoleCablesAvailable() bool {
	capability, ok := u.infra.(consoleCablesCapability)
	return ok && capability.ConsoleCablesAvailable()
}
