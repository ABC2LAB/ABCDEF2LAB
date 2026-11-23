//go:build linux

package llmpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// ErrInvalidOpenRouterManifestFile does not expose the path, policy bytes, or an
// underlying filesystem or parser error.
var ErrInvalidOpenRouterManifestFile = errors.New("llmpolicy: invalid profile manifest file")

// LoadOpenRouterManifestFile reads a host-selected, absolute manifest path on
// Linux. The containing directory and file must be owned by the effective UID
// with exact modes 0700 and 0600. Every path component is opened without
// following symlinks; the file must be regular and have exactly one hard link.
// It only returns a resolver. Installing it and invalidating old providers is
// the host's responsibility.
func LoadOpenRouterManifestFile(path string) (*OpenRouterPolicy, error) {
	return loadOpenRouterManifestFile(path, io.ReadAll, "", false)
}

// LoadOpenRouterManifestFileForRun은 제품 URL 진단 회차 하나가 쓰는 정책을 읽는다(2026-11-23 사용자 결정 (가)).
// v4 manifest는 승인 총량(max_*)이 점검 하나의 값이라, 예약 장부를 점검마다 따로 센다(run은 그 점검의 회차 참조).
// 점검 장부는 이 호출에서 새로 만들고(이미 있으면 거부, 같은 점검을 다시 열지 않음), 점검 중 장부가 사라지거나 바뀌면
// 막는다(openRouterRunLedger). 다른 점검의 장부는 지우지 않고 남긴다. run이 비면 보내지
// 않는 확인(새 점검을 시작할 수 있는지)만 할 수 있고 예약·호출 기록은 거부한다. v1~v3 manifest는 지금처럼 승인 하나를
// 누적해 센다(평가 러너·CLI 두 프로세스 경로가 기대는 동작).
func LoadOpenRouterManifestFileForRun(path, run string) (*OpenRouterPolicy, error) {
	if strings.IndexByte(run, 0) >= 0 || len(run) > openRouterRunRefMax {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	return loadOpenRouterManifestFile(path, io.ReadAll, run, true)
}

// openRouterRunRefMax는 점검 참조의 길이 상한이다. 파일 이름에는 참조의 SHA-256 앞부분만 쓴다.
const openRouterRunRefMax = 256

// Keep the read dependency call-scoped so rechecks can be tested without shared hooks.
func loadOpenRouterManifestFile(path string, readAll func(io.Reader) ([]byte, error), run string, forRun bool) (*OpenRouterPolicy, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	dir, name := filepath.Split(path)
	if name == "" || name == "." || name == ".." {
		return nil, ErrInvalidOpenRouterManifestFile
	}

	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	fds := []int{root}
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	if dir == "/" {
		parts = nil
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, ErrInvalidOpenRouterManifestFile
		}
		fd, err := unix.Openat(fds[len(fds)-1], part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, ErrInvalidOpenRouterManifestFile
		}
		fds = append(fds, fd)
	}
	parent := fds[len(fds)-1]
	var dirBefore unix.Stat_t
	if unix.Fstat(parent, &dirBefore) != nil || !ownedManifestDirectory(dirBefore) {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	file := os.NewFile(uintptr(fd), "profile manifest")
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !ownedManifestFile(before) || before.Size < 1 || before.Size > MaxProfileManifestBytes {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	data, err := readAll(io.LimitReader(file, MaxProfileManifestBytes+1))
	if err != nil || len(data) != int(before.Size) {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	var after, atPath, dirAfter unix.Stat_t
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(parent, name, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(parent, &dirAfter) != nil || !sameManifestFile(before, after) || !sameManifestFile(before, atPath) ||
		!sameManifestDirectory(dirBefore, dirAfter) {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	// Ensure a path component was not replaced while the bounded read ran.
	for i, part := range parts {
		var atPath, opened unix.Stat_t
		if unix.Fstatat(fds[i], part, &atPath, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			unix.Fstat(fds[i+1], &opened) != nil || atPath.Dev != opened.Dev || atPath.Ino != opened.Ino ||
			atPath.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, ErrInvalidOpenRouterManifestFile
		}
	}
	resolver, err := ParseOpenRouterManifest(data)
	if err != nil {
		return nil, ErrInvalidOpenRouterManifestFile
	}
	approval := fmt.Sprintf("%x", sha256.Sum256([]byte(resolver.manifest.ApprovalID)))
	// 이전 1회용 표식이 있는 승인은 예약 기록 없이 이미 쓴 것이므로 다 쓴 승인으로 본다.
	legacyMarker := ".openrouter-" + approval + ".used"
	ledgerName := ".openrouter-" + approval + ".ledger"
	callsName := ".openrouter-" + approval + ".calls"
	// 점검별 장부(v4, 제품 URL 경로). 승인 장부 파일은 그대로 두고 점검마다 새 파일 쌍을 쓴다. 기록을 지우거나 고치지
	// 않고 세는 단위만 점검이 된다.
	noSpend := false
	var guard *openRouterRunLedger
	if forRun && resolver.perRun() {
		if run == "" {
			noSpend = true
		} else {
			ref := fmt.Sprintf("%x", sha256.Sum256([]byte(run)))[:32]
			ledgerName = ".openrouter-" + approval + ".run-" + ref + ".ledger"
			callsName = ".openrouter-" + approval + ".run-" + ref + ".calls"
			// 점검 장부는 점검을 시작할 때 한 번만 새로 만든다(O_EXCL). 이미 있으면 이 점검은 이미 시작된 것이라 다시 열지
			// 않는다(장부를 지우고 다시 만들어 사용량을 되돌리는 길을 막는다). 만든 장부의 정체(장치·inode)와 이 점검이 쓴
			// 크기를 기억해 두고, 뒤의 모든 읽기·쓰기에서 같은 파일이 그대로인지 본다(openRouterRunLedger).
			fresh, err := openRouterProtectedDirectory(dir)
			if err != nil {
				return nil, ErrInvalidOpenRouterManifestFile
			}
			guard, err = openRouterCreateRunLedger(fresh, ledgerName)
			_ = unix.Close(fresh)
			if err != nil {
				return nil, err
			}
			resolver.usage.RunLedger = ledgerName
		}
	}
	openDirectory := func() (int, error) {
		fresh, err := openRouterProtectedDirectory(dir)
		if err != nil {
			return -1, ErrOpenRouterPolicy
		}
		var st unix.Stat_t
		if unix.Fstat(fresh, &st) != nil || !sameManifestDirectory(dirBefore, st) {
			_ = unix.Close(fresh)
			return -1, ErrOpenRouterPolicy
		}
		if err := unix.Fstatat(fresh, legacyMarker, &st, unix.AT_SYMLINK_NOFOLLOW); err != unix.ENOENT {
			_ = unix.Close(fresh)
			if err == nil {
				return -1, errOpenRouterApprovalExhausted
			}
			return -1, ErrOpenRouterPolicy
		}
		return fresh, nil
	}
	resolver.available = func() error {
		fresh, err := openDirectory()
		if err != nil {
			return err
		}
		defer unix.Close(fresh)
		var totals openRouterLedgerTotals
		if !noSpend {
			// 새 점검 확인(noSpend)은 아직 장부가 없는 점검이라 합계가 0이다.
			if totals, err = openRouterReadLedger(fresh, ledgerName, false, resolver.manifest, guard); err != nil {
				return err
			}
		}
		m := resolver.manifest
		if !m.fits(totals, m.MaxInputPerCall, 1, openRouterReservedCost(m, 1)) {
			return errOpenRouterApprovalExhausted
		}
		return nil
	}
	resolver.ledger = func(input, output, cost int64) error {
		// 승인별 누적 예약(P3 결정안 C07). 배타 잠금 안에서 다시 읽고, 승인 총량 안일 때만 한 줄을 더해 fsync한다.
		// 재시작·다른 프로세스가 같은 승인을 써도 총량을 넘지 못한다. 예약은 되돌리지 않는다.
		if noSpend {
			return ErrOpenRouterPolicy
		}
		fresh, err := openDirectory()
		if err != nil {
			return err
		}
		defer unix.Close(fresh)
		return openRouterAppendLedger(fresh, ledgerName, resolver.manifest, input, output, cost, guard)
	}
	resolver.settleLedger = func(reserved, actual [3]int64) error {
		if noSpend {
			return ErrOpenRouterPolicy
		}
		fresh, err := openDirectory()
		if err != nil {
			return err
		}
		defer unix.Close(fresh)
		return openRouterSettleLedger(fresh, ledgerName, resolver.manifest, reserved, actual, guard)
	}
	resolver.callLog = func(rec OpenRouterCallRecord) error {
		if noSpend {
			return ErrOpenRouterPolicy
		}
		fresh, err := openDirectory()
		if err != nil {
			return err
		}
		defer unix.Close(fresh)
		return openRouterAppendCall(fresh, callsName, resolver.manifest, rec)
	}
	return resolver, nil
}

// openRouterCallLineMax는 호출 기록 한 줄의 상한이다. 칸은 정수·고정 어휘·시각·모양을 제한한 id뿐이다.
const openRouterCallLineMax = 512

// openRouterAppendCall은 호출 기록 한 줄을 더하고 fsync한다. 순번은 배타 잠금 안에서 센 기존 줄 수 + 1이라 재시작·다른
// 프로세스를 합쳐도 겹치지 않는다. 보호 조건(소유자·0600·단일 링크·정규 파일)과 크기 상한(승인 요청 수 × 줄 상한)을
// 지킨다. 실패해도 호출 결과는 바꾸지 않는다(OpenRouterPolicy.recordCall).
func openRouterAppendCall(dir int, name string, m openRouterManifest, rec OpenRouterCallRecord) error {
	fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil {
		return ErrOpenRouterPolicy
	}
	var st unix.Stat_t
	limit := m.MaxRequests * openRouterCallLineMax
	if unix.Fstat(fd, &st) != nil || !ownedManifestFile(st) || st.Size < 0 || st.Size > limit {
		return ErrOpenRouterPolicy
	}
	data := make([]byte, st.Size)
	if n, err := unix.Pread(fd, data, 0); err != nil || int64(n) != st.Size {
		return ErrOpenRouterPolicy
	}
	rec.Seq = uint64(bytes.Count(data, []byte("\n"))) + 1
	line, err := json.Marshal(rec)
	if err != nil || len(line)+1 > openRouterCallLineMax || st.Size+int64(len(line))+1 > limit {
		return ErrOpenRouterPolicy
	}
	line = append(line, '\n')
	if n, err := unix.Write(fd, line); err != nil || n != len(line) {
		return ErrOpenRouterPolicy
	}
	if unix.Fsync(fd) != nil || (st.Size == 0 && unix.Fsync(dir) != nil) {
		return ErrOpenRouterPolicy
	}
	return nil
}

// 경로의 모든 조상을 다시 열어 소비 시점의 심볼릭 링크 교체도 차단한다.
func openRouterProtectedDirectory(dir string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrOpenRouterPolicy
	}
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			_ = unix.Close(fd)
			return -1, ErrOpenRouterPolicy
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, ErrOpenRouterPolicy
		}
		fd = next
	}
	return fd, nil
}

// openRouterLedgerLine은 예약 한 건이다. 사람이 읽을 수 있는 고정 형식이고 비밀을 담지 않는다.
const openRouterLedgerLine = "r1 %d %d %d\n"

// openRouterSettleLine은 끝난 호출 하나의 정산이다: 예약(입력·출력·비용)과 실제 사용량. 같은 예약 값의 정산 안 된 예약
// 줄 하나와 짝이 되고, 합계는 그 예약 대신 실제 사용량을 센다(C07 보완). 실제는 예약을 넘지 못한다.
const openRouterSettleLine = "s1 %d %d %d %d %d %d\n"

// openRouterLedgerMaxBytes는 기록 파일 크기 상한이다. 한 줄은 최대 4개 정수와 구분자다.
func openRouterLedgerMaxBytes(m openRouterManifest) int64 {
	// 예약 줄 하나와 정산 줄 하나(정수 6개)가 호출마다 많아야 한 번씩이다.
	const line, settle = 3 + 3*20 + 1, 3 + 6*20 + 1
	return m.MaxRequests * (line + settle)
}

// openRouterLedgerFile은 기록 파일의 보호 조건을 보고 크기를 돌려준다.
func openRouterLedgerFile(fd int, m openRouterManifest) (int64, bool) {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || !ownedManifestFile(st) || st.Size < 0 || st.Size > openRouterLedgerMaxBytes(m) {
		return 0, false
	}
	return st.Size, true
}

// openRouterReadLedger는 누적 합계를 읽는다. 없으면 0이다(create가 아니면 만들지 않는다). 형식이 깨졌거나 보호
// 조건(소유자·0600·단일 링크·정규 파일)이 틀리면 오류다.
func openRouterReadLedger(dir int, name string, create bool, m openRouterManifest, guard *openRouterRunLedger) (openRouterLedgerTotals, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	fd, err := unix.Openat(dir, name, flags, 0)
	// 승인 누적 장부는 아직 없으면 0이다. 점검 장부(guard)는 시작 때 만들었으므로 없으면 사라진 것이라 0으로 읽지 않는다.
	if err == unix.ENOENT && !create && guard == nil {
		return openRouterLedgerTotals{}, nil
	}
	if err != nil {
		return openRouterLedgerTotals{}, ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_SH) != nil || !guard.intact(fd) {
		return openRouterLedgerTotals{}, ErrOpenRouterPolicy
	}
	return openRouterLedgerSum(fd, m)
}

func openRouterLedgerSum(fd int, m openRouterManifest) (openRouterLedgerTotals, error) {
	t, _, err := openRouterLedgerScan(fd, m)
	return t, err
}

// openRouterLedgerScan은 합계와 아직 정산하지 않은 예약(예약 값별 개수)을 돌려준다.
func openRouterLedgerScan(fd int, m openRouterManifest) (openRouterLedgerTotals, map[[3]int64]int, error) {
	open := map[[3]int64]int{}
	size, ok := openRouterLedgerFile(fd, m)
	if !ok {
		return openRouterLedgerTotals{}, nil, ErrOpenRouterPolicy
	}
	// 잠금을 잡은 fd에서 처음부터 크기만큼만 읽는다(상한은 위에서 검사).
	data := make([]byte, size)
	for read := int64(0); read < size; {
		n, err := unix.Pread(fd, data[read:], read)
		if err != nil || n <= 0 {
			return openRouterLedgerTotals{}, nil, ErrOpenRouterPolicy
		}
		read += int64(n)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return openRouterLedgerTotals{}, nil, ErrOpenRouterPolicy
	}
	var t openRouterLedgerTotals
	validReservation := func(r [3]int64) bool {
		return r[0] >= 1 && r[0] <= m.MaxInputPerCall && r[1] >= 1 && r[1] <= m.MaxOutputPerCall && r[2] >= 0 && r[2] <= openRouterReservedCost(m, m.MaxOutputPerCall)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" && len(data) == 0 {
			break
		}
		if strings.HasPrefix(line, "s1 ") {
			var r, a [3]int64
			if n, err := fmt.Sscanf(line+"\n", openRouterSettleLine, &r[0], &r[1], &r[2], &a[0], &a[1], &a[2]); err != nil || n != 6 ||
				fmt.Sprintf(openRouterSettleLine, r[0], r[1], r[2], a[0], a[1], a[2]) != line+"\n" || !validReservation(r) || open[r] == 0 ||
				a[0] < 0 || a[0] > r[0] || a[1] < 0 || a[1] > r[1] || a[2] < 0 || a[2] > r[2] {
				return openRouterLedgerTotals{}, nil, ErrOpenRouterPolicy
			}
			open[r]--
			t.Input -= r[0] - a[0]
			t.Output -= r[1] - a[1]
			t.Cost -= r[2] - a[2]
			continue
		}
		var r [3]int64
		if n, err := fmt.Sscanf(line+"\n", openRouterLedgerLine, &r[0], &r[1], &r[2]); err != nil || n != 3 ||
			fmt.Sprintf(openRouterLedgerLine, r[0], r[1], r[2]) != line+"\n" || !validReservation(r) {
			return openRouterLedgerTotals{}, nil, ErrOpenRouterPolicy
		}
		open[r]++
		t.Requests++
		t.Input += r[0]
		t.Output += r[1]
		t.Cost += r[2]
	}
	return t, open, nil
}

func openRouterAppendLedger(dir int, name string, m openRouterManifest, input, output, cost int64, guard *openRouterRunLedger) error {
	flags := unix.O_RDWR | unix.O_CREAT | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if guard != nil {
		// 점검 장부는 시작 때 만든 파일만 쓴다. 없어졌으면 새로 만들지 않고 실패한다.
		flags &^= unix.O_CREAT
	}
	fd, err := unix.Openat(dir, name, flags, 0600)
	if err != nil {
		return ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil || !guard.intact(fd) {
		return ErrOpenRouterPolicy
	}
	totals, err := openRouterLedgerSum(fd, m)
	if err != nil {
		return err
	}
	if !m.fits(totals, input, output, cost) {
		return errOpenRouterApprovalExhausted
	}
	line := []byte(fmt.Sprintf(openRouterLedgerLine, input, output, cost))
	n, err := unix.Write(fd, line)
	guard.update(fd)
	if err != nil || n != len(line) {
		return ErrOpenRouterPolicy
	}
	if unix.Fsync(fd) != nil || unix.Fsync(dir) != nil {
		return ErrOpenRouterPolicy
	}
	return nil
}

// openRouterSettleLedger는 정산 안 된 같은 예약 값의 예약이 있을 때만 정산 줄을 더해 fsync한다(배타 잠금 안에서 다시
// 읽는다). 기록이 없거나 짝이 없으면 쓰지 않는다. 쓰지 못하면 그 예약은 그대로 다 쓴 것으로 남는다.
func openRouterSettleLedger(dir int, name string, m openRouterManifest, reserved, actual [3]int64, guard *openRouterRunLedger) error {
	fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil || !guard.intact(fd) {
		return ErrOpenRouterPolicy
	}
	_, open, err := openRouterLedgerScan(fd, m)
	if err != nil {
		return err
	}
	if open[reserved] == 0 {
		return ErrOpenRouterPolicy
	}
	for i := range reserved {
		if actual[i] < 0 || actual[i] > reserved[i] {
			return ErrOpenRouterPolicy
		}
	}
	line := []byte(fmt.Sprintf(openRouterSettleLine, reserved[0], reserved[1], reserved[2], actual[0], actual[1], actual[2]))
	n, err := unix.Write(fd, line)
	guard.update(fd)
	if err != nil || n != len(line) {
		return ErrOpenRouterPolicy
	}
	if unix.Fsync(fd) != nil {
		return ErrOpenRouterPolicy
	}
	return nil
}

// openRouterRunLedger는 점검 하나가 시작 때 만든 장부의 정체와 그 점검이 지금까지 쓴 크기다(2026-11-23 사용자 결정).
// 점검 장부는 이 점검의 프로세스만 쓰므로, 파일이 그대로라면 장치·inode·크기·상태 변경 시각(ctime)이 이 점검이 마지막으로
// 쓴 뒤의 값과 정확히 같다(지우고 같은 내용으로 다시 만들면 inode 번호가 재사용돼도 ctime이 다르다).
// 장부가 지워졌거나(없음), 다른 파일로 바뀌었거나, 잘렸거나, 남이 덧붙였으면 0으로 읽거나 이어 쓰지 않고 실패한다 —
// 정책이 잠기고 회차는 사용량 불명으로 끝난다(fail-closed). nil이면(승인 누적 장부) 검사하지 않는다.
type openRouterRunLedger struct {
	mu       sync.Mutex
	dev, ino uint64
	size     int64
	ctime    unix.Timespec
}

// openRouterCreateRunLedger는 점검 장부를 새로 만든다(O_EXCL). 이미 있으면 실패한다.
func openRouterCreateRunLedger(dir int, name string) (*openRouterRunLedger, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fsync(fd) != nil || unix.Fsync(dir) != nil || unix.Fstat(fd, &st) != nil || !ownedManifestFile(st) || st.Size != 0 {
		return nil, ErrOpenRouterPolicy
	}
	return &openRouterRunLedger{dev: uint64(st.Dev), ino: st.Ino, ctime: st.Ctim}, nil
}

// intact는 잠금을 잡은 fd가 시작 때 만든 그 장부이고 이 점검이 쓴 만큼만 있는지 본다.
func (g *openRouterRunLedger) intact(fd int) bool {
	if g == nil {
		return true
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return uint64(st.Dev) == g.dev && st.Ino == g.ino && st.Size == g.size && st.Ctim == g.ctime
}

// update는 이 점검이 장부에 쓴 직후(배타 잠금 안) 파일의 크기·ctime을 새 기준으로 삼는다. 쓰다 만 줄은 다음 합계 읽기가
// 형식 오류로 막는다. 상태를 읽지 못하면 기준을 비워 다음 검사가 실패하게 한다.
func (g *openRouterRunLedger) update(fd int) {
	if g == nil {
		return
	}
	var st unix.Stat_t
	g.mu.Lock()
	defer g.mu.Unlock()
	if unix.Fstat(fd, &st) != nil {
		g.size = -1
		return
	}
	g.size, g.ctime = st.Size, st.Ctim
}
