//go:build linux

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 제품 실행의 시도 경로 패턴은 시도 ID와 값 조각을 가린 경로만 0600 파일에 한 줄씩 남긴다. 경로 없는 거부·시도 ID 없는
// 영수증은 남기지 않고, 링크는 따라가지 않는다.
func TestProductAttemptPatternsWritesMaskedRows(t *testing.T) {
	dir := t.TempDir()
	observe := productAttemptPatterns(dir)
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	observe(e2proxy.Receipt{AttemptID: a}, "/api/orders/42")
	observe(e2proxy.Receipt{AttemptID: b}, "/rest/products/search")
	observe(e2proxy.Receipt{AttemptID: strings.Repeat("c", 32)}, "")
	observe(e2proxy.Receipt{}, "/api/orders/43")
	path := filepath.Join(dir, E2AttemptPatternsFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("pattern file not private")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 || strings.Contains(string(raw), "42") {
		t.Fatalf("rows: %q", raw)
	}
	var first, second E2AttemptPatternRow
	if json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[1]), &second) != nil ||
		first != (E2AttemptPatternRow{AttemptID: a, PathPattern: "/api/orders/{id}"}) || second != (E2AttemptPatternRow{AttemptID: b, PathPattern: "/rest/products/search"}) {
		t.Fatalf("rows: %q", raw)
	}
	// 링크 자리에는 쓰지 않는다.
	linked := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil || os.Symlink(target, filepath.Join(linked, E2AttemptPatternsFile)) != nil {
		t.Fatal("fixture")
	}
	productAttemptPatterns(linked)(e2proxy.Receipt{AttemptID: a}, "/x")
	if data, _ := os.ReadFile(target); len(data) != 0 {
		t.Fatal("pattern written through a symlink")
	}
}
