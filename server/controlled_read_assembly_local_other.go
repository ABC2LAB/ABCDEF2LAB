//go:build !linux

package server

func openControlledLocalPhysical(ControlledReadLocalBinding, [32]byte) (*controlledLocalPhysical, error) {
	return nil, ErrControlledReadLocalHost
}
func checkControlledLocalPhysical(*controlledLocalPhysical, ControlledReadLocalBinding) error {
	return ErrControlledReadLocalHost
}
func closeControlledLocalPhysical(*controlledLocalPhysical) error { return nil }
func ControlledReadLocalDirectoryIdentity(string) (ControlledReadLocalDirectory, error) {
	return ControlledReadLocalDirectory{}, ErrControlledReadLocalHost
}
