//go:build !linux

package main

func openPrivateKey(string) (keyWriter, syncCloser, string) {
	return nil, nil, unsupportedPlatform
}
