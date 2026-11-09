// Package e2prompt는 원문 없이 평가에 전달할 E② 프롬프트 식별값을 정의한다.
package e2prompt

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/Autumn-27/artex/e2config"
)

const Format = "abc2lab.e2-prompt-projection/v1"
const Version = "e2-worker-prompt/v1"
const MaxObservations = 10000
const MaxDetailBytes = 2048

var ErrInvalid = errors.New("e2_prompt_projection_invalid")
var hash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var ref = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var worker = regexp.MustCompile(`^work#[1-9][0-9]{0,5}$`)

func ValidRun(s string) bool    { return ref.MatchString(s) }
func ValidWorker(s string) bool { return worker.MatchString(s) }

type Identity struct {
	Version        string `json:"version"`
	Key            string `json:"key"`
	Source         string `json:"source"`
	TemplateDigest string `json:"template_digest"`
	GuidanceDigest string `json:"guidance_digest"`
	ControlDigest  string `json:"control_digest"`
	ToolsDigest    string `json:"tools_digest"`
}

// Parse는 DB Detail의 닫힌 원본 형식을 검사하고 system_digest를 투영에서 제외한다.
func Parse(raw []byte) (Identity, error) {
	var full struct {
		Identity
		SystemDigest string `json:"system_digest"`
	}
	var fields map[string]json.RawMessage
	if len(raw) == 0 || len(raw) > MaxDetailBytes || e2config.StrictJSON(raw, &full) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 8 {
		return Identity{}, ErrInvalid
	}
	for _, k := range []string{"version", "key", "source", "template_digest", "guidance_digest", "control_digest", "tools_digest", "system_digest"} {
		if _, ok := fields[k]; !ok {
			return Identity{}, ErrInvalid
		}
	}
	if full.Version != Version || full.Key != "e2_worker" || (full.Source != "builtin" && full.Source != "override" && full.Source != "builtin_invalid_override") {
		return Identity{}, ErrInvalid
	}
	for _, v := range []string{full.TemplateDigest, full.GuidanceDigest, full.ControlDigest, full.ToolsDigest, full.SystemDigest} {
		if !hash.MatchString(v) {
			return Identity{}, ErrInvalid
		}
	}
	return full.Identity, nil
}

type Observation struct {
	Sequence   int64  `json:"sequence"`
	ActivityID int64  `json:"activity_id"`
	IntentID   int64  `json:"intent_id"`
	Worker     string `json:"worker"`
	Identity
}
type Projection struct {
	Format           string        `json:"format"`
	TaskID           int64         `json:"task_id"`
	ExplorationID    int64         `json:"exploration_id"`
	RunRef           string        `json:"run_ref"`
	State            string        `json:"state"`
	Complete         bool          `json:"complete"`
	ExpectedCount    int64         `json:"expected_count"`
	ObservedCount    int64         `json:"observed_count"`
	SnapshotSequence int64         `json:"snapshot_sequence"`
	Observations     []Observation `json:"observations"`
}

// Finalize는 수집이 완전한 경우에도 상충·0건을 비교 성공으로 바꾸지 않는다.
func (p *Projection) Finalize(sealed, failed bool) {
	p.Complete = false
	if p.State == "invalid" {
		p.Observations = []Observation{}
		return
	}
	if !sealed {
		p.State = "collecting"
		return
	}
	if failed || p.ExpectedCount != p.ObservedCount || int64(len(p.Observations)) != p.ExpectedCount {
		p.State = "incomplete"
		return
	}
	if p.ExpectedCount == 0 {
		p.State = "empty"
		return
	}
	baseline, _ := json.Marshal(p.Observations[0].Identity)
	for _, o := range p.Observations[1:] {
		b, _ := json.Marshal(o.Identity)
		if !bytes.Equal(b, baseline) {
			p.State = "conflict"
			return
		}
	}
	p.State = "complete"
	p.Complete = true
}
