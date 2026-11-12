package diagnosisflow

import (
	"context"
	"regexp"
	"slices"
	"strings"
)

// SourceAPIReconStatic은 탐색 에이전트가 번들·문서·API 문서에서 찾아 보고한 엔드포인트의 출처다(탐색 계약 §7).
// 실제로 오간 요청이 아니므로 추정(inferred)으로 다루고, 자동 방문·선택하지 않는다.
const SourceAPIReconStatic = "api_recon_static"

// discoveryKind는 보고된 엔드포인트의 종류다. 페이지·폼·스크립트와 구분해 화면에 보인다.
const discoveryKind = "api"

const (
	// 한 번의 보고에 받는 발견 수. 회차 발견 상한(Limits.MaxEndpoints, 최대 32)과 같은 크기다.
	maxDiscoveriesPerReport = 32
	// 엔드포인트 하나에 남기는 query·본문 키 이름 수.
	maxDiscoveryKeys = 32
	// 키 이름 하나의 길이.
	maxDiscoveryKeyBytes = 64
)

// 키 이름은 영문·숫자와 흔한 구분 기호만 받는다(배열 표기 a[b] 포함). 값·공백·따옴표는 받지 않는다.
var discoveryKeyName = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]+$`)

// content-type은 매개변수 없는 type/subtype만 받는다(charset 등은 버린다).
var discoveryContentType = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}/[a-z0-9][a-z0-9!#$&^_.+-]{0,63}$`)

// Discovery는 탐색 실행기가 넘기는 발견 하나다. 메서드·경로와 요청 모양의 이름만 받고 값은 받지 않는다.
type Discovery struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	QueryKeys   []string `json:"query_keys,omitempty"`
	BodyKeys    []string `json:"body_keys,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Source      string   `json:"source,omitempty"`
	Transport   string   `json:"transport,omitempty"`
}

// ReportStatic은 모델·페이지 도구의 정적·동적 메타데이터를 추정 발견으로 저장한다. 이름은 기존 호출자와 호환한다.
// Source를 runtime으로 보고해도 실제 관측으로 승격되지 않는다(탐색 실행기 결정안 §2-3).
// 모델이 쓴 값은 데이터라서(절대규칙 4) 프로그램이 형식과 승인 범위(origin·경로·메서드·제외 경로)를 다시 검사하고,
// 형식 오류·범위 밖은 저장하지 않고 수만 센다. 초기 탐색(crawling) 단계에서만 받는다. 저장한 수를 돌려준다.
func (c *Controller) ReportStatic(ctx context.Context, items []Discovery) (int, error) {
	if c == nil || len(items) == 0 || len(items) > maxDiscoveriesPerReport {
		return 0, ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned {
		return 0, ErrProjection
	}
	if c.record.Snapshot.Stage != "crawling" || c.record.Snapshot.Paused || c.binding.Current() != nil {
		return 0, ErrDenied
	}
	accepted := 0
	for _, item := range items {
		switch c.reportStaticLocked(item) {
		case discoveryStored:
			accepted++
		case discoveryRejected:
			c.record.Snapshot.Counts.DiscoveriesDropped++
		}
	}
	return accepted, c.saveLocked(ctx, false, "discovery", "", "", 0)
}

type discoveryResult int

const (
	discoveryStored discoveryResult = iota
	// 형식 오류·범위 밖. DiscoveriesDropped로 센다.
	discoveryRejected
	// 형식·범위는 맞지만 발견 상한이 차서 못 넣었다. addLocked가 Omitted로 센다.
	discoveryOmitted
)

func (c *Controller) reportStaticLocked(item Discovery) discoveryResult {
	method := normalizedMethod(item.Method)
	query, okQuery := discoveryKeys(item.QueryKeys)
	body, okBody := discoveryKeys(item.BodyKeys)
	contentType, okType := discoveryMediaType(item.ContentType)
	source := item.Source
	if source == "" {
		source = SourceAPIReconStatic
	}
	if source != SourceAPIReconStatic && source != SourceAPIReconRuntime || item.Transport != "" && item.Transport != "http" && item.Transport != "sse" && item.Transport != "websocket" {
		return discoveryRejected
	}
	// 경로는 이 origin의 절대 경로만 받는다. query·fragment는 키 이름 칸으로만 받는다.
	if method == "UNKNOWN" || !okQuery || !okBody || !okType || !strings.HasPrefix(item.Path, "/") || strings.HasPrefix(item.Path, "//") || strings.ContainsAny(item.Path, "?#") {
		return discoveryRejected
	}
	path, ok := c.resolve(item.Path, "/")
	if !ok || path != item.Path {
		// 정규화로 바뀌는 경로(점 조각·인코딩)는 모델이 쓴 것과 다른 주소라 받지 않는다.
		return discoveryRejected
	}
	if in, _ := c.endpointScope(method, path, discoveryKind, source); !in {
		return discoveryRejected
	}
	c.inventoryLocked(DiscoveryEvidence{Method: method, Path: path, Kind: discoveryKind, Source: source, Confidence: "inferred", InScope: true, Transport: item.Transport, QueryKeys: query, BodyKeys: body, ContentType: contentType})
	e := c.addLocked(method, path, discoveryKind, source, "/")
	if e == nil {
		return discoveryOmitted
	}
	if e.DiscoveryConfidence == "observed" {
		return discoveryStored
	}
	e.DiscoveryConfidence = "inferred"
	if e.Transport == "" {
		e.Transport = item.Transport
	}
	e.QueryKeys = mergeDiscoveryKeys(e.QueryKeys, query)
	e.BodyKeys = mergeDiscoveryKeys(e.BodyKeys, body)
	if e.ContentType == "" {
		e.ContentType = contentType
	}
	return discoveryStored
}

// discoveryKeys는 키 이름 목록을 검사한다. 하나라도 형식이 틀리면 보고 전체를 버린다. 정렬·중복 제거는
// mergeDiscoveryKeys가 한다.
func discoveryKeys(keys []string) ([]string, bool) {
	if len(keys) > maxDiscoveryKeys {
		return nil, false
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if len(k) == 0 || len(k) > maxDiscoveryKeyBytes || !discoveryKeyName.MatchString(k) {
			return nil, false
		}
		out = append(out, k)
	}
	return out, true
}

func discoveryMediaType(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	if !discoveryContentType.MatchString(v) {
		return "", false
	}
	return v, true
}

// 같은 엔드포인트를 여러 번 보고하면 이름을 합친다. 상한을 넘는 이름은 버린다.
func mergeDiscoveryKeys(old, add []string) []string {
	merged := append(slices.Clone(old), add...)
	slices.Sort(merged)
	merged = slices.Compact(merged)
	if len(merged) > maxDiscoveryKeys {
		merged = merged[:maxDiscoveryKeys]
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}
