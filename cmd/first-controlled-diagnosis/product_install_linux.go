//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
	"golang.org/x/crypto/bcrypt"
)

type productInstallInput struct {
	Format               string `json:"format"`
	DatabaseHost         string `json:"database_host"`
	DatabaseName         string `json:"database_name"`
	DatabasePasswordFile string `json:"database_password_file"`
	AdminPassword        string `json:"admin_password"`

	ValidForHours int64 `json:"valid_for_hours"`
	MaxRuns       int64 `json:"max_runs"`
	LocalModel    *struct {
		Model          string   `json:"model"`
		BaseURL        string   `json:"base_url"`
		Addresses      []string `json:"addresses"`
		ContextWindowK int      `json:"context_window_k"`
		MaxTokens      int      `json:"max_tokens"`
	} `json:"local_model,omitempty"`
	TargetPolicy       productTargetPolicy `json:"target_policy"`
	OpenRouterManifest json.RawMessage     `json:"openrouter_manifest,omitempty"`

	UpstreamCAFile string `json:"upstream_ca_file,omitempty"`
}

func productKeyword(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
}
func productDSN(host, name, user, password string) string {
	return "host=" + productKeyword(host) + " port=5432 user=" + productKeyword(user) + " password=" + productKeyword(password) + " dbname=" + productKeyword(name) + " sslmode=disable search_path=public connect_timeout=5"
}
func productCA(name string, until time.Time) (cert, key []byte, parsed *x509.Certificate, private ed25519.PrivateKey, err error) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	parsed = &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: until, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, parsed, parsed, pub, private)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(private)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), parsed, private, err
}
func runProductInstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("install-product", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "operator protected installation input")
	root := flags.String("root", "/p", "private product volume")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *input == "" || os.Geteuid() == 0 {
		return 2
	}
	if _, err := os.Stat(filepath.Join(*root, "config", "installation.json")); err == nil {
		if err = productPrepareAPIProjection(*root); err != nil {
			return 1
		}
		fmt.Fprintln(stdout, "product_already_installed")
		return 0
	}
	raw, err := readPrivateFile(*input, 128<<10)
	if err != nil {
		return 1
	}
	defer clear(raw)
	var c productInstallInput
	if decodeLaunchJSON(raw, &c) != nil || c.Format != "abc2lab.product-install/v2" || len(c.AdminPassword) < 12 || c.ValidForHours < 1 || c.ValidForHours > 168 || c.MaxRuns < 1 || c.MaxRuns > 1000 {
		return 2
	}
	if c.DatabasePasswordFile == "" {
		c.DatabasePasswordFile = "/tmp/product-private/postgres-password"
	}
	ip, e := netip.ParseAddr(c.DatabaseHost)
	if e != nil || ip.String() != c.DatabaseHost || !managementReference.MatchString(c.DatabaseName) {
		return 2
	}
	if c.LocalModel == nil && len(c.OpenRouterManifest) == 0 {
		return 2
	}
	if c.TargetPolicy.Format == "" {
		c.TargetPolicy.Format = "abc2lab.product-target-policy/v1"
	}
	if len(c.TargetPolicy.AllowedPorts) == 0 {
		c.TargetPolicy.AllowedPorts = []int{443}
	}
	if c.TargetPolicy.MaxRequests == 0 {
		c.TargetPolicy.MaxRequests = 4
		if c.TargetPolicy.Workflow != nil {
			c.TargetPolicy.MaxRequests = 64
		}
	}
	if c.TargetPolicy.MaxTargets == 0 {
		c.TargetPolicy.MaxTargets = 32
	}
	if !validProductTargetPolicy(c.TargetPolicy) {
		return 2
	}
	for _, d := range []string{"config", "data", "keys", "state", "runs", "ipc"} {
		if checkDirectory(filepath.Join(*root, d), true) != nil {
			return 1
		}
	}
	cfgDir := filepath.Join(*root, "config")
	if productWrite(filepath.Join(cfgDir, ".installing"), []byte("one_time_installation\n")) != nil {
		return 1
	}
	if err = productInstall(ctx, *root, c); err != nil {
		fmt.Fprintln(stderr, "product_installation_failed")
		return 1
	}
	fmt.Fprintln(stdout, "product_installed")
	return 0
}
func productInstall(ctx context.Context, root string, c productInstallInput) error {
	password, err := readPrivateFile(c.DatabasePasswordFile, 4096)
	if err != nil {
		return errPreparation
	}
	defer clear(password)
	admin, err := pgdb.Open(productDSN(c.DatabaseHost, c.DatabaseName, "postgres", strings.TrimSpace(string(password))))
	if err != nil {
		return errPreparation
	}
	defer admin.Close()
	for _, m := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate} {
		if m(ctx, admin.DB) != nil {
			return errPreparation
		}
	}
	if c.TargetPolicy.Workflow != nil && diagnosisflow.Migrate(ctx, admin.DB) != nil {
		return errPreparation
	}
	if admin.EnsureLLMUsageTable() != nil || admin.EnsureLLMRecordsTable() != nil {
		return errPreparation
	}
	var tasks int
	if admin.QueryRowContext(ctx, "SELECT count(*) FROM tasks").Scan(&tasks) != nil || tasks != 0 {
		return errPreparation
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.AdminPassword), bcrypt.DefaultCost)
	if err != nil || admin.SetSetting("auth.password_hash", string(hash)) != nil {
		return errPreparation
	}
	c.AdminPassword = ""
	if admin.SetSetting("engine.workers", "2") != nil {
		return errPreparation
	}
	config := filepath.Join(root, "config")
	now := time.Now().UTC()
	until := now.Add(time.Duration(c.ValidForHours) * time.Hour)
	for _, role := range []string{"product_runtime", "product_api", "product_model_writer"} {
		pw := productRandom(24)
		if _, err = admin.ExecContext(ctx, "CREATE ROLE "+role+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+pw+"'"); err != nil {
			return errPreparation
		}
		name := "runtime.dsn"
		if role == "product_api" {
			name = "api.dsn"
		}
		if role == "product_model_writer" {
			name = "model-writer.dsn"
		}
		if productWrite(filepath.Join(config, name), []byte(productDSN(c.DatabaseHost, c.DatabaseName, role, pw))) != nil {
			return errPreparation
		}
	}
	if productGrantRoles(ctx, admin.DB, c.DatabaseName, c.TargetPolicy.Workflow != nil) != nil {
		return errPreparation
	}
	if productWrite(filepath.Join(root, "keys", "jwt.key"), []byte(productRandom(32))) != nil {
		return errPreparation
	}
	ca, key, _, _, err := productCA("ABC2LAB E2 proxy", until)
	if err != nil {
		return errPreparation
	}
	pins := productPinWriter{dir: config}
	pin := pins.write
	caPin, keyPin, auditPin := pin("ca.pem", ca), pin("ca-key.pem", key), pin("audit-key", []byte(productRandom(16)))
	var upstream *server.E2HTTPFilePin
	if c.UpstreamCAFile != "" {
		raw, e := readPrivateFile(c.UpstreamCAFile, 1<<20)
		if e != nil {
			return errPreparation
		}
		p := pin("upstream.pem", raw)
		clear(raw)
		upstream = &p
		c.TargetPolicy.UpstreamCAFile = filepath.Join(config, "upstream.pem")
		c.TargetPolicy.UpstreamCASHA256 = p.SHA256
	}
	if pins.err != nil {
		return errPreparation
	}
	helper, err := os.ReadFile("/app/e2-bash-sandbox")
	if err != nil {
		return errPreparation
	}
	helperSHA := sha256.Sum256(helper)
	policy := server.E2HTTPFilePolicy{Format: server.E2HTTPFilePolicyFormat, RunRef: "installation-template", ResetRef: "operator-target", PolicyRef: "installed-scope", CaseRef: "first-diagnosis", PolicyRevision: "v1", BudgetRef: "installed-target-budget", OwnerRef: "ARTEX", AccountRef: "operator", SessionRef: "operator", ValidFrom: now.Add(-time.Minute), ValidUntil: until, MaxRequests: 4, HeaderMode: e2proxy.HeaderPreserve, StateDir: filepath.Join(root, "state"), Helper: "/app/e2-bash-sandbox", HelperSHA256: hex.EncodeToString(helperSHA[:]), CACert: caPin, CAKey: keyPin, AuditKey: auditPin, UpstreamCA: upstream, ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Limits: server.E2HTTPFileLimits{MaxAttempts: 8, MaxCalls: 8, MaxConcurrent: 1, MaxBodyBytes: 65536, RequestTimeoutMS: 5000, CallTimeoutMS: 10000, MaxDenials: 2, MaxTransient: 1, MaxUnknown: 1}, Proxy: server.E2HTTPFileProxy{HeaderTimeoutMS: 1000, RequestTimeoutMS: 5000, MaxHeaderBytes: 32768, MaxConnections: 4}, Runtime: server.E2HTTPFileRuntime{MaxWorkerTurns: 8, MaxToolCalls: 8, WorkerTimeoutMS: 30000, MaxPlannerRounds: 6, MaxIntentRuns: 1, DrainTimeoutMS: 10000, OutputLimit: 32768}}
	if flow := c.TargetPolicy.Workflow; flow != nil {
		policy.Limits.MaxBodyBytes = int64(flow.MaxResponseBytes)
		policy.Limits.MaxAttempts = 200
		policy.Limits.MaxCalls = 64
		policy.Limits.CallTimeoutMS = 60000
		policy.Proxy.RequestTimeoutMS = 60000
		policy.Limits.MaxDenials = 16
		policy.Runtime.OutputLimit = flow.MaxResponseBytes + 8192
		policy.Runtime.MaxToolCalls = 64
		policy.Runtime.MaxWorkerTurns = 16
		policy.Runtime.WorkerTimeoutMS = 180000
		policy.Runtime.MaxPlannerRounds = 12
	}
	if !validProductPolicyTemplate(policy) {
		return errPreparation
	}
	policy.Rules = []server.E2HTTPFileRule{}
	policySHA, err := productJSON(filepath.Join(config, "policy.json"), policy)
	if err != nil {
		return errPreparation
	}
	installation := urlPreparationInstallation{Format: urlPreparationFormat, OperatorUID: uint32(os.Geteuid()), StateDir: filepath.Join(root, "state"), RunsDir: filepath.Join(root, "runs"), SocketPath: filepath.Join(root, "ipc", "preparation.sock"), ReportListenAddr: "127.0.0.1:28080", APILaunch: urlProtectedPin{File: filepath.Join(config, "api-launch.json")}, ProtectedEndpoints: []urlProtectedEndpoint{{Host: c.DatabaseHost, Port: 5432, Addresses: []string{c.DatabaseHost}}}}
	securityPath := filepath.Join(config, "target-policy.json")
	securitySHA, e := productJSON(securityPath, c.TargetPolicy)
	if e != nil {
		return e
	}
	installation.Product = &urlProductInstallation{Format: "abc2lab.product-runtime/v2", TargetPolicy: urlProtectedPin{File: securityPath, SHA256: securitySHA}}
	var first launchConfig
	add := func(mode, model, base string, contextK, output int, manifest json.RawMessage) error {
		profile := &pgdb.LLMProfile{Name: "product-" + mode, Format: "openai", Model: model, BaseURL: base, APIKey: "local-managed", ContextWindowK: contextK, MaxTokens: output, Streaming: false}
		if mode == "openrouter" {
			profile.APIKey = ""
		}
		id, e := admin.SaveProfile(profile)
		if e != nil {
			return errPreparation
		}
		launch := launchConfig{Format: launchFormat, Mode: "general_http", ListenAddr: "127.0.0.1:28080", DataDir: filepath.Join(root, "data"), KeyDir: filepath.Join(root, "keys"), SkillDir: "/app/skills", ModelExecutionMode: mode, ModelProfileID: id, ModelFormat: "openai", ModelBaseURL: base, ModelRef: model, RuntimeDSNFile: filepath.Join(config, "runtime.dsn"), ShutdownTimeoutSeconds: 20, HTTP: &httpLaunch{PolicyFile: filepath.Join(config, "policy.json"), AuthorityFile: filepath.Join(config, "authority-template.json"), AuthorityRoot: config}}
		budget := diagnosisprep.Budget{Requests: 4, ModelRequests: 20, InputTokens: 20 * int64(contextK) * 1024, OutputTokens: 20 * int64(output), DurationSeconds: 180}
		if c.TargetPolicy.Workflow != nil {
			budget.Requests = c.TargetPolicy.MaxRequests
			budget.DurationSeconds = 600
		}
		manifestPath := filepath.Join(config, mode+"-model.json")
		var manifestSHA string
		end := until
		if mode == "openrouter" {
			var raw map[string]json.RawMessage
			if json.Unmarshal(manifest, &raw) != nil {
				return errPreparation
			}
			raw["profile_id"], _ = json.Marshal(id)
			encoded, _ := json.Marshal(raw)
			p, e := llmpolicy.ParseOpenRouterManifest(encoded)
			if e != nil {
				return errPreparation
			}
			limits := p.Limits()
			budget.ModelRequests, budget.InputTokens, budget.OutputTokens, budget.CostUSDMicros = limits.Requests, limits.InputTokens, limits.OutputTokens, limits.CostMicros
			if limits.ExpiresAt.Before(end) {
				end = limits.ExpiresAt
			}
			launch.OpenRouterManifestPath = manifestPath
			manifestSHA = urlSHA(encoded)
			if productWrite(manifestPath, encoded) != nil {
				return errPreparation
			}
		} else {
			launch.LocalLLMManifestPath = manifestPath
			manifestSHA, e = productJSON(manifestPath, map[string]any{"version": 1, "profiles": []any{map[string]any{"profile_id": id, "format": "openai", "model": model, "base_url": base, "addresses": c.LocalModel.Addresses}}})
			if e != nil {
				return errPreparation
			}
			for _, addr := range c.LocalModel.Addresses {
				ip, e := netip.ParseAddr(addr)
				if e != nil {
					return errPreparation
				}
				u, e := url.Parse(base)
				if e != nil {
					return errPreparation
				}
				port := 80
				if u.Scheme == "https" {
					port = 443
				}
				if u.Port() != "" {
					fmt.Sscan(u.Port(), &port)
				}
				installation.ProtectedEndpoints = append(installation.ProtectedEndpoints, urlProtectedEndpoint{Host: u.Hostname(), Port: port, Addresses: []string{ip.String()}})
			}
		}
		launchPath := filepath.Join(config, mode+"-launch.json")
		launchSHA, e := productJSON(launchPath, launch)
		if e != nil {
			return errPreparation
		}
		runs := c.MaxRuns
		if mode == "openrouter" {
			runs = 1
		}
		aggregate := budget
		aggregate.Requests *= runs
		aggregate.ModelRequests *= runs
		aggregate.InputTokens *= runs
		aggregate.OutputTokens *= runs
		aggregate.CostUSDMicros *= runs
		aggregate.DurationSeconds *= runs
		installation.Product.Models = append(installation.Product.Models, urlPreparationProfile{ID: "product-model-" + mode, Label: "승인된 URL 진단", Principal: "ARTEX", ModelProfileRef: "model-" + mode, ModelLabel: model, DelegationRef: "product-" + mode, DeriveRuns: true, ValidFrom: now.Add(-30 * time.Second), ValidUntil: end, MaxRuns: runs, AggregateBudget: aggregate, Budget: budget, Launch: urlProtectedPin{File: launchPath, SHA256: launchSHA}, PolicySHA256: policySHA, ManifestSHA256: manifestSHA, ModelLimitsRef: "installed-model-limits", InputTokensPerCall: int64(contextK) * 1024, OutputTokensPerCall: int64(output)})
		if first.Format == "" {
			first = launch
			if admin.SetActiveProfile(id) != nil {
				return errPreparation
			}
		}
		return nil
	}
	if c.LocalModel != nil {
		m := c.LocalModel
		if add("local_only", m.Model, m.BaseURL, m.ContextWindowK, m.MaxTokens, nil) != nil {
			return errPreparation
		}
	}
	if len(c.OpenRouterManifest) > 0 {
		var metadata map[string]json.RawMessage
		if json.Unmarshal(c.OpenRouterManifest, &metadata) != nil {
			return errPreparation
		}
		metadata["profile_id"] = json.RawMessage("1")
		raw, _ := json.Marshal(metadata)
		p, e := llmpolicy.ParseOpenRouterManifest(raw)
		if e != nil {
			return errPreparation
		}
		if add("openrouter", llmpolicy.OpenRouterModel, llmpolicy.OpenRouterBaseURL, 256, int(p.Limits().OutputPerCall), c.OpenRouterManifest) != nil {
			return errPreparation
		}
	}
	first.ListenAddr = "127.0.0.1:28081"
	first.RuntimeDSNFile = filepath.Join(config, "api.dsn")
	installation.APILaunch.SHA256, err = productJSON(installation.APILaunch.File, first)
	if err != nil {
		return errPreparation
	}
	if _, err = productJSON(filepath.Join(config, "installation.json"), installation); err != nil {
		return errPreparation
	}
	installed, installationPin, e := readURLInstallation(filepath.Join(config, "installation.json"))
	if e != nil {
		return errPreparation
	}
	runtime, e := newProductRuntime(installed, installationPin)
	if e != nil {
		return e
	}
	defer runtime.targets.Close()
	if _, e = runtime.profiles(); e != nil {
		return e
	}
	return productPrepareAPIProjection(root)
}
func productGrantRoles(ctx context.Context, pool *sql.DB, name string, workflow ...bool) error {
	exec := func(s string) error { _, e := pool.ExecContext(ctx, s); return e }
	if exec("REVOKE CREATE,TEMP ON DATABASE \""+name+"\" FROM PUBLIC") != nil || exec("REVOKE CREATE ON SCHEMA public FROM PUBLIC") != nil {
		return errPreparation
	}
	for _, r := range []string{"product_runtime", "product_api", "product_model_writer"} {
		if exec("GRANT CONNECT ON DATABASE \""+name+"\" TO "+r) != nil || exec("GRANT USAGE ON SCHEMA public TO "+r) != nil {
			return errPreparation
		}
	}
	for _, table := range []string{"tasks", "explorations", "exploration_nodes", "task_llm_profiles", "request_approvals", "e2_prompt_runs", "e2_prompt_observations"} {
		if exec("GRANT SELECT,INSERT,UPDATE ON "+table+" TO product_runtime") != nil {
			return errPreparation
		}
	}
	for _, table := range []string{"activity", "exploration_edges", "task_scope", "task_constraints", "exploration_anchors", "llm_usage", "llm_records"} {
		if exec("GRANT SELECT,INSERT ON "+table+" TO product_runtime") != nil {
			return errPreparation
		}
	}
	if len(workflow) == 1 && workflow[0] {
		if exec("GRANT SELECT,INSERT,UPDATE ON diagnosis_workflows TO product_runtime") != nil || exec("GRANT SELECT,INSERT ON diagnosis_workflow_events TO product_runtime") != nil {
			return errPreparation
		}
	}
	// 판정의 발급 권한은 주지 않는다. 일반 HTTP 후보는 확정 취약점으로 승격되지 않는다.
	for _, table := range []string{"findings", "task_archives", "task_control_modes", "llm_profiles", "settings", "agents", "agent_prompts", "task_categories", "task_relations", "intercept_rules", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links"} {
		if exec("GRANT SELECT ON "+table+" TO product_runtime") != nil {
			return errPreparation
		}
	}
	if exec("GRANT SELECT,INSERT,UPDATE ON findings TO product_runtime") != nil || exec("GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO product_runtime") != nil || exec("GRANT EXECUTE ON FUNCTION try_inet(text) TO product_runtime") != nil {
		return errPreparation
	}
	for _, table := range []string{"tasks", "exploration_nodes", "findings", "task_archives", "task_control_modes", "settings", "agents", "agent_prompts", "task_categories", "task_relations", "intercept_rules", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules", "exploration_anchors", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links"} {
		if exec("GRANT SELECT ON "+table+" TO product_api") != nil {
			return errPreparation
		}
	}
	if exec("GRANT INSERT ON exploration_nodes TO product_api") != nil || exec("GRANT USAGE ON SEQUENCE exploration_nodes_id_seq TO product_api") != nil {
		return errPreparation
	}
	for _, r := range []string{"product_runtime", "product_api"} {
		for _, lock := range []string{"e3_verdict_issuances(id)", "assets(id)", "task_asset_links(task_id)", "exploration_anchors(node_id)"} {
			if exec("GRANT UPDATE("+strings.Split(strings.Split(lock, "(")[1], ")")[0]+") ON "+strings.Split(lock, "(")[0]+" TO "+r) != nil {
				return errPreparation
			}
		}
	}
	for _, table := range []string{"tasks", "exploration_nodes", "findings"} {
		if exec("GRANT UPDATE(id) ON "+table+" TO product_api") != nil {
			return errPreparation
		}
	}
	if exec("GRANT SELECT(id,model,base_url,format),UPDATE(api_key) ON llm_profiles TO product_model_writer") != nil {
		return errPreparation
	}
	return nil
}
