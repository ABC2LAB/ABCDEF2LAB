package e2host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/e2checkpoint"
)

const maxSnapshot = 256 << 20

// persistHead는 DB ACK와 별도 Host 볼륨의 현재 head를 맞춘다. 두 저장소 중
// 하나라도 과거로 복원되면 다음 기동에서 불일치로 중지한다.
func (r *run) persistHead(ctx context.Context) error {
	if r == nil || r.directory == "" {
		return ErrUnavailable
	}
	head, err := r.owner.store.Head(ctx, e2checkpoint.Query{Epoch: r.owner.epoch, Binding: r.binding})
	if err != nil {
		return ErrUnavailable
	}
	data, err := json.Marshal(head)
	if err != nil {
		return ErrUnavailable
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrUnavailable
	}
	tmp := filepath.Join(r.directory, "head."+hex.EncodeToString(nonce[:])+".tmp")
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrUnavailable
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if os.Rename(tmp, filepath.Join(r.directory, "head.json")) != nil {
		return ErrUnavailable
	}
	dir, err := os.Open(r.directory)
	if err != nil {
		return ErrUnavailable
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func readSnapshot(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > maxSnapshot {
		return nil, ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil || len(data) != int(info.Size()) {
		return nil, ErrUnavailable
	}
	return data, nil
}

// CheckRestoredHeads는 서비스를 열기 전에 모든 과거 저널과 두 저장소의
// 현재 head를 대조한다. DB와 Host 볼륨을 함께 같은 과거 시점으로 되돌린
// 경우는 독립적인 외부 단조 anchor 없이는 탐지할 수 없으므로 운영 복구 대상이다.
func (h *Host) CheckRestoredHeads(ctx context.Context) error {
	if h == nil || h.db == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	rows, err := h.db.QueryContext(ctx, `SELECT run_ref FROM e2_http_audit_heads WHERE store_ref=$1`, h.epoch.StoreRef)
	if err != nil {
		return ErrUnavailable
	}
	stored := make(map[string]bool)
	for rows.Next() {
		var runRef string
		if rows.Scan(&runRef) != nil || runRef == "" || stored[runRef] {
			_ = rows.Close()
			return ErrUnavailable
		}
		stored[runRef] = true
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil {
		return ErrUnavailable
	}
	entries, err := os.ReadDir(h.root)
	if err != nil {
		return ErrUnavailable
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != sha256.Size*2 {
			return ErrUnavailable
		}
		directory := filepath.Join(h.root, entry.Name())
		mirror, err := readSnapshot(filepath.Join(directory, "head.json"))
		if err != nil {
			return ErrUnavailable
		}
		var expected e2checkpoint.Head
		if json.Unmarshal(mirror, &expected) != nil || expected.Epoch != h.epoch || expected.Binding.KeyRef != h.keyRef {
			return ErrUnavailable
		}
		// 비정상 종료 후 open head는 종료·drain 증거가 아니다. 별도 복구 없이
		// 새 실행을 열면 이전 실행 소유권을 잃으므로 기동 자체를 막는다.
		if expected.Phase != e2checkpoint.Sealed {
			return ErrUnavailable
		}
		digest := sha256.Sum256([]byte(expected.Binding.Scope.RunRef))
		if entry.Name() != hex.EncodeToString(digest[:]) {
			return ErrUnavailable
		}
		if !stored[expected.Binding.Scope.RunRef] {
			return ErrUnavailable
		}
		delete(stored, expected.Binding.Scope.RunRef)
		rctx, cancel := context.WithCancel(context.Background())
		r := &run{owner: h, scope: expected.Binding.Scope, binding: expected.Binding, directory: directory, ctx: rctx, cancel: cancel}
		h.mu.Lock()
		if h.active[r.scope.RunRef] != nil {
			h.mu.Unlock()
			cancel()
			return ErrUnavailable
		}
		h.active[r.scope.RunRef] = r
		h.mu.Unlock()
		actual, headErr := h.store.Head(ctx, e2checkpoint.Query{Epoch: h.epoch, Binding: r.binding})
		if headErr == nil && actual == expected {
			var snapshot []byte
			snapshot, err = readSnapshot(filepath.Join(directory, "events.jsonl"))
			if err == nil {
				_, actual, err = h.store.VerifyCurrent(ctx, e2checkpoint.Query{Epoch: h.epoch, Binding: r.binding}, bytes.NewReader(snapshot))
				clear(snapshot)
			}
		}
		h.remove(r)
		if headErr != nil || err != nil || actual != expected {
			return ErrUnavailable
		}
	}
	if len(stored) != 0 {
		return ErrUnavailable
	}
	return nil
}
