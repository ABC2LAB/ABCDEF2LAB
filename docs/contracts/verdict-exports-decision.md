# 판정 내보내기 API 결정 — 측정 뒤로

**상태: 결정(2026-11-19 사용자).** 작성: 최민준. 근거 문서: [판정 페이지 API 제안](verdict-record-page-api-proposal.md)(이동찬), [분리 배치 O 공식 run-record 설계](../evaluation/split-official-run-record.md) §7(`e3_confirmation` 행), [러너 기록 v1.1](../evaluation/run-record-v1.1.md) §3.

## 1. 결정

| 번호 | 결정 | 이유 |
| --- | --- | --- |
| 1 | 판정 내보내기 API(`POST`·`GET /api/tasks/{id}/verdict-exports`)는 **지금 만들지 않는다.** 공식 측정 뒤에 다시 연다. | 공식 측정은 E③ 비활성이다(선언·시험 계정 없음). O 회차의 제품 판정은 0건이라 내보낼 것이 없다. |
| 2 | O 회차의 `e3_confirmation` 행(`status`·`reason`)은 러너가 제품을 멈춘 뒤 볼륨에서 `runs/<job>/e3-confirmation.json`을 복사해 만든다. 원본 파일 SHA를 서명 근거에 남긴다(제품 파일 복사 `product-files.json`과 같은 방식). | 새 API를 열지 않고, 서명 근거가 원본 파일 그대로 남는다. 파일은 제품 회차마다 확정 단계가 0600으로 한 번 쓴다. |
| 3 | 공식 회차의 기대값은 `status=disabled`, `reason=declaration_missing`이다(평가 프로토콜 §5). 연결이 빠진 이미지면 `not_wired`가 나와 그 회차는 무효다. | 비활성 사유를 다른 사유와 구분해야 한다(E③ 계약 §7). |

## 2. 측정 뒤 다시 열 때 정할 것(지금 정하지 않음)

- **제품 판정을 채점에 넣을지.** 제안 문서는 `run_id`가 평가 회차(`eval_run_id`)와 같아야 하고 비평가 발급은 뺀다. 제품 판정(`source_kind=product`)에는 평가 회차가 없다. 넣는다면 실행 결속(`job_ref`·`run_ref`)으로 고르는 규칙을 평가 프로토콜에 먼저 고정한다. VAmPI 실제 검증 뒤에 정한다.
- **동결 저장.** 저장 없이 `export_ref`를 (작업, 실행 결속, 판정 ID·정본 해시 집합)의 digest로 두고, 페이지마다 다시 계산해 다르면 409 `export_snapshot_unavailable`로 하는 안이 있다. DB 표·권한을 늘리지 않는다.
- **설치 위치.** 판정 상세 조회와 같은 제품 관리 중계(작업 결속 읽기). 원본 엔진(B)에는 없으니 러너는 B를 지금처럼 미수집으로 둔다.
- 오류 코드는 러너 수집기가 읽는 값(400·401·403·404·409·422·503)을 그대로 쓴다.
