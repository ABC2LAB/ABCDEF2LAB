//go:build !linux

package main

import "os"

func openRegular(string, bool) (*os.File, error) {
	return nil, errInput
}
