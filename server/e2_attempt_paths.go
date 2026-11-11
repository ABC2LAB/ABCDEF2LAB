package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/pathmask"
)

// E2AttemptPathsFile은 수용시험 회차(eval-e2 설정 v2)의 E② 시도 경로 투영이다(browser-acceptance.md §6).
// 실행 내보내기 디렉터리에 감사 저널과 따로 쓴다. 공식 회차(v1)에는 만들지 않는다.
// 행마다 시도 순번·결정·사유·메서드와, 계측기와 같은 규칙(pathmask)·같은 키로 가린 경로 패턴만 남긴다.
// 경로 원문·query·헤더·본문·키 값은 남기지 않는다. 요청 URL이 없던 거부는 path_pattern이 null이다(경로 미확보).
// 쓰기에 실패하면 그 뒤 행을 쓰지 않는다. 행이 빠진 기록은 러너가 감사 저널의 시도 순번과 대조해 무효로 본다.
const E2AttemptPathsFile = "e2-attempt-paths.jsonl"

type e2AttemptPathRow struct {
	Sequence       uint64  `json:"sequence"`
	Decision       string  `json:"decision"`
	Reason         string  `json:"reason"`
	Method         string  `json:"method"`
	PathPattern    *string `json:"path_pattern"`
	MaskedSegments int     `json:"masked_segments"`
	PathProfileRef string  `json:"path_profile_ref"`
	PathKeyRef     string  `json:"path_key_ref"`
}

// e2AttemptPathLog는 e2proxy.AttemptPathObserver로 연결한다. 훅은 동시에 불릴 수 있어 자체 잠금을 쓴다
// (backend 잠금과 섞지 않는다). 판단·송신·마감에는 끼어들지 않는다.
type e2AttemptPathLog struct {
	mu     sync.Mutex
	key    []byte
	keyRef string
	file   *os.File
	closed bool
	failed bool
}

func newE2AttemptPathLog(key []byte) *e2AttemptPathLog {
	if len(key) != pathmask.KeyBytes {
		return nil
	}
	return &e2AttemptPathLog{key: append([]byte(nil), key...), keyRef: pathmask.KeyRef(key)}
}

// open은 실행별 내보내기 디렉터리에 새 파일을 만든다. 이미 있으면 거부한다(다른 실행의 행과 섞지 않는다).
func (l *e2AttemptPathLog) open(dir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil || l.closed {
		return ErrE2Control
	}
	file, err := os.OpenFile(filepath.Join(dir, E2AttemptPathsFile), e2AttemptPathsOpenFlags, 0600)
	if err != nil {
		return ErrE2Control
	}
	l.file = file
	return nil
}

func (l *e2AttemptPathLog) observe(r e2proxy.Receipt, escapedPath string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.closed || l.file == nil {
		// 열기 전·닫은 뒤의 시도는 기록할 수 없다. 그 행이 빠지므로 러너 대조에서 무효가 된다.
		l.failed = true
		return
	}
	row := e2AttemptPathRow{Sequence: r.Sequence, Decision: r.Decision, Reason: string(r.Reason), Method: r.Method,
		PathProfileRef: pathmask.Profile, PathKeyRef: l.keyRef}
	if escapedPath != "" {
		pattern, masked := pathmask.Mask(escapedPath, l.key)
		row.PathPattern, row.MaskedSegments = &pattern, masked
	}
	line, err := json.Marshal(row)
	if err == nil {
		_, err = l.file.Write(append(line, '\n'))
	}
	if err != nil {
		l.failed = true
	}
}

// close는 기록을 디스크에 내리고 닫는다. 여러 번 불러도 된다. 키는 메모리에서 지운다.
func (l *e2AttemptPathLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	clear(l.key)
	if l.file == nil {
		return nil
	}
	err := l.file.Sync()
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	l.file = nil
	if err != nil {
		l.failed = true
		return ErrE2Control
	}
	return nil
}
