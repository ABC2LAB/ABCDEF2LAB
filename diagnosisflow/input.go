package diagnosisflow

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxProjectionBytes = 48 << 10 // Leaves room for the 64KiB IPC envelope.

func normalizedMethod(method string) string {
	if len(method) > 7 {
		return "UNKNOWN"
	}
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return strings.ToUpper(method)
	}
	return "UNKNOWN"
}

// maxPathBytes는 대상 경로의 인코딩된 표기(EscapedPath) 길이 상한이다.
const maxPathBytes = 128

func canonicalPath(u *url.URL) (string, bool) {
	if u == nil || !utf8.ValidString(u.Path) || u.RawPath != "" || strings.Contains(u.Path, "%") {
		return "", false
	}
	for _, r := range u.Path {
		if unicode.IsControl(r) || r == '\\' {
			return "", false
		}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(path) > maxPathBytes || !strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
		return "", false
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return "", false
		}
	}
	return path, true
}

// diagnosisPath는 진단 단계 요청 경로를 한 번만 풀어 검사한다(2026-11-16 사용자 결정, 통제 경계 변경이라 이동찬 리뷰).
// 경로 조각은 흔한 주입 지점이라 인코딩한 값(따옴표·공백·세미콜론 등)을 막으면 진단 능력이 깎인다(VAmPI 실제 회차:
// E②가 승인 규칙으로 맞춘 GET /users/v1/<값> 요청이 여기서만 거부됨). 크롤·발견은 canonicalPath 그대로 엄격하다.
//
// decoded는 한 번 푼 경로로, 대상이 실제로 처리하는 경로라 범위·제외·위험 단어를 이것으로 본다. canonical은 decoded를
// Go 기본 규칙으로 다시 인코딩한 표기로 decoded와 1:1이다. 크롤이 저장한 끝점 경로(canonicalPath)와 같은 표기라 끝점
// 대조·결과 불명 기억 키에 쓴다. 그래서 같은 경로를 인코딩만 달리 보내도(%2D와 -) 같은 기억을 공유한다.
//
// 거부: 두 번 인코딩(푼 조각에 %가 남음), 조각 경계를 바꾸는 인코딩한 slash, 역슬래시, 제어 문자, 점 조각(;매개변수를
// 뗀 이름 포함), 인코딩한 점, 빈 조각(//), UTF-8 아님. 이 집합은 E② requestPath가 막는 것을 모두 포함한다
// (diagnosisflow 허용 ⊆ E② 허용, 차등 시험 TestDiagnosisEncodedPathAllowsAreWithinE2). 길이 상한은 지금처럼 인코딩된
// 표기에 건다: 보내는 표기(E②·대상이 보는 것)와 끝점·기억에 쓰는 정규 표기 둘 다 넘으면 안 된다. 풀린 경로는 인코딩된
// 표기보다 길 수 없어 따로 재지 않는다.
func diagnosisPath(u *url.URL) (canonical, decoded string, ok bool) {
	escaped := u.EscapedPath()
	if escaped == "" {
		escaped = "/"
	}
	// 점은 인코딩할 이유가 없는 문자이고 E②도 모든 %2e를 막는다. 여기서 받으면 E②보다 넓어진다.
	if len(escaped) > maxPathBytes || !strings.HasPrefix(escaped, "/") || strings.Contains(strings.ToLower(escaped), "%2e") {
		return "", "", false
	}
	segments := strings.Split(escaped, "/")
	for i, raw := range segments {
		segment, err := url.PathUnescape(raw)
		if err != nil || !utf8.ValidString(segment) {
			return "", "", false
		}
		// 한 번 푼 조각에 %가 남으면 두 번 인코딩(%252f → %2f)이다. 대상이 한 번 더 풀면 검사한 경로와 달라진다.
		if strings.Contains(segment, "%") {
			return "", "", false
		}
		// 인코딩한 slash는 조각 경계를, 역슬래시는 일부 서버의 경로 구분을 바꿔 범위 검사와 대상 해석이 갈린다.
		if strings.ContainsAny(segment, "/\\") {
			return "", "", false
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return "", "", false
			}
		}
		// 경로 매개변수(;...)를 떼는 서버는 "..;"를 점 조각으로 풀어 승인 접두 밖으로 간다(E②와 같은 규칙).
		name, _, _ := strings.Cut(segment, ";")
		if segment == "." || segment == ".." || name == "." || name == ".." {
			return "", "", false
		}
		segments[i] = segment
	}
	decoded = strings.Join(segments, "/")
	if strings.Contains(decoded, "//") {
		return "", "", false
	}
	canonical = (&url.URL{Path: decoded}).EscapedPath()
	if len(canonical) > maxPathBytes {
		return "", "", false
	}
	return canonical, decoded, true
}

func boundedStatus(status int) int {
	if status >= 100 && status <= 599 {
		return status
	}
	return 0
}

// Only optional observation detail is trimmed. Scope, selected IDs and command
// receipts stay intact; clipping is explicit and never grants authority.
func compactProjection(s Snapshot) Snapshot {
	fits := func() bool { raw, _ := json.Marshal(s); return len(raw) <= MaxProjectionBytes }
	for !fits() {
		s.DetailTruncated = true
		switch {
		case len(s.Activity) > 0:
			s.Activity = s.Activity[:len(s.Activity)-1]
		case len(s.Plan) > 0:
			s.Plan = s.Plan[:len(s.Plan)-1]
		case len(s.Events) > 0:
			s.Events = s.Events[1:]
		default:
			changed := false
			for i := range s.Endpoints {
				e := &s.Endpoints[i]
				if e.Request != "" || e.Response != "" {
					e.Request = ""
					e.Response = ""
					changed = true
				}
			}
			// 그래도 넘으면 보고된 요청 모양(이름 목록)을 지운다. 범위·선택에는 쓰지 않는 보조 정보다.
			if !changed {
				for i := range s.Endpoints {
					e := &s.Endpoints[i]
					if len(e.QueryKeys) > 0 || len(e.BodyKeys) > 0 || e.ContentType != "" {
						e.QueryKeys, e.BodyKeys, e.ContentType = nil, nil, ""
						changed = true
					}
				}
			}
			if !changed {
				return s
			}
		}
	}
	return s
}
