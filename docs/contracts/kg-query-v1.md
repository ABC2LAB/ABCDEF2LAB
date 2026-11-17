# KG 조회 계약 v1 — 작업 자산·관측 출처 검토안

상태: **팀 검토용 초안 / API 미구현 / 실행 수용시험 NOT_RUN**. 문서 작성·PR 제출 승인은 계약의 팀 합의나 실행 권한 확대 승인이 아니다. 이경준(KG·UI)·이동찬(서버 통합)·최민준(정본 reader·E③ 근거)의 검토가 필요하다.

분석 기준: 2026-11-17 `dev` **`89c6d74b66556f568d620164984fb51fecd22707`**. 아래 코드 링크는 이 기준의 저장소 상대 경로다. 후속 구현은 최신 dev와 다시 대조한다.

연결 계약: [KG 정책](kg-policy.md), [fixture 레지스트리](kg-fixture-registry.md), [접근통제 지도 읽기](access-map-read.md), [실행 결과 상태](run-outcome-states.md), [개발 계획](../development-plan.md).

## 1. 목적과 경계

첫 버전은 PostgreSQL에 이미 있는 작업별 자산 포함 관계와 관측 **참조의 출처**를 읽는다. 관측 참조가 있다는 사실과 실제 관측이 검증됐다는 사실을 분리한다.

**관측된 자산 ≠ 소유 확인 ≠ 접근 허용 ≠ 취약점 확정**

- 기존 ARTEX 자산 그래프는 인프라·자산 문맥, 탐색 그래프는 intent/fact/finding의 추론 흐름이다. IDOR용 KG는 등록 소유·기대 정책의 정본이고, 접근통제 지도는 같은 실행·세대의 검증된 저장 근거를 보여주는 projection이다.
- v1은 새로운 그래프 DB·테이블, 탐색 실행, 대상 송신, E③ 판정 발급, fixture 정본 변경을 요구하지 않는다.
- URL 발견·객체 ID 추정·HTTP 200·모델 추론·E② 허용으로 소유·인가 엣지를 만들지 않는다. `task_scope` 일치는 ARTEX 자산 문맥이며 제품 `target_policy`의 송신 허가가 아니다.
- 기본 자산 응답에 Principal/Resource/owns/access 엣지를 추가하지 않는다. 검증된 등록 소유·접근 결과는 기존 `access-map`의 별도 범위에서 읽는다(§6). 자산 PK와 전체 `resource_ref`의 대응도 추정하지 않는다.

## 2. 현행 구현 조사

| 영역 | 테이블·함수·API | 현재 재사용 범위 / 부족한 것 |
|---|---|---|
| 자산 | [`assets`](../../db/schema.sql), [`AssetStore.Upsert*`, `QueryByTask`](../../db/assets.go) | 자산 PK·6개 유형 재사용. `auth`, `params`, `extra`, URL·제목을 공개 DTO로 복사하지 않음 |
| 작업 연결 | `assets.task_ids`, `task_asset_links`, `sync_task_asset_links()` | 배열은 기존 연결 조회·정리의 호환 기준. trigger가 일반 출처 행을 만들고 상세 등록 경로가 보강 |
| 수동 등록 | [`AttachAssetsToTask`, `RegisterTaskAssetScopes`, `SetTaskAssetSource`](../../db/task_assets.go) → [`attachTaskAssets`](../../server/task_assets.go) | 수동 연결·범위 등록·출처를 구분할 수 있음. 연결이 관측·소유·송신 승인인 것은 아님 |
| 범위 | [`directTaskContextCTE`, `contextCoverageCTE`, `scopeTargetCTE`](../../db/task_assets_context.go) | 현재와 직접 원천 작업만 조회. IP 범위 조건 등 helper 간 차이를 그대로 한 의미로 취급하면 안 됨 |
| 탐색 | [`exploration_nodes/edges/anchors`](../../db/schema.sql), [`AddNode`, `Anchor`, `Link`, `AssetRefs`](../../db/exploration.go) | 노드 종류·상태와 자산 참조. 앵커에는 독립적인 실제 관측 시각·송신 receipt·run/reset 결속이 없음 |
| 직접 상속 | `task_relations`, [`DirectSourceStores`, `AssetRefsWithSources`](../../db/exploration_sources.go), `IntentAssets` | live 직접 원천만 비재귀 조회. 상속 intent의 공개 종료 상태는 `done/blocked/exhausted/stopped` |
| 자산 목록 | [`listAssets`](../../server/assets.go) → `QueryByTask` + `hydrateTaskAssetSources` | `GET /api/assets?task_id=…`, 기본 50·최대 200. 직접 연결 조회이며 scope/anchor/source 전체 합집합은 아님 |
| 자산 그래프 | [`BuildCoverageGraph`](../../db/task_scope.go) → [`taskCoverageGraph`](../../server/server.go) | `GET /api/tasks/{id}/coverage-graph`. 자식→부모 관계, `a:<id>`·회사·가상 루트 노드. 출처별 근거 없음 |
| 관측 참조 | `GET /api/tasks/{id}/asset-refs?asset_id=…`, `/intent-assets` | 작업별 출처 일부 제공. 여러 API 조합만으로 원자적 그래프 snapshot을 얻을 수는 없음 |
| 탐색 그래프 API | `GET /api/exploration/graph?task=…` → `Nodes/Edges/DirectSourceStores` | 자산 포함 그래프와 다름. 현재 코드의 탐색별 2,000 nodes·5,000 edges 제한을 자산 총량으로 오해하지 않음 |
| 소유·정책 | [`kgfixture.Parse`, `Snapshot.ResolvePolicy`](../../kgfixture/registry.go), [`CheckPairing`](../../kgfixture/pairing.go), [`MapResources`](../../kgfixture/map_projection.go) | 등록 정본과 불변 snapshot 재사용. `MapResources`는 자원·소유자·등록 평가 주체만 복사하며 정책 값·유효창은 복사하지 않음 |
| 지도 읽기 | [`readAccessMap`, `readAccessMapRecords`, `projectAccessMap`](../../server/access_map.go), [`accessMapStore`](../../server/access_map_store.go) | 현재 포인터·발급·Source·manifest 결속 재사용. 일반 자산 그래프나 범용 정책 조회 API는 아님 |
| UI | [`getAccessMap`, `mapApiAccessMap`](../../web/src/lib/diagnosis/kg.ts), [`getCoverageGraph`](../../web/src/lib/diagnosis/engine.ts) | 별개 어댑터. `getAccessMap(null)`의 예시 반환을 실제 선택 없음·오류의 fallback으로 쓰지 않음 |

### 2.1 출처가 증명하는 범위

| 항목 | 확인 가능한 사실 | 확인하지 못하는 사실 |
|---|---|---|
| `assets.task_ids` | 저장된 작업 연결 | 실제 관측, 대상 소유, 전송 승인 |
| `task_asset_links.source` | `manual/api/agent/task/company/system/legacy` 등 연결 경로 표기 | 표기 내용 자체의 진실성·권한. DB CHECK enum이 아니므로 미등록 값 가능 |
| `source_summary` | 저장된 표시용 설명 | 정형 정책·관측 증명. 자유 문구를 파싱해 정책으로 승격하지 않음 |
| `source_node_id` | FK로 탐색 노드를 참조함 | 그 노드의 작업 소속·공개 자격·실제 관측. 별도 검증 필요 |
| `task_relations` | 현재 작업이 직접 원천으로 등록한 작업 | 재귀 상속·수정 권한·대상 송신 권한·정책 상속 |
| `exploration_anchors` | 노드와 자산 사이의 참조 행 | 네트워크 도달·실제 관측·자원 소유·허용 또는 취약점 |

[`recordOneFact`](../../agent/tools.go)는 모델의 summary/evidence/confidence를 받아 `kind=fact, state=confirmed`로 저장한다. 초기 범위도 origin fact에 연결된다. `contextCoverageCTE.tested`는 상태와 무관하게 fact 앵커 유무를 집계한다. 따라서 **fact의 `confirmed`나 그래프의 `tested=true`는 E③ 확정·실제 진단 완료·검증된 관측이 아니다.**

기존 그래프는 연결용 회사·가상 루트를 추가하고 서비스 부모를 host 기반으로 fallback 선택한다. 아래 v1은 출처가 있는 자산만 선택하고 애매한 부모를 생략한다. 기존 그래프를 이름만 바꿔 반환하는 계약이 아니다.

### 2.2 제품 입구

현재 [`NewURLDiagnosisManagementHandler`](../../server/diagnosis_url_management.go) → [`managementBridgeTask`](../../server/diagnosis_management_bridge.go)에는 `coverage-graph`와 `access-map` 허용이 없다. 내부 handler 존재만으로 제품 연결 완료가 아니다.

기존 [`URLHandler.BoundTask`](../../diagnosisapi/url_handler.go)는 준비 서비스의 영속 영수증 소유권을 조회한다. v1 제품 중계는 이를 재사용하며, 직접 원천 작업도 동일 운영자의 읽기 권한을 검증해야 한다. `task_relations`만으로 다른 운영자 자료를 공개하지 않는다.

## 3. 제안 조회 API와 조회 범위

```http
GET /api/tasks/41/coverage-graph?view=kg-v1
Authorization: Bearer <기존 관리자 JWT>
```

기존 경로에 선택형 projection을 추가한다. `view` 없는 기존 응답은 유지하며 새 API·중복 저장 경로를 만들지 않는다. **이 selector와 응답은 아직 미구현**이다. 경로·버전 방식은 Open Decision D01이다.

- `view`가 있으면 정확히 하나의 `view=kg-v1`만 허용한다. 다른 query·중복 키·미지원 view·비정규 task ID·비정규 경로는 400이다. `task=`, run/reset/case/이력/재귀 옵션은 없다.
- task ID는 선행 0 없는 양의 int64 십진 표기다. 직접 handler도 GET만 받고 HEAD는 405다. Go mux의 GET→HEAD 자동 매칭에 의존하지 않는다.
- 기존 관리자 JWT의 Bearer 검증을 재사용한다. 새 projection은 cookie/query token으로 직접 인증하지 않는다. 범용 ARTEX의 기존 `requireAuth` 추출 방식과 구분해 handler에서 검증한다.
- 제품 중계는 인증된 운영자의 현재 task 결속과 직접 원천의 읽기 권한을 확인한다. 원천 권한을 검증할 수 없으면 전체 요청을 403으로 거부하며 부분 그래프·원천 ID를 노출하지 않는다.
- 실행 서버는 기동에 결속된 현재 task만 조회한다. 준비 전용 서버에는 열지 않는다. 저장 보고서 서버에서의 새 projection 제공은 기본 v1 범위 밖이며 별도 route·읽기 권한 검토 후 추가한다.
- task/자산/출처는 한 read-only repeatable-read 트랜잭션에서 조회한다. 중계 권한은 응답 공개 전에도 재확인한다. 조회 중 관계·삭제가 바뀌면 한 DB snapshot의 결과만 반환한다.
- `Cache-Control: no-store`. GET으로 모델·탐색·E②·E③ 발급·동기화·복구·DDL·GRANT·저장 갱신을 호출하지 않는다.

### 3.1 자산 집합

`C = {현재 live task} ∪ {task_relations로 직접 연결된 live 원천 task}`다. 원천의 원천은 확장하지 않으며, 다른 task의 연결 ID 목록을 응답하지 않는다.

각 `c ∈ C`에서 다음 근거의 합집합을 선택한다. 근거가 겹쳐도 하나를 우선 선택하지 않는다.

1. `c.id = ANY(assets.task_ids)`인 **명시적 연결**. 대응 `task_asset_links`를 함께 읽는다. 출처 행이 없더라도 연결은 보존하고 `asset_task_ids` 출처와 `legacy_link_missing` issue를 반환한다.
2. `task_scope`의 **범위 일치**. 초기 v1은 `contextCoverageCTE`의 조건 그대로 company ID·root_domain·domain·`try_inet(a.ip)`의 IP/CIDR·정규화된 ICP 일치를 사용한다. `scopeTargetCTE`의 IP-literal domain 확장과 합치지 않는다. `keyword`는 정형 일치가 없어 자산을 추정하지 않고 issue를 반환한다.
3. 해당 exploration의 **공개 가능한 앵커**. `intent/fact/finding` 참조만 제공한다. 원천 intent는 `done/blocked/exhausted/stopped`만 공개하고 선택 집합에서도 같은 조건을 적용한다. 다른 종류만을 근거로 자산을 추가하지 않는다.

연결 행만 있고 배열에는 없는 불일치는 연결 근거로 채택하지 않는다. 다른 정상 근거로 이미 공개 가능한 자산이면 충돌 issue로 표시한다. 숨겨진 자산이나 외부 task ID를 issue로 노출하지 않는다. 안전한 문맥 자체를 구성할 수 없으면 409다.

source task 자산은 `현재 Task → SourceTask → Asset`으로 표시한다. 현재 task의 직접 연결 행을 복제하지 않는다. 상속 정보는 이 조회에서 읽기 전용이며 source task의 policy/run/reset 권위를 현재 작업에 재결속하지 않는다.

### 3.2 실행·세대 경계

일반 자산·앵커에는 공통 run/reset 결속이 없으므로 자산 응답은 `scope.kind=task_context`, `execution_scope=null`이다. fact 생성 시각·task 최신 시각으로 현재 실행 근거를 추정하지 않는다.

지도는 별도로 서버가 선택한 단일 task/run/reset/KG snapshot에 한정한다. registry의 준비 `run_id`, E② `Scope.RunRef`, 평가 run ID는 서로 대체하지 않는다. 직접 상속한 과거 지도·판정을 현재 세대에 재사용하지 않는다.

## 4. 노드·관계·상태·출처

### 4.1 노드와 방향

| 요소 | 제안 wire 표현 | 규칙 |
|---|---|---|
| Task | `kind=task`, `id=t:<task_id>` | `task_id`, `label`, `role=current/source` 필수. source는 읽기 전용 문맥 |
| Asset | `kind=asset`, `id=a:<asset_id>` | `asset_id`, `asset_type`, `label`, `context_state`, `observation_state` 필수 |
| Observation | `provenance[].observation` | 별도 DB·그래프 노드 대신 기존 탐색 참조. 필요하면 UI가 출처 패널로 표시 |
| 작업 포함 | `task_asset`, Task→Asset | `basis=attached/scope/anchor`로 명시 연결·범위 파생·앵커를 구분 |
| 자산 포함 | `contained_by`, 자식→부모 | 결정적인 저장 필드에서만 파생. 양쪽 노드가 이미 조회 자산 집합에 있어야 함 |
| 직접 원천 | `source_task`, 현재 Task→SourceTask | 저장된 직접 관계. `basis`는 생략 |
| Principal/Resource/owns/access | 기본 자산 응답에 없음 | §6의 기존 지도와 후속 근거 계약에 분리 |

회사·가상 루트는 초기 v1 노드가 아니다. 자산 label의 안전 기본값은 `루트 도메인/하위 도메인/IP/애플리케이션/서비스/엔드포인트 #<id>`, task는 `작업 #<id>`다. URL·query·userinfo·제목·자유 문구를 label로 복사하지 않는다. 더 풍부한 안전 라벨은 D02로 남긴다.

포함 관계의 초기 허용 규칙은 다음으로 제한한다. 이는 실제 호스팅·DNS·소유 증명이 아닌 **저장 토폴로지 파생**이다.

- `root_domain`: child가 subdomain이고 `child.root_domain == parent.domain`, parent가 root_domain인 유일한 조회 자산일 때.
- `service_domain`: child가 service이고 비어 있지 않은 `child.domain == parent.domain`, parent가 subdomain인 유일한 조회 자산일 때.
- `service_ip`: domain 부모가 없고 비어 있지 않은 `child.ip == parent.ip`, parent가 ip인 유일한 조회 자산일 때.
- `endpoint_origin`: child가 endpoint, parent가 HTTP service이며 두 URL의 정규 `(scheme, host, effective port)`가 같고 parent URL이 origin 루트(`/` 또는 빈 경로, userinfo/query/fragment 없음)인 유일 후보일 때. host는 ASCII 소문자 또는 정규 IP, 기본 포트는 http=80/https=443이다. 파싱·정규화가 불가능하면 생략한다.
- 해당 단계 후보가 여러 개면 fallback·최신·작은 ID 우선 선택 없이 관계를 생략하고 `containment_ambiguous` issue를 남긴다. URL 경로 접두사만으로 객체 포함·Asset↔Resource 대응을 만들지 않는다.

### 4.2 독립 상태 축

| 축 | 제안 값 | 의미·현재 산출 범위 |
|---|---|---|
| 자산 문맥 | `included` | §3.1의 작업 문맥에 포함. 대상 송신 승인 아님 |
| 실제 관측 | `observed/unknown` | observed는 자산과 결속된 신뢰 관측 필요. **기본 v1은 unknown만 산출**하며 enum observed는 후속 어댑터 계약 전까지 비활성 |
| 관계 근거 | `stored/derived/insufficient/conflict` | 엣지는 stored/derived만 산출. 부족·상충은 issue.state로 분리하고 확정 포함 엣지로 그리지 않음 |
| 진단 | `not_run/insufficient_observation/undecidable/decided/unknown` | 아래 매핑에 따른 의미 구분. 기본 자산 v1에서는 조회·산출하지 않음 |

- `stored`는 저장된 연결·참조를 확인했다는 뜻이며 참조가 담은 주장의 참을 뜻하지 않는다. `derived`는 정형 조건 일치를 뜻한다.
- 원 탐색 상태는 `observation.source_state`로만 보존한다. `fact.confirmed`, `intent.done`, `confidence=observed`를 자산 관측이나 E③ 판정으로 승격하지 않는다.
- `not_run`은 검사계획과 명시적 미시도 근거가 있을 때만 가능하다. 레코드 부재·범위 자산·`tested=false`로 만들지 않는다. 현행 지도 Phase 2 미구현 경계를 유지한다.
- `insufficient_observation`은 실제 시도와 부족 근거가 같은 범위에서 확인돼야 한다. 등록 시도 목록이나 `observation_unavailable`만으로 실제 송신을 단정하지 않는다.
- `undecidable`은 검증된 저장 오라클 원값이다. `decided`도 판정 존재 의미이며 취약 확정과 다르다. `suspected/allowed/confirmed` 원값을 유지한다.
- 실행의 `state=completed/unknown`, `outcome=partial`, `closing_reason=budget_limited`, 계정별 결과인 `account_auth`는 서로 다른 필드다. 이를 개별 자산·관계·검사 상태로 바꾸지 않는다. 예를 들어 completed를 모든 자산의 decided로 매핑하지 않는다.
- 미조회와 조회 후 근거 부재는 `sections`로 구분한다. 소유·정책을 해소하지 못하면 unknown이며 allow/deny/resolved_none/untested/na로 채우지 않는다.

### 4.3 출처 DTO

공통 필드는 `id`, `kind`, `task_id`(nullable), `asset_id`(nullable), `source`(nullable), `inherited`, `observation`(nullable)이다. 배열 필드는 항상 배열이며 null로 대체하지 않는다.

| kind | id / 추가 필수 필드 | 근거 |
|---|---|---|
| `task_asset_link` | `link:<task>:<asset>` | 실제 링크 행. `source`는 알려진 저장 값만, 미등록 값은 unknown |
| `asset_task_ids` | `array:<task>:<asset>` | 출처 행 없는 배열 연결. `source=unknown` |
| `task_scope` | `scope:<scope-id>:<asset>`, `scope_id` | 정확히 일치한 scope 행. source는 manual/agent/auto 등 허용 값 또는 unknown |
| `exploration_anchor` | `anchor:<node>:<asset>` | 실제 앵커. source=null, observation에 참조 메타만 포함 |
| `task_relation` | `relation:<task>:<source>`, `source_task_id` | asset_id/source/observation=null |
| `asset_fields` | `fields:<child>:<parent>:<rule>`, `parent_asset_id`, `rule` | task_id/source/observation=null, inherited=false. §4.1 규칙 식별자만 출력 |

`inherited`는 출처를 보유한 task가 직접 원천이면 true다. 전역 자산 필드 파생은 특정 task의 생성 근거가 아니므로 false다. 하나의 Asset에 현재·원천 근거가 함께 있으면 모든 출처를 유지한다.

링크 source 허용 값은 `system/manual/api/agent/task/company/legacy`, scope source 허용 값은 `manual/agent/auto`다. 미등록 원문을 응답에 반사하지 않는다. `source_summary`는 원문 반환·정책 파싱을 하지 않는다.

observation은 `node_id`, `kind=intent/fact/finding`, `source_state`, `claimed_confidence=observed/inferred/unknown`, `authority=model_claim/unverified_reference`를 갖는다. fact payload의 confidence는 허용 값만 읽고 authority=model_claim으로 표기한다. 다른 종류의 confidence는 unknown, authority=unverified_reference다. 자유 origin/owner/payload는 반환하지 않는다.

`source_node_id`는 같은 출처 보유 task의 exploration과 공개 종류·상태에 맞고 해당 자산의 실제 anchor도 있을 때만 observation으로 제공한다. 참조만 있거나 검증이 부족하면 observation=null과 issue를 남긴다. 다른 작업의 node ID나 payload는 공개하지 않는다.

## 5. 응답·식별자·오류

### 5.1 공통 응답

```json
{
  "version": "abc2lab.kg-query/v1",
  "task_id": "41",
  "scope": {"kind": "task_context", "task_ids": ["41"], "source_depth": 1, "execution_scope": null},
  "sections": {"assets": "complete", "provenance": "complete", "ownership": "not_requested", "diagnosis": "not_requested"},
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
}
```

위 ID는 설명용이며 실제 작업이나 진단 결과가 아니다.

### 5.2 유일성·정렬·누락

- DB ID는 선행 0 없는 양의 int64를 **문자열**로 반환한다. JS Number 변환을 계약에 포함하지 않는다. 기존 access-map의 숫자 ID 형식은 변경하지 않는다.
- Task/Asset 키는 기존 PK다. Asset은 여러 작업·출처가 있어도 하나로 합친다. 관계 키는 `(kind, src, dst, basis)`이며 출처 참조는 합집합이다.
- 관계 ID는 `task_asset:<src>:<dst>:<basis>`, `contained_by:<src>:<dst>`, `source_task:<src>:<dst>`다. contained_by는 `basis=asset_fields`, source_task는 basis를 생략한다. task_asset의 anchor/attached는 stored, scope는 derived다.
- Task는 현재 작업 → 직접 원천의 ID 숫자 오름차순, Asset은 ID 숫자 오름차순이다. 관계·출처는 ID의 UTF-8 바이트 사전순, issues는 `(code, state, refs)` 순이며 refs도 바이트 사전순이다. DB의 미지정 순서에 의존하지 않는다.
- 모든 관계의 끝점·provenance_refs와 issue의 refs는 해당 응답의 안전한 노드·출처 참조로 해소돼야 한다. 과거 페이지·다른 작업의 존재를 가정하지 않는다.
- 배열은 필수다. Asset 노드에는 소유자·판정 필드가 없다. `execution_scope=null`은 일반 자산에 실행 결속이 없음, `observation=null`은 해당 출처에 공개 가능한 관측 참조가 없음, `next_cursor=null`은 다음 페이지 없음을 뜻한다.
- null과 생략을 대용하지 않는다. 종류별로 해당하지 않는 추가 필드만 생략한다. source_task 관계의 basis 생략도 명시된 계약이다.
- 정상 빈 결과는 현재 Task 노드만 있고 edges/provenance는 빈 배열이다. 조회한 sections는 complete다. 미조회 ownership/diagnosis는 not_requested이며 근거 0건이라는 뜻이 아니다.
- v1의 알려진 필드·종류·상태 밖의 응답은 클라이언트가 계약 오류로 처리하고 그래프를 공개하지 않는다. unknown 변환은 앞에서 명시한 미등록 source·confidence에만 적용한다. 서버의 unknown과 JSON 손상을 혼동하지 않는다.

### 5.3 크기·페이지

초기안은 전체 그래프 한 응답이며 묵시적 절단·more/cursor를 지원하지 않는다. 후보 상한은 **Task 포함 노드 2,000·관계 5,000·출처 5,000·issues 5,000·JSON 응답 16MiB·DB 조회 5초**다. 모든 건수·바이트 상한 이하여야 하며 하나라도 초과하면 413, 시간·DB 실패는 503이다.

이는 기존 탐색 그래프의 노드/관계 상한과 지도 조회의 시간/바이트 상한을 참고한 **리뷰 대기 제안값**이다. 기존 자산 그래프의 제한이나 실측된 용량이 아니다. 상한+1을 검출할 수 있는 유계 조회를 사용하고 전체를 메모리에 올린 뒤 자르지 않는다. 대규모 운영·페이지 필요 여부는 D04에서 결정한다. 기존 자산 목록의 페이지 조회는 유지하지만 이 그래프의 누락을 보완한 것으로 간주하지 않는다.

### 5.4 문제·실패

국소 근거 부족은 issues의 code, state=insufficient/conflict, refs 배열로 표시하고 검증된 별도 근거에 따른 게시를 지우지 않는다. 초기 코드는 다음으로 제한한다.

| code | state | 처리 |
|---|---|---|
| `legacy_link_missing` | insufficient | 배열의 연결은 유지, 상세 출처 미확인 |
| `membership_mismatch` | conflict | link와 배열 불일치. 해당 link를 포함 근거로 사용하지 않음 |
| `source_reference_unverified` | insufficient | source_node 공개 자격·앵커 결속 부족. observation=null |
| `scope_kind_not_projected` | insufficient | keyword에서 파생 자산을 추정하지 않음 |
| `containment_ambiguous` | insufficient | 부모 선택 불가, 포함 관계 생략 |
| `unknown_source` | insufficient | source 원문을 내보내지 않고 unknown |

기계적 모순과 자유 문구 fact 사이의 의미적 상충을 구분한다. 후자를 자동 판별할 키·정본은 없으므로 fact 수나 문구 비교만으로 conflict를 만들지 않는다.

```json
{
  "version": "abc2lab.kg-query/v1",
  "error": {"code": "kg_scope_unavailable", "retryable": false}
}
```

| HTTP | 고정 code | 의미 / retryable |
|---|---|---|
| 400 | `kg_request_invalid` | task/query/경로 오류 / false |
| 401 | `kg_auth_required` | Bearer 없음·무효 / false |
| 403 | `kg_task_forbidden` | task/source 권한·결속 위반 / false |
| 404 | `kg_task_not_found` | 현재 task 없음·삭제 / false |
| 405 | `kg_method_not_allowed` | GET 외 메서드 / false |
| 409 | `kg_scope_unavailable` | 안전한 단일 문맥 확인 불가 / false |
| 413 | `kg_graph_too_large` | 건수·바이트 상한 / false |
| 503 | `kg_store_unavailable` | DB·읽기 권한·시간 초과 등 / true |

이 표는 v1 handler의 오류 계약이다. 제품 중계에서 미허용 메서드·경로를 먼저 거부하는 기존 403은 유지한다. 인증 전에 task 존재를 상세 판별하지 않는다. 오류에 SQL·파일 경로·JWT·세션·canary·원문 source_summary/payload를 포함하지 않는다. 실패 시 nodes=[]나 예시를 반환하지 않는다. retryable은 재조회 가능성 안내이지 진단 재실행 권한이 아니다.

## 6. 소유·인가와 기존 access-map

기존 [access-map-read](access-map-read.md)의 책임은 현재의 단일 등록·실행·세대, horizontal/read의 저장 근거에서 셀 상태를 얻는 것이다. 자산 v1은 소유·접근·취약점을 판정하지 않는다. UI는 각 API의 조회 상태를 별도로 다루고 한쪽의 성공으로 다른 쪽의 실패를 숨기지 않는다.

기존 MapResources의 owner_ref는 등록 소유자 참조지만 등록 목록만으로 정책 값·유효창을 검증할 수 없다. 일반 owns/access를 추가하기 전에 최소한 다음을 검증하는 읽기 계약이 필요하다.

1. 동일 task/exploration/intent·등록·실행 Scope.RunRef·모드 버전.
2. 전체 resource_ref, owner/evaluated의 등록 주체 체계. E② resources.accounts는 송신 가능 계정 목록이지 소유자가 아니다.
3. 준비 run_id와 실행 run의 명시적 결속, reset_generation, generation_ref, instance_ref.
4. KG snapshot_ref·정본 내용 digest·policy_version. policy_version은 `kg-fixture/v1:<snapshot_id>:<64hex digest>` 원값을 유지한다.
5. 등록 출처, ExpectedAccess, Sharing, Delegation, 유효창. 관측을 동반한 표시는 observed_at으로 검증하고 과거 유효성을 현재의 무기한 권한으로 만들지 않는다.
6. 판정은 발급 기록·canonical payload/digest·Source·evidence manifest·revision·완료/최종화의 일치. 복수 후보는 우선순위로 선택하지 않는다.
7. Asset과 Resource를 연결하려면 대상·실행·세대를 갖는 명시적 저장 대응. URL/ID 유사성·LLM 추론은 근거가 아니다.

일반 자산에는 이 결속이 없고 현재 MapResources에도 정책 값·유효창이 없다. GET이 만료된 실행 핸들이나 보호 입력을 다시 열어 보완하면 안 된다. 신뢰된 snapshot의 필요한 비밀 없는 항목을 등록 시 보관하는 확장은 별도 계약·리뷰 대상으로 둔다. fixture 정본 형식·저장 구조를 이 제안만으로 변경하지 않는다.

기존 access-map은 현재 포인터·발급 기록·Source·manifest를 읽기 전용으로 대조한다. 미검증·부재·선택 불가는 unknown이며 `observation_unavailable`와 저장된 `oracle_value=undecidable`을 구분한다. `allow-ok`는 등록 정책 외에도 동일 자원·정상 교차 응답·검증 신원·관측 시각의 유효창이 필요하다. `deny-ok/untested/na`를 새 자산 KG에서 보충하지 않는다.

## 7. JSON 사례 8개

다음은 계약 설명용 합성 ID이며 실제 진단 결과가 아니다. **E01은 §5.1 전체 응답**이다. E02~E05/E07은 E01에 대한 최상위 필드 교체 JSON이며 지정하지 않은 최상위 필드는 E01을 유지한다. 중첩 객체를 묵시적으로 merge하지 않는다. E06/E08은 별도 API access-map의 셀 발췌이며 자산 v1 wire 응답이 아니다. 이 분리도 리뷰 대상이다.

### E01 정상 작업 연결 자산

§5.1. task_ids와 대응 link 행이 있으며 stored 연결을 제공한다. 관측·소유를 추정하지 않는다.

### E02 수동 등록

```json
{"provenance": [{"id": "link:41:101", "kind": "task_asset_link", "task_id": "41", "asset_id": "101", "source": "manual", "inherited": false, "observation": null}]}
```

수동 연결 사실만 확인한다. 원래 자유 문구 summary는 내보내지 않는다. API 일괄 등록의 source=api, 작업 생성 시 source=task도 같은 원칙이다.

### E03 fact 관측 참조 있음

```json
{
  "edges": [
    {"id": "task_asset:t:41:a:101:anchor", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "anchor", "state": "stored", "provenance_refs": ["anchor:901:101"]},
    {"id": "task_asset:t:41:a:101:attached", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "attached", "state": "stored", "provenance_refs": ["link:41:101"]}
  ],
  "provenance": [
    {"id": "anchor:901:101", "kind": "exploration_anchor", "task_id": "41", "asset_id": "101", "source": null, "inherited": false, "observation": {"node_id": "901", "kind": "fact", "source_state": "confirmed", "claimed_confidence": "observed", "authority": "model_claim"}},
    {"id": "link:41:101", "kind": "task_asset_link", "task_id": "41", "asset_id": "101", "source": "agent", "inherited": false, "observation": null}
  ]
}
```

모델이 observed로 기록해도 Asset은 observation_state=unknown이다. link 자체의 source_node가 미지정이어도 독립 앵커는 보존한다.

### E04 직접 원천에서 상속

```json
{
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
  ]
}
```

41/40 모두 읽기 권한이 있는 경우다. 40의 원천 39는 포함하지 않고 41의 직접 attached를 위조하지 않는다.

### E05 관측 없는 범위 자산

```json
{
  "edges": [{"id": "task_asset:t:41:a:101:scope", "kind": "task_asset", "src": "t:41", "dst": "a:101", "basis": "scope", "state": "derived", "provenance_refs": ["scope:20:101"]}],
  "provenance": [{"id": "scope:20:101", "kind": "task_scope", "scope_id": "20", "task_id": "41", "asset_id": "101", "source": "manual", "inherited": false, "observation": null}]
}
```

scope와 저장 자산 필드의 일치다. 대상 전송 허가·미실행·안전 판정이 아니다.

### E06 검증된 등록 소유·관측 시각의 허용

`GET /api/tasks/41/access-map`의 단일 유효 셀 발췌다. 실제 응답의 task/run/reset/policy_version과 다른 신호·셀은 기존 계약을 따른다.

```json
{
  "principal": "acct-b", "owner_ref": "acct-a", "resource_ref": "example-resource:1", "resource_label": "example-resource:1",
  "state": "allow-ok", "oracle_value": "allowed", "applicable": true,
  "record_id": 301, "case_ref": "case-1", "observed_at": "2026-11-17T00:00:00Z",
  "projection_error": null
}
```

동일 등록·실행·세대·정본·유효창·관측을 검증한 사례다. 등록 owner_ref와 관측 시각의 허용은 표시할 수 있지만 자산 대응이나 현재의 포괄적 권한을 새로 발급하지 않는다. E06을 자산 v1의 owns/access 엣지로 자동 변환하지 않는다.

### E07 소유 근거 없는 자산·자원

```json
{"sections": {"assets": "complete", "provenance": "complete", "ownership": "not_requested", "diagnosis": "not_requested"}}
```

UI는 소유 미확인·미조회로 표시한다. 별도 정책 조회에서 근거가 없으면 unknown을 유지한다. not_requested를 조회 성공·근거 없음으로 바꾸지 않는다. Asset에서 미확인 Resource나 owner_ref를 생성하지 않는다.

### E08 상충하는 관측·판정 또는 선택 모호

같은 현재 셀에 복수 저장 판정이 있는 access-map 발췌다. 상충하는 값이 있거나 동일 판정이 여러 개여도 단일 선택하지 않는다.

```json
{
  "principal": "acct-b", "owner_ref": "acct-a", "resource_ref": "example-resource:1", "resource_label": "example-resource:1",
  "state": "unknown", "oracle_value": null, "applicable": true,
  "record_id": null, "case_ref": null, "observed_at": null,
  "projection_error": "selection_ambiguous"
}
```

자산 v1의 복수 fact는 원래 참조를 모두 보존하고 문구를 읽어 최신·confirmed 우선 선택을 하지 않는다. 자유 문구만의 상충은 기계적으로 판단할 수 없는 한계다. 구조적 연결 근거가 실제로 모순되면 membership_mismatch/conflict로 분리한다.

## 8. 구현 경계와 기존 계약의 차이

| 부분 | 제안 범위 |
|---|---|
| 재사용 | PostgreSQL 자산 PK·기존 유형, 연결·직접 원천·scope 일치 조건, 공개 가능한 앵커, access-map 기존 검증 |
| 새 읽기 어댑터 | 한 read-only 트랜잭션의 배치 조회, 중복 제거·출처 DTO·상태·유계 출력. 기존 helper 여러 번 호출을 하나의 snapshot으로 간주하지 않음 |
| 제품 중계 | 정확한 v1 GET과 기존 access-map GET을 개별 검토. task/source 인증·소유권, query·응답 범위 검증. 범용 proxy를 열지 않음 |
| UI | 기존 표현 재사용, 포함과 출처 구분, 상속/읽기 전용 표시, 정책 미조회와 실패 구분 |
| DB | 기본 v1은 DDL·migration 불필요. 일반 소유/인가 저장 확장은 후속 승인 계약 |
| 비변경 | E② 전송 허용·E③ 판정기·fixture 정본·기존 그래프 저장 형식·기존 access-map 형식 |

`kg-policy` §1의 과거 설명은 company를 자산 유형으로 적지만 현재 schema에서는 별도 테이블이다. 이 문서는 현재 구현의 6개 유형을 기준으로 하고 원문 계약을 임의 개정하지 않는다.

기존 coverage-graph와의 차이는 (a) 출처 보존, (b) tested를 진단 완료로 승격하지 않음, (c) 명시적 연결 포함, (d) 애매한 부모·회사·가상 루트 미추가, (e) 상속 비종료 intent 비공개, (f) 원문 URL 등의 공개 DTO 복사 금지다. 기존 무인자 응답 호환성을 유지하고 새 view에만 적용한다.

## 9. 계약 시험 계획

다음은 구현 때 필요한 독립 시험이며 **이 문서 PR의 구현·실행 PASS가 아니다**.

| ID | 조건 | 기대 결과 |
|---|---|---|
| Q01 | 수동·API·작업 생성·자동·legacy 연결 | source와 연결 근거를 보존하고 실제 관측으로 승격하지 않음 |
| Q02 | 연결 없는 scope 자산, keyword, IP-literal domain 차이 | 지정 CTE 조건만 사용. 미지원 scope 명시, 범위 확대 없음 |
| Q03 | origin fact·confirmed fact·inferred confidence·intent.done | observed/진단 완료/취약 확정을 만들지 않음 |
| Q04 | 배열만 존재, link만 존재, foreign source_node | 연결과 출처 부족 구분. 외부 node ID·payload 비공개 |
| Q05 | 41→40→39의 직접/간접 원천 | 40만 포함. 원천 삭제·권한 변경 후 재조회에 혼입 없음 |
| Q06 | 원천 intent running/paused/deleted 또는 종료 4상태 | 비종료는 출처/선택에서 제외, 종료만 읽기 전용 상속 |
| Q07 | 다른 task·운영자·정상 서명 다른 subject·query 인증 | 401/403. 범위·존재를 빈 성공이나 오류 상세로 누출하지 않음 |
| Q08 | 같은 Asset의 여러 scope/anchor/현재/원천 | 노드 하나, 연결 종류·출처 모두 보존, 정렬 일정 |
| Q09 | 복수 service·같은 host의 다른 port/scheme·경로 접두사 | 애매한 부모나 Asset↔Resource 대응 미생성 |
| Q10 | 기존 지도의 run/reset/instance/snapshot/digest/mode/유효창 불일치 | 기존 fail-closed 유지, 현재 owns/access에 재사용 금지 |
| Q11 | 같은 셀 복수 판정·미확정/allowed/관측 누락 | selection_ambiguous 등 보존, 최신·확정 우선 선택 없음 |
| Q12 | 정상 빈 결과·미조회·DB 실패·JSON 오류 | 별도 상태·HTTP. 예시 fallback·blank/untested 합성 없음 |
| Q13 | 모든 상한 경계·초과, 큰 int64, 조회 순서 변경 | 절단 없는 413, ID 정밀도·결정적 출력·유계 메모리 |
| Q14 | summary/URL/query/payload/auth의 합성 비밀·canary | 성공·실패 응답/공유 로그에 원문 없음, 정상 참조 보존 |
| Q15 | 실제 제품 HTTP→관리 bridge→실행 서버→일회용 PostgreSQL | 정확한 task/source만 GET 성공. 준비 전용·다른 task·미승인 쓰기 거부 |
| Q16 | v1 조회와 source 변경 경쟁, 조회 중 권한 철회 | DB snapshot 일관성·응답 전 권한 검증. 이전 결과로 실패를 숨기지 않음 |
| Q17 | 읽기 전용성 계측, GET/HEAD/POST와 잘못된 query | 진단 시작·모델·대상 HTTP·E②·E③ 발급·DB 변경 0건 |
| Q18 | 기존 무인자 coverage-graph·access-map 회귀 | DTO·선택·판정·세대·비밀 보호의 기존 계약 보존 |

## 10. Open Decision과 리뷰 책임

모두 **제안·담당자 확인 대기**다. 이경준 등의 동의나 팀 합의 완료를 문서 작성만으로 선언하지 않는다.

| ID | 결정 항목·추천안 | 협의 담당 / 결정에 필요한 근거 |
|---|---|---|
| D01 | coverage-graph의 view=kg-v1 재사용, 무인자 호환 | 이경준·이동찬 / UI 소비·기존 route 가드·API 책임 분리 |
| D02 | `a:<id>`와 안전한 유형+ID 라벨, Observation은 참조 패널 | 이경준 / 가독성, 비밀 없는 추가 라벨의 저장 근거 |
| D03 | 포함·관측 출처 별도 표시, 원천은 명시적 읽기 전용 링크 | 이경준·이동찬 / 화면안, 원천 task 읽기 권한과 경쟁 시험 |
| D04 | 전체 응답·명시적 상한 실패, 페이지는 후속 | 이경준·이동찬 / 실제 자산·출처 건수·응답 크기·UI 지연·페이지 요구 |
| D05 | 근거 없으면 소유 미확인, 미조회와 조회 실패 분리 | 이경준·최민준 / 표시 문구, 정책 unknown과 관측·판정의 의미 |
| D06 | 접근통제 판정은 기존 access-map, 자산 대응 추정 금지 | 이경준·이동찬·최민준 / §6 저장 결속·Resource 대응·세대/유효창 |
| D07 | fixture 정본 유지, 비밀 없는 읽기 projection만 별도 제안 | 이경준·최민준·이동찬 / 정책 의미·정본 writer 책임·resolver/저장·읽기 권한 |

리뷰 분담안: 이경준은 KG 의미·UI 소비·라벨·레이어, 이동찬은 서버/DB 읽기·제품 중계·격리, 최민준은 registry/resolver·Source·E③ 기록 의미를 검토한다. 정본 등록 책임을 이관하거나 각자의 개인 지침/STATUS를 바꾸는 제안이 아니다.

완료 구분: **구현 조사·계약 초안=문서화 / 팀 합의=미완료 / API 구현=미착수 / 계약·제품 수용시험=NOT_RUN**. Open Decision의 담당자 답변을 기록해야 계약 채택으로 표시하고, 이후 구현·시험 결과를 별도로 보고한다.
