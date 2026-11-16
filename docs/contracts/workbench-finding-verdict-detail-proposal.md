# Workbench Finding 상세 E③ 판정 조회 계약 초안

**상태: 계약 초안 — 서버 API 미구현, UI 미연결, 팀 리뷰 대기**

작성: 2026-11-16 · 이동찬. 이 문서의 새 경로·DTO·오류 코드·수용시험은 모두 제안이다.
현재 코드의 사실과 후속 공급 조건을 구분하며 E②·E③·UI·평가 코드를 변경하지 않는다.

## 1. 목적·범위·현재 상태

Finding 상세에서 저장된 오라클 원값, 증거 충분성, 진단 요청의 송신 사실을 서로 구별하여 읽는다.
일반 finding의 severity/status/vulnclass, LLM 설명, HTTP 200으로 E③ 판정을 만들지 않는다.

최초 코드 조사 기준은 당시 `origin/dev` **`bfbabf7b06c8cec47319f34b3d388040dcee611c`**다.
요청의 확인 기준 `83c3cb66f36a`는 이 커밋의 조상이다. 그 뒤 변경은 #595의 C14 문서와
#597의 fixture 대상 이름 정비이며, 이 초안의 저장·화면 조사 대상 코드 변경은 없다.
아래 PR 상태는 최초 작성 때의 스냅샷이며, #596 행과 §9는 후속 리뷰의 최종 병합 문서로 갱신했다.

| 구분 | 실제 확인한 범위 | 이 문서에서의 취급 |
| --- | --- | --- |
| 구현된 저장 | [스키마](../../db/e3_finding_schema.sql)의 `e3_evidence_manifests`, `e3_oracle_verdicts`, `e3_verdict_issuances`, `e3_verdict_links` | 새 판정 테이블을 제안하지 않음 |
| 구현된 최초 저장 | [RecordControlledFinding](../../db/e3_findings.go): 신뢰된 Prepared에서 원자 저장. confirmed만 finding과 link를 함께 생성 | 나머지 세 판정값도 finding 없이 저장 가능 |
| 구현된 읽기 | 같은 파일의 `ControlledFindingRecord(findingID)`, [일반 finding 상세](../../server/server.go)의 `getFinding`, [보고서](../../server/diagnostic_report.go), [접근통제 지도](../../server/access_map.go) | 각 읽기 범위와 검증 한계를 아래에서 구분 |
| 구현된 수집·최종화 | [controlled_read_e3.go](../../server/controlled_read_e3.go)의 collector → Prepare → 저장. 완료·checkpoint·권위·revision을 확인 | 조회 handler에서 이 경로를 호출하지 않음 |
| 기존 화면 | [FindingRecord·getFinding/getFindingHeld](../../web/src/lib/diagnosis/finding.ts), [UrlFindingLive](../../web/src/workbench/views/live/UrlFindingLive.tsx) | 두 getter는 목업. 예시 전환과 예시 배너가 있음 |
| 이번 신규 제안 | 단건 읽기 경로와 안전한 투영, 없는 판정·불명·오류를 표현할 envelope | 구현·기동·실진단 수용 완료 아님 |

[verdict-record](verdict-record.md)의 Open #1과 화면 소스의 “저장 미구현” 주석은 과거 단계 기록이다.
현재 구현 판단에는 [최초 저장 경계](e3-initial-storage.md)를 우선 대조했다.

### 1.1 기존 읽기 기능의 재사용 범위

- `ControlledFindingRecord`는 **finding ID**로 link → issuance → verdict의 canonical 바이트와
  payload digest를 읽어 SHA-256을 확인한다. manifest·KG·발급 scope·독립 권위를 전부 재확인하는
  reader가 아니며 finding 없는 판정도 읽지 못한다. 이 함수만 호출해서 새 상세를 완성했다고 할 수 없다.
- 일반 `GET /api/exploration/findings/{id}`는 finding DTO와 저장 report 등을 반환한다.
  `context_task`가 있으면 출처/상속을 검사하지만 E③ 구조화 단건 DTO는 아니다.
- `loadDiagnosticReportWithCaptures`는 읽기 전용 Repeatable Read 트랜잭션을 사용한다.
  finding 링크와 manifest/payload digest, finding/report 증거 버전을 보고,
  finding 없는 issuance도 별도 항목으로 보존한다. 전체 체크·6신호 상세를 제공하는 API는 아니다.
  이 보고서의 allowed → `outcome=negative`는 보고서 내부 분류다. 새 API의 “취약점 없음”으로 복사하지 않는다.
- `diagnostic_report_overview.go`는 task·종료 기록·모델 사용 요약을 읽는다.
  #595가 지적한 인증 뒤 영역 미검사/`account_auth` 보고서 연결의 공백도 남아 있으므로,
  이 요약으로 A/B 신원 검증 성공을 대신하지 않는다.
- `readAccessMapRecords`의 bounded 읽기, canonical/manifest/issuance 대조 규칙과
  `accessMapRecord.valid`의 enum/정책 결과 매핑은 내부 재사용 후보다.
  지도 전용 resource/주체 축 및 단일 binding 전제를 그대로 단건 API에 적용하지 않는다.
- `e3record.DecodeBoundManifest/CheckManifest`는 내부에서 `evidencebind.Check`와
  오라클 재계산을 수행한다. 조회의 새 판정 계산 금지에 맞게 그대로 호출할지 결정하지 않는다.
  **본 제안은 GET에서 `oracle.Evaluate`·VerifyRead·Prepare를 호출하지 않고** 저장된
  Result와 canonical의 일관성만 확인하는 읽기 검증 분리를 요구한다(OD06).
- [평가 판정 페이지 제안](verdict-record-page-api-proposal.md)은 별도 목적이다.
  정본 바이트/결속 검사 원칙만 공유 가능하며 export 생성·동결·cursor·러너 capability를
  Workbench 단건 조회의 선행 조건으로 붙이지 않는다.
  **이 단건 API는 평가 채점 입력이 아니다. 평가 채점 입력은 판정 페이지 export가 맡는다.**

### 1.2 동시 작업과의 경계

| 확인한 PR | 상태·변경 파일 | 이 초안과의 관계 |
| --- | --- | --- |
| [#596](https://github.com/ABC2LAB/ABCDEF2LAB/pull/596), head `2ed0d262a63ae547ad35c9a014dd1b76faf00ff2` | merged (`b5ce51ec`, 2026-11-16). 결정 문서 한 개이며 구현은 측정 뒤 | §9의 후속 반영 기준. 선행 코드는 구현하지 않음 |
| [#595](https://github.com/ABC2LAB/ABCDEF2LAB/pull/595) | merged. `docs/account-login-decision.md`, `docs/control-restore-backlog.md` | C14 상태·인증 회차 기록의 현재 사실 |
| [#598](https://github.com/ABC2LAB/ABCDEF2LAB/pull/598) | open. `e2runner/`·`e2proxy/execution.go`·server factory/engine·제품 준비 로더·시험·작업공간/상한 문서 | 1-D Bash 병렬 실행 담당 영역. 수정하지 않음 |
| [#594](https://github.com/ABC2LAB/ABCDEF2LAB/pull/594) | open. 평가 원본② 규칙 JSON·설명·시험 | 평가 영역. 수정하지 않음 |

열린 PR 목록과 각 변경 파일을 확인했다. 이 문서의 변경 파일은 이 파일 한 개다.
#571 자격정보 가림·#580 준비 저널·1-C 탐색·Workbench UI·평가 export를 새로 구현하지 않는다.

## 2. 조회 리소스와 식별자

| 식별자 | 현재 정본·뜻 | 혼동 금지 |
| --- | --- | --- |
| task ID | `tasks.id`. issuance의 task/exploration FK와 대응 | URL의 숫자는 조회 권한이 아님 |
| workflow Job ID | `diagnosisprep.Job.ID`. 신뢰 서비스의 Principal·TaskID 결속, 제품 `runs/<job>` 선택에 사용 | task ID·E② run·평가 run과 별개 |
| E② execution/run 참조 | `transmit.Scope.RunRef`, issuance의 `execution_ref`. case/reset/policy scope와 함께 확인 | Job 문자열이나 평가 `run_id`로 대신하지 않음 |
| 일반 finding ID | `findings.id`. triage와 report의 대상 | graph node ID·verdict ID와 다름 |
| E③ verdict ID | `e3_oracle_verdicts.id`. canonical 바이트 자체에는 PK가 없음 | 화면 `id`의 권장 정본 |
| issuance ID | `e3_verdict_issuances.id`. 판정·manifest·task·intent·실행·KG·세대 결속 | 보고서의 `kind=verdict` 항목 ID는 현재 **issuance ID**로 만든 `verdict-<i.id>`다. 그 접미사를 verdict ID로 넘기지 않음 |
| evidence manifest ID | `e3_evidence_manifests.id`. 같은 task/execution/revision의 불변 근거 | traffic ID나 파일 경로가 아님 |
| E③ link | `e3_verdict_links`의 issuance ↔ finding 관계, task/purpose/증거 버전 포함 | nullable 관계이며 판정 본문을 고쳐 채우지 않음 |
| candidate ID | [DiagnosticCandidate](../../db/diagnostic_candidates.go)의 ID. 기존 탐색 fact 저장, origin `e2_diagnostic_candidate_v1` | finding ID와 별도. 현재 후보↔판정 영속 link는 없음 |
| 평가용 run_id | issuance의 nullable `eval_run_id`와 평가 envelope | 비평가 제품에서는 NULL 가능. execution_ref로 채우지 않음 |
| 두 증거 버전 | manifest의 `evidence_revision` 대 finding/link의 `evidence_version` | 숫자가 같아도 같은 세대·역할이라고 추론하지 않음 |

### 2.1 경로 하나의 제안

**신규 제안 경로(미확정):**

~~~text
GET /api/tasks/{task_id}/finding-verdict-detail?verdict_id={id}
GET /api/tasks/{task_id}/finding-verdict-detail?finding_id={id}
GET /api/tasks/{task_id}/finding-verdict-detail?candidate_id={id}
~~~

위 셋은 **같은 경로의 상호 배타적인 selector**다. 정확히 하나만 받는다.
ID는 1..9223372036854775807의 정규 십진 문자열이며, 응답의 ID·revision도 문자열로 전달하는 안이다.
저장 canonical의 기존 정수 타입은 바꾸지 않는다. API 문자열 투영 사본으로 원본 digest를 다시 계산하지 않는다.
JS Number를 거치며 큰 ID를 반올림하지 않는다. 중복 query·다중 selector·알 수 없는 query는 거부한다.

기본 식별은 verdict ID를 권한다. finding이 없는 suspected/undecidable/allowed도 읽어야 한다.
다만 verdict ID만 받으면 “일반 finding에는 연결 판정이 없음”, “후보가 송신 전에 차단됨”을
조회할 대상이 없으므로 같은 경로에서 finding/candidate를 명시 선택하는 안을 함께 제안한다.
제시됐던 `/verdict-records/{verdict_id}`와의 최종 선택은 **OD01·OD02 필수 리뷰**다.

기존 Go·TS·TSX 전체의 `finding-verdict-detail|verdict-records` 검색에서 등록 handler/client는
발견되지 않았다. 평가 제안의 `GET /api/tasks/{task_id}/verdict-records` 목록과도 경로가 다르다.
`firstDiagnosisReportRequestTask` 등의 라우트 허용 목록과 제품/저장 보고서 handler 배선은 후속 구현이다.

### 2.2 선택 규칙과 부수효과 금지

- `verdict_id`: 같은 task의 issuance로 찾아 finding 없는 판정도 반환한다.
  존재하지 않거나 다른 task의 판정이면 같은 404 제안을 적용한다.
- `finding_id`: 같은 task의 finding을 먼저 확인하고 기존 link로만 판정을 찾는다.
  link 없으면 `availability=no_link`. **같은 task의 다른 판정이 존재할 수 있으므로**
  “이 task에 E③ 판정 0건”으로 해석하지 않는다.
- `candidate_id`: task/exploration/intent/run/call을 검증한 후보를 먼저 읽는다.
  #596의 신뢰된 후보 link가 구현되기 전에는 판정을 제목·경로·자원 유사도로 찾아 붙이지 않는다.
  현재는 `availability=no_link`가 가능하며, 명시적인 미실행 정본이 있으면 `not_executed`다.
- 일치하는 link가 여러 개거나 scope가 불일치하면 최신 한 건을 고르지 않고 오류다.
  현재 최초 저장에서는 verdict당 issuance 하나, issuance/finding당 link 최대 하나다.
- GET은 SELECT와 기존 보호 메타데이터 읽기만 수행한다. 대상 HTTP·LLM 호출·판정 생성·재검증 실행,
  issuance/link/finding 생성, export 동결, 실행 복구, marker 발급을 하지 않는다.
  계정 상태/정책이 현재 만료됐다는 이유로 과거 관찰시각의 판정을 재만료시키지 않는다.

## 3. Workbench 필드 매핑

분류: **D** 현재 정본에서 직접 읽음 / **P** 검증된 읽기 투영으로 산출 /
**B** 별도 신뢰 저장 원천과 결속 필요 / **N** 공급·저장 계약이 미구현.
각 필드는 전체 scope·무결성 검증과 §7의 공개 allowlist를 통과한 뒤에만 내보낸다.

| 화면 필드 | 원천 | 현재 저장 여부 | 변환 규칙 | 미확정 사항 |
| --- | --- | --- | --- | --- |
| id | `e3_oracle_verdicts.id` | D, payload 밖 PK | `ids.verdict_id`를 손실 없는 문자열로. finding/candidate ID를 대신 넣지 않음 | 판정 없을 때 FindingRecord를 만들지 않는 UI union(OD01) |
| severity | `binding_canonical`의 `e3record.Source.Severity`; link 있으면 `findings.severity`도 존재 | D+B. Source는 Prepare에서 critical/high/medium/low 검증 | 초기 판정의 심각도는 검증된 Source에서 읽고 현재 triage severity는 별도 필드. oracle 값에서 추론 금지 | 후보에는 severity 필드 없음. null/미평가 표현(현재 UI 타입은 4값 필수) |
| vulnClass | canonical `vuln_class`; 현재 Prepare는 `IDOR` 고정 | D | 저장된 유형 보존. 일반 finding의 mutable 유형과 혼합 금지 | 다른 오라클 유형·수직은 후속 |
| oracleValue | canonical `oracle_value`와 DB `oracle_value`의 일치 | D | 4값 그대로 | 원값과 표시 판정 분리 |
| vulnVerdict | canonical `vuln_verdict` | D | allowed만 null, 그 외는 원값과 동일 | 판정 부재의 null과 allowed의 null을 envelope로 구분 |
| accessPolicyResult | canonical `access_policy_result` | D | confirmed/suspected → 위반_후보, undecidable → 판정_불가, allowed → 위반_미관찰 | 원래 값 보존, “안전” 배지로 대체 금지 |
| checks | canonical `policy_check/identity_check/data_check` | D | 각 yes/no/unknown 그대로 | 필수 칸 누락은 저장 무결성 오류. 가짜 unknown으로 복구 금지 |
| signals | canonical 아래 6개 키 | D | same_resource → sameResource 등 이름만 매핑 | 모르는 enum 거부, 미실행 추론 금지 |
| canaryFields | canonical `canary_fields`와 Source의 필드 집합 | D+B | 값 아닌 검증된 필드명만. 빈 배열은 그 레코드에 필드명이 없다는 뜻 | Source의 현재 검사는 길이·중복·UTF-8 등이며 공개 적격성 전체 보장이 아님. #596 JSON 경로 표기/가림 검토 |
| subjects | canonical `owner_ref/evaluated_ref/owner_status/evaluated_status` | D+B | 당시 host 발급 비민감 라벨·상태만. 현재 계정 사용자명/세션으로 풀지 않음 | 제품별 계정 binding은 #596 영수증 v3 의존 |
| control | canonical `policy_version/reset_generation/observed_at/model_tag/hint_matches_canonical/expected_access` | D | 시각은 UTC 6자리 Z 보존, UI observedAt(ms) 사본 변환만 허용. 원본 digest를 ms로 재계산하지 않음 | E② PolicyRevision과 KG policy_version을 분리 표시 |
| trafficEvidenceRefs | canonical `traffic_evidence_refs`, Source.TrafficEvidenceIDs | D+B | 정확한 정수 ID를 문자열로. 각 traffic의 동일 task/실행 조회 권한 추가 확인 | 빈 배열은 traffic ID 미연결. owner/cross 관찰의 `read:<digest>`·E② AttemptID를 여기 채우지 않음 |
| transmission | 현재 canonical에는 없음. manifest의 registered GET receipt 또는 제품 e2proxy 저널 | P+B, 제품 판정↔시도 link는 N | §5의 별도 투영. 증거가 부족하면 unknown | 독립 checkpoint/마감·후보별 시도 집합·#596 v3 결속 |

추가 화면 필드도 가짜로 채우지 않는다.

- `axis`는 현재 canonical의 horizontal만, `resourceRef`는 전체 논리 참조 그대로다.
  [기존 규약](idor-verifier-seam.md)에 따라 마지막 콜론으로 type/id를 구분한다.
- `title`은 공개 가능한 프로그램 소유 finding 이름 또는 유형 기반 고정 제목을 사용한다.
  없는 제목을 LLM으로 생성하지 않는다.
- `comparison`은 검증된 owner/cross 관찰·역할·신호에서 만든 **설명용 P**다.
  목업의 “비로그인 대조군” 3행을 복제하지 않는다. 실제 저장된 관찰만 표시한다.
- `intent/stateChanging`은 검증된 계획/해당 시도 메서드의 B다. 일반 후보 자유문장만으로 승인/쓰기 여부를 정하지 않는다.
- 실 레코드 응답의 `mock=false`는 API 투영 상수다. 조회 실패 시 목업으로 fallback하지 않는다.

## 4. E③ 판정·증거 충분성

정본 판정의 의미는 [오라클](e3-oracle.md)·[판정 레코드](verdict-record.md)를 따른다.
세 체크는 정책·신원·데이터, 6신호는 아래 그대로다.

~~~text
same_resource, owner_normal_data, cross_normal_data,
nontrivial_data_match, reflection_excluded, template_excluded
각각 yes / no / unknown
~~~

- confirmed의 증거 조건은 세 체크와 여섯 신호 전부 yes이며 등록 정책·신원·관찰·세대 결속이 서야 한다.
- suspected는 정책 내용 불명과 검증된 신원·데이터의 기존 규칙을 보존한다.
- undecidable은 저장된 판단불가다. 읽기 DB 장애나 조회 권한 실패를 새 undecidable 판정으로 만들지 않는다.
- allowed는 정책상 허용 판단이다. `vuln_verdict=null`이고 접근 성공·미발견·차단 성공·fixed가 아니다.
  B 단독 검증 축으로 결정되므로 identity/data unknown이 있을 수 있다. 이를 confirmed 증거 누락과 똑같이 취급하지 않는다.

신규 envelope는 `record`(저장 원값), `record_checks`(읽기 검증 상태),
`view`(표시 사본)를 분리한다. 단순 표시에 원래 canonical·digest·판정을 고치거나 재발급하지 않는다.

| 상태 | 원값 | 표시 제안 |
| --- | --- | --- |
| 저장·출처 검증 성공 | 4값 보존 | 해당 판정값. `confirmation_evidence_complete`는 confirmed 필요조건을 뜻하며 취약점 없음 지표가 아님 |
| 현재 finding의 증거 버전이 초기 link보다 바뀜 | 초기 원값 보존 | `binding_status=historical`, 현재 finding 표시 판정은 undecidable, 원래 초기 판정 배지·관찰시각은 별도 표시 |
| 정본은 온전하지만 보존 KG/checkpoint 확인이 불가능함 | 원값 보존 | `record_checks.status=unavailable`, `view.display_verdict=undecidable`, 부족 사유 별도 |
| canonical·manifest·발급 결속 모순/손상 | 정상 판정 응답 안 함 | 무결성 오류. confirmed 원값을 정상 판단불가로 덮어 숨기지 않음 |
| 판정 없음/미실행 | `record=null` | `display_verdict=null`과 명시적 availability. undecidable 판정을 가공 저장하지 않음 |

현재 화면의 `displayVerdict`·`confirmedEvidenceComplete` 등은
[목업 시험](../../web/src/lib/diagnosis/finding.test.ts)의 보수 표시 원칙만 제공한다.
`allEvidenceUnknown`은 enum의 조합이지 “완전 미실행”의 증명이 아니다.
`comparison=[]`도 “전송·응답 없음”을 증명하지 않는다.
live 연결에서는 이 두 서사를 availability/전송 근거에 결속해야 한다.
현재 FindingRecord의 필수 severity/oracleValue/observedAt만으로 부재·읽기 실패를 표현할 수 없으므로
**getter만 교체하면 완료된다는 전제를 두지 않고** nullable envelope와 UI 연결을 이경준과 확인한다.

## 5. E② 송신 사실의 읽기 투영

### 5.1 신뢰 원천과 현재 한계

| 경로 | 현재 근거 | 읽기 의무·공백 |
| --- | --- | --- |
| 등록 GET | [manifest](../../e3record/manifest.go)의 Owner/Cross Context·[transmit.Receipt](../../transmit/config.go): Attempt, AccountRef, SessionRef, ResourceRef, ApprovalRef, Admitted, RequestWritten, ResponseReceived, Reason, StatusCode | 저장 manifest digest 및 Source/issuance, 보호된 최종 `transmit/auditlog` checkpoint·동일 관찰 결속. 세션 handle/키·원문은 응답에서 제외 |
| 제품 일반 HTTP | [e2proxy.Receipt/Event](../../e2proxy/types.go)의 Scope, IntentID, CallRef, AttemptID, PolicyRevision, Sequence, Decision, Reason, 송신/응답 플래그 | [저널 검증](../../e2proxy/audit.go)과 task/job/run/intent/call/시도 결속 필요. 현재 receipt에는 **요청별 계정·자원·판정 ID가 없음** |
| 기존 흐름 API | [url_live_views.go](../../cmd/first-controlled-diagnosis/url_live_views.go): 정책/감사 키 pin 확인, VerifyJournalEvents 후 표시 | `journal=verified`만으로 최종 마감·rollback 방지·판정↔시도 연결을 보장하지 않음. 표시용 path_pattern은 감사 정본 아님 |

[워크벤치 실데이터 계약](workbench-live-data.md)의 e2-flow는 최근 호출 생략과 페이지 단위 사건을 가진다.
UI가 받은 마지막 페이지나 `counts.allowed`로 판정 한 건의 송신을 역산하지 않는다.

특히 다음을 분리한다.

1. `forward_attempted`: 연결 시도부터 포함한다. 성공한 송신이 아니다.
2. `request_written`: 클라이언트가 요청 쓰기를 관측했다. 대상 앱이 처리·커밋했다는 뜻은 아니다.
3. `response_received`: 응답 상태를 관측했다. 본문 완료·정상 데이터·권한 위반은 별도다.
4. `response_complete`, `response_truncated`: 전달 표현의 완결 여부. 반사·표식 비교 신호와 동일하지 않다.
5. 감사의 HMAC 검증, 저장 ACK, 최종 마감은 별개다.
   `Call.finish`는 result 사건을 기록한 **뒤** 반환 receipt의 AuditComplete를 설정한다.
   따라서 저장 사건의 `audit_complete=false`만 보고 감사 실패로 단정할 수 없다.
   VerifyJournalEvents의 supplied-key 체인 검증은 신뢰된 최신 checkpoint가 없으면 유효한 과거 prefix를 탐지하지 못한다.

### 5.2 네 상태의 판정 규칙 제안

아래는 **완결되고 권한·출처를 검증한 선택 범위**에만 적용한다.
등록 GET 판정의 대표 대상은 cross_read이며 owner_read는 별도 단계로 제시한다.
제품 후보는 해당 call의 실제 시도 집합이다. 일부 시도의 성공을 다른 시도에 복사하지 않는다.

| 입력 사실 | transmission.state | 화면 의미·제약 |
| --- | --- | --- |
| 선택 시도에 검증된 최종 result와 `request_written=true` 또는 `response_received=true` | sent | 요청 쓰기/응답 관측의 근거 있음. 응답·검사 성공은 별도 플래그와 E③ 조건으로만 표시 |
| 선택 시도가 E②의 알려진 거부/미지원 사유로 끝났고, 송신 전 차단을 증명하는 result와 감사 마감이 있으며 forward_attempted/request_written/response_received 모두 false | held_pre_send | 송신 전 차단. 승인 대기·사용자 거절·미지원 사유를 자동으로 같은 문구로 만들지 않음 |
| 해당 검사·시도 집합이 시작되지 않았다는 **신뢰된 실행 계획/상태 기록**이 확인됨 | none | 실제 미실행. 빈 저널·빈 traffic 배열·판정 행 부재로는 지정 불가 |
| intent만 존재, result/연결 불충분, 감사·마감 불명, 시도 혼합/부분 근거, 미지원 버전 | unknown | 모르는 범위와 사유 표시. 무송신/성공 주장 금지 |

제품 call 집합에서 여러 시도의 상태가 섞이면 대표값은 unknown으로 두고
검증된 시도별 사실만 `steps`에 보존하는 보수안을 제안한다.
이 집계 규칙과 범위 선택은 OD03이며, 원래 4값을 조용히 늘리지 않는다.

[#598](https://github.com/ABC2LAB/ABCDEF2LAB/pull/598)의 Bash 자리 대기 만료도 `held_pre_send`의 예다.
명령 시간에는 대기가 포함되며, 프로세스·Call·프록시를 시작하지 않은 명령은 ACK된 제어 영수증의
`not_sent`와 `admission_not_sent`로 구분한다. 이 경우 call_ref·attempt_id가 없을 수 있으므로
가짜 시도 ID를 만들지 않고 실제 CommandRef와 task/run/intent 결속·독립 감사 마감을 확인한다.
이 투영은 #598 병합과 해당 영수증 reader 연결 뒤에만 쓰며, 빈 Call이나 ErrUnavailable만으로
`held_pre_send`를 추정하지 않는다. 대기 만료는 승인 거절·미지원 사유와 별도 사유로 표시한다.

`decision=allowed`만 있는 intent는 sent가 아니다.
`reason=withheld`는 대상 응답 표현을 보류·자른 결과이므로 **held_pre_send가 아니다**.
`response_truncated=true`도 미송신 근거가 아니다.
`transport_failed`은 연결 실패라 request_written/response_received가 false일 수 있지만
전송을 시도했으므로 none·정책 차단으로 바꾸지 않는다. 4값 안에서는 unknown + 송신 실패 사유/플래그로
표시하는 안을 리뷰한다. 연결 뒤 끊김은 원래 unknown을 보존한다.

신규 transmission 투영에는 `scope_role`, `coverage`, `source`, `audit_status`,
`reason_codes`, `steps`를 제안한다. 각 step은 비민감 시도 참조와 위 네 bool을 **bool|null**로 담는다.
미수집 bool의 false 기본값 보충은 금지한다.
`source`는 registered_get/e2proxy/unbound, `scope_role`은 cross_read/candidate_call(미결속이면 null),
`coverage`는 complete/partial/unavailable, `audit_status`는 verified/unavailable/invalid를 제안한다.
coverage는 선택한 검사 근거의 읽기 범위이며 전체 task·egress 경로를 전수 확인했다는 뜻이 아니다.
verified는 키·scope·저널·독립 마감 근거가 함께 확인된 경우만이다.
감사 실패는 E③ 원값을 바꾸는 이유가 아니라 전송/읽기 검증 불명 사유다.

### 5.3 후속 공급 의존성

- 등록 GET: 역사적 manifest와 독립 Source/checkpoint reader의 보존·권한·재조회 범위를 최민준과 정한다.
- 제품: 후보 run/call에서 **검사에 해당하는 정확한 시도 집합**을 발행·보존할 host 계약이 필요하다.
  URL·path_pattern·모델의 계정 라벨로 판정/시도를 조인하지 않는다.
- “none”과 “판정 not_executed”를 증명할 검사별 계획/미실행 상태의 신뢰 저장 원천도 별도 결정이다.
  현재 후보 classification이나 모든 신호 unknown만으로 대신하지 않는다.
- #596의 프로그램 목적·실제 계정 binding·선언 동결 digest·구조화 관찰/완결 상태·영수증 v3는
  **후속 구현 의존성**이다. 기존 v1/v2의 없는 칸을 역으로 채우지 않는다.
- 최종 checkpoint를 독립 확인할 수 없다면 감사 키 pin/체인 검증 성공만으로 `audit_status=verified`를 주지 않는다.
  이 문서는 새 E② 영수증 필드를 확정하거나 recorder 코드를 변경하지 않는다.

## 6. 없는 판정·불일치·읽기 실패

아래 HTTP/코드/availability는 모두 **신규 제안**이다.
도메인 근거 부족, 저장 무결성, 읽기 인프라 장애를 구분한다.

| 경우 | 응답 제안 | 화면 |
| --- | --- | --- |
| 확정 단계 전에 회차 실패·중지, 선택 범위의 단계 계획/미시작/종료가 검증됨 | 200, availability=not_executed, evaluation_status=not_executed, record/record_checks=null | “확정 단계가 실행되지 않음”. 판정 행 생성 0, 기존 후보의 전송 상태는 별도 보존 |
| 일반 finding만 존재, E③ link 없음 | 200, `availability=no_link`, record=null, finding 유지 | “연결된 E③ 판정 없음”, 일반 finding의 triage 표시 유지 |
| E③ 판정은 있지만 finding link 없음 | 200, availability=recorded, ids.finding_id=null | verdict ID로 표시. suspected/undecidable/allowed에서 정상 상태 |
| 현재 최초 저장 경로의 confirmed에 finding link 없음 | 422 `verdict_detail_integrity_failed` | 현재 원자 저장 불변식 위반. #596의 제품 미연결 판정은 별도 source/link 규약 채택 뒤에만 허용 |
| link·issuance·finding의 task/exploration/실행 불일치 | 422 `verdict_detail_integrity_failed` | 연결 오류. 다른 task 자료는 반환 안 함 |
| manifest 누락 또는 payload/manifest digest 오류 | 422 `verdict_detail_integrity_failed` | 원값 포함 정상 record를 반환하지 않음 |
| 과거 KG snapshot이 없음을 신뢰 reader가 확인 | 200, record 원값 유지, record_checks=unavailable, 표시 undecidable | “저장 판정은 있으나 근거 재확인 불가” |
| E② 감사 근거 불명·보존/마감 근거 부재 | record가 온전하면 200 + transmission=unknown, 감사 확인 불가 시 record_checks도 unavailable | 접근 성공·무송신으로 변환 금지 |
| finding의 현재 evidence_version ≠ 최초 link.evidence_version | 200 + `binding_status=historical`, view.display_verdict=undecidable | 초기 원값은 보존. 현재 finding 근거를 확정했다고 표시하지 않음 |
| 감사 HMAC·독립 expected checkpoint·같은 KG ID의 내용 불일치가 확인됨 | 422 `verdict_detail_integrity_failed` | 기록 부재·읽기 실패와 구분. 정상 unavailable로 손상을 숨기지 않음 |
| issuance/manifest/revision binding 자체가 서로 다름 | 422 `verdict_detail_integrity_failed` | 정상 revision 갱신과 다르게 무결성 거부 |
| 판정 자체가 미실행이라는 신뢰 상태 기록 있음 | 200, availability=not_executed, record=null | 미실행. UNKNOWN 신호의 가짜 판정을 만들지 않음 |
| 판정 ID 없음·다른 task의 판정 ID | 같은 404 `verdict_detail_not_found` | 비존재/타 task의 존재 여부 구분 노출 금지 |
| ID는 있으나 출처/권위를 아직 설치하지 않아 확인 불가 | 200 + unavailable 또는 필수 scope 불명은 409 `verdict_detail_binding_unavailable` | 읽기 검증 미지원. 정상 verified로 fallback 금지 |
| DB/보호 reader 일시 장애 | 503 `verdict_detail_unavailable` | 증거 부재/미실행으로 바꾸지 않음 |

현재 confirmed 저장은 finding/link와 한 transaction이라는 사실을 보존한다.
“판정은 있지만 finding link가 없음”의 정상 사례는 지금은 **non-confirmed**를 사용한다.
#596에서 후보가 안 맞아 미연결인 제품 판정을 읽는 형식은 별도 채택 뒤에 추가한다.

행/연결이 없다는 사실은 미실행 증명이 아니다. 명시된 selector가 존재하고 조회가 성공해도
판정 미시행의 출처가 없으면 `availability=no_link` + `evaluation_status=unknown`으로 둔다.
진행 중 writer와 경합한 미연결 상태는 다음 GET에서 달라질 수 있는 조회 시점 상태다.
부분 조인으로 고아 행을 누락시키지 않도록 LEFT JOIN과 무결성 검사를 적용한다.

### 6.1 후속 reader의 검증 순서 제안

1. 인증된 task/job 범위를 먼저 확정한다. selector 리소스의 소속을 확인한 뒤 한 읽기 snapshot에서
   issuance → verdict/manifest, nullable finding link를 읽는다. 행을 INNER JOIN으로 없애 부재처럼 숨기지 않는다.
2. record_version=1, 현재 purpose=initial, 양의 ID/revision과 길이 상한을 검사한다.
   payload_canonical·manifest canonical의 원래 bytes를 SHA-256과 대조한다.
   JSONB payload를 재직렬화하여 정본을 복구하거나 해시를 맞추지 않는다.
3. canonical은 알 수 없는/중복/필수 누락 필드와 틀린 enum을 거부하는 typed reader로 해석한다.
   verdict 열·payload 원값·취약점 판정/정책 결과·체크·신호의 저장 일관성을 확인한다.
4. binding_canonical의 Source와 issuance 열, manifest의 Context/저장 Result를 서로 대조한다.
   task/exploration/intent/execution/case/resource/reset/model/KG/관찰시각/evidence revision을 확인한다.
   이때 binding이 스스로 자기 출처를 인증한다고 보지 않고, 설치된 독립 권위·역사적 KG/checkpoint와
   일치해야 record_checks=verified다. missing reader는 unavailable이다.
5. link의 task·purpose·finding과 증거 버전을 검사한다. mutable triage 상태/현재 심각도 변경은
   과거 oracle 값을 바꾸지 않으며 현재 증거 버전 차이는 historical로 표시한다.
6. 등록 GET과 e2proxy에 맞는 별도 근거로 transmission을 만든다.
   신뢰 불충분이면 원값/읽기 검증/전송 상태를 분리해 표시하거나 §6의 오류로 거부한다.
7. 공개 allowlist만 투영하고 상한·DLP를 검사한 후 응답한다. Prepare/RecordControlledFinding/
   oracle.Evaluate/VerifyRead 호출, 새로운 Source·권한·연결 발급은 하지 않는다.

## 7. 인증·task 결속·비밀정보

제품의 현재 단일 관리자 ARTEX 인증과 신뢰 준비 서비스의 Principal/Job/Task 결속을 따른다.
[제품 라우터](../../cmd/first-controlled-diagnosis/url_preparation_runtime.go)의
`AuthorizeTask`와 [준비 서비스](../../diagnosisprep/service_linux.go)의 Principal/Task 소유 검사를
조회 전에 수행하는 구조를 재사용한다. 새 다중 사용자 RBAC가 구현됐다고 표현하지 않는다.

- [serveDiagnosisRun](../../server/first_diagnosis_run.go)과 같은 Bearer 검증을 제안한다:
  HS256, 만료, 현재 비밀번호 결속, subject 확인. cookie/query token만으로 대체하지 않는다.
  principal은 서버 인증 결과이며 요청 본문/query의 owner 선언이 아니다.
- 인증 → 허가된 task/job 결속 → 선택 레코드 소속 → 실행/증거 검증 순서로 읽는다.
  다른 owner, 다른 task, 다른 실행의 자료를 합치지 않는다.
  단일 task의 실행을 유일하게 선택하지 못하면 최신 job/run을 임의 선택하지 않는다.
- 저장 보고서 모드는 미리 결속된 task만 읽는다([기존 handler](../../server/first_diagnosis_report.go)).
  task 경로가 있다는 이유로 이 모드의 허용 목록이 자동 확장되지 않는다.
  실행 승인 만료와 과거 저장 자료의 읽기 권한을 구분한다.
- DB는 필요한 SELECT만 사용하고 Repeatable Read의 읽기 전용 snapshot에서 task/issuance/link/manifest를 읽는다.
  보호 파일 읽기는 별도 범위·버전 확인을 하고 DB snapshot과 같은 시점이라고 가장하지 않는다.
- 형식 version/크기/개수 상한은 기존 canonical 각 64 KiB를 지키고, 새 상세 전체/배열 상한은 OD08에서 확정한다.
  상한 초과를 일부 필드 삭제나 성공한 잘림으로 감추지 않는다. 응답은 `Cache-Control: no-store`다.

**응답 금지:** 비밀번호, 쿠키/세션/인증 토큰 원문, API 키, E② 감사·권한 키, raw HTTP 요청/응답 본문,
canary 값, 보호된 사용자명/이메일 등 계정 원문, raw URL/query, GT 정답·챌린지 키,
저장 경로·보호된 파일 참조, 세션 handle·권한 capability.
원문 canonical·binding·manifest를 통째로 응답하지 않고 공개 allowlist만 투영한다.
ref/hash라도 내부 경로나 제어 비밀의 fingerprint이면 보내지 않는다.
표시 가능한 공개 라벨·논리 자원 ref·ID·필드명도 정본 계약과 DLP를 통과해야 한다.

오류·접근 로그에는 정본 payload/SQL/파일 경로/입력 query/인증값을 반사하지 않는다.
예시들은 합성 ID·일반 필드명만 사용한다.

## 8. 응답·오류 형식 제안

### 8.1 envelope와 현재 코드와의 차이

다음 와이어 필드는 모두 신규 제안이다. `record`의 값은 §3의 기존 논리 레코드 투영이다.

| 필드 | 제안 의미 |
| --- | --- |
| format | `jindo.workbench-finding-verdict-detail/v1`. 기존 API의 구현 버전이 아님 |
| task_id, selection | 권한 확인된 task와 selector 종류/ID |
| ids | verdict_id/issuance_id/manifest_id/finding_id/candidate_id. 없으면 null |
| availability | recorded / no_link / not_executed. 조회 장애를 부재로 넣지 않음 |
| evaluation_status | recorded / not_executed / unknown. no_link만으로 미실행 결정 금지 |
| record | 오라클 원값·체크·6신호·필드명·정책/주체/시각 등. 판정 없으면 null |
| record_checks | status=verified/unavailable, reason_codes, binding_status=current/historical. 판정 없으면 null. 제품 확정 경로 추가 시 §9.1의 세부 사유를 공급 |
| policy_source, candidate_link | 제품 확정 경로 추가 시 §9.1의 명시적 출처·검증된 연결을 공급. 현재는 미공급/null |
| presentation | 안전한 제목과 초기 심각도(nullable), mock=false. triage severity는 필요시 별도로 |
| view | display_verdict(nullable), confirmation_evidence_complete(bool|null), missing_requirements. 원값 변경 아님 |
| transmission | §5의 state/source/scope_role/coverage/audit_status/steps |
| finding, candidate | 권한·DLP를 통과한 최소 식별/triage 또는 manual_review_required 상태만. 원문 evidence/report는 싣지 않음 |

현재 `writeErr`는 `{"error":"한국어 문구"}`, 제품 live view는 일부 `http.Error` 텍스트 응답을 사용한다.
기존 일반 finding은 잘못된 ID 400·없음 404·읽기 오류 500, 보고서는 404/422/503,
저장 report task 위반은 403을 사용한다. 아래 **고정 code와 HTTP 조합은 이 초안의 신규 제안**이며
기존 오류 구현이라고 보고하지 않는다.

| 조건 | 제안 HTTP | 신규 code |
| --- | --- | --- |
| 인증 실패 | 401 | 기존 인증 오류 정책 재사용(새 코드 확정 안 함) |
| task/owner 권한 실패 | 403 | verdict_detail_forbidden |
| selector 형식·다중/추가 query | 400 | invalid_verdict_detail_request |
| 선택 리소스 부재 또는 타 task verdict/finding/candidate | 404 | verdict_detail_not_found |
| 필수 실행 binding을 선택/검증 못 함 | 409 | verdict_detail_binding_unavailable |
| 저장 정본·발급·연결 무결성 오류 | 422 | verdict_detail_integrity_failed |
| DB·권위 reader 읽기 장애 | 503 | verdict_detail_unavailable |

HTTP/코드와 200 내 unavailable의 경계는 **OD06 필수 결정**이다.
422에는 정상 record를 함께 주지 않는다. 인증/소속 실패에는 다른 리소스의 ID도 반사하지 않는다.

### 8.2 합성 JSON 예시 — 제안 형식

다음은 **합성 값이며 실측 E③ 결과가 아니다**.
첫 예시 이후는 해당 사례의 차이를 보이는 축약 예시다. 생략된 공통 필드를 실제 wire에서
생략/기본값 보충해도 된다는 뜻은 아니다.

**① confirmed·충분한 증거, verdict selector, HTTP 200 (제안)**

~~~json
{
  "format": "jindo.workbench-finding-verdict-detail/v1",
  "task_id": "17",
  "selection": {"kind": "verdict", "id": "101"},
  "ids": {"verdict_id": "101", "issuance_id": "201", "manifest_id": "301", "finding_id": "401", "candidate_id": null},
  "policy_source": null,
  "candidate_link": null,
  "availability": "recorded",
  "evaluation_status": "recorded",
  "record": {
    "case_ref": "case-example-1",
    "axis": "horizontal",
    "vuln_class": "IDOR",
    "resource_ref": "document:example-1",
    "oracle_value": "confirmed",
    "vuln_verdict": "confirmed",
    "access_policy_result": "위반_후보",
    "policy_check": "yes",
    "identity_check": "yes",
    "data_check": "yes",
    "reason_codes": ["policy_deny_resolved", "subjects_distinct_verified", "data_nontrivial_match"],
    "same_resource": "yes",
    "owner_normal_data": "yes",
    "cross_normal_data": "yes",
    "nontrivial_data_match": "yes",
    "reflection_excluded": "yes",
    "template_excluded": "yes",
    "canary_fields": ["note"],
    "owner_ref": "owner",
    "evaluated_ref": "reader",
    "owner_status": "verified",
    "evaluated_status": "verified",
    "policy_version": "policy-example-1",
    "expected_access": "deny",
    "sharing": "resolved_none",
    "delegation": "resolved_none",
    "hint_matches_canonical": true,
    "policy_valid_from": "2026-11-16T00:00:00.000000Z",
    "policy_valid_until": "2026-11-17T00:00:00.000000Z",
    "reset_generation": "reset-example-1",
    "observed_at": "2026-11-16T00:01:00.000000Z",
    "created_at": "2026-11-16T00:01:01.000000Z",
    "model_tag": "synthetic-model",
    "traffic_evidence_refs": []
  },
  "record_checks": {"status": "verified", "binding_status": "current", "reason_codes": []},
  "presentation": {"title": "수평 IDOR 구조화 판정", "severity": "high", "mock": false},
  "view": {"display_verdict": "confirmed", "confirmation_evidence_complete": true, "missing_requirements": []},
  "transmission": {
    "state": "sent",
    "source": "registered_get",
    "scope_role": "cross_read",
    "coverage": "complete",
    "audit_status": "verified",
    "reason_codes": [],
    "steps": [{"role": "cross_read", "attempt_ref": "2", "forward_attempted": null, "request_written": true, "response_received": true, "response_complete": null}]
  },
  "finding": {"id": "401", "triage_status": "pending"},
  "candidate": null
}
~~~

등록 GET receipt에는 forward_attempted/response_complete가 없으므로 null이다.
traffic ID는 manifest의 owner/cross 관찰 참조와 별개이므로 빈 배열이어도 모순이 아니다.

**② undecidable, verdict selector, HTTP 200 (제안·축약)**

~~~json
{
  "policy_source": null,
  "candidate_link": null,
  "availability": "recorded",
  "evaluation_status": "recorded",
  "ids": {"verdict_id": "102", "issuance_id": "202", "manifest_id": "302", "finding_id": null, "candidate_id": null},
  "record": {
    "oracle_value": "undecidable", "vuln_verdict": "undecidable", "access_policy_result": "판정_불가",
    "policy_check": "yes", "identity_check": "yes", "data_check": "unknown",
    "reason_codes": ["policy_deny_resolved", "subjects_distinct_verified", "data_unverifiable"],
    "same_resource": "yes", "owner_normal_data": "yes", "cross_normal_data": "yes",
    "nontrivial_data_match": "unknown", "reflection_excluded": "yes", "template_excluded": "yes"
  },
  "record_checks": {"status": "verified", "binding_status": "current", "reason_codes": []},
  "view": {"display_verdict": "undecidable", "confirmation_evidence_complete": false, "missing_requirements": ["data_check", "nontrivial_data_match"]},
  "transmission": {"state": "sent", "source": "registered_get", "scope_role": "cross_read", "coverage": "complete", "audit_status": "verified"}
}
~~~

송신·응답이 있어도 비교 데이터가 불명이면 undecidable일 수 있다.

**③ 정책상 allowed, HTTP 200 (제안·축약)**

~~~json
{
  "policy_source": null,
  "candidate_link": null,
  "availability": "recorded", "evaluation_status": "recorded",
  "record": {
    "oracle_value": "allowed", "vuln_verdict": null, "access_policy_result": "위반_미관찰",
    "policy_check": "no", "identity_check": "yes", "data_check": "yes",
    "reason_codes": ["policy_allows"]
  },
  "record_checks": {"status": "verified", "binding_status": "current", "reason_codes": []},
  "view": {"display_verdict": "allowed", "confirmation_evidence_complete": false, "missing_requirements": []}
}
~~~

표시 문구는 “정책상 허용”이다. “취약점 없음”·“E②가 차단함”으로 바꾸지 않는다.

**④ 송신 전 차단이 입증된 미검증 후보, candidate selector, HTTP 200 (제안·축약)**

아래는 **신뢰된 검사 미실행 기록과 정확한 시도 집합·마감 결속이 공급된 이후**의 예시다.
현재 candidate classification 하나로 이 결과를 만들 수 있다고 가정하지 않는다.

~~~json
{
  "selection": {"kind": "candidate", "id": "501"},
  "policy_source": null,
  "candidate_link": null,
  "availability": "not_executed", "evaluation_status": "not_executed",
  "record": null, "record_checks": null,
  "candidate": {"id": "501", "verification": "manual_review_required"},
  "view": {"display_verdict": null, "confirmation_evidence_complete": null, "missing_requirements": []},
  "transmission": {
    "state": "held_pre_send", "source": "e2proxy", "scope_role": "candidate_call",
    "coverage": "complete", "audit_status": "verified", "reason_codes": ["policy_denied"],
    "steps": [{"attempt_ref": "11111111111111111111111111111111", "forward_attempted": false, "request_written": false, "response_received": false, "response_complete": false}]
  }
}
~~~

원래 오라클에 없는 차단 사유를 JudgeReason으로 주입하지 않는다. E③ 판정 미실행과
E② 요청 시도/송신 전 거부는 서로 다른 단계의 사실이다.

**⑤ E③ 연결 판정 없음, 일반 finding selector, HTTP 200 (제안·축약)**

~~~json
{
  "selection": {"kind": "finding", "id": "402"},
  "policy_source": null,
  "candidate_link": null,
  "availability": "no_link", "evaluation_status": "unknown",
  "ids": {"verdict_id": null, "issuance_id": null, "manifest_id": null, "finding_id": "402", "candidate_id": null},
  "record": null, "record_checks": null,
  "finding": {"id": "402", "triage_status": "pending"},
  "view": {"display_verdict": null, "confirmation_evidence_complete": null, "missing_requirements": []},
  "transmission": {"state": "unknown", "source": "unbound", "coverage": "unavailable", "audit_status": "unavailable"}
}
~~~

E③ 판정 부재를 `records=[]`나 `confirmed=false`로 숨기지 않는다.
verdict selector의 ID 자체가 없으면 위 200 부재 응답과 구분하여 404를 제안한다.

**⑥ 판정 있음·finding link 없음, verdict selector, HTTP 200 (제안·축약)**

~~~json
{
  "selection": {"kind": "verdict", "id": "103"},
  "ids": {"verdict_id": "103", "issuance_id": "203", "manifest_id": "303", "finding_id": null, "candidate_id": null},
  "policy_source": null,
  "candidate_link": null,
  "availability": "recorded", "evaluation_status": "recorded",
  "record": {"oracle_value": "suspected", "vuln_verdict": "suspected", "access_policy_result": "위반_후보"},
  "record_checks": {"status": "verified", "binding_status": "current", "reason_codes": []},
  "view": {"display_verdict": "suspected", "confirmation_evidence_complete": false, "missing_requirements": ["policy_check"]}
}
~~~

현재 저장 가능한 non-confirmed 사례다. #596의 제품 confirmed 미연결 규칙은 별도 채택 대상이다.

**⑦ 증거 무결성 오류, HTTP 422 (제안)**

~~~json
{
  "error": "저장 판정의 근거 무결성을 확인할 수 없습니다.",
  "code": "verdict_detail_integrity_failed"
}
~~~

손상 위치·DB 원문·canonical·비밀값을 오류에 포함하지 않는다.
KG 유실을 확인한 경우의 정상 읽기 예시(제안·축약)는
`{"record":{"oracle_value":"confirmed"},"record_checks":{"status":"unavailable","reason_codes":["kg_snapshot_unavailable"]},"view":{"display_verdict":"undecidable","confirmation_evidence_complete":null}}`다.
읽기 DB 장애는 이 사본으로 가장하지 않고 503을 반환한다.

## 9. #596 결정안과의 관계

#596은 후속 확인 시 **dev에 병합된 결정 문서**다(merge `b5ce51ec`, 2026-11-16 06:16 UTC).
최초 초안 작성 때는 미병합이었지만 이후 상태를 갱신했다. 구현은 여전히 측정 뒤다.
확인한 원문은 [고정 head의 결정안](https://github.com/ABC2LAB/ABCDEF2LAB/blob/2ed0d262a63ae547ad35c9a014dd1b76faf00ff2/docs/e3-confirm-path-decision.md)이다.
`a45c26cd`의 모델 단계 뒤 확정·예비 몫 결정과, 최종 `2ed0d262`의 다음 회차 리셋 확인 보완을 함께 반영한다.
이 초안이 #596의 판정 규칙이나 구현 소유를 대신 확정하지 않는다.

| 구분 | 현재 경로 | #596 이후 의존 |
| --- | --- | --- |
| 등록 GET/IDOR | 수동 레지스트리·A/B·transmit·보호 checkpoint·E③ 최초 저장 | 평가 경로 유지. 제품 gateway는 현재 이 모드를 거부 |
| 제품 일반 진단 | 후보는 manual_review_required. e2proxy 감사만으로 E③이 되지 않음 | 운영자 소유자 전용 필드 선언, 프로그램 표식 생성→A 대조→B 교차, 구조화 관찰에서 E③ |
| 정책 출처 | KG snapshot/version/digest | 대상 등록 보호 저널의 선언 동결 digest를 정책 출처로 추가 |
| 요청 계정·관찰 | 제품 receipt에는 요청별 계정·표식 검사 결과 없음 | 프로그램 목적 규칙·C14 실제 계정 binding·영수증/저널 v3·관찰 완결성 |
| 후보↔판정 | 영속 link 없음. 기존 e3_verdict_links는 finding용 | 경로 패턴이 선언 조회 틀 하나와 맞을 때만 별도 연결. 0개/여러 개면 각각 표시 |
| 후보 상태 | manual_review_required | 연결 후에도 동일. 판정은 별도 레코드이며 후보를 confirmed로 덮지 않음 |

#596은 E③ 근거 출처에 e2proxy 저널을 더하되 기존 transmit 출처와 함께 둔다.
따라서 읽기 투영은 source discriminator와 검증기를 분리해야 한다.
v1/v2를 읽을 수 있다는 사실은 v3 제품 확정 근거가 있다는 뜻이 아니다.
후보 참조·정책 출처 칸의 저장 형식·새 source version은 #596 담당 구현에서 정하며
이 문서에서는 새 테이블·영수증 필드·프로그램 요청을 선행 구현하지 않는다.
보존율·공식 비교에 이 제품 확정 경로가 자동으로 들어간다고도 가정하지 않는다.

### 9.1 제품 확정 경로 추가 시: UNDECIDABLE 세부 사유와 추가 칸

#596 최종 `2ed0d262`의 §5~§7·§9를 반영하는 **후속 제품 투영 제안**이다. 현재 서버·오라클 enum·
영수증 v1/v2에 아래 코드나 칸이 이미 있다고 주장하지 않는다. 구현은 측정 뒤다.

확정 단계는 탐색·Planner·Worker와 마무리 턴까지 **모든 모델 활동이 끝나고 모델 승인 예약이
닫힌 뒤에만** 시작한다. 표식도 그때 처음 만든다. 회차 시작 시 E②가 시간·대상 요청의 확정 몫을
미리 떼어 두며, 모델 단계는 그 몫을 뺀 범위에서만 돈다. 남은 예산이 있으면 확정한다는 흐름이 아니다.
확정 단계는 모델을 부르지 않으므로 모델 예산은 따로 예약하지 않는다.

기존 canonical의 `record.reason_codes`는 그대로 보존한다. 제품 v3 생산자가 기록한 구체적 사유를
`record_checks.reason_codes`에 아래 제품 전용 코드로 투영하는 안을 제안한다. GET에서 실패를
추정하거나 `oracle.Evaluate`를 다시 호출하지 않는다. `record_checks.status=verified`는 저장·결속의
읽기 검증이 성공했다는 뜻이며, 정책·신원·데이터 확인이 모두 성공했다는 뜻이 아니다.
원래 `data_unverifiable`만 있는 구형 근거에서 아래 세부 코드를 역산하지 않는다.

| 제품 단계의 저장된 관찰 | 세부 사유 코드 제안 | 신호·적용 범위와 운영자에게 보일 뜻 |
| --- | --- | --- |
| 지원 밖 인코딩(압축·base64·겹친 인코딩·비텍스트 등) | `product_data_encoding_unsupported` | 검사하지 못한 데이터 신호는 unknown. 표식 없음(no)으로 만들지 않음. 해당 A/B 쌍 UNDECIDABLE, 지원 응답 형식 확인 필요 |
| 부분 응답 | `product_data_partial_response` | 부분 본문 digest를 전체 근거로 쓰지 않음. 관련 데이터 신호 unknown, 해당 쌍 UNDECIDABLE. 완전 응답 근거 필요 |
| 파싱·검사 실패 | `product_data_check_failed` | 관련 데이터 신호 unknown. 표식 없음으로 보충하지 않음. 해당 쌍 UNDECIDABLE, 선언한 응답 위치/검사 실패 확인 필요 |
| A 대조에서 지원 방식의 표식이 관찰되지 않음 | `product_owner_marker_unobserved` | B 결과와 관계없이 **그 자원 종류 전체 UNDECIDABLE**. A에서 표식이 보이지 않으면 B의 미관찰을 차단 근거로 쓸 수 없음 |
| 신원 경로 실패·식별자 누락·확인 불가 | `product_identity_unverified` | 인증 성공만으로 검증된 신원으로 삼지 않음. 대상 UNDECIDABLE, 뒤 생성·A/B 조회를 시작하지 않음. 신원 선언/응답 확인 필요 |
| A·B 식별자 digest 비교가 같음 | `product_subjects_identical` | 서로 다른 시험 주체를 확인하지 못함. 대상 UNDECIDABLE, 뒤 단계 미실행. 다른 계정 공급 필요. oracle 신원 원값을 조회에서 재계산하지 않음 |
| 사용자 승인 모드의 프로그램 생성 요청 거절 | `product_create_approval_denied` | 해당 종류 UNDECIDABLE. 명시적 미전송 영수증으로 확인하며 이후 A/B 조회를 만들지 않음. 거절 사실을 시간 초과와 구분 |
| 같은 생성 요청의 확인 시간 초과 | `product_create_approval_timeout` | 해당 종류 UNDECIDABLE. 보내지 않은 확인 만료와 결과 불명 쓰기를 구분. 원래 요청을 자동 재전송하지 않음 |
| 시작 때 떼어 둔 확정 단계 전용 시간·요청 상한 소진 | `product_confirmation_budget_exhausted` | 확정 단계만 끝내고 남은 종류를 UNDECIDABLE로 둔다. 완료된 판정은 보존한다. 회차 전체 예산 소진 때 새 송신을 멈추는 규칙과 구분 |

코드에는 단계·적용 범위가 들어가므로 데이터 검사 실패를 모두 `data_unverifiable`로 뭉치지 않는다.
실제 판정 행이 없으면 위 사유만으로 `record.oracle_value=undecidable`을 만들어 내지 않고
§6의 부재/미실행 envelope와 신뢰된 실행 상태를 사용한다. 반대로 저장된 UNDECIDABLE이 있으면
원값을 유지하고 위 세부 사유를 함께 읽는다. 인프라 읽기 실패·손상은 각각 503/422의 별도 경계다.

확정 단계 **전에** 회차가 실패·중지로 끝난 경우는 위 예산 소진 판정과 다르다. 확정 단계가
실제로 시작되지 않았으므로 판정 행을 만들지 않고, §6의 `record=null`·`evaluation_status=not_executed`로
읽는다. 이는 해당 선택 범위의 신뢰된 확정 단계 계획·미시작·회차 종료 근거를 확인한 경우에만 쓴다.
근거가 없으면 `evaluation_status=unknown`이며 판정 행 부재로 미실행을 추정하지 않는다.
모델 단계의 기존 후보 call이 보낸 요청은 별도 전송 사실로 보존하며 확정 미실행을 전체 진단 미송신으로
바꾸지 않는다. 이 경우를 UNDECIDABLE 사유 코드로 꾸며 새 판정을 저장하지 않는다.

`previous_markers_present`와 다음 회차의 필수 리셋 확인 부재(최종 #596 §6.1)는 **회차 시작 거절**이다.
이 단건 판정 API의 새 oracle 사유가 아니며, 시작 실패 흐름에서 표시하고 기존 과거 판정을 덮지 않는다.

추가 칸도 제품 경로 공급 시에만 연다(현재 경로는 null/미공급).

| 제안 칸 | 출처와 검증 규칙 |
| --- | --- |
| `policy_source` | 구분자 `kind=product_declaration`이면 실행 때 동결한 `declaration_digest_sha256`와 대상 승인 판을 검증한다. `kind=kg_snapshot`이면 평가 레지스트리의 `snapshot_digest_sha256`·정책 판을 검증한다. 두 출처는 상호 배타적이며 policy_version의 문자열 모양으로 추정하지 않는다. 원문 선언·레지스트리·키는 응답하지 않는다 |
| `candidate_link` | #596 §9의 후보 **메서드와 call_ref의 E② 경로 패턴**이 선언된 조회 틀 정확히 하나와 맞고 동일 실행·정책에 결속될 때만 비민감 `candidate_ref`와 조회 틀 참조를 채운다. 판정의 후보 참조 칸도 그 검증된 연결에서만 채운다. 0개/복수 일치면 candidate_link는 null로 두고 후보와 판정을 따로 표시한다. candidate selector의 selection/ids.candidate_id는 선택한 후보 ID를 보존할 수 있지만, 그 ID 자체를 판정 연결 성공으로 간주하지 않는다 |

연결 뒤에도 후보는 `manual_review_required` 그대로이고 판정은 별도 레코드다.
새 칸·생산자 사유 코드와 읽기 검증 enum의 최종 형식은 OD06·OD08·OD09에서 최민준·이동찬이,
표시 문구·null 상태는 이경준과 합의한다. 이 초안 변경은 프로그램 생성·v3·새 테이블을 구현하지 않는다.

## 10. 수용시험 계획

아래는 **미래 구현 PR의 미실행 계획**이다. 문서/JSON 정적 확인을 아래 시험 통과로 세지 않는다.
가드마다 음성 사례와 필수 저장·대상 수신 건수 대조를 포함한다.

| ID | 조건 | 기대 결과 |
| --- | --- | --- |
| FVD01 | 동일 task의 유효 verdict와 issuance/manifest/link | 200, 원값·ID·revision·당시 정책/주체 일치 |
| FVD02 | 인증 없음·만료·틀린 subject·cookie/query token만, 타 owner/task 조회 | 인증/권한 거부. 저장 근거 응답 0, 타 task 존재 여부 노출 0 |
| FVD03 | finding/node/issuance ID를 verdict ID로 오인, 큰 int64·다중 selector | selector 소속·형식 검증. 숫자 우연 일치로 타 종류를 조인하지 않음 |
| FVD04 | confirmed/suspected/undecidable/allowed 네 정본 | 원값 보존, allowed는 null vuln_verdict와 정책상 허용 표시 |
| FVD05 | 정책·신원·6신호의 no/unknown, 필수 키 누락·모르는 enum | 정상 증거 부족과 손상 구분. 손상은 unknown으로 보충하지 않음 |
| FVD06 | 심어둔 비밀번호·발급 세션·키·사용자명·canary 값·raw URL/query·GT·내부 경로 | 응답·오류·로그에 원문 0. 전체 canonical/binding/manifest 응답 0 |
| FVD07 | E② 송신 전 거부, withheld/응답 잘림, transport_failed | 각각 held_pre_send, 송신/응답 후 표현 보류, 송신 실패. 서로 오인 0 |
| FVD08 | intent만 있음, forward만 있음, ACK 불명, 과거 유효 prefix, 시도 혼합 | unknown/검증 불가. 실제 송신/응답/감사 완료를 별도 표시 |
| FVD09 | 현재 finding evidence_version 변경 대 manifest/issuance revision 위조 | 전자는 역사적 판정 표시, 후자는 무결성 오류. 원본 재발급 0 |
| FVD10 | payload·manifest digest 손상, binding과 열·task·KG version 불일치 | 422, 정상 record 부분 응답 0 |
| FVD11 | non-confirmed 미연결 판정, 현재 confirmed link 유실 | 전자는 200/null finding, 후자는 현재 저장 불변식 오류 |
| FVD12 | 일반 finding만 존재, 후보만 존재, 분류만 not_executed | 일반 상세 정상 유지, no_link/unknown. 신뢰 미실행 근거 없이는 none 금지 |
| FVD13 | #596 후보↔판정 연결, 연결 0개/복수/다른 실행 | manual_review_required 불변, 원본 후보 자동 확정 0, 타 실행 혼입 0 |
| FVD14 | KG snapshot 부재·같은 ID 다른 내용·reader/DB 장애 | 보존 실패 표시, 무결성 실패, 인프라 오류로 각각 구분 |
| FVD15 | GET 정상/재시도/실패/취소/동시 조회 | 대상 HTTP 0회, LLM 0회, verdict/manifest/issuance/link/finding 쓰기 0회, export 동결 0회 |
| FVD16 | 조회 중 link commit·큰 payload·response 상한 초과 | 일관 snapshot 또는 명시 실패. 부분 성공/잘린 판정 0 |
| FVD17 | 서로 다른 Job/run/model/reset·같은 자원 이름·traffic 빈 배열 | 권위와 scope로만 결속. ID·경로 이름 유사도에 의한 혼입 0 |
| FVD18 | all unknown지만 실행됨, 비교 배열 없지만 송신됨, 판정 없이 송신 차단 | 사실에 맞는 별도 상태. 목업 서사로 미실행/무응답을 단정하지 않음 |
| FVD19 | 지원 밖 인코딩·부분 응답·검사 실패 | 각각 §9.1의 다른 데이터 코드, 관련 신호 unknown. 가짜 no/차단 근거 0 |
| FVD20 | A 표식 미관찰, B 관찰 있음/없음 | B와 관계없이 해당 종류 UNDECIDABLE, 코드 `product_owner_marker_unobserved` |
| FVD21 | 신원 확인 실패 대 A/B 식별자 동일 | 서로 다른 코드, 대상 UNDECIDABLE, 후속 생성·조회 0 |
| FVD22 | 사용자 승인 모드 프로그램 생성 거절 대 확인 만료 | 서로 다른 승인 코드, 생성 송신·후속 조회·자동 재전송 0 |
| FVD23 | 제품 선언 동결본 대 평가 KG snapshot, 후보 조회 틀 일치 0/1/복수 | 출처를 명시 검증. 후보 연결은 정확히 1개일 때만, 후보 상태 불변 |
| FVD24 | #598 실제 미전송 제어 영수증 대 빈 Call/감사 미확인 | 전자만 결속·마감 확인 뒤 held_pre_send, 후자는 unknown. 가짜 시도 생성 0 |
| FVD25 | 확정 단계 전용 시간·요청 예비 몫 소진 | product_confirmation_budget_exhausted, 그 단계만 종료·남은 종류 UNDECIDABLE·완료 판정 보존 |
| FVD26 | 확정 전에 회차 실패/중지, 미시작 근거 있음/없음, 모델 후보의 이미 송신된 call | 판정 행 생성 0. 검증된 미시작만 evaluation_status=not_executed, 그 밖은 unknown. 후보의 기존 송신 사실 보존 |

## 11. Open Decisions

전부 팀 확인 전 제안이며, 구현 담당의 변경을 이 세션이 대신 수행하지 않는다.

| ID·결정 | 현재 코드 사실 | 제안 | 근거·관련 파일 | 검토 담당 | 구현 전 필수 |
| --- | --- | --- | --- | --- | --- |
| OD01 중심 ID·경로 | getter의 ID가 모호하고 PK는 서로 다름 | verdict 우선 + 동일 경로의 명시 finding/candidate selector. 더 좁은 verdict-only안과 선택 | §2, finding.ts, e3_finding_schema.sql, 평가 페이지 제안 | 최민준·이경준·이동찬 | 예 |
| OD02 link 없는 판정 접근 | 현재 non-confirmed는 finding 없음, report ID는 issuance ID | verdict로 직접 접근. finding-only 경로로 누락시키지 않음 | §1·§2, diagnostic_report.go, db/e3_findings.go | 최민준·이경준 | 예 |
| OD03 transmission 신뢰 원천 | registered GET와 제품 저널은 별개. 현재 시도 계정/판정 link 없음 | cross/call 범위, 시도 집합·독립 checkpoint·마감 reader·혼합 대표값 확정 | §5, transmit/config.go, e2proxy/types.go·audit.go, url_live_views.go | 이동찬·최민준, 표시 이경준 | 예 |
| OD04 일반 finding과 E③ 표시 | 일반 triage confirmed와 E③ 원값은 다름 | envelope로 구분, no_link일 때 가짜 FindingRecord 생성 금지. severity null/비교 없음/원값 배지 검토 | §3·§4, UrlFindingLive.tsx, finding.test.ts | 이경준·최민준 | 예 |
| OD05 후보↔판정 연결 | 현재 DiagnosticCandidate는 manual_review_required | link가 생겨도 후보 상태 보존. 연결 탐색·공급은 #596 담당 | §9, db/diagnostic_candidates.go, #596 §9 | 최민준·이경준, 실행 결속 이동찬 | 예(제품 연결 전) |
| OD06 증거 revision/digest/KG 검증 실패 | digest reader만으로 출처 전부 확인 못 함. 일부 기존 helper는 오라클 재계산 | 읽기 전용 검사 분리, 역사적 버전 표시/200 unavailable/409/422/503 경계·고정 코드 확정 | §1·§6·§8, access_map.go, e3record/manifest.go, e3-storage-gate §9.1 | 최민준·이동찬, 화면 이경준 | 예 |
| OD07 비평가 제품 vs 평가 run | eval_run_id는 NULL 가능. 평가 API는 동결 export | UI 단건에 export 동결/러너 권한을 강제하지 않음. source별 읽기 권위 설치 | §1·§9, verdict-record-page-api-proposal.md | 최민준·이동찬 | 예 |
| OD08 추가 공급·projection 필드 | 기본 저장 있음. 제품 시도 link·검사별 미실행 정본·새 envelope 없음 | 신뢰 reader·전체 응답/배열 상한·DLP allowlist·wire 타입 확정. 새 E③ 테이블은 제안하지 않음 | §3·§5·§7·§8, e3record/model.go | 서버/송신 인터페이스 이동찬, 정본/issuance·검증 최민준, 필드/표시 이경준 | 예 |
| OD09 #596 반영 순서 | 결정 문서 dev 병합·구현은 측정 뒤 | source/link/v3 구현·수용→상세 reader→UI 연결. 등록 GET 단건 부분 착수 여부 별도 합의 | §9, #596 고정 head | 최민준·이경준·이동찬 | 예 |
| OD10 읽기 권한·설치 위치 | 제품 AuthorizeTask, 보고서 boundTask, 단일 ARTEX 신원 | 인증된 task/job/execution 문맥의 별도 읽기 handler로만 설치. 재시작 시 실행 복구 금지 | §2·§7, diagnosisprep/service_linux.go, first_diagnosis_report.go | 이동찬·최민준 | 예 |
| OD11 공개 필드의 적격성 | canonical safeText는 문자열 형식 검사이며 전체 DLP 보장 아님 | canary 필드 경로·계정 라벨·논리 ref·심각도/제목의 안전 투영 규칙 확인 | §3·§7, e3record/prepare.go, #596 §6 | 최민준·이경준·이동찬 | 예 |

문서 채택과 서버/UI 구현·실제 E③ 수용은 별개다. 위 결정을 구현 PR이 충족한 뒤
FVD 수용시험을 실행하고 결과 건수를 기록한다. 이 Draft를 Ready로 바꾸는 판단은 팀 리뷰 뒤에 한다.
