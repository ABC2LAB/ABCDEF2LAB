//go:build !linux

package main

import "errors"

// Run-scoped tmpfs exists only in the Linux evaluation containers.
func statFilesystem(string) (fsStat, error) { return fsStat{}, errors.ErrUnsupported }
