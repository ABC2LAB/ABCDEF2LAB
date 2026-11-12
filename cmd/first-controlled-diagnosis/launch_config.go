package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/llmpolicy"
)

const launchFormat = "abc2lab.first-diagnosis-launch/v1"
const maxLaunchBytes = 64 << 10

type launchConfig struct {
	Format                 string `json:"format"`
	Mode                   string `json:"mode"`
	ListenAddr             string `json:"listen_addr"`
	DataDir                string `json:"data_dir"`
	KeyDir                 string `json:"key_dir"`
	SkillDir               string `json:"skill_dir"`
	LocalLLMManifestPath   string `json:"local_llm_manifest_path"`
	ModelExecutionMode     string `json:"model_execution_mode,omitempty"`
	OpenRouterManifestPath string `json:"openrouter_manifest_path,omitempty"`
	ModelProfileID         int64  `json:"model_profile_id"`
	ModelFormat            string `json:"model_format"`
	ModelBaseURL           string `json:"model_base_url"`
	ModelRef               string `json:"model_ref"`
	RuntimeDSNFile         string `json:"runtime_dsn_file"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
	// ModelConcurrency는 동시 모델 호출 수다(C32). 없거나 0이면 1이다.
	ModelConcurrency int               `json:"model_concurrency,omitempty"`
	Registered       *registeredLaunch `json:"registered_get,omitempty"`
	HTTP             *httpLaunch       `json:"general_http,omitempty"`
}
type registeredLaunch struct {
	AuthorityDSNFile string `json:"authority_dsn_file"`
	HostBindingFile  string `json:"host_binding_file"`
	MetadataFile     string `json:"metadata_file"`
	SnapshotDir      string `json:"snapshot_dir"`
	StateDir         string `json:"state_dir"`
	RegistryFile     string `json:"registry_file"`
}
type httpLaunch struct {
	PolicyFile    string `json:"policy_file"`
	AuthorityFile string `json:"authority_file"`
	AuthorityRoot string `json:"authority_root"`
}

type taskPreparation struct {
	Description    string `json:"description"`
	Goal           string `json:"goal"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func parseLaunchConfig(data []byte) (launchConfig, error) {
	var c launchConfig
	if len(data) == 0 || len(data) > maxLaunchBytes || !utf8.Valid(data) || decodeLaunchJSON(data, &c) != nil || c.Format != launchFormat || c.ModelProfileID <= 0 || c.ShutdownTimeoutSeconds < 1 || c.ShutdownTimeoutSeconds > 60 {
		return launchConfig{}, errPreparation
	}
	paths := []string{c.DataDir, c.KeyDir, c.SkillDir, c.RuntimeDSNFile}
	switch c.ModelExecutionMode {
	case "", "local_only":
		if c.OpenRouterManifestPath != "" {
			return launchConfig{}, errPreparation
		}
		paths = append(paths, c.LocalLLMManifestPath)
	case "openrouter":
		if c.Mode != "general_http" || c.LocalLLMManifestPath != "" || c.ModelFormat != "openai" || c.ModelBaseURL != llmpolicy.OpenRouterBaseURL || c.ModelRef != llmpolicy.OpenRouterModel {
			return launchConfig{}, errPreparation
		}
		paths = append(paths, c.OpenRouterManifestPath)
	default:
		return launchConfig{}, errPreparation
	}
	if c.Mode == "registered_get" && c.Registered != nil && c.HTTP == nil {
		r := c.Registered
		paths = append(paths, r.AuthorityDSNFile, r.HostBindingFile, r.MetadataFile, r.SnapshotDir, r.StateDir)
		if r.RegistryFile == "" || r.RegistryFile == "." || r.RegistryFile == ".." || filepath.Base(r.RegistryFile) != r.RegistryFile || strings.ContainsAny(r.RegistryFile, "\\/\x00") {
			return launchConfig{}, errPreparation
		}
	} else if c.Mode == "general_http" && c.HTTP != nil && c.Registered == nil {
		paths = append(paths, c.HTTP.PolicyFile, c.HTTP.AuthorityFile, c.HTTP.AuthorityRoot)
	} else {
		return launchConfig{}, errPreparation
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !plainText(path, maxPathBytes) {
			return launchConfig{}, errPreparation
		}
	}
	rel, err := filepath.Rel(c.DataDir, c.KeyDir)
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return launchConfig{}, errPreparation
	}
	host, port, err := net.SplitHostPort(c.ListenAddr)
	n, e := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || e != nil || n < 1 || n > 65535 || host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return launchConfig{}, errPreparation
	}
	if !plainText(c.ModelFormat, maxFormatBytes) || !plainText(c.ModelBaseURL, maxPathBytes) || !plainText(c.ModelRef, maxModelBytes) {
		return launchConfig{}, errPreparation
	}
	return c, nil
}

// 모든 계층에서 중복 키·null·알 수 없는 키·대소문자 별칭을 거부한다.
func decodeLaunchJSON(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var read func() error
	read = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return errPreparation
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return errPreparation
				}
				seen[name] = true
				if read() != nil {
					return errPreparation
				}
			}
		case '[':
			for d.More() {
				if read() != nil {
					return errPreparation
				}
			}
		default:
			return errPreparation
		}
		_, err = d.Token()
		return err
	}
	if read() != nil {
		return errPreparation
	}
	if _, err := d.Token(); err != io.EOF {
		return errPreparation
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errPreparation
	}
	var a, b any
	if json.Unmarshal(data, &a) != nil {
		return errPreparation
	}
	out, err := json.Marshal(dst)
	if err != nil || json.Unmarshal(out, &b) != nil || !launchJSONKeys(a, b) {
		return errPreparation
	}
	return nil
}
func launchJSONKeys(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range x {
			other, found := y[key]
			if !found || !launchJSONKeys(value, other) {
				return false
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !launchJSONKeys(x[i], y[i]) {
				return false
			}
		}
	}
	return true
}
