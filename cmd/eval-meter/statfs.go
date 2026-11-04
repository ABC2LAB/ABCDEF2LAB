package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The runner measures run-scoped tmpfs from outside the engine: this role runs
// in its own container that shares the measured container's PID namespace and
// reads that container's view through PID 1 (/proc/1/root, /proc/1/mountinfo).
// Nothing inside the engine (a binary or PATH that model-driven tools could
// replace) produces the numbers.
//
// A path alone does not identify the run's tmpfs: a renamed parent or a symlink
// can put another filesystem (for example /dev/shm) at the same path. Each
// measurement is therefore bound to the mount the target's mountinfo lists at
// that exact mount point, and the reported mount ID lets the runner require the
// same mount object before and after the window.
const (
	maxStatfsPlanBytes = 4 << 10
	maxStatfsPaths     = 4
	maxStatfsPathBytes = 256
	// Upper bound on the mount table read through /proc; a container has far fewer entries.
	maxMountinfoBytes = 1 << 20
)

type statfsPlan struct {
	Paths []string `json:"paths"`
}

type fsUsage struct {
	Path           string `json:"path"`
	MountID        uint64 `json:"mount_id"`
	SizeBytes      uint64 `json:"size_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type statfsReport struct {
	Results []fsUsage `json:"results"`
}

// fsStat is one statfs answer about an opened object. Whether it is tmpfs comes
// from the kernel's filesystem type, not from a mount or device name.
type fsStat struct {
	tmpfs     bool
	blocks    uint64
	free      uint64
	avail     uint64
	blockSize uint64
	mountID   uint64
}

type mountEntry struct {
	id     uint64
	point  string
	fstype string
}

var errNoMountID = errors.New("statfs: kernel did not report a mount id")

// Seams for tests. In the role they always name the target's PID 1.
var (
	targetRoot  = "/proc/1/root"
	readMounts  = func() ([]mountEntry, error) { return readMountinfo("/proc/1/mountinfo") }
	statMounted = statOpened
)

func runStatfs(stdin io.Reader, stdout, stderr io.Writer) int {
	plan, ok := readStatfsPlan(stdin)
	if !ok {
		return fail(stderr, invalidInput)
	}
	report := statfsReport{Results: make([]fsUsage, 0, len(plan.Paths))}
	for _, mount := range plan.Paths {
		usage, code := measureMount(mount)
		if code != "" {
			return fail(stderr, code)
		}
		report.Results = append(report.Results, usage)
	}
	out, err := json.Marshal(report)
	if err != nil {
		return fail(stderr, probeOutputFailed)
	}
	if _, err := fmt.Fprintln(stdout, string(out)); err != nil {
		return fail(stderr, probeOutputFailed)
	}
	return 0
}

// measureMount reads the tmpfs mounted at exactly mount (the target's own path)
// and refuses anything that is not that mount object.
func measureMount(mount string) (fsUsage, string) {
	mounts, err := readMounts()
	if err != nil {
		return fsUsage{}, statfsFailed
	}
	var entry *mountEntry
	for i := range mounts { // the last entry at a point is the one the path resolves to
		if mounts[i].point == mount {
			entry = &mounts[i]
		}
	}
	if entry == nil {
		return fsUsage{}, statfsNotMounted
	}
	if entry.fstype != "tmpfs" {
		return fsUsage{}, statfsNotTmpfs
	}
	st, err := statMounted(targetRoot + mount)
	if err != nil {
		return fsUsage{}, statfsFailed
	}
	if !st.tmpfs {
		return fsUsage{}, statfsNotTmpfs
	}
	if st.mountID != entry.id {
		return fsUsage{}, statfsMountMismatch
	}
	usage, ok := usageOf(mount, st)
	if !ok {
		return fsUsage{}, statfsFailed
	}
	return usage, ""
}

// usageOf rejects answers that cannot describe a real filesystem instead of
// reporting numbers the runner would then trust.
func usageOf(path string, st fsStat) (fsUsage, bool) {
	if st.blockSize == 0 || st.blocks == 0 || st.free > st.blocks || st.avail > st.free || st.mountID == 0 ||
		st.blocks > math.MaxUint64/st.blockSize {
		return fsUsage{}, false
	}
	return fsUsage{Path: path, MountID: st.mountID, SizeBytes: st.blocks * st.blockSize,
		UsedBytes: (st.blocks - st.free) * st.blockSize, AvailableBytes: st.avail * st.blockSize}, true
}

func readMountinfo(path string) ([]mountEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMountinfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMountinfoBytes {
		return nil, errors.New("mountinfo: too large")
	}
	return parseMountinfo(string(data))
}

// parseMountinfo reads proc(5) mountinfo: "ID PARENT MAJ:MIN ROOT POINT OPTS [OPTIONAL...] - FSTYPE SOURCE SUPER".
// A line it cannot read fails the whole table rather than being skipped.
func parseMountinfo(text string) ([]mountEntry, error) {
	var mounts []mountEntry
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, tail := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 6 || len(tail) < 1 {
			return nil, errors.New("mountinfo: malformed line")
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || id == 0 {
			return nil, errors.New("mountinfo: invalid mount id")
		}
		point, err := unescapeMountPath(fields[4])
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mountEntry{id: id, point: point, fstype: tail[0]})
	}
	if len(mounts) == 0 {
		return nil, errors.New("mountinfo: empty")
	}
	return mounts, nil
}

// unescapeMountPath undoes the kernel's octal escapes (\040 space, \011 tab, \012 newline, \134 backslash).
func unescapeMountPath(field string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] != '\\' {
			out.WriteByte(field[i])
			continue
		}
		if i+4 > len(field) {
			return "", errors.New("mountinfo: truncated escape")
		}
		value, err := strconv.ParseUint(field[i+1:i+4], 8, 8)
		if err != nil {
			return "", errors.New("mountinfo: invalid escape")
		}
		out.WriteByte(byte(value))
		i += 3
	}
	return out.String(), nil
}

func readStatfsPlan(stdin io.Reader) (statfsPlan, bool) {
	var plan statfsPlan
	body, err := io.ReadAll(io.LimitReader(stdin, maxStatfsPlanBytes+1))
	if err != nil || len(body) > maxStatfsPlanBytes {
		return plan, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil || decoder.More() {
		return plan, false
	}
	if len(plan.Paths) == 0 || len(plan.Paths) > maxStatfsPaths {
		return plan, false
	}
	seen := make(map[string]bool, len(plan.Paths))
	for _, path := range plan.Paths {
		if len(path) > maxStatfsPathBytes || path == "/" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
			seen[path] {
			return plan, false
		}
		seen[path] = true
	}
	return plan, true
}
