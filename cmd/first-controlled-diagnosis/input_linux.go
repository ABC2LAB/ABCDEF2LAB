//go:build linux

package main

import "github.com/Autumn-27/artex/internal/privateinput"

func readPrivateFile(path string, limit int64) ([]byte, error) {
	data, err := privateinput.ReadFile(path, limit)
	if err != nil {
		return nil, errPreparation
	}
	return data, nil
}
func checkDirectory(path string, private bool) error {
	if privateinput.CheckDirectory(path, private) != nil {
		return errPreparation
	}
	return nil
}
