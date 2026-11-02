//go:build !linux

package server

func readPrivate(string, int64) ([]byte, error) {
	return nil, errControlledReadStorage
}
