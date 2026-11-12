package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"golang.org/x/net/html"
)

// e2HTTPWebFetchTimeoutSeconds는 E② WebFetch가 curl에 거는 응답 시간 상한이다. 제품 흐름의 정적 크롤과 같은 값이다.
const e2HTTPWebFetchTimeoutSeconds = "15"

// e2HTTPWebFetchStaticExts는 원본 WebFetch처럼 페이지가 아닌 정적 자원(스크립트·스타일·이미지·글꼴·미디어)을 거부한다.
var e2HTTPWebFetchStaticExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".webp": true, ".avif": true, ".ico": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".wasm": true, ".mp4": true, ".webm": true, ".mp3": true, ".wav": true, ".ogg": true,
}

// newE2HTTPWebFetchTool은 원본 WebFetch(이름·입력 형식이 같다)의 진단 판이다(루프 복원 결정안 §2). 가져오기는
// 넘겨받은 E② Bash 도구로 curl을 돌려 같은 샌드박스 실행기·intent 예약·감사·호출 상한을 지난다. 서버 프로세스가
// 대상에 직접 보내지 않는다. 호스트는 가려진 결과의 HTML을 읽기 쉬운 글로 바꾸기만 하고, trusted 관찰은 그대로 둔다.
// redact는 변환한 글에 한 번 더 건다. 파싱이 HTML 엔티티를 풀면, 엔티티로 인코딩돼 정확한 값 가림을 지나간 자격값이
// 평문으로 나오기 때문이다.
func newE2HTTPWebFetchTool(bash actool.CoreTool, redact func([]byte) []byte) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "WebFetch",
		Description: "Fetches an HTTP(S) HTML page, documentation page, or API response through the bound E2 execution and returns its content as Markdown in tool_output, with the same trusted call fields as Bash. Do not use this tool for static assets (JavaScript, CSS, source maps, images, fonts, audio, video). Set extract=true to list comments, scripts, endpoints, forms (including hidden inputs), links, and meta tags. Large responses may be truncated. Each call is one E2 request.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string", "description": "The absolute http(s) URL to fetch. Static-asset suffix URLs are not allowed."},
				"extract": map[string]any{"type": "boolean", "description": "When true, append a RECON section with HTML comments, script sources, inline-script endpoints, forms/inputs (including hidden), links and meta tags. Default false."},
			},
			"required": []any{"url"},
		},
		// 송신 허용은 이 도구가 아니라 E②가 정한다. 호출은 Bash처럼 하나씩 한다.
		Concurrent: func(json.RawMessage) bool { return false },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var in struct {
				URL     string `json:"url"`
				Extract bool   `json:"extract"`
			}
			if json.Unmarshal(input, &in) != nil {
				return actool.Errorf("e2_http_webfetch_input_invalid"), nil
			}
			target, ok := e2HTTPWebFetchURL(in.URL)
			if !ok {
				return actool.Errorf("e2_http_webfetch_url_invalid"), nil
			}
			// URL은 모델 입력이므로 따로 셸 인용한다. 송신 허용은 E②가 정한다.
			command := "curl --silent --show-error --globoff --include --suppress-connect-headers --max-redirs 0 --max-time " + e2HTTPWebFetchTimeoutSeconds +
				" --url '" + strings.ReplaceAll(target, "'", `'"'"'`) + "'"
			raw, _ := json.Marshal(map[string]any{"command": command})
			result, err := bash.Call(ctx, raw, tc)
			if err != nil || result.IsError {
				return result, err
			}
			return e2HTTPWebFetchRender(result, in.Extract, redact), nil
		},
	})
}

// e2HTTPWebFetchURL은 절대 http(s) URL만 받는다. 제어 문자·공백·정적 자원은 거부한다.
func e2HTTPWebFetchURL(raw string) (string, bool) {
	if raw == "" || len(raw) > 4096 || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	if e2HTTPWebFetchStaticExts[strings.ToLower(path.Ext(u.Path))] {
		return "", false
	}
	return u.String(), true
}

// e2HTTPWebFetchRender는 Bash 결과 JSON의 tool_output(가려진 curl 출력: 헤더 + 본문)만 바꾼다. trusted 관찰과
// worker_control은 그대로 둔다. 바꿀 수 없으면 원래 결과를 돌려준다.
func e2HTTPWebFetchRender(result actool.Result, extract bool, redact func([]byte) []byte) actool.Result {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(result.Flatten()), &fields) != nil {
		return result
	}
	var output string
	if json.Unmarshal(fields["tool_output"], &output) != nil {
		return result
	}
	status, headers, body := e2HTTPWebFetchSplit(output)
	rendered := body
	if strings.Contains(strings.ToLower(headers), "text/html") || strings.HasPrefix(strings.TrimSpace(strings.ToLower(body)), "<!doctype html") || strings.HasPrefix(strings.TrimSpace(strings.ToLower(body)), "<html") {
		if doc, err := html.Parse(strings.NewReader(body)); err == nil {
			rendered = e2HTTPHTMLMarkdown(doc)
			if extract {
				rendered += e2HTTPHTMLRecon(doc)
			}
		}
	}
	if status != "" {
		rendered = status + "\n\n" + rendered
	}
	if redact == nil {
		return result
	}
	rendered = string(redact([]byte(rendered)))
	encoded, err := json.Marshal(rendered)
	if err != nil {
		return result
	}
	fields["tool_output"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return result
	}
	text := actool.Text(string(out))
	text.IsError = result.IsError
	return text
}

// e2HTTPWebFetchSplit은 curl --include 출력에서 마지막 응답의 상태 줄·헤더와 본문을 나눈다(1xx 중간 응답은 건너뜀).
func e2HTTPWebFetchSplit(output string) (status, headers, body string) {
	rest := output
	for strings.HasPrefix(rest, "HTTP/") {
		sep := "\r\n\r\n"
		i := strings.Index(rest, sep)
		if i < 0 {
			sep = "\n\n"
			if i = strings.Index(rest, sep); i < 0 {
				return "", "", output
			}
		}
		block := rest[:i]
		rest = rest[i+len(sep):]
		status, headers = strings.SplitN(block, "\n", 2)[0], block
		if !strings.HasPrefix(rest, "HTTP/") {
			break
		}
	}
	return strings.TrimSpace(status), headers, rest
}

var e2HTTPBlockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "header": true, "footer": true, "main": true, "nav": true,
	"aside": true, "table": true, "tr": true, "ul": true, "ol": true, "form": true, "blockquote": true, "br": true, "hr": true,
}
var e2HTTPSkipTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true, "head": true}

// e2HTTPHTMLMarkdown은 보이는 글을 마크다운으로 옮긴다. 제목·목록·링크·코드만 살리고 나머지는 글로 둔다.
func e2HTTPHTMLMarkdown(doc *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			if text := strings.Join(strings.Fields(n.Data), " "); text != "" {
				if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") && !strings.HasSuffix(b.String(), " ") {
					b.WriteByte(' ')
				}
				b.WriteString(text)
			}
			return
		case html.ElementNode:
			if e2HTTPSkipTags[n.Data] {
				return
			}
			switch n.Data {
			case "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
				e2HTTPHTMLChildren(n, walk)
				b.WriteString("\n\n")
				return
			case "li":
				b.WriteString("\n- ")
				e2HTTPHTMLChildren(n, walk)
				return
			case "a":
				var text strings.Builder
				e2HTTPHTMLText(&text, n)
				if href := e2HTTPHTMLAttr(n, "href"); href != "" {
					b.WriteString(" [" + strings.TrimSpace(text.String()) + "](" + href + ")")
				} else {
					b.WriteString(" " + strings.TrimSpace(text.String()))
				}
				return
			case "pre", "code":
				var text strings.Builder
				e2HTTPHTMLText(&text, n)
				b.WriteString(" `" + strings.TrimSpace(text.String()) + "`")
				return
			}
			if e2HTTPBlockTags[n.Data] {
				b.WriteString("\n")
				e2HTTPHTMLChildren(n, walk)
				b.WriteString("\n")
				return
			}
		}
		e2HTTPHTMLChildren(n, walk)
	}
	walk(doc)
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func e2HTTPHTMLChildren(n *html.Node, walk func(*html.Node)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
}

func e2HTTPHTMLText(b *strings.Builder, n *html.Node) {
	if n.Type == html.TextNode {
		b.WriteString(strings.Join(strings.Fields(n.Data), " ") + " ")
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		e2HTTPHTMLText(b, c)
	}
}

func e2HTTPHTMLAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

// 인라인 스크립트의 경로처럼 보이는 문자열. 확인된 엔드포인트가 아니라 단서다.
var e2HTTPInlineEndpoint = regexp.MustCompile(`["'` + "`" + `](/[A-Za-z0-9_\-./]*[A-Za-z0-9_\-][A-Za-z0-9_\-./?=&{}:]*)["'` + "`" + `]`)

// e2HTTPHTMLRecon은 마크다운에서 빠지는 정찰 단서(주석·스크립트·폼·숨은 입력·링크·meta)를 따로 적는다.
func e2HTTPHTMLRecon(doc *html.Node) string {
	var comments, scripts, endpoints, forms, links, metas []string
	seen := map[string]bool{}
	add := func(list *[]string, value string) {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			*list = append(*list, value)
		}
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.CommentNode:
			add(&comments, strings.Join(strings.Fields(n.Data), " "))
		case html.ElementNode:
			switch n.Data {
			case "script":
				if src := e2HTTPHTMLAttr(n, "src"); src != "" {
					add(&scripts, src)
				} else if n.FirstChild != nil {
					for _, m := range e2HTTPInlineEndpoint.FindAllStringSubmatch(n.FirstChild.Data, -1) {
						add(&endpoints, m[1])
					}
				}
			case "link":
				add(&links, e2HTTPHTMLAttr(n, "href"))
			case "a":
				add(&links, e2HTTPHTMLAttr(n, "href"))
			case "meta":
				if name := e2HTTPHTMLAttr(n, "name") + e2HTTPHTMLAttr(n, "property"); name != "" {
					add(&metas, name+"="+e2HTTPHTMLAttr(n, "content"))
				}
			case "form":
				var inputs []string
				var collect func(*html.Node)
				collect = func(c *html.Node) {
					if c.Type == html.ElementNode && (c.Data == "input" || c.Data == "select" || c.Data == "textarea" || c.Data == "button") {
						kind := strings.ToLower(e2HTTPHTMLAttr(c, "type"))
						switch {
						case kind != "":
						case c.Data == "input":
							kind = "text" // HTML 기본값
						default:
							kind = c.Data
						}
						inputs = append(inputs, e2HTTPHTMLAttr(c, "name")+":"+kind)
					}
					for cc := c.FirstChild; cc != nil; cc = cc.NextSibling {
						collect(cc)
					}
				}
				collect(n)
				method := strings.ToUpper(e2HTTPHTMLAttr(n, "method"))
				if method == "" {
					method = "GET"
				}
				add(&forms, method+" "+e2HTTPHTMLAttr(n, "action")+" ["+strings.Join(inputs, ", ")+"]")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	var b bytes.Buffer
	b.WriteString("\n\n## RECON")
	for _, section := range []struct {
		title string
		items []string
	}{{"Comments", comments}, {"Scripts", scripts}, {"Inline endpoints (unconfirmed)", endpoints}, {"Forms", forms}, {"Links", links}, {"Meta", metas}} {
		if len(section.items) == 0 {
			continue
		}
		b.WriteString("\n### " + section.title + "\n")
		for _, item := range section.items {
			b.WriteString("- " + item + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
