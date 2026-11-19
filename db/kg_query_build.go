package db

// KG 조회 v1 응답 조립(docs/contracts/kg-query-v1.md). 이 파일은 DB를 만지지 않는다 —
// kg_query.go가 한 snapshot에서 읽은 행을 받아 계약의 노드·관계·출처·문제로 바꾼다.
// - 포함 근거(attached/scope/anchor)와 관측 참조 출처를 따로 남긴다. 관측은 참조일 뿐 검증이 아니다(§4.2·§4.3).
// - URL·제목·source_summary·payload 원문은 응답에 넣지 않는다. 라벨은 "<유형> #<id>"뿐이다(§4.1).
// - 상한을 넘으면 자르지 않고 ErrKGTooLarge다(§5.3).

import (
	"errors"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const KGQueryVersion = "abc2lab.kg-query/v1"

// KGLimits는 §5.3 후보 상한이다. Task 노드도 노드 수에 들어간다.
var KGLimits = struct{ Nodes, Edges, Provenance, Issues, Bytes int }{2000, 5000, 5000, 5000, 16 << 20}

// ErrKGTooLarge는 건수·바이트 상한 초과다. 응답을 자르지 않는다(413).
var ErrKGTooLarge = errors.New("kg_graph_too_large")

// KGContextTask는 권한을 확인한 작업 문맥 한 줄이다. [0]이 현재 작업, 나머지는 직접 원천(ID 오름차순).
type KGContextTask struct{ ID, Exploration int64 }

// ---------- wire 형식(§5.1) ----------

type KGGraph struct {
	Version  string     `json:"version"`
	TaskID   string     `json:"task_id"`
	Scope    KGScope    `json:"scope"`
	Sections KGSections `json:"sections"`
	// v1은 정책 출처를 읽지 않아 항상 null이다(§5.2·§11-2). 자리만 둔다.
	PolicySource any            `json:"policy_source"`
	Nodes        []any          `json:"nodes"`
	Edges        []KGEdge       `json:"edges"`
	Provenance   []KGProvenance `json:"provenance"`
	Issues       []KGIssue      `json:"issues"`
	Page         KGPage         `json:"page"`
}

type KGScope struct {
	Kind           string   `json:"kind"`
	TaskIDs        []string `json:"task_ids"`
	SourceDepth    int      `json:"source_depth"`
	ExecutionScope any      `json:"execution_scope"`
}

type KGSections struct {
	Assets       string `json:"assets"`
	Provenance   string `json:"provenance"`
	Ownership    string `json:"ownership"`
	Diagnosis    string `json:"diagnosis"`
	PolicySource string `json:"policy_source"`
}

type KGPage struct {
	Mode       string `json:"mode"`
	HasMore    bool   `json:"has_more"`
	NextCursor any    `json:"next_cursor"`
}

type KGTaskNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	TaskID string `json:"task_id"`
	Label  string `json:"label"`
	Role   string `json:"role"`
}

type KGAssetNode struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	AssetID          string `json:"asset_id"`
	AssetType        string `json:"asset_type"`
	Label            string `json:"label"`
	ContextState     string `json:"context_state"`
	ObservationState string `json:"observation_state"`
}

// KGEdge의 basis는 source_task에서만 생략한다(§5.2).
type KGEdge struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Src            string   `json:"src"`
	Dst            string   `json:"dst"`
	Basis          string   `json:"basis,omitempty"`
	State          string   `json:"state"`
	ProvenanceRefs []string `json:"provenance_refs"`
}

// KGProvenance의 공통 칸은 null이어도 항상 낸다. 종류별 추가 칸만 생략한다(§4.3·§5.2).
type KGProvenance struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	ScopeID       string         `json:"scope_id,omitempty"`
	TaskID        *string        `json:"task_id"`
	SourceTaskID  string         `json:"source_task_id,omitempty"`
	AssetID       *string        `json:"asset_id"`
	ParentAssetID string         `json:"parent_asset_id,omitempty"`
	Rule          string         `json:"rule,omitempty"`
	Source        *string        `json:"source"`
	Inherited     bool           `json:"inherited"`
	Observation   *KGObservation `json:"observation"`
}

type KGObservation struct {
	NodeID            string `json:"node_id"`
	Kind              string `json:"kind"`
	SourceState       string `json:"source_state"`
	ClaimedConfidence string `json:"claimed_confidence"`
	Authority         string `json:"authority"`
}

type KGIssue struct {
	Code  string   `json:"code"`
	State string   `json:"state"`
	Refs  []string `json:"refs"`
}

// ---------- DB에서 읽은 행 ----------

type kgPair struct{ Task, Asset int64 }

type kgLinkRow struct {
	Task, Asset int64
	Source      string
	SourceNode  *int64
}

type kgScopeMatchRow struct {
	Scope, Task, Asset int64
	Source             string
}

type kgNodeRow struct {
	ID, Exploration int64
	Kind, State     string
	// fact payload의 confidence 원문(허용 값만 쓴다). fact가 아니면 "".
	Confidence string
}

type kgAnchorRow struct {
	Node  kgNodeRow
	Asset int64
}

// kgSourceNodeRow는 연결 행의 source_node_id 검증용이다. Anchored = 그 노드와 연결 자산 사이 실제 앵커가 있음.
type kgSourceNodeRow struct {
	Link     kgPair
	Node     kgNodeRow
	Anchored bool
}

type kgAssetRow struct {
	ID                                      int64
	Type, Domain, RootDomain, IP, URL, Kind string
}

type kgRows struct {
	Tasks        []KGContextTask
	Arrays       []kgPair // assets.task_ids 연결
	Links        []kgLinkRow
	Mismatches   []kgPair // 배열에 없는 연결 행(포함된 자산만)
	ScopeMatches []kgScopeMatchRow
	KeywordTasks []int64
	Anchors      []kgAnchorRow
	SourceNodes  []kgSourceNodeRow
	Assets       []kgAssetRow
}

// ---------- 허용 값(§4.3) ----------

var kgLinkSources = map[string]bool{"system": true, "manual": true, "api": true, "agent": true, "task": true, "company": true, "legacy": true}
var kgScopeSources = map[string]bool{"manual": true, "agent": true, "auto": true}
var kgIntentDone = map[string]bool{"done": true, "blocked": true, "exhausted": true, "stopped": true}

var kgAssetTypeLabel = map[string]string{
	"root_domain": "루트 도메인", "subdomain": "하위 도메인", "ip": "IP",
	"app": "애플리케이션", "service": "서비스", "endpoint": "엔드포인트",
}

const (
	kgIssueLegacyLinkMissing    = "legacy_link_missing"
	kgIssueMembershipMismatch   = "membership_mismatch"
	kgIssueSourceUnverified     = "source_reference_unverified"
	kgIssueScopeNotProjected    = "scope_kind_not_projected"
	kgIssueContainmentAmbiguous = "containment_ambiguous"
	kgIssueUnknownSource        = "unknown_source"
)

var kgIssueState = map[string]string{
	kgIssueLegacyLinkMissing:    "insufficient",
	kgIssueMembershipMismatch:   "conflict",
	kgIssueSourceUnverified:     "insufficient",
	kgIssueScopeNotProjected:    "insufficient",
	kgIssueContainmentAmbiguous: "insufficient",
	kgIssueUnknownSource:        "insufficient",
}

// kgPublicNode는 출처를 가진 작업 기준으로 그 탐색 노드를 공개할 수 있는지다(§3.1-3).
// 원천 작업의 intent는 끝난 네 상태만, 현재 작업은 삭제된 intent만 뺀다.
func kgPublicNode(n kgNodeRow, inherited bool) bool {
	switch n.Kind {
	case "fact", "finding":
		return true
	case "intent":
		if inherited {
			return kgIntentDone[n.State]
		}
		return n.State != "deleted"
	}
	return false
}

// kgObservationOf: fact만 payload confidence를 읽고 model_claim이다. 나머지는 unknown·unverified_reference(§4.3).
func kgObservationOf(n kgNodeRow) *KGObservation {
	o := &KGObservation{NodeID: kgID(n.ID), Kind: n.Kind, SourceState: n.State, ClaimedConfidence: "unknown", Authority: "unverified_reference"}
	if n.Kind == "fact" {
		o.Authority = "model_claim"
		if n.Confidence == "observed" || n.Confidence == "inferred" {
			o.ClaimedConfidence = n.Confidence
		}
	}
	return o
}

func kgID(v int64) string       { return strconv.FormatInt(v, 10) }
func kgTaskRef(v int64) string  { return "t:" + kgID(v) }
func kgAssetRef(v int64) string { return "a:" + kgID(v) }
func kgStr(s string) *string    { return &s }

type kgBuilder struct {
	current     int64
	inherited   map[int64]bool // 작업 → 직접 원천인지
	exploration map[int64]int64
	prov        map[string]KGProvenance
	// (작업, 자산, basis) → 출처 ID
	include map[kgPair]map[string]map[string]bool
	issues  map[string]KGIssue
}

func (b *kgBuilder) addInclusion(task, asset int64, basis string, p KGProvenance) {
	b.prov[p.ID] = p
	key := kgPair{task, asset}
	if b.include[key] == nil {
		b.include[key] = map[string]map[string]bool{}
	}
	if b.include[key][basis] == nil {
		b.include[key][basis] = map[string]bool{}
	}
	b.include[key][basis][p.ID] = true
}

func (b *kgBuilder) addIssue(code string, refs ...string) {
	sort.Strings(refs)
	b.issues[code+"\x00"+strings.Join(refs, "\x00")] = KGIssue{Code: code, State: kgIssueState[code], Refs: refs}
}

// buildKGGraph는 읽은 행을 계약 응답으로 바꾼다. Tasks[0]이 현재 작업이어야 한다.
func buildKGGraph(in kgRows) (*KGGraph, error) {
	if len(in.Tasks) == 0 {
		return nil, errors.New("kg: empty task context")
	}
	b := &kgBuilder{
		current:     in.Tasks[0].ID,
		inherited:   map[int64]bool{},
		exploration: map[int64]int64{},
		prov:        map[string]KGProvenance{},
		include:     map[kgPair]map[string]map[string]bool{},
		issues:      map[string]KGIssue{},
	}
	for i, t := range in.Tasks {
		b.inherited[t.ID] = i > 0
		b.exploration[t.ID] = t.Exploration
	}
	known := func(task int64) bool { _, ok := b.inherited[task]; return ok }

	// 1. 명시적 연결: 배열이 기준이고 연결 행은 상세 출처다(§3.1-1).
	links := map[kgPair]kgLinkRow{}
	for _, l := range in.Links {
		links[kgPair{l.Task, l.Asset}] = l
	}
	sourceNodes := map[kgPair]kgSourceNodeRow{}
	for _, s := range in.SourceNodes {
		sourceNodes[s.Link] = s
	}
	for _, pair := range in.Arrays {
		if !known(pair.Task) {
			continue
		}
		inherited := b.inherited[pair.Task]
		l, ok := links[pair]
		if !ok {
			p := KGProvenance{ID: "array:" + kgID(pair.Task) + ":" + kgID(pair.Asset), Kind: "asset_task_ids",
				TaskID: kgStr(kgID(pair.Task)), AssetID: kgStr(kgID(pair.Asset)), Source: kgStr("unknown"), Inherited: inherited}
			b.addInclusion(pair.Task, pair.Asset, "attached", p)
			b.addIssue(kgIssueLegacyLinkMissing, p.ID)
			continue
		}
		p := KGProvenance{ID: "link:" + kgID(pair.Task) + ":" + kgID(pair.Asset), Kind: "task_asset_link",
			TaskID: kgStr(kgID(pair.Task)), AssetID: kgStr(kgID(pair.Asset)), Source: kgStr(l.Source), Inherited: inherited}
		if !kgLinkSources[l.Source] {
			p.Source = kgStr("unknown")
			b.addIssue(kgIssueUnknownSource, p.ID)
		}
		if l.SourceNode != nil {
			// 같은 작업 탐색의 공개 가능한 노드이고 이 자산에 실제 앵커가 있을 때만 관측 참조로 낸다(§4.3).
			s, found := sourceNodes[pair]
			if found && s.Node.ID == *l.SourceNode && s.Node.Exploration == b.exploration[pair.Task] && s.Anchored && kgPublicNode(s.Node, inherited) {
				p.Observation = kgObservationOf(s.Node)
			} else {
				b.addIssue(kgIssueSourceUnverified, p.ID)
			}
		}
		b.addInclusion(pair.Task, pair.Asset, "attached", p)
	}

	// 2. 범위 일치(§3.1-2). keyword는 자산을 추정하지 않고 문제로만 남긴다.
	for _, m := range in.ScopeMatches {
		if !known(m.Task) {
			continue
		}
		p := KGProvenance{ID: "scope:" + kgID(m.Scope) + ":" + kgID(m.Asset), Kind: "task_scope", ScopeID: kgID(m.Scope),
			TaskID: kgStr(kgID(m.Task)), AssetID: kgStr(kgID(m.Asset)), Source: kgStr(m.Source), Inherited: b.inherited[m.Task]}
		if !kgScopeSources[m.Source] {
			p.Source = kgStr("unknown")
			b.addIssue(kgIssueUnknownSource, p.ID)
		}
		b.addInclusion(m.Task, m.Asset, "scope", p)
	}
	for _, task := range in.KeywordTasks {
		if known(task) {
			b.addIssue(kgIssueScopeNotProjected, kgTaskRef(task))
		}
	}

	// 3. 공개 가능한 앵커(§3.1-3). 노드의 탐색이 곧 출처를 가진 작업이다.
	byExploration := map[int64]int64{}
	for task, e := range b.exploration {
		byExploration[e] = task
	}
	for _, a := range in.Anchors {
		task, ok := byExploration[a.Node.Exploration]
		if !ok || !kgPublicNode(a.Node, b.inherited[task]) {
			continue
		}
		p := KGProvenance{ID: "anchor:" + kgID(a.Node.ID) + ":" + kgID(a.Asset), Kind: "exploration_anchor",
			TaskID: kgStr(kgID(task)), AssetID: kgStr(kgID(a.Asset)), Inherited: b.inherited[task], Observation: kgObservationOf(a.Node)}
		b.addInclusion(task, a.Asset, "anchor", p)
	}

	// 포함된 자산 집합. 자산 행이 없으면(같은 snapshot이라 생기면 안 됨) 계약 응답을 만들 수 없다.
	selected := map[int64]bool{}
	for k := range b.include {
		selected[k.Asset] = true
	}
	assets := map[int64]kgAssetRow{}
	for _, a := range in.Assets {
		if selected[a.ID] {
			assets[a.ID] = a
		}
	}
	for id := range selected {
		if _, ok := assets[id]; !ok {
			return nil, errors.New("kg: selected asset row missing")
		}
		if kgAssetTypeLabel[assets[id].Type] == "" {
			return nil, errors.New("kg: unknown asset type")
		}
	}
	if len(in.Tasks)+len(selected) > KGLimits.Nodes {
		return nil, ErrKGTooLarge
	}

	// 배열에 없는 연결 행은 포함 근거로 쓰지 않는다. 다른 근거로 이미 보이는 자산일 때만 상충을 알린다(§3.1).
	for _, m := range in.Mismatches {
		if known(m.Task) && selected[m.Asset] {
			b.addIssue(kgIssueMembershipMismatch, kgAssetRef(m.Asset), kgTaskRef(m.Task))
		}
	}

	var edges []KGEdge
	for key, bases := range b.include {
		for basis, ids := range bases {
			state := "stored"
			if basis == "scope" {
				state = "derived"
			}
			src, dst := kgTaskRef(key.Task), kgAssetRef(key.Asset)
			edges = append(edges, KGEdge{ID: "task_asset:" + src + ":" + dst + ":" + basis, Kind: "task_asset", Src: src, Dst: dst,
				Basis: basis, State: state, ProvenanceRefs: kgSortedKeys(ids)})
		}
	}
	for _, t := range in.Tasks[1:] {
		pid := "relation:" + kgID(b.current) + ":" + kgID(t.ID)
		b.prov[pid] = KGProvenance{ID: pid, Kind: "task_relation", TaskID: kgStr(kgID(b.current)), SourceTaskID: kgID(t.ID)}
		src, dst := kgTaskRef(b.current), kgTaskRef(t.ID)
		edges = append(edges, KGEdge{ID: "source_task:" + src + ":" + dst, Kind: "source_task", Src: src, Dst: dst, State: "stored", ProvenanceRefs: []string{pid}})
	}
	edges = append(edges, b.containment(assets)...)

	nodes := make([]any, 0, len(in.Tasks)+len(assets))
	taskIDs := make([]string, 0, len(in.Tasks))
	for i, t := range in.Tasks {
		role := "source"
		if i == 0 {
			role = "current"
		}
		taskIDs = append(taskIDs, kgID(t.ID))
		nodes = append(nodes, KGTaskNode{ID: kgTaskRef(t.ID), Kind: "task", TaskID: kgID(t.ID), Label: "작업 #" + kgID(t.ID), Role: role})
	}
	ids := make([]int64, 0, len(assets))
	for id := range assets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		a := assets[id]
		nodes = append(nodes, KGAssetNode{ID: kgAssetRef(id), Kind: "asset", AssetID: kgID(id), AssetType: a.Type,
			Label: kgAssetTypeLabel[a.Type] + " #" + kgID(id), ContextState: "included", ObservationState: "unknown"})
	}

	provenance := make([]KGProvenance, 0, len(b.prov))
	for _, p := range b.prov {
		provenance = append(provenance, p)
	}
	sort.Slice(provenance, func(i, j int) bool { return provenance[i].ID < provenance[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	issues := make([]KGIssue, 0, len(b.issues))
	for _, x := range b.issues {
		issues = append(issues, x)
	}
	sort.Slice(issues, func(i, j int) bool { return kgIssueLess(issues[i], issues[j]) })
	if edges == nil {
		edges = []KGEdge{}
	}
	if len(edges) > KGLimits.Edges || len(provenance) > KGLimits.Provenance || len(issues) > KGLimits.Issues {
		return nil, ErrKGTooLarge
	}
	return &KGGraph{
		Version:  KGQueryVersion,
		TaskID:   kgID(b.current),
		Scope:    KGScope{Kind: "task_context", TaskIDs: taskIDs, SourceDepth: 1},
		Sections: KGSections{Assets: "complete", Provenance: "complete", Ownership: "not_requested", Diagnosis: "not_requested", PolicySource: "not_requested"},
		Nodes:    nodes, Edges: edges, Provenance: provenance, Issues: issues,
		Page: KGPage{Mode: "complete"},
	}, nil
}

func kgSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// kgIssueLess는 (code, state, refs) 바이트 사전순이다. refs는 원소별로 비교하고 짧은 쪽이 앞이다(§5.2).
func kgIssueLess(a, b KGIssue) bool {
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	if a.State != b.State {
		return a.State < b.State
	}
	for i := 0; i < len(a.Refs) && i < len(b.Refs); i++ {
		if a.Refs[i] != b.Refs[i] {
			return a.Refs[i] < b.Refs[i]
		}
	}
	return len(a.Refs) < len(b.Refs)
}

// containment은 §4.1 네 규칙으로 자식→부모 관계를 저장 필드에서만 만든다. 후보가 여럿이면 고르지 않고 문제로 남긴다.
// 이것은 호스팅·DNS·소유 증명이 아닌 저장 토폴로지 파생이다.
func (b *kgBuilder) containment(assets map[int64]kgAssetRow) []KGEdge {
	byType := map[string][]kgAssetRow{}
	for _, a := range assets {
		byType[a.Type] = append(byType[a.Type], a)
	}
	match := func(typ string, ok func(kgAssetRow) bool) []kgAssetRow {
		var out []kgAssetRow
		for _, p := range byType[typ] {
			if ok(p) {
				out = append(out, p)
			}
		}
		return out
	}
	var edges []KGEdge
	link := func(child kgAssetRow, parents []kgAssetRow, rule string) bool {
		switch len(parents) {
		case 0:
			return false
		case 1:
			p := parents[0]
			pid := "fields:" + kgID(child.ID) + ":" + kgID(p.ID) + ":" + rule
			b.prov[pid] = KGProvenance{ID: pid, Kind: "asset_fields", AssetID: kgStr(kgID(child.ID)), ParentAssetID: kgID(p.ID), Rule: rule}
			src, dst := kgAssetRef(child.ID), kgAssetRef(p.ID)
			edges = append(edges, KGEdge{ID: "contained_by:" + src + ":" + dst, Kind: "contained_by", Src: src, Dst: dst,
				Basis: "asset_fields", State: "derived", ProvenanceRefs: []string{pid}})
		default:
			b.addIssue(kgIssueContainmentAmbiguous, kgAssetRef(child.ID))
		}
		return true
	}
	for _, c := range assets {
		switch c.Type {
		case "subdomain":
			if c.RootDomain != "" {
				link(c, match("root_domain", func(p kgAssetRow) bool { return p.Domain == c.RootDomain }), "root_domain")
			}
		case "service":
			decided := c.Domain != "" && link(c, match("subdomain", func(p kgAssetRow) bool { return p.Domain == c.Domain }), "service_domain")
			if !decided && c.IP != "" {
				link(c, match("ip", func(p kgAssetRow) bool { return p.IP == c.IP }), "service_ip")
			}
		case "endpoint":
			origin, ok := kgOrigin(c.URL, false)
			if ok {
				link(c, match("service", func(p kgAssetRow) bool {
					o, ok := kgOrigin(p.URL, true)
					return p.Kind == "http" && ok && o == origin
				}), "endpoint_origin")
			}
		}
	}
	return edges
}

// kgOrigin은 http(s) URL의 정규 (scheme, host, effective port)다. root=true면 origin 루트만 받는다
// (경로 "" 또는 "/", userinfo·query·fragment 없음). 정규화할 수 없으면 false — 그 관계는 생략한다(§4.1).
func kgOrigin(raw string, root bool) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := map[string]string{"http": "80", "https": "443"}[scheme]
	if port == "" {
		return "", false
	}
	if root && (u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Path != "" && u.Path != "/") {
		return "", false
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return "", false
		}
		host = ip.Unmap().String()
	} else {
		for i := 0; i < len(host); i++ {
			if host[i] >= 0x80 || host[i] <= 0x20 {
				return "", false
			}
		}
		host = strings.ToLower(host)
	}
	if host == "" {
		return "", false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		port = strconv.Itoa(n)
	} else if strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	return scheme + "://" + host + ":" + port, true
}
