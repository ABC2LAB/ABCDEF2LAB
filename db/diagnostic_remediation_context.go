package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/remediation"
)

// 실제 호스트가 별도 trusted 필드로 보존한 HTTP projection만 사용한다.
// tool_output·model summary를 관측 사실로 재해석하지 않는다.
func diagnosticObservedContext(in *remediation.Input, c DiagnosticCandidate) error {
	var evidence struct {
		Related []json.RawMessage `json:"related_calls"`
		Trusted struct {
			Call e2proxy.CallResult `json:"call"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(c.Evidence), &evidence) != nil {
		return ErrDiagnosticRemediation
	}
	for _, raw := range evidence.Related {
		var nested struct {
			Trusted struct {
				Call            e2proxy.CallResult `json:"call"`
				OutputTruncated bool               `json:"output_truncated"`
			} `json:"trusted"`
		}
		if json.Unmarshal(raw, &nested) != nil {
			return ErrDiagnosticRemediation
		}
		other := c
		other.Evidence = string(raw)
		other.CallRef = nested.Trusted.Call.CallRef
		other.OutputTruncated = nested.Trusted.OutputTruncated
		if err := diagnosticObservedContext(in, other); err != nil {
			return err
		}
	}
	call := evidence.Trusted.Call
	if len(call.Observations) > 32 {
		return ErrDiagnosticRemediation
	}
	in.Context.ObservationsTruncated = in.Context.ObservationsTruncated || call.ObservationsTruncated || c.OutputTruncated
	var previous uint64
	for _, o := range call.Observations {
		if o.Sequence == 0 || o.Sequence <= previous || o.AttemptRef == "" || o.ObservedAt.IsZero() || len(o.Technologies) > 2 {
			return ErrDiagnosticRemediation
		}
		previous = o.Sequence
		step := fmt.Sprintf("attempt-%d", o.Sequence)
		ref := "candidate-call-" + c.CallRef + "-" + step
		component := "route-" + o.RuleRef
		in.Context.Observations = append(in.Context.Observations, remediation.ContextObservation{Method: o.Method, Component: component, ComponentRole: "unknown", Endpoint: o.Method + " " + o.Endpoint, Observation: o.Summary(), Source: "observed", EvidenceRef: ref, ObservedAt: o.ObservedAt.Format(time.RFC3339Nano), RunID: c.RunRef, CallID: c.CallRef, StepID: step, Truncated: c.OutputTruncated})
		for _, tech := range o.Technologies {
			kind, role := "server", "unknown"
			if tech.Header == "X-Powered-By" {
				kind, role = "framework", "unknown"
			}
			// 관측된 헤더의 주장일 뿐 해당 컴포넌트의 언어/프레임워크 확정이 아니다.
			addDiagnosticTechnology(in, remediation.Technology{Kind: kind, Name: tech.Name, Version: tech.Version, Component: component, ComponentRole: role, Source: "observed", Confidence: "inferred", EvidenceRef: ref, ObservedAt: o.ObservedAt.Format(time.RFC3339Nano)})
		}
	}
	sort.SliceStable(in.Context.Observations, func(i, j int) bool {
		a, _ := strconv.ParseUint(strings.TrimPrefix(in.Context.Observations[i].StepID, "attempt-"), 10, 64)
		b, _ := strconv.ParseUint(strings.TrimPrefix(in.Context.Observations[j].StepID, "attempt-"), 10, 64)
		return a < b
	})
	if len(in.Context.Observations) > 32 {
		in.Context.Observations = in.Context.Observations[:32]
		in.Context.ObservationsTruncated = true
	}
	return nil
}

// anchor와 finding의 명시 자산 ID만 사용한다. 시간·URL 유사성이나 다른 task의 자산은 합치지 않는다.
func diagnosticLinkedTechnologies(ctx context.Context, q DiagnosticRemediationQueryer, taskID, anchorID int64, ids []int64, in *remediation.Input, lock bool) (string, error) {
	if len(ids) > 128 {
		return "", ErrDiagnosticRemediation
	}
	suffix := ""
	if lock {
		// task는 호출자가 이미 잠갔다. anchor owner의 FK 삽입과 기존 anchor 삭제도 고정한다.
		if anchorID > 0 {
			var owner int64
			if err := q.QueryRowContext(ctx, `SELECT n.id FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.id=$2 FOR UPDATE OF n`, taskID, anchorID).Scan(&owner); err != nil {
				return "", err
			}
			anchors, err := q.QueryContext(ctx, `SELECT asset_id FROM exploration_anchors WHERE node_id=$1 FOR SHARE`, anchorID)
			if err != nil {
				return "", err
			}
			for anchors.Next() {
				var id int64
				if err := anchors.Scan(&id); err != nil {
					anchors.Close()
					return "", err
				}
			}
			err = anchors.Err()
			anchors.Close()
			if err != nil {
				return "", err
			}
		}
		suffix = " FOR SHARE OF a,l"
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id,array_to_json(a.technologies)::text,a.last_seen
 FROM assets a JOIN task_asset_links l ON l.asset_id=a.id AND l.task_id=$1
 WHERE a.type='service' AND (a.id=ANY($3::bigint[]) OR EXISTS(SELECT 1 FROM exploration_anchors x JOIN exploration_nodes n ON n.id=x.node_id JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND x.node_id=$2 AND x.asset_id=a.id))
 ORDER BY a.id LIMIT 129`+suffix, taskID, anchorID, ids)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var fingerprints []any
	count := 0
	for rows.Next() {
		count++
		if count > 128 {
			return "", ErrDiagnosticRemediation
		}
		var id int64
		var raw string
		var seen time.Time
		if err := rows.Scan(&id, &raw, &seen); err != nil {
			return "", err
		}
		if len(raw) > 16<<10 {
			return "", ErrDiagnosticRemediation
		}
		var names []string
		if json.Unmarshal([]byte(raw), &names) != nil {
			return "", ErrDiagnosticRemediation
		}
		fingerprints = append(fingerprints, []any{id, json.RawMessage(raw), seen})
		for _, name := range names {
			addDiagnosticTechnology(in, remediation.Technology{Kind: "server", Name: name, Version: "unknown", Component: "asset-" + strconv.FormatInt(id, 10), ComponentRole: "unknown", Source: "inferred", Confidence: "unknown", EvidenceRef: "asset-" + strconv.FormatInt(id, 10), ObservedAt: seen.UTC().Format(time.RFC3339Nano)})

		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return diagnosticSourceHash(fingerprints), nil
}

// 반복된 제품 흔적은 첫 근거에 연결한다. 모든 요청 단계의 관측 참조는 따로 보존한다.
func addDiagnosticTechnology(in *remediation.Input, t remediation.Technology) {
	for _, old := range in.Technologies {
		if old.Kind == t.Kind && old.Name == t.Name && old.Version == t.Version && old.Source == t.Source && old.Component == t.Component && old.ComponentRole == t.ComponentRole {
			return
		}
	}
	if len(in.Technologies) >= 32 {
		in.Context.ObservationsTruncated = true
		return
	}
	in.Technologies = append(in.Technologies, t)
}
