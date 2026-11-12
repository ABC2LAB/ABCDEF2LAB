package exploreboundary

import (
	"strings"
	"unicode/utf8"
)

// 기록 한 행의 크기를 정하는 상한이다. 기록 응답은 제어 소켓 한 줄(maxControlLine)에 담겨야 읽힌다. 탐색 쪽(브라우저·
// 직접 입구의 Bash·Node)이 고른 값(메서드·Sec-Fetch-*·Host·경로)이 그대로 들어가면 행 하나가 수십 KB가 돼 기록
// 전체를 읽지 못하게 만들 수 있다(#488 리뷰 P1). 그래서 값을 정해진 어휘·길이로 줄인 뒤 기록한다.
const (
	maxRecordMethodBytes  = 16
	maxRecordPatternBytes = 1024
	maxRecordHostBytes    = 253
	// maxRecordBytes는 위 상한으로 줄인 한 행의 JSON 크기 상한이다(record_bounds_test가 최악 행으로 확인).
	maxRecordBytes = 2048
	// recordEnvelopeBytes는 기록 응답의 행 밖 부분(키·omitted 등) 여유다.
	recordEnvelopeBytes = 4096
	recordInvalid       = "invalid"
)

// Sec-Fetch-Dest·Sec-Fetch-Mode의 Fetch 표준 값이다. 밖의 값은 recordInvalid로 남긴다.
var (
	fetchDests = map[string]bool{"audio": true, "audioworklet": true, "document": true, "embed": true, "empty": true, "fencedframe": true, "font": true,
		"frame": true, "iframe": true, "image": true, "json": true, "manifest": true, "object": true, "paintworklet": true, "report": true, "script": true,
		"serviceworker": true, "sharedworker": true, "style": true, "track": true, "video": true, "webidentity": true, "worker": true, "xslt": true}
	fetchModes = map[string]bool{"cors": true, "navigate": true, "no-cors": true, "same-origin": true, "websocket": true}
)

func boundedFetch(raw string, known map[string]bool) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case v == "":
		return ""
	case known[v]:
		return v
	}
	return recordInvalid
}

// boundedMethod는 HTTP 메서드 토큰을 짧은 대문자 토큰으로만 받는다. 밖의 값은 승인 메서드와 맞지 않으므로 판정에서도
// 막힌다.
func boundedMethod(raw string) string {
	if raw == "" || len(raw) > maxRecordMethodBytes {
		return strings.ToUpper(recordInvalid)
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '-' || c == '_') {
			return strings.ToUpper(recordInvalid)
		}
	}
	return raw
}

// boundedText는 UTF-8 경계에서 자른다. 자른 값은 끝에 표시를 붙여 원래 값과 구분한다.
func boundedText(raw string, limit int) string {
	if len(raw) <= limit {
		return strings.ToValidUTF8(raw, "")
	}
	const mark = "...truncated"
	cut := raw[:limit-len(mark)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + mark
}

// normalizeRequest는 판정 전에 탐색 쪽이 고른 메타데이터를 정한 어휘로 맞춘다. 판정과 기록이 같은 값을 본다.
func normalizeRequest(req Request) Request {
	req.Method = boundedMethod(req.Method)
	req.FetchDest = boundedFetch(req.FetchDest, fetchDests)
	req.FetchMode = boundedFetch(req.FetchMode, fetchModes)
	return req
}

// boundRecord는 기록 직전에 행의 가변 칸을 상한 안으로 줄인다.
func boundRecord(r Record) Record {
	r.Method = boundedMethod(r.Method)
	r.FetchDest = boundedFetch(r.FetchDest, fetchDests)
	r.FetchMode = boundedFetch(r.FetchMode, fetchModes)
	r.PathPattern = boundedText(r.PathPattern, maxRecordPatternBytes)
	if len(r.ExternalHost) > maxRecordHostBytes {
		r.ExternalHost = recordInvalid
	}
	r.ExternalHost = strings.ToValidUTF8(r.ExternalHost, "")
	return r
}

// recordsPerPage는 기록 응답 한 쪽의 행 수다. 행 크기 상한으로 제어 소켓 한 줄에 담기는 만큼이다.
const recordsPerPage = (maxControlLine - recordEnvelopeBytes) / maxRecordBytes
