package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSelectedCandidateFullFlowEvalLocalModel은 구조 근거 분석 → 분석 제안을 담은 최종 입력 → v2 게이트의 전체 흐름을 승인된
// 로컬 모델로 잰다(사례당 모델 호출 2회, 대상 송신 0). 모델이 없으면 SKIP이며 실제 품질은 미측정이다. 모의 모델로는 분석 제안이
// 최종 판정을 바꾸지 않아 gate_v2 코호트와 같으므로 따로 돌리지 않는다.
func TestSelectedCandidateFullFlowEvalLocalModel(t *testing.T) {
	endpoint, model := os.Getenv("ARTEX_SELECTED_EVAL_MODEL_URL"), os.Getenv("ARTEX_SELECTED_EVAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("local model not configured; full-flow quality not measured")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" || u.User != nil || u.RawQuery != "" ||
		!strings.HasSuffix(u.Path, "/chat/completions") {
		t.Fatal("only an explicit loopback chat/completions endpoint is allowed")
	}
	client := &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	analyze := func(t *testing.T, system, user string, usage *evalUsage) string {
		body, _ := json.Marshal(map[string]any{"model": model, "temperature": 0, "max_tokens": 2048, "messages": []map[string]string{
			{"role": "system", "content": system}, {"role": "user", "content": user}}})
		started := time.Now()
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
		usage.Calls++
		if err != nil {
			t.Fatalf("local model unavailable: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		usage.Elapsed += time.Since(started)
		var payload struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &payload) != nil || len(payload.Choices) == 0 {
			t.Fatalf("local model response invalid (%d)", resp.StatusCode)
		}
		usage.InputTokens += payload.Usage.Prompt
		usage.OutputTokens += payload.Usage.Completion
		text := strings.TrimSpace(payload.Choices[0].Message.Content)
		if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
			text = text[i : j+1]
		}
		return text
	}
	final := localModel(endpoint, model)
	cases := append(loadEvalCases(t), loadGateV2EvalCases(t)...)
	rows := map[string]string{}
	var usage evalUsage
	for _, c := range cases {
		selected := analysisEvalContext(t, c, false)
		request, err := BuildCandidateAnalysisRequest(selected, 2048)
		if err != nil {
			t.Fatal(err)
		}
		ctx := evalContext(t, c, true, candidateEvidenceGateV2)
		if proposal, err := ParseCandidateAnalysisProposal([]byte(analyze(t, strings.Join(request.System, "\n"), request.Messages[0].Content[0].Text, &usage)), selected); err == nil {
			// 분석 제안은 최종 입력의 analysis로만 들어간다(실행 행동은 사례의 고정 후속 관찰을 쓴다: 대상 송신 없음).
			ctx.Analysis.Proposal = proposal
		} else {
			usage.Invalid++
		}
		advice, ok := evalModelOutput(final(t, c, ctx, &usage))
		product := "insufficient"
		if ok {
			raw, _ := json.Marshal(advice)
			if parsed, err := ParseCandidateFinalVerdict(raw, ctx); err == nil {
				product = evalLabel(parsed.Verdict)
			}
		}
		rows[c.ID] = product
	}
	report := evalReport{Mode: "local_model_full_flow", Model: model, Totals: map[string]evalCounts{"full_flow_product": tally(cases, rows)},
		Rows: map[string]map[string]string{"full_flow_product": rows}, Usage: map[string]evalUsage{"full_flow": usage},
		Note: "Structured analysis then gate v2 final verdict. Local loopback model, no target requests."}
	writeEvalReport(t, report)
}
