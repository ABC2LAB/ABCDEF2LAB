package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
	"net/url"
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
	activeMu    sync.Mutex
	active      map[string]map[string]context.CancelFunc
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
		if v.Workflow != nil {
			v.WorkflowRequestLimit = p.targets.policy.MaxRequests
		}
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
func (p *productRuntime) load(principal string, input diagnosisprep.Input, reportOnly bool, reportID string) (*urlLoadedProfile, error) {
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
	p.bindMu.Lock()
	defer p.bindMu.Unlock()
	root := filepath.Join(p.config.StateDir, "bindings")
	if e = os.Mkdir(root, 0700); e != nil && !os.IsExist(e) {
		return nil, errPreparation
	}
	if checkDirectory(root, true) != nil {
		return nil, errPreparation
	}
	bindingSource := productTargetDigest(rec) + "\n" + m.ID + "\n" + m.Launch.SHA256
	if input.WorkflowMode != "" {
		bindingSource += "\n" + workflowInputDigest(input)
	}
	name := "bound-" + urlSHA([]byte(bindingSource))[:32]
	dir := filepath.Join(root, name)
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
	derived.AllowPrivateTarget = netip.MustParseAddr(rec.SelectedIP).IsPrivate()
	derived.Budget.Requests = rec.Target.Requests
	policy.ValidFrom = derived.ValidFrom
	policy.ValidUntil = derived.ValidUntil
	policy.MaxRequests = uint64(rec.Target.Requests)
	policy.PolicyRef = derived.ScopeRef
	if input.WorkflowMode != "" {
		derived.WorkflowMode = input.WorkflowMode
		policy.Workflow = &server.E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: input.WorkflowMode, InputSHA256: workflowInputDigest(input)}
		if input.WorkflowMode == diagnosisflow.Manual {
			policy.ApprovalMode = e2proxy.ApprovalWorkbenchV1
		}
	}

	methods := append([]string(nil), rec.Target.Methods...)
	if input.WorkflowMode != diagnosisflow.Manual {
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
		policy.Rules = append(policy.Rules, server.E2HTTPFileRule{Ref: "target-path-" + string(rune('a'+i)), Origin: rec.Target.Origin, ConnectIP: rec.SelectedIP, PathPrefix: path, PathScopeRef: "readonly-path-" + string(rune('a'+i)), Methods: methods, ApprovalDisplay: server.RequestApprovalDisplay{PublicPath: input.WorkflowMode == diagnosisflow.Manual, QueryKeys: []string{}, JSONFields: []string{}}, TLSSPKISHA256: rec.Target.TLSSPKISHA256})
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
}

func (e productPreparationEndpoint) Call(ctx context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
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
	if strings.HasPrefix(r.Product.Operation, "workflow_") {
		control.Workflow, err = e.workflowControl(ctx, r)
	} else {
		switch r.Product.Operation {
		case "resolve":
			control.Target, err = p.targets.resolve(r.Principal, r.Product.TargetURL)
		case "targets":
			_, control.Targets, err = p.targets.view(r.Principal, "")
		case "target":
			control.Target, _, err = p.targets.view(r.Principal, r.Product.TargetID)
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
			if err == nil {
				control.AccountStatement = productAccountStatement
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
				control.Account = &a
			}
		case "account_register", "account_mark_test", "account_unmark":
			var a diagnosisprep.ProductAccount
			switch r.Product.Operation {
			case "account_register":
				a, err = p.targets.registerAccount(r.Principal, r.Product.TargetID, r.Product.Account.Label)
			case "account_mark_test":
				a, err = p.targets.markTestAccount(r.Principal, r.Product.TargetID, r.Product.Account.Label, r.Product.Account.StatementConfirmed)
			default:
				a, err = p.targets.unmarkAccount(r.Principal, r.Product.TargetID, r.Product.Account.Label)
			}
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
func (p *productRuntime) revoke(principal, id string) (diagnosisprep.ProductTarget, error) {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	t, e := p.targets.revoke(principal, id)
	if e != nil {
		return t, e
	}
	for _, cancel := range p.active[id] {
		cancel()
	}
	return t, nil
}

func workflowInputDigest(input diagnosisprep.Input) string {
	raw, _ := json.Marshal(input)
	return urlSHA(raw)
}
