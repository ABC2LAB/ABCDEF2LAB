//go:build linux

package main

import (
	"bytes"
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
	"math"
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
	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2host"
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
	// RuntimeLimits는 실행 상한을 설치 조직이 고르는 선택 블록이다(C32). 없으면 지금 기본값이다.
	RuntimeLimits *productRuntimeLimits `json:"runtime_limits,omitempty"`
	// WebSearch는 진단 Planner·Worker의 원본 검색 도구를 켜는 선택 블록이다(루프 복원 결정안 §2-1). 외부 API(OpenRouter)만
	// 쓰는 설치만 받는다. 로컬 모델이 있는 설치(완전 로컬 모드)는 외부 송신 전면 차단이라 거부한다(규칙 8).
	WebSearch *productWebSearch `json:"web_search,omitempty"`
}

// productWebSearch는 검색 백엔드와 키 파일이다. ddgs(DuckDuckGo)는 키가 없고, brave-free·tavily는 키 파일(설치 보호
// 입력)이 있어야 한다. 키는 제어 비밀이라 설치 디렉터리의 보호 파일에만 옮기고 설치 기록에는 해시만 남긴다.
type productWebSearch struct {
	Backend    string `json:"backend"`
	APIKeyFile string `json:"api_key_file,omitempty"`
}

func validProductWebSearch(c productInstallInput) bool {
	w := c.WebSearch
	if w == nil {
		return true
	}
	if c.LocalModel != nil || len(c.OpenRouterManifest) == 0 {
		return false
	}
	switch w.Backend {
	case "ddgs":
		return w.APIKeyFile == ""
	case "brave-free", "tavily":
		return w.APIKeyFile != ""
	}
	return false
}

// productWebSearchLaunch는 설치 입력의 검색 설정을 launch 설정으로 바꾼다. 키는 설치 디렉터리의 보호 파일로 옮기고 pin만
// launch에 적는다. 키 값은 다른 곳에 쓰지 않는다.
func productWebSearchLaunch(w *productWebSearch, pins *productPinWriter, config string) (*launchWebSearch, error) {
	if w == nil {
		return nil, nil
	}
	if w.Backend == "ddgs" {
		return &launchWebSearch{Backend: w.Backend}, nil
	}
	raw, err := readPrivateFile(w.APIKeyFile, maxWebSearchKeyBytes)
	defer clear(raw)
	if err != nil {
		return nil, errPreparation
	}
	key := bytes.TrimSpace(raw)
	if len(key) == 0 || !plainText(string(key), maxWebSearchKeyBytes) {
		return nil, errPreparation
	}
	pin := pins.write("web-search.key", key)
	if pins.err != nil {
		return nil, errPreparation
	}
	return &launchWebSearch{Backend: w.Backend, KeyFile: filepath.Join(config, pin.File), KeySHA256: pin.SHA256}, nil
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

// withTargetPolicyDefaults는 설치 입력에서 비운 대상 정책 칸에 기본값을 넣는다. 기본 포트는 http·https 기본 포트다
// (C22: http 대상도 받는다). 다른 포트는 설치 조직이 정한다.
func withTargetPolicyDefaults(p productTargetPolicy) productTargetPolicy {
	if p.Format == "" {
		p.Format = "abc2lab.product-target-policy/v1"
	}
	if len(p.AllowedPorts) == 0 {
		p.AllowedPorts = []int{80, 443}
	}
	if p.MaxRequests == 0 {
		p.MaxRequests = productTargetRequests
	}
	if p.MaxTargets == 0 {
		p.MaxTargets = 32
	}
	return p
}

func runProductInstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("install-product", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "operator protected installation input")
	root := flags.String("root", "/p", "private product volume")
	hostRoot := flags.String("host-root", "", "separate E2 audit host volume")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *input == "" || os.Geteuid() == 0 ||
		*hostRoot != "" && *hostRoot != "/h" {
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
	if decodeLaunchJSON(raw, &c) != nil {
		return 2
	}
	// 칸을 생략했는지와 0을 적었는지를 가른다. 0은 잘못 적은 값으로 보고 아래 검사가 거부한다.
	var given struct {
		ValidForHours *int64 `json:"valid_for_hours"`
		MaxRuns       *int64 `json:"max_runs"`
	}
	if json.Unmarshal(raw, &given) != nil {
		return 2
	}
	productInstallDefaults(&c, given.ValidForHours == nil, given.MaxRuns == nil)
	if c.Format != "abc2lab.product-install/v2" || len(c.AdminPassword) < 12 || c.ValidForHours < 1 || c.ValidForHours > productMaxInstallHours || c.MaxRuns < 1 || c.MaxRuns > productMaxDelegatedRuns {
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
	if !validProductWebSearch(c) {
		return 2
	}
	c.TargetPolicy = withTargetPolicyDefaults(c.TargetPolicy)
	if !validProductTargetPolicy(c.TargetPolicy) {
		return 2
	}
	// 실행 상한(runtime_limits)은 설치 부작용 전에 검사한다. 범위 밖이면 설치하지 않는다.
	if _, ok := productPolicyBounds(c); !ok {
		return 2
	}
	if _, ok := productRunSecondsFor(c.RuntimeLimits, c.TargetPolicy.Workflow != nil); !ok {
		return 2
	}
	if _, ok := productModelConcurrencyFor(c.RuntimeLimits, c.TargetPolicy.Workflow != nil, c.LocalModel != nil, productOpenRouterMaxConcurrent(c)); !ok {
		return 2
	}
	if _, ok := productPlannerLimitsFor(c.RuntimeLimits, c.TargetPolicy.Workflow != nil); !ok {
		return 2
	}
	if _, ok := productStatusReadRetriesFor(c.RuntimeLimits, c.TargetPolicy.Workflow != nil); !ok {
		return 2
	}
	for _, d := range []string{"config", "data", "keys", "state", "runs", "ipc"} {
		if checkDirectory(filepath.Join(*root, d), true) != nil {
			return 1
		}
	}
	if *hostRoot != "" && checkDirectory(*hostRoot, true) != nil {
		return 1
	}
	cfgDir := filepath.Join(*root, "config")
	if productWrite(filepath.Join(cfgDir, ".installing"), []byte("one_time_installation\n")) != nil {
		return 1
	}
	if err = productInstall(ctx, *root, c, *hostRoot); err != nil {
		fmt.Fprintln(stderr, "product_installation_failed")
		return 1
	}
	fmt.Fprintln(stdout, "product_installed")
	return 0
}

// productMaxBodyBytes는 일반 HTTP 모드의 요청·응답 본문 상한 기본값이다. 공식 평가 정책 템플릿
// (docs/evaluation/profiles/official-e2-policy.json `max_body_bytes`)과 같은 16MiB다(C16 결정안 3). 64KiB였을 때는
// 보통 크기의 페이지 응답도 상한을 넘어 결과 불명으로 끝나 그 intent가 멈췄다. 코드 상한(e2proxy)과 같은 값이다.
const productMaxBodyBytes = 16 << 20

// 제품 설치 기본 상한(C32 결정안 §8). 근거는 원본 ARTEX(포크 기준 fdfb7a17)와 공식 평가 정책(official-e2-policy.json)이다.
// 안전 통제가 아닌 상한(Bash 호출·턴·라운드·Worker 시간)은 요청 예산·실행 시간·동시성·모델 예산보다 먼저 걸리지 않게
// 공식 평가 값이나 원본 값으로 두고, 설치 조직은 runtime_limits로 더 낮게 둘 수 있다. 코드 상한은 그대로다.
const (
	// productTargetRequests는 대상 정책 max_requests가 없을 때 회차 하나의 대상 요청 상한이다(공식 프로파일 request_budget).
	productTargetRequests = 400
	// productMaxCalls는 회차 전체의 E② Bash 실행 수다(E②판 WebFetch 포함, 그래프·파일 도구·후보 제출은 세지 않음).
	productMaxCalls = 1000
	// productMaxConcurrent는 E② 동시 송신 수다. 프록시 연결 수는 이 값 이상이어야 한다(validProductPolicyTemplate).
	productMaxConcurrent    = 16
	productRequestTimeoutMS = 60000
	// productCallTimeoutMS는 E② Bash 호출 하나의 시간 상한이다. 원본 ARTEX Bash 도구 상한(10분)·진단 도구 상한
	// (agent e2HTTPMaxToolDuration)과 같게 둔다. 120초였을 때는 모델이 timeout_ms로 더 길게 줘도 E②가 먼저 끊었다.
	// 송신량은 회차 시간·대상 요청 예산이 묶으므로 이 값을 올려도 통제 범위는 그대로다(2026-11-16 사용자 결정).
	productCallTimeoutMS   = 600000
	productWorkerTurns     = 1000
	productWorkerTimeoutMS = 1200000
	productPlannerRounds   = 100
	// Bash 호출 하나 안에서만 새 요청을 막는 상한(C01). 결과 불명은 쓰기 안전 때문에 1이다.
	productMaxDenials   = 16
	productMaxTransient = 4
	productMaxUnknown   = 1
	// productLocalModelRequests는 로컬 모델 설치의 회차 모델 요청 상한이다(OpenRouter는 승인 manifest가 정한다).
	productLocalModelRequests = 200
)

// productRunBudget은 설치가 모델 프로파일에 적는 회차 예산이다. 대상 요청 예산은 대상 정책 상한이다(대상 등록 값은 그
// 이하라서 회차 불러오기의 총량 검사를 넘지 않는다, 모드와 상관없이). 모델 요청은 로컬 모델 기본값이고 OpenRouter
// 설치는 뒤에서 승인 manifest 값으로 바꾼다.
func productRunBudget(p productTargetPolicy, contextK, output int, runSeconds int64) diagnosisprep.Budget {
	return diagnosisprep.Budget{Requests: productTargetRequestLimit(p), ModelRequests: productLocalModelRequests, InputTokens: productLocalModelRequests * int64(contextK) * 1024, OutputTokens: productLocalModelRequests * int64(output), DurationSeconds: runSeconds}
}

// productTargetRequestLimit은 대상 정책의 회차 요청 상한이다. 비어 있으면 기본값이다(설치는 withTargetPolicyDefaults로
// 먼저 채운다).
func productTargetRequestLimit(p productTargetPolicy) int64 {
	if p.MaxRequests > 0 {
		return p.MaxRequests
	}
	return productTargetRequests
}

// productPolicyBounds는 설치의 E② 한도·프록시·런타임 상한이다. 모드별 기본값에 runtime_limits를 덮어쓰고
// validProductPolicyTemplate으로 검사한다(C32). 설치 입력 검사와 실제 설치가 같은 값을 쓴다.
func productPolicyBounds(c productInstallInput) (server.E2HTTPFilePolicy, bool) {
	// 송신 시도 상한은 대상 정책의 회차 요청 상한과 같게 둔다. 회차 요청 수(대상 등록 값)는 그 상한 이하라서 시도 상한이
	// 요청 예산보다 먼저 걸리지 않는다(C32 결정안 §8).
	requests := productTargetRequestLimit(c.TargetPolicy)
	policy := server.E2HTTPFilePolicy{MaxRequests: uint64(requests), HeaderMode: e2proxy.HeaderPreserve, ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Limits:  server.E2HTTPFileLimits{MaxAttempts: uint64(requests), MaxCalls: productMaxCalls, MaxConcurrent: productMaxConcurrent, MaxBodyBytes: productMaxBodyBytes, RequestTimeoutMS: productRequestTimeoutMS, CallTimeoutMS: productCallTimeoutMS, MaxDenials: productMaxDenials, MaxTransient: productMaxTransient, MaxUnknown: productMaxUnknown},
		Proxy:   server.E2HTTPFileProxy{HeaderTimeoutMS: 1000, RequestTimeoutMS: productRequestTimeoutMS, MaxHeaderBytes: 32768, MaxConnections: productMaxConcurrent},
		Runtime: server.E2HTTPFileRuntime{MaxWorkerTurns: productWorkerTurns, MaxToolCalls: productMaxCalls, WorkerTimeoutMS: productWorkerTimeoutMS, MaxPlannerRounds: productPlannerRounds, MaxIntentRuns: 1, DrainTimeoutMS: 10000, OutputLimit: 32768}}
	// 워크플로 모드는 응답 크기만 대상 정책의 워크플로 칸을 따른다. 나머지 상한은 제품 모드와 같다(§8).
	if flow := c.TargetPolicy.Workflow; flow != nil {
		policy.Limits.MaxBodyBytes = int64(flow.MaxResponseBytes)
		policy.Runtime.OutputLimit = flow.MaxResponseBytes + 8192
	}
	if !applyRuntimeLimits(&policy, c.RuntimeLimits, c.TargetPolicy.Workflow != nil) || !validProductPolicyTemplate(policy) {
		return server.E2HTTPFilePolicy{}, false
	}
	return policy, true
}

// productOpenRouterMaxConcurrent는 설치 입력의 OpenRouter 승인이 허용한 동시 호출 수다. OpenRouter가 없거나 읽을 수
// 없으면 0이다(읽을 수 없는 승인은 설치가 따로 거부한다).
// applyProductLaunchLimits는 runtime_limits 중 launch 설정에 들어가는 칸(동시 모델 호출 수·Plan당 상한)을 고른 모델
// 모드의 launch에 넣는다. 로컬 launch는 동시 호출 1, OpenRouter launch는 그 승인 max_concurrent 이하만 받는다.
// 범위 밖이면 false다(설치 거부).
func applyProductLaunchLimits(launch *launchConfig, c productInstallInput, mode string) bool {
	workflow := c.TargetPolicy.Workflow != nil
	concurrency, ok := productModelConcurrencyFor(c.RuntimeLimits, workflow, mode != "openrouter", productOpenRouterMaxConcurrent(c))
	if !ok {
		return false
	}
	planner, ok := productPlannerLimitsFor(c.RuntimeLimits, workflow)
	if !ok {
		return false
	}
	retries, ok := productStatusReadRetriesFor(c.RuntimeLimits, workflow)
	if !ok {
		return false
	}
	launch.StatusReadRetries = retries
	launch.ModelConcurrency = concurrency
	launch.PlannerTurnsPerPlan, launch.PlannerSecondsPerPlan = planner.PlannerTurnsPerPlan, planner.PlannerSecondsPerPlan
	return true
}

func productOpenRouterMaxConcurrent(c productInstallInput) int64 {
	if len(c.OpenRouterManifest) == 0 {
		return 0
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(c.OpenRouterManifest, &metadata) != nil {
		return 0
	}
	// 설치 전에는 프로필 ID가 없다. 실제 설치와 같은 방식으로 자리값을 넣어 읽는다.
	metadata["profile_id"] = json.RawMessage("1")
	raw, _ := json.Marshal(metadata)
	p, err := llmpolicy.ParseOpenRouterManifest(raw)
	if err != nil {
		return 0
	}
	return p.Limits().MaxConcurrent
}

// productOpenRouterLimits는 OpenRouter 승인으로 회차 예산의 모델 칸을 채우고 설치 기한을 승인 만료로 줄인다.
// perRunTotals는 승인 총량이 점검 하나의 값(v4)인지다. 참이면 설치가 위임 실행 횟수만큼 합산 예산을 잡는다.
func productOpenRouterLimits(p *llmpolicy.OpenRouterPolicy, budget diagnosisprep.Budget, end time.Time) (diagnosisprep.Budget, time.Time, bool) {
	limits := p.Limits()
	budget.ModelRequests, budget.InputTokens, budget.OutputTokens, budget.CostUSDMicros = limits.Requests, limits.InputTokens, limits.OutputTokens, limits.CostMicros
	if limits.ExpiresAt.Before(end) {
		end = limits.ExpiresAt
	}
	return budget, end, p.PerRunTotals()
}

// productInstallDefaults는 설치 입력에서 생략한 수명·위임 실행 횟수를 기본값으로 채운다(2026-11-23 사용자 결정: 30일,
// 100회). 적은 값(0·음수 포함)은 그대로 두어 뒤의 입력 검사가 판단한다.
func productInstallDefaults(c *productInstallInput, hoursOmitted, runsOmitted bool) {
	if hoursOmitted {
		c.ValidForHours = productDefaultInstallHours
	}
	if runsOmitted {
		c.MaxRuns = productDefaultDelegatedRuns
	}
}

// 설치가 저장하는 위임 프로파일의 회차·합산 예산을 한 경로에서 완성한다. OpenRouter 승인 총량이 점검 하나의 값(v4,
// perRunTotals)이면 로컬처럼 위임 실행 횟수만큼 합산 예산을 잡는다(2026-11-23 사용자 결정 (가)). v1~v3 승인은 총량이
// 승인 전체의 누적 값이라 지금처럼 실행 한 번으로 묶는다. 합산이 int64를 넘으면 설치를 거부한다.
func productDelegateProfile(profile urlPreparationProfile, mode string, runs int64, perRunTotals bool) (urlPreparationProfile, error) {
	if mode == "openrouter" && !perRunTotals {
		runs = 1
	}
	if runs < 1 {
		return urlPreparationProfile{}, errPreparation
	}
	profile.MaxRuns = runs
	profile.AggregateBudget = profile.Budget
	for _, v := range []*int64{&profile.AggregateBudget.Requests, &profile.AggregateBudget.ModelRequests, &profile.AggregateBudget.InputTokens,
		&profile.AggregateBudget.OutputTokens, &profile.AggregateBudget.CostUSDMicros, &profile.AggregateBudget.DurationSeconds} {
		if *v < 0 || *v > math.MaxInt64/runs {
			return urlPreparationProfile{}, errPreparation
		}
		*v *= runs
	}
	return profile, nil
}

func productInstall(ctx context.Context, root string, c productInstallInput, hostRoot ...string) error {
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
	for _, m := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate, e2checkpoint.Migrate} {
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
	// 현재 head의 정본 쓰기 역할은 실행·API 역할과 분리한다. LOGIN 자격은
	// 독립 Host 서비스가 준비되기 전에는 발급하지 않는다.
	if _, err = admin.ExecContext(ctx, "CREATE ROLE product_e2_head_writer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
		return errPreparation
	}
	if productGrantRoles(ctx, admin.DB, c.DatabaseName, c.TargetPolicy.Workflow != nil) != nil {
		return errPreparation
	}
	if len(hostRoot) == 1 && hostRoot[0] != "" {
		if productProvisionE2Host(ctx, admin.DB, c.DatabaseHost, c.DatabaseName, hostRoot[0]) != nil {
			return errPreparation
		}
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
	if c.TargetPolicy.Exploration != nil {
		defaults := withExplorationDefaults(*c.TargetPolicy.Exploration)
		c.TargetPolicy.Exploration = &defaults
	}
	if x := c.TargetPolicy.Exploration; x != nil {
		// 탐색 경계 CA(공개 인증서)를 설치 디렉터리로 복사해 고정한다. 개인키는 경계 컨테이너의 보호 입력에만 있다.
		raw, e := readPrivateFile(x.BoundaryCAFile, 64<<10)
		if e != nil {
			return errPreparation
		}
		p := pin("explore-ca.pem", raw)
		_, hostErr := newProductExplorationHost(productExplorationPolicy{ControlSocket: x.ControlSocket, DirectProxy: x.DirectProxy, BoundaryCAFile: filepath.Join(config, "explore-ca.pem"),
			BoundaryCASHA256: p.SHA256, MCPURL: x.MCPURL, MaxExternalRequests: x.MaxExternalRequests, DirectBash: x.DirectBash}, raw)
		clear(raw)
		if hostErr != nil {
			return errPreparation
		}
		x.BoundaryCAFile, x.BoundaryCASHA256 = filepath.Join(config, "explore-ca.pem"), p.SHA256
	}
	if pins.err != nil {
		return errPreparation
	}
	helper, err := os.ReadFile("/app/e2-bash-sandbox")
	if err != nil {
		return errPreparation
	}
	helperSHA := sha256.Sum256(helper)
	policy := server.E2HTTPFilePolicy{Format: server.E2HTTPFilePolicyFormat, RunRef: "installation-template", ResetRef: "operator-target", PolicyRef: "installed-scope", CaseRef: "first-diagnosis", PolicyRevision: "v1", BudgetRef: "installed-target-budget", OwnerRef: "ARTEX", AccountRef: "operator", SessionRef: "operator", ValidFrom: now.Add(-time.Minute), ValidUntil: until, MaxRequests: uint64(productTargetRequestLimit(c.TargetPolicy)), HeaderMode: e2proxy.HeaderPreserve, StateDir: filepath.Join(root, "state"), Helper: "/app/e2-bash-sandbox", HelperSHA256: hex.EncodeToString(helperSHA[:]), CACert: caPin, CAKey: keyPin, AuditKey: auditPin, UpstreamCA: upstream, ApprovalMode: e2proxy.ApprovalPreauthorizedScope}
	bounds, ok := productPolicyBounds(c)
	if !ok {
		return errPreparation
	}
	policy.Limits, policy.Proxy, policy.Runtime = bounds.Limits, bounds.Proxy, bounds.Runtime
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
	webSearch, err := productWebSearchLaunch(c.WebSearch, &pins, config)
	if err != nil {
		return errPreparation
	}
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
		launch := launchConfig{Format: launchFormat, Mode: "general_http", ListenAddr: "127.0.0.1:28080", DataDir: filepath.Join(root, "data"), KeyDir: filepath.Join(root, "keys"), SkillDir: "/app/skills", ModelExecutionMode: mode, ModelProfileID: id, ModelFormat: "openai", ModelBaseURL: base, ModelRef: model, RuntimeDSNFile: filepath.Join(config, "runtime.dsn"), ShutdownTimeoutSeconds: productShutdownTimeoutSeconds, HTTP: &httpLaunch{PolicyFile: filepath.Join(config, "policy.json"), AuthorityFile: filepath.Join(config, "authority-template.json"), AuthorityRoot: config}}
		if len(hostRoot) == 1 && hostRoot[0] != "" {
			launch.E2HostSocket = e2host.ProductSocketPath
		}
		runSeconds, ok := productRunSecondsFor(c.RuntimeLimits, c.TargetPolicy.Workflow != nil)
		if !ok {
			return errPreparation
		}
		if !applyProductLaunchLimits(&launch, c, mode) {
			return errPreparation
		}
		// 검색은 외부 API 실행만 켠다(설치 입력 검사가 로컬 모델 설치를 이미 거부했다).
		if mode == "openrouter" {
			launch.WebSearch = webSearch
		}
		budget := productRunBudget(c.TargetPolicy, contextK, output, runSeconds)
		manifestPath := filepath.Join(config, mode+"-model.json")
		var manifestSHA string
		end := until
		perRunTotals := false
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
			budget, end, perRunTotals = productOpenRouterLimits(p, budget, end)
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
		delegated, e := productDelegateProfile(urlPreparationProfile{ID: "product-model-" + mode, Label: "승인된 URL 진단", Principal: "ARTEX", ModelProfileRef: "model-" + mode, ModelLabel: model, DelegationRef: "product-" + mode, DeriveRuns: true, ValidFrom: now.Add(-30 * time.Second), ValidUntil: end, Budget: budget, Launch: urlProtectedPin{File: launchPath, SHA256: launchSHA}, PolicySHA256: policySHA, ManifestSHA256: manifestSHA, ModelLimitsRef: "installed-model-limits", InputTokensPerCall: int64(contextK) * 1024, OutputTokensPerCall: int64(output)}, mode, c.MaxRuns, perRunTotals)
		if e != nil {
			return e
		}
		installation.Product.Models = append(installation.Product.Models, delegated)
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

// productProvisionE2Host는 신규 설치에서만 Host의 독립 자격을 발급한다.
// 이미 설치된 제품의 무검증 자동 이관에는 호출하지 않는다.
func productProvisionE2Host(ctx context.Context, admin *sql.DB, databaseHost, databaseName, hostRoot string) error {
	if ctx == nil || admin == nil || !managementReference.MatchString(databaseName) || checkDirectory(hostRoot, true) != nil {
		return errPreparation
	}
	password := productRandom(32)
	if _, err := admin.ExecContext(ctx, "CREATE ROLE product_e2_host LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+password+"'"); err != nil {
		return errPreparation
	}
	if _, err := admin.ExecContext(ctx, "GRANT product_e2_head_writer TO product_e2_host"); err != nil {
		return errPreparation
	}
	if _, err := admin.ExecContext(ctx, "GRANT CONNECT ON DATABASE \""+databaseName+"\" TO product_e2_host"); err != nil {
		return errPreparation
	}
	if os.Mkdir(filepath.Join(hostRoot, "journals"), 0700) != nil ||
		productWrite(filepath.Join(hostRoot, "host.dsn"), []byte(productDSN(databaseHost, databaseName, "product_e2_host", password))) != nil ||
		productWrite(filepath.Join(hostRoot, "audit.key"), []byte(productRandom(32))) != nil ||
		productWrite(filepath.Join(hostRoot, "digest.key"), []byte(productRandom(32))) != nil {
		return errPreparation
	}
	anchor := sha256.Sum256([]byte(productRandom(32)))
	epoch := e2checkpoint.Epoch{StoreRef: "product-" + productRandom(8), EpochRef: "epoch-" + productRandom(8), Generation: 1, AnchorDigest: anchor}
	if _, err := admin.ExecContext(ctx, `INSERT INTO e2_http_audit_epochs(store_ref,epoch_ref,generation,anchor_digest) VALUES($1,$2,$3,$4)`,
		epoch.StoreRef, epoch.EpochRef, int64(epoch.Generation), epoch.AnchorDigest[:]); err != nil {
		return errPreparation
	}
	if _, err := productJSON(filepath.Join(hostRoot, "epoch.json"), epoch); err != nil {
		return errPreparation
	}
	return nil
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
	if exec("GRANT USAGE ON SCHEMA public TO product_e2_head_writer") != nil ||
		exec("GRANT SELECT,UPDATE(lock_guard) ON e2_http_audit_epochs TO product_e2_head_writer") != nil ||
		exec("GRANT SELECT,INSERT,UPDATE(revision,phase,checkpoint,snapshot_bytes,snapshot_digest) ON e2_http_audit_heads TO product_e2_head_writer") != nil {
		return errPreparation
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
	// 평가 판정의 발급·finding 연결 권한은 주지 않는다. 일반 HTTP 후보는 확정 취약점으로 승격되지 않는다.
	for _, table := range []string{"diagnostic_captures_v1", "findings", "task_archives", "task_control_modes", "llm_profiles", "settings", "agents", "agent_prompts", "task_categories", "task_relations", "intercept_rules", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links"} {
		if exec("GRANT SELECT ON "+table+" TO product_runtime") != nil {
			return errPreparation
		}
	}
	// E③ 제품 확정 단계 판정과 봉인 확인은 실행 역할이 INSERT만 한다(docs/contracts/e3-product-verdict-store.md §5). 바꾸거나
	// 지우는 권한(UPDATE·DELETE·TRUNCATE)은 주지 않고, 불변 트리거도 막는다. finding과 잇는 link는 주지 않는다(제품 판정은
	// finding을 만들지 않는다).
	for _, table := range []string{"e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_seal_checks"} {
		if exec("GRANT SELECT,INSERT ON "+table+" TO product_runtime") != nil {
			return errPreparation
		}
	}
	if exec("GRANT SELECT,INSERT,UPDATE ON findings TO product_runtime") != nil || exec("GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO product_runtime") != nil || exec("GRANT EXECUTE ON FUNCTION try_inet(text) TO product_runtime") != nil {
		return errPreparation
	}
	for _, table := range []string{"diagnostic_captures_v1", "tasks", "exploration_nodes", "findings", "task_archives", "task_control_modes", "settings", "agents", "agent_prompts", "task_categories", "task_relations", "task_scope", "intercept_rules", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules", "exploration_anchors", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links", "e3_seal_checks"} {
		if exec("GRANT SELECT ON "+table+" TO product_api") != nil {
			return errPreparation
		}
	}
	if exec("GRANT INSERT ON exploration_nodes TO product_api") != nil || exec("GRANT USAGE ON SEQUENCE exploration_nodes_id_seq TO product_api") != nil {
		return errPreparation
	}
	if exec("GRANT SELECT, INSERT, UPDATE(original_digest, evidence_entry, step_id, binding_digest, outcome, reason, reviewer, revision, reviewed_at) ON remediation_followup_reviews TO product_api") != nil {
		return errPreparation
	}
	// 선택 후보 검증의 요청·사건은 관리 API만 append한다. 실행 역할과 보고서
	// 조회 역할에 이 표의 변경 권한을 확대하지 않는다.
	for _, table := range []string{"diagnostic_candidate_verification_requests", "diagnostic_candidate_verification_events"} {
		if exec("GRANT SELECT, INSERT ON "+table+" TO product_api") != nil {
			return errPreparation
		}
	}
	for _, table := range []string{"diagnostic_candidate_verification_plans", "diagnostic_candidate_verification_decisions"} {
		if exec("GRANT SELECT, INSERT ON "+table+" TO product_api") != nil ||
			exec("GRANT SELECT ON "+table+" TO product_runtime") != nil {
			return errPreparation
		}
	}
	if exec("GRANT SELECT ON diagnostic_candidate_verification_executions TO product_api") != nil ||
		exec("GRANT SELECT, INSERT ON diagnostic_candidate_verification_executions TO product_runtime") != nil ||
		exec("GRANT SELECT, INSERT ON diagnostic_candidate_budget_reservations TO product_runtime") != nil ||
		exec("GRANT SELECT ON diagnostic_candidate_verification_analyses TO product_api") != nil ||
		exec("GRANT SELECT, INSERT ON diagnostic_candidate_verification_analyses TO product_runtime") != nil ||
		exec("GRANT SELECT ON diagnostic_candidate_verification_verdicts TO product_api") != nil ||
		exec("GRANT SELECT, INSERT ON diagnostic_candidate_verification_verdicts TO product_runtime") != nil ||
		exec("GRANT SELECT, INSERT ON diagnostic_candidate_verification_answers TO product_api") != nil ||
		exec("GRANT SELECT ON diagnostic_candidate_verification_answers TO product_runtime") != nil ||
		exec("GRANT SELECT ON diagnostic_candidate_verification_requests TO product_runtime") != nil ||
		exec("GRANT SELECT, INSERT ON diagnostic_candidate_verification_events TO product_runtime") != nil ||
		exec("GRANT USAGE ON SEQUENCE diagnostic_candidate_verification_events_id_seq TO product_runtime") != nil {
		return errPreparation
	}
	if exec("GRANT SELECT, INSERT ON diagnostic_candidate_observations_v2 TO product_runtime") != nil ||
		exec("GRANT SELECT ON diagnostic_candidate_observations_v2 TO product_api") != nil {
		return errPreparation
	}
	// 원본 후보가 같은 탐색 intent에서 나온 사실만 확인한다. 그래프 전체
	// 변경 권한은 제품 API에 주지 않는다.
	if exec("GRANT SELECT(exploration_id,src_id,rel,dst_id) ON exploration_edges TO product_api") != nil {
		return errPreparation
	}
	for _, sequence := range []string{"diagnostic_candidate_verification_requests_id_seq", "diagnostic_candidate_verification_events_id_seq"} {
		if exec("GRANT USAGE ON SEQUENCE "+sequence+" TO product_api") != nil {
			return errPreparation
		}
	}
	// 진단 Worker의 insert_assets(C24, 루프 복원 결정안 §9-5): 자산 upsert 칼럼과 작업 연결 출처 칼럼만 갱신하고, 연결
	// 동기화 트리거(sync_task_asset_links)가 호출자 권한으로 지우는 연결 행만 DELETE한다. 서버 검사와 같은 목록이다.
	if exec("GRANT INSERT, UPDATE("+strings.Join(server.FirstDiagnosisAssetUpdateColumns, ", ")+") ON assets TO product_runtime") != nil ||
		exec("GRANT INSERT, DELETE, UPDATE("+strings.Join(server.FirstDiagnosisAssetLinkUpdateColumns, ", ")+") ON task_asset_links TO product_runtime") != nil {
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
	if exec("GRANT INSERT, UPDATE(capture_ref,state,png,image_digest,width,height,captured_at,mask_policy) ON diagnostic_captures_v1 TO product_runtime") != nil {
		return errPreparation
	}
	if exec("GRANT SELECT(id,model,base_url,format),UPDATE(api_key) ON llm_profiles TO product_model_writer") != nil {
		return errPreparation
	}
	return nil
}
