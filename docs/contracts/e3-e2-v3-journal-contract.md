# E③ ↔ E② 영수증·저널 v3 계약

**상태: 확정본 — 이동찬 답(2026-11-19) 반영, 리뷰 이동찬.** 작성: 최민준. "**송신 활성화 전 확정 필수**"로 표시한 항목(§3 MAC·체크포인트, §5 반환·봉인 순서, §6 세대·인스턴스)은 E③ 프로그램 송신을 켜기 전에 남은 세부를 정한다. 그 전에는 E²가 E③ 프로그램 호출을 거부한다(`ErrE3JournalUnavailable`).

근거: [E③ 확정 경로 결정](../e3-confirm-path-decision.md) §7(E② 쪽), [E③ 제품 확정 단계 인터페이스](e3-product-confirmation-interface.md)(#698, `e3contract`), [E② 감사 저널](e2-audit-journal.md), `e2proxy`의 E③ 목적 규칙(`PurposeE3Identity`·`PurposeE3Create`·`PurposeE3Read`, `Receipt.Program`의 `ProgramE3*`).

## 1. 분담과 경계

| 갈래 | 담당 | 하는 일 |
| --- | --- | --- |
| E② 영수증·저널 v3 | 이동찬 | v3 영수증·저널 **생성**, HMAC·체인·체크포인트, **무결성 검증**, 목적별 프로그램 요청 송신(목적 규칙·계정 붙이기·응답 검사), 계정 결속 참조 발급(세션 저장소) |
| E③ Transport·Journal 어댑터 | 최민준 | E²가 내놓는 목적별 프로그램 요청과 **검증된** v3 이벤트 조회를 `e3contract.Transport`·`e3contract.Journal`로 바꾼다. 저널을 만들거나 MAC을 계산하지 않는다 |
| 세대·인스턴스 참조 발급 | 최민준(대상 등록 경로, 제안) | 서버가 확인한 대상 세대·인스턴스에서 실행마다 발급(§6) |
| 평가 측 v3 읽기 | 최민준(러너 세션) | 검증기·평가 내보내기가 v3를 읽는다(v1·v2 계속). 없는 칸을 추정하지 않는다 |

E² 안에서 E③ 판정(CONFIRMED·UNDECIDABLE 등)을 하지 않는다. E²는 구조화 관찰과 그 무결성까지만 책임진다. 어댑터는 E²가 검증해 돌려준 이벤트만 쓰고, 검증 전 바이트·모델이 쓴 값·다른 실행의 이벤트는 근거로 쓰지 않는다. fail-closed 규칙은 #698 계약 그대로다(§7).

## 2. v3 영수증·저널 칸

형식 판은 **`e2-http-v3`**다. 목적은 새 칸을 두지 않고 기존 `Receipt.Program`(`e3_identity`·`e3_create`·`e3_read`)을 쓴다.

### 2.1 v2에서 그대로 두는 칸

`version`, `program`, `intent_id`(E③ 프로그램 호출은 0), `scope{run_ref, task_ref, policy_ref, …}`, `call_ref`, `attempt_id`, `sequence`, `policy_revision`, `rule_ref`, `method`, `decision`, `reason`, `forward_attempted`, `request_written`, `response_received`, `response_complete`, `not_sent`, `audit_complete`, `status_code`, `at`, 이벤트 `index`·`stage`.

### 2.2 E③ 프로그램 호출에 더하는 칸

| 칸 | 뜻 | 어댑터가 옮기는 곳(`e3contract.Observation`) |
| --- | --- | --- |
| `program` | `e3_identity`·`e3_create`·`e3_read` | `Purpose` |
| `role` | `owner`(A)·`cross`(B) | `Role` |
| `account_label` | E²가 **실제로 붙인** C14 시험 계정 라벨(모델이 준 값 아님) | `AccountLabel` |
| `account_binding_ref` | C14 인증 성공에 기반해 서버가 발급하는 비민감 참조. 실행·계정 라벨·세션 판에 결속하고, 다시 로그인해 세션이 바뀌면 다른 값이다(발급은 이동찬, 세션 저장소) | (대조만: 같은 실행·같은 라벨) |
| `e3_binding{job_ref, generation_ref, instance_ref, policy{kind, ref, digest_sha256, target_approval_revision}}` | 확정 단계 실행 결속. `run_ref`·`task_ref`·`e2_policy_ref`는 `scope`와 같아야 한다 | `Binding` |
| `resource_type` | 선언한 자원 종류(생성·조회) | `ResourceType` |
| `create_ref{call_ref, attempt_id, sequence}` | 조회가 읽은 생성 관찰(조회만) | `Create` |
| `not_sent_reason` | 연결 전 거부 넷: `policy_denied`·`approval_denied`·`approval_timeout`·`confirmation_budget_exhausted` | `NotSent` |
| `timed_out` | 확정 요청 전용 시간 상한으로 끊김(보낸 뒤만) | `TimedOut` |
| `normal` | 선언한 상태 칸 또는 HTTP 상태 판정으로 본 정상 여부(`yes`·`no`·`unknown`) | `Normal` |
| `request_marker_free` | E²가 보낸 조회·신원 요청(URL·헤더·본문)에 표식이 없음을 확인했는지 | `RequestMarkerFree` |
| `marker{status, presence, method, field}` | 응답 표식 검사: `complete`·`partial`·`unsupported`·`failed`, `present`·`absent`, 일치 방식, 검사한 선언 칸 | `Marker` |
| `body_digest_sha256`·`body_complete` | 응답 본문 digest와 전체 여부 | `BodyDigestSHA256`·`BodyComplete` |
| `object_id_digest` | 생성 응답 객체 ID의 digest(생성만) | `ObjectIDDigest` |
| `subject_digest` | 신원 응답 사용자 식별자의 digest(신원만) | `SubjectDigest` |

넣지 않는 것: URL 원문·본문·표식 값(어느 인코딩이든)·객체 ID 원문·사용자 식별자 원문·자격 값·세션 값.

### 2.3 단계별 필수 조건

E③ 프로그램 호출 하나는 `control`(송신 전 결정) 이벤트와, 송신했으면 `result` 이벤트를 남긴다. 어댑터의 관찰은 그 호출의 마지막 이벤트(송신했으면 `result`, 송신 전 거부면 `control`)에서 만든다.

| 단계 | 언제 | 반드시 있어야 하는 칸 | 없어야 하는 칸 |
| --- | --- | --- | --- |
| 공통(모든 단계) | — | `version=e2-http-v3`, `program`, `scope`, `call_ref`, `attempt_id`, `sequence`, `policy_revision`, `rule_ref`, `method`, `role`, `account_label`, `account_binding_ref`, `e3_binding`, 생성·조회면 `resource_type`, 조회면 `create_ref` | §2.2의 "넣지 않는 것" |
| `control`, 송신 전 거부 | E²가 연결을 시도하기 전에 거부 | `decision`·`reason`, `not_sent=true`, `not_sent_reason`(넷 중 하나) | `result` 이벤트, 응답 칸 전부 |
| `control`, 허용 | 송신 허가 | `decision`·`reason` | `not_sent_reason` |
| `result` | 연결을 시도한 뒤(성공·실패·끊김 모두) | `forward_attempted=true`, `request_written`, `response_received`, `response_complete`, 끊겼으면 `timed_out`. `response_complete=true`면 `status_code`·`normal`·`body_digest_sha256`·`body_complete`, 조회면 `marker`·`request_marker_free`, 생성이면 `object_id_digest`(꺼냈을 때), 신원이면 `subject_digest`·`request_marker_free` | `not_sent_reason` |

**송신 판정은 E²가 한다.** 연결을 시도한 뒤 결과를 확실히 모르면(C02의 `Unknown`·`TransportFailed` 포함) `result` 이벤트에 `response_complete=false`로 남긴다(결과 불명). 확실한 송신 전 거부만 `control`의 `not_sent`다. E③는 이 구분으로 쓰기 기록을 남기거나 거둔다(#738 시험 `TestRunWriteWithdrawOnlyBeforeConnect`: 연결 전 거부 넷만 거둔다, 그 밖의 `sent=false`는 관찰 계약 위반).

## 3. MAC 검증 범위 — **송신 활성화 전 확정 필수**

- **HMAC-SHA256.** 머리와 이벤트의 도메인을 나누고, 이벤트는 순번·직전 MAC으로 체인한다(v2와 같은 성질: 삽입·삭제·순서 변경·되감기 거부).
- **v3 새 칸은 전부 MAC 범위 안이다**(§2.2·§2.3의 모든 칸). 어댑터는 MAC 밖 칸을 근거로 쓰지 않는다.
- **머리**는 형식 판·저널 ID·`Scope`·키 참조·만든 시각을 담고 인증한다.
- **키 분리.** 실행별 감사 키(저널 HMAC)와 객체·신원 digest 키(`object_id_digest`·`subject_digest`)는 다른 키다. digest 키는 저널·로그·모델 어디에도 없다.
- **신뢰 체크포인트.** E③ 검증은 저널과 **따로 보관된** 신뢰 체크포인트로 한다. 체크포인트가 없거나 맞지 않으면 검증 실패다(머리부터 끝까지 MAC·체인이 맞아도, 끝 행 삭제·이전 기록으로 되감기를 가르지 못하므로).
- 남은 세부(송신 활성화 전): 체크포인트 보존 위치와 형식, 키 보관 경로.

## 4. 결속

어댑터가 관찰마다 대조한다(하나라도 다르면 §7의 진위 실패).
- **계정**: `account_label`이 확정 단계가 요청한 라벨(선언한 A·B, E③ 계약 §4)과 같고, `account_binding_ref`가 같은 실행·같은 라벨의 결속이다. 모델 요청의 계정 정보는 쓰지 않는다.
- **정책 출처**: `e3_binding.policy`가 실행 시작 때 동결한 출처(`kind=product_declaration`, `ref=declaration-<job>`, 동결본 `digest_sha256`, 대상 승인 판)와 같다.
- **실행·세대**: `e3_binding`의 `job_ref`·`generation_ref`·`instance_ref`와 `scope`의 `run_ref`·`task_ref`·`policy_ref`가 확정 단계 `RunBinding`과 같다.
- **목적·종류·역할·생성 참조**: 요청한 값과 같다. 같은 `call_ref`+`attempt_id`를 두 번 쓰지 않는다.

## 5. 반환과 봉인 — **송신 활성화 전 확정 필수**

- **`Transport`는 결과 이벤트 기록과 Sync가 성공한 뒤에 반환한다.** 송신 전 거부면 `control` 이벤트, 송신했으면 `result` 이벤트가 Sync된 뒤다. Sync에 실패하면 관찰을 돌려주지 않고 오류다(결과 불명).
- **`Journal.Sealed`는 기다리지 않고, 검증된 기록만 돌려준다.** Sync만으로는 검증 완료가 아니다. `Sealed(ctx, binding, CallRef)`는:
  1. 저널과 독립된 경로에서 같은 실행의 **신뢰 체크포인트**를 얻는다.
  2. 같은 조회 스냅샷을 머리부터 그 체크포인트까지 MAC·체인으로 검증하고, 끝이 체크포인트와 같은지 본다.
  3. 검증된 스냅샷에서 그 참조의 이벤트를 찾아 §2.2 표대로 옮긴 관찰을 돌려준다.
- **실패.**
  - 체크포인트가 없음·맞지 않음, MAC·체인·형식 오류, 다른 실행의 저널: `ErrJournalIntegrity`(무결성 실패). 관찰을 돌려주지 않는다.
  - 검증된 스냅샷에 그 참조의 이벤트가 없음: `ErrJournalEventMissing`(이벤트 없음).
  - 문맥 취소: 그 오류.
- `Sealed`가 돌려준 관찰은 `Transport`가 돌려준 관찰과 칸별로 같아야 한다(`Observation.Same`).

## 6. 세대·인스턴스 참조 — **송신 활성화 전 확정 필수**

- `generation_ref`(대상 세대)·`instance_ref`(대상 인스턴스)는 **서버가 확인한 대상 세대·인스턴스에서** 실행마다 발급한다(결정 §5 0단계). 발급 구현은 최민준이 대상 등록 경로에서 맡겠다고 제안했다.
- 지금 #738 제품 연결의 `generation-<job>`·`instance-<job>`은 임시 값이고 이 발급으로 바꾼다.
- **이동찬 확인**: 대상 리셋·인스턴스 교체 때 참조가 바뀌는 규칙(리셋하면 세대가 바뀌는지, 같은 대상 판 안에서 인스턴스를 바꾸면 어떻게 되는지).

## 7. 실패 처리(어댑터 → E③ 상태·사유)

fail-closed는 #698 계약 그대로다. 진위 실패면 판정 행을 만들지 않고 후보는 SUSPECTED 그대로다.

| 일어난 일 | E③ 결과 | 쓰기 기록 |
| --- | --- | --- |
| 어댑터를 연결하지 않음(Transport·Journal 없음) | 확정 단계 `disabled`, `not_wired` | 없음(요청 전) |
| 어댑터는 있는데 E²가 v3 감사 미연결로 거부(`ErrE3JournalUnavailable`) | 표식 등록에서 막혀 `start_refused`, `marker_registration_failed`. 어댑터는 이 오류를 생성·조회까지 넘기지 않는다 | 없음(요청 전) |
| 실행 권한 철회(`ErrAuthorityRevoked`) | `confirmation_integrity_failed`, `authority_revoked` | 생성 뒤면 ID 모름으로 남음 |
| `Transport` 그 밖의 오류(Sync 실패 포함) | 그 요청 결과 불명 → 그 종류 UNDECIDABLE(`product_request_result_unknown`) | 남음 |
| 관찰 모양 위반(연결 전 거부가 아닌 `sent=false` 포함) | `confirmation_integrity_failed`, `observation_invalid` | 남음 |
| 결속 불일치(실행·세대·정책 출처) | `confirmation_integrity_failed`, `binding_mismatch` | 남음 |
| 목적·역할·계정·종류·생성 참조 불일치 | `confirmation_integrity_failed`, `observation_mismatch` | 남음 |
| 같은 참조 재사용 | `confirmation_integrity_failed`, `observation_reused` | 남음 |
| `Sealed`가 무결성 실패(`ErrJournalIntegrity`) | `confirmation_integrity_failed`, `journal_integrity_failed` | 남음 |
| `Sealed`가 이벤트 없음(`ErrJournalEventMissing`) | `confirmation_integrity_failed`, `journal_event_missing` | 남음 |
| `Transport` 관찰 ≠ 봉인된 이벤트 | `confirmation_integrity_failed`, `journal_event_mismatch` | 남음 |
| 연결 전 거부(`not_sent`, 넷) | 그 종류 UNDECIDABLE(거부·승인·예산 사유) | 거둠(not_sent) |

`journal_integrity_failed`는 이 확정본에서 더하는 사유다. 지금 `e3contract.Run`은 `Sealed`의 오류를 모두 `journal_event_missing`으로 보는데, 이 표대로 둘로 나눈다(E③ 계약·시험과 같이 고침). 두 사유 모두 E③ 결과는 진위 실패다.

## 8. 평가 측 v3 읽기(러너 세션)

- 검증기·평가 내보내기가 v1·v2·v3를 모두 읽는다. 형식 판(`version`)으로 고르고, 없는 계정·관찰 칸을 추정해 채우지 않는다.
- 평가 읽기가 쓰는 칸: §2.1 칸 전부(기존 집계), `program`(모델 진단 요청과 E③ 프로그램 호출을 가름), §2.3의 송신 판정 칸(`forward_attempted`·`not_sent`·`not_sent_reason`·`response_complete`·`timed_out`), `e3_binding.job_ref`·`scope`(회차 결속).
- 프로그램 호출(`program`이 E③ 셋)은 모델 진단 요청 집계와 따로 센다. 진단 요청 수에 넣을지는 평가 프로토콜에 고정한 뒤 읽기를 바꾼다.
- 공식 측정은 E③ 비활성이라 프로그램 호출 이벤트가 없어야 한다. 있으면 그 회차는 조건 위반이다(`e3_confirmation` 행 규칙과 같음).

## 9. 어댑터 구조와 가짜 v3 반례

- E²가 목적별 프로그램 요청(신원·생성·조회)과 검증된 v3 이벤트 조회를 Go 인터페이스로 내놓고, 어댑터가 그것을 `e3contract.Transport`·`Journal`로 바꾼다. E² 안에서 E③ 판정을 하지 않는다.
- 어댑터는 먼저 **가짜 v3 저널**(같은 인터페이스, 시험 전용)로 구현하고, 실제 v3가 들어오면 가짜 자리에 바꿔 끼운다.
- 가짜 저널로 고정할 반례(관찰을 돌려주지 않아야 하는 것):
  1. 신뢰 체크포인트 없음 → 무결성 실패.
  2. 체크포인트와 스냅샷 끝이 다름(끝 행 삭제·이전 기록으로 되감기) → 무결성 실패. 머리부터의 MAC·체인은 맞는 경우도 넣는다.
  3. MAC·체인 손상, 다른 실행의 저널 → 무결성 실패.
  4. 검증된 스냅샷에 그 참조 없음 → 이벤트 없음.
  5. 결속·계정 라벨·계정 결속 참조·목적·역할·생성 참조 불일치 → 해당 진위 실패.
  6. `Transport` 관찰과 봉인 이벤트가 칸 하나라도 다름 → `journal_event_mismatch`.
  7. 송신 전 거부가 아닌 `not_sent`, `result` 단계에 `not_sent_reason` → 관찰 계약 위반.

## 10. 답과 남은 확인

이동찬 답(2026-11-19)으로 정한 것: 형식 판 `e2-http-v3`·`Program` 재사용(§2), 계정 결속 참조(§2.2), 송신 판정은 E²(§2.3), HMAC·도메인 분리·체인·키 분리·신뢰 체크포인트(§3), Transport 반환과 Sealed(§5), 이벤트 없음과 무결성 실패 분리(§5·§7), 세대·인스턴스는 서버가 확인한 대상에서 발급(§6), E²는 목적별 요청과 검증된 이벤트 조회만 제공(§9).

**송신 활성화 전 확정 필수**(그 전에는 E² 프로그램 호출 거부 유지):
1. 신뢰 체크포인트 보존 위치·형식, 감사 키·digest 키 보관 경로(§3).
2. `Transport` 반환 전 Sync와 `Sealed`의 체크포인트 조회 구현(§5).
3. 세대·인스턴스 참조의 리셋·인스턴스 교체 규칙(§6, 이동찬 확인).
