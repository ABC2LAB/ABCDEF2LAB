package db

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 계약 사례 ID는 설명용 합성 값이다(kg-query-v1 §7). 실제 진단 결과가 아니다.

func kgTasks(ids ...int64) []KGContextTask {
	out := make([]KGContextTask, 0, len(ids))
	for _, id := range ids {
		out = append(out, KGContextTask{ID: id, Exploration: id + 1000})
	}
	return out
}

func kgEndpoint(id int64) kgAssetRow { return kgAssetRow{ID: id, Type: "endpoint"} }

func kgBuild(t *testing.T, in kgRows) *KGGraph {
	t.Helper()
	g, err := buildKGGraph(in)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// kgJSON은 응답을 일반 JSON 값으로 바꾼다 — 칸 이름·null·생략을 계약 원문과 그대로 비교한다.
func kgJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func kgSame(t *testing.T, got *KGGraph, want string) {
	t.Helper()
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if g := kgJSON(t, got); !reflect.DeepEqual(g, w) {
		raw, _ := json.Marshal(got)
		t.Fatalf("계약 사례와 다름:\n got %s\nwant %s", raw, want)
	}
}

// §5.1 E01 전체 응답. 다른 사례는 E01의 최상위 칸만 바꾼다(§7).
const kgE01 = `{
  "version": "abc2lab.kg-query/v1",
  "task_id": "41",
  "scope": {"kind": "task_context", "task_ids": ["41"], "source_depth": 1, "execution_scope": null},
  "sections": {"assets": "complete", "provenance": "complete", "ownership": "not_requested", "diagnosis": "not_requested", "policy_source": "not_requested"},
  "policy_source": null,
  "nodes": [
    {"id": "t:41", "kind": "task", "task_id": "41", "label": "작업 #41", "role": "current"},
    {"id": "a:101", "kind": "asset", "asset_id": "101", "asset_type": "endpoint", "label": "엔드포인트 #101", "context_state": "included", "observation_state": "unknown"}
  ],
  "edges": [
    {"id": "task_asset:t:41:a:101:attached", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "attached", "state": "stored", "provenance_refs": ["link:41:101"]}
  ],
  "provenance": [
    {"id": "link:41:101", "kind": "task_asset_link", "task_id": "41", "asset_id": "101", "source": "system", "inherited": false, "observation": null}
  ],
  "issues": [],
  "page": {"mode": "complete", "has_more": false, "next_cursor": null}
}`

func kgE01With(t *testing.T, patch string) string {
	t.Helper()
	var base, p map[string]any
	if err := json.Unmarshal([]byte(kgE01), &base); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(patch), &p); err != nil {
		t.Fatal(err)
	}
	for k, v := range p {
		base[k] = v
	}
	raw, _ := json.Marshal(base)
	return string(raw)
}

func TestKGContractCases(t *testing.T) {
	t.Run("E01 작업 연결", func(t *testing.T) {
		kgSame(t, kgBuild(t, kgRows{Tasks: kgTasks(41), Arrays: []kgPair{{41, 101}},
			Links: []kgLinkRow{{Task: 41, Asset: 101, Source: "system"}}, Assets: []kgAssetRow{kgEndpoint(101)}}), kgE01)
	})
	t.Run("E02 수동 등록", func(t *testing.T) {
		kgSame(t, kgBuild(t, kgRows{Tasks: kgTasks(41), Arrays: []kgPair{{41, 101}},
			Links: []kgLinkRow{{Task: 41, Asset: 101, Source: "manual"}}, Assets: []kgAssetRow{kgEndpoint(101)}}),
			kgE01With(t, `{"provenance": [{"id": "link:41:101", "kind": "task_asset_link", "task_id": "41", "asset_id": "101", "source": "manual", "inherited": false, "observation": null}]}`))
	})
	t.Run("E03 fact 관측 참조 — 모델이 observed라 해도 자산은 unknown", func(t *testing.T) {
		kgSame(t, kgBuild(t, kgRows{Tasks: kgTasks(41), Arrays: []kgPair{{41, 101}},
			Links:   []kgLinkRow{{Task: 41, Asset: 101, Source: "agent"}},
			Anchors: []kgAnchorRow{{Node: kgNodeRow{ID: 901, Exploration: 1041, Kind: "fact", State: "confirmed", Confidence: "observed"}, Asset: 101}},
			Assets:  []kgAssetRow{kgEndpoint(101)}}),
			kgE01With(t, `{
  "edges": [
    {"id": "task_asset:t:41:a:101:anchor", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "anchor", "state": "stored", "provenance_refs": ["anchor:901:101"]},
    {"id": "task_asset:t:41:a:101:attached", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "attached", "state": "stored", "provenance_refs": ["link:41:101"]}
  ],
  "provenance": [
    {"id": "anchor:901:101", "kind": "exploration_anchor", "task_id": "41", "asset_id": "101", "source": null, "inherited": false, "observation": {"node_id": "901", "kind": "fact", "source_state": "confirmed", "claimed_confidence": "observed", "authority": "model_claim"}},
    {"id": "link:41:101", "kind": "task_asset_link", "task_id": "41", "asset_id": "101", "source": "agent", "inherited": false, "observation": null}
  ]}`))
	})
	t.Run("E04 직접 원천 상속 — 현재 작업 연결을 위조하지 않음", func(t *testing.T) {
		kgSame(t, kgBuild(t, kgRows{Tasks: kgTasks(41, 40), Arrays: []kgPair{{40, 101}},
			Links: []kgLinkRow{{Task: 40, Asset: 101, Source: "manual"}}, Assets: []kgAssetRow{kgEndpoint(101)}}),
			kgE01With(t, `{
  "scope": {"kind": "task_context", "task_ids": ["41", "40"], "source_depth": 1, "execution_scope": null},
  "nodes": [
    {"id": "t:41", "kind": "task", "task_id": "41", "label": "작업 #41", "role": "current"},
    {"id": "t:40", "kind": "task", "task_id": "40", "label": "작업 #40", "role": "source"},
    {"id": "a:101", "kind": "asset", "asset_id": "101", "asset_type": "endpoint", "label": "엔드포인트 #101", "context_state": "included", "observation_state": "unknown"}
  ],
  "edges": [
    {"id": "source_task:t:41:t:40", "kind": "source_task", "src": "t:41", "dst": "t:40", "state": "stored", "provenance_refs": ["relation:41:40"]},
    {"id": "task_asset:t:40:a:101:attached", "kind": "task_asset", "src": "t:40", "dst": "a:101", "basis": "attached", "state": "stored", "provenance_refs": ["link:40:101"]}
  ],
  "provenance": [
    {"id": "link:40:101", "kind": "task_asset_link", "task_id": "40", "asset_id": "101", "source": "manual", "inherited": true, "observation": null},
    {"id": "relation:41:40", "kind": "task_relation", "task_id": "41", "source_task_id": "40", "asset_id": null, "source": null, "inherited": false, "observation": null}
  ]}`))
	})
	t.Run("E05 범위 자산", func(t *testing.T) {
		kgSame(t, kgBuild(t, kgRows{Tasks: kgTasks(41), ScopeMatches: []kgScopeMatchRow{{Scope: 20, Task: 41, Asset: 101, Source: "manual"}},
			Assets: []kgAssetRow{kgEndpoint(101)}}),
			kgE01With(t, `{
  "edges": [{"id": "task_asset:t:41:a:101:scope", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "scope", "state": "derived", "provenance_refs": ["scope:20:101"]}],
  "provenance": [{"id": "scope:20:101", "kind": "task_scope", "scope_id": "20", "task_id": "41", "asset_id": "101", "source": "manual", "inherited": false, "observation": null}]}`))
	})
	t.Run("정상 빈 결과는 현재 작업 노드만", func(t *testing.T) {
		kgSame(t, kgBuild(t, kgRows{Tasks: kgTasks(41)}),
			kgE01With(t, `{"nodes": [{"id": "t:41", "kind": "task", "task_id": "41", "label": "작업 #41", "role": "current"}], "edges": [], "provenance": []}`))
	})
}

func kgIssueCodes(g *KGGraph) []string {
	var out []string
	for _, x := range g.Issues {
		out = append(out, x.Code+"|"+x.State+"|"+strings.Join(x.Refs, ","))
	}
	return out
}

// Q01·Q04: 연결 출처를 보존하고 배열·연결 행 불일치를 구분한다. 미등록 source 원문은 내보내지 않는다.
func TestKGMembershipAndSources(t *testing.T) {
	g := kgBuild(t, kgRows{
		Tasks:      kgTasks(41),
		Arrays:     []kgPair{{41, 1}, {41, 2}, {41, 3}},
		Links:      []kgLinkRow{{Task: 41, Asset: 1, Source: "api"}, {Task: 41, Asset: 3, Source: "synthetic-private-source"}},
		Mismatches: []kgPair{{41, 2}, {41, 9}}, // 9는 포함되지 않은 자산 — 드러내지 않는다
		Assets:     []kgAssetRow{kgEndpoint(1), kgEndpoint(2), kgEndpoint(3), kgEndpoint(9)},
	})
	want := []string{
		"legacy_link_missing|insufficient|array:41:2",
		"membership_mismatch|conflict|a:2,t:41",
		"unknown_source|insufficient|link:41:3",
	}
	if got := kgIssueCodes(g); !reflect.DeepEqual(got, want) {
		t.Fatalf("issues=%v", got)
	}
	raw, _ := json.Marshal(g)
	if strings.Contains(string(raw), "synthetic-private-source") || strings.Contains(string(raw), "a:9") {
		t.Fatal("미등록 원문이나 포함되지 않은 자산이 응답에 남음")
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("nodes=%d", len(g.Nodes))
	}
}

// Q03·Q04·Q06: source_node는 같은 작업 탐색·공개 상태·실제 앵커가 있을 때만 관측 참조다.
func TestKGSourceNodeVerification(t *testing.T) {
	node := func(id, exploration int64, kind, state string) kgNodeRow {
		return kgNodeRow{ID: id, Exploration: exploration, Kind: kind, State: state, Confidence: "observed"}
	}
	n := func(v int64) *int64 { return &v }
	for name, tc := range map[string]struct {
		task  int64
		row   *kgSourceNodeRow
		valid bool
	}{
		"same task anchored fact":        {41, &kgSourceNodeRow{Node: node(7, 1041, "fact", "confirmed"), Anchored: true}, true},
		"not anchored":                   {41, &kgSourceNodeRow{Node: node(7, 1041, "fact", "confirmed")}, false},
		"foreign exploration":            {41, &kgSourceNodeRow{Node: node(7, 9999, "fact", "confirmed"), Anchored: true}, false},
		"hint kind":                      {41, &kgSourceNodeRow{Node: node(7, 1041, "hint", "active"), Anchored: true}, false},
		"current deleted intent":         {41, &kgSourceNodeRow{Node: node(7, 1041, "intent", "deleted"), Anchored: true}, false},
		"current running intent":         {41, &kgSourceNodeRow{Node: node(7, 1041, "intent", "running"), Anchored: true}, true},
		"inherited running intent":       {40, &kgSourceNodeRow{Node: node(7, 1040, "intent", "running"), Anchored: true}, false},
		"inherited done intent":          {40, &kgSourceNodeRow{Node: node(7, 1040, "intent", "done"), Anchored: true}, true},
		"node missing (deleted/foreign)": {41, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			in := kgRows{Tasks: kgTasks(41, 40), Arrays: []kgPair{{tc.task, 1}},
				Links: []kgLinkRow{{Task: tc.task, Asset: 1, Source: "agent", SourceNode: n(7)}}, Assets: []kgAssetRow{kgEndpoint(1)}}
			if tc.row != nil {
				tc.row.Link = kgPair{tc.task, 1}
				in.SourceNodes = []kgSourceNodeRow{*tc.row}
			}
			g := kgBuild(t, in)
			var link KGProvenance
			for _, p := range g.Provenance {
				if p.Kind == "task_asset_link" {
					link = p
				}
			}
			if (link.Observation != nil) != tc.valid {
				t.Fatalf("observation=%v", link.Observation)
			}
			unverified := len(g.Issues) == 1 && g.Issues[0].Code == kgIssueSourceUnverified && g.Issues[0].Refs[0] == link.ID
			if unverified == tc.valid {
				t.Fatalf("issues=%v", kgIssueCodes(g))
			}
			raw, _ := json.Marshal(g)
			if !tc.valid && strings.Contains(string(raw), `"node_id":"7"`) {
				t.Fatal("검증 안 된 노드 ID가 응답에 남음")
			}
			if link.Observation != nil && (link.Observation.Authority != "model_claim" || link.Observation.ClaimedConfidence != "observed") && link.Observation.Kind == "fact" {
				t.Fatal("fact 관측 권위 표기")
			}
		})
	}
}

// Q03: fact가 아닌 관측은 confidence를 읽지 않는다. 미등록 confidence는 unknown.
func TestKGObservationAuthority(t *testing.T) {
	for _, tc := range []struct {
		node       kgNodeRow
		conf, auth string
	}{
		{kgNodeRow{ID: 1, Kind: "fact", State: "confirmed", Confidence: "inferred"}, "inferred", "model_claim"},
		{kgNodeRow{ID: 1, Kind: "fact", State: "origin", Confidence: "certain"}, "unknown", "model_claim"},
		{kgNodeRow{ID: 1, Kind: "intent", State: "done", Confidence: "observed"}, "unknown", "unverified_reference"},
		{kgNodeRow{ID: 1, Kind: "finding", State: "confirmed"}, "unknown", "unverified_reference"},
	} {
		o := kgObservationOf(tc.node)
		if o.ClaimedConfidence != tc.conf || o.Authority != tc.auth || o.SourceState != tc.node.State {
			t.Fatalf("%+v → %+v", tc.node, o)
		}
	}
}

// Q08: 같은 자산의 여러 근거는 노드 하나, 관계는 (작업, basis)마다 하나, 출처는 모두 보존한다.
func TestKGMergesSameAsset(t *testing.T) {
	g := kgBuild(t, kgRows{
		Tasks:        kgTasks(41, 40),
		Arrays:       []kgPair{{41, 5}, {40, 5}},
		Links:        []kgLinkRow{{Task: 41, Asset: 5, Source: "manual"}, {Task: 40, Asset: 5, Source: "task"}},
		ScopeMatches: []kgScopeMatchRow{{Scope: 3, Task: 41, Asset: 5, Source: "auto"}, {Scope: 4, Task: 41, Asset: 5, Source: "agent"}},
		Anchors: []kgAnchorRow{
			{Node: kgNodeRow{ID: 80, Exploration: 1041, Kind: "finding", State: "confirmed"}, Asset: 5},
			{Node: kgNodeRow{ID: 81, Exploration: 1040, Kind: "intent", State: "running"}, Asset: 5}, // 원천의 진행 중 intent — 비공개
		},
		Assets: []kgAssetRow{kgEndpoint(5)},
	})
	if len(g.Nodes) != 3 {
		t.Fatalf("nodes=%d", len(g.Nodes))
	}
	var ids []string
	for _, e := range g.Edges {
		ids = append(ids, e.ID+"="+strings.Join(e.ProvenanceRefs, ","))
	}
	want := []string{
		"source_task:t:41:t:40=relation:41:40",
		"task_asset:t:40:a:5:attached=link:40:5",
		"task_asset:t:41:a:5:anchor=anchor:80:5",
		"task_asset:t:41:a:5:attached=link:41:5",
		"task_asset:t:41:a:5:scope=scope:3:5,scope:4:5",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("edges=%v", ids)
	}
	raw, _ := json.Marshal(g)
	if strings.Contains(string(raw), "anchor:81") {
		t.Fatal("원천의 끝나지 않은 intent가 공개됨")
	}
	again, _ := json.Marshal(kgBuild(t, kgRows{Tasks: kgTasks(41, 40), Arrays: []kgPair{{40, 5}, {41, 5}},
		Links:        []kgLinkRow{{Task: 40, Asset: 5, Source: "task"}, {Task: 41, Asset: 5, Source: "manual"}},
		ScopeMatches: []kgScopeMatchRow{{Scope: 4, Task: 41, Asset: 5, Source: "agent"}, {Scope: 3, Task: 41, Asset: 5, Source: "auto"}},
		Anchors:      []kgAnchorRow{{Node: kgNodeRow{ID: 80, Exploration: 1041, Kind: "finding", State: "confirmed"}, Asset: 5}},
		Assets:       []kgAssetRow{kgEndpoint(5)}}))
	if string(raw) != string(again) {
		t.Fatal("입력 순서가 바뀌면 출력이 달라짐")
	}
}

// Q09: 포함 관계는 유일 후보일 때만. 같은 host의 다른 port·scheme·경로 접두사로 부모를 고르지 않는다.
func TestKGContainment(t *testing.T) {
	scope := func(ids ...int64) []kgScopeMatchRow {
		var out []kgScopeMatchRow
		for _, id := range ids {
			out = append(out, kgScopeMatchRow{Scope: 1, Task: 41, Asset: id, Source: "manual"})
		}
		return out
	}
	assets := []kgAssetRow{
		{ID: 1, Type: "root_domain", Domain: "example.test"},
		{ID: 2, Type: "subdomain", Domain: "app.example.test", RootDomain: "example.test"},
		{ID: 3, Type: "service", Domain: "app.example.test", URL: "https://APP.example.test/", Kind: "http"},
		{ID: 4, Type: "endpoint", URL: "https://app.example.test:443/api/items?id=1"},
		{ID: 5, Type: "service", Domain: "app.example.test", URL: "http://app.example.test:8080", Kind: "http"},
		{ID: 6, Type: "endpoint", URL: "http://app.example.test:8080/x"},
		{ID: 7, Type: "service", URL: "https://app.example.test/base", Kind: "http"}, // 경로 있는 서비스 — 부모 아님
		{ID: 8, Type: "ip", IP: "192.0.2.10"},
		{ID: 9, Type: "service", IP: "192.0.2.10", URL: "http://192.0.2.10/", Kind: "http"},
		{ID: 10, Type: "service", IP: "192.0.2.10", URL: "https://192.0.2.10/", Kind: "other"},
		{ID: 11, Type: "endpoint", URL: "http://192.0.2.10/login"},
		{ID: 12, Type: "endpoint", URL: "ftp://app.example.test/file"},
		{ID: 13, Type: "endpoint", URL: "https://other.test/x"},
	}
	g := kgBuild(t, kgRows{Tasks: kgTasks(41), ScopeMatches: scope(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13), Assets: assets})
	got := map[string]string{}
	for _, e := range g.Edges {
		if e.Kind == "contained_by" {
			got[e.Src] = e.Dst + " " + e.ProvenanceRefs[0]
		}
	}
	want := map[string]string{
		"a:2":  "a:1 fields:2:1:root_domain",
		"a:4":  "a:3 fields:4:3:endpoint_origin",
		"a:6":  "a:5 fields:6:5:endpoint_origin",
		"a:11": "a:9 fields:11:9:endpoint_origin",
	}
	// 서비스 3·5는 하위 도메인 2의 자식(domain 일치), 9·10은 domain이 없어 IP 8의 자식. 7은 domain·IP가 없어 부모 없음.
	for _, s := range []string{"3", "5"} {
		want["a:"+s] = "a:2 fields:" + s + ":2:service_domain"
	}
	for _, s := range []string{"9", "10"} {
		want["a:"+s] = "a:8 fields:" + s + ":8:service_ip"
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contained_by=%v", got)
	}
	if len(g.Issues) != 0 {
		t.Fatalf("issues=%v", kgIssueCodes(g))
	}

	// 후보가 둘이면 고르지 않는다.
	g = kgBuild(t, kgRows{Tasks: kgTasks(41), ScopeMatches: scope(1, 2, 3), Assets: []kgAssetRow{
		{ID: 1, Type: "service", URL: "https://dup.test", Kind: "http"},
		{ID: 2, Type: "service", URL: "https://dup.test:443/", Kind: "http"},
		{ID: 3, Type: "endpoint", URL: "https://dup.test/a"},
	}})
	for _, e := range g.Edges {
		if e.Kind == "contained_by" {
			t.Fatalf("애매한 부모를 골랐음: %s", e.ID)
		}
	}
	if got := kgIssueCodes(g); !reflect.DeepEqual(got, []string{"containment_ambiguous|insufficient|a:3"}) {
		t.Fatalf("issues=%v", got)
	}
}

func TestKGOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Example.TEST/":        "https://example.test:443",
		"http://example.test":          "http://example.test:80",
		"HTTP://example.test:080/":     "http://example.test:80",
		"http://[2001:db8::1]:8080/":   "http://2001:db8::1:8080",
		"http://user:pw@example.test/": "",
		"https://example.test/?q=1":    "",
		"https://example.test/#f":      "",
		"https://example.test/a":       "",
		"https://example.test:/":       "",
		"https://example.test:0/":      "",
		"ftp://example.test/":          "",
		"https://exämple.test/":        "",
		"not a url":                    "",
	} {
		got, ok := kgOrigin(raw, true)
		if want == "" && ok || want != "" && got != want {
			t.Errorf("%q → %q,%v; want %q", raw, got, ok, want)
		}
	}
	if got, ok := kgOrigin("https://user@example.test/a?b#c", false); !ok || got != "https://example.test:443" {
		t.Fatalf("자식 URL origin: %q", got)
	}
}

// Q02: keyword 범위는 자산을 추정하지 않고 문제만 남긴다.
func TestKGKeywordScopeIssue(t *testing.T) {
	g := kgBuild(t, kgRows{Tasks: kgTasks(41, 40), KeywordTasks: []int64{40, 41}})
	if got := kgIssueCodes(g); !reflect.DeepEqual(got, []string{
		"scope_kind_not_projected|insufficient|t:40",
		"scope_kind_not_projected|insufficient|t:41",
	}) {
		t.Fatalf("issues=%v", got)
	}
}

// Q02: 범위 일치 조건은 contextCoverageCTE와 같은 글자다(scopeTargetCTE의 IP-literal 확장을 쓰지 않음).
func TestKGScopeConditionMatchesCoverageCTE(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(norm(contextCoverageCTE), norm(kgScopeMatchCond)) {
		t.Fatal("범위 일치 조건이 contextCoverageCTE와 달라짐")
	}
	if strings.Contains(kgScopeMatchCond, "try_inet(a.domain)") {
		t.Fatal("IP-literal domain 확장이 섞임")
	}
}

// Q13: 상한은 자르지 않고 실패한다. 큰 int64 ID도 정밀도 손실 없이 문자열이다.
func TestKGLimitsAndIDs(t *testing.T) {
	big := int64(9223372036854775807)
	g := kgBuild(t, kgRows{Tasks: []KGContextTask{{ID: big, Exploration: 1}}, Arrays: []kgPair{{big, big - 1}},
		Links: []kgLinkRow{{Task: big, Asset: big - 1, Source: "system"}}, Assets: []kgAssetRow{kgEndpoint(big - 1)}})
	if g.TaskID != "9223372036854775807" || g.Provenance[0].ID != "link:9223372036854775807:9223372036854775806" {
		t.Fatalf("큰 ID: %s %s", g.TaskID, g.Provenance[0].ID)
	}

	edgesAt := func(n int) kgRows {
		in := kgRows{Tasks: kgTasks(41)}
		for i := 1; i <= n; i++ {
			in.Arrays = append(in.Arrays, kgPair{41, int64(i)})
			in.Links = append(in.Links, kgLinkRow{Task: 41, Asset: int64(i), Source: "system"})
			in.Assets = append(in.Assets, kgEndpoint(int64(i)))
		}
		return in
	}
	if _, err := buildKGGraph(edgesAt(KGLimits.Nodes - 1)); err != nil {
		t.Fatalf("노드 상한 경계: %v", err)
	}
	if _, err := buildKGGraph(edgesAt(KGLimits.Nodes)); !errors.Is(err, ErrKGTooLarge) {
		t.Fatalf("노드 상한+1: %v", err)
	}
	// 출처 상한: 자산 하나에 범위 출처를 상한+1개
	in := kgRows{Tasks: kgTasks(41), Assets: []kgAssetRow{kgEndpoint(1)}}
	for i := 1; i <= KGLimits.Provenance+1; i++ {
		in.ScopeMatches = append(in.ScopeMatches, kgScopeMatchRow{Scope: int64(i), Task: 41, Asset: 1, Source: "auto"})
	}
	if _, err := buildKGGraph(in); !errors.Is(err, ErrKGTooLarge) {
		t.Fatalf("출처 상한+1: %v", err)
	}
	in.ScopeMatches = in.ScopeMatches[:KGLimits.Provenance]
	if _, err := buildKGGraph(in); err != nil {
		t.Fatalf("출처 상한 경계: %v", err)
	}
}

// 포함된 자산 행이 snapshot에 없으면 그래프를 만들지 않는다(빈 성공으로 바꾸지 않음).
func TestKGMissingAssetRowFails(t *testing.T) {
	if _, err := buildKGGraph(kgRows{Tasks: kgTasks(41), Arrays: []kgPair{{41, 1}}}); err == nil || errors.Is(err, ErrKGTooLarge) {
		t.Fatalf("err=%v", err)
	}
}
