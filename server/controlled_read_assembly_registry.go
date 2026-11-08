package server

import (
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/kgfixture"
	"path/filepath"
)

func loadControlledAssemblyRegistry(dir, name string) (*kgfixture.Snapshot, error) {
	if !safeFile.MatchString(name) {
		return nil, ErrControlledReadHostConfiguration
	}
	body, err := privateinput.ReadFile(filepath.Join(dir, name), kgfixture.MaxRegistryBytes)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	defer clear(body)
	snapshot, err := kgfixture.Parse(body)
	if err != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	return snapshot, nil
}
