package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
)

// 브라우저 입력을 전달하지 않고 관리 서버가 DB 문맥을 확인한 뒤 만드는 내부 중계 결속이다.
const reportReadScopeHeader = "X-ABC2LAB-Report-Read-Scope"
const maxReportSourceTasks = 256

var errReportReadScope = errors.New("report_read_scope_changed")

type reportReadTask struct{ ID, Exploration int64 }
type reportReadBinding struct {
	Tasks []reportReadTask
	Mode  uint64
	Map   []json.RawMessage
}

type reportReadQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func reportGraphRequestTask(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet || r.URL.ForceQuery || r.URL.RawPath != "" {
		return 0, false
	}
	tail, ok := strings.CutPrefix(r.URL.Path, "/api/tasks/")
	if !ok {
		return 0, false
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[1] != "coverage-graph" && parts[1] != "access-map" {
		return 0, false
	}
	// query는 coverage-graph의 KG 조회 v1 선택자 하나만 받는다(kg-query-v1 §3).
	if r.URL.RawQuery != "" && (parts[1] != "coverage-graph" || r.URL.RawQuery != kgV1Query) {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == parts[0]
}

func reportReadDigest(b reportReadBinding) string {
	raw, _ := json.Marshal(b)
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}

func readReportBinding(ctx context.Context, q reportReadQuery, task int64, graph bool) (reportReadBinding, error) {
	b := reportReadBinding{Tasks: []reportReadTask{}, Map: []json.RawMessage{}}
	var own reportReadTask
	if err := q.QueryRowContext(ctx, `SELECT id,exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, task).Scan(&own.ID, &own.Exploration); err != nil {
		return b, err
	}
	b.Tasks = append(b.Tasks, own)
	if graph {
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT t.id,t.exploration_id FROM task_relations r JOIN tasks t ON t.id=r.source_task_id AND t.deleted_at IS NULL WHERE r.task_id=$1 ORDER BY t.id LIMIT $2`, task, maxReportSourceTasks+1)
		if err != nil {
			return b, err
		}
		defer rows.Close()
		for rows.Next() {
			var t reportReadTask
			if err := rows.Scan(&t.ID, &t.Exploration); err != nil {
				return b, err
			}
			b.Tasks = append(b.Tasks, t)
		}
		if len(b.Tasks) > maxReportSourceTasks+1 {
			return b, errReportReadScope
		}
		return b, rows.Err()
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM task_control_modes WHERE task_id=$1),0)`, task).Scan(&b.Mode); err != nil {
		return b, err
	}
	rows, err := q.QueryContext(ctx, `SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='confirmed' AND origin=$2 ORDER BY id LIMIT 2`, own.Exploration, accessMapOrigin)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return b, err
		}
		if len(raw) > maxAccessMapBytes {
			return b, errAccessMapUnavailable
		}
		b.Map = append(b.Map, json.RawMessage(raw))
	}
	return b, rows.Err()
}

func reportBindingSnapshot(ctx context.Context, pool *sql.DB, task int64, graph bool) (reportReadBinding, error) {
	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return reportReadBinding{}, err
	}
	defer tx.Rollback()
	b, err := readReportBinding(ctx, tx, task, graph)
	if err == nil {
		err = tx.Commit()
	}
	return b, err
}

func reportExpectedScope(r *http.Request) (string, bool) {
	values := r.Header.Values(reportReadScopeHeader)
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 || len(values[0]) != 64 {
		return "", false
	}
	raw, err := hex.DecodeString(values[0])
	return values[0], err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == values[0]
}

func reportBindingUnchanged(ctx context.Context, pool *sql.DB, task int64, graph bool, prior reportReadBinding) error {
	next, err := reportBindingSnapshot(ctx, pool, task, graph)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(prior, next) {
		return errReportReadScope
	}
	return nil
}

func (s *Server) serveCoverageGraph(w http.ResponseWriter, r *http.Request, boundTask int64) {
	w.Header().Set("Cache-Control", "no-store")
	// query가 붙은 요청은 KG 조회 v1을 뜻하므로 오류를 §5.4 kg 봉투로 낸다. 무인자 오류 본문은 그대로다.
	kg := kgRequested(r)
	fail := func(status int, message string) {
		if kg {
			writeKGError(w, status)
			return
		}
		writeErr(w, status, message)
	}
	// Go mux의 GET 패턴은 HEAD도 받는다. v1은 GET만 받고 HEAD는 405다(§3).
	if kg && r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeKGError(w, http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticateReportRead(w, r) {
		return
	}
	task, ok := reportGraphRequestTask(r)
	if !ok || !strings.HasSuffix(r.URL.Path, "/coverage-graph") {
		fail(400, "자산 그래프 조회 형식이 올바르지 않습니다")
		return
	}
	if boundTask > 0 && task != boundTask {
		fail(403, "선택한 작업의 자산 그래프만 사용할 수 있습니다")
		return
	}
	expected, ok := reportExpectedScope(r)
	if !ok {
		fail(400, "자산 그래프 조회 결속이 올바르지 않습니다")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), accessMapTimeout)
	defer cancel()
	tx, err := s.m.pg.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		fail(503, "저장된 자산 그래프를 확인할 수 없습니다")
		return
	}
	defer tx.Rollback()
	binding, err := readReportBinding(ctx, tx, task, true)
	if err == nil && expected != "" && reportReadDigest(binding) != expected {
		err = errReportReadScope
	}
	var graph *db.CoverageGraphData
	var kgGraph *db.KGGraph
	if err == nil {
		// v1은 권한을 확인한 같은 작업 문맥(현재+직접 원천)을 같은 snapshot에서 읽는다(§3.1).
		if kg {
			kgGraph, err = db.QueryKGTx(ctx, tx, kgContextTasks(binding))
		} else {
			graph, err = s.m.pg.Assets().BuildCoverageGraphTx(ctx, tx, task)
		}
	}
	var kgBody []byte
	if err == nil && kg {
		kgBody, err = json.Marshal(kgGraph)
		if err == nil && len(kgBody) > db.KGLimits.Bytes {
			err = db.ErrKGTooLarge
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		err = reportBindingUnchanged(ctx, s.m.pg.DB, task, true, binding)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(404, "작업을 찾을 수 없습니다")
	case errors.Is(err, errReportReadScope):
		fail(409, "자산 그래프의 조회 문맥이 변경되었습니다")
	case kg && errors.Is(err, db.ErrKGTooLarge):
		fail(http.StatusRequestEntityTooLarge, "")
	case err != nil:
		fail(503, "저장된 자산 그래프를 확인할 수 없습니다")
	case kg:
		w.Header().Set(reportReadScopeHeader, reportReadDigest(binding))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(kgBody)
	default:
		w.Header().Set(reportReadScopeHeader, reportReadDigest(binding))
		writeJSON(w, 200, publicCoverageGraph(graph))
	}
}

// 공개 응답은 원문 URL의 자격·경로 조각·쿼리와 자유 문구를 제거한 복사본이다.
func publicCoverageGraph(g *db.CoverageGraphData) coverageGraphPublic {
	out := coverageGraphPublic{
		Nodes: make([]coverageGraphPublicNode, len(g.Nodes)),
		Edges: slices.Clone(g.Edges),
	}
	remap := map[string]string{}
	for i, node := range g.Nodes {
		out.Nodes[i].CoverageGraphNode = node
		n := &out.Nodes[i]
		if strings.HasPrefix(n.Key, "r:") && !reportPublicHost(strings.TrimPrefix(n.Key, "r:")) {
			hash := sha256.Sum256([]byte(n.Key))
			next := "r:" + hex.EncodeToString(hash[:16])
			remap[n.Key], n.Key = next, next
		}
	}
	for i := range out.Edges {
		if next, ok := remap[out.Edges[i].Src]; ok {
			out.Edges[i].Src = next
		}
		if next, ok := remap[out.Edges[i].Dst]; ok {
			out.Edges[i].Dst = next
		}
	}
	for i := range out.Nodes {
		n := &out.Nodes[i]
		n.Label = "자산 #" + strconv.FormatInt(n.AssetID, 10)
		if n.Kind == "company" {
			n.Label = "회사 #" + strconv.FormatInt(n.CompanyID, 10)
		}
		if u, err := url.Parse(n.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && reportPublicHost(u.Hostname()) {
			if n.Kind == "endpoint" {
				n.DisplayPath = coverageGraphDisplayPath(u.EscapedPath())
			}
			n.URL = (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
			n.Label = n.URL
		} else {
			n.URL = ""
		}
		for _, value := range []*string{&n.Domain, &n.RootDomain} {
			if !reportPublicHost(*value) {
				*value = ""
			}
		}
		if ip, err := netip.ParseAddr(n.IP); err != nil || ip.Zone() != "" {
			n.IP = ""
		}
		if n.URL == "" && n.Domain != "" {
			n.Label = n.Domain
		}
		n.AppName, n.PageTitle, n.ServiceType = "", "", ""
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Key < out.Nodes[j].Key })
	sort.Slice(out.Edges, func(i, j int) bool {
		a, b := out.Edges[i], out.Edges[j]
		return a.Src < b.Src || a.Src == b.Src && a.Dst < b.Dst
	})
	return out
}

func reportPublicHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (s *Server) authenticateReportRead(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	header := r.Header.Get("Authorization")
	// KG 조회 v1은 인증 실패도 §5.4 kg 봉투다. 무인자 조회의 본문은 그대로다.
	fail := func(status int, message string) {
		if kgRequested(r) {
			writeKGError(w, status)
			return
		}
		writeErr(w, status, message)
	}
	if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(header, "Bearer ") || len(header) <= len("Bearer ") {
		fail(http.StatusUnauthorized, authErrUnauthorized)
		return false
	}
	hash, err := s.reloadReportPassHash(r.Context())
	if err != nil {
		fail(http.StatusServiceUnavailable, authErrDataSourceUnavailable)
		return false
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(*jwt.Token) (any, error) { return s.signingKey(hash), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.Subject != "ARTEX" {
		fail(http.StatusUnauthorized, authErrTokenInvalid)
		return false
	}
	return true
}

// 추가한 응답 전 인증 조회도 요청 취소·상한을 따르게 한다. 실패는 빈 비밀번호로 캐시하지 않는다.
func (s *Server) reloadReportPassHash(ctx context.Context) (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", errManagementAuthenticationUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, accessMapTimeout)
	defer cancel()
	var hash string
	err := s.m.pg.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, authPassKey).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		hash = ""
		err = nil
	}
	if err != nil {
		return "", err
	}
	s.setPassHash(hash)
	return hash, nil
}
