package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
)

// The runner measures run-scoped tmpfs from outside the engine: this role runs
// in its own container that shares the measured container's PID namespace and
// reads /proc/1/root/<mount>. Nothing inside the engine (a binary or PATH that
// model-driven tools could replace) produces the numbers.
const (
	maxStatfsPlanBytes = 4 << 10
	maxStatfsPaths     = 4
	maxStatfsPathBytes = 256
)

type statfsPlan struct {
	Paths []string `json:"paths"`
}

type fsUsage struct {
	Path           string `json:"path"`
	SizeBytes      uint64 `json:"size_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type statfsReport struct {
	Results []fsUsage `json:"results"`
}

// fsStat is one statfs answer. Whether it is tmpfs comes from the kernel's
// filesystem type, not from a mount or device name.
type fsStat struct {
	tmpfs     bool
	blocks    uint64
	free      uint64
	avail     uint64
	blockSize uint64
}

// statPath is the platform statfs; tests replace it.
var statPath = statFilesystem

func runStatfs(stdin io.Reader, stdout, stderr io.Writer) int {
	plan, ok := readStatfsPlan(stdin)
	if !ok {
		return fail(stderr, invalidInput)
	}
	report := statfsReport{Results: make([]fsUsage, 0, len(plan.Paths))}
	for _, path := range plan.Paths {
		st, err := statPath(path)
		if err != nil {
			return fail(stderr, statfsFailed)
		}
		if !st.tmpfs {
			return fail(stderr, statfsNotTmpfs)
		}
		usage, ok := usageOf(path, st)
		if !ok {
			return fail(stderr, statfsFailed)
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

// usageOf rejects answers that cannot describe a real filesystem instead of
// reporting numbers the runner would then trust.
func usageOf(path string, st fsStat) (fsUsage, bool) {
	if st.blockSize == 0 || st.blocks == 0 || st.free > st.blocks || st.avail > st.free ||
		st.blocks > math.MaxUint64/st.blockSize {
		return fsUsage{}, false
	}
	return fsUsage{Path: path, SizeBytes: st.blocks * st.blockSize, UsedBytes: (st.blocks - st.free) * st.blockSize,
		AvailableBytes: st.avail * st.blockSize}, true
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
		if len(path) > maxStatfsPathBytes || !filepath.IsAbs(path) || filepath.Clean(path) != path || seen[path] {
			return plan, false
		}
		seen[path] = true
	}
	return plan, true
}
