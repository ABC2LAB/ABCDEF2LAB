package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
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
// 쓰기에 실패하면 그 뒤 행을 쓰지 않는다.
//
// 마감(Sync·Close)까지 성공했을 때만 완료 표식(E2AttemptPathsDoneFile)을 쓴다. 표식에는 판·행 수·기록 파일 바이트의
// SHA-256이 있다. 수집기는 표식이 없거나, 행 수·digest가 기록과 다르거나, 행 순번이 감사 저널의 결과 시도와 1:1이 아니면
// 그 회차의 경로 투영을 받지 않는다. 행이 다 있어도 마감에 실패한 기록은 표식이 없으므로 받지 않는다.
// E② 자체의 마감·게시는 이 결과와 상관없이 진행한다.
const (
	E2AttemptPathsFile     = "e2-attempt-paths.jsonl"
	E2AttemptPathsDoneFile = "e2-attempt-paths.done.json"
	E2AttemptPathsFormat   = "abc2lab.e2-attempt-paths/v1"
)

type e2AttemptPathsDone struct {
	Format string `json:"format"`
	Rows   uint64 `json:"rows"`
	SHA256 string `json:"sha256"`
}

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
	dir    string
	rows   uint64
	digest hash.Hash
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
	l.file, l.dir, l.digest = file, dir, sha256.New()
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
		line = append(line, '\n')
		_, err = l.file.Write(line)
	}
	if err != nil {
		l.failed = true
		return
	}
	l.rows++
	l.digest.Write(line) // hash.Hash의 Write는 오류를 돌려주지 않는다.
}

// close는 기록을 디스크에 내리고 닫은 뒤, 성공했을 때만 완료 표식을 쓴다. 키는 메모리에서 지운다.
// 여러 번 불러도 되고, 한 번이라도 실패했으면 다시 불러도 실패를 돌려준다.
func (l *e2AttemptPathLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		if l.failed {
			return ErrE2Control
		}
		return nil
	}
	l.closed = true
	clear(l.key)
	if l.file == nil {
		// 실행을 얻지 못해 연 적이 없다. 기록도 표식도 없다(시도도 없다). 연 적 없이 시도가 있었으면 failed다.
		if l.failed {
			return ErrE2Control
		}
		return nil
	}
	err := l.file.Sync()
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	l.file = nil
	if err == nil && !l.failed {
		err = l.writeDone()
	}
	if err != nil || l.failed {
		l.failed = true
		return ErrE2Control
	}
	return nil
}

// writeDone은 완료 표식을 새로 만들어 내리고 디렉터리 항목까지 내린다.
func (l *e2AttemptPathLog) writeDone() error {
	data, err := json.Marshal(e2AttemptPathsDone{Format: E2AttemptPathsFormat, Rows: l.rows, SHA256: hex.EncodeToString(l.digest.Sum(nil))})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(l.dir, E2AttemptPathsDoneFile), e2AttemptPathsOpenFlags, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if syncErr := file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(l.dir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return err
}
