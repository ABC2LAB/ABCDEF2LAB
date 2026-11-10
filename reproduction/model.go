// Package reproduction은 저장된 관측을 재현 자료로 투영한다. 요청이나 재현을 실행하지 않는다.
package reproduction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/remediation"
)

const Version = "diagnostic-reproduction/v1"
const MissingScreenshot = "스크린샷 미확보 — 실제 요청·응답 및 실행 기록으로 증거 제공"

type Capture struct {
	Status     string    `json:"status"`
	Ref        string    `json:"ref,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	CapturedAt time.Time `json:"captured_at,omitempty"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	PNG        []byte    `json:"-"`
}
type Step struct {
	TaskID          int64    `json:"task_id"`
	EntryID         string   `json:"entry_id"`
	ID              string   `json:"id"`
	Number          int      `json:"number"`
	Title           string   `json:"title"`
	SourceStepID    string   `json:"source_step_id"`
	RunRef          string   `json:"run_ref"`
	CallRef         string   `json:"call_ref"`
	EvidenceRef     string   `json:"evidence_ref"`
	EvidenceVersion int64    `json:"evidence_version"`
	EvidenceDigest  string   `json:"evidence_digest"`
	BindingDigest   string   `json:"binding_digest"`
	ObservedAt      string   `json:"observed_at"`
	Method          string   `json:"method"`
	Path            string   `json:"path"`
	Prerequisites   string   `json:"prerequisites"`
	Observation     string   `json:"observation"`
	DependsOn       []string `json:"depends_on,omitempty"`
	Truncated       bool     `json:"truncated"`
	Capture         Capture  `json:"capture"`
}

func FromStored(task int64, entry string, version int64, digest string, context remediation.Context) []Step {
	out := []Step{}
	if task <= 0 || version <= 0 || len(digest) != 64 {
		return out
	}
	if _, e := hex.DecodeString(digest); e != nil {
		return out
	}
	context = remediation.NormalizeContext(context)
	seen := map[string]bool{}
	for _, o := range context.Observations {
		if o.Source != "observed" || o.RunID == "" || o.StepID == "" || o.EvidenceRef == "" {
			continue
		}
		if _, e := time.Parse(time.RFC3339Nano, o.ObservedAt); e != nil {
			continue
		}
		raw, _ := json.Marshal([]any{Version, task, entry, version, digest, o})
		sum := sha256.Sum256(raw)
		binding := hex.EncodeToString(sum[:])
		if seen[binding] {
			continue
		}
		seen[binding] = true
		method, path := o.Method, o.Endpoint
		parts := strings.SplitN(path, " ", 2)
		if len(parts) == 2 {
			method, path = parts[0], parts[1]
		}
		if !strings.HasPrefix(path, "/") {
			path = ""
		}
		n := len(out) + 1
		out = append(out, Step{TaskID: task, EntryID: entry, ID: "step-" + binding[:24], Number: n, Title: "저장된 실행 관찰 " + strconv.Itoa(n), SourceStepID: o.StepID, RunRef: o.RunID, CallRef: o.CallID, EvidenceRef: o.EvidenceRef, EvidenceVersion: version, EvidenceDigest: digest, BindingDigest: binding, ObservedAt: o.ObservedAt, Method: method, Path: path, Prerequisites: o.Prerequisites, Observation: o.Observation, DependsOn: append([]string{}, o.DependsOn...), Truncated: o.Truncated, Capture: Capture{Status: "not_captured"}})
	}
	return out
}
func Validate(step Step, version int64, digest string) error {
	if step.TaskID <= 0 || step.EntryID == "" || version < 1 || len(digest) != 64 || step.Number < 1 || step.Number > 32 || step.EvidenceVersion != version || step.EvidenceDigest != digest || len(step.BindingDigest) != 64 || step.ID != "step-"+step.BindingDigest[:24] || step.RunRef == "" || step.EvidenceRef == "" || step.SourceStepID == "" {
		return fmt.Errorf("reproduction_binding_invalid")
	}
	if _, e := hex.DecodeString(step.BindingDigest); e != nil {
		return fmt.Errorf("reproduction_binding_invalid")
	}
	if _, e := hex.DecodeString(digest); e != nil {
		return fmt.Errorf("reproduction_binding_invalid")
	}
	if _, e := time.Parse(time.RFC3339Nano, step.ObservedAt); e != nil {
		return fmt.Errorf("reproduction_binding_invalid")
	}
	return nil
}
