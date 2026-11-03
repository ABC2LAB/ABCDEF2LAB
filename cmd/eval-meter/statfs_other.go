//go:build !linux

package main

import "errors"

// Run-scoped tmpfs exists only in the Linux evaluation containers.
func statOpened(string) (fsStat, error) { return fsStat{}, errors.ErrUnsupported }
