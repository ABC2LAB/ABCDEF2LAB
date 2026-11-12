# P1 탐색 복원 구현과 의존성

2026-11-12 작업의 구현 기준은 `dev`의 `b679f7b7e62744943f88f4334c49820265811633`이다. 최종 통합 기준은 `57020d0827b27c7d891336b0fb49c45dc26402e5`이며 #447·#444·#450의 병합을 네 자체 브랜치에 반영했다. 실제 Chromium 회차는 그 전에 고정 빌드한 `07bed28d0832e75e1f9964889253b7f662eaadd2`와 바이너리 hash를 근거로 삼고, dev 통합 후 전체 회귀는 CI에서 따로 확인한다. 원본 비교 자료는 저장소에서 읽을 수 있는 ARTEX import `e53a79fe4df0c95a5b09274de8db321fa038b00b`를 사용한다. 실제 원본 실행의 발견 수나 보존율은 이 조사에서 만들지 않는다.

## 조사와 구현 순서

기존 흐름은 `Planner → 보호 Worker → diagnosisflow.Crawl → E② Bash/curl → HTML/JS 수집 → 진단`이다. `agent/e2_http.go`의 기존 Crawl은 E② `e2runner.Result`의 감사·drain·마감 검증을 필요로 한다. 이를 제거하거나 직접 `http.Get`으로 바꾸지 않는다. 일반 Worker Augment는 원래 native Bash·WebFetch·파일·graph·MCP를 열 수 있으므로 탐색 프로필에 사용하지 않는다.

추가 경로는 `서버가 승인 회차 결속 → 탐색 세션 → 직접 입구의 정적 크롤 → 원본 Worker의 제한 프로필·HTTP MCP/API Recon → private 관측과 inferred 후보 저장 → 기존 자동/수동 선택 → E② 진단`이다. 다음 순서로 별도 worktree에서 구현한다.

1. A: 독립 exploration 결속, 고정 MCP 21개 도구, 기존 agentcore Session 재사용, 서버 소유 취소·마감·연결 정리.
2. B: E②형과 별도 탐색 Fetch/Reader, 메타데이터 수집, observed/inferred·저장 상한·선택 흐름.
3. C: 실제 제품 stage hook, 소유 Docker fixture·수신기·고정 Chromium/MCP 종단, 실패/미실행 표시.
4. D: 기존 계측기와 평가 runner의 explore/main 분리, 별도 서명 sidecar·비교 조건 검증. A/B/C와 독립이다.

A [#451](https://github.com/ABC2LAB/ABCDEF2LAB/pull/451) → B [#453](https://github.com/ABC2LAB/ABCDEF2LAB/pull/453) → C 스택은 최대 세 PR이며 D [#452](https://github.com/ABC2LAB/ABCDEF2LAB/pull/452)는 dev 독립 PR이다. 원격 PR을 자동 병합하지 않는다. 운영 탐색 경계 담당의 어댑터가 아직 없으므로 제품 기본값으로 임의 프록시를 생성하거나 인터넷 직결을 열지 않는다. host가 공급한 실제 Boundary·BrowserLease·private Reader·직접 Fetch가 모두 갖춰져야 새 경로가 활성화된다. 미공급은 NOT_RUN이고 기존 보호 정적 크롤을 사용할 수 있다.

## 열린 PR 확인

| PR | 작업 시작 시 상태 및 영향 | 제출 전 재확인 |
|---|---|---|
| #443 | 작업 공간 반입·반출, 도중 b679f7b로 병합 | 기준에 포함 |
| #445 | 설치 실행 예산, 05b2097로 병합 | 기존 예산 사용 |
| #449 | intent 0개 no_intents, 병합 | 탐색 미실행/부분 완료와 구분 |
| #444 | intent별 보관, #443 위 스택 | a35227e로 dev 병합, 최종 통합에 포함 |
| #446 | 긴 출력 저장, #444 위 스택 | 열림, head ed034bbbe1fa26a6fd926852dcd77fc8e8b2e2e2, base dev, 리뷰 없음; 최신 CI 실행은 PR에서 확인 |
| #447 | Workbench 화면 구조 변경 | aa034c8 병합. 화면 파일 수정 없음 |
| #450 | 중지된 실행 intent 개수 | 57020d0으로 dev 병합, 최종 통합에 포함 |
| #454 | 저장 보고서 실행 결과 | 열림, head 4b3c920af6a2a830fec6b1114968f4906e40af88, #450 브랜치 위 스택. 리뷰·inline 없음, CI 실행 중, 변경 파일은 이 작업과 겹치지 않음 |

#444/#446의 runner·sandbox 파일과 `agent/e2_http.go`는 변경하지 않는다. #450의 `url_preparation_runtime.go`는 제품 host capability 연결과 같은 파일이므로 각각의 diff를 확인하고 기존 중지 결과 분기는 유지한다. 다른 담당자의 브랜치·리뷰·PR은 변경하지 않는다. 목록과 CI는 조회 시점의 상태이며 최종 PR 설명에서 다시 확인한다.

## 복원 범위와 남은 차이

- MCP 0.0.84의 HTTP 연결과 정상 navigation/evaluate/click/snapshot/network metadata 도구를 재사용한다. unsafe code·파일 upload·헤더/route 임의 조작·추가 MCP·일반 Augment는 제공하지 않는다.
- 모델/턴/시간과 비용 provider는 기존 task Worker에서 가져온다. 모델 실패에 합성 응답으로 대체하지 않는다. 시험의 결정적 provider는 명시적인 test fixture다.
- API Recon은 기존 정적 문법과 metadata 관찰 로직을 경계 안에서 사용한다. 원본 Python/Node 송신 스크립트, TLS 무시, 브라우저 밖 fetch, mock 인증 성공은 열지 않는다.
- 모델/JS 보고는 inferred다. observed는 host private Reader가 session/run/job/task/target/revision/source/seq를 검증한 실제 요청뿐이다. 공개 가린 경로 기록으로 private 경로를 추측 복원하지 않는다.
- 공개 선택 목록 32개/48KiB와 별도 발견 inventory 256개/64KiB의 상한·누락 수를 구분한다. 저장 확장이 승인 범위·진단 예산을 넓히지 않는다.
- 탐색 Worker에는 graph·후보 확정·E③ 도구가 없다. 진단 프로필은 MCP를 계속 거부하며 E② 정책·Receipt·감사 정본을 유지한다.
- 현재 MCP 어댑터는 text 블록을 전달한다. screenshot의 모델 시각 입력, 실제 계정 분류·인증 준비, mock/stub 화면 분석의 자동 통합, 운영 경계 및 WS/QUIC 집행은 동등 복원 완료라고 주장하지 않는다.
- 실제 Qwen과 원본 대비 R-P/R-F/R-R은 승인·예산·동일 비교 조건이 없어 NOT_RUN이다. 합성 서명 회귀는 보존 실측이 아니다.

실제 fixture 실행 절차는 [하네스](../evaluation/browser-fixture-harness.md), 관측 저장 계약은 [API Recon 복원](../contracts/api-recon-restoration.md), 실제 실행 결과와 항목별 미검증 범위는 [결과](../evaluation/p1-browser-fixture-results.md)에 기록한다. 평가 PR D의 서명 형식은 해당 선행 커밋/PR의 문서를 따른다.

실제 별도 제어망 검사에서 브라우저 컨테이너의 Node 자식이 자기 MCP 제어 포트에 TCP 연결했다(B16 **FAIL**). 도구 필터가 이 네트워크 도달을 막는다고 주장하지 않는다. 운영 어댑터를 연결하기 전에 경계 담당이 프로세스·제어 채널의 접근 통제를 완성하고 같은 음성 시험을 통과해야 한다. 현재 제품의 host capability 기본값은 nil이다.
