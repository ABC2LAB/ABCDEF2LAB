# 접근통제 지도 Phase 2 셀 상태(`untested`·`na`) — 의미·소비 계약 제안

작성: 2026-11-10 · 이경준 · **팀 리뷰용 제안, 미확정.** 서버 projection·저장·E① 구현은 이동찬 lane이고, 여기서 고정하는 것은 **표시 의미와 소비 규칙**이다.
검토 기준: `dev` `9b948d8`.
연결: [access-map-read](./access-map-read.md)(1단계 구현 계약) · [kg-policy §4.2·§6 Open #7 (a)](./kg-policy.md#42-접근통제-지도-실데이터-읽기-어댑터-open-6-구체화--verdict-record-open-4-공동-응답) · [kg-fixture-registry §2](./kg-fixture-registry.md) · [verdict-record](./verdict-record.md) · E①(`agent/planner.go:303` — 탐색 종료·재계획, 포크 SHA 참조점)

이 문서는 [kg-policy §6 Open #7 (a)](./kg-policy.md#6-open--팀-정렬-필요)("Phase 2 `untested`·`na`: … **이경준 계약 제안 예정**")의 응답이다. [access-map-read](./access-map-read.md) "셀 선택·상태"가 미산출로 남긴 두 상태를 산출 가능하게 하는 근거·발행·소비를 정한다. 구현 완료나 팀 최종 합의 선언이 아니다.

## 0. 범위

고정하는 것:
- **Phase 2 두 셀 상태**: `untested`(미시도)·`na`(적용불가). 각 상태의 **의미**, 산출에 필요한 **양의 근거**, projection이 내보낼 **신호 제안**(→ 이동찬), UI가 받는 **소비·교차검증 계약**(내 lane).

비범위:
- `deny-ok`(Phase 3 — 앱 인가 차단 관측 계약 선행, [kg-policy §4.2(3)](./kg-policy.md)). 재검증 `evidence_revision`·이력(Open #7 (c)).
- 서버 projection read·관측/판정 저장 **구현**(이동찬), 판정 로직·case 산출(최민준), E① 플래너 **내부 구현**(이동찬). 레지스트리 물리 스키마(최민준 [kg-fixture-registry](./kg-fixture-registry.md)).

전제:
- **Phase 1 분기를 바꾸지 않는다.** 이 제안은 [access-map-read](./access-map-read.md)의 "0건: 레코드 없음 분기" 안을 세분할 뿐, 1건·2건 이상(`selection_ambiguous`) 분기와 `observation_unavailable`≠`undecidable` 분리는 그대로다.
- 색 추정 금지·fail-closed는 Phase 1과 동일하게 유지한다([kg-policy §4.1(c)](./kg-policy.md)).

## 1. 지금 경계 (코드·계약 현황)

**서버 계약**([access-map-read](./access-map-read.md)):
- 1단계 `state` ∈ `confirmed`·`suspected`·`allow-ok`·`unknown`·`blank`. `untested`·`na`는 "Phase 2 … 미산출"로 명시.
- `applicable` ∈ `true`(정책에 평가 주체 등록)·`null`(등록 근거 없음). **`false`는 "1단계 미산출"**.
- 레코드 0건 분기: 시도 후 누락 `record_missing_after_attempt`(unknown) · 적용 근거만 있고 관측 미확인 `observation_unavailable`(unknown) · 성공한 조회에서 관련 근거 전무 `blank`.

**클라 seam**([web/src/lib/diagnosis/kg.ts](../../web/src/lib/diagnosis/kg.ts)):
- #353(Workbench 단일화)으로 지도 화면이 빠져 지금은 **seam만 남아 있다**(화면 재부착은 이경준 후속). 아래 현황은 옮겨진 파일 기준이다.
- `AccessDecision` 타입에 `untested`·`na`가 **이미 있다** — 라벨(`미검사`·`—`), 셀 클래스(`cell-none`·`cell-na`), `KNOWN_DECISIONS` 포함. 색·집계 자리는 준비됨.
- 그러나 `API_STATE_DECISION`은 Phase 1 상태만 매핑한다 → 서버가 지금 `state="untested"`나 `"na"`를 보내도 **매핑 없음 → `미등록 state`로 fail-closed unknown**. 즉 UI는 실데이터 경로에서 아직 두 상태를 **신뢰해서 받지 않는다**. `kg.ts` MOCK이 `untested`를 보여 주는 것은 **예시 표시일 뿐** Phase 2 동작 근거가 아니다.
- `applicable`은 `cellNote`에서만 소비된다(`false`→"적용 불가 근거 있음", `null`→"적용 여부 미확인"). **색(decision)을 결정하지는 않는다.**

따라서 Phase 2 작업은 둘로 갈린다: **(서버)** `state`에 두 값을 추가 발행 + 뒷받침 근거 필드 결속 / **(UI)** 두 값의 `state→decision` 매핑과 교차검증 추가.

## 2. 핵심 구분 — 왜 어려운가 (fail-closed)

"레코드 없음"으로 보이는 셀 안에 **다섯 사정**이 섞여 있다. Phase 2는 뒤 둘을 **부재가 아니라 양의 근거**로만 갈라낸다.

| 사정 | 의미 | 상태 | 단계 |
|---|---|---|---|
| 시도했으나 레코드 누락 | 전송은 했는데 판정/관측 레코드가 없음 | `unknown`(`record_missing_after_attempt`) | Phase 1 |
| 적용 근거만 있고 관측 미확인 | 계획·정책은 있는데 관측을 확인 못 함 | `unknown`(`observation_unavailable`) | Phase 1 |
| 조회 성공 + 모든 근거 전무 | 레코드·시도·커버리지·applicability 다 없음 | `blank` | Phase 1 |
| **검사 계획에 있으나 명시적 미시도** | 보기로 한 칸인데 시도 자체를 안 함 | `untested` | **Phase 2** |
| **명시적 적용불가 근거** | 설계상 평가 대상이 아님(명시 선언) | `na` | **Phase 2** |

**불변식(절대규칙 5):** `untested`·`na`는 **양의 근거가 있을 때만** 산출한다. 부재·침묵에서 추론하지 않는다. 특히 —
- 레코드 부재 ⇏ `untested` (시도 여부를 모르면 `unknown`/`blank`).
- 커버리지(자산 scope) 부재 ⇏ `untested` ([kg-policy §4.2(3)](./kg-policy.md): "현재 coverage로는 불가").
- `owner==evaluated` ⇏ `na`. 소유자 셀은 등록 정책 있으면 `observation_unavailable`, 없으면 `blank`([access-map-read](./access-map-read.md)).
- `applicable=null` ⇏ `na`. `null`은 "등록 근거 없음"이지 "명시적 적용불가"가 아니다.
- E② 사전 거부·네트워크 실패·송신 0건 ⇏ `untested`. 이는 `unknown`이다(송신 0건 ≠ 미시도, [kg-policy §4.2(3)](./kg-policy.md)).

## 3. `na` (적용불가 · `applicable=false`)

### 3.1 의미
셀 `(principal, resource_ref)`가 **설계상 평가 대상이 아님**을 명시적 근거로 선언한 상태다. "아직 안 봤다"(`untested`)도, "봤는데 모른다"(`unknown`)도, "소유자라 해당 없음 추정"도 아니다.

왜 별도 상태가 필요한가: 축소 격자에서 애초에 평가 주체가 아닌 칸을 `untested`나 `blank`와 섞으면 커버리지·시각화가 "검사 안 한 공백"으로 오도된다. 명시적 `na`는 "설계상 범위 밖"을 정직하게 분리해, 분모(검사 대상)에서 빼야 할 칸을 드러낸다.

### 3.2 근거 출처 (제안 — 저장·스키마는 owner)
- `applicable=false`는 **검사 계획/정책의 명시적 비적용 선언**에서만 온다. 현재 레지스트리 `resources[].policies[]`는 **적용 pair만** 선언하므로(→ `applicable=true`), **비적용을 선언할 수단이 지금 없다.** 새 근거가 필요하다(§7 Open #1).
- 후보 두 가지 — 어느 쪽이든 **명시적 선언**이어야 하고 부재 추론은 금지:
  - (A) 레지스트리 `resources[]`에 자원별 **out-of-scope principal** 선언(최민준 [kg-fixture-registry](./kg-fixture-registry.md) 확장). **비용**: 레지스트리 §2는 객체마다 다른 키를 금지하고 `format`이 정확히 `abc2lab.kg-fixture-registry/v1`이므로, 키를 더하면 현재 해석기가 레지스트리 전체를 `registry_format`으로 거부한다 → 형식 버전(`/v2`)과 해석기를 함께 바꿔야 한다.
  - (B) E①/검사 계획이 "이 `(resource_ref, principal)`은 계획상 비대상"을 명시 기록(이동찬).
- **금지 추론**: 레코드 부재, `owner==evaluated`, `applicable=null`, 평가 정답 목록(절대규칙 7)에서 `na`를 끌어내지 않는다.

### 3.3 projection 발행 (제안 → 이동찬)
- `state="na"` ⟺ `applicable=false`로 **1:1**. 둘이 어긋나면(한쪽만 설정) 불일치이므로 projection은 그 셀을 **projection-error(예: `na_evidence_mismatch`)로만** 내린다. `blank`(근거 전무)·`observation_unavailable`(적용 근거 있음)로 내리지 않는다 — `applicable=false` 자체가 근거라 두 상태의 정의와 부딪치고, 불일치가 정상 색 안에 묻힌다.
- `na` 셀: `oracle_value`·`record_id`·`case_ref`·`observed_at`은 `null`. `reason_codes`·`canary_fields`는 `[]`. 적용불가 **근거 참조**는 별도 필드(라벨/코드만, 값·URL·계정 원문 금지 — 절대규칙 2).

### 3.4 UI 소비 (내 lane — 구현 가능)
- `API_STATE_DECISION`에 `na: "na"` 추가.
- **교차검증(fail-closed)**: `state="na"`인데 `applicable !== false`면 색을 주지 않고 `unknown` + projection-error "na 근거 불일치". 역방향(`applicable===false`인데 `state!=="na"`)도 §3.3으로 서버가 막되, UI도 같은 fail-closed로 `unknown` + projection-error "적용불가 근거와 충돌"로 내린다(§5).
- 집계·표시: `countDecisions`의 `na`는 이미 0 초기화됨. 라벨 `—`, 셀 `cell-na`, 배지 없음. **헤드라인 커버리지 분모에서 `na`를 빼는 집계 보조는 §7 Open #5에서 접근성과 함께 확인.**

## 4. `untested` (미시도) — E① 선행

### 4.1 의미
셀이 **검사 계획에 포함**(⇒ `applicable=true`)되었고 **명시적으로 시도되지 않았음**을 같은 scope에서 확인한 상태다. "시도했으나 레코드 누락"(`unknown`/`record_missing_after_attempt`)·"조회 성공+근거 전무"(`blank`)와 엄격히 구분한다.

### 4.2 왜 현재 coverage로 불가 ([kg-policy §4.2(3)·(5)](./kg-policy.md))
현재 coverage는 **자산 scope·탐색 fact anchor 집계**다. "이 `(principal, resource, 행동)`을 **검사 계획했고 시도하지 않았다**"를 담지 않는다. 시도 0건이 (계획했으나 미시도)인지 (계획에 없음)인지 (시도했으나 기록 유실)인지 coverage만으로는 못 가른다. 그래서 `untested`는 새 근거가 필요하다.

### 4.3 필요한 두 근거 (제안 → 이동찬 E① · 최민준 case)
두 근거가 **같은 scope**(task/run·reset 세대·정책 snapshot·판정 축 — [kg-policy §4.2(4)](./kg-policy.md))에 결속돼야 한다.
1. **검사 계획(test plan)** — 어떤 `(resource_ref, evaluated_ref, 행동)`을 볼 작정이었나. 출처 = **레지스트리 `policies[]`의 `(resource_ref, evaluated_ref)`로 한정**한다. 계획에 있음 = `applicable=true`의 근거이고, [access-map-read](./access-map-read.md)의 `applicable=true` 정의(레지스트리 정책에 평가 주체 등록)와 같은 출처다. E① 플래너가 레지스트리 밖 `(resource, principal)`을 계획한 경우는 Phase 2 `untested` 대상이 아니다 — 넓히려면 `applicable` 정의 확장을 access-map-read 소유자에게 제안해야 한다(§7 Open #3).
2. **시도 원장(attempt ledger)** — 계획 항목별 상태: (a) **미시도**, (b) 시도했으나 레코드 없음(`record_missing_after_attempt`), (c) 시도→레코드 있음, (d) **시도했으나 미송신**(E② 사전 거부·예산/중지·네트워크 실패, 사유 코드 동반).
- `untested` = 계획에 있음(1) ∧ 시도 원장이 **명시적 미시도 (a)**로 확정. **(b)·(d)는 `unknown`, (c)는 Phase 1 레코드 분기로 간다.** (d)를 따로 두는 이유: 송신 0건 시도를 (a)로 기록하면 차단된 검사가 "미검사"로 보여 §2 불변식(송신 0건 ⇏ `untested`)을 어긴다.

### 4.4 E① 의존성 (왜 선행)
CLAUDE.md E①(탐색 종료·재계획, `agent/planner.go:303`)이 명시 처리하는 것은 '검사완료·발견없음'/'증거부족'/'정책상 검증불가'다. **'미시도'는 들어 있지 않으므로 이 문서가 E①에 새로 추가 요청한다.** '정책상 검증불가'(E② 사전 거부 등)는 이 지도에서 시도 원장 (d) → `unknown`이다. E①이 '미시도'까지 명시 처리해야 projection이 "미시도"를 **양의 신호**로 받아 발행할 수 있다. E① 없이 시도 0건을 미시도로 추정하면 fail-closed 위반이다. 그래서 `untested`는 **E① 선행**이다([kg-policy §4.2(3) "E① 선행"·§6 Open #7 (a)](./kg-policy.md)).

**범위 가드**: 시도 원장은 **조회(읽기) 한정**이다. 상태변경 행동은 얇은 IDOR 경로 밖(절대규칙 6)이라 이 계획/원장에 포함하지 않는다. E② 사전 거부·예산/중지·네트워크 실패는 미시도가 아니라 `unknown`이다(§2 불변식).

### 4.5 projection 발행 (제안 → 이동찬)
- `state="untested"`는 **(계획 결속 + 명시적 미시도 신호)가 둘 다 확인**됐을 때만. 하나라도 없으면 `untested` 금지 → 적용 근거만 있으면 `observation_unavailable`(unknown), 근거 전무면 `blank`로 내린다.
- `untested` 셀: `applicable=true`(계획에 있으니까), `oracle_value`·`record_id`·`case_ref`·`observed_at`은 `null`. 미시도 **근거 참조**는 별도 필드(계획 ref + 시도 상태 코드, 값 없음). `canary_fields` `[]`.

### 4.6 UI 소비 (내 lane — 일부 선행 가능)
- `API_STATE_DECISION`에 `untested: "untested"` 추가.
- **교차검증(fail-closed)**: `state="untested"`인데 `applicable !== true`면 `unknown` + projection-error "미시도 근거 불일치".
- **선행 하드닝(이미 반영됨, 유지)**: `buildCellResolver`는 **projection 미반환 셀을 `untested`로 추정하지 않고** `unknown`("projection 미반환")으로 둔다. 이 제안은 그 가드를 그대로 두고 **서버가 명시 발행한** `untested`만 신뢰한다 — "미반환"과 "명시적 미시도"는 영원히 다른 칸이다.
- note: 계획 결속·시도 상태 코드 요약만(값·URL·계정 원문 없음 — 절대규칙 2).

## 5. 소비 계약 요약 (state → decision + 교차검증)

UI가 서버 `state`를 받아 색으로 확정하는 규칙. Phase 2가 추가하는 두 행이 굵게. 공통 규칙: **`applicable===false`면 `na` 말고는 전부 `unknown`**이고, `blank`는 `applicable=null`일 때만 그린다.

| 서버 `state` | 기대 `applicable` | UI `decision` | fail-closed(어긋나면) |
|---|---|---|---|
| `confirmed` | `false` 아님 | `vuln` | `applicable===false` → `unknown`("적용불가 근거와 충돌") |
| `suspected` | `false` 아님 | `suspected` | `applicable===false` → `unknown`("적용불가 근거와 충돌") |
| `allow-ok` | `false` 아님 | `allow-ok` | `applicable===false` → `unknown`("적용불가 근거와 충돌") |
| `unknown` | (무관) | `unknown` + 사유 | — |
| `blank` | `null` 필수 | `blank` | `applicable!==null` → `unknown`("blank 근거 불일치" — 정책 등록 셀은 access-map-read상 `observation_unavailable`이어야 함) |
| **`untested`** | **`true` 필수** | **`untested`** | **`applicable!==true` → `unknown`("미시도 근거 불일치")** |
| **`na`** | **`false` 필수** | **`na`** | **`applicable!==false` → `unknown`("na 근거 불일치")** |
| (미등록 state) | — | `unknown` | 색 추정 금지("미등록 state") |

`projection_error`가 있으면 Phase 1처럼 **항상 우선**해서 `unknown`으로 내린다([kg-policy §4.2(2)](./kg-policy.md) 별도 네임스페이스). 색 추정 금지·중복 셀 `selection_ambiguous`·미반환 `unknown`은 그대로다. 이 교차검증은 기존 UI 선행 하드닝([kg-policy §4.2(7)](./kg-policy.md))의 Phase 2 확장이다.

## 6. 단계·담당

**Phase 2 분해:**
- **(2a) `na`** — 명시적 비적용 근거 + `state=na` 발행 + UI 매핑. **E① 불필요**(검사 계획/정책의 applicability 선언만 있으면 됨).
- **(2b) `untested`** — E① 시도 원장 선행 **필수**.

**제안 순서: 2a(na) 먼저.** E① 없이 명시적 applicability 근거만으로 닫을 수 있어, Phase 1 뒤 바로 착수 가능하다. 2b(untested)는 E①가 미시도를 양의 신호로 남긴 뒤.

**담당(제안·공동확인 전제, "합의·구현 완료" 아님):**
- 표시 의미·소비·UI 매핑/교차검증 = **이경준**.
- 검사 계획·시도 원장·E① 미시도 신호 = **이동찬**(+ case 결속·판정 필드 의미 = 최민준).
- projection read 발행·근거 필드 저장·결속 = **이동찬**.

## 7. Open — 팀 정렬 필요

1. **`na` 근거 출처** — 레지스트리 비적용 선언(§3.2 (A), 최민준) vs E①/검사 계획 비대상 기록(§3.2 (B), 이동찬). 어디에 둘지. (`applicable=false`의 정본 발급 주체 확정)
2. **`untested` 시도 원장** — E① 산출물 형태·결속 키(계획 ref ↔ 시도 상태 코드)·scope 결속 방식. [verdict-record](./verdict-record.md)의 읽기 계약(`count_basis`)과 중복·정렬되는지(미시도를 거기서 읽나 별도 원장인가).
3. **`applicable`↔`state` 발행 위치** — projection이 둘을 각각 내보내나, 하나에서 다른 하나를 파생하나. §3.3·§5 교차검증의 정본. 레지스트리 밖 E① 계획 항목까지 `untested`로 볼지(=`applicable` 정의 확장, access-map-read 소유자)도 여기서 정한다.
4. **시도 상태 코드 네임스페이스** — 미시도/시도-후-누락/시도-후-레코드/시도-미송신 코드를 projection-error·oracle `reason_codes`와 **분리**해서 둘지([kg-policy §4.2(2)](./kg-policy.md)의 별도 네임스페이스 관례).
5. **축소 화면 시각 구분·집계** — `na`/`blank`/`untested` 3색(`cell-na`·`cell-blank`·`cell-none`)의 접근성 대비, 헤드라인 커버리지 분모에서 `na` 제외 여부·집계 보조.

> 교차참조: 이 문서는 [kg-policy §6 Open #7 (a)](./kg-policy.md)의 "이경준 계약 제안 예정"에 대한 응답 제안이다. [access-map-read](./access-map-read.md)·[verdict-record](./verdict-record.md)·[kg-fixture-registry](./kg-fixture-registry.md) 쪽 역링크나 스키마 변경은 각 문서 소유자(이동찬·최민준) 리뷰로 제안한다 — 직접 편집하지 않는다. 서버 projection·저장·E① 구현은 이동찬 lane이다.
