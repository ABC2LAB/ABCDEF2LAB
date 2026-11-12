# 제품 탐색 단계의 host adapter 연결

이 문서는 P1 추가 코드의 조립·소유권을 설명한다. 기존 탐색 계약의 제안 상태나 팀 담당·승인 상태를 바꾸지 않으며, 운영 탐색 경계가 완성됐다는 뜻이 아니다.

## 권위와 조립

`urlPreparationRuntime.hostCapabilities`는 보호된 Go 조립에서만 공급한다. 기본값은 비어 있다. URL·설치 JSON·모델 출력·환경 변수를 읽어 탐색 경계나 Docker 실행 권위를 자동 생성하지 않는다. 제품 URL 회차의 실제 `ProductTarget.ID/Revision`, job/task/run, 원본 SourceDigest를 `diagnosisflow.Binding`과 탐색 Binding에 결속한다. 읽기 모드를 우선 사용한다. 시간·요청·동시성·본문 상한은 기존 설치 정책보다 넓히지 않고, 외부 자원 예산은 별도로 host 정책에 명시된 값만 받는다. 완전 로컬 모드의 외부 예산은 0이다.

`RunExplorationPipeline`은 초기 일시정지가 있으면 `Workflow.Wait`에서 재개를 기다리고 취소 시 I/O 없이 종료한다. 재개 뒤 SourceDigest의 형식·회차 일치와 TargetURL의 보호된 Controller 결속 일치를 확인한 뒤 factory를 호출한다. origin·URL 문자열을 승인 참조로 바꾸지 않는다. boundary는 승인 origin/IP/경로/메서드/TLS를 보호 저장소에서 직접 읽고 요청마다 집행해야 한다.

`ExplorationPipelineFactory`는 어댑터 객체와 아직 실행하지 않은 callback만 조립한다. 실제 boundary lease의 소유는 `OpenExplorationSession`의 `Boundary.Open`에서, 브라우저 자원의 소유는 그 뒤 `StartBrowser`에서 시작한다. factory 오류나 누락 capability가 NOT_RUN으로 돌아갈 때 미소유 컨테이너·프로필·연결·자식 프로세스를 남기면 안 된다. factory가 먼저 임의 프로세스를 띄우고 정리를 Session에 떠넘기는 구현은 이 인터페이스의 사용 조건을 만족하지 않는다.

## 실행과 저장

기존 authoritative task resolver에서 같은 Worker를 가져와 모델 정책·예약·정산 provider를 재사용한다. 별도 provider나 secret 읽기, 합성 모델 대체를 만들지 않는다. 단계는 `BindExploration → CrawlExploration → ExecuteExploration → private 관찰 마감 → Close`다. `PreparePlanning`과 `PrepareWork`가 같은 task gate를 통과하므로 탐색은 한 번만 실행하며, 정리가 끝나기 전에 진단 자원을 받지 않는다. 저장된 동일 private 결속의 COMPLETED/PARTIAL 마감 뒤에는 레거시 E② 정적 크롤을 반복하지 않고 기존 `StartDeepWork`로 넘긴다. 수동 선택과 일시정지는 그 기존 게이트가 계속 기다리며, 미선택·범위 밖·위험 요청은 기존 `Before`와 E②가 거부한다. 초기 intent가 있는 원본 엔진도 첫 Worker에서 이 gate를 통과한다. 옵션이 없는 기존 E② 실행은 그대로다.

정적 fetch는 private reader의 동일 session/run/job/task/target/revision/SourceDigest/seq와 본문 digest를 확인한다. 모델·JS 보고는 inferred로 남긴다. Browser/Bash/Skill 결과 직후의 host Observe는 다음 모델 호출 전에 새 private seq를 저장한다. 관찰 저장 실패가 발생하면 새 모델·도구 호출을 막고 오류를 보존한다.

종료 순서는 admission latch·취소, `browser_close`, private Finalize, MCP DELETE, BrowserLease 정리, Boundary Close다. Finalize는 기존 관찰의 저장·`FinishExploration`만 하며 모델·대상 송신·원문 로그를 만들지 않는다. 취소와 독립된 짧은 cleanup context의 deadline을 지켜야 한다. callback이나 어느 정리가 실패해도 뒤의 정리를 시도하며 실패를 정상 fallback으로 숨기지 않는다. 취소·철회·경계 종료 뒤에는 새 관찰을 observed로 승격하지 않고 이미 저장한 partial을 보존한다. 정상 browser_close 실패 시 private 저장은 시도하지만 background 송신이 정지됐다고 주장하지 않는다. 운영 boundary의 강제 fence·프로세스 정리 검증은 별도 구현·수용 범위다.

## 기본 가용성과 범위

실제 host adapter가 없으면 `NOT_RUN / boundary_unavailable`을 영속 기록하고 기존 E② 정적 크롤을 유지한다. 이 기록은 initial discovery를 완료하거나 선택 단계를 건너뛰지 않는다. 실제 adapter가 있는 회차의 상한·경계 장애·모델 오류는 실제 종료 사유와 부분 발견을 기록한다. 정리 불명이나 관찰 저장 실패는 진단 admission 성공으로 바꾸지 않는다.

인증 준비·시험 계정 분류, 스크린샷의 모델 시각 입력, 원본 대비 보존율·실제 Qwen, 운영 proxy/registry/namespace 강제는 이 연결 코드의 완료 근거가 아니다. 별도 수용 결과가 없으면 NOT_RUN 또는 미검증으로 남긴다.


## 진단 시작 전 모델 예산 마감

탐색이 공유 모델 예산을 먼저 소진한 경우에는 실패한 진단 실행을 합성하지 않는다. pipeline이 실제 Session.Close 성공, 동일 공유 원장의 한도 도달·정산 완료·사용량 확인, 보호 Controller의 같은 회차/대상/revision/저장 revision·cap_exhausted 부분 마감을 검사하여 비공개 종료 증명을 만든다. Controller 검사는 revision과 private 상태를 같은 lock 안에서 확인한다.

E② runtime은 같은 task/run/workflow/budget와 lease·active·runs·results·calls의 부재를 다시 검사한다. 엔진 시작 전 admission과 Supervisor는 이 증명이 있는 경우에만 기존 미완료 intent를 `partial/budget_limited`로 마감한다. intent가 실제로 0이면 `no_intents/budget_limited`이며 수를 만들지 않는다. 일반 ErrBudget, nil Execution, 공개 Snapshot, 불명 모델 사용량만으로 이 경로를 열지 않는다. 기존 진단의 E② Receipt·감사·E③ 검증은 그대로 수행한다.

실제 production constructor·독립 DB LOGIN·Supervisor.Run·CloseTask/drain·권한 반납·PersistRunOutcomeStored까지 시험했다. 시험 provider 실호출2/공유 예산2/known, observed3/inferred1이 보존되고 진단 Deep0/E② Acquire0/calls0/receipt0이었다. 결과는 incomplete1의 partial/budget_limited였다. typed 증명 제거·불명 원장·가짜 완료·stale/zero 저장 revision은 거부한다. 관련 closure/pipeline/Supervisor race 66개가 통과했다. 이는 실제 Qwen 수용이나 E② 진단 송신 성공 시험이 아니다.

## 회차 요청 예산의 탐색·진단 분할

탐색과 E②가 각각 승인 요청 수 N을 받으면 대상 전달이 최대 2N이 될 수 있다. 프로그램 쪽은 불변 할당으로 같은 회차의 합계를 N에 묶는다(P1-E).

- 탐색 단계(`E2HTTPExplorationStage`)는 오류와 함께 `ExplorationCharge`를 돌려준다. pipeline이 호스트 경계를 감싸 열림 여부와 경계의 마감 상태만 기록한다. 경계를 열지 않았으면 몫 0이 확정이다. 열었으면 `Lease.Close`가 오류 없이 돌려준 비활성·연결 0·결속 일치 상태의 `TargetRequests`만 확정 몫 E다. 마감을 확인하지 못하면 몫을 모르는 것으로 남긴다. 영값 `ExplorationCharge{}`도 모르는 몫이다.
- 탐색은 진단 lease보다 먼저 같은 task gate 안에서 끝난다. `PrepareWork`는 몫을 모르거나 E≥N이면 진단 자원·DB 조회 없이 `budget_limited`로 마감한다. 남으면 `E2HTTPRequest.ExplorationRequests`로 넘기고, 로컬 공급자는 `N-E`만 원장에 등록한다(`e2HTTPDiagnosisRequests`). 공급자도 같은 판정을 다시 해서 runtime 검사와 어긋나도 N-E를 넘기지 않는다. 진단 lease는 task당 한 번만 받으므로 탐색 뒤 새 N이 다시 생기지 않는다.
- 원격 E²는 몫을 뺀 등록을 아직 받지 않으므로 몫이 0이 아니면 lease를 거부한다.
- 탐색 단계가 없는 기존 E② 실행과 경계 미공급 NOT_RUN(몫 0)은 기존 예산 N 그대로다.

남은 경계 의무: 운영 경계는 승인 대상으로 넘긴 요청을 실패·결과 불명·재시도를 포함해 admission 때 세고(`TargetRequests`), `MaxRequests`를 넘기지 않고, `Close`가 새 송신이 끝났음을 확정한 뒤에만 비활성 상태를 돌려줘야 한다. 이 몫은 경계가 실제로 센 값이고 프로그램이 대신 셀 수 없다. 재기동 뒤 같은 작업을 이어 실행하는 경로가 생기면 몫을 영속 기록에서 다시 읽어야 한다. 운영 경계·B16 차단이 들어오기 전까지 운영 adapter 기본 nil은 그대로다. 모델 요청·토큰·USD의 기존 공유 예약 원장은 별개로 유지한다.

## 실제 E② 진단으로 이어지는 시험 범위

리뷰 보완의 owned PostgreSQL 시험은 동일 workflow의 탐색 관측3·추정2 저장 → 명시적 수동 선택 → production 시작 hook → 원본 Worker → native sandbox의 실제 TLS GET1 → E② 감사 Final/drain → Supervisor Done1/complete → 결과·private 발견 영속 재조회를 확인한다. 선택 전 Acquire0/GET0이고, 선택 뒤 레거시 크롤 송신을 반복하지 않는다. 이 시험의 탐색 MCP/Reader는 fixture다. 실제 Chromium 두 모드 실행과는 별도 근거이며 운영 adapter·두 입구 계측기 종단을 대신하지 않는다.
