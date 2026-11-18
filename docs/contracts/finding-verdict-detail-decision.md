# 판정 상세 조회 결정안 (finding-verdict-detail, OD01~OD11)

**상태: 결정안 — 리뷰 대기.** 작성: 최민준(2026-11-18). 리뷰: 이동찬(서버·송신 근거, OD03·OD10), 이경준(화면, OD02·OD04·OD11).

[판정 상세 계약 초안](workbench-finding-verdict-detail-proposal.md)(#599, 이동찬)의 열린 결정 OD01~OD11을 정하고, 구현을 조각으로 나눈다. 초안의 응답 형식·오류 코드·검증 순서(§6.1)는 그대로 따른다. 여기서 정하지 않은 것은 초안의 제안을 그대로 채택한다.

v4.2 계획 4번 4a의 두 번째 PR이다. 첫 PR은 판정 표의 출처 종류(#714: `source_kind` eval/product, 제품 칸, 기존 읽기의 eval 한정)다. 이 API는 #714 위에서 구현한다.

## 1. 결정

| OD | 결정 | 이유 |
| --- | --- | --- |
| OD01 중심 ID·경로 | 초안 제안대로 한 경로 `GET /api/tasks/{task_id}/finding-verdict-detail`와 서로 배타인 selector 셋(`verdict_id`·`finding_id`·`candidate_id`)이다. verdict 전용안은 쓰지 않는다. | finding·후보에서 "연결된 판정 없음"을 답할 대상이 있어야 한다. 경로가 하나면 인증·결속 검사도 한곳이다. |
| OD02 link 없는 판정 접근 | 보고서의 `verdict-<issuance_id>` 행 ID는 그대로 둔다. 상세로 갈 때 쓰는 검증된 `verdict_id`는 보고서 행에 별도 칸으로 싣는다(보고서 칸 추가는 화면 연결 PR). issuance selector는 열지 않는다. | 이경준 동의안. 행 ID를 화면이 분해하지 않는다. |
| OD03 transmission 근거 | 1조각은 평가 등록 GET만 manifest의 owner/cross 영수증으로 투영한다(`source=registered_get`, 대표 `scope_role=cross_read`). 독립 감사 checkpoint 대조 reader가 붙기 전에는 `audit_status=unavailable`이고, 영수증만으로 `sent`를 정하되 감사 확인 상태와 분리한다. 제품 판정은 E² 저널 v3(이동찬 Transport·Journal) 전에는 `state=unknown`, `source=e2proxy`, `coverage=unavailable`, 사유 `journal_v3_unavailable`이다. 시도 혼합 대표값은 초안대로 unknown이다. | 제품 근거 원천(v3)이 아직 없다. 없는 근거로 sent·none을 만들지 않는다. |
| OD04 일반 finding과 E③ 표시 | 초안대로 envelope로 구분한다. `no_link`일 때 가짜 FindingRecord를 만들지 않는다. 초기 판정 심각도가 없으면 null(화면 '미평가'). | 일반 triage confirmed와 E③ 원값을 섞지 않는다. |
| OD05 후보↔판정 연결 | 제품 판정의 `candidate_refs`(확정 단계가 저장 때 검증한 연결, #714 칸)만 쓴다. `candidate_id` selector는 그 후보를 참조하는 제품 판정이 정확히 하나면 그 판정을, 없으면 `no_link`를, 둘 이상이면 422를 낸다. 유사도로 찾지 않는다. 후보 상태는 `manual_review_required` 그대로다. | #596 §9. 연결은 확정 단계가 저장 때 한 번 검증한다. |
| OD06 저장 판정 검증·출처 가용성 | 초안 §6.1 순서를 따른다. 평가 판정은 기존 검증(e3record Source·manifest·세 해시)이다. 제품 판정은 `record_version=2` 정본(`e3-product-verdict/v1`)의 정본 해시·발급 결속(`binding_canonical`↔발급 칸)·정책 출처 칸(참조·digest·대상 승인 판)을 대조한다. 당시 정책 동결본은 실행 결속의 `e3-declaration.json`(#708)이다. 그 바이트의 SHA-256이 발급의 `policy_source_digest`와 다르면 `record_integrity_failed`, 파일을 읽을 수 없으면 `policy_source_unavailable`(원값 표시 유지)이다. | 초안 OD06 기준을 제품 출처에 같은 규칙으로 적용한다. |
| OD07 비평가 제품 vs 평가 run | 화면 단건 조회는 평가 동결 export·러너 권한을 요구하지 않는다. 응답 최상위에 `source_kind`를 싣고(OD08 공개 칸), 두 출처는 각자의 검증 규칙으로만 읽는다. | 평가 판정 수집은 판정 내보내기 API(따로, 최민준)의 일이다. |
| OD08 상한·공개 칸 | ID·revision은 정규 십진 문자열이다. 배열 상한: `reason_codes` 32, `canary_fields` 32, `steps` 8, `candidate_refs` 64. 응답 전체 256 KiB. 넘으면 잘라 보내지 않고 503이 아니라 422 `verdict_detail_integrity_failed`다(저장 상한 64 KiB×3을 넘을 수 없는 값이라 저장 이상으로 본다). 공개 칸은 초안 §8.2 예시 칸에 최상위 `source_kind` 하나를 더한 것이다(allowlist). `source_kind`는 `ids` 옆 최상위 칸이고 값은 `eval`·`product`이며, 판정 행이 없으면(`availability`가 `no_link`·`not_executed`) `null`이다. 저장 판정 진위 실패(`record=null`)여도 발급 행의 출처 종류는 싣는다. 초안 §8.2 예시들은 이 칸이 빠진 형식이므로 구현 PR의 시험 예시는 이 칸을 넣는다. | 잘림으로 감추지 않는다. 출처는 표시·검증 규칙을 가르는 칸이라 응답에 늘 있어야 한다. |
| OD09 반영 순서 | 1조각: verdict·finding selector, 평가·제품 저장 판정 읽기·검증, view, 평가 등록 GET transmission. 2조각: candidate selector(4b에서 제품 판정 저장이 연결된 뒤). 3조각: 제품 transmission(E² 저널 v3 뒤). 화면 연결은 이경준. | 근거 원천이 있는 것부터 연다. |
| OD10 읽기 권한·설치 위치 | 제품 관리 중계의 작업 결속 읽기(진행 중 후보 조회 #694와 같은 자리, `AuthorizeTask`·Principal/Job/Task 소유 검사)에만 설치한다. 저장 보고서 모드의 허용 목록은 늘리지 않는다. 조회는 실행 복구·재검증을 하지 않는다. | 초안 §7. |
| OD11 공개 칸 적격성 | `canary_fields`는 필드 이름만(값 없음). `owner_ref`·`evaluated_ref`는 평가 KG 라벨, 제품은 시험 계정 라벨(C14 라벨, 자격·세션 없음). `resource_ref`는 평가는 논리 ref, 제품은 `<종류>:<ID digest>`. 제목은 프로그램 고정 문구(평가 "수평 IDOR 구조화 판정", 제품 "제품 확정 단계 판정")이다. 정본 바이트는 통째로 내보내지 않는다. | 계정 원문·표식·경로를 내보내지 않는다. |

## 2. 1조각 범위와 시험

- 구현: `server` 읽기 핸들러 + `db` 읽기(Repeatable Read 읽기 전용 snapshot, LEFT JOIN), 제품 중계 배선(OD10).
- 시험(초안 §10 FVD에서 1조각 해당분):
  - 평가 판정 4값 각각 verdict selector로 읽기, 정본·해시 변조마다 `record_integrity_failed`(record=null)
  - 제품 판정: 정책 동결본 일치는 verified, 동결본 없음은 `policy_source_unavailable`(원값 유지), digest 불일치·정본 변조는 `record_integrity_failed`
  - finding selector: link 없음은 `no_link`·`evaluation_status=unknown`, 증거 버전이 바뀌었으면 `historical`, 최초 저장 confirmed에 link가 없으면 422
  - 다른 task의 ID와 없는 ID는 같은 404, selector 0·2개·모르는 query는 400
  - 조회 전후 판정·manifest·발급·link·finding 행 수와 대상 HTTP·모델 호출이 0
  - 응답에 비밀번호·쿠키·토큰·표식·계정 원문·파일 경로가 없음(건수 검사)
- 리뷰: 이동찬(필수), 이경준(응답 칸·화면).
