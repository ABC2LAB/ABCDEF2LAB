package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
)

// productRows는 제품 transport가 쓰는 기록 형식(llmpolicy.OpenRouterCallRecord)으로 줄을 만든다.
func productRows(t *testing.T, records ...llmpolicy.OpenRouterCallRecord) []byte {
	t.Helper()
	var out bytes.Buffer
	for i, r := range records {
		r.Seq = uint64(i + 1)
		r.Method = http.MethodPost
		if r.StartedAt == "" {
			r.StartedAt, r.CompletedAt = "2026-11-16T00:00:00Z", "2026-11-16T00:00:01.5Z"
		}
		if r.RequestBytes == 0 {
			r.RequestBytes = 100
		}
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(line, '\n'))
	}
	return out.Bytes()
}

func readVerified(t *testing.T, dirPath string) verifiedMarker {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dirPath, llmVerifiedName))
	if err != nil {
		t.Fatal(err)
	}
	var m verifiedMarker
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// 제품 호출 기록은 게이트웨이와 같은 조회(generationVerifier)로 확인하고, 게이트웨이 finish()와 같은 규칙으로 센 표식을
// 쓴다. 같은 id를 게이트웨이 경로로 조회한 결과와 표식의 generation이 같다.
func TestGenerationVerifySharesGatewayLookup(t *testing.T) {
	h := newHarness(t, backendAPI)
	h.provider.mu.Lock()
	h.provider.lookups["gen-a"] = []int{http.StatusNotFound, http.StatusOK}
	h.provider.lookups["gen-b"] = []int{http.StatusOK}
	h.provider.mu.Unlock()
	rows := productRows(t,
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallForwarded, Status: 200, ResponseBytes: 10, GenerationID: "gen-a"},
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallForwarded, Status: 429, ResponseBytes: 5},
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallForwarded, Status: 200, ResponseBytes: 10, GenerationID: "gen-a"},
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallUpstreamError, Status: 200, ResponseBytes: 3, UnfinishedEnd: llmpolicy.OpenRouterEndUpstreamCutBeforeDone},
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallUpstreamError, Status: 0, UnfinishedEnd: llmpolicy.OpenRouterEndClientClosedBeforeDone, GenerationID: "gen-b"},
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallForwarded, Status: 200, ResponseBytes: 10},
	)
	sampling, _ := parseSampling(pinnedSampling)
	path, dir := privateTestDir(t)
	var sleeps []time.Duration
	v := h.g.verifier()
	v.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	if code := runGenerationVerify(dir, v, pinnedSlug, sampling, bytes.NewReader(rows), time.Now); code != "" {
		t.Fatal(code)
	}
	m := readVerified(t, path)
	wantEnds := map[string]uint64{endClientClosedAfterDone: 0, endClientClosedBeforeDone: 1, endUpstreamCutAfterDone: 0, endUpstreamCutBeforeDone: 1}
	if m.Role != roleGenerationVerify || m.Backend != backendAPI || m.Provider != pinnedSlug || !m.LogComplete ||
		m.Forwarded != 4 || m.UpstreamErrors != 2 || !reflect.DeepEqual(m.UpstreamEnds, wantEnds) || m.MissingIDs != 1 ||
		m.Rejected != 0 || m.RejectedAfterSeal != 0 || m.CancelledBySeal != 0 || len(m.Generations) != 2 || !sameSampling(t, m.Sampling, sampling) {
		t.Fatalf("marker %+v", m)
	}
	if m.Generations[0].ID != "gen-a" || m.Generations[0].Status != "verified" || m.Generations[0].ProviderName != "Example" ||
		m.Generations[1].ID != "gen-b" || len(sleeps) != 1 {
		t.Fatalf("generations %+v sleeps %v", m.Generations, sleeps)
	}
	// 같은 조회를 게이트웨이 쪽으로 한 번 더 하면 같은 결과다(같은 코드·같은 응답 해석).
	h.provider.mu.Lock()
	h.provider.lookups["gen-a"] = []int{http.StatusOK}
	h.provider.mu.Unlock()
	if got := h.g.verifier().lookup("gen-a"); !reflect.DeepEqual(got, m.Generations[0]) {
		t.Fatalf("gateway lookup %+v, verify marker %+v", got, m.Generations[0])
	}
}

// 검증할 수 없는 기록은 표식을 쓰지 않고 거부한다.
func TestGenerationVerifyRejectsMalformedRecords(t *testing.T) {
	h := newHarness(t, backendAPI)
	good := string(productRows(t,
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallForwarded, Status: 200, ResponseBytes: 10, GenerationID: "gen-a"},
		llmpolicy.OpenRouterCallRecord{Decision: llmpolicy.OpenRouterCallUpstreamError, Status: 200, ResponseBytes: 3, UnfinishedEnd: llmpolicy.OpenRouterEndUpstreamCutBeforeDone}))
	lines := strings.SplitAfter(good, "\n")
	first, second := lines[0], lines[1]
	for name, raw := range map[string]string{
		"unknown_field":         strings.Replace(first, `"seq":1,`, `"seq":1,"path":"/x",`, 1),
		"duplicate_key":         strings.Replace(first, `"seq":1,`, `"seq":1,"seq":1,`, 1),
		"missing_field":         strings.Replace(first, `"response_bytes":10,`, ``, 1),
		"seq_gap":               first + strings.Replace(second, `"seq":2`, `"seq":3`, 1),
		"seq_not_from_one":      strings.Replace(first, `"seq":1`, `"seq":2`, 1),
		"bad_decision":          strings.Replace(first, `"forwarded"`, `"rejected_policy"`, 1),
		"forwarded_with_end":    strings.Replace(first, `"generation_id":"gen-a"`, `"generation_id":"gen-a","unfinished_end":"upstream_cut_before_done"`, 1),
		"error_without_end":     first + strings.Replace(second, `,"unfinished_end":"upstream_cut_before_done"`, ``, 1),
		"unknown_end":           first + strings.Replace(second, `upstream_cut_before_done`, `upstream_gone`, 1),
		"not_post":              strings.Replace(first, `"POST"`, `"GET"`, 1),
		"bad_generation_id":     strings.Replace(first, `"gen-a"`, `"gen a/b"`, 1),
		"forwarded_status_zero": strings.Replace(first, `"status":200`, `"status":0`, 1),
		"completed_before":      strings.Replace(first, `"completed_at":"2026-11-16T00:00:01.5Z"`, `"completed_at":"2026-11-15T00:00:00Z"`, 1),
		"no_final_newline":      strings.TrimSuffix(first, "\n"),
		// 칸은 모두 맞고 JSON 공백으로 줄만 길다. 줄 상한만 거부한다.
		"line_too_long": strings.Replace(first, `"seq":1,`, `"seq":1,`+strings.Repeat(" ", 600), 1),
	} {
		t.Run(name, func(t *testing.T) {
			path, dir := privateTestDir(t)
			if code := runGenerationVerify(dir, h.g.verifier(), pinnedSlug, nil, strings.NewReader(raw), time.Now); code != invalidInput {
				t.Fatalf("code %q", code)
			}
			if _, err := os.Stat(filepath.Join(path, llmVerifiedName)); !os.IsNotExist(err) {
				t.Fatal("marker written for a rejected record")
			}
		})
	}
	// 빈 기록(모델 호출 0)은 받는다. 셀 것이 없을 뿐이다.
	path, dir := privateTestDir(t)
	if code := runGenerationVerify(dir, h.g.verifier(), pinnedSlug, nil, strings.NewReader(""), time.Now); code != "" {
		t.Fatal(code)
	}
	if m := readVerified(t, path); m.Forwarded != 0 || len(m.Generations) != 0 || m.Sampling != nil {
		t.Fatalf("empty record marker %+v", m)
	}
}

// 역할 설정: 키는 이 역할과 게이트웨이만 받는다. 리슨·allow-path·backend를 주거나 키·조회 칸이 틀리면 시작하지 않는다.
func TestGenerationVerifyFlags(t *testing.T) {
	path, _ := privateTestDir(t)
	base := []string{"-role", roleGenerationVerify, "-upstream", "https://openrouter.ai", "-provider", pinnedSlug, "-generation-path", genPath,
		"-verify-attempts", "3", "-verify-interval", "1s", "-log-dir", path}
	t.Setenv(llmKeyEnv, canaryKey)
	// 기준 설정은 그대로 통과한다(아래 거부가 다른 이유로 난 것이 아니게). 빈 기록이라 조회하지 않는다.
	if code := run(base, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("valid generation-verify flags refused", code)
	}
	if err := os.Remove(filepath.Join(path, llmVerifiedName)); err != nil {
		t.Fatal("marker not written by the valid run", err)
	}
	// 제품이 실제로 보낸 샘플링(-sampling)은 표식에 그대로 실린다. 주지 않으면 null이다(위 실행).
	if code := run(append(append([]string{}, base...), "-sampling", pinnedSampling), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("valid sampling refused", code)
	}
	want, _ := parseSampling(pinnedSampling)
	if m := readVerified(t, path); !sameSampling(t, m.Sampling, want) {
		t.Fatalf("marker sampling %v", m.Sampling)
	}
	if err := os.Remove(filepath.Join(path, llmVerifiedName)); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string][]string{
		"listen": {"-listen", "127.0.0.1:0"}, "allow_path": {"-allow-path", chatPath}, "backend": {"-backend", backendAPI},
		"http_upstream": {"-upstream", "http://openrouter.ai"}, "bad_sampling": {"-sampling", `{"temperature":1}`},
		"no_attempts": {"-verify-attempts", "0"},
	} {
		var stderr bytes.Buffer
		if code := run(append(append([]string{}, base...), extra...), strings.NewReader(""), &bytes.Buffer{}, &stderr); code == 0 {
			t.Errorf("%s accepted", name)
		}
		if strings.Contains(stderr.String(), canaryKey) {
			t.Fatal("key echoed")
		}
	}
	t.Setenv(llmKeyEnv, "")
	if code := run(base, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Error("generation-verify without a key accepted")
	}
}
