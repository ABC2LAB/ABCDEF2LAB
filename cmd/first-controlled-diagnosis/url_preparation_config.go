package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Autumn-27/artex/targetaddr"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
	"github.com/jackc/pgx/v5"
)

const urlPreparationFormat = "abc2lab.url-preparation-installation/v1"

// urlMaxRunSeconds는 회차 하나의 실행 시간 상한(초)이다. 설치 프로파일 검사와 설치 입력 runtime_limits.run_seconds
// 검사가 같은 값을 쓴다.
const urlMaxRunSeconds = 600

type urlProtectedPin struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}
type urlProtectedEndpoint struct {
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	Addresses []string `json:"addresses"`
}
type urlNonmutatingAttestation struct {
	Ref          string   `json:"ref"`
	Verified     bool     `json:"verified"`
	PathPrefixes []string `json:"path_prefixes"`
	Methods      []string `json:"methods"`
	QueryPolicy  string   `json:"query_policy"`
	BodyPolicy   string   `json:"body_policy"`
	MaxBodyBytes int64    `json:"max_body_bytes"`
}
type urlPreparationProfile struct {
	WorkflowMode        string                    `json:"workflow_mode,omitempty"`
	ID                  string                    `json:"id"`
	Label               string                    `json:"label"`
	Principal           string                    `json:"principal"`
	TargetRef           string                    `json:"target_ref"`
	ScopeRef            string                    `json:"scope_ref"`
	ModelProfileRef     string                    `json:"model_profile_ref"`
	ModelLabel          string                    `json:"model_label"`
	DelegationRef       string                    `json:"delegation_ref"`
	DeriveRuns          bool                      `json:"derive_runs"`
	ValidFrom           time.Time                 `json:"valid_from"`
	ValidUntil          time.Time                 `json:"valid_until"`
	MaxRuns             int64                     `json:"max_runs"`
	AggregateBudget     diagnosisprep.Budget      `json:"aggregate_budget"`
	Budget              diagnosisprep.Budget      `json:"budget"`
	Launch              urlProtectedPin           `json:"launch"`
	PolicySHA256        string                    `json:"policy_sha256"`
	ManifestSHA256      string                    `json:"manifest_sha256"`
	AllowPrivateTarget  bool                      `json:"allow_private_target"`
	Attestation         urlNonmutatingAttestation `json:"attestation,omitzero"`
	ModelLimitsRef      string                    `json:"model_limits_ref"`
	InputTokensPerCall  int64                     `json:"input_tokens_per_call"`
	OutputTokensPerCall int64                     `json:"output_tokens_per_call"`
}
type urlPreparationInstallation struct {
	Format             string                  `json:"format"`
	OperatorUID        uint32                  `json:"operator_uid"`
	StateDir           string                  `json:"state_dir"`
	RunsDir            string                  `json:"runs_dir"`
	SocketPath         string                  `json:"socket_path"`
	ReportListenAddr   string                  `json:"report_listen_addr"`
	APILaunch          urlProtectedPin         `json:"api_launch"`
	ProtectedEndpoints []urlProtectedEndpoint  `json:"protected_endpoints"`
	Profiles           []urlPreparationProfile `json:"profiles,omitempty"`
	Product            *urlProductInstallation `json:"product,omitempty"`
}
type urlInputPin struct {
	path, digest string
	info         os.FileInfo
	parentInfo   os.FileInfo
	limit        int64
	executable   bool
}
type urlLoadedProfile struct {
	profile         urlPreparationProfile
	launch          launchConfig
	policy          server.E2HTTPFilePolicy
	origin          string
	address         netip.Addr
	pins            []urlInputPin
	materials       map[string][]byte
	inspection      diagnosisprep.Inspection
	heldCheck       func() error
	releaseInputs   func()
	productTarget   *productTargetRecord
	productRegistry *productTargetRegistry
	// productAccounts는 이 실행에 결속한 시험 계정이다(C14). 각각이 지금도 같은 자격으로 test여야 송신을 이어 간다.
	productAccounts []productRunAccount
}

func (*urlLoadedProfile) String() string { return "urlLoadedProfile[protected]" }

func urlSHA(raw []byte) string { d := sha256.Sum256(raw); return hex.EncodeToString(d[:]) }
func urlDigestValid(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == v
}
func urlReadPin(path string, limit int64, executable bool) ([]byte, urlInputPin, error) {
	var pin urlInputPin
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nil, pin, errPreparation
	}
	var raw []byte
	if executable {
		if before.Mode().Perm()&0022 != 0 || before.Mode().Perm()&0111 == 0 {
			return nil, pin, errPreparation
		}
		f, e := os.Open(path)
		if e != nil {
			return nil, pin, errPreparation
		}
		raw, e = io.ReadAll(io.LimitReader(f, limit+1))
		closeErr := f.Close()
		if e != nil || closeErr != nil || int64(len(raw)) > limit {
			clear(raw)
			return nil, pin, errPreparation
		}
	} else {
		raw, err = readPrivateFile(path, limit)
		if err != nil {
			return nil, pin, errPreparation
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() || before.Size() != after.Size() {
		clear(raw)
		return nil, pin, errPreparation
	}
	parentInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parentInfo.IsDir() {
		clear(raw)
		return nil, pin, errPreparation
	}
	return raw, urlInputPin{path: path, digest: urlSHA(raw), info: after, parentInfo: parentInfo, limit: limit, executable: executable}, nil
}
func (p urlInputPin) check() error {
	raw, current, err := urlReadPin(p.path, p.limit, p.executable)
	clear(raw)
	if err != nil || p.digest != current.digest || !os.SameFile(p.info, current.info) || !os.SameFile(p.parentInfo, current.parentInfo) {
		return errPreparation
	}
	return nil
}
func readURLInstallation(path string) (urlPreparationInstallation, urlInputPin, error) {
	var c urlPreparationInstallation
	raw, pin, err := urlReadPin(path, 256<<10, false)
	defer clear(raw)
	if err != nil || decodeLaunchJSON(raw, &c) != nil || c.Format != urlPreparationFormat || urlOperatorUID() != c.OperatorUID ||
		c.OperatorUID == 0 || (c.Product == nil && len(c.Profiles) < 1) || len(c.Profiles) > 64 || len(c.ProtectedEndpoints) < 1 || len(c.ProtectedEndpoints) > 64 {
		return c, pin, errPreparation
	}
	for _, p := range []string{c.StateDir, c.RunsDir, filepath.Dir(c.SocketPath)} {
		if checkDirectory(p, true) != nil {
			return c, pin, errPreparation
		}
	}
	if c.StateDir == c.RunsDir || filepath.Dir(c.SocketPath) == c.RunsDir || !filepath.IsAbs(c.SocketPath) ||
		filepath.Clean(c.SocketPath) != c.SocketPath || !managementReference.MatchString(filepath.Base(c.SocketPath)) {
		return c, pin, errPreparation
	}
	host, port, err := net.SplitHostPort(c.ReportListenAddr)
	n, e := strconv.Atoi(port)
	ip, iperr := netip.ParseAddr(host)
	if err != nil || e != nil || iperr != nil || !ip.IsLoopback() || ip.String() != host || ip.Zone() != "" || ip.Is4In6() || strconv.Itoa(n) != port || n < 1 || n > 65535 {
		return c, pin, errPreparation
	}
	if !urlDigestValid(c.APILaunch.SHA256) {
		return c, pin, errPreparation
	}
	apiRaw, apiPin, apiErr := urlReadPin(c.APILaunch.File, maxLaunchBytes, false)
	defer clear(apiRaw)
	if apiErr != nil || apiPin.digest != c.APILaunch.SHA256 {
		return c, pin, errPreparation
	}
	apiLaunch, apiErr := parseLaunchConfig(apiRaw)
	if apiErr != nil {
		return c, pin, errPreparation
	}
	apiHost, apiPort, _ := net.SplitHostPort(apiLaunch.ListenAddr)
	apiN, _ := strconv.Atoi(apiPort)
	apiIP, apiErr := netip.ParseAddr(apiHost)
	if apiErr != nil || !apiIP.IsLoopback() || apiIP.String() != apiHost || apiIP.Zone() != "" || apiIP.Is4In6() || strconv.Itoa(apiN) != apiPort || apiN < 1 || apiN > 65535 {
		return c, pin, errPreparation
	}
	c.ProtectedEndpoints = append(c.ProtectedEndpoints, urlProtectedEndpoint{Host: apiHost, Port: apiN, Addresses: []string{apiHost}})
	ids := map[string]bool{}
	for _, p := range c.Profiles {
		if ids[p.ID] || !managementReference.MatchString(p.ID) || !plainText(p.Label, 256) || p.Principal != "ARTEX" {
			return c, pin, errPreparation
		}
		ids[p.ID] = true
	}
	for _, endpoint := range c.ProtectedEndpoints {
		if endpoint.Host == "" || strings.ContainsAny(endpoint.Host, "/%\\?#@") || endpoint.Port < 1 || endpoint.Port > 65535 || len(endpoint.Addresses) < 1 || len(endpoint.Addresses) > 32 {
			return c, pin, errPreparation
		}
		for _, value := range endpoint.Addresses {
			ip, e := netip.ParseAddr(value)
			if e != nil || ip.String() != value || ip.Zone() != "" {
				return c, pin, errPreparation
			}
		}
	}
	return c, pin, nil
}
func urlProfilePublic(p urlPreparationProfile) diagnosisprep.Profile {
	return diagnosisprep.Profile{ID: p.ID, Label: p.Label, ModelProfileRef: p.ModelProfileRef, ModelLabel: p.ModelLabel}
}
func urlBudgetWithin(a, b diagnosisprep.Budget) bool {
	return a.Requests > 0 && a.ModelRequests > 0 && a.InputTokens > 0 && a.OutputTokens > 0 && a.DurationSeconds > 0 && a.CostUSDMicros >= 0 &&
		a.Requests <= b.Requests && a.ModelRequests <= b.ModelRequests && a.InputTokens <= b.InputTokens && a.OutputTokens <= b.OutputTokens && a.DurationSeconds <= b.DurationSeconds && a.CostUSDMicros <= b.CostUSDMicros
}
func urlOrigin(raw string) (string, error) {
	in, err := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: raw, ProfileID: "origin"})
	if err != nil {
		return "", errPreparation
	}
	u, _ := url.Parse(in.TargetURL)
	if u.Path != "/" {
		return "", errPreparation
	}
	return u.Scheme + "://" + u.Host, nil
}
func urlPort(u *url.URL) int {
	if u.Port() == "" && u.Scheme == "http" {
		return 80
	}
	if u.Port() == "" {
		return 443
	}
	n, _ := strconv.Atoi(u.Port())
	return n
}
func urlPathWithin(p, prefix string) bool {
	return prefix == "/" || p == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(p, strings.TrimSuffix(prefix, "/")+"/")
}

// 대상 등록과 탐색 경계가 같은 규칙을 쓰도록 targetaddr에 둔다.
func urlTargetIPAllowed(ip netip.Addr, private bool) bool { return targetaddr.Allowed(ip, private) }
func urlProtectedConflict(c urlPreparationInstallation, origin string, address netip.Addr) bool {
	u, _ := url.Parse(origin)
	port := urlPort(u)
	reportHost, reportPort, _ := net.SplitHostPort(c.ReportListenAddr)
	if strconv.Itoa(port) == reportPort && (u.Hostname() == reportHost || address.String() == reportHost) {
		return true
	}
	for _, endpoint := range c.ProtectedEndpoints {
		if endpoint.Port != port {
			continue
		}
		if strings.EqualFold(endpoint.Host, u.Hostname()) {
			return true
		}
		for _, ip := range endpoint.Addresses {
			if ip == address.String() {
				return true
			}
		}
	}
	return false
}

type urlPreparationError string

func (e urlPreparationError) Error() string { return string(e) }

func urlLoadProfile(c urlPreparationInstallation, installationPin urlInputPin, p urlPreparationProfile) (*urlLoadedProfile, error) {
	return urlLoadProfileForReport(c, installationPin, p, false)
}
func urlLoadProfileForReport(c urlPreparationInstallation, installationPin urlInputPin, p urlPreparationProfile, reportOnly bool) (loaded *urlLoadedProfile, resultErr error) {
	stage := "installation_profile_invalid"
	defer func() {
		if resultErr != nil {
			resultErr = urlPreparationError(stage)
		}
	}()
	for _, ref := range []string{p.TargetRef, p.ScopeRef, p.ModelProfileRef, p.DelegationRef, p.Attestation.Ref, p.ModelLimitsRef} {
		if !managementReference.MatchString(ref) {
			return nil, errPreparation
		}
	}
	if !p.DeriveRuns || !p.Attestation.Verified || p.MaxRuns < 1 || p.MaxRuns > 1000 || p.ValidFrom.IsZero() || !p.ValidUntil.After(p.ValidFrom) ||
		!urlBudgetWithin(p.Budget, p.AggregateBudget) || p.Budget.DurationSeconds > urlMaxRunSeconds || !urlDigestValid(p.PolicySHA256) ||
		!urlDigestValid(p.ManifestSHA256) || !urlDigestValid(p.Launch.SHA256) ||
		p.Attestation.QueryPolicy != "any" || p.Attestation.BodyPolicy != "bounded" || p.Attestation.MaxBodyBytes < 1 ||
		p.InputTokensPerCall < 4096 || p.InputTokensPerCall > 4<<20 || p.InputTokensPerCall%1024 != 0 ||
		p.OutputTokensPerCall < 1 || p.OutputTokensPerCall > p.InputTokensPerCall || p.InputTokensPerCall > p.Budget.InputTokens || p.OutputTokensPerCall > p.Budget.OutputTokens {
		return nil, errPreparation
	}
	stage = "installation_launch_invalid"
	launchRaw, launchPin, err := urlReadPin(p.Launch.File, maxLaunchBytes, false)
	defer clear(launchRaw)
	if err != nil || launchPin.digest != p.Launch.SHA256 {
		return nil, errPreparation
	}
	launch, err := parseLaunchConfig(launchRaw)
	if err != nil || launch.Mode != "general_http" {
		return nil, errPreparation
	}
	l := &urlLoadedProfile{profile: p, launch: launch, pins: []urlInputPin{installationPin, launchPin}, materials: map[string][]byte{}}
	if launch.ModelExecutionMode != "openrouter" && p.Budget.CostUSDMicros != 0 {
		return nil, errPreparation
	}
	if launch.ModelFormat != "openai" {
		return nil, errPreparation
	}
	stage = "installation_policy_invalid"
	policyRaw, policyPin, err := urlReadPin(launch.HTTP.PolicyFile, e2config.MaxPolicyBytes, false)
	defer clear(policyRaw)
	if err != nil || policyPin.digest != p.PolicySHA256 || e2config.StrictJSON(policyRaw, &l.policy) != nil {
		return nil, errPreparation
	}
	apiRaw, apiPin, apiErr := urlReadPin(c.APILaunch.File, maxLaunchBytes, false)
	defer clear(apiRaw)
	if apiErr != nil || apiPin.digest != c.APILaunch.SHA256 {
		return nil, errPreparation
	}
	l.pins = append(l.pins, policyPin, apiPin)
	policy := l.policy
	if policy.Format != server.E2HTTPFilePolicyFormat || policy.Evaluation != nil || len(policy.Rules) < 1 || len(policy.Rules) > 64 ||
		!(policy.HeaderMode == e2proxy.HeaderPreserve || policy.HeaderMode == e2proxy.HeaderTestAccountPassthrough && policy.Auth != nil) || policy.OwnerRef != "ARTEX" || policy.MaxRequests != uint64(p.Budget.Requests) ||
		policy.Limits.MaxBodyBytes != p.Attestation.MaxBodyBytes || policy.Limits.MaxConcurrent != 1 ||
		(policy.ApprovalMode != e2proxy.ApprovalPreauthorizedScope && policy.ApprovalMode != e2proxy.ApprovalConfirmEveryRequest && !(p.WorkflowMode != "" && policy.ApprovalMode == e2proxy.ApprovalWorkbenchV1)) ||
		policy.ValidFrom.After(p.ValidFrom) || policy.ValidUntil.Before(p.ValidUntil) {
		return nil, errPreparation
	}
	stage = "installation_target_invalid"
	paths, methods := []string{}, []string{}
	seenMethods := map[string]bool{}
	for _, rule := range policy.Rules {
		origin, e := urlOrigin(rule.Origin)
		ip, ie := netip.ParseAddr(rule.ConnectIP)
		if e != nil || ie != nil || !urlTargetIPAllowed(ip, p.AllowPrivateTarget) || rule.Credentials != nil || urlProtectedConflict(c, origin, ip) {
			return nil, errPreparation
		}
		if l.origin == "" {
			l.origin, l.address = origin, ip
		} else if l.origin != origin || l.address != ip {
			return nil, errPreparation
		}
		normalized, e := diagnosisprep.CanonicalInput(diagnosisprep.Input{TargetURL: origin + rule.PathPrefix, ProfileID: p.ID})
		if e != nil || normalized.TargetURL != origin+rule.PathPrefix {
			return nil, errPreparation
		}
		// 인증 준비 규칙(C14)은 진단 범위(증명한 읽기 경로·메서드) 비교에서 빼고 따로 본다: auth_prep·POST 하나·인증 칸이
		// 가리키는 규칙·증명한 경로 접두 안. 모델 요청은 이 규칙을 쓰지 못한다(E② Purpose).
		if rule.Purpose != "" {
			if !urlAuthRuleValid(policy, rule, p.Attestation.PathPrefixes) {
				return nil, errPreparation
			}
			continue
		}
		paths = append(paths, rule.PathPrefix)
		for _, method := range rule.Methods {
			if method != "GET" && method != "HEAD" && !(p.WorkflowMode != "" && (method == "POST" || method == "PUT" || method == "PATCH")) {
				return nil, errPreparation
			}
			if !seenMethods[method] {
				seenMethods[method] = true
				methods = append(methods, method)
			}
		}
	}
	sort.Strings(paths)
	sort.Strings(methods)
	ap := append([]string(nil), p.Attestation.PathPrefixes...)
	am := append([]string(nil), p.Attestation.Methods...)
	sort.Strings(ap)
	sort.Strings(am)
	if !reflect.DeepEqual(paths, ap) || !reflect.DeepEqual(methods, am) || len(methods) == 0 {
		return nil, errPreparation
	}
	stage = "installation_model_invalid"
	manifestPath := launch.LocalLLMManifestPath
	if launch.ModelExecutionMode == "openrouter" {
		manifestPath = launch.OpenRouterManifestPath
	}
	manifest, manifestPin, err := urlReadPin(manifestPath, llmpolicy.MaxProfileManifestBytes, false)
	defer clear(manifest)
	if err != nil || manifestPin.digest != p.ManifestSHA256 {
		return nil, errPreparation
	}
	l.pins = append(l.pins, manifestPin)
	if launch.ModelExecutionMode == "openrouter" {
		targetModel, m, quote, err := llmpolicy.DescribeOpenRouterManifest(manifest, p.OutputTokensPerCall)
		if err != nil || targetModel != (llmpolicy.ProfileTarget{ID: launch.ModelProfileID, Format: launch.ModelFormat, BaseURL: launch.ModelBaseURL, Model: launch.ModelRef}) {
			return nil, errPreparation
		}
		if !reportOnly {
			external, err := llmpolicy.LoadOpenRouterManifestFile(manifestPath)
			if err != nil {
				return nil, errPreparation
			}
			if _, err = external.Resolve(targetModel); err != nil || external.CheckAvailable() != nil {
				return nil, errPreparation
			}
		}
		if err != nil || p.Budget.CostUSDMicros < quote || p.Budget.CostUSDMicros > m.CostMicros || p.Budget.ModelRequests > m.Requests || p.Budget.InputTokens > m.InputTokens || p.Budget.OutputTokens > m.OutputTokens || p.InputTokensPerCall != m.InputPerCall || p.OutputTokensPerCall > m.OutputPerCall {
			return nil, errPreparation
		}
		// Model API permission never grants permission to scan the model service.
		target, _ := url.Parse(l.origin)
		if strings.EqualFold(target.Hostname(), "openrouter.ai") {
			return nil, errPreparation
		}
		l.inspection.Scope = &diagnosisprep.Scope{ExpiresAt: m.ExpiresAt, Model: &diagnosisprep.ModelInfo{Mode: "openrouter", Model: launch.ModelRef, Provider: m.Provider, ExternalTransfer: true, InputPerCall: p.InputTokensPerCall, OutputPerCall: p.OutputTokensPerCall}}
	} else {
		resolver, err := llmpolicy.ParseProfileManifest(manifest)
		if err != nil {
			return nil, errPreparation
		}
		if _, err = resolver.Resolve(llmpolicy.ProfileTarget{ID: launch.ModelProfileID, Format: launch.ModelFormat, BaseURL: launch.ModelBaseURL, Model: launch.ModelRef}); err != nil {
			return nil, errPreparation
		}
		// 모델 origin·pin과 DB endpoint는 설치자 목록 누락 여부도 대조한다.
		var modelDoc struct {
			Profiles []struct {
				ProfileID int64    `json:"profile_id"`
				BaseURL   string   `json:"base_url"`
				Addresses []string `json:"addresses"`
			} `json:"profiles,omitempty"`
		}
		if json.Unmarshal(manifest, &modelDoc) != nil {
			return nil, errPreparation
		}
		target, _ := url.Parse(l.origin)
		for _, model := range modelDoc.Profiles {
			mu, e := url.Parse(model.BaseURL)
			if e != nil {
				return nil, errPreparation
			}
			mp := mu.Port()
			if mp == "" {
				if mu.Scheme == "https" {
					mp = "443"
				} else {
					mp = "80"
				}
			}
			if strconv.Itoa(urlPort(target)) == mp {
				if target.Hostname() == mu.Hostname() {
					return nil, errPreparation
				}
				for _, ip := range model.Addresses {
					if ip == l.address.String() {
						return nil, errPreparation
					}
				}
			}
		}
	}
	stage = "db_endpoint_unpinned"
	dsn, dsnPin, err := urlReadPin(launch.RuntimeDSNFile, 16<<10, false)
	defer clear(dsn)
	if err != nil {
		return nil, errPreparation
	}
	conn, err := urlParseDatabase(string(bytes.TrimSpace(dsn)))
	if err != nil {
		return nil, errPreparation
	}
	if !urlDatabaseProtected(c, conn) {
		return nil, errPreparation
	}
	l.pins = append(l.pins, dsnPin)
	apiLaunch, err := parseLaunchConfig(apiRaw)
	if err != nil {
		return nil, errPreparation
	}
	apiDSN, apiDSNPin, err := urlReadPin(apiLaunch.RuntimeDSNFile, 16<<10, false)
	defer clear(apiDSN)
	if err != nil {
		return nil, errPreparation
	}
	apiConn, err := urlParseDatabase(string(bytes.TrimSpace(apiDSN)))
	if err != nil || !urlDatabaseProtected(c, apiConn) {
		return nil, errPreparation
	}
	if apiDSNPin.path != dsnPin.path {
		l.pins = append(l.pins, apiDSNPin)
	}
	stage = "installation_material_invalid"
	for _, entry := range []struct {
		pin   server.E2HTTPFilePin
		limit int64
	}{{policy.CACert, 1 << 20}, {policy.CAKey, 1 << 20}, {policy.AuditKey, 32}} {
		if !managementReference.MatchString(entry.pin.File) || !urlDigestValid(entry.pin.SHA256) {
			return nil, errPreparation
		}
		raw, pin, e := urlReadPin(filepath.Join(filepath.Dir(launch.HTTP.PolicyFile), entry.pin.File), entry.limit, false)
		if e != nil || pin.digest != entry.pin.SHA256 {
			clear(raw)
			return nil, errPreparation
		}
		if _, duplicate := l.materials[entry.pin.File]; duplicate {
			clear(raw)
			return nil, errPreparation
		}
		l.materials[entry.pin.File] = raw
		l.pins = append(l.pins, pin)
	}
	if policy.UpstreamCA != nil {
		pin := *policy.UpstreamCA
		if !managementReference.MatchString(pin.File) || !urlDigestValid(pin.SHA256) {
			return nil, errPreparation
		}
		raw, held, e := urlReadPin(filepath.Join(filepath.Dir(launch.HTTP.PolicyFile), pin.File), 1<<20, false)
		if e != nil || held.digest != pin.SHA256 {
			return nil, errPreparation
		}
		if _, duplicate := l.materials[pin.File]; duplicate {
			clear(raw)
			return nil, errPreparation
		}
		l.materials[pin.File] = raw
		l.pins = append(l.pins, held)
	}
	stage = "installation_helper_invalid"
	helper, helperPin, err := urlReadPin(policy.Helper, 64<<20, true)
	clear(helper)
	if err != nil || helperPin.digest != policy.HelperSHA256 {
		return nil, errPreparation
	}
	l.pins = append(l.pins, helperPin)
	refs := []string{}
	for _, pin := range l.pins {
		refs = append(refs, pin.path+"\x00"+pin.digest)
	}
	raw, _ := json.Marshal(refs)
	expires := p.ValidUntil.UTC()
	var model *diagnosisprep.ModelInfo
	if l.inspection.Scope != nil {
		model = l.inspection.Scope.Model
		if l.inspection.Scope.ExpiresAt.Before(expires) {
			expires = l.inspection.Scope.ExpiresAt
		}
	}
	l.inspection = diagnosisprep.Inspection{Scope: &diagnosisprep.Scope{Model: model, TargetRef: p.TargetRef, ScopeRef: p.ScopeRef, Origin: l.origin, PathPrefixes: paths, Methods: methods, QueryPolicy: "any", BodyPolicy: "bounded", MaxBodyBytes: policy.Limits.MaxBodyBytes, ApprovalMode: string(policy.ApprovalMode), AttestationRef: p.Attestation.Ref, Budget: p.Budget, ExpiresAt: expires}, InstallationDigest: urlSHA(raw), DelegationRef: p.DelegationRef, MaxRuns: p.MaxRuns, AggregateBudget: p.AggregateBudget}
	return l, nil
}
func (l *urlLoadedProfile) clearMaterials() {
	for _, raw := range l.materials {
		clear(raw)
	}
}
func (l *urlLoadedProfile) current() error {
	now := time.Now()
	targetExpiry := l.productTarget != nil && !l.profile.ValidUntil.Before(l.productTarget.Target.ExpiresAt)
	if now.Before(l.profile.ValidFrom) || !now.Before(l.profile.ValidUntil) && !targetExpiry {
		return errPreparation
	}
	if l.heldCheck != nil {
		if l.heldCheck() != nil {
			return errPreparation
		}
	} else {
		for _, pin := range l.pins {
			if pin.check() != nil {
				return errPreparation
			}
		}
	}
	if l.productTarget != nil {
		if err := l.productRegistry.current(*l.productTarget); err != nil {
			return err
		}
		return l.productRegistry.accountsCurrent(*l.productTarget, l.productAccounts)
	}
	return nil
}

func (l *urlLoadedProfile) dnsCheck(ctx context.Context, resolver *net.Resolver) error {
	if l.productTarget != nil {
		return l.productRegistry.recheck(ctx, *l.productTarget)
	}
	u, _ := url.Parse(l.origin)
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if ip != l.address {
			return errPreparation
		}
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupNetIP(bounded, "ip", u.Hostname())
	if err != nil || len(ips) == 0 || len(ips) > 32 {
		return errPreparation
	}
	// DNS 변경은 pin 갱신의 권위가 아니다. 하나라도 승인 pin과 다르면 회차를 시작하지 않는다.
	for _, ip := range ips {
		if ip.Unmap() != l.address {
			return errPreparation
		}
	}
	return nil
}

// 모든 pgx fallback도 실제 연결 가능한 제어 endpoint이므로 빠짐없이 보호한다.
func urlDatabaseProtected(c urlPreparationInstallation, conn *pgx.ConnConfig) bool {
	if conn == nil {
		return false
	}
	allowed := func(host string, port uint16) bool {
		if strings.HasPrefix(host, "/") {
			return filepath.IsAbs(host) && filepath.Clean(host) == host
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || ip.String() != host || ip.Zone() != "" || ip.Is4In6() {
			return false
		}
		for _, endpoint := range c.ProtectedEndpoints {
			if endpoint.Port != int(port) {
				continue
			}
			for _, address := range endpoint.Addresses {
				if address == host {
					return true
				}
			}
		}
		return false
	}
	if !allowed(conn.Host, conn.Port) {
		return false
	}
	for _, fallback := range conn.Fallbacks {
		if fallback == nil || !allowed(fallback.Host, fallback.Port) {
			return false
		}
	}
	return true
}

func urlAuthRuleValid(policy server.E2HTTPFilePolicy, rule server.E2HTTPFileRule, prefixes []string) bool {
	if rule.Purpose != e2proxy.PurposeAuthPrep || len(rule.Methods) != 1 || rule.Methods[0] != "POST" || policy.Auth == nil || policy.Auth.RuleRef != rule.Ref || policy.Auth.Login.Path != rule.PathPrefix {
		return false
	}
	for _, prefix := range prefixes {
		if prefix == "/" || rule.PathPrefix == prefix || strings.HasPrefix(rule.PathPrefix, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}
