package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxConfigBytes     = 16 << 10
	maxPathBytes       = 4096
	maxFormatBytes     = 128
	maxModelBytes      = 256
	maxShutdownSeconds = 300
)

var errPreparation = errors.New("diagnosis_preparation_unavailable")

type preparationConfig struct {
	ListenAddr             string `json:"listen_addr"`
	DataDir                string `json:"data_dir"`
	KeyDir                 string `json:"key_dir"`
	SkillDir               string `json:"skill_dir"`
	SnapshotDir            string `json:"snapshot_dir"`
	StateDir               string `json:"state_dir"`
	LocalLLMManifestPath   string `json:"local_llm_manifest_path"`
	TargetOrigin           string `json:"target_origin"`
	ModelProfileID         int64  `json:"model_profile_id"`
	ModelFormat            string `json:"model_format"`
	ModelBaseURL           string `json:"model_base_url"`
	ModelRef               string `json:"model_ref"`
	ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
	EvalRunID              string `json:"eval_run_id"`
}

var configFields = map[string]bool{
	"listen_addr": true, "data_dir": true, "key_dir": true, "skill_dir": true,
	"snapshot_dir": true, "state_dir": true, "local_llm_manifest_path": true,
	"target_origin": true, "model_profile_id": true, "model_format": true,
	"model_base_url": true, "model_ref": true, "shutdown_timeout_seconds": true, "eval_run_id": true,
}

func parseConfig(data []byte) (preparationConfig, error) {
	refuse := func() (preparationConfig, error) { return preparationConfig{}, errPreparation }
	if len(data) == 0 || len(data) > maxConfigBytes || !utf8.Valid(data) {
		return refuse()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return refuse()
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !configFields[key] || seen[key] {
			return refuse()
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return refuse()
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return refuse()
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return refuse()
	}
	if _, err = d.Token(); err != io.EOF {
		return refuse()
	}
	var c preparationConfig
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || !validConfig(c) {
		return refuse()
	}
	return c, nil
}

func validConfig(c preparationConfig) bool {
	if c.EvalRunID != "" || c.ModelProfileID <= 0 || c.ShutdownTimeoutSeconds < 1 || c.ShutdownTimeoutSeconds > maxShutdownSeconds {
		return false
	}
	for _, path := range []string{c.DataDir, c.KeyDir, c.SkillDir, c.SnapshotDir, c.StateDir, c.LocalLLMManifestPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !plainText(path, maxPathBytes) {
			return false
		}
	}
	relativeKeyPath, keyErr := filepath.Rel(c.DataDir, c.KeyDir)
	if keyErr != nil || relativeKeyPath == "." || relativeKeyPath != ".." && !strings.HasPrefix(relativeKeyPath, ".."+string(filepath.Separator)) {
		return false
	}
	host, port, err := net.SplitHostPort(c.ListenAddr)
	p, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || p < 1 || p > 65535 {
		return false
	}
	address := net.ParseIP(host)
	if host != "localhost" && (address == nil || !address.IsLoopback()) {
		return false
	}
	u, err := url.Parse(c.TargetOrigin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(c.TargetOrigin, "?#") ||
		u.Path != "" && u.Path != "/" {
		return false
	}
	if u.Port() != "" {
		p, err = strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return false
		}
	}
	return plainText(c.TargetOrigin, maxPathBytes) && plainText(c.ModelFormat, maxFormatBytes) && plainText(c.ModelBaseURL, maxPathBytes) && plainText(c.ModelRef, maxModelBytes)
}

func plainText(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}
