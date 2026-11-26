# 화면 NodeGoat 점검 실측 — 사용자 승인 모드 첫 회차 둘 (2026-11-26)

2단계 첫 대상 NodeGoat([phase2-targets.md](phase2-targets.md) §1)에서 사용자가 화면으로 돌린 승인 모드 회차 둘의 기록이다. 목적은 처음 쓰는 대상에서 제품이 도는지 보고 고칠 거리를 찾는 것이다. 측정·채점 회차가 아니다.

- 러너 세션은 준비·관찰·기록을 맡았다.
- 회차 도중에는 회차 컨테이너에 명령을 넣지 않고 제품 API만 읽었다.
- 컨테이너 안 자료(진단 로그·활동 기록·준비 로그)는 회차가 끝난 뒤에 읽었다.
- 비밀번호·토큰·승인 ID 값은 적지 않는다.

**결과 요약**: 두 회차 모두 `failed`로 끝났다. 진단 단계(승인·모델 단계 요청·확정)까지 가지 못했다.
- 회차 1: 탐색 중 요청 하나의 관찰 실패가 회차 전체를 끝냈다(발견 1).
- 회차 2: 탐색은 끝났지만 범위 확인을 기다리는 동안 회차 시간 상한에 닿았다(발견 5·6).

앞 기록: [ui-vampi-rehearsal-1125-approval-results.md](ui-vampi-rehearsal-1125-approval-results.md)(VAmPI 승인 모드, 2026-11-25).

## 1. 회차 고정값

| 항목 | 값 |
|---|---|
| 소스 | dev `e5fb7829`(#946 승인 대기 동안 프록시 요청 상한 멈춤, #952 확정 투영, #953 `closed_reason`, #951 포함) |
| 설치 | 리허설 설치(볼륨 새로). 외부 모델(OpenRouter) manifest v4 고정 모드(PrimeIntellect 하나, 넘김 없음), 단가 상한 입력 $0.255·출력 $2.28, 비용 상한 $5. 회차 실행 시간은 제품 기본값 1800초 |
| 모델 | `qwen/qwen3.6-35b-a3b`, 제공자 `primeintellect/fp8` |
| 대상 | NodeGoat `OWASP/NodeGoat c5cb68a7084e4ae7dcc60e6a98768720a81841e8` 고정 빌드 이미지 `abc2lab-practice-nodegoat:c5cb68a7084e`(`sha256:e6bbbf4e…`, 빌드 바탕 node:12-alpine `sha256:d4b15b3d…`) + mongo:4.4 `sha256:4be76f67…`. 앞단이 `nodegoat.test:9443`(TLS)을 받아 web:4000으로 넘긴다 |
| 대상 망 | web·mongo는 내부 망(compose `target`, `internal: true`)에만 붙는다. 대상 서버가 외부로 못 나가므로 `/research`는 제외하지 않았다 |
| 띄우는 법 | 레포 밖 리허설 overlay(`compose.rehearsal-nodegoat.yaml`, VAmPI overlay와 같은 모양). 레포 overlay는 아직 없다 |
| 대상 승인 | 경로 `/`, 되돌릴 수 있는 쓰기 허용, 4시간 |
| 로그인 방식 | `{"method":"POST","path":"/login","format":"form","username_field":"userName","password_field":"password","cookies":["connect.sid"]}` |
| 시험 계정 | 시드 `user1`·`user2`(비관리자, 비밀번호는 `db-reset.js` 공개 기본값 → 보호 파일), 시험 계정 표시·자격 등록 |
| 정책 선언 | 넣지 않음(HTML뿐이라 확정 단계 `declaration_missing`이 정상, phase2-targets §1.5) |
| 점검 설정 | 사용자 승인(`user_approval_v1`), 제외 경로 `/logout`, 시험 계정 외부 전송 켬, 시작 확인 두 칸 |
| 회차 1 | task 1(초안 `prep-7eacecbd…`) |
| 회차 2 | task 2(초안 `prep-b371cb05…`). 같은 설치. 회차 사이에 NodeGoat 웹을 다시 시작했다(시작마다 db-reset으로 초기화) |

## 2. 회차 직전 시드 로그인 확인(사용자 2026-11-26 결정)

제품의 쿠키 로그인 성공 판정은 틀린 비밀번호도 '인증됨'으로 볼 수 있다(phase2-targets §1.2). 제품 수정은 보류됐다(#958). 그래서 2단계 회차마다 러너가 회차 직전에 두 계정 로그인 결과를 직접 확인한다.
- 확인은 대상 망의 일회용 컨테이너에서 했다(`ng_login_check.sh`, 레포 밖).
- 앞단 `https://nodegoat.test:9443`과 `nodegoat:4000` 두 길로 불렀다.

| 시각(KST) | user1 | user2 | 틀린 비밀번호 | 판정 |
|---|---|---|---|---|
| 10:24(회차 1 전) | 302 `/dashboard` | 302 `/dashboard` | 200 + `connect.sid` | 통과 |
| 11:29(회차 2 전, 웹 재시작 뒤) | 302 `/dashboard` | 302 `/dashboard` | 200 + `connect.sid` | 통과 |

두 길의 결과가 같았다.

## 3. 회차 1 — 탐색 1분 만에 실패

경과(KST)

| 시각 | 단계 |
|---|---|
| 11:18:59 | 점검 시작(계획) |
| 11:19:27 | 실행 단계, 탐색 |
| 11:20:29 | 탐색 작업자의 도구 결과 `exploration_observation_unavailable` → `exploration_session_unavailable` → 작업자 취소 |
| 11:20:30 | 범위 확인 대기로 바뀐 직후 |
| 11:20:32 | 회차 `failed` |

- 탐색: 대상 요청 106건, 찾은 끝점 69개(GET 404 40·200 23·302 5, HEAD 302 1)
  - 시험 계정 세션 없이 돌아서, 화면 대부분이 `/login`으로 302였다(발견 4).
- 모델 호출 18번, 약 $0.07
- 인증 준비 전에 끝나 `account_auth`가 없다. 승인 요청은 0건이다.
- 확정 단계: `disabled`/`declaration_missing`(예상대로)
- 진단 로그
  - `planning_exploration_failed` → `engine_task_failed`(63초) → `execution_failed`
  - `diagnosis_finished` `observation_incomplete`

## 4. 회차 2 — 탐색은 끝났지만 시간 상한 도달

| 시각(KST) | 단계 |
|---|---|
| 11:31:21 | 점검 시작. 목표 만들기 87초 |
| 11:35:21 | 계획 끝(240초) |
| 11:35:23 | 실행 단계, 탐색 |
| 11:52:17 | 탐색 끝(17분, 대상 요청 279건, 끊김 없음). 범위 확인 대기 |
| 11:59:31 | 정리 시작 `time_budget_reached` |
| 12:01:01 | 계획 `planning_workflow_blocked` → `engine_task_failed` → 회차 `failed` |

- 범위 선택 전에 끝나, 인증 준비·승인·모델 단계 요청·후보가 모두 0이다.
- 확정 단계: `disabled`/`declaration_missing`
- **모델 호출 112번 중 429(`http_rate_limited`) 45번(40%)**
  - 목표·계획·탐색 전 구간에서 났다. 목표·계획에 5분, 탐색에 17분이 걸린 주된 까닭이다.
- 비용 약 $0.33. 이 설치의 두 회차 합계 키 사용은 $0.40이다.

## 5. 이 회차에서 확인하지 못한 것

진단 단계에 가지 못해서 아래는 이번에 보지 못했다. 다음 실제 모델 승인 모드 회차에서 본다.
- #946: 2분 둔 승인이 60초에 닫히지 않는지
- #953: 닫힌 승인의 `closed_reason`(`operator_stopped`·`model_stage_closed`)
- #952: 확정 생성 승인 이력이 `awaiting_dispatch`로 남지 않는지
  - NodeGoat는 확정 단계가 꺼지므로 VAmPI에서 봐야 한다.
- 인증 준비에서 시드 두 계정의 `account_auth`
- 제외 경로 `/logout` 집행

## 6. 발견

| # | 발견 | 근거 | 담당·상태 |
|---|---|---|---|
| 1 | **탐색 요청 하나의 관찰 실패가 탐색 세션을 끊고 회차 전체를 실패시킨다.** 탐색 작업자(모델)가 Bash curl로 `/api/*`·`/web/api/*`(셸 글롭이 안 풀린 `*`)가 든 경로를 보냈다. 그 요청은 대상에 실제로 나갔고, 탐색 경계도 정상으로 보내고 기록했다. 그런데 진단 흐름이 그 관찰을 발견 목록에 올릴 때, 경로 검사(`diagnosisflow` resolve)가 Go가 다시 인코딩하는 문자(`* ( ) ! ' [ ]`)가 든 경로를 거부한다. 그 거부가 관찰 실패(`exploration_observation_unavailable`)로 바뀌어 세션을 끊었다. 기조('요청 하나의 실패는 그 요청만 실패')에 어긋난다 | 회차 1 활동 기록 111~116(두 curl, 도구 결과 두 개, 작업자 중단), 준비 로그 `[exploration] 탐색 단계 실패 tool_call/tool_error → tool_admission/cancelled → worker_run/cancelled`, 진단 로그. 원인은 기존 세션이 코드로 확인했다(경계 로그 0줄은 정상) | 기존 세션. 사용자 결정 (가)로 고친다(리뷰 동찬, 머지 전 러너 합성) |
| 2 | 실패한 초안의 `reason_code`가 비어 있어 화면에는 '실패'만 보인다. 원인은 진단 로그에만 있다(`planning_exploration_failed`·`time_budget_reached`→`engine_task_failed`) | 두 회차 초안 끝 상태 | 원래 있던 문제(러너 §7 확장 판정 기준 메모). 제품 계약 변경이라 결정 필요 |
| 3 | 탐색 작업자 취소에 원인 이름이 붙지 않는다(`canceled_no_cause`, 활동 기록이 `agent/cancelcause.go`에 이름 등록을 안내) | 회차 1 활동 기록 116 | 발견 1과 같이(기존 세션) |
| 4 | 탐색이 시험 계정 세션 없이 돈다. NodeGoat는 거의 모든 화면이 로그인 뒤라, 탐색이 로그인·가입·튜토리얼·정적 파일과 추측 경로(404)만 본다. 진단 단계에서 세션을 쓰기 전까지 앱 구조를 모른다 | 회차 1 끝점 69개(404 40), NodeGoat 로그 `redirecting to login` | 설계 질문(탐색 담당) |
| 5 | **범위 확인을 기다리는 중에 회차 시간 상한이 닿으면 `failed`로 끝난다.** #830의 시간 상한 마감은 `completed`/`budget_limited`인데 그렇게 끝나지 않는다. 탐색 결과는 남지만 회차는 실패로 보인다 | 회차 2 진단 로그 `drain time_budget_reached`(11:59:31) → `planning_workflow_blocked` → `engine_task_failed` | 기존 세션(사용자 결정 2026-11-26): 범위 확인 대기 중 시간 상한에 닿아도 탐색 결과가 있으면 정한 마감 상태로 끝나게 한다 |
| 6 | 승인 모드에서 사람이 범위를 고르는 시간도 회차 시간 상한(제품 기본 1800초)에 들어간다. 429로 목표·계획·탐색이 22분 걸려서 고를 시간이 7분쯤 남았다 | 회차 2 경과, 429 45/112 | 기존 세션(사용자 결정 2026-11-26): 범위 선택 대기 동안 회차 시간을 멈추고 따로 대기 상한을 둔다. 넘으면 고정 사유로 끝낸다 |

## 7. 보관 자료(레포 밖, 0600)

러너 PC `abc2lab-logs/rehearsal/obs-nodegoat/`(회차 1)과 `obs-nodegoat/run2/`(회차 2)에 있다.
- 감시 출력: 흐름·계정 인증·e2-flow·후보·로그·승인
- 진단 로그 사본
- 회차 1 활동 기록 구간
- 준비 로그 사본
- 시드 로그인 확인 결과

설치는 기록 뒤 내렸다(볼륨 삭제).
