package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/pathscope"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
)

type urlProductInstallation struct {
	Format       string                  `json:"format"`
	TargetPolicy urlProtectedPin         `json:"target_policy"`
	Models       []urlPreparationProfile `json:"models"`
}
type productRuntime struct {
	flowMu          sync.RWMutex
	flows           map[string]*diagnosisflow.Controller
	config          urlPreparationInstallation
	installationPin urlInputPin
	targets         *productTargetRegistry
	// exploration은 설치 정책의 탐색 블록으로 만든 host다. 없으면 탐색은 NOT_RUN이다.
	exploration *productExplorationHost
	bindMu      sync.Mutex
	// consentMu는 외부 전송 동의 기록 쓰기를 한 번에 하나로 묶는다(파일 link가 먼저 남긴 동의를 지키지만 오류를 줄인다).
	consentMu sync.Mutex
	activeMu  sync.Mutex
	active    map[string]map[string]context.CancelFunc
}

func productModels(c urlPreparationInstallation) []urlPreparationProfile {
	if c.Product != nil {
		return c.Product.Models
	}
	return c.Profiles
}
func newProductRuntime(c urlPreparationInstallation, pin urlInputPin) (*productRuntime, error) {
	if c.Product == nil || c.Product.Format != "abc2lab.product-runtime/v2" || len(c.Product.Models) < 1 || len(c.Product.Models) > 8 {
		return nil, errPreparation
	}
	raw, policyPin, e := urlReadPin(c.Product.TargetPolicy.File, 16<<10, false)
	defer clear(raw)
	var policy productTargetPolicy
	if e != nil || policyPin.digest != c.Product.TargetPolicy.SHA256 || decodeLaunchJSON(raw, &policy) != nil {
		return nil, errPreparation
	}
	pins := []urlInputPin{pin, policyPin}
	if policy.UpstreamCAFile != "" {
		b, cp, e := urlReadPin(policy.UpstreamCAFile, 1<<20, false)
		clear(b)
		if e != nil || cp.digest != policy.UpstreamCASHA256 {
			return nil, errPreparation
		}
		pins = append(pins, cp)
	}
	var explorationHost *productExplorationHost
	if policy.Exploration != nil {
		ca, cp, e := urlReadPin(policy.Exploration.BoundaryCAFile, 64<<10, false)
		if e != nil || cp.digest != policy.Exploration.BoundaryCASHA256 {
			clear(ca)
			return nil, errPreparation
		}
		explorationHost, e = newProductExplorationHost(*policy.Exploration, ca)
		clear(ca)
		if e != nil {
			return nil, errPreparation
		}
		pins = append(pins, cp)
		// 재시작 정리: 열린 탐색 MCP 세션을 기록하고, 시작할 때 이전 실행이 남긴 경계 세션·브라우저 문맥을 정리한다.
		if explorationHost.wireCleanup(context.Background(), c.StateDir) != nil {
			return nil, errPreparation
		}
	}
	check := func() error {
		for _, p := range pins {
			if p.check() != nil {
				return errPreparation
			}
		}
		return nil
	}
	dir, e := productTargetDirectory(c.StateDir)
	if e != nil {
		return nil, e
	}
	targets, e := newProductTargetRegistry(dir, policy, check)
	if e != nil {
		return nil, e
	}
	targets.endpointAllowed = func(origin string, ip netip.Addr) bool { return !urlProtectedConflict(c, origin, ip) }
	p := &productRuntime{config: c, installationPin: pin, targets: targets, exploration: explorationHost, flows: map[string]*diagnosisflow.Controller{}, active: map[string]map[string]context.CancelFunc{}}
	if _, e = p.profiles(); e != nil {
		_ = targets.Close()
		return nil, e
	}
	return p, nil
}
func (p *productRuntime) model(id string) (urlPreparationProfile, launchConfig, server.E2HTTPFilePolicy, []urlInputPin, error) {
	var cfg launchConfig
	var policy server.E2HTTPFilePolicy
	var m urlPreparationProfile
	found := false
	for _, v := range p.config.Product.Models {
		if v.ID == id {
			m = v
			found = true
			break
		}
	}
	if !found || m.Principal != "ARTEX" || m.TargetRef != "" || m.ScopeRef != "" || m.Attestation.Verified || !m.DeriveRuns || m.MaxRuns < 1 || !urlBudgetWithin(m.Budget, m.AggregateBudget) {
		return m, cfg, policy, nil, errPreparation
	}
	raw, lp, e := urlReadPin(m.Launch.File, maxLaunchBytes, false)
	defer clear(raw)
	if e != nil || lp.digest != m.Launch.SHA256 {
		return m, cfg, policy, nil, errPreparation
	}
	cfg, e = parseLaunchConfig(raw)
	if e != nil || cfg.Mode != "general_http" || cfg.HTTP == nil {
		return m, cfg, policy, nil, errPreparation
	}
	b, pp, e := urlReadPin(cfg.HTTP.PolicyFile, e2config.MaxPolicyBytes, false)
	defer clear(b)
	if e != nil || pp.digest != m.PolicySHA256 || e2config.StrictJSON(b, &policy) != nil || len(policy.Rules) != 0 || policy.Format != server.E2HTTPFilePolicyFormat || policy.OwnerRef != "ARTEX" || !validProductPolicyTemplate(policy) {
		return m, cfg, policy, nil, errPreparation
	}
	manifestPath := cfg.LocalLLMManifestPath
	if cfg.ModelExecutionMode == "openrouter" {
		manifestPath = cfg.OpenRouterManifestPath
	}
	manifest, mp, e := urlReadPin(manifestPath, 64<<10, false)
	defer clear(manifest)
	if e != nil || mp.digest != m.ManifestSHA256 {
		return m, cfg, policy, nil, errPreparation
	}
	if cfg.ModelExecutionMode == "openrouter" {
		target, _, _, e := llmpolicy.DescribeOpenRouterManifest(manifest, m.OutputTokensPerCall)
		if e != nil {
			return m, cfg, policy, nil, errPreparation
		}
		if cfg.ModelRef != llmpolicy.OpenRouterModel || cfg.ModelBaseURL != llmpolicy.OpenRouterBaseURL || target.ID != cfg.ModelProfileID {
			return m, cfg, policy, nil, errPreparation
		}
	} else {
		resolver, e := llmpolicy.ParseProfileManifest(manifest)
		if e != nil {
			return m, cfg, policy, nil, errPreparation
		}
		if _, e = resolver.Resolve(llmpolicy.ProfileTarget{ID: cfg.ModelProfileID, Format: cfg.ModelFormat, BaseURL: cfg.ModelBaseURL, Model: cfg.ModelRef}); e != nil {
			return m, cfg, policy, nil, errPreparation
		}
	}
	return m, cfg, policy, []urlInputPin{p.installationPin, lp, pp, mp}, nil
}
func (p *productRuntime) profiles() ([]diagnosisprep.Profile, error) {
	out := []diagnosisprep.Profile{}
	for _, base := range p.config.Product.Models {
		m, cfg, _, _, e := p.model(base.ID)
		if e != nil {
			return nil, e
		}
		v := urlProfilePublic(m)
		v.Workflow = p.targets.policy.Workflow
		// 화면은 이 값으로 대상을 등록한다(회차당 요청 상한). 워크플로 여부와 상관없이 싣는다. 예전에는 워크플로 모드만
		// 실어서 일반 HTTP 모드 화면이 고정값 4로 등록했다.
		v.WorkflowRequestLimit = p.targets.policy.MaxRequests
		if cfg.ModelExecutionMode == "openrouter" {
			raw, e := readPrivateFile(cfg.OpenRouterManifestPath, 64<<10)
			if e != nil {
				return nil, e
			}
			_, meta, _, e := llmpolicy.DescribeOpenRouterManifest(raw, m.OutputTokensPerCall)
			clear(raw)
			if e != nil {
				return nil, e
			}
			v.Model = &diagnosisprep.ModelInfo{Mode: "openrouter", Model: cfg.ModelRef, Provider: meta.Provider, ExternalTransfer: true, InputPerCall: m.InputTokensPerCall, OutputPerCall: m.OutputTokensPerCall}
		}
		out = append(out, v)
	}
	return out, nil
}

// load는 실행(또는 저장 보고서)을 위한 결속을 읽는다. 저장 보고서는 현재 송신 권한·계정 선택과 상관없이 그 실행이 쓴
// 결속을 다시 찾아야 하므로, sourceDigest(작업의 원본 요약)를 주면 계정 없는 결속부터 같은 기반 이름의 계정 결속까지
// 차례로 보고 원본 요약이 맞는 것을 돌려준다(C14).
func (p *productRuntime) load(principal string, input diagnosisprep.Input, reportOnly bool, reportID string, sourceDigest ...string) (*urlLoadedProfile, error) {
	if !reportOnly {
		return p.loadBinding(principal, input, false, reportID, "")
	}
	loaded, err := p.loadBinding(principal, input, true, reportID, "")
	if len(sourceDigest) == 0 {
		return loaded, err
	}
	if err == nil && diagnosisprep.SourceDigest(input, loaded.inspection.InstallationDigest) == sourceDigest[0] {
		return loaded, nil
	}
	if loaded != nil {
		loaded.clearMaterials()
	}
	for _, suffix := range p.accountBindingSuffixes(principal, input, reportID) {
		candidate, e := p.loadBinding(principal, input, true, reportID, suffix)
		if e == nil && diagnosisprep.SourceDigest(input, candidate.inspection.InstallationDigest) == sourceDigest[0] {
			return candidate, nil
		}
		if candidate != nil {
			candidate.clearMaterials()
		}
	}
	if err == nil {
		err = errPreparation
	}
	return nil, err
}

// bindingBase는 결속 디렉터리 이름의 기반이다(대상 기록·모델·워크플로 입력). 계정을 결속한 실행은 그 뒤에 계정 요약
// 16자를 붙인다(bound-<기반>-<계정>). 계정 없는 결속 이름은 이 변경 전과 같다.
func bindingBase(rec productTargetRecord, m urlPreparationProfile, input diagnosisprep.Input) string {
	source := productTargetDigest(rec) + "\n" + m.ID + "\n" + m.Launch.SHA256
	if input.WorkflowMode != "" {
		source += "\n" + workflowInputDigest(input)
	}
	// 제외 경로가 있는 워크플로 결속은 E② 정책에 제외 경로 칸이 생겨(#627) 이전 결속 디렉터리의 정책과 다르다. 이름을
	// 바꿔 새로 만든다(이전 이름을 다시 쓰면 정책 대조가 실패해 실행이 시작되지 않음). 제외 경로 없는 결속 이름은 그대로다.
	if len(inputExcludedPaths(input)) > 0 {
		source += "\nexcluded-e2-v1"
	}
	return "bound-" + urlSHA([]byte(source))[:32]
}

// accountBindingSuffixes는 저장 보고서가 찾을 수 있는 같은 기반 이름의 계정 결속 접미사들이다.
func (p *productRuntime) accountBindingSuffixes(principal string, input diagnosisprep.Input, reportID string) []string {
	rec, e := p.targets.lookup(principal, input.TargetURL, reportID)
	if e != nil {
		return nil
	}
	m, _, _, _, e := p.model(input.ProfileID)
	if e != nil {
		return nil
	}
	base := bindingBase(rec, m, input) + "-"
	root := filepath.Join(p.config.StateDir, "bindings")
	var out []string
	for _, dir := range []string{root, filepath.Join(root, "ordinary")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if suffix, ok := strings.CutPrefix(entry.Name(), base); ok && entry.IsDir() && len(suffix) == 16 && urlHex(suffix) {
				out = append(out, suffix)
			}
		}
	}
	sort.Strings(out)
	return out
}

func urlHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}

// accountsFromBinding은 저장 보고서용으로 이미 있는 계정 결속의 정책에서 결속 계정을 다시 만든다. 다시 계산한 계정 요약이
// 디렉터리 접미사와 같아야 한다.
func accountsFromBinding(dir, suffix string) (productRunAccounts, string, error) {
	raw, err := readPrivateFile(filepath.Join(dir, "policy.json"), e2config.MaxPolicyBytes)
	if err != nil {
		return productRunAccounts{}, "", err
	}
	// 정책 파일에는 null 칸(evaluation 등)이 있어 일반 해독으로 읽는다. 다시 만든 정책이 이 파일과 바이트로 같은지는 결속
	// 읽기가 따로 확인한다.
	var stored server.E2HTTPFilePolicy
	decodeErr := json.Unmarshal(raw, &stored)
	clear(raw)
	if decodeErr != nil || stored.Auth == nil {
		return productRunAccounts{}, "", errPreparation
	}
	login := stored.Auth.Login
	out := productRunAccounts{Login: &login, PasswordResponse: stored.Auth.PasswordResponse}
	for _, a := range stored.Auth.Accounts {
		out.Accounts = append(out.Accounts, productRunAccount{Label: a.Label, CredentialSHA256: a.Credential.SHA256})
	}
	digest, err := runAccountsDigest(out)
	if err != nil || digest[:16] != suffix {
		return productRunAccounts{}, "", errPreparation
	}
	return out, digest, nil
}

func (p *productRuntime) loadBinding(principal string, input diagnosisprep.Input, reportOnly bool, reportID, reportAccounts string) (*urlLoadedProfile, error) {
	if reportOnly && reportID == "" {
		return nil, errPreparation
	}
	if input.WorkflowMode != "" {
		u, e := url.Parse(input.TargetURL)
		if p.targets.policy.Workflow == nil || !diagnosisflow.ValidMode(input.WorkflowMode) || e != nil || len(u.Path) > 128 {
			return nil, urlPreparationError("workflow_unavailable")
		}
	}
	rec, e := p.targets.lookup(principal, input.TargetURL, reportID)
	if e != nil {
		return nil, urlPreparationError("target_not_approved")
	}
	m, cfg, policy, basePins, e := p.model(input.ProfileID)
	if e != nil {
		return nil, e
	}
	// 인증 준비(C14): 실행은 지금 test이고 자격·로그인 방식이 있는 계정을 고른다. 고른 계정·자격 해시의 요약은 결속 이름에
	// 붙어 바뀌면 새 결속 디렉터리가 된다. 저장 보고서는 현재 계정 선택을 보지 않고(철회·재승인 뒤에도 읽힘), 계정 결속을
	// 찾을 때는 그 결속의 정책에서 계정을 다시 만든다.
	var accounts productRunAccounts
	accountDigest := ""
	// 외부 모델 실행은 설치 단위 외부 전송 동의가 있어야 한다(사용자 결정 2026-11-26). 없으면 검토 사유로 막는다 — 회차마다
	// 묻지 않는다. 저장 보고서 읽기(reportOnly)는 이미 끝난 회차라 동의와 상관없이 읽는다. 동의가 있으면 로컬 모델과 같이
	// 시험 계정을 결속한다(대상 응답 · 계정 자격 · 세션이 제공자로 가는 것을 그 동의 문장이 다룬다).
	external := cfg.ModelExecutionMode == "openrouter"
	if !reportOnly && external && !p.externalTransferConsented() {
		return nil, urlPreparationError("external_transfer_consent_required")
	}
	if !reportOnly {
		if accounts, accountDigest, e = p.targets.runAccounts(rec); e != nil {
			return nil, e
		}
	}
	p.bindMu.Lock()
	defer p.bindMu.Unlock()
	root := filepath.Join(p.config.StateDir, "bindings")
	if e = os.Mkdir(root, 0700); e != nil && !os.IsExist(e) {
		return nil, errPreparation
	}
	if checkDirectory(root, true) != nil {
		return nil, errPreparation
	}
	name := bindingBase(rec, m, input)
	if reportAccounts != "" {
		name += "-" + reportAccounts
	} else if accountDigest != "" {
		name += "-" + accountDigest[:16]
	}
	dir := filepath.Join(root, name)
	if reportAccounts != "" {
		// 저장 보고서의 계정 결속은 이미 있어야 한다(새로 만들지 않음).
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			dir = filepath.Join(root, "ordinary", name)
		}
		if accounts, accountDigest, e = accountsFromBinding(dir, reportAccounts); e != nil {
			return nil, e
		}
	}
	// Keep historical immutable bindings. A full workflow cache must not consume
	// the independent capacity reserved for ordinary preauthorized rounds.
	if input.WorkflowMode == "" {
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			entries, readErr := os.ReadDir(root)
			if readErr != nil {
				return nil, errPreparation
			}
			if len(entries) >= 512 {
				root = filepath.Join(root, "ordinary")
				if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
					return nil, errPreparation
				}
				if checkDirectory(root, true) != nil {
					return nil, errPreparation
				}
				dir = filepath.Join(root, name)
			}
		}
	}
	derived := m
	derived.TargetRef = rec.Target.ID
	derived.ScopeRef = "scope-" + rec.Target.ID
	derived.Attestation = urlNonmutatingAttestation{Ref: "proof-" + rec.Target.ID, Verified: true, PathPrefixes: rec.Target.PathPrefixes, Methods: rec.Target.Methods, QueryPolicy: "any", BodyPolicy: "bounded", MaxBodyBytes: policy.Limits.MaxBodyBytes}
	derived.ValidFrom = rec.Target.ApprovedAt
	if derived.ValidUntil.After(rec.Target.ExpiresAt) {
		derived.ValidUntil = rec.Target.ExpiresAt
	}
	// This flag validates the approval-time profile and historical report inputs.
	// C35 authorizes each execution IP through addressAllowed/checkDestinations.
	derived.AllowPrivateTarget = netip.MustParseAddr(rec.SelectedIP).IsPrivate()
	derived.Budget.Requests = rec.Target.Requests
	policy.ValidFrom = derived.ValidFrom
	policy.ValidUntil = derived.ValidUntil
	policy.MaxRequests = uint64(rec.Target.Requests)
	policy.PolicyRef = derived.ScopeRef
	if input.WorkflowMode != "" {
		derived.WorkflowMode = input.WorkflowMode
		// 운영자 제외 경로는 진단 흐름 경계와 E²가 같은 목록을 따로 집행한다(#627).
		policy.Workflow = &server.E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: input.WorkflowMode, InputSHA256: workflowInputDigest(input), ExcludedPaths: inputExcludedPaths(input)}
		if input.WorkflowMode == diagnosisflow.Manual {
			policy.ApprovalMode = e2proxy.ApprovalWorkbenchV1
		}
	}

	// 자동 모드도 대상에 승인한 메서드를 그대로 쓴다(C21). 쓰기 메서드는 설치 정책과 대상 등록이 되돌릴 수 있는 쓰기를
	// 함께 승인했을 때만 대상 메서드에 들어 있다. 진단 흐름이 없는 일반 모드는 위험 요청·제외 경로 검사(진단 흐름
	// controller)를 거치지 않으므로 읽기만 둔다. 자동 모드는 "자동 모드는 건별 확인 없이 보냄"에 동의한 등록만 쓰기를
	// 쓴다(WriteConsent, 이전 등록은 "요청마다 확인"에 동의).
	methods := append([]string(nil), rec.Target.Methods...)
	if input.WorkflowMode == "" || input.WorkflowMode == diagnosisflow.Auto && !rec.autoWritesAllowed() {
		methods = nil
		for _, method := range rec.Target.Methods {
			if method == "GET" || method == "HEAD" {
				methods = append(methods, method)
			}
		}
	}
	derived.Attestation.Methods = append([]string(nil), methods...)
	policy.Rules = nil
	for i, path := range rec.Target.PathPrefixes {
		scope, ok := pathscope.Parse(path)
		if !ok {
			return nil, errPreparation
		}
		policy.Rules = append(policy.Rules, server.E2HTTPFileRule{Ref: "target-path-" + string(rune('a'+i)), Origin: rec.Target.Origin, ConnectIP: rec.SelectedIP, PathPrefix: scope.Path, PathMatch: string(scope.Mode), PathScopeRef: "readonly-path-" + string(rune('a'+i)), Methods: methods, ApprovalDisplay: server.RequestApprovalDisplay{PublicPath: input.WorkflowMode == diagnosisflow.Manual, QueryKeys: []string{}, JSONFields: []string{}}, TLSSPKISHA256: rec.Target.TLSSPKISHA256})
	}
	// 로그인 시도 판정은 계정 등록 여부와 상관없이 제품 회차마다 한다(같은 아이디 연속 실패 회차당 3번, 절대규칙 1·3). 계정이
	// 없는 회차도 등록 밖 아이디(기본 계정 시험 등)의 로그인을 센다.
	policy.LoginAttempts = &server.E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: productLoginConsecutiveFailures, MaxContrastProbes: productLoginContrastProbes, TokenMinBytes: productLoginTokenMinBytes}
	if len(accounts.Accounts) > 0 {
		// 로그인 경로 하나만 정확히 가리키는 인증 준비 규칙. 모델 요청은 이 규칙을 쓰지 못한다(E② Purpose).
		policy.Rules = append(policy.Rules, server.E2HTTPFileRule{Ref: "auth-login", Origin: rec.Target.Origin, ConnectIP: rec.SelectedIP, PathPrefix: accounts.Login.Path, PathScopeRef: "auth-login-path",
			Methods: []string{accounts.Login.Method}, Purpose: e2proxy.PurposeAuthPrep, ApprovalDisplay: server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}, TLSSPKISHA256: rec.Target.TLSSPKISHA256})
		// 모델이 직접 로그인한다(사용자 결정 2026-11-26). 확정 단계 다시 로그인만 프로그램이 지금 로그인 방식으로 한다.
		auth := &server.E2HTTPFileAuth{RuleRef: "auth-login", Login: *accounts.Login, PasswordResponse: accounts.PasswordResponse, ModelLogin: true}
		for _, a := range accounts.Accounts {
			auth.Accounts = append(auth.Accounts, server.E2HTTPFileAuthAccount{Label: a.Label, Credential: server.E2HTTPFilePin{File: bindingCredentialName(rec.Target.ID, a.Label, a.CredentialSHA256), SHA256: a.CredentialSHA256}})
		}
		policy.Auth = auth
		// 결속한 시험 계정이 있으면 모델이 쓴 Authorization·Cookie를 승인 origin에 그대로 보낸다(C14 결정 4).
		policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
	}
	oldPolicyDir := filepath.Dir(cfg.HTTP.PolicyFile)
	cfg.HTTP.PolicyFile = filepath.Join(dir, "policy.json")
	cfg.HTTP.AuthorityFile = filepath.Join(dir, "authority-template.json")
	cfg.HTTP.AuthorityRoot = dir
	expectedPolicy, _ := json.Marshal(policy)
	expectedLaunch, _ := json.Marshal(cfg)

	if _, e = os.Stat(dir); os.IsNotExist(e) {
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) >= 512 {
			return nil, urlPreparationError("workflow_binding_limit")
		}
		temp, e := os.MkdirTemp(root, ".binding-")
		if e != nil {
			return nil, e
		}
		defer os.RemoveAll(temp)
		for _, pin := range []server.E2HTTPFilePin{policy.CACert, policy.CAKey, policy.AuditKey} {
			raw, e := readPrivateFile(filepath.Join(oldPolicyDir, pin.File), 1<<20)
			if e != nil {
				return nil, e
			}
			if urlSHA(raw) != pin.SHA256 || productWrite(filepath.Join(temp, pin.File), raw) != nil {
				clear(raw)
				return nil, errPreparation
			}
			clear(raw)
		}
		if policy.UpstreamCA != nil {
			pin := policy.UpstreamCA
			raw, e := readPrivateFile(filepath.Join(oldPolicyDir, pin.File), 1<<20)
			if e != nil {
				return nil, e
			}
			if urlSHA(raw) != pin.SHA256 || productWrite(filepath.Join(temp, pin.File), raw) != nil {
				clear(raw)
				return nil, errPreparation
			}
			clear(raw)
		}
		// 시험 계정 자격 사본을 결속 디렉터리에 고정한다(해시 대조). 철회·자격 교체 · 자격 철회 때 removeBindingCredentials가 지운다.
		for _, a := range accounts.Accounts {
			raw, e := readPrivateFile(a.credentialPath, maxCredentialFileBytes)
			if e != nil {
				return nil, e
			}
			if urlSHA(raw) != a.CredentialSHA256 || productWrite(filepath.Join(temp, bindingCredentialName(rec.Target.ID, a.Label, a.CredentialSHA256)), raw) != nil {
				clear(raw)
				return nil, errPreparation
			}
			clear(raw)
		}
		if _, e = productJSON(filepath.Join(temp, "policy.json"), policy); e != nil {
			return nil, e
		}
		cfg.HTTP.PolicyFile = filepath.Join(dir, "policy.json")
		cfg.HTTP.AuthorityFile = filepath.Join(dir, "authority-template.json")
		cfg.HTTP.AuthorityRoot = dir
		if _, e = productJSON(filepath.Join(temp, "launch.json"), cfg); e != nil {
			return nil, e
		}
		f, e := os.Open(temp)
		if e != nil {
			return nil, e
		}
		e = f.Sync()
		_ = f.Close()
		if e != nil {
			return nil, e
		}
		if e = os.Rename(temp, dir); e != nil {
			return nil, e
		}
		f, e = os.Open(root)
		if e != nil {
			return nil, e
		}
		e = f.Sync()
		_ = f.Close()
		if e != nil {
			return nil, e
		}
	}
	launchRaw, launchPin, e := urlReadPin(filepath.Join(dir, "launch.json"), maxLaunchBytes, false)
	clear(launchRaw)
	if e != nil || launchPin.digest != urlSHA(expectedLaunch) {
		return nil, errPreparation
	}
	derived.Launch = urlProtectedPin{File: launchPin.path, SHA256: launchPin.digest}
	policyRaw, policyPin, e := urlReadPin(filepath.Join(dir, "policy.json"), e2config.MaxPolicyBytes, false)
	clear(policyRaw)
	if e != nil {
		return nil, e
	}
	if policyPin.digest != urlSHA(expectedPolicy) {
		return nil, errPreparation
	}
	derived.PolicySHA256 = policyPin.digest
	loaded, e := urlLoadProfileForReport(p.config, p.installationPin, derived, reportOnly)
	if e != nil {
		return nil, e
	}
	loaded.pins = append(loaded.pins, basePins...)
	loaded.inspection.InstallationDigest = urlSHA([]byte(loaded.inspection.InstallationDigest + "\n" + productTargetDigest(rec)))
	loaded.productTarget = &rec
	loaded.productRegistry = p.targets
	loaded.productAccounts = accounts.Accounts
	// 외부 모델 실행에 시험 계정을 결속했으면 서버가 그 실행의 인증 준비 정책을 받도록 동의를 launch로 넘긴다.
	loaded.externalTestAccounts = external && len(accounts.Accounts) > 0
	// 준비 서비스는 이 판정으로 외부 모델 회차의 회차별 확인을 요구하지 않는다(위에서 동의 기록을 확인했다).
	loaded.inspection.ExternalTransferConsented = external && !reportOnly
	if !reportOnly && p.targets.current(rec) != nil {
		loaded.clearMaterials()
		return nil, urlPreparationError("target_approval_changed")
	}
	return loaded, nil
}
func (p *productRuntime) credential(ctx context.Context, id string) (*diagnosisprep.ProductCredential, error) {
	_, cfg, _, pins, e := p.model(id)
	if e != nil {
		return nil, e
	}
	for _, pin := range pins {
		if pin.check() != nil {
			return nil, errPreparation
		}
	}
	_, dsnPin, e := urlReadPin(cfg.RuntimeDSNFile, 16<<10, false)
	if e != nil {
		return nil, e
	}
	pool, e := urlOpenDatabase(ctx, p.config, dsnPin)
	if e != nil {
		return nil, e
	}
	defer pool.Close()
	var model, base, format string
	var configured bool
	e = pool.QueryRowContext(ctx, `SELECT model,base_url,format,COALESCE(api_key,'')<>'' FROM llm_profiles WHERE id=$1`, cfg.ModelProfileID).Scan(&model, &base, &format, &configured)
	if e != nil || model != cfg.ModelRef || base != cfg.ModelBaseURL || format != cfg.ModelFormat {
		return nil, errPreparation
	}
	return &diagnosisprep.ProductCredential{ProfileID: id, Model: model, Configured: configured}, nil
}

type productPreparationEndpoint struct {
	base    diagnosisprep.Endpoint
	product *productRuntime
	backend *urlPreparationRuntime
}

func (e productPreparationEndpoint) Call(ctx context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	// 멈춘 작업 해제는 제품 관리자(운영자)만 쓴다. API도 막지만 비공개 IPC에서도 한 번 더 막는다.
	if (r.Command == diagnosisprep.Release || r.Command == diagnosisprep.ReleaseStatus) && r.Principal != "ARTEX" {
		return diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID, Error: "forbidden"}, nil
	}
	if r.Command == diagnosisprep.Status && r.Principal == "ARTEX" && e.product != nil {
		// 회차 상태에 E③ 확정 단계 결과 상태를 붙인다(e3-product-confirmation-interface.md '회차 상태 응답'). 판정 목록의
		// policy_source와 같은 판독기를 쓴다. 같은 파일 상태를 읽으면 두 응답의 상태·사유가 같다. 회차 소유는 위 Status가 이미 확인했다.
		res, err := e.base.Call(ctx, r)
		if err == nil && res.Error == "" && res.Job != nil && res.Job.ID == r.JobID {
			res.Confirmation = readConfirmationState(e.product.config.RunsDir, res.Job.ID)
		}
		return res, err
	}
	if r.Command != diagnosisprep.ProductControl {
		return e.base.Call(ctx, r)
	}
	out := diagnosisprep.Response{Version: diagnosisprep.Version, RequestID: r.RequestID}
	if diagnosisprep.ValidateRequest(r) != nil {
		return out, diagnosisprep.ErrProtocol
	}
	if r.Principal != "ARTEX" {
		out.Error = "forbidden"
		return out, nil
	}
	p := e.product
	if p == nil {
		out.Error = "unavailable"
		return out, nil
	}
	control := &diagnosisprep.ProductResponse{}
	out.Product = control
	var err error
	if r.Product.Operation == "policy_source" {
		control.PolicySource, control.Confirmation, err = e.policySource(ctx, r)
	} else if strings.HasPrefix(r.Product.Operation, "workflow_") {
		control.Workflow, err = e.workflowControl(ctx, r)
	} else {
		switch r.Product.Operation {
		case "resolve":
			control.Target, err = p.targets.resolve(r.Principal, r.Product.TargetURL)
		case "targets":
			_, control.Targets, err = p.targets.view(r.Principal, "")
		case "target":
			control.Target, _, err = p.targets.view(r.Principal, r.Product.TargetID)
		case "candidate_execute":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.scheduleCandidateVerification(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "candidate_llm_execute":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.scheduleCandidateLLMExecution(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "candidate_llm_cancel":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.cancelCandidateLLMExecution(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "candidate_analyze":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.scheduleCandidateAnalysis(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "candidate_analysis_cancel":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.cancelCandidateAnalysis(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "candidate_stored_cancel":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.cancelCandidateStoredAssessment(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "candidate_cancel":
			if e.backend == nil {
				err = errPreparation
			} else {
				control.Reason, err = e.backend.cancelCandidateVerification(ctx, r.Product.JobID, r.Product.CommandKey)
			}
		case "register":
			var t diagnosisprep.ProductTarget
			t, err = p.targets.register(r.Principal, *r.Product.Registration)
			if err == nil {
				control.Target = &t
			}
		case "approve":
			var t diagnosisprep.ProductTarget
			t, err = p.targets.approve(ctx, r.Principal, r.Product.TargetID)
			if err == nil {
				control.Target = &t
			}
		case "revoke":
			var t diagnosisprep.ProductTarget
			t, err = p.revoke(r.Principal, r.Product.TargetID)
			if err == nil {
				control.Target = &t
			}
		case "credentials":
			control.Credential, err = p.credential(ctx, r.Product.ProfileID)
		case "accounts":
			control.Accounts, err = p.targets.listAccounts(r.Principal, r.Product.TargetID)
		case "external_consent":
			control.ExternalConsent, err = p.externalConsent()
		case "external_consent_record":
			control.ExternalConsent, control.Reason, err = p.recordExternalConsent(r.Principal, r.Product.ExternalConsent.StatementConfirmed, r.Product.ExternalConsent.StatementSHA256, time.Now())
		case "declaration":
			var d diagnosisprep.ProductDeclaration
			if d, err = p.targets.declaration(r.Principal, r.Product.TargetID); err == nil {
				control.Declaration = &d
			}
		case "declaration_put":
			var d diagnosisprep.ProductDeclaration
			d, err = p.targets.putDeclaration(r.Principal, r.Product.TargetID, r.Product.ExpectedRevision, *r.Product.Declaration)
			var fe *declarationInputError
			if errors.As(err, &fe) {
				control.Field = fe.field
			}
			if err == nil {
				control.Declaration = &d
			}
		case "reset_confirmation", "reset_confirm":
			var st diagnosisprep.ProductResetState
			if r.Product.Operation == "reset_confirm" {
				st, err = p.confirmReset(r.Principal, r.Product.TargetID, r.Product.Reset.StatementConfirmed, r.Product.Reset.WritesRef)
			} else {
				st, err = p.targets.resetConfirmation(r.Principal, r.Product.TargetID)
			}
			if err == nil {
				control.Reset = &st
			}
		case "target_login":
			control.Login, err = p.targets.targetLogin(r.Principal, r.Product.TargetID)
		case "target_login_set":
			control.Login, err = p.targets.setTargetLogin(r.Principal, r.Product.TargetID, *r.Product.Login)
		case "account_credential_set":
			var a diagnosisprep.ProductAccount
			// 요청 구조체는 전송 계층이 응답을 보내기 전에 다시 검증하므로 여기서 비우지 않는다(비우면 응답 프레임 없이
			// 연결이 닫힌다). 자격 값은 응답·로그에 넣지 않고, 요청이 끝나면 함께 버려진다.
			a, err = p.targets.setAccountCredential(r.Principal, r.Product.TargetID, r.Product.Account.Label, *r.Product.Credential)
			if err == nil {
				p.removeBindingCredentials(r.Product.TargetID, r.Product.Account.Label)
			}
			if err == nil {
				control.Account = &a
			}
		case "account_credential_clear":
			// 그 계정만 자격 철회(#982 이동찬 P2): 저널 기록 · 자격 파일 삭제 뒤 결속 · 실행 디렉터리의 그 계정 사본도 지운다
			var a diagnosisprep.ProductAccount
			a, err = p.targets.clearAccountCredential(r.Principal, r.Product.TargetID, r.Product.Account.Label)
			if err == nil {
				p.removeBindingCredentials(r.Product.TargetID, r.Product.Account.Label)
				control.Account = &a
			}
		case "account_register":
			var a diagnosisprep.ProductAccount
			a, err = p.targets.registerAccount(r.Principal, r.Product.TargetID, r.Product.Account.Label)
			if err == nil {
				control.Account = &a
			}
		default:
			err = errors.New("invalid_product_operation")
		}
	}
	if err != nil {
		reason := err.Error()
		if len(reason) > 80 || strings.ContainsAny(reason, " /:\n") {
			reason = "target_service_unavailable"
		}
		control.Reason = reason
	}
	return out, nil
}

// 대상 저널이 늘어나도 모델·대상 설치 상한은 불변으로 유지한다.
func sameProductInstallation(a, b *urlProductInstallation) bool { return reflect.DeepEqual(a, b) }

func (p *productRuntime) attach(rec productTargetRecord, id string, cancel context.CancelFunc) (func(), error) {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	if p.targets.current(rec) != nil {
		return nil, errPreparation
	}
	if p.active == nil {
		p.active = map[string]map[string]context.CancelFunc{}
	}
	if p.active[rec.Target.ID] == nil {
		p.active[rec.Target.ID] = map[string]context.CancelFunc{}
	}
	p.active[rec.Target.ID][id] = cancel
	return func() { p.activeMu.Lock(); defer p.activeMu.Unlock(); delete(p.active[rec.Target.ID], id) }, nil
}

// confirmReset은 리셋 확인을 받기 전에 그 쓰기 기록의 회차가 아직 실행 중인지 본다. 실행 중이면 확인 뒤에도 그 회차가
// 생성 요청을 더 보낼 수 있어서, 확인이 아직 생기지 않은 객체까지 리셋한 것처럼 기록되면 안 된다(#738 이동찬 리뷰).
func (p *productRuntime) confirmReset(principal, id string, statementConfirmed bool, writesRef string) (diagnosisprep.ProductResetState, error) {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	st, err := p.targets.resetConfirmation(principal, id)
	if err != nil {
		return st, err
	}
	if st.LastWritesJob != "" {
		if _, running := p.active[id][st.LastWritesJob]; running {
			return diagnosisprep.ProductResetState{}, errors.New("reset_run_active")
		}
	}
	return p.targets.confirmReset(principal, id, statementConfirmed, writesRef)
}

func (p *productRuntime) revoke(principal, id string) (diagnosisprep.ProductTarget, error) {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	t, e := p.targets.revoke(principal, id)
	if e != nil {
		return t, e
	}
	p.removeBindingCredentials(id, "")
	for _, cancel := range p.active[id] {
		cancel()
	}
	return t, nil
}

// inputExcludedPaths는 워크플로 입력의 운영자 제외 경로 목록이다. 진단 흐름 결속과 E² 정책이 같은 목록을 쓴다.
func inputExcludedPaths(input diagnosisprep.Input) []string {
	if input.ExcludedPaths == "" {
		return nil
	}
	return strings.Split(input.ExcludedPaths, "\n")
}

func workflowInputDigest(input diagnosisprep.Input) string {
	raw, _ := json.Marshal(input)
	return urlSHA(raw)
}

// removeBindingCredentials는 실행 결속 디렉터리에 고정한 그 대상(라벨이 있으면 그 계정) 자격 사본을 지운다. 결속 디렉터리는
// 다시 쓰이지 않거나(요약이 바뀜) 자격 파일이 없어 실행 전에 막힌다. 못 지운 파일은 로그만 남긴다.
func (p *productRuntime) removeBindingCredentials(targetID, label string) {
	if p.config.StateDir == "" {
		return
	}
	prefix := "account-" + targetID + "."
	if label != "" {
		prefix = bindingCredentialPrefix(targetID, label)
	}
	// 진행 중·끝난 실행 디렉터리(runs/<job>)의 사본도 지운다. 진행 중 실행은 결속 계정 확인이 account_binding_changed로
	// 멈추고, 그 사이 refresh_session은 사본이 없어 보내지 않는다(#551 윤재상 리뷰).
	if p.config.RunsDir != "" {
		if runs, err := os.ReadDir(p.config.RunsDir); err == nil {
			for _, run := range runs {
				if !run.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(p.config.RunsDir, run.Name()))
				if err != nil {
					continue
				}
				for _, f := range files {
					if strings.HasPrefix(f.Name(), prefix) {
						if err := os.Remove(filepath.Join(p.config.RunsDir, run.Name(), f.Name())); err != nil {
							log.Printf("[preparation] 실행 디렉터리 자격 사본 삭제 실패: 대상=%s", targetID)
						}
					}
				}
			}
		}
	}
	root := filepath.Join(p.config.StateDir, "bindings")
	for _, base := range []string{root, filepath.Join(root, "ordinary")} {
		dirs, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() || !strings.HasPrefix(d.Name(), "bound-") {
				continue
			}
			files, err := os.ReadDir(filepath.Join(base, d.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				if strings.HasPrefix(f.Name(), prefix) {
					if err := os.Remove(filepath.Join(base, d.Name(), f.Name())); err != nil {
						log.Printf("[preparation] 결속 디렉터리 자격 사본 삭제 실패: 대상=%s", targetID)
					}
				}
			}
		}
	}
}

// removeRunCredentials는 실행 디렉터리(runs/<job>)에 옮긴 시험 계정 자격 사본을 지운다. 이름은 정책의 pin 그대로다.
// 못 지우면 로그만 남긴다(값은 남기지 않음).
func removeRunCredentials(runsDir, jobID string, auth *server.E2HTTPFileAuth) {
	if auth == nil || runsDir == "" || !managementReference.MatchString(jobID) {
		return
	}
	parent, err := os.OpenRoot(runsDir)
	if err != nil {
		return
	}
	defer parent.Close()
	root, err := parent.OpenRoot(jobID)
	if err != nil {
		return
	}
	defer root.Close()
	for _, account := range auth.Accounts {
		if !managementReference.MatchString(account.Credential.File) {
			continue
		}
		if err := root.Remove(account.Credential.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[preparation] 실행 디렉터리 자격 사본 삭제 실패: 작업=%s", jobID)
		}
	}
}
