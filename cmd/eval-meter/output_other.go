//go:build !linux

package main

import "os"

// The evaluation fronts run in Linux containers; other platforms fail closed
// instead of writing evidence without the private-directory checks.
func openPrivateDir(string) (*os.File, string) { return nil, unsupportedPlatform }

func createPrivateFile(*os.File, string) (*os.File, string) { return nil, unsupportedPlatform }

func writeMarker(*os.File, string, any) error { return os.ErrPermission }

var sealSignals []os.Signal

func isSealSignal(os.Signal) bool { return false }
