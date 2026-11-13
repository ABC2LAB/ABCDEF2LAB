package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 설치 입력 web_search는 외부 API(OpenRouter)만 쓰는 설치만 받는다. 로컬 모델이 섞인 설치는 거부한다(#558 재상 요청 1).
func TestProductWebSearchInstallInput(t *testing.T) {
	external := productInstallInput{OpenRouterManifest: json.RawMessage(`{}`)}
	mixed := external
	mixed.LocalModel = &struct {
		Model          string   `json:"model"`
		BaseURL        string   `json:"base_url"`
		Addresses      []string `json:"addresses"`
		ContextWindowK int      `json:"context_window_k"`
		MaxTokens      int      `json:"max_tokens"`
	}{Model: "local:fixture"}
	localOnly := productInstallInput{LocalModel: mixed.LocalModel}
	for _, tc := range []struct {
		name  string
		input productInstallInput
		w     *productWebSearch
		ok    bool
	}{
		{"absent", localOnly, nil, true},
		{"ddgs-external", external, &productWebSearch{Backend: "ddgs"}, true},
		{"tavily-external", external, &productWebSearch{Backend: "tavily", APIKeyFile: "/tmp/product-private/search.key"}, true},
		{"ddgs-local", localOnly, &productWebSearch{Backend: "ddgs"}, false},
		{"ddgs-mixed", mixed, &productWebSearch{Backend: "ddgs"}, false},
		{"ddgs-with-key", external, &productWebSearch{Backend: "ddgs", APIKeyFile: "/tmp/product-private/search.key"}, false},
		{"brave-without-key", external, &productWebSearch{Backend: "brave-free"}, false},
		{"deepseek", external, &productWebSearch{Backend: "deepseek"}, false},
	} {
		tc.input.WebSearch = tc.w
		if validProductWebSearch(tc.input) != tc.ok {
			t.Fatal(tc.name)
		}
	}
}

// 키는 설치 디렉터리의 0600 보호 파일로만 옮기고 launch에는 pin(파일·SHA-256)만 적는다(#558 재상 요청 2).
func TestProductWebSearchLaunchKeepsKeyInProtectedFile(t *testing.T) {
	private := t.TempDir()
	config := t.TempDir()
	for _, d := range []string{private, config} {
		if err := os.Chmod(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	keyFile := filepath.Join(private, "search.key")
	if err := os.WriteFile(keyFile, []byte(" synthetic-brave-key \n"), 0600); err != nil {
		t.Fatal(err)
	}
	pins := productPinWriter{dir: config}
	launch, err := productWebSearchLaunch(&productWebSearch{Backend: "brave-free", APIKeyFile: keyFile}, &pins, config)
	if err != nil || launch == nil || launch.Backend != "brave-free" || launch.KeyFile != filepath.Join(config, "web-search.key") || launch.KeySHA256 != urlSHA([]byte("synthetic-brave-key")) {
		t.Fatalf("launch=%+v err=%v", launch, err)
	}
	info, err := os.Stat(launch.KeyFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key file not protected", err)
	}
	encoded, _ := json.Marshal(launch)
	if strings.Contains(string(encoded), "synthetic-brave-key") {
		t.Fatal("key value in launch", string(encoded))
	}
	c := launchConfig{WebSearch: launch}
	search, err := c.webSearch()
	if err != nil || search.BraveKey != "synthetic-brave-key" {
		t.Fatal("installed key not readable through its pin", err)
	}
	if launch, err := productWebSearchLaunch(&productWebSearch{Backend: "ddgs"}, &pins, config); err != nil || launch == nil || launch.KeyFile != "" || launch.KeySHA256 != "" {
		t.Fatal("ddgs launch", launch, err)
	}
	empty := filepath.Join(private, "empty.key")
	if err := os.WriteFile(empty, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := productWebSearchLaunch(&productWebSearch{Backend: "tavily", APIKeyFile: empty}, &productPinWriter{dir: t.TempDir()}, config); err == nil {
		t.Fatal("empty key accepted")
	}
	if launch, err := productWebSearchLaunch(nil, &pins, config); err != nil || launch != nil {
		t.Fatal("absent setting produced a launch block")
	}
}
