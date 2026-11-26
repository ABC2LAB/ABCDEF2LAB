package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 설치 단위 외부 전송 동의 기록(사용자 결정 2026-11-26): 한 번 남기면 바꾸지 않고, 지금 문장의 동의만 동의로 읽는다.
func TestProductExternalConsentRecordOnce(t *testing.T) {
	dir := t.TempDir()
	if _, err := readProductExternalConsent(dir); !errors.Is(err, errExternalConsentMissing) {
		t.Fatalf("empty state read as %v", err)
	}
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	first, err := writeProductExternalConsent(dir, productExternalConsentInstall, "ARTEX", at)
	if err != nil || first.Source != productExternalConsentInstall || first.StatementSHA256 != productExternalTransferStatementSHA256() || !first.At.Equal(at) {
		t.Fatalf("install consent: %+v %v", first, err)
	}
	// 먼저 남긴 동의가 이긴다: 관리자가 다시 동의해도 설치 기록 그대로다.
	again, err := writeProductExternalConsent(dir, productExternalConsentAdmin, "ARTEX", at.Add(time.Hour))
	if err != nil || again != first {
		t.Fatalf("second consent replaced the first: %+v %v", again, err)
	}
	info, err := os.Stat(productExternalConsentPath(dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("consent file mode: %v %v", info, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary consent files left: %d", len(entries))
	}
	// 기록에는 해시 · 출처 · 주체 · 시각만 있다(문장 원문 없음).
	raw, _ := os.ReadFile(productExternalConsentPath(dir))
	if strings.Contains(string(raw), "OpenRouter") || strings.Contains(string(raw), "statement\"") {
		t.Fatalf("consent record carries more than the digest: %s", raw)
	}
}

func TestProductExternalConsentRejectsTamperedRecord(t *testing.T) {
	good := productExternalConsentRecord{Format: productExternalConsentFormat, StatementSHA256: productExternalTransferStatementSHA256(), Source: productExternalConsentAdmin, Actor: "ARTEX", At: time.Now().UTC()}
	for name, change := range map[string]func(*productExternalConsentRecord){
		"other statement": func(r *productExternalConsentRecord) { r.StatementSHA256 = strings.Repeat("0", 64) },
		"unknown source":  func(r *productExternalConsentRecord) { r.Source = "runner" },
		"bad actor":       func(r *productExternalConsentRecord) { r.Actor = "a b" },
		"zero time":       func(r *productExternalConsentRecord) { r.At = time.Time{} },
		"old format":      func(r *productExternalConsentRecord) { r.Format = "abc2lab.external-transfer-consent/v0" },
	} {
		dir := t.TempDir()
		rec := good
		change(&rec)
		raw, _ := json.Marshal(rec)
		if err := os.WriteFile(productExternalConsentPath(dir), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		// 형식이 틀린 기록은 동의 없음으로도, 동의로도 읽지 않는다(외부 회차는 시작하지 않음).
		if _, err := readProductExternalConsent(dir); err == nil || errors.Is(err, errExternalConsentMissing) {
			t.Errorf("%s: tampered record read as %v", name, err)
		}
		if _, err := writeProductExternalConsent(dir, productExternalConsentAdmin, "ARTEX", time.Now()); err == nil {
			t.Errorf("%s: tampered record overwritten", name)
		}
	}
	// 다른 사람도 읽을 수 있는 권한이면 거부한다.
	dir := t.TempDir()
	raw, _ := json.Marshal(good)
	if err := os.WriteFile(productExternalConsentPath(dir), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProductExternalConsent(dir); err == nil {
		t.Error("world readable consent accepted")
	}
	// 알 수 없는 칸이 있으면 거부한다.
	extra := strings.Replace(string(raw), "{", `{"statement":"x",`, 1)
	dir = t.TempDir()
	if err := os.WriteFile(productExternalConsentPath(dir), []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProductExternalConsent(dir); err == nil {
		t.Error("consent with unknown field accepted")
	}
}

// 문장이 바뀌면 파일 이름이 달라 옛 문장의 동의는 지금 동의가 아니다.
func TestProductExternalConsentPathFollowsStatement(t *testing.T) {
	name := filepath.Base(productExternalConsentPath("/state"))
	if name != "external-transfer-consent-"+productExternalTransferStatementSHA256()[:productExternalConsentNameDigest]+".json" {
		t.Fatalf("consent path %s", name)
	}
}

// 설치 입력의 동의 칸은 외부 모델이 있는 설치이고 지금 문장과 글자 그대로 같을 때만 받는다.
func TestProductInstallConsentInput(t *testing.T) {
	manifest := json.RawMessage(`{"version":1}`)
	cases := []struct {
		name  string
		input productInstallInput
		ok    bool
	}{
		{"absent", productInstallInput{}, true},
		{"absent with external", productInstallInput{OpenRouterManifest: manifest}, true},
		{"exact", productInstallInput{OpenRouterManifest: manifest, ExternalTransferConsent: &productInstallConsent{Statement: productExternalTransferStatement}}, true},
		{"local only", productInstallInput{ExternalTransferConsent: &productInstallConsent{Statement: productExternalTransferStatement}}, false},
		{"other text", productInstallInput{OpenRouterManifest: manifest, ExternalTransferConsent: &productInstallConsent{Statement: productExternalTransferStatement + " "}}, false},
		{"old statement", productInstallInput{OpenRouterManifest: manifest, ExternalTransferConsent: &productInstallConsent{Statement: productExternalConsentStatementV2}}, false},
		{"empty", productInstallInput{OpenRouterManifest: manifest, ExternalTransferConsent: &productInstallConsent{}}, false},
	}
	for _, c := range cases {
		if got := validProductInstallConsent(c.input); got != c.ok {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	// 동의 문장은 DLP 폐기 뒤 가는 것(대상 응답 · 시험 계정 아이디 · 비밀번호 · 세션)과 책임 주체를 그대로 적는다.
	for _, part := range []string{"대상 응답", "아이디 · 비밀번호", "세션(쿠키 · 토큰)", "가리지 않고", "시험 계정만", "책임은 설치 조직"} {
		if !strings.Contains(productExternalTransferStatement, part) {
			t.Errorf("statement misses %q", part)
		}
	}
}
