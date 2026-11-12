package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/exploreboundary"
	"github.com/Autumn-27/artex/exploresandbox"
	"github.com/Autumn-27/artex/server"
)

// productExplorationPolicy는 설치된 대상 정책 파일(SHA-256 고정)의 선택 블록이다. 설치 조직이 탐색 경계와 브라우저
// 샌드박스를 compose로 띄웠을 때만 넣는다. 없으면 지금처럼 탐색은 NOT_RUN(boundary_unavailable)이고 E② 정적
// 크롤을 쓴다. URL·모델 출력·환경 변수로는 켜지지 않는다.
type productExplorationPolicy struct {
	// ControlSocket은 탐색 경계 제어 소켓(엔진 uid만 접속)이다.
	ControlSocket string `json:"control_socket"`
	// DirectProxy는 경계의 직접 입구다. 서버의 정적 fetch가 쓴다.
	DirectProxy string `json:"direct_proxy"`
	// BoundaryCAFile·BoundaryCASHA256은 경계가 TLS를 가로챌 때 쓰는 CA 인증서(공개)와 그 고정값이다. 개인키는 경계 밖에 없다.
	BoundaryCAFile   string `json:"boundary_ca_file"`
	BoundaryCASHA256 string `json:"boundary_ca_sha256"`
	// MCPURL은 브라우저 샌드박스의 Playwright MCP(http, 연결마다 새 브라우저 문맥)다.
	MCPURL string `json:"mcp_url"`
	// MaxExternalRequests는 외부 통신 승인 모드에서 렌더링용 외부 자원 GET 상한이다. 0이면 외부 자원 없음.
	MaxExternalRequests uint64 `json:"max_external_requests,omitempty"`
	// DirectBash면 탐색 에이전트에 직접 Bash를 준다(C30). 명령은 진단 Bash와 같은 격리 실행 안에서 돌고 나가는 길은
	// 직접 입구 하나다(exploresandbox). 엔진 컨테이너가 진단 Bash 샌드박스를 띄울 수 있어야 한다.
	DirectBash bool `json:"direct_bash,omitempty"`
}

const (
	explorationControlTimeout = 10 * time.Second
	explorationFetchTimeout   = 30 * time.Second
	explorationCloseTimeout   = 30 * time.Second
	explorationStatusInterval = time.Second
	// unix 소켓 경로(sun_path) 상한보다 짧게 둔다.
	maxExplorationSocketPath = 100
	maxExplorationExternal   = 4096
	// 탐색 직접 Bash: 진단 Bash와 같은 helper, 명령 하나의 시간·정리 상한, 출력 캡처 상한(모델 입력은 바깥에서 가린다).
	explorationBashHelper  = "/app/e2-bash-sandbox"
	explorationBashTimeout = 2 * time.Minute
	explorationBashDrain   = 10 * time.Second
	explorationBashOutput  = 32 << 10
)

var errExplorationPolicy = errors.New("exploration_policy_invalid")

func (p productExplorationPolicy) validate() error {
	if !filepath.IsAbs(p.ControlSocket) || filepath.Clean(p.ControlSocket) != p.ControlSocket || len(p.ControlSocket) > maxExplorationSocketPath ||
		!filepath.IsAbs(p.BoundaryCAFile) || filepath.Clean(p.BoundaryCAFile) != p.BoundaryCAFile || !validSHA256Hex(p.BoundaryCASHA256) ||
		p.MaxExternalRequests > maxExplorationExternal {
		return errExplorationPolicy
	}
	proxy, err := url.Parse(p.DirectProxy)
	if err != nil || proxy.Scheme != "http" || proxy.Hostname() == "" || proxy.Port() == "" || proxy.User != nil || proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" {
		return errExplorationPolicy
	}
	mcp, err := url.Parse(p.MCPURL)
	if err != nil || mcp.Scheme != "http" || mcp.Hostname() == "" || mcp.Port() == "" || mcp.User != nil || mcp.RawQuery != "" || mcp.Fragment != "" ||
		mcp.Path != "" && mcp.Path != "/" && mcp.Path != "/mcp" {
		return errExplorationPolicy
	}
	return nil
}

func validSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// productExplorationHost는 실제 탐색 경계(제어 소켓·직접 입구)와 브라우저 샌드박스(MCP)를 서버 탐색 단계에 묶는다.
type productExplorationHost struct {
	control             exploreboundary.ControlClient
	proxy               *url.URL
	roots               *x509.CertPool
	mcpURL              string
	maxExternalRequests uint64
	// 직접 Bash(DirectBash 정책일 때만). caPEM은 경계 CA 사본, bashRoot는 호출 디렉터리를 둘 0700 디렉터리다(처음 쓸 때
	// 만든다. 호출 디렉터리는 호출이 끝날 때 지운다).
	directBash bool
	bashHelper string
	caPEM      []byte
	bashOnce   sync.Once
	bashRoot   string
	bashErr    error
}

func newProductExplorationHost(p productExplorationPolicy, caPEM []byte) (*productExplorationHost, error) {
	if p.validate() != nil {
		return nil, errExplorationPolicy
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errExplorationPolicy
	}
	proxy, _ := url.Parse(p.DirectProxy)
	return &productExplorationHost{control: exploreboundary.ControlClient{Path: p.ControlSocket, Timeout: explorationControlTimeout}, proxy: proxy, roots: roots,
		mcpURL: p.MCPURL, maxExternalRequests: p.MaxExternalRequests, directBash: p.DirectBash, bashHelper: explorationBashHelper,
		caPEM: append([]byte(nil), caPEM...)}, nil
}

// sessionBash는 회차 하나의 탐색 직접 Bash를 만든다. 정책이 끄면 nil이다. 만들 수 없으면(샌드박스 helper·디렉터리)
// 오류를 돌려주고 호출자는 Bash 없이 탐색한다.
func (h *productExplorationHost) sessionBash() (*exploresandbox.DirectBash, error) {
	if !h.directBash {
		return nil, nil
	}
	h.bashOnce.Do(func() {
		// MkdirTemp는 0700으로 만든다(exploresandbox가 정확히 0700을 요구).
		h.bashRoot, h.bashErr = os.MkdirTemp("", "explore-bash-")
	})
	if h.bashErr != nil {
		return nil, h.bashErr
	}
	return exploresandbox.New(exploresandbox.Config{Helper: h.bashHelper, RootDir: h.bashRoot, DirectProxy: h.proxy, CACert: h.caPEM,
		Timeout: explorationBashTimeout, DrainTimeout: explorationBashDrain, OutputLimit: explorationBashOutput})
}

// exploreTargetOf는 승인된 대상 등록 기록을 경계가 판정에 쓰는 대상 값으로 바꾼다. 연결은 등록 때 고른 주소로만 하고
// SPKI 고정값을 붙인다. 위험 단어는 진단 흐름과 같은 파괴 동작 단어, 제외 경로는 이 실행의 입력이다.
func exploreTargetOf(rec productTargetRecord, excluded []string) (exploreboundary.Target, error) {
	u, err := url.Parse(rec.Target.Origin)
	ip, ipErr := netip.ParseAddr(rec.SelectedIP)
	if err != nil || ipErr != nil || u.Scheme != "https" || u.Hostname() == "" || rec.Target.Port <= 0 || rec.Target.Port > 65535 || rec.Target.TLSSPKISHA256 == "" {
		return exploreboundary.Target{}, exploreboundary.ErrConfig
	}
	return exploreboundary.Target{Ref: rec.Target.ID, Revision: rec.Target.Revision, Host: u.Hostname(), Port: uint16(rec.Target.Port),
		PathPrefixes: append([]string(nil), rec.Target.PathPrefixes...), Methods: append([]string(nil), rec.Target.Methods...), ConnectIP: ip,
		SPKISHA256: rec.Target.TLSSPKISHA256, ExpiresAt: rec.Target.ExpiresAt, RiskyWords: diagnosisflow.DestructiveWords(),
		ExcludedPrefixes: append([]string(nil), excluded...)}, nil
}

func redactExplorationOutput(out []byte) []byte { return []byte(dlp.CredentialText(string(out))) }

// productMCPLease는 compose가 띄운 브라우저 샌드박스의 MCP를 가리킨다. MCP는 연결마다 새 브라우저 문맥을 만들고
// (--isolated, CDP), 탐색 단계가 MCP 세션을 지우면 그 문맥이 닫힌다. 컨테이너 수명은 compose가 가진다.
type productMCPLease struct{ info server.ExplorationBrowserInfo }

func (l productMCPLease) Info() server.ExplorationBrowserInfo { return l.info }
func (l productMCPLease) Close(context.Context) error         { return nil }

// factory는 한 실행의 결속(대상 기록·원본 digest·제외 경로)으로 탐색 host를 조립한다. 실제 연결·송신은 탐색 단계가
// Boundary.Open·StartBrowser를 부를 때 시작한다. current는 대상 승인이 지금도 같은지 본다(철회·만료는 target_ended).
func (h *productExplorationHost) factory(rec productTargetRecord, current func(productTargetRecord) error, digest string, excluded []string, maxBytes int64) server.ExplorationPipelineFactory {
	return func(_ context.Context, b exploration.Binding, w *agent.Worker) (server.ExplorationPipelineHost, error) {
		if b.TargetRef != rec.Target.ID || b.TargetRevision != rec.Target.Revision || w == nil || maxBytes <= 0 {
			return server.ExplorationPipelineHost{}, exploration.ErrBinding
		}
		target, err := exploreTargetOf(rec, excluded)
		if err != nil {
			return server.ExplorationPipelineHost{}, err
		}
		boundary := exploreboundary.ControlBoundary{Client: h.control, SourceDigest: digest, Target: func(_ context.Context, ref string, revision uint64) (exploreboundary.Target, error) {
			if ref != rec.Target.ID || revision != rec.Target.Revision {
				return exploreboundary.Target{}, exploreboundary.ErrTargetEnded
			}
			if err := current(rec); err != nil {
				if errors.Is(err, server.ErrFirstDiagnosisTargetEnded) {
					return exploreboundary.Target{}, exploreboundary.ErrTargetEnded
				}
				return exploreboundary.Target{}, exploreboundary.ErrUnavailable
			}
			return target, nil
		}}
		flow := diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest}
		reader := exploreboundary.ControlReader{Client: h.control, Binding: b, Flow: flow}
		fetch := exploreboundary.DirectFetch{Proxy: h.proxy, Roots: h.roots, Timeout: explorationFetchTimeout, MaxBytes: maxBytes, Status: reader.Status}
		browser := server.ExplorationBrowserInfo{Binding: b, PackageVersion: exploration.MCPPackageVersion,
			MCP: db.MCPServer{Name: exploration.MCPServerName, Transport: "http", URL: h.mcpURL, Enabled: true}}
		session := server.ExplorationSessionOptions{Boundary: boundary,
			StartBrowser: func(_ context.Context, got exploration.Binding) (server.ExplorationBrowserLease, error) {
				if got != b {
					return nil, exploration.ErrBinding
				}
				return productMCPLease{info: browser}, nil
			},
			// 도구 결과(MCP 결과·오류)가 모델 입력·활동 기록으로 가기 전에 자격 형식(인증·쿠키 헤더, 비밀번호·토큰 값, Bearer,
			// JWT, 개인키)을 가린다. 계정 분류·수용시험 S09 개정 전에는 시험 계정 세션도 모델에 넣지 않는다. 주소는 남긴다
			// (모델이 다음 페이지를 고르는 데 필요). 인증 준비가 들어오면 프로그램이 넣은 값의 정확값 가림을 더한다.
			Redact:       redactExplorationOutput,
			CloseTimeout: explorationCloseTimeout, StatusInterval: explorationStatusInterval}
		// 직접 Bash를 만들 수 없으면 그 회차는 Bash 없이 탐색한다(브라우저·정적 fetch는 그대로). 사유는 서버 로그에 남긴다.
		if shell, err := h.sessionBash(); err != nil {
			exploration.ReportFailure("host_capabilities", err)
		} else {
			session.DirectBash = shell
		}
		return server.ExplorationPipelineHost{Session: session, Reader: reader, Fetch: fetch.Fetch, Sequences: reader.Sequences}, nil
	}
}
