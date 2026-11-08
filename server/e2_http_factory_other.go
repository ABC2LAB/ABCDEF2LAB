//go:build !linux

package server

func e2HTTPExecutableDigest(string) ([32]byte, error) { return [32]byte{}, ErrE2HTTPHost }
func e2HTTPCreatePrivateDirectory(string) error       { return ErrE2HTTPHost }
