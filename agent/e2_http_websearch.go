package agent

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// e2HTTPWebSearchToolName은 원본 검색 도구 이름이다(SDK web_search). 진단 프로필에서 bound 항목으로만 채운다.
const e2HTTPWebSearchToolName = "web_search"

// E2HTTPWebSearch는 진단 Planner·Worker에 줄 원본 검색 도구(web_search)의 설정이다(루프 복원 결정안 §2-1). 외부
// API(OpenRouter)만 쓰는 설치에서 설치 입력 web_search를 켰을 때만 엔진이 채운다. 비어 있으면(Backend "") 도구가 없다.
// 검색은 대상이 아니라 검색 제공자로 나가고 E②를 거치지 않는다(대상 요청이 아니다). 키는 제어 비밀이라 모델 입력·로그에
// 넣지 않는다(규칙 2).
type E2HTTPWebSearch struct {
	// Backend는 원본 SDK 백엔드 이름이다: "ddgs"(DuckDuckGo, 키 없음), "brave-free", "tavily". 모델 호출로 검색하는
	// "deepseek"은 받지 않는다(모델 예산·제공자 밖 호출).
	Backend   string
	BraveKey  string
	TavilyKey string
}

func (E2HTTPWebSearch) String() string   { return "agent.E2HTTPWebSearch[protected]" }
func (E2HTTPWebSearch) GoString() string { return "agent.E2HTTPWebSearch[protected]" }

// Valid는 설정이 끔이거나 백엔드에 맞는 키만 있는지다. 다른 백엔드의 키가 섞여 있으면 거부한다.
func (w E2HTTPWebSearch) Valid() bool {
	switch w.Backend {
	case "":
		return w.BraveKey == "" && w.TavilyKey == ""
	case "ddgs":
		return w.BraveKey == "" && w.TavilyKey == ""
	case "brave-free":
		return w.BraveKey != "" && w.TavilyKey == ""
	case "tavily":
		return w.TavilyKey != "" && w.BraveKey == ""
	}
	return false
}

// Enabled는 검색 도구를 줄지다.
func (w E2HTTPWebSearch) Enabled() bool { return w.Backend != "" && w.Valid() }

type e2HTTPWebSearchKey struct{}

// WithE2HTTPWebSearch는 진단 Planner·Worker가 쓸 검색 설정을 문맥에 싣는다. 없거나 끔이면 도구가 없다.
func WithE2HTTPWebSearch(ctx context.Context, w E2HTTPWebSearch) context.Context {
	return context.WithValue(ctx, e2HTTPWebSearchKey{}, w)
}

func e2HTTPWebSearchFrom(ctx context.Context) E2HTTPWebSearch {
	if ctx == nil {
		return E2HTTPWebSearch{}
	}
	w, _ := ctx.Value(e2HTTPWebSearchKey{}).(E2HTTPWebSearch)
	return w
}

// newE2HTTPWebSearchTool은 원본 web_search(이름·입력·결과 형식 그대로)를 만든다. 설정이 끔이면 nil이다. redact가
// 있으면(진단 Worker) 검색어에 이 실행의 보호 값(시험 계정 세션·비밀번호·판정 표식 등, 실행 가림이 바꾸는 값)이 있을 때
// 보내지 않고, 결과 글에도 같은 가림을 건다. 검색 제공자는 대상이 아니므로 그 값을 보낼 이유가 없다.
func newE2HTTPWebSearchTool(w E2HTTPWebSearch, redact func([]byte) []byte) (actool.CoreTool, error) {
	if !w.Valid() {
		return nil, ErrE2HTTPBinding
	}
	if w.Backend == "" {
		return nil, nil
	}
	tool, err := actool.NewWebSearch(actool.WebSearchConfig{Backend: w.Backend, BraveAPIKey: w.BraveKey, TavilyAPIKey: w.TavilyKey})
	if err != nil || tool == nil || tool.Name() != e2HTTPWebSearchToolName {
		return nil, ErrE2HTTPBinding
	}
	if redact == nil {
		return tool, nil
	}
	return &e2HTTPWebSearchTool{CoreTool: tool, redact: redact}, nil
}

type e2HTTPWebSearchTool struct {
	actool.CoreTool
	redact func([]byte) []byte
}

func (t *e2HTTPWebSearchTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if !bytes.Equal(t.redact(bytes.Clone(input)), input) {
		return actool.Errorf("Error: the search query contains a protected value of this run (session, credential or marker) and was not sent. Remove it and search again."), nil
	}
	result, err := t.CoreTool.Call(ctx, input, tc)
	for i, block := range result.Content {
		if block.Type == llm.BlockText {
			result.Content[i].Text = string(t.redact([]byte(block.Text)))
		}
	}
	return result, err
}
