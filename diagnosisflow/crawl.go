package diagnosisflow

import (
	"bufio"
	"context"
	"github.com/Autumn-27/artex/e2proxy"
	"golang.org/x/net/html"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// 스크립트 후보가 발견 칸의 절반을 넘어 API 경로를 밀어내지 않게 한다.
const scriptSlotDivisor = 2

func (c *Controller) resolve(raw, from string) (string, bool) {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\\") {
		return "", false
	}
	base, err := url.Parse(c.origin + from)
	if err != nil {
		return "", false
	}
	u, err := base.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != c.origin || u.RawPath != "" || strings.Contains(u.Path, "%") {
		return "", false
	}
	return canonicalPath(u)
}

// 스크립트 후보는 회차 전체에서 발견 상한의 절반까지만 칸을 쓴다.
func (c *Controller) scriptSlotsLocked() int {
	used := 0
	for _, e := range c.record.Snapshot.Endpoints {
		if e.Kind == "script" {
			used++
		}
	}
	return c.binding.Limits.MaxEndpoints/scriptSlotDivisor - used
}

// 스크립트를 참조한 문서를 따라 올라가 기준 주소를 돌려준다. 문서를 모르면 known이 false이고,
// 문서의 base가 다른 origin을 가리키면 foreign이 true다.
func (c *Controller) documentLocked(from string) (doc string, known, foreign bool) {
	for range c.record.Snapshot.Endpoints {
		e := c.endpointLocked("GET", from)
		if e == nil {
			return "", false, false
		}
		if e.Kind != "script" {
			if b, ok := c.documentBases[from]; ok {
				return b.path, b.known, !b.known
			}
			return from, true, false
		}
		from = e.From
	}
	return "", false, false
}

func preloadsScript(attrs map[string]string) bool {
	for _, rel := range strings.Fields(strings.ToLower(attrs["rel"])) {
		if rel == "modulepreload" || rel == "preload" && strings.EqualFold(attrs["as"], "script") {
			return true
		}
	}
	return false
}

func (c *Controller) discoverBodyLocked(body []byte, contentType, from string) {
	c.discoverDocumentLocked(body, contentType, from, false)
}

func (c *Controller) discoverDocumentLocked(body []byte, contentType, from string, inferred bool) {
	addFrom := func(base, method, raw, kind, source string) {
		if p, ok := c.resolve(raw, base); ok {
			endpointSource := source
			if inferred {
				endpointSource = SourceAPIReconStatic
			}
			if e := c.addLocked(method, p, kind, endpointSource, from); inferred && e != nil && e.DiscoveryConfidence != "observed" {
				e.DiscoveryConfidence = "inferred"
			}
			c.staticFactLocked(normalizedMethod(method), p, kind, source, raw)
		} else {
			c.record.Snapshot.Counts.InvalidPaths++
		}
	}
	// 문서의 기준 주소다. <base href>를 만나면 바뀌고, 다른 origin을 가리키면 상대 주소를 풀 수 없다.
	base, baseKnown, baseSeen := from, true, false
	formMethod, formPath, formType := "", "", ""
	formKeys := []string{}
	finishForm := func() {
		if formPath != "" {
			in, _ := c.endpointScope(formMethod, formPath, "form", "html_form")
			fact := DiscoveryEvidence{Method: formMethod, Path: formPath, Kind: "form", Source: "html_form", Confidence: "inferred", InScope: in}
			if formMethod == "GET" || formMethod == "HEAD" {
				fact.QueryKeys = formKeys
			} else {
				fact.BodyKeys = formKeys
				fact.ContentType = formType
			}
			// 선택 목록이 차도 폼의 이름 근거는 보호 목록에 남긴다.
			c.inventoryLocked(fact)
			if e := c.endpointLocked(formMethod, formPath); e != nil && e.DiscoveryConfidence != "observed" {
				e.DiscoveryConfidence = "inferred"
				e.QueryKeys = mergeDiscoveryKeys(e.QueryKeys, fact.QueryKeys)
				e.BodyKeys = mergeDiscoveryKeys(e.BodyKeys, fact.BodyKeys)
				if e.ContentType == "" {
					e.ContentType = fact.ContentType
				}
			}
		}
		formMethod, formPath, formType, formKeys = "", "", "", nil
	}
	add := func(method, raw, kind, source string) {
		// base가 다른 origin이면 `/x`도 그 origin으로 가므로 전체 주소만 받는다.
		if !baseKnown && !strings.Contains(raw, "://") {
			c.record.Snapshot.Counts.InvalidPaths++
			return
		}
		addFrom(base, method, raw, kind, source)
	}
	scriptBody := body
	if strings.Contains(contentType, "html") {
		scriptBody = nil
		inScript := false
		z := html.NewTokenizer(strings.NewReader(string(body)))
		for {
			token := z.Next()
			if token == html.ErrorToken {
				break
			}
			if token == html.TextToken && inScript {
				scriptBody = append(scriptBody, z.Text()...)
				scriptBody = append(scriptBody, '\n')
			}
			if token == html.EndTagToken {
				if name, _ := z.TagName(); string(name) == "script" {
					inScript = false
				} else if string(name) == "form" {
					finishForm()
				}
			}
			if token != html.StartTagToken && token != html.SelfClosingTagToken {
				continue
			}
			t := z.Token()
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Key] = a.Val
			}
			switch t.Data {
			case "base":
				if attrs["href"] != "" && !baseSeen {
					baseSeen = true
					base, baseKnown = c.resolve(attrs["href"], from)
					if !inferred {
						if c.documentBases == nil {
							c.documentBases = map[string]documentBase{}
						}
						c.documentBases[from] = documentBase{base, baseKnown}
					}
				}
			case "a":
				add("GET", attrs["href"], "page", "html_link")
			case "script":
				inScript = true
				if attrs["src"] != "" {
					add("GET", attrs["src"], "script", "html_script")
				}
			case "link":
				if attrs["href"] != "" && preloadsScript(attrs) {
					add("GET", attrs["href"], "script", "html_script")
				}
			case "input", "select", "textarea", "button":
				if formPath != "" && attrs["name"] != "" {
					if keys, ok := discoveryKeys([]string{attrs["name"]}); ok {
						formKeys = mergeDiscoveryKeys(formKeys, keys)
					}
				}
			case "form":
				finishForm()
				method := strings.ToUpper(attrs["method"])
				if method == "" {
					method = "GET"
				}
				action := attrs["action"]
				if action == "" {
					action = from
				}
				add(method, action, "form", "html_form")
				if baseKnown {
					formPath, _ = c.resolve(action, base)
					formMethod = normalizedMethod(method)
					formType, _ = discoveryMediaType(attrs["enctype"])
					if attrs["enctype"] == "" {
						formType = "application/x-www-form-urlencoded"
					}
				}
			}
		}

		finishForm()
	}
	if strings.Contains(contentType, "javascript") || strings.Contains(contentType, "json") || strings.Contains(contentType, "html") {
		doc, docKnown, foreignBase := base, baseKnown, !baseKnown
		if !strings.Contains(contentType, "html") {
			doc, docKnown, foreignBase = c.documentLocked(from)
		}
		for _, ref := range bundleRefs(scriptBody, c.scriptSlotsLocked()) {
			switch {
			case ref.kind != "api" || strings.Contains(ref.path, "://"):
				add(ref.method, ref.path, ref.kind, ref.source)
			case docKnown:
				// 브라우저는 XHR·fetch의 상대 주소를 스크립트가 아니라 문서 기준 주소로 푼다.
				addFrom(doc, ref.method, ref.path, ref.kind, ref.source)
			case strings.HasPrefix(ref.path, "/") && !foreignBase:
				add(ref.method, ref.path, ref.kind, ref.source)
			default:
				// 어느 경로인지 모르는 후보로 칸을 쓰지 않는다.
				c.record.Snapshot.Counts.InvalidPaths++
			}
		}
		if !foreignBase {
			if doc == "" {
				doc = from
			}
			c.discoverReconLocked(scriptBody, contentType, doc)
		}
	}
}

// 이 검사는 정적 크롤 생략만 허용한다. 실제 진단은 StartDeepWork·선택·E②의 기존 검사를 그대로 거친다.
func (c *Controller) finishedExplorationForDiagnosisLocked(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil || c.poisoned || c.explorationReader == nil || c.binding.Current == nil {
		return false
	}
	state, s := c.record.Exploration, c.record.Snapshot
	if state == nil || !c.matchingExploration(state.Binding) || c.binding.Current() != nil || ctx.Err() != nil {
		return false
	}
	b := state.Binding
	if s.Format != Version || s.JobID != b.JobID || s.TaskID != b.TaskID || s.RunRef != b.RunRef || s.SourceDigest != b.SourceDigest || s.Mode != c.binding.Mode || s.Failure != "" || !s.InitialCompleted {
		return false
	}
	if !explorationCloseReason(state.ClosedReason) || state.ClosedReason == "cancelled" || state.ClosedReason == "target_ended" || s.ExplorationReason != state.ClosedReason {
		return false
	}
	switch s.ExplorationStatus {
	case "COMPLETED":
		if s.ExplorationPartial || state.ClosedReason != "completed" {
			return false
		}
	case "PARTIAL":
		if !s.ExplorationPartial {
			return false
		}
	default:
		return false
	}
	if c.binding.Mode == Auto && s.Stage != "diagnosing" || c.binding.Mode == Manual && s.Stage != "awaiting_selection" && s.Stage != "diagnosing" || !ValidMode(c.binding.Mode) {
		return false
	}
	// private 마감 결속과 남아 있는 관측이 다른 세션으로 갈라진 상태도 통과시키지 않는다.
	for _, fact := range c.record.DiscoveryInventory {
		if fact.Confidence == "observed" && (fact.SessionRef != b.SessionRef || fact.Seq == 0 || fact.Seq > state.LastSequence) {
			return false
		}
	}
	return true
}

// 첫 보호 Worker가 같은 Bash·공유 호출 예산·E② proxy로 초기 탐색을 수행한다.
// 정적 발견은 실제 방문이나 SPA 동적 탐색 성공을 의미하지 않는다.
func (c *Controller) Crawl(ctx context.Context, fetch Fetch) (handled bool, failure error) {
	c.mu.Lock()
	// 별도 탐색이 저장된 마감까지 끝났으면 정적 송신을 반복하지 않고 기존 진단 게이트로 넘긴다.
	if c.record.Exploration != nil {
		skip := c.finishedExplorationForDiagnosisLocked(ctx)
		c.mu.Unlock()
		if skip {
			return false, nil
		}
		return true, ErrExplorationBinding
	}
	if c.claimed {
		failed := c.poisoned || c.record.Snapshot.Stage == "failed"
		c.mu.Unlock()
		if failed {
			return true, ErrDenied
		}
		return false, nil
	}
	c.claimed = true
	c.mu.Unlock()
	defer func() {
		if failure != nil && ctx.Err() == nil {
			_ = c.Fail(context.WithoutCancel(ctx), "crawl_failed")
		}
		c.mu.Lock()
		c.nativePath = ""
		c.mu.Unlock()
	}()
	c.mu.Lock()
	if fetch == nil || c.poisoned || c.record.Snapshot.Stage != "crawling" {
		c.mu.Unlock()
		return true, ErrUnavailable
	}
	if err := c.saveLocked(ctx, false, "crawl_started", "", "", 0); err != nil {
		c.mu.Unlock()
		return true, err
	}
	c.mu.Unlock()
	attempted := map[string]bool{}
	observed := 0
	for page := 0; page < c.binding.Limits.MaxPages; page++ {
		if err := c.Wait(ctx); err != nil {
			return true, err
		}
		c.mu.Lock()
		if c.record.Snapshot.Paused {
			c.mu.Unlock()
			page--
			continue
		}
		path := ""
		bestID, bestRank := "", 0
		for _, e := range c.record.Snapshot.Endpoints {
			if e.Method == "GET" && e.InScope && e.Change == "attested_read" && !e.Visited && !attempted[e.ID] {
				if rank := crawlRank(e); bestID == "" || rank < bestRank {
					path, bestID, bestRank = e.Path, e.ID, rank
				}
			}
		}
		if bestID != "" {
			attempted[bestID] = true
		}
		if path == "" {
			c.mu.Unlock()
			break
		}
		c.record.Snapshot.CurrentPath = path
		c.nativePath = path // This one program-owned fetch is admitted before pause.
		if err := c.saveLocked(ctx, false, "visit_started", "GET", path, 0); err != nil {
			c.mu.Unlock()
			return true, err
		}
		c.mu.Unlock()
		result, fetchErr := fetch(ctx, c.origin+path)
		c.mu.Lock()
		c.nativePath = ""
		c.mu.Unlock()
		trusted := result.Trusted
		if trusted.Version != e2proxy.Version || !trusted.Final || !trusted.Call.Final || !trusted.ProcessDrained || !trusted.ProxyDrained || !trusted.Call.AuditComplete || trusted.Call.Counts.Unknown != 0 {
			return true, ErrUnavailable
		}
		pageError := ""
		var body []byte
		contentType := ""
		if fetchErr != nil || trusted.ExitCode != 0 || trusted.Call.Counts.ForwardAttempted != 1 {
			pageError = "fetch_failed"
		} else {
			response, parseErr := http.ReadResponse(bufio.NewReader(strings.NewReader(result.ToolOutput)), &http.Request{Method: "GET"})
			if parseErr != nil {
				pageError = "response_parse_failed"
			} else {
				contentType = response.Header.Get("Content-Type")
				var readErr error
				body, readErr = io.ReadAll(io.LimitReader(response.Body, int64(c.binding.Limits.MaxResponseBytes)+1))
				_ = response.Body.Close()
				if boundedStatus(response.StatusCode) == 0 {
					pageError = "unsupported_status"
				} else if readErr != nil || len(body) > c.binding.Limits.MaxResponseBytes || trusted.OutputTruncated {
					pageError = "body_incomplete"
				} else if response.StatusCode >= 400 {
					pageError = "http_error"
				}
			}
		}
		c.mu.Lock()
		if pageError == "" {
			observed++
			c.discoverBodyLocked(body, contentType, path)
		} else if e := c.endpointLocked("GET", path); e != nil {
			e.ObservationError = pageError
		}
		clear(body)
		kind := "visit_finished"
		if pageError != "" {
			kind = "page_failed"
		}
		err := c.saveLocked(ctx, false, kind, "GET", path, 0)
		c.mu.Unlock()
		if err != nil {
			return true, err
		}
	}
	if observed == 0 {
		return true, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record.Snapshot.InitialCompleted = true
	c.record.Snapshot.CurrentPath = ""
	if c.binding.Mode == Manual {
		c.record.Snapshot.Stage = "awaiting_selection"
	} else {
		c.record.Snapshot.Stage = "diagnosing"
		for i := range c.record.Snapshot.Endpoints {
			e := &c.record.Snapshot.Endpoints[i]
			e.Selected = e.InScope && e.Change == "attested_read" && e.Kind != "script" && (e.ObservationError == "" || e.ObservationError == "body_incomplete")
		}
	}
	return true, c.saveLocked(ctx, false, "crawl_finished", "", "", 0)
}
