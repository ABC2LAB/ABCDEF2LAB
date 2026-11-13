//go:build linux

package llmpolicy

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	return loadOpenRouterManifestFile(path, io.ReadAll)
}

// Keep the read dependency call-scoped so rechecks can be tested without shared hooks.
func loadOpenRouterManifestFile(path string, readAll func(io.Reader) ([]byte, error)) (*OpenRouterPolicy, error) {
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
		totals, err := openRouterReadLedger(fresh, ledgerName, false, resolver.manifest)
		if err != nil {
			return err
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
		fresh, err := openDirectory()
		if err != nil {
			return err
		}
		defer unix.Close(fresh)
		return openRouterAppendLedger(fresh, ledgerName, resolver.manifest, input, output, cost)
	}
	resolver.settleLedger = func(reserved, actual [3]int64) error {
		fresh, err := openDirectory()
		if err != nil {
			return err
		}
		defer unix.Close(fresh)
		return openRouterSettleLedger(fresh, ledgerName, resolver.manifest, reserved, actual)
	}
	return resolver, nil
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
func openRouterReadLedger(dir int, name string, create bool, m openRouterManifest) (openRouterLedgerTotals, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	fd, err := unix.Openat(dir, name, flags, 0)
	if err == unix.ENOENT && !create {
		return openRouterLedgerTotals{}, nil
	}
	if err != nil {
		return openRouterLedgerTotals{}, ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_SH) != nil {
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

func openRouterAppendLedger(dir int, name string, m openRouterManifest, input, output, cost int64) error {
	fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil {
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
	if n, err := unix.Write(fd, line); err != nil || n != len(line) {
		return ErrOpenRouterPolicy
	}
	if unix.Fsync(fd) != nil || unix.Fsync(dir) != nil {
		return ErrOpenRouterPolicy
	}
	return nil
}

// openRouterSettleLedger는 정산 안 된 같은 예약 값의 예약이 있을 때만 정산 줄을 더해 fsync한다(배타 잠금 안에서 다시
// 읽는다). 기록이 없거나 짝이 없으면 쓰지 않는다. 쓰지 못하면 그 예약은 그대로 다 쓴 것으로 남는다.
func openRouterSettleLedger(dir int, name string, m openRouterManifest, reserved, actual [3]int64) error {
	fd, err := unix.Openat(dir, name, unix.O_RDWR|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrOpenRouterPolicy
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX) != nil {
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
	if n, err := unix.Write(fd, line); err != nil || n != len(line) {
		return ErrOpenRouterPolicy
	}
	if unix.Fsync(fd) != nil {
		return ErrOpenRouterPolicy
	}
	return nil
}
