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
	return c.binding.Limits.MaxEndpoints/2 - used
}

// 스크립트를 참조한 문서를 따라 올라간다. 문서를 모르면 false다.
func (c *Controller) documentLocked(from string) (string, bool) {
	for range c.record.Snapshot.Endpoints {
		e := c.endpointLocked("GET", from)
		if e == nil {
			return "", false
		}
		if e.Kind != "script" {
			if b, ok := c.documentBases[from]; ok {
				return b.path, b.known
			}
			return from, true
		}
		from = e.From
	}
	return "", false
}

func absoluteRef(raw string) bool {
	return strings.HasPrefix(raw, "/") || strings.Contains(raw, "://")
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
	addFrom := func(base, method, raw, kind, source string) {
		if p, ok := c.resolve(raw, base); ok {
			c.addLocked(method, p, kind, source, from)
		} else {
			c.record.Snapshot.Counts.InvalidPaths++
		}
	}
	// 문서의 기준 주소다. <base href>를 만나면 바뀌고, 다른 origin을 가리키면 상대 주소를 풀 수 없다.
	base, baseKnown, baseSeen := from, true, false
	add := func(method, raw, kind, source string) {
		if !baseKnown && !absoluteRef(raw) {
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
					if c.documentBases == nil {
						c.documentBases = map[string]documentBase{}
					}
					c.documentBases[from] = documentBase{base, baseKnown}
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
			case "form":
				method := strings.ToUpper(attrs["method"])
				if method == "" {
					method = "GET"
				}
				action := attrs["action"]
				if action == "" {
					action = from
				}
				add(method, action, "form", "html_form")
			}
		}
	}
	if strings.Contains(contentType, "javascript") || strings.Contains(contentType, "json") || strings.Contains(contentType, "html") {
		doc, docKnown := base, baseKnown
		if !strings.Contains(contentType, "html") {
			doc, docKnown = c.documentLocked(from)
		}
		for _, ref := range bundleRefs(scriptBody, c.scriptSlotsLocked()) {
			switch {
			case ref.kind != "api" || absoluteRef(ref.path):
				add(ref.method, ref.path, ref.kind, ref.source)
			case docKnown:
				// 브라우저는 XHR·fetch의 상대 주소를 스크립트가 아니라 문서 기준 주소로 푼다.
				addFrom(doc, ref.method, ref.path, ref.kind, ref.source)
			default:
				// 어느 경로인지 모르는 후보로 칸을 쓰지 않는다.
				c.record.Snapshot.Counts.InvalidPaths++
			}
		}
	}
}

// 첫 보호 Worker가 같은 Bash·공유 호출 예산·E② proxy로 초기 탐색을 수행한다.
// 정적 발견은 실제 방문이나 SPA 동적 탐색 성공을 의미하지 않는다.
func (c *Controller) Crawl(ctx context.Context, fetch Fetch) (handled bool, failure error) {
	c.mu.Lock()
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
			e.Selected = e.InScope && e.Change == "attested_read" && (e.ObservationError == "" || e.ObservationError == "body_incomplete")
		}
	}
	return true, c.saveLocked(ctx, false, "crawl_finished", "", "", 0)
}
