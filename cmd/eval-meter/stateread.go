package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Target state kept only in memory cannot be copied, so the runner reads it
// through registered read-only paths. This role runs once in the target's
// network namespace, outside the isolation window, and only ever talks to
// loopback: a plan cannot point it anywhere else.
const (
	maxStateReadPlanBytes = 64 << 10
	maxStateReadPaths     = 256
	maxStateReadBodyBytes = 16 << 20
	maxStateReadTimeoutMS = 600_000
)

const (
	stateReadOK         = "ok"
	stateReadStatus     = "status"
	stateReadTransport  = "transport"
	stateReadTooLarge   = "too_large"
	stateReadNotCounted = "not_counted"
)

type stateReadPlan struct {
	Port         int      `json:"port"`
	TimeoutMS    int      `json:"timeout_ms"`
	MaxBodyBytes int64    `json:"max_body_bytes"`
	CountField   string   `json:"count_field"`
	Paths        []string `json:"paths"`
}

// stateReadResult is positional, one per plan path. It carries no path and no
// body: the digest stays in the runner's memory and is never written.
type stateReadResult struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
	Count  *int   `json:"count"`
	SHA256 string `json:"sha256"`
}

type stateReadReport struct {
	Results []stateReadResult `json:"results"`
}

func runStateRead(stdin io.Reader, stdout, stderr io.Writer) int {
	plan, ok := readStateReadPlan(stdin)
	if !ok {
		return fail(stderr, invalidInput)
	}
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	report := stateReadReport{Results: make([]stateReadResult, 0, len(plan.Paths))}
	for _, path := range plan.Paths {
		report.Results = append(report.Results, stateReadOnce(client, plan, path))
	}
	out, err := json.Marshal(report)
	if err != nil {
		return fail(stderr, probeOutputFailed)
	}
	if _, err := fmt.Fprintln(stdout, string(out)); err != nil {
		return fail(stderr, probeOutputFailed)
	}
	return 0
}

func readStateReadPlan(stdin io.Reader) (stateReadPlan, bool) {
	var plan stateReadPlan
	body, err := io.ReadAll(io.LimitReader(stdin, maxStateReadPlanBytes+1))
	if err != nil || len(body) > maxStateReadPlanBytes {
		return plan, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil || decoder.More() {
		return plan, false
	}
	if plan.Port <= 0 || plan.Port > 65535 || plan.TimeoutMS <= 0 || plan.TimeoutMS > maxStateReadTimeoutMS || plan.CountField == "" ||
		plan.MaxBodyBytes <= 0 || plan.MaxBodyBytes > maxStateReadBodyBytes ||
		len(plan.Paths) == 0 || len(plan.Paths) > maxStateReadPaths {
		return plan, false
	}
	for _, path := range plan.Paths {
		if !validAllowPath(path) {
			return plan, false
		}
	}
	return plan, true
}

func stateReadOnce(client *http.Client, plan stateReadPlan, path string) stateReadResult {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(plan.TimeoutMS)*time.Millisecond)
	defer cancel()
	target := "http://127.0.0.1:" + strconv.Itoa(plan.Port) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return stateReadResult{Code: stateReadTransport}
	}
	resp, err := client.Do(req)
	if err != nil {
		return stateReadResult{Code: stateReadTransport}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, plan.MaxBodyBytes+1))
	closeErr := resp.Body.Close()
	result := stateReadResult{Status: resp.StatusCode}
	switch {
	case readErr != nil || closeErr != nil:
		result.Code = stateReadTransport
	case int64(len(body)) > plan.MaxBodyBytes:
		result.Code = stateReadTooLarge
	case resp.StatusCode != http.StatusOK:
		result.Code = stateReadStatus
	default:
		digest := sha256.Sum256(body)
		result.SHA256 = hex.EncodeToString(digest[:])
		result.Count = stateReadCount(body, plan.CountField)
		result.Code = stateReadOK
		if result.Count == nil {
			result.Code = stateReadNotCounted
		}
	}
	return result
}

// stateReadCount is the length of the named top-level JSON array, or nil when
// the body does not have that shape.
func stateReadCount(body []byte, field string) *int {
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	var items []json.RawMessage
	raw, present := document[field]
	if !present || json.Unmarshal(raw, &items) != nil || items == nil {
		return nil
	}
	count := len(items)
	return &count
}
