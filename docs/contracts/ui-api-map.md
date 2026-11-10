# Workbench ↔ 진단 API 연결

> 아래는 [#353](https://github.com/ABC2LAB/ABCDEF2LAB/pull/353)의 구현 제안이다. UI 삭제·미이관 기능과 12/18 화면 결정은 담당자 동의 전이며, [단일화 검토안](workbench-consolidation-review.md)에 범위와 채택 조건을 기록했다. 최신 dev에 채택됐거나 기존 화면과 기능이 동등하다는 뜻이 아니다.

제품 UI는 `web/src/workbench/` 하나이며 `/`와 `/workbench`가 같은 컴포넌트를 표시한다. 기존 레일·여섯 화면·서랍·반응형·스타일을 유지한다. 중복 ABC2LabUI·목업 HTML·가짜 데이터·재생 엔진은 제거했다. 제품 배치와 구성도는 [Docker 설치 문서](../../deploy/product/README.md), 실제 검증 범위는 [수용 기록](../workbench-product-acceptance.md)을 따른다.

## 제품 진입점

로그인 → URL 입력 → 설치된 로컬 프로필 또는 **외부 LLM 사용하기** → 모델명·API 키 등록 → 서버 범위 확인 → 명시적 시작 순서다. `diagnosis-api.v2`만 사용하고 404/501·조회 실패·결과 불명 상태에서 일반 `POST /api/tasks`나 v1으로 전환하지 않는다. 기존 공식 CLI prepare/check/run/report는 유지한다. 요청별 허용·거절 UI가 없는 현재 Workbench는 `confirm_every_request`의 새 시작을 차단하며, 서버·CLI의 승인 계약은 유지한다.

API handler는 모델 선택이나 임의 셸 생성으로 실행 권위를 만들지 않는다. 인증된 URL v2가 설치된 private preparation service를 호출하고, 보호된 실행 자료·Planner/Worker·E②를 재사용한다. 시작 전 키 등록·초안 생성·범위 확인은 모델 completion·대상 HTTP를 호출하지 않는다.

## 화면별 연결

아래 `v2`는 `/api/diagnoses/v2`다.

| 화면 | 실제 API·데이터 | 미지원·실패 표시 |
|---|---|---|
| 로그인·계정 | `/api/auth/status`, `/api/auth/login` | 초기 설정·연결 실패·401을 구분 |
| 대시보드 | `GET v2/catalog`, 현재 진단의 receipt/status | 전체 서버 이력·없는 값을 만들지 않음 |
| 새 점검 | `POST v2/drafts`, `POST /api/product/model-credentials` | 설치 프로필·승인 모델 없으면 차단 |
| 자산 탐색 | 현재 진단의 서버 승인 scope | 별도 인벤토리·자동 크롤링 미지원 |
| 범위 확인 | `POST v2/drafts/{id}/validate`, `/start` | 대상·모델·provider·토큰·USD·외부 전송 동의 확인 |
| 점검 진행 | `GET v2/drafts/{id}/status`, receipt GET, `/stop`, task request-approvals 조회 | 허용·거절 조작 UI 미지원. 결과 불명은 조회만 허용, 실행 재전송 금지 |
| 보고서 | `/api/tasks/{id}/diagnosis-run`, `/api/report/diagnostic/remediation`, diagnostic download | 저장·마감·판정·다운로드 실패를 구분 |

초안·시작에는 idempotency key와 expected revision을 바인딩한다. 브라우저 영수증은 인증 주체에 결속되며 재진입은 서버 GET으로 확인한다. 응답 유실 뒤 재시작·모델 호출을 자동으로 다시 보내지 않는다. 보고서 bridge는 해당 주체의 실제 task만 허용한다.

## 모델 키와 인증

API 키는 비제어 password 입력에서 별도 인증 설정 API로 보낸 뒤 비운다. React store·URL·진단 초안·시작 본문·local/session storage·보고서에는 넣지 않는다. 서버의 별도 writer는 설치된 `llm_profiles.api_key`만 갱신하며 키 읽기·모델·endpoint·예산 변경 권한이 없다. 저장 자체는 자격 유효성이나 모델 호출 성공을 의미하지 않는다.

현재 OpenRouter 어댑터는 설치된 Qwen 모델과 보호 manifest에 한정된다. 외부 전송·실제 비용 상한과 단회 승인은 기존 계약을 유지한다. 키 입력으로 범위를 확대하지 않는다. 세션은 Bearer 인증을 사용하며 오래된 요청의 401이 새 로그인 세션을 지우지 않도록 한다. 공개 개발 자격증명을 사용한 자동 로그인은 제거했다.

## 후보·판정 경계

일반 HTTP 관찰은 `manual_review_required` 후보로 표시한다. task·intent·call_ref·근거 digest와 실제 전송 결과가 판단 근거다. 실행 마감·허용 결정·HTTP 응답·지식 fact의 상태는 E③ 취약점 확정이 아니다. 보고서의 고유 entry를 한 번씩 집계하며 일반 findings 수를 더하지 않는다.

판정·증거 정본은 [verdict-record](verdict-record.md)·[E③ oracle](e3-oracle.md), 지도 제안은 [KG 정책](kg-policy.md)이다. KG·세부 커버리지·E② 투영 등의 공용 타입·fail-closed 변환은 `web/src/lib/diagnosis/`에 보존했으나, 해당 기능을 Workbench의 실제 데이터 화면으로 구현한 것으로 간주하지 않는다. 서버가 제공하지 않는 기능은 미지원으로 표시한다.

## 코드 근거

- UI·상태: `web/src/workbench/Workbench.tsx`, `live/urlDiagnosis.tsx`, `views/live/Url*.tsx`.
- 공용 v2·영수증·판정·다운로드: `web/src/lib/diagnosis/`.
- 인증·키 설정·bridge: `server/product_http.go`, `diagnosis_url_management.go`, `diagnosis_management_bridge.go`.
- private preparation·실제 엔진: `cmd/first-controlled-diagnosis/`, `diagnosisprep/`, 기존 Planner/Worker·E②.

범용 ARTEX의 task/exploration/findings/intercept API가 존재하는 사실과 제품 URL 진단 경로의 수용은 다르다. ARTEX 도구 승인과 E② 실제 전송 집행도 구분한다. 일반 HTTP 후보를 지원되지 않는 구조화 E③ 판정으로 승격하지 않는다.
