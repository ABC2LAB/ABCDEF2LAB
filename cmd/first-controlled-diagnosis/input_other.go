//go:build !linux

package main

func readPrivateFile(string, int64) ([]byte, error) { return nil, errPreparation }
func checkDirectory(string, bool) error             { return errPreparation }
