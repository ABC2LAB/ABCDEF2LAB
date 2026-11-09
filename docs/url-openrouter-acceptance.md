# Workbench OpenRouter 합성 수용 기록

2026-11-10 KST. 기준 dev `8f479109af72200b665605927c18af1b5fbccb97`, 서버 PR [#351](https://github.com/ABC2LAB/ABCDEF2LAB/pull/351). 서버 설치와 비용·키·승인 계약은 [통합 문서](url-openrouter-integration.md)를 따른다.

## 관측 결과

| 경로 | 결과 | 실제 관측 |
|---|---|---|
| 로컬 URL v2 회귀 | PASS | 단일 회차 9회, 동시 Worker 회차 11회, 기존 비용 0 |
| OpenRouter SDK 단일 회차 | PASS | goals 2 · Planner 3 · Worker 4 = 9회, 합성 보고 비용 9µUSD, 대상 허용 전송 1 · 차단 경로 도달 0, 후보 1 · 확정 취약점 0 |
| OpenRouter 동시 Worker | PASS | 실제 동시 intent 2개, Worker 2개. 한 모델 응답 대기 중 추가 예약/송신 0. 총 11회 · 합성 보고 비용 11µUSD, 허용 전송 2 · 차단 경로 도달 0, 후보 2 |
| 단계별 비용 저장 | PASS | 준비 4회와 실행 5회를 별도로 저장. 합은 URL job의 9회/9µUSD와 일치하며 준비 비용을 중복 합산하지 않음 |
| API·준비 서비스 재시작 | PASS | 같은 Start key 중복 실행 없음, job/source digest·저장 결과/HTML 보존, 조회에서 추가 모델·대상 호출 0 |
| 실제 Chrome · Next · URL API | PASS | 프로필/URL → 모델·provider·USD·외부 전송 확인 → 동의 → 명시적 시작 → 시작 응답 유실 → GET 복원 → 결과·HTML 내보내기 |
| 실제 Chrome 중지 버튼 | PASS | 상태 다시 조회 → 중지 POST 200, 모델 3회 시점 취소, 예약 792576µUSD 보존, usage 불명, 후속 모델·대상 전송 0 |
| URL 프로세스 오류 5종 | PASS | usage 누락, model 불일치, provider 불일치, 비용 초과, HTTP 401. 각각 모델 1회 뒤 unknown 유지, 예약 보존, 재시작용 재전송 없이 추가 호출/대상 전송 0 |
| USD 단독 경계·소비 승인 | PASS | 요청/토큰 여유를 둔 USD 1µ 부족 거절, 미소비 승인 대조군, 소비 marker 생성자 거절. 각 검사를 제거한 시험 overlay에서 새 시험이 실패하는 것도 확인 |
| Web 단위/전송 회귀 | PASS | 925개 중 소켓 권한으로 실패했던 기존 3개는 loopback 허용 환경에서 재검증 PASS, 별도 Node 시험 2개 PASS |
| 프로덕션 Web 빌드·타입 검사 | PASS | 기존 의존성, 전용 SWC 캐시로 기본 `next build` 통과 |
| 실제 유료 OpenRouter | NOT_RUN | 별도 명시 승인·실제 USD 총액·운영 키/계정/provider 설정 미제공 |
| 실제 Sparrow | NOT_RUN | 대상 범위·프로토콜·인증·예산 승인 미확인. 실제 요청하지 않음 |

합성 비용은 시험 서버가 응답한 값이다. 실제 계정 청구나 Qwen3.5 추론 성능으로 해석하지 않는다. 대상은 소유 loopback HTTPS 서버와 일회용 PostgreSQL이며 제품의 E②·TLS 검증을 사용한다. Go overlay는 시험 바이너리에서만 승인된 모델 transport의 물리 연결을 loopback으로 고정한다. 제품에 테스트 endpoint·TLS 우회 설정을 추가하지 않는다.

## 재현

기존 설치 의존성만 사용한다. `ARTEX_FINALIZATION_SCOPE_TEST_DSN`에 일회용 PostgreSQL을 지정하고 `ARTEX_FINALIZATION_SCOPE_TEST_OWNED=1`, `ARTEX_URL_DIAGNOSIS_TEST=1`을 설정한다. Linux 비특권 namespace/helper가 필요하다. `/var/tmp`처럼 짧은 TMPDIR를 사용해야 회차별 E② Unix 소켓 경로가 106바이트 제한을 만족한다.

```sh
go test ./server -run 'TestFirstDiagnosisURL(OpenRouter.*|IndependentProcesses|ConcurrentWorkers)OwnedPostgres|TestURLOpenRouterFailuresStopFurtherCallsOwnedPostgres' -count=1 -timeout=15m
go test -race ./llmpolicy ./diagnosisprep ./diagnosisapi ./server -run 'OpenRouter|ModelBudget|ExternalTransfer' -count=1
```

브라우저는 [기존 절차](first-diagnosis-browser-acceptance.md)에 따라 개인 브라우저와 분리한 일회용 프로필·loopback CDP로 실행한다. `ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE`, `ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT`에 `scripts/workbench-url-diagnosis-browser.cjs`, `ARTEX_FIRST_DIAGNOSIS_BROWSER_OUTPUT`, `ARTEX_FIRST_DIAGNOSIS_CDP`를 설정한 뒤 다음을 실행한다.

```sh
go test ./server -run 'TestFirstDiagnosisURLOpenRouterBrowser' -count=1 -timeout=15m
```

WSL에서 Windows Node를 사용할 때 CDP·출력 경로·`ARTEX_URL_OPENROUTER_BROWSER`는 WSLENV로 전달해야 한다. 모델 키는 시험 환경변수로 전달하지 않는다. 새로고침·결과 조회는 GET만 사용하고 Stop/Start POST를 자동 재시도하지 않는다. revision 충돌 시 명시적 상태 재조회 후 사용자가 다시 판단한다.

실행 중 채팅·추가 지시는 별도 계약과 후속 PR 범위다. 일반 채팅 API를 URL host에 연결하지 않았다. 진단 범위나 예산을 확대할 수 없는 task/intent/권위 결속 명령 계약과 감사가 먼저 필요하다.
