package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/loginattempt"
)

// E2HTTPLoginAttemptsFile은 회차 상태 디렉터리의 로그인 시도 기록 파일이다(계약 §6, 0600, 덧붙이기). 탐색·진단 단계가 같은
// 파일에 쓰고, 진단 단계는 시작 때 앞 단계 행으로 연속 실패 수를 잇는다(§5 회차당).
const E2HTTPLoginAttemptsFile = "login-attempts.jsonl"

// maxLoginAttemptRowBytes는 기록 한 줄 상한이다. 넘는 줄은 읽지 않는다(행은 칸 이름·상한 있는 목록뿐이라 닿지 않는다).
const maxLoginAttemptRowBytes = 64 << 10

// E2HTTPLoginAttemptLimits는 모델 직접 로그인 판정 설정이다(계약 §4·§5). 제품 발급이 값을 정하고 코드에 박지 않는다.
type E2HTTPLoginAttemptLimits struct {
	// MaxConsecutiveFailures는 아이디별 연속 실패 상한이다(사용자 결정 2026-11-26, 기본 3).
	MaxConsecutiveFailures int `json:"max_consecutive_failures"`
	// MaxContrastProbes는 시도마다 보내는 프로그램 대조 요청 URL 수 상한이다(계약 §4, 기본 3).
	MaxContrastProbes int `json:"max_contrast_probes"`
	// TokenMinBytes는 응답 JSON·헤더 값을 세션 토큰 후보로 볼 최소 길이다(계약 §3).
	TokenMinBytes int `json:"token_min_bytes"`
}

func (l *E2HTTPLoginAttemptLimits) valid() bool {
	return l != nil && l.MaxConsecutiveFailures > 0 && l.MaxContrastProbes >= 0 && l.TokenMinBytes > 0
}

var errLoginAttempts = errors.New("server: login attempt tracker unavailable")

// newE2HTTPLoginAttempts는 모델 직접 로그인 회차의 진단 단계 판정기를 만든다. 성공한 등록 계정 로그인의 세션은 세션
// 저장소에 넣어 확정 단계 계정 결속(E③)과 보낸 요청의 계정 귀속에 쓴다. 기록 파일의 앞 단계 행으로 연속 실패 수를 잇는다.
func newE2HTTPLoginAttempts(ctx context.Context, policy E2HTTPFilePolicy, credentials []agent.E2HTTPAccountCredential, store *e2HTTPSessionStore, prober *loginContrastProber) (*loginattempt.Tracker, error) {
	if policy.Auth == nil || !policy.Auth.LoginAttempts.valid() || store == nil {
		return nil, errLoginAttempts
	}
	origin := ""
	for _, rule := range policy.Rules {
		if rule.Ref == policy.Auth.RuleRef {
			origin = rule.Origin
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || prober == nil {
		return nil, errLoginAttempts
	}
	prober.origin = u
	accounts := make([]loginattempt.Account, 0, len(credentials))
	for _, c := range credentials {
		accounts = append(accounts, loginattempt.Account{Label: c.Label, Username: c.Username, Password: c.Password})
	}
	path := filepath.Join(policy.StateDir, E2HTTPLoginAttemptsFile)
	seed, err := readLoginAttemptRows(path)
	if err != nil {
		return nil, err
	}
	limits := policy.Auth.LoginAttempts
	tracker, err := loginattempt.New(loginattempt.Config{RunRef: policy.RunRef, Stage: loginattempt.StageDiagnosis, Origin: u, Accounts: accounts,
		MaxConsecutiveFailures: limits.MaxConsecutiveFailures, MaxContrastProbes: limits.MaxContrastProbes, TokenMinBytes: limits.TokenMinBytes,
		Sink:        func(row loginattempt.Row) { appendLoginAttemptRow(ctx, path, row) },
		OnSucceeded: func(label string, session http.Header) { store.replace(label, session) },
		OnProbe:     prober.notify})
	if err != nil {
		return nil, errLoginAttempts
	}
	tracker.Seed(seed)
	return tracker, nil
}

// readLoginAttemptRows는 앞 단계가 남긴 행을 읽는다. 파일이 없으면 빈 목록이다. 형식이 틀린 줄은 건너뛰고 로그만 남긴다
// (기록은 감사가 아니라 판정 보조다).
func readLoginAttemptRows(path string) ([]loginattempt.Row, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errLoginAttempts
	}
	defer f.Close()
	var rows []loginattempt.Row
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), maxLoginAttemptRowBytes)
	skipped := 0
	for scanner.Scan() {
		var row loginattempt.Row
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.Schema != loginattempt.Schema {
			skipped++
			continue
		}
		rows = append(rows, row)
	}
	if scanner.Err() != nil {
		return nil, errLoginAttempts
	}
	if skipped > 0 {
		log.Printf("[e2-http] 로그인 시도 기록에서 읽지 못한 줄 %d개를 건너뜀", skipped)
	}
	return rows, nil
}

// appendLoginAttemptRow는 행 하나를 덧붙인다. 쓰지 못해도 송신은 멈추지 않는다(감사 기록이 아님). 로그와 진단 사건만 남긴다.
func appendLoginAttemptRow(ctx context.Context, path string, row loginattempt.Row) {
	b, err := json.Marshal(row)
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err == nil {
			_, err = f.Write(append(b, '\n'))
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		log.Print("[e2-http] 로그인 시도 기록을 쓰지 못함")
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "login_attempt_record_failed", Stage: "execution", Status: "failed", RunRef: row.RunRef})
	}
}

// AppendLoginAttemptRows는 앞 단계(탐색 경계)의 로그인 시도 행을 회차 상태 디렉터리 기록에 덧붙인다(0600). 진단 단계 판정기가
// 실행을 잡을 때 이 행으로 연속 실패 수를 잇는다.
func AppendLoginAttemptRows(stateDir string, rows []loginattempt.Row) error {
	if stateDir == "" {
		return errLoginAttempts
	}
	if len(rows) == 0 {
		return nil
	}
	var buf []byte
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			return errLoginAttempts
		}
		buf = append(append(buf, b...), '\n')
	}
	f, err := os.OpenFile(filepath.Join(stateDir, E2HTTPLoginAttemptsFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return errLoginAttempts
	}
	_, err = f.Write(buf)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
