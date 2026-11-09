//go:build !linux

package main

func writeNew(string, []byte) string { return unsupportedPlatform }
