package diagnosisflow

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 번들에서 요청 주소를 글자로만 찾는다. JS를 실행하지 않으므로 실행 중에 조합되는 주소는 놓친다.
// 찾은 경로는 후보일 뿐이고, 실제 전송은 범위·선택·E② 검사를 그대로 거친다.

const (
	maxBundleMatches     = 256
	maxBundleChunks      = 64
	maxBundleChunkTables = 8
	bundleQuote          = "[\"'`]"
	bundlePathTail       = "[^\"'`\\s<>$]{0,180}"
	bundleTemplateTail   = "[^`$\\s<>]{0,180}"
	bundleLiteralTail    = "[^\"'`\\s<>]{1,160}"
	bundleFetchOptions   = "[^{}]{0,200}?"
	bundleVerb           = "(get|post|put|patch|delete)"
)

var (
	apiLiteral = regexp.MustCompile(bundleQuote + "(/(?:api|rest)/" + bundleLiteralTail + ")" + bundleQuote)
	// `.post(this.host+"/x"`처럼 앞에 식이 붙어도 경로 글자를 잡는다. 번역 키 같은 `.get("KEY")`는 `/`로 시작하지 않아 빠진다.
	callConcat   = regexp.MustCompile(`(?i)\.` + bundleVerb + `\(\s*(?:([\w$.]+)\s*\+\s*)?` + bundleQuote + "(\\.?/" + bundlePathTail + ")" + bundleQuote)
	callTemplate = regexp.MustCompile("(?i)\\." + bundleVerb + "\\(\\s*`(?:\\$\\{([\\w$.]+)\\})?(/" + bundleTemplateTail + ")?(\\$\\{|`)")
	callMember   = regexp.MustCompile(`(?i)\.` + bundleVerb + `\(\s*(this\.[\w$]+)\s*[,)]`)
	baseAssign   = regexp.MustCompile(`(this\.[\w$]+)\s*=\s*(?:[\w$.]+\s*\+\s*)?` + bundleQuote + "(/" + bundlePathTail + ")" + bundleQuote)
	baseTemplate = regexp.MustCompile("(this\\.[\\w$]+)\\s*=\\s*`(?:\\$\\{[\\w$.]+\\})?(/" + bundleTemplateTail + ")`")
	fetchCall    = regexp.MustCompile(`\bfetch\(\s*(?:[\w$.]+\s*\+\s*)?` + bundleQuote + "(/" + bundlePathTail + ")" + bundleQuote + `(?:\s*,\s*\{` + bundleFetchOptions + `\bmethod\s*:\s*` + bundleQuote + `(\w+)` + bundleQuote + `)?`)
	xhrOpen      = regexp.MustCompile(`\.open\(\s*` + bundleQuote + `(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)` + bundleQuote + `\s*,\s*` + bundleQuote + "(/" + bundlePathTail + ")" + bundleQuote)
	urlField     = regexp.MustCompile(`\burl\s*:\s*` + bundleQuote + "(/" + bundlePathTail + ")" + bundleQuote)
	// 슬래시 없는 "moment.js" 같은 모듈 이름은 파일 경로로 보지 않는다.
	chunkFile = regexp.MustCompile(bundleQuote + `((?:\.{0,2}/)?(?:[\w-]+/)*[\w.-]+\.m?js)` + bundleQuote)
	// webpack 런타임의 청크 표: ({1:"abc"})[e]+".js", e+"."+{1:"abc"}[e]+".js", "chunk-"+{...}[e]+".js"
	chunkMap   = regexp.MustCompile(`=>\s*(?:"([\w-]*)"\s*\+\s*)?(?:\w\s*\+\s*"([.\w-]*)"\s*\+\s*)?\(?\{((?:\d+:"[\w-]+",?)+)\}\)?\[\w\]\s*\+\s*"(\.[\w.-]*js)"`)
	chunkEntry = regexp.MustCompile(`(\d+):"([\w-]+)"`)
	publicPath = regexp.MustCompile(`\.p\s*=\s*"(/[\w./-]*)"`)
)

type bundleRef struct {
	method, path, kind, source string
}

type bundleBase struct {
	name, path string
	at         int
}

// 한도에서 잘리면 그 뒤에 더 가까운 대입이 있을 수 있으므로, 믿을 수 있는 구간의 끝도 함께 돌려준다.
func bundleBases(body []byte) ([]bundleBase, int) {
	var bases []bundleBase
	cutoff := len(body)
	for _, re := range []*regexp.Regexp{baseAssign, baseTemplate} {
		matches := re.FindAllSubmatchIndex(body, maxBundleMatches)
		if len(matches) == maxBundleMatches {
			cutoff = min(cutoff, matches[len(matches)-1][0])
		}
		for _, m := range matches {
			bases = append(bases, bundleBase{string(body[m[2]:m[3]]), string(body[m[4]:m[5]]), m[0]})
		}
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i].at < bases[j].at })
	return bases, cutoff
}

// 번들러는 클래스를 차례로 내보내므로 가장 가까운 앞쪽 대입이 그 호출의 기준 주소다.
func baseBefore(bases []bundleBase, name string, at int) (string, bool) {
	for i := len(bases) - 1; i >= 0; i-- {
		if bases[i].at < at && bases[i].name == name {
			return bases[i].path, true
		}
	}
	return "", false
}

func group(body []byte, m []int, n int) string {
	if m[2*n] < 0 {
		return ""
	}
	return string(body[m[2*n]:m[2*n+1]])
}

// 경로 문자열 뒤에 무엇이 붙었는지 보려고 공백을 건너뛴 첫 글자를 돌려준다.
func byteAfter(body []byte, at int) byte {
	for ; at < len(body); at++ {
		if c := body[at]; c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return c
		}
	}
	return 0
}

func bundleRefs(body []byte) []bundleRef {
	var refs []bundleRef
	seen, called := map[string]bool{}, map[string]bool{}
	emit := func(method, path, kind, source string) {
		if method != "UNKNOWN" {
			called[path] = true
		}
		key := method + " " + path
		if kind == "api" && !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "./") || seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, bundleRef{method, path, kind, source})
	}
	bases, cutoff := bundleBases(body)
	// 앞 식을 기준 주소로 푼다. 식에 뭐가 들었는지 모르면 경로를 확정하지 않는다.
	full := func(expr, suffix string, at int) (string, bool) {
		switch {
		case expr == "":
			return suffix, true
		case !strings.Contains(expr, "."):
			return suffix, false // 지역 변수는 전체 주소일 수도, 경로 일부일 수도 있다.
		case !strings.HasPrefix(expr, "this."):
			return suffix, true // env.api 같은 설정값은 origin으로 본다.
		case at > cutoff:
			return suffix, false
		}
		if base, ok := baseBefore(bases, expr, at); ok {
			return base + suffix, true
		}
		return suffix, true
	}
	call := func(method, expr, suffix string, at int, dynamic bool) {
		path, known := full(expr, suffix, at)
		source := "script_call"
		if dynamic {
			// 뒤가 실행 중에 정해지는 주소라 앞부분만 후보로 남긴다.
			path, source = strings.TrimSuffix(path, "/"), "script_prefix"
		}
		if !known {
			method, source = "UNKNOWN", "static_literal"
		}
		emit(method, path, "api", source)
	}
	for _, m := range callConcat.FindAllSubmatchIndex(body, maxBundleMatches) {
		call(strings.ToUpper(group(body, m, 1)), group(body, m, 2), group(body, m, 3), m[0], byteAfter(body, m[1]) == '+')
	}
	for _, m := range callTemplate.FindAllSubmatchIndex(body, maxBundleMatches) {
		call(strings.ToUpper(group(body, m, 1)), group(body, m, 2), group(body, m, 3), m[0], group(body, m, 4) == "${")
	}
	for _, m := range callMember.FindAllSubmatchIndex(body, maxBundleMatches) {
		if base, ok := baseBefore(bases, group(body, m, 2), m[0]); ok && m[0] <= cutoff {
			emit(strings.ToUpper(group(body, m, 1)), base, "api", "script_call")
		}
	}
	for _, m := range fetchCall.FindAllSubmatchIndex(body, maxBundleMatches) {
		method := strings.ToUpper(group(body, m, 2))
		if method == "" {
			method = "GET"
		}
		emit(method, group(body, m, 1), "api", "script_call")
	}
	for _, m := range xhrOpen.FindAllSubmatchIndex(body, maxBundleMatches) {
		emit(group(body, m, 1), group(body, m, 2), "api", "script_call")
	}
	literals := append(urlField.FindAllSubmatchIndex(body, maxBundleMatches), apiLiteral.FindAllSubmatchIndex(body, maxBundleMatches)...)
	for _, m := range literals {
		// 동적 호출은 끝 `/`를 뗀 경로로 기록되므로 같은 literal을 UNKNOWN으로 다시 세지 않는다.
		if path := group(body, m, 1); !called[path] && !called[strings.TrimSuffix(path, "/")] {
			emit("UNKNOWN", path, "api", "static_literal")
		}
	}
	for _, path := range bundleChunks(body) {
		emit("GET", path, "script", "bundle_chunk")
	}
	return refs
}

func bundleChunks(body []byte) []string {
	var chunks []string
	for _, m := range chunkFile.FindAllSubmatchIndex(body, maxBundleMatches) {
		path := group(body, m, 1)
		if strings.Contains(path, "/") && !strings.Contains(path, "node_modules") {
			chunks = append(chunks, path)
		}
	}
	prefix := ""
	if m := publicPath.FindSubmatch(body); m != nil {
		prefix = string(m[1])
	}
	for _, m := range chunkMap.FindAllSubmatchIndex(body, maxBundleChunkTables) {
		for _, entry := range chunkEntry.FindAllSubmatch(body[m[6]:m[7]], -1) {
			id, err := strconv.Atoi(string(entry[1]))
			if err != nil {
				continue
			}
			name := group(body, m, 1)
			if m[2*2] >= 0 {
				name += strconv.Itoa(id) + group(body, m, 2)
			}
			chunks = append(chunks, prefix+name+string(entry[2])+group(body, m, 4))
		}
	}
	if len(chunks) > maxBundleChunks {
		chunks = chunks[:maxBundleChunks]
	}
	return chunks
}

// 라이브러리 묶음은 API 호출이 거의 없어서 페이지 한도가 빠듯할 때 뒤로 미룬다.
var vendorScript = regexp.MustCompile(`(?i)(^|[/.~-])(vendors?|polyfills?|framework|chunk-vendors)([.~-]|$)`)

// 번들이 경로를 가장 많이 알려 주므로 시작 페이지 다음에 먼저 받는다.
func crawlRank(e Endpoint) int {
	switch {
	case e.Source == "start":
		return 0
	case e.Kind == "script" && vendorScript.MatchString(e.Path):
		return 3
	case e.Kind == "script":
		return 1
	}
	return 2
}
