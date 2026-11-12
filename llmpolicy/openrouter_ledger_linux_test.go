//go:build linux

package llmpolicy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// openRouterLedgerFixture는 보호 디렉터리에 manifest를 쓰고 그 경로와 기록 파일 경로를 돌려준다.
func openRouterLedgerFixture(t *testing.T, maxRequests int64) (string, string) {
	t.Helper()
	p := openRouterFixture(t)
	p.manifest.MaxRequests = maxRequests
	p.manifest.MaxInputTokens = maxRequests * openRouterContext
	p.manifest.MaxOutputTokens = maxRequests * 100
	raw, _ := json.Marshal(p.manifest)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "approval.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, fmt.Sprintf(".openrouter-%x.ledger", sha256.Sum256([]byte(p.manifest.ApprovalID))))
	return path, ledger
}

func loadForLedger(t *testing.T, path string) *OpenRouterPolicy {
	t.Helper()
	p, err := LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 기록 파일이 깨졌거나 보호 조건이 틀리면 예약하지 않고 막는다(재사용 방지 목적 유지).
func TestOpenRouterLedgerRejectsUntrustedRecord(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, ledger string){
		"bad-line":      func(t *testing.T, ledger string) { _ = os.WriteFile(ledger, []byte("r1 1 1\n"), 0600) },
		"leading-zero":  func(t *testing.T, ledger string) { _ = os.WriteFile(ledger, []byte("r1 0262144 100 1\n"), 0600) },
		"no-newline":    func(t *testing.T, ledger string) { _ = os.WriteFile(ledger, []byte("r1 262144 100 1"), 0600) },
		"over-call-cap": func(t *testing.T, ledger string) { _ = os.WriteFile(ledger, []byte("r1 262144 101 1\n"), 0600) },
		"public-mode": func(t *testing.T, ledger string) {
			_ = os.WriteFile(ledger, nil, 0600)
			_ = os.Chmod(ledger, 0644)
		},
		"symlink": func(t *testing.T, ledger string) {
			target := filepath.Join(filepath.Dir(ledger), "elsewhere")
			_ = os.WriteFile(target, nil, 0600)
			if err := os.Symlink(target, ledger); err != nil {
				t.Fatal(err)
			}
		},
		"hard-link": func(t *testing.T, ledger string) {
			_ = os.WriteFile(ledger, nil, 0600)
			if err := os.Link(ledger, ledger+".copy"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path, ledger := openRouterLedgerFixture(t, 3)
			damage(t, ledger)
			p := loadForLedger(t, path)
			if p.CheckAvailable() == nil {
				t.Fatal("untrusted record reported available")
			}
			if err := p.reserve(100); err == nil || !p.Snapshot().Blocked || p.Snapshot().FailureReason != "approval_unavailable" || p.Snapshot().Requests != 0 {
				t.Fatalf("untrusted record allowed a reservation: %+v", p.Snapshot())
			}
		})
	}
}

// 이전 1회용 표식이 남은 승인은 기록이 없어도 이미 쓴 것으로 본다.
func TestOpenRouterLedgerHonoursLegacyUsedMarker(t *testing.T) {
	path, ledger := openRouterLedgerFixture(t, 3)
	marker := ledger[:len(ledger)-len(".ledger")] + ".used"
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p := loadForLedger(t, path)
	if p.CheckAvailable() == nil {
		t.Fatal("legacy consumed approval reported available")
	}
	if err := p.reserve(100); err == nil || p.Snapshot().FailureReason != "approval_consumed" {
		t.Fatalf("legacy consumed approval reserved: %+v", p.Snapshot())
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Fatal("record created for a consumed approval")
	}
}

// 여러 프로세스(정책 인스턴스)가 같은 승인을 동시에 써도 누적 예약은 승인 총량을 넘지 않는다.
func TestOpenRouterLedgerSerializesConcurrentReservations(t *testing.T) {
	const total, workers = 3, 8
	path, ledger := openRouterLedgerFixture(t, total)
	var granted atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		p := loadForLedger(t, path)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.reserve(100) == nil {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if granted.Load() != total || len(data) != total*len(fmt.Sprintf(openRouterLedgerLine, int64(openRouterContext), int64(100), openRouterReservedCost(openRouterFixture(t).manifest, 100))) {
		t.Fatalf("granted=%d record=%q", granted.Load(), data)
	}
	info, err := os.Stat(ledger)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("record not protected")
	}
}
