package diagnosisflow

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// 원본 harvest_static의 API·라우트 후보 로직만 재사용한다. 네트워크·TLS 우회·파일 저장은 실행하지 않는다.
var reconLiteral = regexp.MustCompile("[\"'`](/(?:api|apis|rest|service|services|gateway|graphql|v[0-9]+|web|admin|backend|open)(?:/[^\"'`\\s<>$]{0,180})?)[\"'`]")
var reconRoute = regexp.MustCompile(`\b(?:path|to|redirect|href)\s*:\s*["'](/[A-Za-z0-9_\-/:]{0,128})["']`)

// 인접한 단순 객체만 이름 후보로 읽는다. 변수·spread·중첩 식은 추측해서 펼치지 않는다.
var reconBodyCall = regexp.MustCompile(`(?i)\.(post|put|patch)\(\s*["'](/[^"'\s<>]{0,180})["']\s*,\s*\{([^{}]{0,512})\}`)
var reconFetchBody = regexp.MustCompile(`(?i)\bfetch\(\s*["'](/[^"'\s<>]{0,180})["']\s*,\s*\{[^{}]{0,256}\bmethod\s*:\s*["'](POST|PUT|PATCH)["'][^{}]{0,256}\bbody\s*:\s*JSON\.stringify\(\s*\{([^{}]{0,512})\}`)
var reconObjectKey = regexp.MustCompile(`(?:^|[,{}])\s*(?:["']([A-Za-z0-9_.\-\[\]]+)["']|([A-Za-z_$][A-Za-z0-9_$]*))\s*:`)

// 정적 JSON·JS object에서 이름만 읽는다. 실행·값 추론·필수 여부 판정은 하지 않는다.
func staticBodyKeys(body string) []string {
	names := []string{}
	for _, m := range reconObjectKey.FindAllStringSubmatch(body, maxDiscoveryKeys) {
		k := m[1]
		if k == "" {
			k = m[2]
		}
		if valid, ok := discoveryKeys([]string{k}); ok {
			names = mergeDiscoveryKeys(names, valid)
		}
	}
	return names
}
func staticQueryKeys(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	keys := []string{}
	for key := range u.Query() {
		if valid, ok := discoveryKeys([]string{key}); ok {
			keys = mergeDiscoveryKeys(keys, valid)
		}
	}
	return keys
}
func (c *Controller) staticFactLocked(method, path, kind, source, raw string) {
	in, _ := c.endpointScope(method, path, kind, source)
	c.inventoryLocked(DiscoveryEvidence{Method: method, Path: path, Kind: kind, Source: source, Confidence: "inferred", InScope: in, QueryKeys: staticQueryKeys(raw)})
}
func (c *Controller) staticShapeLocked(method, raw, object, from string) {
	if strings.Contains(object, "...") {
		return
	}
	path, ok := c.resolve(raw, from)
	if !ok {
		return
	}
	keys := staticBodyKeys(object)
	if len(keys) == 0 {
		return
	}
	method = normalizedMethod(method)
	in, _ := c.endpointScope(method, path, "api", SourceAPIReconStatic)
	c.inventoryLocked(DiscoveryEvidence{Method: method, Path: path, Kind: "api", Source: SourceAPIReconStatic, Confidence: "inferred", InScope: in, QueryKeys: staticQueryKeys(raw), BodyKeys: keys})
	if e := c.addLocked(method, path, "api", SourceAPIReconStatic, from); e != nil && e.DiscoveryConfidence != "observed" {
		e.BodyKeys = mergeDiscoveryKeys(e.BodyKeys, keys)
		e.DiscoveryConfidence = "inferred"
	}
}
func (c *Controller) discoverReconLocked(body []byte, contentType, from string) {
	if !strings.Contains(contentType, "javascript") && !strings.Contains(contentType, "json") && !strings.Contains(contentType, "html") {
		return
	}
	for _, m := range reconBodyCall.FindAllSubmatch(body, maxBundleMatches) {
		c.staticShapeLocked(string(m[1]), string(m[2]), string(m[3]), from)
	}
	for _, m := range reconFetchBody.FindAllSubmatch(body, maxBundleMatches) {
		c.staticShapeLocked(string(m[2]), string(m[1]), string(m[3]), from)
	}
	// 더 넓은 원본 API dialect를 method 미확인 추정으로 보존한다. 실제 호출 근거 없이 GET으로 바꾸지 않는다.
	for _, m := range reconLiteral.FindAllSubmatch(body, maxBundleMatches) {
		raw := string(m[1])
		path, ok := c.resolve(raw, from)
		if !ok {
			continue
		}
		c.staticFactLocked("UNKNOWN", path, "api", SourceAPIReconStatic, raw)
		known := false
		for _, e := range c.record.Snapshot.Endpoints {
			known = known || e.Path == path
		}
		if !known {
			if e := c.addLocked("UNKNOWN", path, "api", SourceAPIReconStatic, from); e != nil {
				e.DiscoveryConfidence = "inferred"
			}
		}
	}
	for _, m := range reconRoute.FindAllSubmatch(body, maxBundleMatches) {
		raw := string(m[1])
		path, ok := c.resolve(raw, from)
		if !ok {
			continue
		}
		c.staticFactLocked("GET", path, "page", SourceAPIReconStatic, raw)
		if e := c.addLocked("GET", path, "page", SourceAPIReconStatic, from); e != nil && e.DiscoveryConfidence != "observed" {
			e.DiscoveryConfidence = "inferred"
		}
	}
}

// CollectStaticDocument는 명시적 프로그램 입력이다. 모델 보고는 ReportStatic을 쓰고 관측으로 승격되지 않는다.
func (c *Controller) CollectStaticDocument(ctx context.Context, path, contentType string, body []byte) error {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return ErrDenied
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	canonical, ok := c.resolve(path, "/")
	if c.poisoned || c.record.Snapshot.Stage != "crawling" || c.record.Snapshot.Paused || c.binding.Current() != nil || !ok || canonical != path || !c.allowedPath(path) || len(body) > c.binding.Limits.MaxResponseBytes {
		return ErrDenied
	}
	c.discoverDocumentLocked(body, contentType, path, true)
	return c.saveLocked(ctx, false, "api_recon_static", "", path, 0)
}
