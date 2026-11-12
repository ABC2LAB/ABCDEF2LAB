# 통제 때문에 원본보다 깎이는 항목 — 우선 개선 목록

2026-11-11 조사(최민준). dev `54023077` 코드를 원본 ARTEX(`fdfb7a17`)와 비교해 읽은 결과다. **실행해서 잰 것이 아니고**, 얼마나 깎이는지는 측정 전이라 적지 않는다. 숫자는 이미 저장소 문서에 잰 근거가 있는 것만 썼다. 위치(파일:줄)는 그 커밋 기준이다.

[개발 계획의 11/11 개선 순서](development-plan.md#1111-개선-순서--원본-능력-보존-우선)에 따라 이 목록을 우선 개선 목록으로 관리한다. 담당은 2026-11-12에 정했다. 실행 허용 판단(범위·메서드·위험 요청·탐색 경계)은 이동찬에게 두고, 허용 판단이 아닌 배관·운영·화면 쪽을 최민준·이경준이 나눠 맡는다. 한 항목에 허용 판단과 화면이 섞이면(C21·C22) 허용 판단 부분은 이동찬에게 두고, 집행 경로를 바꾸는 항목(C15·C16)은 이동찬 페어 리뷰를 필수로 한다(CLAUDE.md "E²(실행 허용)는 시나리오/IDOR 만드는 사람과 분리"). `미정`은 아직 담당을 정하지 않은 항목이다. 각 PR은 걷어낸 항목의 ID, 유지한 통제, 원본 대비 남은 차이를 적는다. 영향은 **오류**(정상 동작을 멈추거나 막음)와 **저하**(발견·품질·처리량을 줄임)로 나눈다.

## 1. 실패 한 번이 실행·설치 전체를 멈추는 것

| ID | 내용 | 영향 | 위치 | 순서·담당 |
|---|---|---|---|---|
| C01 | E② 누적 정지. 제품 기본값은 거부 2(워크플로 16)·결과 불명 1·일시 실패 1이면 실행 전체 정지. 시도 상한(8/200)은 보내지 않은 거부까지 센다 | 오류 | `e2proxy/execution.go:777-794`, `cmd/first-controlled-diagnosis/product_install_linux.go:235` | P2·이동찬 |
| C02 | 대상 연결 실패·TLS 실패가 결과 불명이 된다(연결 시도 표시를 연결 전에 세움) | 오류 | `e2proxy/execution.go:1103-1124` | P2·이동찬 |
| C03 | Worker가 실행 전체의 unknown 건수를 보고 다른 Worker의 unknown에도 멈춘다 | 오류 | `agent/e2_http.go:262-271` | P2·이동찬 |
| C04 | 결과 불명 작업 하나가 설치 전체의 새 시작을 막고 해제 API가 없다 | 오류 | `diagnosisprep/service_linux.go:322-325` | P3·이경준 |
| C05 | 모델 호출 오류 한 번이 실행 차단·결과 불명으로 이어진다(실행 중 중지·대상 철회도 같은 길) | 오류 | `server/first_diagnosis_model_budget.go:258-271` | P3·최민준 |
| C06 | OpenRouter가 200이 아닌 응답(429·5xx 포함) 한 번에 정책을 영구 차단하고 재시도하지 않는다 | 오류 | `llmpolicy/openrouter_transport.go:207-254` | P3·최민준 |
| C07 | 유료 API 승인이 1회용이라 같은 승인으로 두 번째 실행이 거부된다 | 오류 | `llmpolicy/openrouter_file_linux.go:113-133` | P3·최민준 |
| C08 | intent 하나라도 턴 소진·시간 초과·Bash 미호출·일시 실패면 실행 실패. Planner 라운드·호출 상한 도달도 실패로 끝난다 | 오류 | `server/first_diagnosis_supervisor.go:599-610`, `server/e2_http_runtime.go:301` | P3·최민준 |
| C09 | 마지막 호출 하나가 intent 결과를 정하고 대조가 깨지면 실행 실패 | 오류 | `agent/e2_http.go:608-623` | P3·최민준 |
| C10 | 진단 로그 기록 실패·상태 조회 1회 실패가 감사 실패처럼 실행을 취소한다 | 오류 | `cmd/first-controlled-diagnosis/url_preparation_runtime.go:254-257` | P3·최민준 |
| C11 | 준비 단계 대체 경로 없음. 목표 분해 실패 시 중단(원본은 원래 목표로 계속), 2분 안에 intent 0개면 실패인데 Planner 입력은 "0개도 괜찮다"고 한다 | 오류 | `server/first_diagnosis_startup.go:125`, `server/first_diagnosis_supervisor.go:372-384`, `agent/planner.go:486-490` | P3·최민준 |
| C12 | 저장된 intercept 정규식 하나가 깨지면 모든 도구 호출이 막힌다(원본은 그 규칙만 건너뜀) | 오류 | `intercept/intercept.go:197-235` | P3·최민준 |
| C13 | 워크플로 투영 실패 한 번으로 그 뒤 요청이 전부 거부된다 | 오류 | `e2proxy/execution.go:899-903` | P2·이동찬 |

## 2. 할 수 있는 일을 막는 것

| ID | 내용 | 영향 | 위치 | 순서·담당 |
|---|---|---|---|---|
| C14 | 로그인 불가. 제품은 `Authorization`·`Cookie` 요청을 거부하고, `Set-Cookie`·`WWW-Authenticate` 응답을 통째로 숨겨 unknown으로 센다(C01·C03 정지로 이어짐). 대상 자격 설정도 거부. [결정안](account-login-decision.md) | 오류 | `e2proxy/execution.go:924`, `e2proxy/headers.go:145-166`, `cmd/first-controlled-diagnosis/url_preparation_config.go:367` | P2·이동찬 |
| C15 | `Accept-Encoding`이 identity가 아니면 거부(측정: gzip 요청 502, [근거](evaluation/e2-product-preflight.md)) | 저하 | `e2proxy/execution.go:924,1316-1330` | P2·최민준(이동찬 페어 리뷰 필수) |
| C16 | 64KiB 상한이 자르지 않고 막는다. 넘는 응답은 unknown, 넘는 요청은 미지원 | 오류 | `e2proxy/execution.go:963-968,1138-1144` | P2·최민준(이동찬 페어 리뷰 필수) |
| C17 | 인코딩·점 경로(`%`, `..`, `//`) 거부, 거부 수에도 들어감 | 저하 | `e2proxy/execution.go:186-188,817` | P2·이동찬 |
| C18 | 진단은 크롤에서 본 정확한 메서드+경로만. `/x/1`을 봐도 `/x/2`는 못 보냄. 자동 모드는 GET·HEAD·본문 없음, 32개 상한, JS 미실행, 새 경로 추가 안 함 | 오류 | `diagnosisflow/controller.go:379-417`, `diagnosisflow/contract.go:34`, `diagnosisflow/input.go:36` | P2·이동찬 |
| C19 | 같은 명령 재전송 금지, intent당 대안 1회 | 오류 | `agent/e2_http.go:695-790` | P1(루프 복원)·최민준 |
| C20 | 동시 실행 1개를 넘으면 대기 대신 거부로 셈. Bash 슬롯도 실행 전체에 1개 | 오류 | `e2proxy/execution.go:942-944`, `e2runner/runner_linux.go:110` | P2·이동찬 |
| C21 | 요청별 승인이 10초(워크플로 60초) 안에 안 오면 거부로 셈, 그동안 Bash 슬롯 점유. 자동 모드는 승인된 쓰기 메서드를 GET·HEAD로 깎음(11/11 사전 1회 승인과 충돌) | 저하 | `e2proxy/execution.go:977-1005`, `cmd/first-controlled-diagnosis/product_runtime.go:248-260` | P2·이동찬(메서드 범위)·이경준(승인 화면·흐름) |
| C22 | 대상은 https만, query 없는 URL만 | 오류 | `diagnosisprep/validation.go:77` | P2·이동찬(대상 스킴·TLS 조건)·이경준(등록 화면) |
| C23 | 위험 경로 단어 필터가 GET도 막음(`reset`·`payment` 등). "무엇을" 통제 범위지만 범위가 넓음 | 저하 | `diagnosisflow/controller.go:63-72` | P2·이동찬 |

## 3. 원본보다 덜 하게 만드는 것

| ID | 내용 | 영향 | 위치 | 순서·담당 |
|---|---|---|---|---|
| C24 | Worker 도구가 Bash·node_detail·record_fact·후보 제출 4개뿐. WebFetch·WebSearch·파일·트래픽 조회·skills·MCP·자산·발견·추적 조회 없음(원본 1회차 대상 요청 361건 중 WebFetch 15건, [근거](e2-proxy-control-decision-draft.md)) | 저하 | `agent/e2_http.go:385-399,499-508` | P1(루프 복원)·최민준 |
| C25 | Worker 프롬프트가 6줄 지침으로 바뀜. 그래프 개요·제약·대상 자산·intent ID 줄이 빠지고 목표가 "읽기 전용, 후보만"으로 고정 | 저하 | `agent/e2_http.go:509-543`, `cmd/first-controlled-diagnosis/url_preparation_runtime.go:339-342` | P1(루프 복원)·최민준 |
| C26 | Planner에서 prove_goal·goal_met·kill/steer·report_finding·scope·skills·MCP·WebFetch·WebSearch·Todo 제거, Plan당 16턴·2분, 마무리 턴 없음 | 저하 | `agent/e2_http.go:367-383,792-840`, `agent/planner.go:360-390` | P1(루프 복원)·최민준 |
| C27 | Planner가 Worker가 다 끝날 때까지 못 돈다(원본은 동시) | 저하 | `server/e2_http_runtime.go:304-306` | P1(루프 복원)·최민준 |
| C28 | 모델 호출이 한 번에 하나이고 호출마다 컨텍스트 전체를 예약(기록상 실제 90/90에 예약 1,179,648/18,432, [근거](url-diagnosis-preparation.md)) | 저하 | `server/first_diagnosis_model_budget.go:157-236` | P3·최민준 |
| C29 | 호출마다 작업 디렉터리를 새로 만들고 지움, `/work` 32MiB·`/tmp` 16MiB 실행 불가, 출력 32KiB 넘으면 버림(원본은 파일로 넘겨 다시 읽음). [결정안](workspace-decision.md) | 저하 | `e2runner/runner_linux.go:256-267,387-390`, `e2sandbox/helper_linux.go:411,482` | P3·최민준 |
| C30 | 이미지 데이터 미탑재(워드리스트·Playwright·skills). 도구 동등성 79개 중 57개 일치([근거](evaluation/e2-tool-equivalence.md)). [결정안](sandbox-data-decision.md) | 저하 | `e2sandbox/helper_linux.go` | P3·최민준 |
| C31 | 거부·미지원·불명·대상 오류가 같은 502 본문이라 모델이 사유를 모름 | 저하 | `e2proxy/http.go:334-378` | P2·최민준 |
| C32 | 제품 기본 상한이 작음(요청 시간 5초, 호출 10초, Worker 턴 8, 도구 호출 8, Planner 라운드 6, intent 실행 1, Worker 수는 설치가 `engine.workers`=2를 쓰지만 엔진이 읽는 키가 `workers`라 실제로는 원본과 같은 3, 실행 180초). 장치는 유지하고 값을 설정으로([결정안](install-limits-decision.md)) | 저하 | `cmd/first-controlled-diagnosis/product_install_linux.go:234-247` | P3·최민준 |
| C33 | 제품 경로에서 일시정지·재개·채팅·조종·설정·활동 조회가 403 | 저하 | `server/first_diagnosis_startup.go:66-84` | P3·이경준 |
| C34 | 설치 수명 상한(승인 168시간·실행 1000회, 작업 1024, 대상 32개 삭제 불가, 이미지 갱신 시 지문 거부) | 오류 | `cmd/first-controlled-diagnosis/product_install_linux.go:101,184`, `diagnosisprep/service_linux.go:16,215,326-337` | P3·이경준 |
| C35 | 대상 승인 조건이 빡빡함(DNS 주소 집합·SPKI 동일, 24시간, 시작마다 TXT 재확인, IP 8개 초과 거부). CDN·인증서 교체에 약함 | 저하 | `cmd/first-controlled-diagnosis/product_targets.go:262-275,363-366,449-455` | P3·이경준 |

## 진행

| ID | 상태 | 바꾼 것 | 유지한 통제 | 원본 대비 남은 차이 |
|---|---|---|---|---|
| C01 | 구현·단위시험 | 거부·미지원·결과 불명·송신 실패 상한(`MaxDenials`·`MaxUnknown`·`MaxTransient`)을 call 단위로. 닿으면 그 call의 새 요청만 기록 없이 거절. 시도 상한(`MaxAttempts`)은 예산을 예약한 시도만 셈. 시도 상한 도달은 실행 정지 대신 그 뒤 송신 거부 | 감사 실패·집행 장애·철회·만료·운영자 중지는 실행 전체 정지. 회차 예산(`MaxRequests`) 원장, 결과 불명 쓰기 자동 재전송 없음 | Supervisor 마감은 결과 불명 intent를 `partial`로, 연결 실패 call을 그 intent의 결과로 셈(C08). 명령 시간 초과는 신뢰 결과에 종료 원인이 없어 아직 마감 실패([계약](contracts/run-outcome-states.md) 열린 항목 2) |
| C02 | 구현·단위시험 | 연결(TLS 포함)을 맺기 전 TCP·TLS·SPKI 실패는 `transport_failed`, 연결 뒤 실패만 `unknown`. 연결 시도 자체는 `forward_attempted`로 남김. 새 계수 `Counts.Transient`, runner 분류 `transient_failure` | 연결 뒤 결과 불명은 그대로 불명, TLS 검증·SPKI 고정 | — |
| C03 | 구현·단위시험 | `ExecutionView.IntentCounts` 추가. Worker 분류·정지·intent 마감이 실행 전체 대신 자기 intent의 불명 수를 봄 | 감사·집행 장애는 실행 상태·`AuditComplete`로 전체 반영. 자기 intent의 불명은 그 intent를 멈춤 | — (마감 판정도 자기 intent의 불명 수만 봄, C08) |
| C08 | 구현·통합시험 | 결과 불명 intent를 실패 대신 `completed` + `partial`로 셈. 결과 불명 송신이 있는 호출의 intent는 결과 불명이어야 하고 결과 불명 intent 수는 E② 근거와 같아야 함. 연결 실패(`transient_failure` + 송신 실패 수) 호출은 그 intent의 결과 | 감사 실패·철회·만료·비최종 호출은 마감 실패. 결과 불명 intent는 다시 보내지 않음 | 명령 시간 초과·프록시 장애는 신뢰 결과에서 구분되지 않아 마감 실패 |
| C13 | 구현·단위시험 | 요청 관찰(After) 저장의 일시 실패는 판을 되돌리고 다음 저장에서 다시 씀. 연속 3회 실패·판 충돌이면 이후 허용 차단 | 선택·모드·단계 같은 허용 상태 저장 실패는 즉시 이후 허용 차단. E② 감사 실패는 실행 정지 | — |

## 4. 유지 범위 안에서 따로 볼 것

- 위험 query 차단은 "무엇을" 통제로 유지하기로 했지만 E②에 구현돼 있지 않다(`e2proxy`의 `query`·`RawQuery`는 규칙 설정 파싱에만 나옴).
- 최대 연결 4개를 넘는 연결은 감사 기록 없이 닫힌다(`e2proxy/http.go:156-161`).
- 그대로 두는 것: origin·IP 고정, TLS 검증, 리다이렉트 재검사, DNS 없는 격리 망, 예산·만료·감사 실패·철회 시 정지, 우리 자격 값 가림.
