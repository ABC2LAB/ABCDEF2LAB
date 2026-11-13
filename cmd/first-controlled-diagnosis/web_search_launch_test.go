package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// launch의 web_search는 외부 API 일반 HTTP launch만 받고, 키는 pin(SHA-256)이 맞을 때만 읽는다. 키 값은 설정 묶음의
// 출력 형식에 나오지 않는다(루프 복원 결정안 §2-1).
func TestLaunchWebSearchOnlyExternalAndPinned(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "web-search.key")
	if err := os.WriteFile(keyFile, []byte("synthetic-tavily-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest := urlSHA([]byte("synthetic-tavily-key\n"))
	parse := func(base map[string]any, search map[string]any) (launchConfig, error) {
		t.Helper()
		if search != nil {
			base["web_search"] = search
		}
		raw, _ := json.Marshal(base)
		return parseLaunchConfig(raw)
	}
	c, err := parse(openRouterLaunchConfig(), map[string]any{"backend": "tavily", "key_file": keyFile, "key_sha256": digest})
	if err != nil {
		t.Fatal("external launch with pinned key rejected", err)
	}
	model, err := c.modelOptions()
	host, ok := c.httpHostConfig()
	if err != nil || !ok || model.WebSearch.Backend != "tavily" || model.WebSearch.TavilyKey != "synthetic-tavily-key" || host.WebSearch != model.WebSearch {
		t.Fatal("search setting not carried to preparation and run servers", err, ok)
	}
	if out := fmt.Sprintf("%v %+v %#v %v", model, model, host, c.WebSearch); strings.Contains(out, "synthetic-tavily-key") {
		t.Fatal("search key printed", out)
	}
	if c, err = parse(openRouterLaunchConfig(), map[string]any{"backend": "ddgs"}); err != nil {
		t.Fatal("ddgs rejected", err)
	} else if model, err := c.modelOptions(); err != nil || model.WebSearch.Backend != "ddgs" || model.WebSearch.BraveKey != "" || model.WebSearch.TavilyKey != "" {
		t.Fatal("ddgs setting", err)
	}
	for name, tc := range map[string]struct {
		base   map[string]any
		search map[string]any
	}{
		"local-mode":          {launchTestConfig(), map[string]any{"backend": "ddgs"}},
		"unknown-backend":     {openRouterLaunchConfig(), map[string]any{"backend": "deepseek"}},
		"ddgs-with-key":       {openRouterLaunchConfig(), map[string]any{"backend": "ddgs", "key_file": keyFile, "key_sha256": digest}},
		"brave-without-key":   {openRouterLaunchConfig(), map[string]any{"backend": "brave-free"}},
		"relative-key":        {openRouterLaunchConfig(), map[string]any{"backend": "tavily", "key_file": "web-search.key", "key_sha256": digest}},
		"bad-digest-format":   {openRouterLaunchConfig(), map[string]any{"backend": "tavily", "key_file": keyFile, "key_sha256": strings.ToUpper(digest)}},
		"unknown-field":       {openRouterLaunchConfig(), map[string]any{"backend": "ddgs", "proxy": "http://127.0.0.1:1"}},
		"registered-get-mode": {func() map[string]any { c := openRouterLaunchConfig(); c["mode"] = "registered_get"; return c }(), map[string]any{"backend": "ddgs"}},
	} {
		if _, err := parse(tc.base, tc.search); err == nil {
			t.Fatal(name, "accepted")
		}
	}
	// 키 파일이 pin과 다르면 실행·준비 설정을 만들지 않는다.
	c, err = parse(openRouterLaunchConfig(), map[string]any{"backend": "tavily", "key_file": keyFile, "key_sha256": urlSHA([]byte("other"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.modelOptions(); err == nil {
		t.Fatal("key with a different digest loaded")
	}
	if _, ok := c.httpHostConfig(); ok {
		t.Fatal("run host built with a different key digest")
	}
}
