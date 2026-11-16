package e2proxy

import (
	"regexp"
	"strings"
)

// 시험 계정 실행에서 모델이 보내는 요청 자격 자리(명령의 Authorization·Cookie 헤더 값, curl --cookie 값, curl -v가 되찍는
// 요청 헤더 줄)를 값이 무엇이든 구조로 가린다. 자리(헤더 이름·구분자·여는 따옴표)는 정규식으로 찾고, 값의 끝은 값을 연
// 따옴표로 정한다. 정규식만으로는 여는 따옴표에 따라 끝을 고를 수 없어서, 값 안의 다른 따옴표나 더 깊게 이스케이프된 같은
// 따옴표(JSON 값 안의 \"en\" 같은 것)에서 멈춰 뒤 쿠키의 자격 값을 남겼다(#571 이동찬 리뷰).

// requestCredentialSite는 자격 값 자리 하나의 모양이다. before·nameClose·value는 하위 그룹 번호이고 0이면 없다.
// before는 헤더 이름 앞 따옴표("Cookie: …"), nameClose는 이름을 닫는 따옴표("Cookie": …), value는 값 앞 따옴표다.
type requestCredentialSite struct {
	re                       *regexp.Regexp
	before, nameClose, value int
	// bare는 따옴표 없이 시작한 값이 공백에서도 끝나는 자리다(curl -b 값은 셸 인수 하나).
	bare bool
}

var requestCredentialSites = []requestCredentialSite{
	// Authorization·Proxy-Authorization 헤더 값: 줄 머리(curl -v의 "> "), 명령 인수(-H "Authorization: …", wget
	// --header=…, httpie Authorization:'…'), 코드 안 헤더 객체(python dict, JSON).
	{re: regexp.MustCompile(`(?i)(\\*["'])?(?:proxy-)?authorization(\\*["'])?[ \t]*:[ \t]*(\\*["'])?[ \t]*`), before: 1, nameClose: 2, value: 3},
	// 요청 Cookie 헤더 값(형태는 위와 같음). Set-Cookie는 issuedCookieLine이 이름·속성을 남기고 따로 가린다.
	{re: regexp.MustCompile(`(?i)(?:^|[^a-z0-9\-"'\\])(\\*["'])?cookie(\\*["'])?[ \t]*:[ \t]*(\\*["'])?[ \t]*`), before: 1, nameClose: 2, value: 3},
	// 이름과 값을 쉼표로 나눈 코드 형태(python req.add_header('Cookie', '…'), [('Authorization', '…')]). 이름이 따옴표로
	// 닫혀 있고 값이 따옴표로 시작할 때만 본다.
	{re: regexp.MustCompile(`(?i)(?:^|[^a-z0-9-])\\*["'](?:(?:proxy-)?authorization|cookie)\\*["'][ \t]*,[ \t]*(\\*["'])`), value: 1},
	// curl -b·--cookie(--cookie=) 인수 값. 따옴표로 시작한 값은 쿠키 여러 개(a=1; token=…)가 공백을 담으므로 닫는
	// 따옴표까지, 따옴표 없는 값은 공백까지다.
	{re: regexp.MustCompile(`(?i)(?:^|\s)(?:-b[ \t]+|--cookie(?:[ \t]+|=))(\\*["'])?`), value: 1, bare: true},
}

// redactRequestCredentials는 자리마다 차례로 값을 [REDACTED]로 바꾼다.
func redactRequestCredentials(b []byte) []byte {
	for _, site := range requestCredentialSites {
		b = site.redact(b)
	}
	return b
}

func (s requestCredentialSite) redact(b []byte) []byte {
	var out []byte
	last := 0
	for _, m := range s.re.FindAllSubmatchIndex(b, -1) {
		start := m[1]
		if m[0] < last {
			continue
		}
		// 값 앞 따옴표가 있으면 그 따옴표가 값을 닫는다. 없고 이름도 따옴표로 닫히지 않았으면 이름 앞 따옴표가 헤더 전체
		// ("Cookie: …")를 감싼 것이라 그 따옴표가 값을 닫는다.
		q := credentialQuoteAt(b, m, s.value)
		nameClosed := credentialQuoteAt(b, m, s.nameClose).char != 0
		// 이름이 따옴표로 닫히지 않았는데 콜론 뒤 공백 다음에 따옴표가 오면(curl -v 줄 Cookie: '&=…) 감싼 인용부가 아니라
		// 값의 첫 글자다. 감싼 인용부는 이름이 닫힌 객체 형태("Cookie": "…")나 붙여 쓴 인수(Cookie:'…')에서만 본다.
		if q.char != 0 && s.nameClose != 0 && !nameClosed {
			if vs := m[2*s.value]; vs > 0 && (b[vs-1] == ' ' || b[vs-1] == '\t') {
				q, start = credentialQuote{}, vs
			}
		}
		if q.char == 0 && !nameClosed {
			q = credentialQuoteAt(b, m, s.before)
		}
		end := credentialValueEnd(b, start, q, s.bare && q.char == 0, credentialStringLevel(b, m[0]))
		if end <= start {
			continue
		}
		out = append(out, b[last:start]...)
		out = append(out, "[REDACTED]"...)
		last = end
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// credentialQuote는 값을 연 따옴표다. char가 0이면 따옴표 없이 시작한 값이다. escapes는 그 따옴표 앞 백슬래시 수(이스케이프
// 단계)다. JSON 문자열 안의 셸 인수(-H \"Cookie: …\")는 1, 그것을 한 번 더 JSON으로 감싸면 3이다.
type credentialQuote struct {
	char    byte
	escapes int
}

func credentialQuoteAt(b []byte, m []int, group int) credentialQuote {
	if group == 0 || 2*group+1 >= len(m) || m[2*group] < 0 || m[2*group+1] <= m[2*group] {
		return credentialQuote{}
	}
	start, end := m[2*group], m[2*group+1]
	return credentialQuote{char: b[end-1], escapes: end - 1 - start}
}

// credentialValueEnd는 start에서 시작한 자격 값의 끝(포함하지 않음)이다.
//   - 줄바꿈(실제 CR·LF와 이스케이프 \n·\r·\t)에서 끝난다. 헤더 값에는 줄바꿈이 없고, JSON으로 감싼 curl -v 출력의 다음
//     헤더 줄을 남긴다.
//   - 값을 연 따옴표가 있으면 같은 종류이고 이스케이프 단계가 같거나 얕은 따옴표에서 끝난다. 다른 종류의 따옴표와 더 깊게
//     이스케이프된 같은 따옴표는 값 안의 글자다(JSON 값 "language=\"en\"; token=…").
//   - 따옴표 없이 시작한 값(curl -v 줄)은 따옴표가 JSON 구조 문자 앞에 올 때만 끝난다. 그 밖의 따옴표는 쿠키 값의 일부다
//     (a="x"; token=…). `=` 바로 뒤 따옴표는 인용부를 열고, 닫을 때까지 안의 글자는 구조 문자여도 값이다(lang="}").
//     bare 자리는 인용부 밖의 따옴표와 공백에서 끝난다(-b token="…"는 인용부까지 값).
func credentialValueEnd(b []byte, start int, q credentialQuote, bare bool, level int) int {
	// inner는 따옴표 없이 시작한 값 안에서 `=` 바로 뒤에 연 인용부다(쿠키 값 lang="…", Digest 매개변수 realm="…", 셸
	// 인수 -b token="…"). 같은 종류·같은 이스케이프 단계의 따옴표가 닫을 때까지 안의 글자는 구조 문자여도 값이다(#571 이동찬
	// 리뷰: lang="}"에서 여는 따옴표 뒤 `}`를 바깥 구조로 보고 뒤 token을 남김).
	var inner credentialQuote
	i := start
	for i < len(b) {
		// 이 글자 앞의 백슬래시를 같이 읽는다(n개, 0이면 원문 글자).
		j := i
		for j < len(b) && b[j] == '\\' {
			j++
		}
		if j == len(b) {
			return len(b)
		}
		n, c := j-i, b[j]
		switch {
		case c == '\r' || c == '\n':
			return i
		case level >= 0 && n == level+1 && (c == 'n' || c == 'r'):
			// 헤더 값에는 줄바꿈이 없다. 감싼 JSON 문자열 단계의 줄바꿈 이스케이프(닫는 따옴표 백슬래시 수 + 1개)는 다음
			// 줄의 시작이다. 문자열 밖(원문 줄)의 백슬래시와 더 깊은 단계는 글자다(쿠키 값 안의 글자 \r). 탭은 헤더 값에 올 수
			// 있어 값의 글자다.
			return i
		case bare && inner.char == 0 && level >= 0 && n == level+1 && c == 't':
			return i
		case c == '"' || c == '\'':
			switch {
			case q.char != 0:
				// 감싼 인용부는 같은 종류 따옴표에서 닫히되, 같은 단계면 뒤가 닫힘 자리여야 한다. 아니면 그 따옴표는 값의
				// 일부다(쿠키 이름이 '로 시작하는 Cookie: '&=…; token=…에서 다음 '로 끝내 token을 남김). 얕은 단계는 바깥 끝이다.
				if c == q.char && (n < q.escapes || n == q.escapes && credentialClosingAt(b, j+1)) {
					return i
				}
			case c == '"' && level >= 0 && n < level:
				// 줄 값을 감싼 JSON 문자열보다 얕은 단계의 `"`는 그 문자열이 이미 끝난 바깥 경계다.
				return i
			case c == '"' && n == level && credentialStructuralAt(b, j+1):
				// 따옴표 없이 시작한 줄 값을 감싼 바깥 문자열은 JSON이라 `"`로만 끝나고, 그 문자열의 이스케이프 단계(level)의
				// `"`만 끝 후보다. 원문 curl -v 줄(level -1)의 `"`와 한 겹 JSON 안의 \"는 값의 글자다(Cookie: 0=;"); token=…).
				// 홑따옴표는 값 안의 글자다(쿠키 값 {[x y'}). 홑따옴표로 감싼 바깥(python repr)이면 줄 끝까지 더 가린다(유출 없음).
				// 인용부·`=` 뒤 인용부보다 먼저 본다: 올바른 JSON에서 같은 단계의 원문 `"`는 늘 문자열 끝이고 그 안의 인용부도
				// 함께 끝난다. 그래서 `=`로 끝나는 값(base64 패딩 Basic cmV2…=, token=…=)의 닫는 따옴표를 인용부로 열지 않는다
				// (#571 이동찬 리뷰).
				return i
			case inner.char != 0:
				// 다른 종류의 따옴표는 이스케이프 단계와 상관없이 값 안의 글자다(JSON은 '를 이스케이프하지 않는다).
				if c == inner.char && n == inner.escapes {
					inner = credentialQuote{}
				} else if c == inner.char && n < inner.escapes {
					// 인용부보다 얕게 이스케이프된 같은 따옴표는 바깥 문자열의 끝이다.
					return i
				}
			case i > start && b[i-1] == '=':
				inner = credentialQuote{char: c, escapes: n}
			case bare:
				return i
			}
		case bare && inner.char == 0 && (c == ' ' || c == '\t'):
			return i
		}
		i = j + 1
	}
	return i
}

// credentialStringLevel은 i가 들어 있는 JSON 문자열의 닫는 따옴표 이스케이프 단계(앞 백슬래시 수)다. 같은 원문 줄에서 i 앞의
// `"`를 앞 백슬래시 수별로 세어 홀수인 단계 중 가장 깊은 것을 고른다(원문 0, 한 겹 감싼 JSON 안 1, 두 겹 3). 문자열 밖이면 -1이다.
func credentialStringLevel(b []byte, i int) int {
	odd := map[int]bool{}
	for k := i - 1; k >= 0 && b[k] != '\n'; k-- {
		if b[k] != '"' {
			continue
		}
		escapes := 0
		for k-escapes-1 >= 0 && b[k-escapes-1] == '\\' {
			escapes++
		}
		odd[escapes] = !odd[escapes]
	}
	level := -1
	for escapes, open := range odd {
		if open && escapes > level {
			level = escapes
		}
	}
	return level
}

// credentialClosingAt은 감싼 인용부를 닫는 따옴표 바로 뒤(i)가 닫힘 자리인지다: 끝, 공백·줄바꿈, 구조·셸 구분 문자, 이스케이프된
// 공백·따옴표.
func credentialClosingAt(b []byte, i int) bool {
	if i >= len(b) {
		return true
	}
	if strings.IndexByte(" \t\r\n,}]:);&|>", b[i]) >= 0 {
		return true
	}
	j := i
	for j < len(b) && b[j] == '\\' {
		j++
	}
	return j > i && j < len(b) && strings.IndexByte("nrt\"'", b[j]) >= 0
}

// credentialStructuralAt은 i부터 공백을 건너뛴 자리가 끝이거나 JSON·코드 구조 문자인지다. 따옴표 없이 시작한 값에서 이
// 앞 따옴표는 바깥 문자열의 끝이다(한 줄 JSON {"tool_output":"> Cookie: …","trusted":…}). 들여쓴 JSON처럼 닫는 따옴표
// 뒤에 공백·줄바꿈(원문 또는 이스케이프된 \n·\r·\t)이 와도 같다(#571 재상·이동찬 리뷰: 공백 뒤 쉼표·줄바꿈 뒤 중괄호를
// 놓쳐 닫는 따옴표까지 가려 그래프 도구 인수 JSON이 깨짐).
func credentialStructuralAt(b []byte, i int) bool {
	for i < len(b) {
		switch c := b[i]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
			continue
		case c == '\\':
			n := 1
			for i+n < len(b) && b[i+n] == '\\' {
				n++
			}
			if i+n < len(b) && strings.IndexByte("nrt", b[i+n]) >= 0 {
				i += n + 1
				continue
			}
		}
		break
	}
	return i >= len(b) || strings.IndexByte(",}]:)", b[i]) >= 0
}
