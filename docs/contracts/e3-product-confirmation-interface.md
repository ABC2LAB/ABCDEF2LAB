# E③ 제품 확정 경로 인터페이스 계약

상태: **계약 초안**(2026-11-18, 최민준 작성, 리뷰 이동찬·이경준). [개발 계획 v4.2](../development-plan.md) "E③ 인터페이스 PR 완료 기준"의 공통 계약이다. 무엇을 왜 하는지는 [E③ 확정 경로 결정](../e3-confirm-path-decision.md)(#596)이 정본이고, 이 문서는 세 갈래(판정·E② 연동·화면)가 만나는 모양만 고정한다. 코드는 `e3contract` 패키지, 가짜 구현은 `e3contract/e3fake`다.

이 계약이 바꾸지 않는 것: 오라클 판정 규칙(`oracle.Evaluate`)과 사유 목록, 평가 경로(등록 GET `transmit`·`evidencebind`·`e3record`), 모델 단계의 E② 동작. 선언·시험 계정이 없는 회차에서는 확정 단계가 아무 요청도 보내지 않는다(§7 `disabled`).

## 1. 갈래와 담당

| 갈래 | 이 계약에서 맡는 것 | 담당 |
|---|---|---|
| 판정 | `Run`(확정 단계 순서), 판정 행, 정책 선언 저장·동결(`DeclarationReader`), 리셋 확인(`ResetGate`) | 최민준 |
| E② 연동 | `Transport`(프로그램 요청 셋·표식 등록), `Journal`(봉인된 저널 v3 읽기) | 이동찬 |
| 화면 | `RunResult`의 상태·사유·후보 표시, 정책 선언 입력 | 이경준·윤재상 |

각 갈래는 상대 쪽을 `e3fake`로 두고 만든다. 실제 구현은 같은 계약 시험(§10)을 통과해야 한다.

## 2. 정책 출처와 실행 결속

- `PolicySource{kind, ref, digest_sha256, target_approval_revision}`. 종류는 `kind`로만 정하고 `policy_version` 문자열 모양으로 추정하지 않는다.
  - `product_declaration`: 운영자 정책 선언의 동결본(제품 경로).
  - `kg_snapshot`: 평가 fixture 레지스트리 snapshot(평가 경로).
- **제품 확정 단계는 `product_declaration`만 받는다.** `kg_snapshot`이면 모양이 맞아도 확정 단계를 켜지 않는다(`disabled`/`policy_kind_not_product`, 요청 0건).
- `RunBinding{job_ref, run_ref, task_ref, e2_policy_ref, generation_ref, instance_ref, policy}`는 서버가 실행마다 발급한다. `run_ref`·`task_ref`·`e2_policy_ref`는 E² 실행 범위(`transmit.Scope`)와 같은 값이다. `e2_policy_ref`는 실행 승인 정책이고 E③ 정책 정본(`policy`)과 다르다.
- 확정 단계의 모든 관찰에 같은 결속이 들어간다. 칸 하나라도 다르면 진위 실패(`binding_mismatch`)다.

## 3. 정책 선언 레코드

[결정 §4.1](../e3-confirm-path-decision.md#41-선언하는-것-자원-종류마다)의 칸을 그대로 쓴다. 형식 판은 `e3-product-declaration/v1`이다.

| 칸 | 뜻 |
|---|---|
| `target_ref`·`target_approval_revision` | 선언이 붙은 대상과 승인 판. 다시 승인하면 무효 |
| `declared_by`·`declared_at` | 누가·언제(감사) |
| `identity{path, subject_id_path}` | 신원 확인 GET. 대상마다 하나(결정 §5.1이 대상 단위로 확인하므로) |
| `types[]` | 자원 종류마다 `type`·`sharing`·`owner_only_fields`·`create{path, method(POST·PUT), body_kind(json·form), body_template, marker_field, id_path}`·`read{path_template, data_path, status_path?, normal_value?}` |

- `owner_only`이고 `marker_field`가 `owner_only_fields`에 있는 종류만 시험한다. `shared` 종류는 요청 칸을 두지 않고, 확정 단계는 요청도 판정 행도 만들지 않고 `skipped`로 보인다. 판정 값은 오라클만 만들기 때문에, 사유만으로 UNDECIDABLE 행을 꾸미지 않는다.
- 경로는 승인 범위 안 절대 경로다. query·조각·점 조각·공백을 받지 않는다. `read.path_template`에는 `{id}` 조각이 정확히 하나다. 승인 범위 안인지는 E²가 요청마다 다시 본다.
- 정본 바이트는 칸 순서를 고정한 JSON이다(HTML 이스케이프 없음, 시각은 UTC 마이크로초, 종류·필드는 오름차순, 빈 목록은 null). `digest_sha256`은 이 바이트의 SHA-256이다. `e3record`의 정본 함수가 비공개라 이 형식 판에 규칙을 따로 둔다.
- `Freeze(decl, ref)`가 실행 시작 때 정책 출처를 만든다. `VerifyFrozen(source, decl)`은 읽은 동결본의 digest와 대상 판을 대조한다.
- `DeclarationReader.ReadFrozen`은 저장된 동결본을 그대로 돌려준다. 읽을 수 없으면 `ErrPolicyUnavailable`이다(시작 거절, 진위 실패 아님). 읽었는데 digest·대상 판이 다르면 진위 실패(`policy_digest_mismatch`)다.

## 4. E② 프로그램 요청

- 목적 셋: `e3_identity`(신원 GET), `e3_create`(A 세션 생성, POST·PUT), `e3_read`(A·B 세션 조회 GET). 인증 준비처럼 목적 규칙으로만 열리고 모델 요청의 규칙 찾기에서는 빠진다.
- 계정 라벨은 서버가 C14 인증 결과(`authenticated`)에서 고른다. 관찰에는 E²가 실제로 붙인 라벨이 들어가고, 요청한 라벨과 다르면 진위 실패다.
- 요청 하나는 영수증 하나(`CallRef{call_ref, attempt_id, sequence}`)다. 같은 요청을 다시 보내지 않는다(결과 불명 쓰기 포함).
- 객체 ID 원문은 E² 밖으로 나오지 않는다. 생성 관찰은 실행별 키 digest만 주고, 조회 요청은 생성 관찰의 `CallRef`를 가리키면 E²가 그 ID로 경로를 채운다.
- 보내지 않은 것이 확실하면 `sent=false`와 사유(`approval_denied`·`approval_timeout`·`confirmation_budget_exhausted`·`policy_denied`)다. 보냈는지 모르면 오류를 돌려주거나 `sent=true, response_complete=false`다.
- 실행 권한이 철회됐으면 `ErrAuthorityRevoked`다(진위 실패).

## 5. 표식

- `Marker`는 소문자 영숫자 32자이고 확정 단계마다 새로 만든다. 값은 비공개 칸에만 있다. 문자열(`%v` 등 모든 동사)·slog로 내보내면 `[REDACTED]`가 되고, JSON·텍스트·바이너리·gob으로 내보내면 오류다. `Reveal()`은 E²가 본문에 넣고 응답을 검사하고 보호 값으로 등록할 때만 쓴다.
- **이 타입은 값 직렬화만 막는다.** 요청 본문에 들어간 표식은 일반 바이트라 저널·영수증·승인 미리보기(`input_preview`)와 그 저장 기록·로그·평가 내보내기로 샐 수 있다. 그래서:
  - 확정 단계를 시작할 때 `Transport.RegisterMarker`로 표식을 그 실행의 보호 값으로 올린다(`AddProtectedValues`와 같은 방식). 등록 뒤 E²는 그 기록들에서 표식의 원문과 지원 인코딩 모양(`MarkerForms`: 원문·대문자·JSON `\u`·URL `%XX`·HTML 10진·16진 엔티티, 대소문자 무시)을 가린다.
  - 등록이 실패하면 확정 단계를 시작하지 않는다(`start_refused`/`marker_registration_failed`). 등록하지 않은 표식으로 생성·조회를 부르면 E²는 보내지 않고 `ErrMarkerNotRegistered`다.
  - 확정 단계 한 회를 돈 뒤 저널·영수증·승인 미리보기·로그·평가 내보내기·결과 JSON 어디에도 표식이 없어야 한다. `LeakingSinks(marker, sinks)`가 이것을 확인한다. 실제 E² 구현은 실제 기록들에 대해 같은 확인을 통과해야 한다.
- **프로세스 경계.** E③와 E²는 지금 같은 서버 프로세스다(`server/e2_http_factory.go`의 `e2proxy.NewExecution`). 표식은 Go 값으로만 넘어가고 직렬화되지 않는다. 평가용 E² 서비스(`cmd/e2-eval-service`)는 별도 프로세스지만 확정 단계는 제품 경로에서만 돈다. 둘을 다른 프로세스로 나누면 그 전달 경로를 이 절에 적고 따로 시험한다.
- 다음 회차 보호(결정 §6.1)는 §8의 리셋 게이트가 맡는다.

## 6. 관찰과 저널 대조

`Observation`은 요청 하나의 구조화 관찰이다. URL·본문·표식·객체 ID·사용자 식별자 원문은 없다.

| 칸 | 규칙 |
|---|---|
| `marker.status` | `complete`·`partial`·`unsupported`·`failed`. 검사하지 못한 것을 "표식 없음"으로 적지 않는다 |
| `marker.presence` | `complete`일 때만 `present`·`absent`. 그 밖에는 비움 |
| `marker.method` | `present`일 때만 `exact`·`case_insensitive`·`decoded_json`·`decoded_url`·`decoded_html_numeric`·`decoded_html_named` |
| `marker.field` | 검사한 선언 칸. 선언의 `marker_field`와 다르면 진위 실패 |
| `body_digest_sha256`·`body_complete` | `complete`는 전체 본문일 때만이다. 부분 digest를 전체 근거로 쓰지 않는다 |
| `normal` | 선언한 상태 칸(없으면 E²의 HTTP 상태 판정)으로 본 정상 여부. 확정은 여전히 표식 일치가 필요하다 |
| `request_marker_free` | 조회·신원 요청에서 E²가 보낸 요청에 표식이 없음을 확인했는지 |
| `object_id_digest`·`subject_digest` | 생성·신원 관찰만. 실행별 키 digest |

- **저널 대조(① (다)).** `Transport`가 돌려준 관찰을 받을 때마다 `Journal.Sealed(binding, ref)`로 봉인된 저널 이벤트를 읽어 같은지 본다(시각은 순간으로 비교). 이벤트가 없으면 `journal_event_missing`, 다르면 `journal_event_mismatch`다. 둘 다 진위 실패이고 판정 행을 만들지 않는다.
- 같은 `CallRef`가 두 번 오면 `observation_reused`(진위 실패)다.
- `Journal`은 무결성 검증을 마친 이벤트만 돌려준다. E² 저널 v3의 새 칸도 무결성 범위에 든다(결정 §7).

## 7. 결과와 실패 처리

`RunResult`(형식 판 `e3-confirmation-run/v1`)의 `status`는 회차 상태·결과 화면·공식 회차 run-record에 그대로 싣는다.

| status | 언제 | 판정 행 | 요청 |
|---|---|---|---|
| `disabled` | 쓰는 조건(결정 §3)이 모자람: `not_wired`·`binding_invalid`·`policy_kind_not_product`·`write_not_consented`·`test_accounts_unavailable`·`policy_window_invalid`·`no_confirmable_type` | 없음 | 0 |
| `start_refused` | 시작 점검에서 멈춤: `policy_source_unavailable`·`reset_confirmation_missing`·`previous_markers_present`·`reset_state_unavailable`·`marker_generation_failed`·`marker_registration_failed` | 없음 | 0 |
| `completed` | 끝까지 돎 | 종류마다 하나(UNDECIDABLE 포함) | - |
| `interrupted` | 회차 중지로 끊김 | 끝낸 종류만 | - |
| `confirmation_integrity_failed` | 결속·진위가 깨짐(아래) | **없음** | 그때까지 |

**진위 실패(② (다)).** 사유는 `policy_digest_mismatch`·`policy_target_mismatch`·`observation_invalid`·`binding_mismatch`·`observation_mismatch`·`observation_reused`·`journal_event_missing`·`journal_event_mismatch`·`authority_revoked`다.
- 그때까지 만든 판정(이미 CONFIRMED였던 종류 포함)을 버리고 판정 행을 만들지 않는다.
- 회차 상태와 결과 화면에 `confirmation_integrity_failed`와 사유를 그대로 보인다. "발견 없음"·"판정 없음"으로 보이면 안 된다.
- 그 회차 후보는 모두 `level=suspected`, `reason=confirmation_integrity_failed`이고 CONFIRMED로 올라가지 않는다. 후보의 검토 상태(`manual_review_required`)는 그대로다.
- 공식 회차에서 나오면 러너가 run-record에 `status`·`reason`을 옮겨 결과 보고에 보인다(O 공식 run-record v1.1, 러너 담당).

**증거 부족.** 판정 행은 만들고(값은 오라클 결과, 대개 UNDECIDABLE) 제품 사유 코드를 `product_reason_codes`에 따로 둔다. 오라클 사유(`oracle_reason_codes`)는 그대로다. 코드는 [#692 §9.1](workbench-finding-verdict-detail-proposal.md#91-제품-확정-경로-추가-시-undecidable-세부-사유와-추가-칸)의 9개에 셋을 더했다.

| 코드 | 뜻 | 뒤 요청 |
|---|---|---|
| `product_identity_unverified`·`product_subjects_identical` | 신원 확인 실패·A·B 같음 | 생성·조회 0 |
| `product_create_approval_denied`·`product_create_approval_timeout` | 사용자 승인 모드에서 생성 거절·만료 | 그 종류 0(다른 종류는 계속, 결정 §7) |
| `product_confirmation_budget_exhausted` | 확정 단계 전용 몫(요청·시간) 소진 | 전체 0, 끝낸 판정은 보존 |
| `product_owner_marker_unobserved` | A 조회에서 표식이 안 보임 | 그 종류 B 조회 0 |
| `product_data_partial_response`·`product_data_encoding_unsupported`·`product_data_check_failed` | 검사 못 함(부분·지원 밖·실패) | - |
| `product_request_result_unknown`(추가) | 보냈는지·결과를 모름 | 다시 보내지 않음 |
| `product_request_denied`(추가) | E² 정책이 프로그램 요청을 거부 | 그 종류 0 |
| `product_create_failed`(추가) | 생성 응답이 정상이 아니거나 객체 ID를 못 꺼냄 | 그 종류 0 |

**오라클 입력.** `Run`은 사실만 모으고 값은 `oracle.Evaluate`가 정한다.
- 정책: `owner_only` 선언 → B 기대 접근 거부, 공유·위임 없음. 동결본 대조를 통과한 뒤에만 `has_registered_source`·`hint_matches_canonical`이 참이다. 유효창은 대상 승인 기간과 실행 기간이 겹치는 구간(입력)이다.
- 신원: C14 인증 성공과 신원 확인이 둘 다 섰을 때만 `verified`이고, 참조는 실행별 키 digest다.
- 데이터:
  - `same_resource`: 두 조회가 같은 생성 관찰을 읽음.
  - `owner_normal_data`·`cross_normal_data`: A·B 조회의 정상 여부.
  - `nontrivial_data_match`: B 검사 `complete`에서 `present`면 yes, `absent`면 no.
  - `reflection_excluded`: B 요청에 표식이 없음을 E²가 확인했을 때만 yes.
  - `template_excluded`: 이 회차에 새로 만든 무작위 표식을 A 객체 한 칸에만 넣었으므로 yes.
  - 확인하지 못한 사실은 모두 unknown이다.

## 8. 리셋 게이트

`ResetGate.Check`는 `{previous_writes, reset_confirmed, residue}`를 돌려준다(결정 §6.1). `CheckRunStart`는 다음 경우 모두 거절한다:
- 읽기 실패, 또는 오류와 함께 온 상태
- `residue`가 `absent`가 아님(`present`면 `previous_markers_present`, 그 밖에는 `reset_state_unavailable`)
- 지난 쓰기 뒤 리셋 확인 기록이 없음

서버는 회차 시작 때(모델 단계 전) 이것으로 회차를 거절하고, `Run`은 확정 단계 시작 때 다시 본다.

## 9. 후보 연결

후보(메서드·E² 시도 경로 패턴, `pathmask.Readable` 모양)를 시험한 종류의 조회 경로 틀과 조각마다 맞춘다. 가린 조각(`{…}`)은 가린 조각끼리만 맞는다. GET이고 정확히 한 종류와 맞을 때만 잇고, 0개면 `link_none`, 둘 이상이면 `link_ambiguous`다. 이어진 판정이 CONFIRMED일 때만 후보 `level`이 `confirmed`이고, 그 밖에는 `suspected`다. 판정에는 검증된 연결의 후보 참조만 들어간다(`candidate_refs`).

## 10. 계약 시험

`e3contract/run_test.go`·`types_test.go`. 실패 상태마다 판정·후보에 CONFIRMED가 없음을 공통으로 단언한다. 양성 대조로 정상 흐름은 CONFIRMED가 나와야 한다(아래 시험들이 공허하지 않게).

| 시험 | 고정하는 것 |
|---|---|
| `TestRunConfirmsWhenCrossSeesMarker` | 양성 대조. 판정 값이 같은 입력의 `oracle.Evaluate`와 같음, 공유 종류 요청 0, 후보 연결 |
| `TestRunDisabledSendsNothing`·`TestRunProductRejectsKGSnapshot` | 선언·시험 계정·쓰기 동의·연결이 없거나 `kg_snapshot`이면 요청 0 |
| `TestRunNotImplementedTransportDoesNotStart` | 미구현 E²(`ErrNotImplemented`)면 시작 거절, 요청 0 |
| `TestRunBindingMismatchIsIntegrityFailure` | 결속 칸 10개 각각, 앞서 CONFIRMED였던 종류도 버림 |
| `TestRunObservationMismatchIsIntegrityFailure`·`TestRunInvalidObservationIsIntegrityFailure` | 계정·역할·종류·생성 참조·검사 칸 불일치, "complete 아닌데 표식 없음" 등 모양 위반 |
| `TestRunJournalCrossCheck` | 저널 이벤트 없음·다름(표식 유무·시각·digest)·참조 재사용·권한 철회 |
| `TestRunPolicySourceChecks` | 동결본 digest·대상 판·대상 불일치는 진위 실패, 읽기 실패는 시작 거절 |
| `TestRunResetGate` | 리셋 확인 없음·표식 잔존·잔존 모름·게이트 오류는 시작 거절 |
| `TestRunIdentityFailureStopsWrites` | 신원 실패·A·B 같음이면 생성·조회 0 |
| `TestRunApprovalDenialStopsThatType` | 승인 거절·만료면 그 종류 UNDECIDABLE·이후 그 종류 요청 0 |
| `TestRunBudgetExhaustionStopsAllRequests` | 몫 소진(요청·시간)이면 이후 요청 0, 끝낸 판정 보존 |
| `TestRunCreateUnknownIsNotResent` | 결과 불명·생성 실패·정책 거부는 다시 보내지 않음 |
| `TestRunMarkerCheckNotCompleteIsUndecidable`·`TestRunCrossBlockedOrAbsentIsNotConfirmed` | 검사 상태별 사유, A 미관찰이면 B 조회 0, 차단·반사는 확정 아님 |
| `TestRunLeavesNoMarker`·`TestLeakingSinksFindsEveryForm`·`TestMarkerNeverSerializes` | 표식이 결과·로그·가짜 E² 기록에 없음, 찾기 함수가 모든 모양을 찾음, 직렬화 막힘 |

변이 60개를 모두 잡는다(가드마다 하나, 2026-11-18).

## 11. 갈래별 다음 일

- **E② 연동(이동찬)**: `Transport`·`Journal`의 실제 구현. 목적 규칙 셋, E² 안 표식 검사와 정규화기, 영수증·저널 v3, 요청별 계정 라벨, 표식 보호 값 등록. 실제 구현으로 §10 시험(특히 저널 대조·표식 누출 확인)을 다시 돌린다.
- **판정(최민준)**: 정책 선언 저장·동결과 `DeclarationReader`·`ResetGate` 실제 구현, 서버의 확정 몫 예약·모델 단계 뒤 호출, 판정 레코드 저장(정책 출처·후보 참조·제품 사유), #599 상세 API.
- **화면(이경준·윤재상)**: `status`·`reason`과 후보 `level`·`reason` 표시. `confirmation_integrity_failed`를 "발견 없음"으로 보이지 않게 한다. 정책 선언 입력 화면은 §3 칸을 쓴다.
- **러너**: 공식 회차 run-record에 확정 단계 `status`·`reason`을 싣는다.

## 12. 결정문과 다르게 읽힐 수 있는 곳

- 사용자 승인 모드의 생성 거절·만료는 결정 §7대로 **그 종류만** 멈춘다. 다른 종류는 계속 시험한다.
- 신원 확인 선언은 대상마다 하나다(결정 §4.1 표는 종류마다 칸에 두었지만 §5.1은 대상 단위로 확인한다).
- `shared` 종류는 판정 행 없이 `skipped`로 보인다(결정 §4.1의 "UNDECIDABLE"을 판정 행으로 꾸미지 않음).
- 확정 단계 시간 몫은 `Run`에 넘기는 문맥의 시한이다. 시한이 지나면 몫 소진과 같게 다룬다.
