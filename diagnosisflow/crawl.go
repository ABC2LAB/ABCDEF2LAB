package diagnosisflow

import (
	"bufio"
	"context"
	"golang.org/x/net/html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var apiLiteral = regexp.MustCompile("[\"'`](/(?:api|rest)/[^\"'`\\s<>]{1,160})[\"'`]")
var methodLiteral = regexp.MustCompile("(?i)\\.(get|post|put|patch|delete)\\(\\s*[\"'`]([^\"'`\\s<>]{1,180})[\"'`]")

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
	p := u.Path
	if p == "" {
		p = "/"
	}
	if len(p) > 128 || strings.Contains(p, "//") {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return "", false
		}
	}
	return p, true
}
func (c *Controller) discoverBodyLocked(body []byte, contentType, from string) {
	add := func(method, raw, kind, source string) {
		if p, ok := c.resolve(raw, from); ok {
			c.addLocked(method, p, kind, source, from)
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
		explicit := map[string]bool{}
		for _, match := range methodLiteral.FindAllSubmatch(scriptBody, 128) {
			method, raw := strings.ToUpper(string(match[1])), string(match[2])
			explicit[raw] = true
			add(method, raw, "api", "script_call")
		}
		for _, match := range apiLiteral.FindAllSubmatch(scriptBody, 128) {
			raw := string(match[1])
			if !explicit[raw] {
				add("UNKNOWN", raw, "api", "static_literal")
			}
		}
	}
}

// 첫 보호 Worker가 같은 Bash·공유 호출 예산·E② proxy로 초기 탐색을 수행한다.
// 정적 발견은 실제 방문이나 SPA 동적 탐색 성공을 의미하지 않는다.
func (c *Controller) Crawl(ctx context.Context, fetch Fetch) (bool, error) {
	c.mu.Lock()
	if c.claimed {
		c.mu.Unlock()
		return false, nil
	}
	c.claimed = true
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
	for page := 0; page < c.binding.Limits.MaxPages; page++ {
		if err := c.Wait(ctx); err != nil {
			return true, err
		}
		c.mu.Lock()
		path := ""
		for _, e := range c.record.Snapshot.Endpoints {
			if e.Method == "GET" && e.InScope && e.Change == "attested_read" && !e.Visited && !attempted[e.ID] {
				path = e.Path
				attempted[e.ID] = true
				break
			}
		}
		if path == "" {
			c.mu.Unlock()
			break
		}
		c.record.Snapshot.CurrentPath = path
		if err := c.saveLocked(ctx, false, "visit_started", "GET", path, 0); err != nil {
			c.mu.Unlock()
			return true, err
		}
		c.mu.Unlock()
		result, err := fetch(ctx, c.origin+path)
		if err != nil || !result.Trusted.Final || !result.Trusted.ProcessDrained || !result.Trusted.ProxyDrained || !result.Trusted.Call.AuditComplete || result.Trusted.Call.Counts.ForwardAttempted != 1 || result.Trusted.Call.Counts.Unknown != 0 {
			return true, ErrUnavailable
		}
		response, parseErr := http.ReadResponse(bufio.NewReader(strings.NewReader(result.ToolOutput)), &http.Request{Method: "GET"})
		c.mu.Lock()
		if parseErr == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, int64(c.binding.Limits.MaxResponseBytes)+1))
			_ = response.Body.Close()
			if readErr == nil && len(body) <= c.binding.Limits.MaxResponseBytes && !result.Trusted.OutputTruncated {
				c.discoverBodyLocked(body, response.Header.Get("Content-Type"), path)
			}
			clear(body)
		}
		if err := c.saveLocked(ctx, false, "visit_finished", "GET", path, 0); err != nil {
			c.mu.Unlock()
			return true, err
		}
		c.mu.Unlock()
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
			e.Selected = e.InScope && e.Change == "attested_read"
		}
	}
	return true, c.saveLocked(ctx, false, "crawl_finished", "", "", 0)
}
