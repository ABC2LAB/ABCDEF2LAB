# E③ ↔ E② 영수증·저널 v3 계약 (초안)

**상태: 초안 — 리뷰 이동찬.** 작성: 최민준(2026-11-19). "**동찬님 확인**"으로 표시한 곳은 이동찬님이 정할 부분이라 비워 두었다. 추측으로 채우지 않는다.

근거: [E③ 확정 경로 결정](../e3-confirm-path-decision.md) §7(E② 쪽), [E③ 제품 확정 단계 인터페이스](e3-product-confirmation-interface.md)(#698, `e3contract`), [E② 감사 저널](e2-audit-journal.md), `e2proxy`의 E③ 목적 규칙(`PurposeE3Identity`·`PurposeE3Create`·`PurposeE3Read`, v3 감사 전에는 `ErrE3JournalUnavailable`로 프로그램 호출 거부).

## 1. 분담과 경계

| 갈래 | 담당 | 하는 일 |
| --- | --- | --- |
| E② 영수증·저널 v3 | 이동찬 | v3 영수증·저널 **생성**, MAC 계산·체인·체크포인트, **무결성 검증**, 실제 프로그램 송신(목적 규칙·계정 붙이기·응답 검사) |
| E③ Transport·Journal 어댑터 | 최민준 | E② v3를 `e3contract.Transport`·`e3contract.Journal`로 감싼다. **검증을 마친 근거만** 관찰로 옮긴다. 저널을 만들거나 MAC을 계산하지 않는다 |
| 평가 측 v3 읽기 | 최민준(러너 세션) | 검증기·평가 내보내기가 v3를 읽는다(v1·v2 계속). 없는 칸을 추정하지 않는다 |

어댑터는 E²가 검증해 돌려준 이벤트만 쓴다. 검증 전 바이트, 모델이 쓴 값, 다른 실행의 이벤트는 근거로 쓰지 않는다. fail-closed 규칙은 #698 계약 그대로다(§6).

## 2. v3 영수증·저널 필수 칸

어댑터(`Observation`으로 옮김)와 평가 읽기가 쓰는 칸만 적는다. 이름은 제안이다. 칸 이름·배치(영수증 안인지 저널 이벤트 확장인지)는 **동찬님 확인**.

### 2.1 v2에서 그대로 두는 칸

`version`(v3 값은 **동찬님 확인**, 제안 `e2-http-v3`), `scope{run_ref, task_ref, policy_ref, …}`, `call_ref`, `attempt_id`, `sequence`, `policy_revision`, `rule_ref`, `method`, `decision`, `reason`, `forward_attempted`, `request_written`, `response_received`, `response_complete`, `not_sent`, `audit_complete`, `status_code`, `at`, 이벤트 `index`·`stage`.

### 2.2 프로그램 요청(E③)에만 더하는 칸

| 칸(제안) | 뜻 | 어댑터가 옮기는 곳(`e3contract.Observation`) |
| --- | --- | --- |
| `program_purpose` | `e3_identity`·`e3_create`·`e3_read`(규칙의 `Purpose`와 같은 값) | `Purpose` |
| `role` | `owner`(A)·`cross`(B) | `Role` |
| `account_label` | E²가 **실제로 붙인** C14 시험 계정 라벨(모델이 준 값 아님) | `AccountLabel` |
| `account_binding_ref` | 그 라벨의 C14 결속 참조(계정 결속 판). 형식 **동찬님 확인** | (대조만) |
| `e3_binding{job_ref, generation_ref, instance_ref, policy{kind, ref, digest_sha256, target_approval_revision}}` | 확정 단계 실행 결속. `run_ref`·`task_ref`·`e2_policy_ref`는 `scope`와 같아야 한다 | `Binding` |
| `resource_type` | 선언한 자원 종류(생성·조회) | `ResourceType` |
| `create_ref{call_ref, attempt_id, sequence}` | 조회가 읽은 생성 관찰(조회만) | `Create` |
| `not_sent_reason` | `sent=false`일 때만. 연결 전 거부 넷: `policy_denied`·`approval_denied`·`approval_timeout`·`confirmation_budget_exhausted` | `NotSent` |
| `timed_out` | 확정 요청 전용 시간 상한으로 끊김(보낸 뒤만) | `TimedOut` |
| `normal` | 선언한 상태 칸 또는 HTTP 상태 판정으로 본 정상 여부(`yes`·`no`·`unknown`) | `Normal` |
| `request_marker_free` | E²가 보낸 조회·신원 요청(URL·헤더·본문)에 표식이 없음을 확인했는지 | `RequestMarkerFree` |
| `marker{status, presence, method, field}` | 응답 표식 검사: `complete`·`partial`·`unsupported`·`failed`, `present`·`absent`, 일치 방식, 검사한 선언 칸 | `Marker` |
| `body_digest_sha256`·`body_complete` | 응답 본문 digest와 전체 여부 | `BodyDigestSHA256`·`BodyComplete` |
| `object_id_digest` | 생성 응답 객체 ID의 실행별 키 digest(생성만) | `ObjectIDDigest` |
| `subject_digest` | 신원 응답 사용자 식별자의 실행별 키 digest(신원만) | `SubjectDigest` |

넣지 않는 것: URL·본문·표식 값(어느 인코딩이든)·객체 ID 원문·사용자 식별자 원문·자격 값. 실행별 digest 키는 저널 밖이다(키 출처 **동찬님 확인**).

`sent`는 v2 칸에서 만든다: 연결을 시도했으면(`forward_attempted`) `sent=true`다. 연결 전 거부만 `sent=false`이고 그때 `not_sent_reason`이 위 넷 중 하나다. 연결 뒤 실패·결과 불명(C02의 `Unknown`·`TransportFailed`)은 `sent=true, response_complete=false`로 둔다. 이 변환을 E²가 할지 어댑터가 할지 **동찬님 확인**(제안: E²가 칸으로 내고 어댑터는 옮기기만).

## 3. MAC 검증 범위

- §2의 **모든 칸**(v2 칸과 프로그램 칸)이 이벤트 MAC 범위 안이어야 한다. 어댑터는 MAC 밖 칸을 근거로 쓰지 않는다.
- 저널 머리(형식 판·저널 ID·범위·키 참조)와 이벤트 사이 체인·순번은 v2와 같은 성질을 지킨다(삽입·삭제·순서 변경·되감기 거부).
- MAC 계산 방식(키, 도메인 분리, 머리·이벤트 정본 바이트), 체크포인트 보존 위치, v3 키를 v2와 같이 쓸지는 **동찬님 확인**.
- 어댑터는 "검증됨"을 E²의 검증 함수 결과로만 판단한다. 바이트를 따로 다시 해석하지 않는다.

## 4. 결속

어댑터가 관찰마다 대조한다(하나라도 다르면 §6의 진위 실패).
- **계정**: `account_label`이 확정 단계가 요청한 라벨(선언한 A·B, 계약 §4)과 같다. 모델 요청의 계정 정보는 쓰지 않는다.
- **정책 출처**: `e3_binding.policy`가 실행 시작 때 동결한 출처(`kind=product_declaration`, `ref=declaration-<job>`, 동결본 `digest_sha256`, 대상 승인 판)와 같다.
- **실행·세대**: `e3_binding`의 `job_ref`·`generation_ref`·`instance_ref`와 `scope`의 `run_ref`·`task_ref`·`policy_ref`가 확정 단계 `RunBinding`과 같다.
- **목적·종류·역할·생성 참조**: 요청한 값과 같다. 같은 `call_ref`+`attempt_id`를 두 번 쓰지 않는다.

## 5. `Journal.Sealed` 반환 계약

`Sealed(ctx, binding, CallRef{call_ref, attempt_id, sequence}) (Observation, error)`

- **봉인 뒤에만 돌려준다.** 그 이벤트가 저널에 동기화(Sync)되고, 머리부터 그 이벤트까지 MAC·체인 검증을 마친 뒤에만 관찰을 돌려준다.
- **봉인 전.** `Transport`가 관찰을 돌려줄 때 그 이벤트가 이미 봉인돼 있는지(그래서 `Sealed`를 바로 불러도 되는지), 아니면 `Sealed`가 봉인을 기다리는지 **동찬님 확인**(제안: `Transport`는 이벤트 Sync 뒤에 돌려주고, `Sealed`는 기다리지 않는다).
- **돌려주는 값.** 봉인된 이벤트를 §2.2 표대로 옮긴 관찰이다. `Transport`가 돌려준 관찰과 칸별로 같아야 한다(`Observation.Same`).
- **실패.**
  - 그 참조의 이벤트가 없음: `ErrJournalEventMissing`.
  - 체인·MAC·체크포인트 검증 실패, 저널 형식 오류, 다른 실행의 이벤트: 오류(종류 구분은 아래 질문).
  - 문맥 취소: 그 오류.
- 확인할 것: 검증 실패를 "이벤트 없음"과 다른 오류로 줄지(제안: `ErrJournalIntegrity`를 따로 두고 E③ 사유도 따로 `journal_integrity_failed`)는 **동찬님 확인**. 지금 #698 구현은 `Sealed`의 오류를 모두 `journal_event_missing`으로 본다.

## 6. 세대·인스턴스 참조

- `generation_ref`(대상 세대)·`instance_ref`(대상 인스턴스)는 서버가 실행마다 발급한다(결정 §5 0단계).
- 지금 #738 제품 연결은 `generation-<job>`·`instance-<job>`으로 만든 **임시 값**이다.
- **동찬님 확인**: 누가·어디서 발급하는지(E² 실행 정책 칸인지, Transport가 돌려주는지), 값의 모양, 대상 리셋과의 관계(리셋하면 세대가 바뀌는지). 정해지면 어댑터와 제품 연결이 그 값을 그대로 쓴다.

## 7. 실패 처리(어댑터 → E③ 상태·사유)

fail-closed는 #698 계약 그대로다. 아래는 어댑터에서 일어나는 일과 그 결과다.

| 일어난 일 | E③ 결과 | 쓰기 기록 |
| --- | --- | --- |
| 어댑터를 연결하지 않음(Transport·Journal 없음) | 확정 단계 `disabled`, `not_wired` | 없음(요청 전) |
| 어댑터는 있는데 E²가 v3 감사 미연결로 거부(`ErrE3JournalUnavailable`) | 표식 등록에서 막혀 `start_refused`, `marker_registration_failed`. 어댑터는 이 오류를 생성·조회까지 넘기지 않는다 | 없음(요청 전) |
| 실행 권한 철회(`ErrAuthorityRevoked`) | `confirmation_integrity_failed`, `authority_revoked` | 생성 뒤면 ID 모름으로 남음 |
| `Transport` 그 밖의 오류 | 그 요청 결과 불명 → 그 종류 UNDECIDABLE(`product_request_result_unknown`) | 남음 |
| 관찰 모양 위반(연결 전 거부가 아닌 `sent=false` 포함) | `confirmation_integrity_failed`, `observation_invalid` | 남음 |
| 결속 불일치(실행·세대·정책 출처) | `confirmation_integrity_failed`, `binding_mismatch` | 남음 |
| 목적·역할·계정·종류·생성 참조 불일치 | `confirmation_integrity_failed`, `observation_mismatch` | 남음 |
| 같은 참조 재사용 | `confirmation_integrity_failed`, `observation_reused` | 남음 |
| 저널 이벤트 없음·검증 실패 | `confirmation_integrity_failed`, `journal_event_missing`(검증 실패를 따로 둘지는 §5 질문) | 남음 |
| `Transport` 관찰 ≠ 봉인된 이벤트 | `confirmation_integrity_failed`, `journal_event_mismatch` | 남음 |
| 연결 전 거부(`sent=false`, 넷) | 그 종류 UNDECIDABLE(거부·승인·예산 사유) | 거둠(not_sent) |

진위 실패면 판정 행을 만들지 않고 후보는 SUSPECTED 그대로다(#698).

## 8. 평가 측 v3 읽기(러너 세션)

- 검증기·평가 내보내기가 v1·v2·v3를 모두 읽는다. 형식 판으로 고르고, 없는 계정·관찰 칸을 추정해 채우지 않는다.
- 프로그램 요청(§2.2 칸이 있는 이벤트)은 모델 진단 요청 집계와 따로 센다. 집계 기준(따로 셀지, 진단 요청 수에 넣을지)은 평가 프로토콜에 고정한 뒤 읽기를 바꾼다.
- 공식 측정은 E③ 비활성이라 프로그램 요청 이벤트가 없어야 한다. 있으면 그 회차는 조건 위반으로 본다(기존 `e3_confirmation` 행 규칙과 같음).

## 9. 어댑터 구조(구현은 계약 확정 뒤)

- E²가 내놓을 Go 인터페이스(예: 프로그램 송신 하나와 검증된 이벤트 읽기 하나)를 어댑터가 받는다. 이름·모양은 **동찬님 확인**.
- 계약이 정해지면 어댑터를 **가짜 v3 저널**(같은 인터페이스, 시험 전용)로 먼저 구현하고 §7 표를 시험으로 고정한다. 실제 v3가 들어오면 가짜 자리에 바꿔 끼운다.

## 10. 동찬님 확인 목록

1. v3 형식 판 값, §2.2 칸 이름·배치(영수증 안 / 이벤트 확장).
2. `account_binding_ref`의 모양.
3. `sent`·`not_sent_reason` 변환을 E²가 칸으로 낼지.
4. MAC 계산·키·체크포인트 보존(v2와 같은 키인지), 실행별 digest 키의 출처.
5. `Transport` 반환 시점과 봉인의 순서, `Sealed`가 기다리는지.
6. 검증 실패를 "이벤트 없음"과 다른 오류·사유로 둘지.
7. 세대·인스턴스 참조의 발급처·모양·리셋과의 관계.
8. 어댑터가 받을 E² Go 인터페이스.
