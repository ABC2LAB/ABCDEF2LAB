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
| C05 | 구현·단위시험 (#419) | 실패한 모델 호출은 예약량을 쓴 것으로 정산하고 계속. 사용량을 모르는 호출은 `unknown_usage_calls`로 따로 셈 | 모델 예산 상한, 로컬 provider의 정책 거부는 일반 오류로 넘기지 않고 멈춤 | — |
| C06 | 구현·단위시험 (#422) | OpenRouter 설정 오류는 즉시 잠그고, 429·5xx·연결·본문 끊김은 연속 상한(`max_consecutive_transient_errors` 1~16)에서만 잠금 | 영구 오류(정책·인증·설정)는 첫 회에 잠금 | — |
| C07 | 구현·단위시험 (#424) | 유료 승인을 1회용 대신 승인별 누적 예약 기록(`.ledger`, 배타 잠금·fsync)으로 | 승인 총량 상한, 옛 1회용 표시는 다 쓴 것으로 봄 | — |
| C09 | 구현·통합시험 (#428·#492) | intent 결과를 마지막 호출 하나가 아니라 모든 호출 분류로 정함. 신뢰 결과 대조가 깨지면 그 intent만 결과 불명 | 대조 실패를 성공으로 바꾸지 않음, 같은 호출 참조 중복·비최종 호출은 거부 | — |
| C10 | 구현·단위시험 (#418·#426·#471) | 진단 로그·감시자·admission 기록 실패가 실행을 취소하지 않음(마감 때 경고 사유·누락 건수). 상태 조회 실패를 설치 설정 횟수만큼 다시 물음(`status_read_retries`, 기본 0) | E② 감사 실패는 그대로 실행 정지 | — |
| C11 | 구현·단위시험 (#417·#449) | 목표 분해가 실패하거나 0개여도 원래 목표로 준비 계속. intent 없이 끝난 실행은 실패 대신 `no_intents` | 계획 전 task 재확인 | — |
| C12 | 구현·단위시험 (#421) | 깨진 허용 규칙만 건너뜀 | 깨진 막기·확인 규칙은 전체 차단(`rule_invalid`·규칙 ID) | — |
| C14 | 2단계 일부(계정 등록·분류, 결정안 #500) | 대상별 계정 라벨 등록, 시험 계정 표시(대상 승인자·진술 동의·그 대상 판에만 묶임)·해제, 판이 바뀌면 real(`target_reapproved`), 해제는 `manual_unmark`, 보호 저널이 감사 기록, 관리 API `/api/product/targets/{id}/accounts`. 대상 로그인 방식 설정과 계정 자격 보호 파일 저장(저널엔 해시만, `…/login`·`…/credential`). E② 인증 준비 전용 규칙·`AuthPrep`, 실행 정책 `auth` 칸으로 실행 시작 때 계정별 로그인, 실행 결속(test·자격 있는 계정만, 실행 중 해제·만료·자격 교체면 새 송신 정지) | 기본 real, 모델 입력·E② 헤더 모드는 아직 그대로(시험 계정 세션도 모델에 안 감) | 실행 결과 `account_auth` 칸·정지 사유(3단계 나머지), `test_account_passthrough`·세션 줄(4단계), 화면(이경준) |
| C15 | 구현·단위시험 (#399) | `Accept-Encoding`을 거부 대신 `identity`로 바꿔 보냄 | 압축 응답은 표현 보류(C16 1단계부터 `withheld`) | — |
| C19 | 구현·통합시험 (#491) | 정책 거부 뒤 고친 재시도 허용. 결과 불명·실행기 실패만 재전송 금지(`intent_outcome_uncertain`) | 결과 불명 쓰기 자동 재전송 없음 | — |
| C24 | 구현·단위시험 (#415·#427·#461·#481) | Worker에 원본 그래프 조회 도구, E② 경유 WebFetch, 보관소 파일 도구(Read·Write·Edit·LS), 승인 대상 안의 `insert_assets` | 대상 요청은 E② 경유, 파일 도구는 intent 보관소 안만 | WebSearch·skills·MCP는 진단 Worker에 없음 |
| C25 | 구현·단위시험 (#416·#425) | Worker 프롬프트를 원본 지침·문맥(제약·intent 번호·범위 안 자산·개요)으로, 목표 문장을 승인 메서드로 | 후보는 확정이 아님(규칙 5) | `report_finding`은 `submit_diagnostic_candidate`로 남음 |
| C26 | 구현·통합시험 (#466·#468·#469·#470) | Planner에 `prove_goal`·`goal_met`(목표 달성 마감), `add_task_scope`(E② 범위 불변), Plan당 상한 설정, 원본 마무리 턴·Todo | 범위 도구는 E② 승인 범위를 바꾸지 않음 | `report_finding`(후보)·skills·MCP·WebSearch |
| C27 | 구현·통합시험 (#494) | Planner가 Worker 실행 중에도 돌고 `kill_work`·`steer_work`로 진행 중 작업을 멈추거나 고침 | kill로 끊긴 E② 호출은 그 intent만 결과 불명 | — |
| C28 | 구현·단위시험 (#420·#430·#431·#458·#464) | 입력 예약을 컨텍스트 전체 대신 요청 크기 상한(요청 JSON 바이트 + 4096)으로, 호출별 예약·정산, OpenRouter 호출 칸·비용 대조, 동시 모델 호출 수 설정(`model_concurrency`, 기본 1) | 모델 예산 총량·승인 동시성 상한 | 로컬 모델은 동시 1 |
| C29 | 구현·단위시험 (#443·#444·#446·#460·#461) | intent별 작업 공간 보관소(호출마다 넣고 빼기), 긴 출력을 `.outputs/<call_ref>.txt`로 남김, 보관소 파일 연산·도구 | 샌드박스 격리, 보관 상한 | — |
| C30 | 일부(탐색 직접 Bash) | 탐색 에이전트에 원본처럼 Bash 도구를 줌(설치 정책 `exploration.direct_bash`, 비우면 켬·`false`면 끔, 같은 프로세스 실제 경계 시험·제품 compose 종단 확인). 명령은 진단 Bash와 같은 격리 실행 안에서 돌고 relay가 경계 직접 입구 한 주소로만 잇는 typed adapter(`exploresandbox.DirectBash`)만 받음. 실제 샌드박스 단위시험(직접 입구 외 주소·다른 peer 불가, 시간 상한·정리, 정리 실패 시 닫힘) | 탐색 대상 요청의 범위·쓰기·예산·기록은 경계, 진단은 E② 그대로. 일반 Skill(API Recon 스크립트)은 계속 거부 | `api-recon` 스크립트 마운트 |
| C31 | 구현·단위시험 (#397) | E② 실패 응답에 결과 종류·사유(`X-ABC2LAB-E2-Outcome`·`Reason`)와 사유별 모델 안내 | 대상이 흉내 낼 수 있어 판단은 trusted 관찰로만 | — |
| C32 | 구현·단위시험 (#445·#455·#464·#469·#471) | 설치 입력 `runtime_limits`로 요청·호출 시간, 호출·시도 수, Worker 턴·시간, Planner 라운드, intent 실행, 회차 시간, 동시 모델 호출, Plan당 상한, 상태 조회 재시도를 고름 | 코드 검사 상한, 정책 지문·설치 기록 결속 | 기본값은 그대로(설치 조직이 올림) |
| C01 | 구현·단위시험 | 거부·미지원·결과 불명·송신 실패 상한(`MaxDenials`·`MaxUnknown`·`MaxTransient`)을 call 단위로. 닿으면 그 call의 새 요청만 기록 없이 거절. 시도 상한(`MaxAttempts`)은 예산을 예약한 시도만 셈. 시도 상한 도달은 실행 정지 대신 그 뒤 송신 거부 | 감사 실패·집행 장애·철회·만료·운영자 중지는 실행 전체 정지. 회차 예산(`MaxRequests`) 원장, 결과 불명 쓰기 자동 재전송 없음 | Supervisor 마감은 결과 불명 intent를 `partial`로, 연결 실패 call을 그 intent의 결과로 셈(C08). 명령 시간 초과는 신뢰 결과에 종료 원인이 없어 아직 마감 실패([계약](contracts/run-outcome-states.md) 열린 항목 2) |
| C02 | 구현·단위시험 | 연결(TLS 포함)을 맺기 전 TCP·TLS·SPKI 실패는 `transport_failed`, 연결 뒤 실패만 `unknown`. 연결 시도 자체는 `forward_attempted`로 남김. 새 계수 `Counts.Transient`, runner 분류 `transient_failure` | 연결 뒤 결과 불명은 그대로 불명, TLS 검증·SPKI 고정 | — |
| C03 | 구현·단위시험 | `ExecutionView.IntentCounts` 추가. Worker 분류·정지·intent 마감이 실행 전체 대신 자기 intent의 불명 수를 봄 | 감사·집행 장애는 실행 상태·`AuditComplete`로 전체 반영. 자기 intent의 불명은 그 intent를 멈춤 | — (마감 판정도 자기 intent의 불명 수만 봄, C08) |
| C08 | 구현·통합시험 | 결과 불명 intent를 실패 대신 `completed` + `partial`로 셈. 결과 불명 송신이 있는 호출의 intent는 결과 불명이어야 하고 결과 불명 intent 수는 E② 근거와 같아야 함. 연결 실패(`transient_failure` + 송신 실패 수) 호출은 그 intent의 결과 | 감사 실패·철회·만료·비최종 호출은 마감 실패. 결과 불명 intent는 다시 보내지 않음 | 명령 시간 초과·프록시 장애는 신뢰 결과에서 구분되지 않아 마감 실패 |
| C16 | 일부(결정안 3의 기본값) | 제품 일반 HTTP 모드 본문 상한 기본값 64 KiB → 16 MiB(공식 평가 템플릿·코드 상한과 같음). 제품 기본값과 템플릿 값 대조 시험 | 넘는 응답은 여전히 결과 불명, 넘는 요청은 미지원(자르기·`withheld`·형식 판 v2는 결정안 1·2·4) | 요청·응답 상한 분리, 설치 설정 칸, 기존 설치는 새 설치 전까지 64 KiB |
| C13 | 구현·단위시험 | 요청 관찰(After) 저장의 일시 실패는 판을 되돌리고 다음 저장에서 다시 씀. 연속 3회 실패·판 충돌이면 이후 허용 차단 | 선택·모드·단계 같은 허용 상태 저장 실패는 즉시 이후 허용 차단. E② 감사 실패는 실행 정지 | — |
| C17 | 구현·단위시험 | E② 경로 범위를 풀린 경로로 비교하고 정상 percent 인코딩(공백·비ASCII 등)을 보냄. 대상에는 원래 표기 그대로 감 | 인코딩한 slash·backslash·점·`%`(두 번 풀기)·NUL, 점 조각, `//`는 계속 거부. 경로 매개변수(`;`)를 뗀 이름이 점 조각(`..;`, `..%3b`)이어도 거부(Tomcat·Spring 계열이 `..`로 풂, #487 리뷰) | 워크플로(diagnosisflow) 모드는 발견 경로와 정확히 같아야 해서 인코딩 경로를 아직 받지 않음(C18과 같이) |
| C23 | 구현·단위시험 | 위험 단어를 부분 문자열 대신 경로 조각·query의 단어 단위(camelCase·글자/숫자 경계 포함)로 맞춤. 거래 단어(checkout·payment·purchase·transfer)는 쓰기 메서드에서만 막음. query 값은 동작을 고르는 이름(`action`·`cmd`·`op`·`do` 등)일 때만 봄(`?q=delete`·`?sort=remove_date` 같은 검색·정렬 값은 보냄). 앞뒤 일치(deleteall·deleted)는 하지 않음 | 파괴 동작 단어(delete·remove·reset·logout·signout·logoff·destroy·truncate·shutdown)는 GET 포함 모든 메서드에서 차단. 나뉜 합성어(logOut·log-out·shutDown·checkOut)는 이웃 두 단어를 붙여서도 봄. query 이름의 파괴 동작, 동작 이름의 파괴 값 차단. `;`를 구분자로 쓰는 대상도 같은 이름으로 봄(#487 리뷰). 제외 경로 그대로 | 단어 목록은 아직 코드 고정값(대상 정책 설정은 이후) |
| C20 | 구현·단위시험 (E② 쪽) | 동시 송신 상한을 넘는 요청은 거부 대신 자리를 기다림(도착 순서). 자리는 사람 승인 대기 뒤에 잡아 승인 대기가 다른 송신을 막지 않음. 대기 중 취소·철회는 보내지 않고 예산도 쓰지 않음 | 동시 송신 수 상한은 그대로(넘지 않음). 자리를 얻은 뒤 권한·정책 세대·시도 상한·예산·요청 경계(워크플로 일시정지·단계·선택)·같은 call의 상한(C01) 재검사. 대기 중 호출자 시간 초과는 보내지 않은 요청(`closed`)으로 남고 call 거부 상한에는 세지 않음. 보낸 뒤에는 호출자 기한이 아니라 E② 요청 시간 상한으로만 끊어, 대기에 시간을 쓴 요청이 결과 불명이 되지 않음(호출자 취소는 그대로 끊음, #487 리뷰) | 러너 Bash 자리는 실행당 1개 그대로(이미 거부 아닌 대기). 늘리려면 샌드박스·작업 공간(C29) 용량 결정 필요 |
| C16 | 1·2단계 구현·단위시험 (결정안 1·2·4) | 본문 상한을 넘는 응답은 앞부분만 정상 HTTP 응답으로 줌(`withheld` + `response_truncated`, 실제 curl 0 종료). 대상이 답했지만 표현을 주지 않은 응답(자격 응답 헤더·보호 값 반사·압축·부분 응답 상한 초과)을 결과 불명 대신 `withheld`로 셈(`Counts.Withheld`, `response_complete=false`). 결과 불명 상한에 넣지 않아 Set-Cookie를 매번 붙이는 앱에서도 그 call이 멈추지 않음. 도구에는 E2 결과 응답(`response_withheld`). 기록 형식을 `e2-http-v2`로 올리고 검증기·평가 내보내기·수집기는 v1도 읽음. 마감 판정은 withheld를 완료처럼 받음 | 응답 표현 보류 자체(자격 값·보호 값·압축 본문은 도구에 안 감), 끝까지 받기 전 끊긴 응답은 `unknown`, 요청 본문 상한은 송신 전 미지원 | — |
| C16 | 3단계 구현·단위시험 (결정안 3) | 응답 전달 상한(`max_response_body_bytes`)·검사 상한(`max_response_scan_bytes`)을 요청 본문 상한과 따로 줄 수 있음(정책 선택 칸·설치 `runtime_limits`·서버 정책 파일 읽기). 검사 상한은 상한 사이 조건을 적재 때 검사. 칸을 안 준 정책의 지문은 그대로 | 요청 본문 상한은 송신 전 미지원, 응답 상한 코드 상한 64 MiB, 모델 입력은 `OutputLimit`으로 따로 자름 | 기존 설치는 새 설치·갱신 전까지 이전 정책 그대로 |
| C18 | 구현·단위시험 (경로 범위) | 진단 단계에서 선택한 끝점은 같은 자리의 객체 ID 조각만 같은 종류(숫자·UUID·긴 16진·이메일)로 바꾼 같은 메서드 요청도 받음. 경로 가림 규칙(애매하면 값)은 허용 쪽에 쓰지 않음(긴 이름·`:` 동사·대소문자 숫자 섞인 조각은 이름). `/x/1`을 선택하면 `/x/2`를 보낼 수 있음. 같은 경로가 따로 발견됐지만 선택되지 않았으면 근거가 같을 때만 선택한 끝점 쪽으로 보고, 그 경로의 근거가 더 엄격하면(폼·추정이라 possible) 그것을 따름. 모델 지침에 같은 내용 한 문장 | 요청 경로를 승인 접두·제외 경로·위험 단어·읽기 전용 접두로 다시 보고, 선택한 끝점과 범위·변경 분류(`attested_read`·`possible`)가 같을 때만 받음. 이름 조각이 다르거나 메서드가 다르면 거부. 크롤 단계는 발견한 정확한 경로만. 쓰기는 지금처럼 요청마다 사람 승인 | 자동 모드의 GET·HEAD·본문 없음, 끝점 32개 상한, JS 미실행, 진단 중 새 경로 추가 안 함은 그대로 |
| C21 | 구현·단위시험 (자동 모드 쓰기) | 자동 모드(`ai_auto_v1`)가 대상에 승인한 쓰기 메서드를 GET·HEAD로 깎지 않고 건별 확인 없이 보낸다. 쓰기 승인이 있으면 사용자 모드에서 고를 수 있는 폼·쓰기·추정 끝점도 자동으로 고른다(2026-11-13 사용자 결정: 자동 모드는 승인 없이 돈다) | 쓰기 메서드는 설치 정책·대상 등록의 되돌릴 수 있는 쓰기 승인 안에서만(POST·PUT·PATCH), 위험 동작 단어·제외 경로·범위·예산. 결과 불명으로 끝난 상태 변경 가능 요청(쓰기 메서드, GET 폼처럼 비변경 확인 안 된 끝점)과 같은 메서드·경로는 명령이 달라도 그 회차 끝까지 다시 보내지 않음(#525 리뷰). 자동 모드 쓰기는 "AI 자동 모드는 건별 확인 없이 보냄" 문구로 동의한 대상 등록(`write_consent`)만 — 이전 등록은 재등록 전까지 자동 모드에서 GET·HEAD(#525 이경준 리뷰). workflow 없는 일반 회차는 GET·HEAD | 사용자 모드의 요청별 승인 대기 시간(10초·60초)과 그동안 슬롯 점유는 그대로(사용자가 고른 건별 확인 모드) |

## 4. 유지 범위 안에서 따로 볼 것

- 위험 query 차단은 "무엇을" 통제로 유지하기로 했지만 E②에 구현돼 있지 않다(`e2proxy`의 `query`·`RawQuery`는 규칙 설정 파싱에만 나옴).
- 최대 연결 4개를 넘는 연결은 감사 기록 없이 닫힌다(`e2proxy/http.go:156-161`).
- 그대로 두는 것: origin·IP 고정, TLS 검증, 리다이렉트 재검사, DNS 없는 격리 망, 예산·만료·감사 실패·철회 시 정지, 우리 자격 값 가림.
