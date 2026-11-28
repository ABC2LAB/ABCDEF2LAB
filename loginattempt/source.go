package loginattempt

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/learnedlogin"
)

// sourceResponse는 추적기 메모리에만 존재한다. 토큰과 쿠키를 로그인 시도 행이나 학습 방법에 복사하지 않는다.
type sourceResponse struct {
	ref, context, path string
	status             int
	header             http.Header
	requestHeader      http.Header
	screen             learnedlogin.Screen
}

func (t *Tracker) rememberSourceLocked(req Request, resp Response) {
	if req.Method != http.MethodGet || req.RawQuery != "" || req.TransportRef == "" || resp.Status < 200 || resp.Status > 299 ||
		len(resp.Body) == 0 || len(resp.Body) >= maxRecentBodyBytes {
		return
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" && media != "text/html" && media != "application/xhtml+xml" {
		return
	}
	// 잘린 JSON·HTML 앞부분에도 토큰이 있을 수 있다. 정확한 본문 길이가 없으면 완료를 입증할 수 없다.
	length := resp.Header.Get("Content-Length")
	n, lengthErr := strconv.ParseInt(length, 10, 64)
	if lengthErr != nil || n != int64(len(resp.Body)) {
		return
	}
	screen, reason := learnedlogin.ParseScreen(learnedlogin.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body})
	if reason != "" {
		return
	}
	t.recentSources = append(t.recentSources, sourceResponse{ref: req.TransportRef, context: req.Context, path: cleanPath(req.Path),
		status: resp.Status, header: resp.Header.Clone(), requestHeader: req.Header.Clone(), screen: screen})
	if len(t.recentSources) > maxRecentBodies {
		t.recentSources = t.recentSources[len(t.recentSources)-maxRecentBodies:]
	}
}

func (t *Tracker) fieldSourceLocked(req Request, destination, value string) (RowFieldSource, bool) {
	if value == "" || req.RawQuery != "" {
		return RowFieldSource{}, false
	}
	var result RowFieldSource
	count := 0
	for _, source := range t.recentSources {
		if source.context != req.Context || source.path == cleanPath(req.Path) || source.ref == req.TransportRef {
			continue
		}
		// V2는 인증 정보를 담지 않은 GET 한 건만 보낸다. 원래 발급에 선행 인증이 필요했다면 재현할 수 없다.
		if !replayableSourceHeaders(source.requestHeader) {
			continue
		}
		typ, field, ok := source.screen.FindValueSource(value)
		if !ok {
			continue
		}
		jar := learnedlogin.Jar{}
		jar.AbsorbForLogin(learnedlogin.Response{Status: source.status, Header: source.header}, source.path, cleanPath(req.Path), t.cfg.Origin, t.cfg.Now())
		cookies := requestCookies(req.Header)
		if source.context == "" && len(jar) == 0 {
			// 직접 E② 요청에는 브라우저 문맥 식별자가 없다. 응답 쿠키가 있어야 발급 GET을 로그인과 결속한다.
			continue
		}
		bound := true
		for name, cookie := range jar {
			if cookies[name] != cookie {
				bound = false
				break
			}
		}
		if !bound {
			continue
		}
		count++
		result = RowFieldSource{Destination: destination, SourceMethod: http.MethodGet, SourcePath: source.path,
			SourceType: typ, SourceField: field, SourceRef: source.ref, SourceStatus: source.status}
	}
	return result, count == 1
}

// E③ 준비 GET은 포착된 인증 문맥을 보내지 않는다. 발급자가 미확인 헤더나 계정별 헤더에
// 의존할 수 있으므로 원래 GET의 일반 콘텐츠 협상 헤더만 허용한다.
func replayableSourceHeaders(header http.Header) bool {
	for name := range header {
		canonical := http.CanonicalHeaderKey(name)
		switch canonical {
		case "Accept", "Accept-Language", "Accept-Encoding", "User-Agent", "Cache-Control", "Pragma", "Connection", "Dnt", "Upgrade-Insecure-Requests",
			"Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-User":
		default:
			return false
		}
	}
	return true
}
