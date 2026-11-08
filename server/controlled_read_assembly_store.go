// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"github.com/Autumn-27/artex/internal/privateinput"
	"os"
	"path/filepath"
)

func existingControlledReadAssemblyStore(root string) (*controlledReadStore, error) {
	snapshots, state := os.Getenv("ARTEX_E2_SNAPSHOT_DIR"), os.Getenv("ARTEX_E2_STATE_DIR")
	if snapshots == "" || state != root || state == "" {
		return nil, ErrControlledReadHostConfiguration
	}
	return existingControlledReadAssemblyStoreAt(state, snapshots)
}

func existingControlledReadAssemblyStoreAt(state, snapshots string) (*controlledReadStore, error) {
	for _, path := range []string{snapshots, state, filepath.Join(state, "journals")} {
		if privateinput.CheckDirectory(path, true) != nil {
			return nil, ErrControlledReadHostConfiguration
		}
	}
	return &controlledReadStore{snapshotDir: snapshots, stateDir: state}, nil
}
