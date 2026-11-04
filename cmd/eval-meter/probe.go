package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"
)

// A plan names a handful of paths; anything larger is not a probe plan.
const (
	maxProbePlanBytes = 64 << 10
	maxProbeChecks    = 64
	// Reading a little of the body confirms the response arrived; the
	// content itself is never inspected or printed.
	maxProbeBodyBytes = 4 << 10
)

const (
	probeTCP     = "tcp"
	probeHTTPGet = "http_get"
	probeDNS     = "dns"
)

type probePlan struct {
	TimeoutMS int          `json:"timeout_ms"`
	Checks    []probeCheck `json:"checks"`
}

type probeCheck struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
	URL     string `json:"url,omitempty"`
	Name    string `json:"name,omitempty"`
}

// probeResult reports what happened, not whether it was expected; the runner
// compares results with its own expectations so the probe cannot vouch for
// itself.
type probeResult struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Observed string `json:"observed"`
	Code     string `json:"code"`
}

type probeReport struct {
	Results []probeResult `json:"results"`
}

func runProbe(stdin io.Reader, stdout, stderr io.Writer) int {
	plan, ok := readProbePlan(stdin)
	if !ok {
		return fail(stderr, invalidInput)
	}
	timeout := time.Duration(plan.TimeoutMS) * time.Millisecond
	report := probeReport{Results: make([]probeResult, 0, len(plan.Checks))}
	for _, check := range plan.Checks {
		observed, code := probeOnce(check, timeout)
		report.Results = append(report.Results, probeResult{ID: check.ID, Kind: check.Kind, Observed: observed, Code: code})
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

func readProbePlan(stdin io.Reader) (probePlan, bool) {
	var plan probePlan
	body, err := io.ReadAll(io.LimitReader(stdin, maxProbePlanBytes+1))
	if err != nil || len(body) > maxProbePlanBytes {
		return plan, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil || decoder.More() {
		return plan, false
	}
	if plan.TimeoutMS <= 0 || len(plan.Checks) == 0 || len(plan.Checks) > maxProbeChecks {
		return plan, false
	}
	seen := make(map[string]bool, len(plan.Checks))
	for _, check := range plan.Checks {
		if check.ID == "" || seen[check.ID] || !validProbeCheck(check) {
			return plan, false
		}
		seen[check.ID] = true
	}
	return plan, true
}

func validProbeCheck(check probeCheck) bool {
	switch check.Kind {
	case probeTCP:
		host, port, err := net.SplitHostPort(check.Address)
		if err != nil || host == "" || check.URL != "" || check.Name != "" {
			return false
		}
		n, err := strconv.Atoi(port)
		return err == nil && n > 0 && n <= 65535
	case probeHTTPGet:
		u, err := url.Parse(check.URL)
		return err == nil && u.Scheme == "http" && u.Host != "" && u.User == nil && u.Fragment == "" &&
			check.Address == "" && check.Name == ""
	case probeDNS:
		return check.Name != "" && check.Address == "" && check.URL == ""
	default:
		return false
	}
}

func probeOnce(check probeCheck, timeout time.Duration) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	switch check.Kind {
	case probeTCP:
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", check.Address)
		if err != nil {
			return "fail", dialCode(err)
		}
		if err := conn.Close(); err != nil {
			return "success", "connected_close_error"
		}
		return "success", "connected"
	case probeHTTPGet:
		return probeHTTP(ctx, check.URL)
	case probeDNS:
		addrs, err := lookupHost(ctx, check.Name)
		if err != nil || len(addrs) == 0 {
			return "fail", dnsCode(err)
		}
		return "success", "resolved"
	}
	return "fail", "unknown_kind"
}

// probeHTTP never follows redirects or uses an environment proxy: either
// would test a different path from the one the runner asked about.
func probeHTTP(ctx context.Context, rawURL string) (string, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "fail", "request_invalid"
	}
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "fail", dialCode(err)
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBodyBytes))
	closeErr := resp.Body.Close()
	code := "http_" + strconv.Itoa(resp.StatusCode)
	if readErr != nil || closeErr != nil {
		code += "_body_error"
	}
	return "success", code
}

func dialCode(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "unreachable"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsCode(err)
	}
	return "error"
}

// lookupHost is the resolver of the namespace the probe runs in. Tests replace
// it so a missing name does not depend on reaching a DNS server.
var lookupHost = net.DefaultResolver.LookupHost

func dnsCode(err error) string {
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		return "no_address"
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "not_found"
	case errors.As(err, &dnsErr) && dnsErr.IsTimeout, errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "dns_error"
}
