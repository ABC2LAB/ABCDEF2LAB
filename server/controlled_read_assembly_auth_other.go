//go:build !linux

package server

func readControlledReadAssemblyJWTKey(string, string) ([]byte, error) {
	return nil, ErrControlledReadHostConfiguration
}
