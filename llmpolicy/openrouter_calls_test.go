package llmpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const openRouterReplyWithID = `{"id":"gen-1700000000-AbCd_12","model":"qwen/qwen3.6-35b-a3b","provider":"Fixture","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"cost":0.000012}}`

// recordingFixture는 호출 기록을 메모리에 모으는 정책이다(보호 파일 로더 대신).
func recordingFixture(t *testing.T) (*OpenRouterPolicy, func() []OpenRouterCallRecord) {
	t.Helper()
	p := openRouterFixture(t)
	var mu sync.Mutex
	var records []OpenRouterCallRecord
	p.callLog = func(r OpenRouterCallRecord) error {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, r)
		return nil
	}
	return p, func() []OpenRouterCallRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]OpenRouterCallRecord(nil), records...)
	}
}

// cutReader는 앞부분을 준 뒤 위쪽 끊김처럼 끝난다.
type cutReader struct{ r io.Reader }

func (c *cutReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

// waitReader는 요청 문맥이 끝날 때까지 기다렸다가 그 오류로 끝난다(호출자 취소).
type waitReader struct{ ctx context.Context }

func (w waitReader) Read([]byte) (int, error) { <-w.ctx.Done(); return 0, w.ctx.Err() }

// 호출 기록은 게이트웨이 llm-requests 행과 같은 결정·끝 모양 어휘로 남는다. 상태 코드가 무엇이든 본문을 끝까지 받았으면
// forwarded이고, 끝까지 받지 못하면 upstream_error와 끝 모양이다. generation id는 끝까지 읽은 JSON의 id만, 모양이 맞을
// 때만 남긴다.
func TestOpenRouterCallRecordsUseGatewayVocabulary(t *testing.T) {
	big := strings.Repeat("x", openRouterResponseLimit+10)
	badShape := strings.Replace(openRouterReplyWithID, "gen-1700000000-AbCd_12", "gen id/with space", 1)
	cases := []struct {
		name                 string
		respond              func(r *http.Request, cancel context.CancelFunc) (*http.Response, error)
		decision, end, genID string
		status               int
		bytes                int64
	}{
		{"ok", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return openRouterResponse(openRouterReplyWithID), nil
		}, OpenRouterCallForwarded, "", "gen-1700000000-AbCd_12", 200, int64(len(openRouterReplyWithID))},
		{"bad_id_shape", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return openRouterResponse(badShape), nil
		}, OpenRouterCallForwarded, "", "", 200, int64(len(badShape))},
		{"rate_limited", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":{"code":429}}`)), Header: make(http.Header)}, nil
		}, OpenRouterCallForwarded, "", "", 429, int64(len(`{"error":{"code":429}}`))},
		{"server_error", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("down")), Header: make(http.Header)}, nil
		}, OpenRouterCallForwarded, "", "", 503, 4},
		{"upstream_cut", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(&cutReader{strings.NewReader(openRouterReplyWithID[:20])}), Header: make(http.Header)}, nil
		}, OpenRouterCallUpstreamError, OpenRouterEndUpstreamCutBeforeDone, "", 200, 20},
		{"transport_error", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return nil, errors.New("connection reset by peer")
		}, OpenRouterCallUpstreamError, OpenRouterEndUpstreamCutBeforeDone, "", 0, 0},
		{"caller_cancelled", func(r *http.Request, cancel context.CancelFunc) (*http.Response, error) {
			cancel()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(waitReader{r.Context()}), Header: make(http.Header)}, nil
		}, OpenRouterCallUpstreamError, OpenRouterEndClientClosedBeforeDone, "", 200, 0},
		{"over_limit", func(*http.Request, context.CancelFunc) (*http.Response, error) {
			return openRouterResponse(big), nil
		}, OpenRouterCallUpstreamError, OpenRouterEndClientClosedBeforeDone, "", 200, openRouterResponseLimit + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, records := recordingFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) { return tc.respond(r, cancel) }))
			resp, err := c.Do(openRouterRequest().WithContext(ctx))
			if err == nil {
				resp.Body.Close()
			}
			got := records()
			if len(got) != 1 {
				t.Fatalf("records %+v", got)
			}
			r := got[0]
			if r.Decision != tc.decision || r.UnfinishedEnd != tc.end || r.GenerationID != tc.genID || r.Status != tc.status ||
				r.ResponseBytes != tc.bytes || r.Method != http.MethodPost || r.RequestBytes <= 0 || r.StartedAt == "" || r.CompletedAt == "" {
				t.Fatalf("record %+v", r)
			}
		})
	}
}

// 기록은 전송을 바꾸지 않는다: 호출마다 위쪽 요청은 openrouter.ai의 고정 끝점 하나뿐이고(generation 조회 같은 추가 요청
// 없음), 호출자는 위쪽 본문을 바이트 그대로 받는다. 기록을 남기지 못해도 호출 결과는 같고 실패 수만 는다. 기록 칸은
// 사용량 요약(OpenRouterUsage)에 섞이지 않는다.
func TestOpenRouterCallRecordDoesNotChangeTheWire(t *testing.T) {
	p, records := recordingFixture(t)
	fail := true
	inner := p.callLog
	p.callLog = func(r OpenRouterCallRecord) error {
		if fail {
			fail = false
			return errors.New("disk full")
		}
		return inner(r)
	}
	var upstream []string
	c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		upstream = append(upstream, r.Method+" "+r.URL.String()+" host="+r.URL.Host)
		return openRouterResponse(openRouterReplyWithID), nil
	}))
	for i := 0; i < 2; i++ {
		resp, err := c.Do(openRouterRequest())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(body, []byte(openRouterReplyWithID)) {
			t.Fatal("caller did not receive the upstream body unchanged")
		}
	}
	want := "POST " + openRouterEndpoint + " host=openrouter.ai"
	if len(upstream) != 2 || upstream[0] != want || upstream[1] != want {
		t.Fatalf("upstream requests %v", upstream)
	}
	if p.CallRecordFailures() != 1 || len(records()) != 1 || p.Snapshot().Requests != 2 || p.Snapshot().Blocked {
		t.Fatalf("failures %d records %d usage %+v", p.CallRecordFailures(), len(records()), p.Snapshot())
	}
	raw, _ := json.Marshal(p.Snapshot())
	for _, field := range []string{"generation", "seq", "unfinished_end", "decision"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("usage summary carries call record field %q", field)
		}
	}
}

func TestOpenRouterCallRecordFieldsMatchStruct(t *testing.T) {
	raw, _ := json.Marshal(OpenRouterCallRecord{UnfinishedEnd: OpenRouterEndUpstreamCutBeforeDone})
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if len(fields) != len(OpenRouterCallRecordFields) {
		t.Fatalf("fields %v want %v", fields, OpenRouterCallRecordFields)
	}
	for _, name := range OpenRouterCallRecordFields {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing field %s", name)
		}
	}
}
