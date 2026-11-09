//go:build !linux

package diagnosticlog

func Create(string, string, int64, string) (*Logger, error) { return nil, ErrCreate }
