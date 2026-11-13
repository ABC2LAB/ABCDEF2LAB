//go:build linux

package llmpolicy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
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

// openRouterSettleFixture는 요청 수는 넉넉하고 출력 승인은 예약 두 개분(200)인 보호 manifest다.
func openRouterSettleFixture(t *testing.T) (string, string) {
	t.Helper()
	p := openRouterFixture(t)
	p.manifest.MaxRequests = 10
	p.manifest.MaxInputTokens = 10 * openRouterContext
	p.manifest.MaxOutputTokens = 200
	raw, _ := json.Marshal(p.manifest)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "approval.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Join(dir, fmt.Sprintf(".openrouter-%x.ledger", sha256.Sum256([]byte(p.manifest.ApprovalID))))
}

// 응답을 확인한 호출은 실제 사용량을 장부에 남겨, 예약과 실제의 차이를 다시 쓴다(C07 "승인별 누적 사용량", C28 결정 2).
// 출력 승인이 예약 두 개분이어도 실제 출력이 작으면 더 보낸다. 다른 프로세스·재시작도 같은 합계를 읽는다.
func TestOpenRouterLedgerSettlementReleasesUnusedReservation(t *testing.T) {
	path, ledger := openRouterSettleFixture(t)
	p := loadForLedger(t, path)
	hits := 0
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		hits++
		return openRouterResponse(openRouterReply), nil // 출력 2토큰
	}))
	for i := 0; i < 5; i++ {
		resp, err := c.Do(openRouterRequest())
		if err != nil {
			t.Fatalf("call %d refused although actual output is far below the approval: %v", i, err)
		}
		_ = resp.Body.Close()
	}
	reloaded := loadForLedger(t, path)
	if err := reloaded.CheckAvailable(); err != nil {
		t.Fatal("settled approval not available after reload", err)
	}
	fd, err := os.Open(ledger)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	totals, err := openRouterLedgerSum(int(fd.Fd()), reloaded.manifest)
	if err != nil || totals.Requests != 5 || totals.Output != 5*2 || totals.Cost != 5*12 {
		t.Fatalf("ledger totals after settlement: %+v err=%v", totals, err)
	}
	if hits != 5 {
		t.Fatal("unexpected sends", hits)
	}
}

// 실패한 호출은 정산하지 않아 예약이 그대로 다 쓴 것으로 남는다(안전한 쪽). 출력 승인 두 개분이면 실패 두 번 뒤 막힌다.
func TestOpenRouterLedgerFailedCallKeepsReservation(t *testing.T) {
	path, _ := openRouterSettleFixture(t)
	p := loadForLedger(t, path)
	p.manifest.Version, p.manifest.MaxConsecutiveTransient = 2, 16
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { return openRouterStatus(503), nil }))
	for i := 0; i < 2; i++ {
		if _, err := c.Do(openRouterRequest()); err == nil {
			t.Fatal("failure accepted")
		}
	}
	if err := p.reserve(100); err == nil || p.Snapshot().FailureReason != "token_limit" {
		t.Fatalf("failed calls' reservations released: %+v", p.Snapshot())
	}
}

// 장부의 정산 줄은 짝이 되는 정산 안 된 예약이 있고 실제가 예약 이하일 때만 믿는다.
func TestOpenRouterLedgerRejectsUntrustedSettlement(t *testing.T) {
	m := openRouterFixture(t).manifest
	cost := openRouterReservedCost(m, 100)
	r := fmt.Sprintf(openRouterLedgerLine, int64(openRouterContext), int64(100), cost)
	for name, body := range map[string]string{
		"no-reservation": fmt.Sprintf(openRouterSettleLine, int64(openRouterContext), int64(100), cost, int64(10), int64(2), int64(12)),
		"double":         r + fmt.Sprintf(openRouterSettleLine, int64(openRouterContext), int64(100), cost, int64(10), int64(2), int64(12)) + fmt.Sprintf(openRouterSettleLine, int64(openRouterContext), int64(100), cost, int64(10), int64(2), int64(12)),
		"over-reserved":  r + fmt.Sprintf(openRouterSettleLine, int64(openRouterContext), int64(100), cost, int64(10), int64(101), int64(12)),
		"other-value":    r + fmt.Sprintf(openRouterSettleLine, int64(openRouterContext), int64(99), cost, int64(10), int64(2), int64(12)),
		"negative":       r + "s1 262144 100 " + fmt.Sprint(cost) + " -1 2 12\n",
	} {
		t.Run(name, func(t *testing.T) {
			path, ledger := openRouterLedgerFixture(t, 3)
			if err := os.WriteFile(ledger, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if loadForLedger(t, path).CheckAvailable() == nil {
				t.Fatal("untrusted settlement accepted")
			}
		})
	}
}
