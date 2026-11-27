package responseevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// HTMLStructure는 HTML 문서의 구조 요약이다. 글·속성 값·숨은 입력 값은 없다.
type HTMLStructure struct {
	Doctype  bool `json:"doctype,omitempty"`
	Title    bool `json:"title,omitempty"`
	Elements int  `json:"elements"`
	// TagCounts는 고정 태그 목록의 개수다. 구조 유사도 계산에 쓴다.
	TagCounts map[string]int `json:"tag_counts,omitempty"`
	// StructureDigest는 얕은 깊이의 시작 태그 순서 digest다. 같으면 문서 뼈대가 같다.
	StructureDigest string `json:"structure_digest"`
	// PasswordInput은 비밀번호 입력이 하나라도 있다는 뜻이다(로그인 화면 신호).
	PasswordInput bool       `json:"password_input,omitempty"`
	AlertRegions  int        `json:"alert_regions,omitempty"`
	Forms         []HTMLForm `json:"forms,omitempty"`
	FormsOmitted  int        `json:"forms_omitted,omitempty"`
}

// HTMLForm은 폼 하나다. Action은 경로 모양만 남긴다(호스트·query·fragment 없음).
type HTMLForm struct {
	Method        string      `json:"method"`
	Action        string      `json:"action"`
	Inputs        []HTMLInput `json:"inputs,omitempty"`
	InputsOmitted int         `json:"inputs_omitted,omitempty"`
}

type HTMLInput struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var countedTags = map[string]bool{"a": true, "button": true, "div": true, "form": true, "h1": true, "h2": true, "h3": true,
	"iframe": true, "img": true, "input": true, "li": true, "p": true, "pre": true, "script": true, "section": true,
	"select": true, "span": true, "table": true, "td": true, "textarea": true, "tr": true}

var voidTags = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}

var inputTypes = map[string]bool{"text": true, "password": true, "email": true, "hidden": true, "submit": true, "checkbox": true,
	"radio": true, "number": true, "search": true, "tel": true, "url": true, "file": true, "date": true, "button": true}

var (
	htmlNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-\[\]]{0,63}$`)
	htmlSegmentPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,30}$`)
	alertWord          = regexp.MustCompile(`(?i)(^|[-_ ])(error|errors|alert|warning|danger|invalid)($|[-_ ])`)
)

const (
	htmlDigestDepth = 6
	htmlDigestTags  = 4000
	htmlTextBytes   = 256 << 10
)

func extractHTML(body []byte, l Limits, protected Protected, limited map[string]bool) (*HTMLStructure, []byte, bool) {
	z := html.NewTokenizer(bytes.NewReader(body))
	z.SetMaxBuf(len(body) + 1)
	out := &HTMLStructure{TagCounts: map[string]int{}}
	digest := sha256.New()
	var text bytes.Buffer
	var open []string
	var form *HTMLForm
	tokens, digestTags, skipText, inTitle := 0, 0, 0, false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() == io.EOF {
				break
			}
			return nil, nil, false
		}
		tokens++
		if tokens > l.MaxHTMLTokens {
			limited[LimitHTMLTokens] = true
			break
		}
		switch tt {
		case html.DoctypeToken:
			out.Doctype = true
		case html.TextToken:
			raw := z.Text()
			if inTitle && len(bytes.TrimSpace(raw)) != 0 {
				out.Title = true
			}
			if skipText == 0 && text.Len() < htmlTextBytes {
				text.Write(raw)
				text.WriteByte('\n')
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			nameRaw, hasAttr := z.TagName()
			name := string(nameRaw)
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				key := string(k)
				if _, dup := attrs[key]; !dup {
					attrs[key] = string(v)
				}
			}
			out.Elements++
			if countedTags[name] {
				out.TagCounts[name]++
			}
			if len(open) < htmlDigestDepth && digestTags < htmlDigestTags {
				digest.Write([]byte(name))
				digest.Write([]byte{byte('0' + len(open)), 0})
				digestTags++
			}
			if attrs["role"] == "alert" || alertWord.MatchString(attrs["class"]) || alertWord.MatchString(attrs["id"]) {
				out.AlertRegions++
			}
			switch name {
			case "title":
				inTitle = tt == html.StartTagToken
			case "script", "style", "template", "noscript":
				if tt == html.StartTagToken {
					skipText++
				}
			case "form":
				if form != nil {
					out.appendForm(*form, l, limited)
				}
				form = &HTMLForm{Method: formMethod(attrs["method"]), Action: formAction(attrs["action"], protected)}
			case "input", "select", "textarea":
				typ := name
				if name == "input" {
					typ = strings.ToLower(strings.TrimSpace(attrs["type"]))
					if typ == "" {
						typ = "text"
					} else if !inputTypes[typ] {
						typ = "other"
					}
				}
				if typ == "password" {
					out.PasswordInput = true
				}
				if form != nil {
					if len(form.Inputs) >= l.MaxInputs {
						form.InputsOmitted++
						limited[LimitInputs] = true
					} else {
						form.Inputs = append(form.Inputs, HTMLInput{Name: inputName(attrs["name"], protected), Type: typ})
					}
				}
			}
			if tt == html.StartTagToken && !voidTags[name] {
				if len(open) >= l.MaxDepth {
					limited[LimitDepth] = true
				} else {
					open = append(open, name)
				}
			}
		case html.EndTagToken:
			nameRaw, _ := z.TagName()
			name := string(nameRaw)
			switch name {
			case "title":
				inTitle = false
			case "script", "style", "template", "noscript":
				if skipText > 0 {
					skipText--
				}
			case "form":
				if form != nil {
					out.appendForm(*form, l, limited)
					form = nil
				}
			}
			// 닫는 태그와 맞는 가장 가까운 여는 태그까지 닫는다. 틀린 중첩은 브라우저처럼 관대하게 넘긴다.
			for i := len(open) - 1; i >= 0; i-- {
				if open[i] == name {
					open = open[:i]
					break
				}
			}
		}
	}
	if form != nil {
		out.appendForm(*form, l, limited)
	}
	if len(out.TagCounts) == 0 {
		out.TagCounts = nil
	}
	out.StructureDigest = hex.EncodeToString(digest.Sum(nil)[:8])
	return out, text.Bytes(), true
}

func (h *HTMLStructure) appendForm(f HTMLForm, l Limits, limited map[string]bool) {
	if len(h.Forms) >= l.MaxForms {
		h.FormsOmitted++
		limited[LimitForms] = true
		return
	}
	h.Forms = append(h.Forms, f)
}

func formMethod(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "", "GET":
		return "GET"
	case "POST":
		return "POST"
	case "DIALOG":
		return "DIALOG"
	}
	return "OTHER"
}

// formAction은 폼 대상의 경로 모양만 남긴다. 절대 URL은 호스트를 남기지 않고 absolute로 표시한다.
func formAction(raw string, protected Protected) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "same_document"
	}
	if protected != nil && protected(raw) {
		return "{protected}"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "non_http"
	}
	prefix := ""
	if u.Scheme != "" || u.Host != "" {
		prefix = "absolute:"
	}
	if u.Path == "" {
		return prefix + "same_path"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if !htmlSegmentPattern.MatchString(p) {
			parts[i] = "{segment}"
		}
	}
	shape := strings.Join(parts, "/")
	if strings.HasPrefix(u.Path, "/") {
		shape = "/" + shape
	}
	return prefix + shape
}

func inputName(raw string, protected Protected) string {
	switch {
	case raw == "":
		return ""
	case protected != nil && protected(raw):
		return "{protected}"
	case !htmlNamePattern.MatchString(raw):
		return "{name}"
	}
	return raw
}
