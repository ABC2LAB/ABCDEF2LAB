//go:build !linux

package privateinput

func ReadFile(string, int64) ([]byte, error) { return nil, ErrUnavailable }
func CheckDirectory(string, bool) error      { return ErrUnavailable }
