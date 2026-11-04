package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const dataMount = "/app/data"

type statfsSeams struct {
	mounts  []mountEntry
	err     error
	stat    fsStat
	statErr error
	asked   []string
}

func runStatfsWith(t *testing.T, plan string, seams *statfsSeams) (int, string, string) {
	t.Helper()
	prevRoot, prevMounts, prevStat := targetRoot, readMounts, statMounted
	targetRoot = "/proc/1/root"
	readMounts = func() ([]mountEntry, error) { return seams.mounts, seams.err }
	statMounted = func(path string) (fsStat, error) {
		seams.asked = append(seams.asked, path)
		return seams.stat, seams.statErr
	}
	t.Cleanup(func() { targetRoot, readMounts, statMounted = prevRoot, prevMounts, prevStat })
	var stdout, stderr bytes.Buffer
	code := run([]string{"-role", "statfs"}, strings.NewReader(plan), &stdout, &stderr)
	return code, stdout.String(), strings.TrimSpace(stderr.String())
}

func tmpfsAt(id uint64) fsStat {
	return fsStat{tmpfs: true, blocks: 2048, free: 1792, avail: 1792, blockSize: 4096, mountID: id}
}

func TestStatfsMeasuresTheMountListedAtThePoint(t *testing.T) {
	seams := &statfsSeams{mounts: []mountEntry{{1, "/", "overlay"}, {41, dataMount, "tmpfs"}}, stat: tmpfsAt(41)}
	code, out, errOut := runStatfsWith(t, `{"paths":["/app/data"]}`, seams)
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	var report statfsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || len(report.Results) != 1 {
		t.Fatalf("report %q: %v", out, err)
	}
	want := fsUsage{Path: dataMount, MountID: 41, SizeBytes: 8 << 20, UsedBytes: 1 << 20, AvailableBytes: 7 << 20}
	if report.Results[0] != want {
		t.Fatalf("got %+v want %+v", report.Results[0], want)
	}
	// The object is opened through the target's root, never through a path of this container.
	if len(seams.asked) != 1 || seams.asked[0] != "/proc/1/root/app/data" {
		t.Fatalf("stat asked %v", seams.asked)
	}
	// Used is blocks minus free, not minus the unprivileged-available count (they differ on reserved filesystems).
	usage, ok := usageOf("/x", fsStat{tmpfs: true, blocks: 2048, free: 1800, avail: 1792, blockSize: 4096, mountID: 3})
	if !ok || usage.UsedBytes != 248*4096 || usage.AvailableBytes != 1792*4096 {
		t.Fatalf("usage %+v ok=%v", usage, ok)
	}
}

func TestStatfsRejectsAnythingButThatMount(t *testing.T) {
	listed := []mountEntry{{1, "/", "overlay"}, {41, dataMount, "tmpfs"}}
	cases := map[string]struct {
		seams statfsSeams
		code  string
	}{
		// #140 review: a renamed parent or a symlink puts another tmpfs (e.g. /dev/shm) at the same path.
		"other tmpfs at the path": {statfsSeams{mounts: listed, stat: tmpfsAt(77)}, statfsMountMismatch},
		"mount moved away": {statfsSeams{mounts: []mountEntry{{1, "/", "overlay"}, {41, "/old/data", "tmpfs"}},
			stat: tmpfsAt(41)}, statfsNotMounted},
		"listed but not tmpfs": {statfsSeams{mounts: []mountEntry{{41, dataMount, "ext4"}}, stat: tmpfsAt(41)},
			statfsNotTmpfs},
		"kernel says not tmpfs": {statfsSeams{mounts: listed,
			stat: fsStat{blocks: 10, free: 5, avail: 5, blockSize: 4096, mountID: 41}}, statfsNotTmpfs},
		"mountinfo unreadable": {statfsSeams{err: errors.New("denied")}, statfsFailed},
		"stat error":           {statfsSeams{mounts: listed, statErr: errors.New("permission denied")}, statfsFailed},
		"zero block size": {statfsSeams{mounts: listed,
			stat: fsStat{tmpfs: true, blocks: 10, free: 5, avail: 5, mountID: 41}}, statfsFailed},
		"zero blocks": {statfsSeams{mounts: listed, stat: fsStat{tmpfs: true, blockSize: 4096, mountID: 41}},
			statfsFailed},
		"free > blocks": {statfsSeams{mounts: listed,
			stat: fsStat{tmpfs: true, blocks: 10, free: 11, avail: 11, blockSize: 4096, mountID: 41}}, statfsFailed},
		"avail > free": {statfsSeams{mounts: listed,
			stat: fsStat{tmpfs: true, blocks: 10, free: 5, avail: 6, blockSize: 4096, mountID: 41}}, statfsFailed},
		"overflow": {statfsSeams{mounts: listed,
			stat: fsStat{tmpfs: true, blocks: math.MaxUint64 / 2, free: 1, avail: 1, blockSize: 4096, mountID: 41}},
			statfsFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			seams := tc.seams
			code, out, errOut := runStatfsWith(t, `{"paths":["/app/data"]}`, &seams)
			if code != 1 || out != "" || errOut != tc.code {
				t.Fatalf("exit %d out %q stderr %q, want %s", code, out, errOut, tc.code)
			}
		})
	}
	// Mounts stacked at one point: the path resolves to the last one listed.
	stacked := []mountEntry{{41, dataMount, "tmpfs"}, {42, dataMount, "tmpfs"}}
	for id, want := range map[uint64]int{42: 0, 41: 1} {
		seams := &statfsSeams{mounts: stacked, stat: tmpfsAt(id)}
		if code, _, _ := runStatfsWith(t, `{"paths":["/app/data"]}`, seams); code != want {
			t.Fatalf("stacked mount %d: exit %d want %d", id, code, want)
		}
	}
}

func TestStatfsPlanIsStrict(t *testing.T) {
	long := `{"paths":["/` + strings.Repeat("a", maxStatfsPathBytes) + `"]}`
	for name, plan := range map[string]string{
		"empty":     `{"paths":[]}`,
		"root":      `{"paths":["/"]}`,
		"relative":  `{"paths":["app/data"]}`,
		"unclean":   `{"paths":["/app/../etc"]}`,
		"trailing":  `{"paths":["/app/data/"]}`,
		"duplicate": `{"paths":["/a","/a"]}`,
		"too many":  `{"paths":["/a","/b","/c","/d","/e"]}`,
		"unknown":   `{"paths":["/a"],"extra":1}`,
		"two docs":  `{"paths":["/a"]}{"paths":["/b"]}`,
		"too long":  long,
		"not json":  `paths=/a`,
	} {
		t.Run(name, func(t *testing.T) {
			never := &statfsSeams{}
			if code, _, errOut := runStatfsWith(t, plan, never); code != 2 || !strings.HasPrefix(errOut, invalidInput) {
				t.Fatalf("exit %d stderr %q", code, errOut)
			}
			if len(never.asked) != 0 {
				t.Fatal("statfs called for an invalid plan")
			}
		})
	}
	var stderr bytes.Buffer
	if code := run([]string{"-role", "statfs", "-listen", "127.0.0.1:1"}, strings.NewReader(`{"paths":["/a"]}`),
		&bytes.Buffer{}, &stderr); code != 2 {
		t.Fatalf("statfs accepted a front flag: exit %d", code)
	}
}

func TestParseMountinfo(t *testing.T) {
	table := "22 1 0:21 / /proc rw,nosuid shared:5 - proc proc rw\n" +
		"41 30 0:52 / /app/data rw,relatime - tmpfs tmpfs rw,size=8192k\n" +
		"43 30 0:53 / /odd\\040dir\\134x rw master:1 shared:2 - tmpfs tmpfs rw\n"
	mounts, err := parseMountinfo(table)
	if err != nil {
		t.Fatal(err)
	}
	want := []mountEntry{{22, "/proc", "proc"}, {41, "/app/data", "tmpfs"}, {43, "/odd dir\\x", "tmpfs"}}
	if len(mounts) != len(want) {
		t.Fatalf("got %+v", mounts)
	}
	for i := range want {
		if mounts[i] != want[i] {
			t.Fatalf("entry %d: got %+v want %+v", i, mounts[i], want[i])
		}
	}
	for name, bad := range map[string]string{
		"empty":        "",
		"no separator": "41 30 0:52 / /app/data rw tmpfs tmpfs rw\n",
		"short":        "41 30 0:52 / - tmpfs\n",
		"no fstype":    "41 30 0:52 / /app/data rw - \n",
		"zero id":      "0 30 0:52 / /app/data rw - tmpfs tmpfs rw\n",
		"bad id":       "x 30 0:52 / /app/data rw - tmpfs tmpfs rw\n",
		"cut escape":   "41 30 0:52 / /app\\04 rw - tmpfs tmpfs rw\n",
		"bad escape":   "41 30 0:52 / /app\\09x rw - tmpfs tmpfs rw\n",
		"one bad line": table + "garbage\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMountinfo(bad); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// The real kernel, checked against this process's own mountinfo so neither a
// wrong TMPFS_MAGIC nor a path redirect can hide behind a skip.
func TestStatfsKernelBinding(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("statfs role is Linux only")
	}
	mounts, err := readMountinfo("/proc/self/mountinfo")
	if err != nil {
		t.Skipf("mountinfo unavailable: %v", err)
	}
	var shm *mountEntry
	for i := range mounts {
		if mounts[i].point == "/dev/shm" {
			shm = &mounts[i]
		}
	}
	if shm == nil || shm.fstype != "tmpfs" {
		t.Skip("no tmpfs mount at /dev/shm to compare against")
	}
	prevRoot, prevMounts := targetRoot, readMounts
	readMounts = func() ([]mountEntry, error) { return mounts, nil }
	t.Cleanup(func() { targetRoot, readMounts = prevRoot, prevMounts })

	targetRoot = ""
	usage, code := measureMount("/dev/shm")
	if code != "" || usage.MountID != shm.id {
		t.Fatalf("real /dev/shm: code %q usage %+v want mount %d", code, usage, shm.id)
	}
	// Same requested path, but the object behind it is not that mount: a symlink to it, or a plain directory.
	fake := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fake, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/shm", filepath.Join(fake, "dev", "shm")); err != nil {
		t.Fatal(err)
	}
	targetRoot = fake
	if _, code := measureMount("/dev/shm"); code != statfsFailed {
		t.Fatalf("symlink to the real tmpfs accepted: %q", code)
	}
	if err := os.Remove(filepath.Join(fake, "dev", "shm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fake, "dev", "shm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, code := measureMount("/dev/shm"); code != statfsMountMismatch && code != statfsNotTmpfs {
		t.Fatalf("plain directory accepted as the mount: %q", code)
	}
}
