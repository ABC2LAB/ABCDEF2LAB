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
func (c *Controller) discoverBodyLocked(body []byte, contentType, from string) {
	add := func(method, raw, kind, source string) {
		if p, ok := c.resolve(raw, from); ok {
			c.addLocked(method, p, kind, source, from)
		} else {
			c.record.Snapshot.Counts.InvalidPaths++
		}
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
			case "a":
				add("GET", attrs["href"], "page", "html_link")
			case "script":
				inScript = true
				if attrs["src"] != "" {
					add("GET", attrs["src"], "script", "html_script")
				}
			case "link":
				if rel := strings.ToLower(attrs["rel"]); rel == "modulepreload" || rel == "preload" && strings.EqualFold(attrs["as"], "script") {
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
		for _, ref := range bundleRefs(scriptBody) {
			add(ref.method, ref.path, ref.kind, ref.source)
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
