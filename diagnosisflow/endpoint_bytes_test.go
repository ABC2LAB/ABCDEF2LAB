package diagnosisflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 끝점 바이트 절단(개발 계획): 공개 끝점 목록은 개수 상한(256) 대신 공개 투영 바이트에서 먼저 잘린다. 받은 끝점에
// 나중에 붙을 수 있는 칸(방문·상태·선택·호출 참조·관찰 오류·탐색 세션·요청/응답·이름 목록)이 최악으로 다 붙고, 명령
// 영수증 32개·활동·계획·사건·긴 현재 경로·인벤토리 상한까지 차도, 공개 투영은 MaxProjectionBytes 안으로 줄고 저장
// 기록은 128KiB 안이다. 짧은 경로는 이전 개수 상한 64보다 많이 받고, 잘린 수는 counts.omitted에 남는다.
func TestEndpointListCutByProjectionBytes(t *testing.T) {
	pad := func(prefix string, i int, n int) string {
		id := fmt.Sprintf("/%d", i)
		return prefix + strings.Repeat("p", max(0, n-len(prefix)-len(id))) + id
	}
	for _, tc := range []struct {
		name string
		path func(int) string
		min  int
	}{
		{"short", func(i int) string { return fmt.Sprintf("/read/e%d", i) }, 100},
		{"128-byte", func(i int) string { return pad("/read/", i, 128) }, 40},
		// JSON이 &으로 늘리는 문자(&)만으로 된 경로도 바이트로 잰다.
		{"json-escaped", func(i int) string { return "/read/" + strings.Repeat("&", 100) + fmt.Sprintf("/%d", i) }, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := flowFixture(t, Auto)
			c.binding.Limits.MaxEndpoints = MaxWorkflowEndpoints
			c.mu.Lock()
			defer c.mu.Unlock()
			attempts := MaxWorkflowEndpoints + 16
			added := 0
			for i := 0; i < attempts; i++ {
				if c.addLocked("GET", tc.path(i), "link", "html_link", tc.path(i)) != nil {
					added++
				}
			}
			s := &c.record.Snapshot
			if added < tc.min || added >= attempts || s.Counts.Omitted != uint64(attempts-added) {
				t.Fatalf("added=%d omitted=%d (attempts %d, min %d)", added, s.Counts.Omitted, attempts, tc.min)
			}
			t.Logf("%s: %d endpoints admitted, %d omitted", tc.name, added, s.Counts.Omitted)
			names := make([]string, maxDiscoveryKeys)
			for i := range names {
				names[i] = strings.Repeat("k", 60) + fmt.Sprint(i)
			}
			for i := range s.Endpoints {
				e := &s.Endpoints[i]
				e.Visited, e.Status, e.Selected, e.Duplicates = true, 599, true, 1<<30
				e.CallRef = "call-" + strings.Repeat("f", 32)
				e.DiscoveryConfidence = "observed"
				e.ExplorationSessionRef, e.ExplorationSeq = "explore-session-"+strings.Repeat("f", 64), 1<<62
				e.Transport, e.ObservationError = "websocket", "response_too_large"
				e.Kind, e.Source = "script", SourceAPIReconRuntime
				e.Request, e.Response = strings.Repeat("q", 4096), strings.Repeat("r", 4096)
				e.QueryKeys, e.BodyKeys, e.ContentType = names, names, "application/"+strings.Repeat("x", 60)
			}
			for i := 0; i < 32; i++ {
				key := fmt.Sprintf("%0128d", i)
				c.record.Commands[key] = commandReceipt{Fingerprint: strings.Repeat("f", 64), Operation: "selection"}
				s.Commands = append(s.Commands, Command{Key: key, Operation: "selection", Revision: 1 << 40})
			}
			for i := 0; i < 64; i++ {
				s.Activity = append(s.Activity, Activity{ID: int64(i), At: time.Now(), Worker: "worker", Kind: "bash", Summary: strings.Repeat("a", 200)})
				s.Plan = append(s.Plan, Plan{ID: int64(i), State: "open", Title: strings.Repeat("t", 200)})
				s.Events = append(s.Events, Event{At: time.Now(), Kind: "request", Method: "GET", Path: tc.path(i), Status: 599})
			}
			s.CurrentPath = "/read/" + strings.Repeat("&", 120)
			s.Failure, s.ExplorationReason = "workflow_projection_failed", "exploration_budget_exhausted"
			for i := 0; ; i++ {
				before := len(c.record.DiscoveryInventory)
				c.inventoryLocked(DiscoveryEvidence{Method: "GET", Path: pad("/read/inv", i, 128), Kind: "api", Source: SourceAPIReconRuntime, Confidence: "observed", SessionRef: strings.Repeat("s", 64), Seq: uint64(i), InScope: true, At: time.Now()})
				if len(c.record.DiscoveryInventory) == before {
					break
				}
			}
			projected := compactProjection(Project(clone(c.record).Snapshot))
			raw, _ := json.Marshal(projected)
			if len(raw) > MaxProjectionBytes {
				t.Fatalf("public projection %d bytes > %d", len(raw), MaxProjectionBytes)
			}
			if len(projected.Endpoints) != len(c.record.Snapshot.Endpoints) {
				t.Fatalf("projection dropped endpoints: %d of %d", len(projected.Endpoints), len(c.record.Snapshot.Endpoints))
			}
			stored := clone(c.record)
			stored.Snapshot = compactProjection(stored.Snapshot)
			record, _ := json.Marshal(stored)
			if len(record) > 128<<10 {
				t.Fatalf("stored record %d bytes > 128KiB", len(record))
			}
			// 범위·선택에 쓰는 칸과 크롤 문서 기준(From)은 압축에서 남는다.
			for _, e := range projected.Endpoints {
				if e.ID == "" || e.Path == "" || !e.Selected || !e.Visited || e.From == "" || e.Source != SourceAPIReconRuntime || e.DiscoveryConfidence != "observed" {
					t.Fatalf("compaction removed a scope field: %+v", e)
				}
			}
		})
	}
}

// 목록이 바이트에 닿은 뒤에도 범위 안 발견은 범위 밖·미방문·미선택 끝점 자리를 쓸 수 있다(바꿔도 바이트 안일 때만).
func TestEndpointByteCapKeepsInScopeReplacement(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	c.binding.Limits.MaxEndpoints = MaxWorkflowEndpoints
	c.mu.Lock()
	defer c.mu.Unlock()
	// 범위 밖(/outside) 끝점 하나를 먼저 받고, 범위 안 끝점으로 바이트를 채운다.
	if e := c.addLocked("GET", "/outside/x", "link", "html_link", "/read"); e == nil || e.InScope {
		t.Fatal("out-of-scope fixture")
	}
	replaced := false
	for i := 0; i < MaxWorkflowEndpoints; i++ {
		omitted := c.record.Snapshot.Counts.Omitted
		path := fmt.Sprintf("/read/fill%03d", i)
		e := c.addLocked("GET", path, "link", "html_link", "/read")
		if c.endpointLocked("GET", "/outside/x") == nil {
			// 바이트에 닿은 첫 범위 안 발견이 범위 밖 자리를 썼다. 빠진 끝점(범위 밖)으로 센다.
			if e == nil || !e.InScope || c.record.Snapshot.Counts.Omitted != omitted+1 {
				t.Fatal("replacement after the byte cap not admitted or not counted")
			}
			replaced = true
			break
		}
		if e == nil {
			t.Fatal("in-scope discovery rejected while an out-of-scope slot was free")
		}
	}
	if !replaced {
		t.Fatal("byte cap never reached")
	}
	if c.addLocked("GET", "/read/later", "link", "html_link", "/read") != nil {
		t.Fatal("no out-of-scope slot left but endpoint admitted beyond the byte cap")
	}
}

// 끝점별 바닥 크기를 더한 값(받을 때 재는 값)은 스냅숏 전체를 압축 바닥 꼴로 직렬화한 크기와 같다(JSON 배열 = 원소 +
// 쉼표). From 갱신 뒤에도 기억한 값이 맞고, 기억이 빈 컨트롤러(저장 기록을 다시 읽은 경우)도 같은 값을 낸다.
func TestEndpointFloorSumMatchesFullProjection(t *testing.T) {
	c, _ := flowFixture(t, Auto)
	c.binding.Limits.MaxEndpoints = MaxWorkflowEndpoints
	full := func(s Snapshot) int {
		s.Activity, s.Plan, s.Events = nil, nil, nil
		eps := make([]Endpoint, len(s.Endpoints))
		copy(eps, s.Endpoints)
		for i := range eps {
			stripObservationDetail(&eps[i])
		}
		s.Endpoints = eps
		raw, _ := json.Marshal(s)
		return len(raw)
	}
	sum := func(c *Controller) int {
		s := c.record.Snapshot
		total := snapshotBaseFloorBytes(s) + max(len(s.Endpoints)-1, 0)
		for _, e := range s.Endpoints {
			n, ok := c.floorCache[e.ID]
			if !ok {
				n = endpointFloorBytes(e)
			}
			total += n
		}
		return total
	}
	c.mu.Lock()
	c.addLocked("GET", "/outside/x", "link", "html_link", "/read")
	for i := 0; i < 40; i++ {
		c.addLocked("GET", fmt.Sprintf("/read/item%d", i), "link", "html_link", "/read")
	}
	e := c.endpointLocked("GET", "/read/item3")
	c.setFromLocked(e, "/read/"+strings.Repeat("f", 100))
	if got, want := sum(c), full(c.record.Snapshot); got != want {
		c.mu.Unlock()
		t.Fatalf("floor sum %d != full projection floor %d", got, want)
	}
	// 다시 읽은 컨트롤러는 기억이 비어 있다. 비운 기억으로 다시 재도 같은 값이고, 받기 검사가 기억을 채운다.
	c.floorCache = map[string]int{}
	if sum(c) != full(c.record.Snapshot) {
		c.mu.Unlock()
		t.Fatal("empty cache computed a different floor")
	}
	if _, fits := c.endpointFitsLocked(-1, Endpoint{ID: "probe", Method: "GET", Path: "/read/probe"}); !fits || len(c.floorCache) != len(c.record.Snapshot.Endpoints) {
		c.mu.Unlock()
		t.Fatal("admission check did not refill the floor cache")
	}
	c.mu.Unlock()
}
