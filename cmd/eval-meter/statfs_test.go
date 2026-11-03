package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
)

func runStatfsWith(t *testing.T, plan string, stat func(string) (fsStat, error)) (int, string, string) {
	t.Helper()
	previous := statPath
	statPath = stat
	t.Cleanup(func() { statPath = previous })
	var stdout, stderr bytes.Buffer
	code := run([]string{"-role", "statfs"}, strings.NewReader(plan), &stdout, &stderr)
	return code, stdout.String(), strings.TrimSpace(stderr.String())
}

func TestStatfsReportsTmpfsUsageFromTheKernelAnswer(t *testing.T) {
	stat := func(path string) (fsStat, error) {
		return fsStat{tmpfs: true, blocks: 2048, free: 1792, avail: 1792, blockSize: 4096}, nil
	}
	code, out, errOut := runStatfsWith(t, `{"paths":["/proc/1/root/app/data","/proc/1/root/var/lib/x"]}`, stat)
	if code != 0 || errOut != "" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	var report statfsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || len(report.Results) != 2 {
		t.Fatalf("report %q: %v", out, err)
	}
	want := fsUsage{Path: "/proc/1/root/app/data", SizeBytes: 8 << 20, UsedBytes: 1 << 20, AvailableBytes: 7 << 20}
	if report.Results[0] != want {
		t.Fatalf("got %+v want %+v", report.Results[0], want)
	}
	// Used is blocks minus free, not minus the unprivileged-available count (they differ on reserved filesystems).
	usage, ok := usageOf("/x", fsStat{tmpfs: true, blocks: 2048, free: 1800, avail: 1792, blockSize: 4096})
	if !ok || usage.UsedBytes != 248*4096 || usage.AvailableBytes != 1792*4096 {
		t.Fatalf("usage %+v ok=%v", usage, ok)
	}
}

func TestStatfsFailsClosed(t *testing.T) {
	plan := `{"paths":["/proc/1/root/app/data"]}`
	cases := map[string]struct {
		stat fsStat
		err  error
		code string
	}{
		"not tmpfs":       {fsStat{tmpfs: false, blocks: 10, free: 5, avail: 5, blockSize: 4096}, nil, statfsNotTmpfs},
		"statfs error":    {fsStat{}, errors.New("permission denied"), statfsFailed},
		"zero block size": {fsStat{tmpfs: true, blocks: 10, free: 5, avail: 5}, nil, statfsFailed},
		"zero blocks":     {fsStat{tmpfs: true, blockSize: 4096}, nil, statfsFailed},
		"free > blocks":   {fsStat{tmpfs: true, blocks: 10, free: 11, avail: 11, blockSize: 4096}, nil, statfsFailed},
		"avail > free":    {fsStat{tmpfs: true, blocks: 10, free: 5, avail: 6, blockSize: 4096}, nil, statfsFailed},
		"overflow":        {fsStat{tmpfs: true, blocks: math.MaxUint64 / 2, free: 1, avail: 1, blockSize: 4096}, nil, statfsFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := runStatfsWith(t, plan, func(string) (fsStat, error) { return tc.stat, tc.err })
			if code != 1 || out != "" || errOut != tc.code {
				t.Fatalf("exit %d out %q stderr %q, want %s", code, out, errOut, tc.code)
			}
		})
	}
}

func TestStatfsPlanIsStrict(t *testing.T) {
	never := func(string) (fsStat, error) {
		t.Fatal("statfs called for an invalid plan")
		return fsStat{}, nil
	}
	long := `{"paths":["/` + strings.Repeat("a", maxStatfsPathBytes) + `"]}`
	for name, plan := range map[string]string{
		"empty":     `{"paths":[]}`,
		"relative":  `{"paths":["proc/1/root/app"]}`,
		"unclean":   `{"paths":["/proc/1/root/app/../etc"]}`,
		"trailing":  `{"paths":["/proc/1/root/app/"]}`,
		"duplicate": `{"paths":["/a","/a"]}`,
		"too many":  `{"paths":["/a","/b","/c","/d","/e"]}`,
		"unknown":   `{"paths":["/a"],"extra":1}`,
		"two docs":  `{"paths":["/a"]}{"paths":["/b"]}`,
		"too long":  long,
		"not json":  `paths=/a`,
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, errOut := runStatfsWith(t, plan, never); code != 2 || !strings.HasPrefix(errOut, invalidInput) {
				t.Fatalf("exit %d stderr %q", code, errOut)
			}
		})
	}
	var stderr bytes.Buffer
	if code := run([]string{"-role", "statfs", "-listen", "127.0.0.1:1"}, strings.NewReader(`{"paths":["/a"]}`),
		&bytes.Buffer{}, &stderr); code != 2 {
		t.Fatalf("statfs accepted a front flag: exit %d", code)
	}
}

// The real kernel answer, checked against /proc/self/mountinfo so a wrong
// TMPFS_MAGIC cannot hide behind a skip: whatever the mount table calls tmpfs
// must be reported as tmpfs, and the root filesystem here is not tmpfs.
func TestStatfsKernelTypeCheck(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("statfs role is Linux only")
	}
	types := mountTypes(t)
	checked := 0
	for _, mount := range []string{"/dev/shm", "/"} {
		fsType, ok := types[mount]
		if !ok {
			continue
		}
		st, err := statFilesystem(mount)
		if err != nil {
			t.Fatalf("%s: %v", mount, err)
		}
		if st.tmpfs != (fsType == "tmpfs") {
			t.Fatalf("%s: mountinfo says %q, statfs tmpfs=%v", mount, fsType, st.tmpfs)
		}
		if st.tmpfs {
			if _, ok := usageOf(mount, st); !ok {
				t.Fatalf("tmpfs answer rejected: %+v", st)
			}
		}
		checked++
	}
	if checked == 0 || types["/dev/shm"] != "tmpfs" {
		t.Skip("no tmpfs mount at /dev/shm to compare against")
	}
}

func mountTypes(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Skipf("mountinfo unavailable: %v", err)
	}
	types := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, tail := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 5 || len(tail) < 1 {
			continue
		}
		types[fields[4]] = tail[0] // the last mount at a point wins, as in the kernel's view
	}
	return types
}
