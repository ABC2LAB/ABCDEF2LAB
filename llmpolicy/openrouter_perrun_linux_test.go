//go:build linux

package llmpolicy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// perRunFixture는 v4(점검당 총량) 또는 v3(승인 누적) manifest를 보호 디렉터리에 쓴다. 승인 하나에 요청 2번까지다.
func perRunFixture(t *testing.T, version int) (string, string) {
	t.Helper()
	routing := ""
	if version == 4 {
		routing = pinnedRoutingJSON
	}
	p := samplingPolicy(t, routingManifest(t, version, routing))
	p.manifest.MaxRequests = 2
	p.manifest.MaxInputTokens = 2 * openRouterContext
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
	return path, fmt.Sprintf("%x", sha256.Sum256([]byte(p.manifest.ApprovalID)))
}

// reserveFree는 예약 하나를 하고 동시 호출 자리를 바로 푼다(여러 번 예약하는 시험용, openrouter_test.go와 같은 방식).
func reserveFree(p *OpenRouterPolicy) error {
	err := p.reserve(100)
	if err == nil {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}
	return err
}

func loadForRun(t *testing.T, path, run string) *OpenRouterPolicy {
	t.Helper()
	p, err := LoadOpenRouterManifestFileForRun(path, run)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// v4 승인은 점검마다 장부를 따로 센다. 한 점검이 총량을 다 써도 다른 점검은 새로 시작하고, 같은 점검을 다시 읽으면
// (재시작) 같은 장부를 이어 센다. 다 쓴 점검의 장부는 지우지 않고 남는다.
func TestOpenRouterPerRunLedgerV4(t *testing.T) {
	path, approval := perRunFixture(t, 4)
	a := loadForRun(t, path, "job-a")
	if !a.PerRunTotals() || reserveFree(a) != nil || reserveFree(a) != nil {
		t.Fatalf("점검 A 예약: %+v", a.Snapshot())
	}
	if err := reserveFree(a); err == nil {
		t.Fatal("점검 A가 점검당 총량을 넘음")
	}
	again := loadForRun(t, path, "job-a")
	if again.CheckAvailable() == nil || reserveFree(again) == nil {
		t.Fatal("같은 점검을 다시 읽자 장부가 비워짐")
	}
	b := loadForRun(t, path, "job-b")
	if b.CheckAvailable() != nil || reserveFree(b) != nil {
		t.Fatalf("다른 점검이 앞 점검 장부에 막힘: %+v", b.Snapshot())
	}
	// 새 점검 확인(run 없음)은 승인 크기만 보고, 예약은 거부한다.
	probe := loadForRun(t, path, "")
	if probe.CheckAvailable() != nil {
		t.Fatal("새 점검 확인이 다른 점검 장부에 막힘")
	}
	if err := reserveFree(probe); err == nil || probe.Snapshot().Requests != 0 {
		t.Fatal("run 없는 정책이 예약함")
	}
	dir := filepath.Dir(path)
	entries, _ := os.ReadDir(dir)
	var ledgers []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ledger") {
			ledgers = append(ledgers, e.Name())
		}
	}
	if len(ledgers) != 2 {
		t.Fatalf("점검별 장부 파일: %v", ledgers)
	}
	for _, name := range ledgers {
		if !strings.HasPrefix(name, ".openrouter-"+approval+".run-") {
			t.Fatalf("점검 장부 이름: %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".openrouter-"+approval+".ledger")); !os.IsNotExist(err) {
		t.Fatal("v4 점검이 승인 누적 장부를 씀")
	}
	// 승인 누적 경로(CLI)는 v4 승인도 지금처럼 누적 장부를 쓴다.
	cumulative, err := LoadOpenRouterManifestFile(path)
	if err != nil || reserveFree(cumulative) != nil {
		t.Fatal("누적 로더 예약 실패", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".openrouter-"+approval+".ledger")); err != nil {
		t.Fatal("누적 로더가 승인 장부를 쓰지 않음")
	}
}

// v1~v3 승인은 점검 로더로 읽어도 승인 하나를 누적해 센다(평가 러너·CLI가 기대는 이전 동작).
func TestOpenRouterPerRunLoaderKeepsCumulativeBeforeV4(t *testing.T) {
	path, approval := perRunFixture(t, 3)
	a := loadForRun(t, path, "job-a")
	if a.PerRunTotals() || reserveFree(a) != nil || reserveFree(a) != nil {
		t.Fatalf("v3 예약: %+v", a.Snapshot())
	}
	b := loadForRun(t, path, "job-b")
	if b.CheckAvailable() == nil || reserveFree(b) == nil {
		t.Fatal("v3 승인이 점검마다 새로 셈")
	}
	if loadForRun(t, path, "").CheckAvailable() == nil {
		t.Fatal("v3 새 점검 확인이 누적 장부를 보지 않음")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".openrouter-"+approval+".ledger")); err != nil {
		t.Fatal("v3 승인 장부 이름이 바뀜")
	}
}

// 이전 1회용 표식이 있는 승인은 v4 점검 로더로도 다 쓴 승인이다. 점검 참조 모양도 본다.
func TestOpenRouterPerRunHonoursLegacyMarkerAndRunRef(t *testing.T) {
	path, approval := perRunFixture(t, 4)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".openrouter-"+approval+".used"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if loadForRun(t, path, "job-a").CheckAvailable() == nil || loadForRun(t, path, "").CheckAvailable() == nil {
		t.Fatal("1회용 표식 승인을 점검 로더가 받음")
	}
	for _, run := range []string{"bad\x00run", strings.Repeat("r", openRouterRunRefMax+1)} {
		if _, err := LoadOpenRouterManifestFileForRun(path, run); err == nil {
			t.Fatalf("점검 참조 %q를 받음", run[:8])
		}
	}
}
