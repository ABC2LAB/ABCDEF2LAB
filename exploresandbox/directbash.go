// Package exploresandbox는 탐색 단계의 직접 Bash다(탐색 계약 §5, C30). 명령은 진단과 같은 Bash 격리 실행
// (e2sandbox: 빈 network namespace·seccomp·자원 상한) 안에서 돌고, 밖으로 나가는 길은 호출마다 만든 relay
// 소켓 하나뿐이다. relay는 받은 바이트를 경계의 직접 입구 한 주소로만 잇는다. 그래서 명령은 브라우저 입구·CDP·
// 경계 제어 소켓·엔진 망에 닿지 못하고, 대상 요청의 범위·쓰기·예산·기록은 경계가 정한다. 진단 요청 통제(E②)는
// 거치지 않는다(탐색은 E② 밖).
package exploresandbox

import (
	"errors"
	"net/url"
	"time"
)

var (
	// ErrConfig는 신뢰한 조립 설정이 틀렸다는 뜻이다.
	ErrConfig = errors.New("exploresandbox: invalid trusted configuration")
	// ErrUnavailable은 격리 실행을 시작하지 못했거나 이미 정리 실패로 닫힌 상태라는 뜻이다.
	ErrUnavailable = errors.New("exploresandbox: isolated execution unavailable")
	// ErrIncomplete는 명령 프로세스·relay 정리를 확인하지 못했다는 뜻이다. 그 뒤 이 DirectBash는 새 명령을 받지 않는다.
	ErrIncomplete = errors.New("exploresandbox: cleanup incomplete")
)

const (
	maxCommandBytes  = 1 << 20
	maxOutputBytes   = 4 << 20
	maxCommandTime   = 10 * time.Minute
	maxDrainTime     = 2 * time.Minute
	maxCACertBytes   = 1 << 20
	maxRelayConns    = 64
	relayDialTimeout = 10 * time.Second
)

// Config는 설치가 고정하는 값이다. 모델 입력으로 바꾸지 않는다.
type Config struct {
	// Helper는 신뢰한 e2-bash-sandbox 실행 파일이다(진단 Bash와 같은 것).
	Helper string
	// RootDir는 서버 uid 소유 0700 디렉터리다. 호출마다 하위 디렉터리를 만들고 끝나면 지운다.
	RootDir string
	// DirectProxy는 경계의 직접 입구다(http://<host>:<port>). relay가 잇는 유일한 주소다.
	DirectProxy *url.URL
	// CACert는 경계 CA(PEM)다. 경계가 대상 TLS를 가로채므로 명령의 HTTPS는 이 CA로 검증한다.
	CACert []byte
	// Timeout은 명령 하나의 실행 상한, DrainTimeout은 그 뒤 정리 상한이다.
	Timeout      time.Duration
	DrainTimeout time.Duration
	// OutputLimit은 명령 출력 캡처 상한(바이트)이다.
	OutputLimit int
	// Seed는 명령마다 /work에 미리 풀어 두는 작업 공간 tar다(비면 빈 작업 공간). 탐색 프로필의 api-recon 지시문·
	// 파이썬 스크립트를 둔다(docs/sandbox-data-decision.md §2-2). 명령마다 새로 풀므로 명령이 바꿔도 다음 명령에 남지 않는다.
	Seed []byte
}

// Result는 프로세스 근거다. Output은 신뢰하지 않는 도구 출력이라 모델·화면에 주기 전에 가려야 한다.
type Result struct {
	Output          []byte
	OutputTruncated bool
	ExitCode        int
	BashExited      bool
	// Reason은 샌드박스가 남긴 종료 사유다(timeout·stopped 등, 정상 종료면 빈 값일 수 있음).
	Reason string
	// RelayConnections는 relay가 직접 입구로 이은 연결 수, RelayRefused는 peer 확인에 실패해 끊은 연결 수다.
	RelayConnections int
	RelayRefused     int
}

func validProxy(u *url.URL) bool {
	return u != nil && u.Scheme == "http" && u.Host != "" && u.Hostname() != "" && u.Port() != "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && u.Opaque == ""
}
