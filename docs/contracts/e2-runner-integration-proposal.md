# E②–러너 연결·준비 결속 계약 초안

2026-11-07 · 이동찬 · **팀 검토용 제안. 새 전달 형식·서버 준비/시작 API는 미구현이며 운영 비활성이다.**

기준 소스는 `dev` `1e185a1e917caa0618bc017078b16bfab31604b0`다. 이 문서는 E②가 러너에 넘길 비민감 결과와 마감 근거, IDOR 준비 자료의 실행 결속, 판정 export의 호출 순서를 검토하기 위한 서버 측 초안이다. 기존 wire·평가 프로토콜·판정 계약을 자동으로 개정하지 않는다.

[#275](https://github.com/ABC2LAB/ABCDEF2LAB/pull/275)의 [A/B 준비 절차](https://github.com/ABC2LAB/ABCDEF2LAB/blob/f1c24c11ba04ed06be6f5342f5993fcba1e2db0f/docs/contracts/ab-session-prep-stage1.md)는 **미병합 초안**으로 참조한다. 그 초안의 범위는 `EvalRunID`가 없는 비평가 통합 회차다. 아래 평가용 결속 제안을 #275에 이미 구현되거나 합의된 기능으로 해석하지 않는다.

## 1. 기준·담당·현재 상태

| 경계 | 담당·정본 | 이번 제안의 범위 |
| --- | --- | --- |
| 집행 정책·호출 귀속·감사·서버 수명 | 이동찬, [E② 공통 계약](./e2-network-enforcement.md) | 러너 소비 접점과 미완료 처리 |
| 러너·계측·시도별 대조·공식 조건 | 최민준, [평가 프로토콜](../evaluation/evaluation-protocol.md) | 서버 산출물과의 공동 정렬. 러너/채점 코드는 변경하지 않음 |
| carrier·소유·정책·표식 내용 | 이경준·공급 운영자, [carrier 선정](./kg-canary-carrier-stage1.md) | 등록·페어링을 소비. 판정/GT로 내용 선정하지 않음 |
| reset·계정·세션·준비 자료 공급 | 윤재상 fixture 하네스·운영자, [입력 대장](./first-idor-host-inputs.md) | 실제 실행 주체·장비 경로는 공급 담당 확인 필요 |
| 판정 export | 서버 동결 = 이동찬, 러너 adapter = 최민준 | [#141 응답 제안](./verdict-record-page-api-proposal.md)의 순서·결속. 판정 필드 의미는 변경하지 않음 |

현재 코드와 제안의 구분은 다음과 같다.

- `e2proxy`에는 `e2-http-v1`의 `Receipt`·`CallResult`·`ExecutionView`와 별도 감사 기록이 있다. 라이브러리 구현이 제품 서버의 일반 경로 활성화나 실제 배포 수용을 뜻하지 않는다.
- 등록 GET은 기존 `transmit`·private snapshot·[KG registry](./kg-fixture-registry.md)를 사용한다. 일반 HTTP 프록시의 wire나 예산·승인을 이 경로에 섞지 않는다.
- 현재 러너의 `e2_placement`는 배치/집계 근거이고 완전한 시도별 E② export가 아니다. `e2_mode=none|stub`만 허용하는 현재 소비 경로에서 `stub`을 공식 제품 결과로 계산하지 않는다. 새 생산자·소비자 연결은 후속 구현이다.
- #141의 동결 API·manifest와 러너 adapter, 아래 준비/결속/시작 경계도 미구현이다. 이 문서의 수용 항목은 시험 계획이며 실행 결과가 아니다.

11/09에는 서버 접점·결속·마감 조건의 공동 정렬, 11/10 전에는 러너가 의존할 인터페이스 합의를 목표로 한다. 구현 완료일·실제 회차 적격성·type/GT 완료를 이 날짜로 보장하지 않는다.

## 2. 집행·호출·시도 연결

### 2.1 요청 통제와 전송 범위

[공통 계약 §7.1](./e2-network-enforcement.md#71-제품-일반-http-정책-모드와-공식-정책-인스턴스-제안운영-비활성)의 일반 HTTP 모드와 [등록 GET 전송기](./e2-http-transmitter.md)를 분리한다. 대상 origin·메서드·경로 범위·IP·승인·예산·만료·중지는 신뢰 host가 공급하고 E②가 각 지원 송신 전에 검사한다. 메서드 허용을 앱의 비변경성 증명으로 사용하지 않는다.

HTTP 비교에 적용할 정책은 해당 [공식 프로파일](../evaluation/profiles/official-35b.json)에서 고정하며 값을 코드에 복제하지 않는다. HTTPS 검사·실행별 CA·대상 인증서 검증·검사 실패 거부는 별도 제품 수용을 따른다. HTTP 수용으로 HTTPS 수용을 대신하지 않는다. 무검사 터널·우회 출구·미지원 경로의 처리도 공통 계약의 경로별 수용으로 확인한다.

요청/응답 보존·자격 처리의 현재 한계는 [전달 범위](./e2-proxy-transfer-scope.md)와 [평가 전용 caller 자격](./e2-evaluation-caller-credentials.md)을 따른다. 이 초안이 보호 세션 후보 모드·평가 특례를 기본 경로에 설치하거나 승인하지 않는다.

### 2.2 식별자와 결속 주체

| 값 | 의미·발급/결속 | 소비 제한 |
| --- | --- | --- |
| `task_id`, `intent_id` | 서버/DB의 실제 작업·Planner 의도 식별자 | 요청·파일 값만 믿지 않고 실제 소유·mode와 대조 |
| registry `run_id` | 하네스의 준비 회차 | E② `Scope.RunRef`나 평가 `run_id`로 대체하지 않음 |
| 평가 `run_id` / 서버 `EvalRunID` | 신뢰 러너의 평가 회차와 그 서버 결속 | 비평가 첫 회차는 `EvalRunID` 없음 |
| `Scope.RunRef` | E② 개별 실행 | 새 실행 참조로 승인·누적 예산을 초기화하지 않음 |
| `command_ref`, `call_ref` | 신뢰 기동기의 도구 호출 참조와 E② 호출 참조 | 활동의 기존 `tool_use_id`와의 대응은 신뢰 기동기가 결속. 모델 제출값·시각 근접으로 추정하지 않음 |
| `attempt_id`, `sequence` | E②가 판정 전에 발급한 시도 ID·실행 내 순번 | 승인 자격이 아님. 시도별로 새로 발급하고 거부에도 보존 |
| `policy_revision`, `rule_ref` | 해당 시점의 정책 revision·규칙 참조 | 정책 내용 지문이나 쓰기 1회 승인 참조를 대신하지 않음 |

하나의 도구 호출은 E② 시도 **0건·1건·여러 건**을 가질 수 있다. 재시도·리다이렉트의 지원 여부는 각 모드 계약에 따르고, 지원되는 새 송신은 새 시도와 현재 gate를 거친다. 미결속은 별도 상태로 보존한다.

`X-ABC2LAB-E2-Attempt`는 E②가 발급한 무작위 128비트 소문자 hex 32자를 쓴다. 호출자 값은 신뢰하지 않고 교체하며, 계측기는 유효 ID를 기록한 뒤 대상 전달 전에 제거한다. 형식 유효성은 출처 인증이 아니고, 실행·시도·정책 결속과 중복/재사용은 마감 후 대조한다. ID 누락·오류 행도 계측 총량에서 제거하지 않는다.

## 3. 결과·사유 어휘와 소비

### 3.1 현행 wire

현재 [`e2proxy/types.go`](../../e2proxy/types.go)의 `Version`은 `e2-http-v1`이다. 다음은 현행 필드이며 새 export 스키마 선언이 아니다.

| 타입/필드 | 현행 의미 |
| --- | --- |
| `Receipt.decision`, `reason` | 집행점의 정책 결정과 고정 사유. HTTP 상태·모델 설명과 별개 |
| `forward_attempted`, `request_written`, `response_received`, `response_complete`, `not_sent` | 전송 단계의 관측. 어느 한 비트로 대상 처리 완료를 추정하지 않음 |
| `audit_complete` | 해당 결과의 감사 저장 ACK 상태. 전체 실행·자식 종료·러너 수집 완료를 뜻하지 않음 |
| `CallResult.final`, `audit_complete` | 해당 호출의 최종화와 감사 상태. 전체 실행의 최종 checkpoint를 대신하지 않음 |
| `scope`, `intent_id`, `call_ref`, `command_ref`, `attempt_id`, `sequence`, `policy_revision`, `rule_ref` | 해당 타입이 보유한 실행·호출·시도 결속. 필드가 없는 타입에 자동 추가하지 않음 |

현재 `Reason` 값은 `complete`, `unsupported`, `policy_denied`, `closed`, `audit_failed`, `expired`, `revoked`, `budget_exhausted`, `transport_failed`, `unknown`, `attempt_limit`, `denial_limit`, `transient_limit`, `unknown_limit`, `invalid`다. 다른 문서의 제안 상태어를 이 enum에 들어 있는 값처럼 쓰지 않는다.

시도 기록의 `decision` 어휘는 `allowed`·`denied`·`unsupported`다. 시도 이전 거절이나 관리/종료 사건에서 wire가 허용하는 빈 decision·없는 시도 ID는 그대로 구분하고, 누락을 `allowed`나 가짜 시도 ID로 채우지 않는다.

### 3.2 논리 결과와 미완료

공통 계약의 `policy_denied`, `not_sent`, `send_attempted`, `response_received`, `outcome_unknown`은 전달할 의미의 구분이다. **정책 결정 축과 관측 축을 분리**하고, 이를 현재 `Reason` 하나로 덮어쓰거나 단계 이력을 서로 배타적인 총건수로 합산하지 않는다. 러너 소비용 직렬화와 변환표는 최민준과 합의한 새 버전에서 고정한다.

특히 현재 구현에는 `Reason=unknown`이면서 `response_received=true`, `response_complete=true`인 응답 보류가 있다. 응답 수신, 도구에 원문 공개 가능 여부, 전송/처리 결과 불명을 보존해야 한다. 응답 보류를 미송신 거부나 대상의 접근통제 성공으로 바꾸지 않는다. Unknown 한도와의 연동은 전달 범위 문서의 선언된 한계로 남긴다.

E② 내부 결과는 인증된 서버 경계에서만 받는다. Bash stdout·종료코드·HTTP 표식·Planner 요약·`purpose`는 인증과 관측 근거를 대신하지 않는다. 거부를 가짜 대상 응답으로 만들어 E③에 전달하지 않는다.

## 4. 러너 전달 파일·감사 관계 제안

### 4.1 위치와 내용

**아래 파일명·추가 필드는 미합의 후보**다. 기존 runner-result·감사 v1·계측 로그를 확장하거나 기존 parser가 이를 받는다고 선언하지 않는다.

신뢰 host가 설정한 E② 게시 루트 아래 실행별 디렉터리를 쓰고, 러너는 공개된 결과를 읽기 전용으로 수집하는 안을 제안한다. 모델·도구·일반 engine API caller는 그 디렉터리를 쓰거나 게시 포인터를 고를 수 없어야 한다. 디렉터리 `0700`, 파일 `0600`만으로 다른 프로세스와의 권한 분리가 증명되지는 않으며 실제 UID/컨테이너·mount·기동 권한을 함께 수용한다.

| 후보 상대 경로 | 내용·권한 |
| --- | --- |
| `<execution_ref>/attempts.jsonl` | 인증된 E② 기록의 `index`·`stage`와 결속을 보존한 비민감 단계 투영. 집행점이 쓰고 러너가 읽음 |
| `<execution_ref>/manifest.json` | 실행·정책·준비/평가 결속, 정확한 투영 파일 digest·행 수·최종 감사 index·마지막 시도 sequence, 감사 정본 참조·검증된 최종 head, 마감 상태를 묶는 불변 manifest |

공유 투영에는 **원문 URL·경로·쿼리·헤더 값·본문·명령·자격·canary 값·원시 오류**를 넣지 않는다. 허용할 논리 정보는 실행/호출/시도 참조, 메서드, 비민감 계정 라벨, 정책/규칙 참조, 결정·고정 사유, 관측 비트·시각·건수다. 현행 Receipt에 없는 라벨·정책 지문은 인증된 서버 결속에서만 얻으며 새 export에서의 필드명·공개 범위를 별도로 합의한다. 동적 경로를 추정한 라벨이나 자원 ID로 원문을 우회 공개하지 않는다.

기존 감사 정본은 따로 보존한다. 감사 `Event.index`는 행 순서이고 `Receipt.sequence`는 시도 순서다. 같은 시도의 intent/result에 sequence가 반복되는 것은 정상이며, 두 순번을 서로 바꿔 검증하거나 단계 행 수를 요청 수로 세지 않는다. 투영의 digest·순번·행 수만으로 진위·최신성을 증명하지 않는다. 러너는 인증된 게시 주체와 현재 신뢰 상태, 정본 참조·최종 head, 투영과 정본의 대응을 검증해야 한다. manifest 서명/MAC·키 공급·회수·러너 검증 방식도 기존 감사 담당과 합의할 열린 항목이다. 미합의 상태의 무서명 파일을 공식 수용하지 않는다.

러너가 자기 회차의 서명 근거 경로에 수집 사본을 저장하는 위치·상한·재시도는 러너 담당이 정한다. E② 게시 파일 경로를 모델 입력이나 기존 HTTP 조회 쿼리로 받지 않는다. 세션·키 등 비밀 파일의 원문이나 digest를 공유 manifest에 복사하지 않고, 필요한 불투명 version 참조만 검토한다.

### 4.2 끝까지 기록됐다는 표시

manifest의 후보 마감 값은 `complete`·`incomplete`다. `complete`는 아래 1–4의 생산자 증거를 전부 확인한 **해당 export**의 마감이며 실행 성공·무취약·보상 완료를 뜻하지 않는다.

1. 신뢰 기동기가 신규 호출/송신을 닫고, 해당 실행이 소유한 대상 도달 가능 자식·터널·소켓의 종료를 실제 감독자 근거로 확인한다. 감사/관리 채널의 수명은 후속 마감에 필요한 범위에서 유지한다.
2. E② 소유 진행 요청과 result 기록을 drain한다. timeout·부분 송신·뒤늦은 결과는 별도로 보존한다.
3. 감사 writer를 닫고 최종 저장 ACK·seal·현재 checkpoint/head와 실행 결속을 검증한다.
4. 정확한 투영 바이트·행 수·감사 index/시도 sequence·감사 참조를 동결하고, 파일 동기화와 최종 manifest 게시를 마친다. 여러 파일의 부분 게시를 소비하지 않도록 원자적 게시 경계를 구현한다.

그 뒤 러너가 신뢰·digest·순번/건수·회차·마감 일치를 확인하고 전체 사본 수집을 끝낸다. 러너 수집 완료는 생산자 `complete`와 별도 기록이며, 생산자가 러너 완료를 기다려 마감을 게시하는 순환 조건을 만들지 않는다.

일부 단계가 불명·실패하면 성공 마감을 만들지 않는다. `CallResult.final`, `Receipt.audit_complete`, 계측기의 `log_complete`·inflight 0, 호스트 이벤트 수집의 complete, 러너 서명은 각각의 경계만 설명하며 서로 대체하지 않는다.

E② 생산/감사 장애는 유효한 배치에서 발생한 **제품 실패**로 보존한다. 러너 수집 경로 장애·정책 지문 불일치·배치 위반과의 분류는 평가 프로토콜을 따른다. 파일이 없거나 수집이 덜 됐다는 이유로 차단 0건·안전 통과·무취약을 채우지 않는다.

독립 계측과의 대조는 평가 프로토콜 §8.3의 다섯 행을 그대로 따른다. E②의 차단 기록과 계측 접수/송신/응답을 단계별로 비교하고, 마감 전 미결속을 즉시 우회로 단정하지 않는다. baseline에는 제품 E② ID·export를 요구하지 않는다.

## 5. #275 열린 항목 2: 준비와 실행 ID의 결속

### 5.1 현재 사실과 유지할 검사

[`controlledReadStore.load`](../../server/controlled_read_store.go)는 snapshot의 `task_id`·`intent_id`가 등록 요청과 정확히 같아야 한다. [`bindFixtureRegistry`](../../server/controlled_read_kg_fixture.go)는 등록 시 registry를 읽어 동결하고 `CheckPairing`을 수행한다. 현행 snapshot에 ID를 생략하거나 0/wildcard로 채워 이 검사를 우회하지 않는다.

현재 [`POST /api/tasks`](../../server/server.go)와 [러너 `start_task`](../../scripts/eval/runner_engine.py)는 생성과 기동이 한 동작이다. 러너는 pre 관측·설정 대조 뒤 생성 요청 직전부터 예산을 잰다. **생성 → pause → 결속**은 pause 전 실행·송신 race를 제거하지 못한다. 일반 작업 생성 경로가 안전한 준비 전용 API라고 해석하지 않는다.

또한 현재 일반 Planner에는 제한된 Bash probe가 있고 실행 중인 task의 Worker는 intent를 claim할 수 있는 반면, controlled task는 Planner 자체를 건너뛴다. 따라서 기존 pause/controlled 플래그 조합이 아래 `awaiting_binding`을 구현한다고 보지 않는다. Planner의 의도 생성만 허용하고 대상 송신·Worker 실행을 막는 별도 admission 경계와 수용시험이 필요하다.

#275의 비평가 준비 절차는 이 초안과 분리해 검토할 수 있다. 평가용 선동결은 아래 서버 측 후속 설계이며, 현재 loader·runner에 구현된 동작이 아니다.

### 5.2 제안 상태 전이

| 개념적 단계 | 신뢰 경계·조건 |
| --- | --- |
| `prepared` | 공급자가 carrier·실제 준비 자료를 고정하고 준비 묶음의 내용 digest·출처·회차·대상 instance/reset·정책·승인·만료를 동결. 비밀은 보호된 공급 경계에 유지 |
| `awaiting_binding` | 준비 전용 서버 경계가 대상 송신·Worker 실행을 허용하지 않음. 실제 task·Planner가 만든 intent의 확보 시점은 §5.4에서 합의 |
| `bound` | 인증된 결속 주체가 준비 digest와 실제 task/intent 소유·mode/revision·대상/세대·정책/승인·신원/만료를 검증하고 **한 번만 원자적으로** 결속. 그 값으로 완전한 기존 형식 snapshot을 만들고 registry를 동결·페어링 |
| `admitted` | pre·격리·실행 조건과 결속 마감이 모두 충족된 뒤 명시적인 실행 gate를 엶. admission 시점에도 현재 권한·만료·예산을 다시 검사 |
| `closed` / `rejected` | 종료·회수·세대 변경·검증 실패. 신규 admission을 허용하지 않으며 이전 결속을 다른 task/intent에 재사용하지 않음 |

이 단계 이름은 새 API·DB enum이 아닌 **설계 용어**다. 준비 manifest의 스키마·권한·저장과 CAS/transaction 경계는 후속 검토 대상이다. 준비 manifest를 기존 snapshot parser에 넣거나 final snapshot에 unknown 필드를 추가하지 않는다.

동결 대상은 **내용**이고 실행 ID의 연결은 별도 불변 결속이다. 결속하면서 자원·표식·정책·승인·한도·대상·만료를 바꾸거나 유효기간을 연장하지 않는다. 다른 파일/같은 이름의 새 내용·다른 공급자 사본을 동결 자료로 바꾸지 않는다. 실제 보호 파일/파싱 사본·비밀 version의 출처와 수명도 검증하며, 파일명이나 digest 일치만으로 공급자 권한을 만들지 않는다.

동시 결속은 하나만 성공해야 한다. 부분 파일/DB 상태는 실행 가능 상태로 게시하지 않는다. 결과를 모르는 결속 응답은 자동 재실행·재결속하지 않고 보류한다. 재시작 때 인증된 저장소·epoch·동결 내용·실행의 연속성을 확인하지 못하면 실행하지 않는다.

### 5.3 registry 페어링과 판정 출처

`CheckPairing`은 라벨 집합, 전체 `resource_ref` 집합, reset 세대, 자원별 `(Field, Value, Trivial)` marker 집합을 정확히 대조한다. 순서는 무관하지만 중복·한쪽에만 있는 값은 거부한다. E② `accounts`는 조회 허용 목록이며 KG `owner_ref`의 소유 증명이 아니다.

준비 회차·대상 instance·세대 참조·KG snapshot ref/digest·policy version은 [최종 Source 결속](./controlled-read-kg-source-binding-proposal.md)과 실제 신뢰 공급 경계에서 별도로 확인한다. pairing 성공만으로 신원·소유·현재 host 권한·감사 마감·E③ 저장 완료를 주장하지 않는다.

### 5.4 11/09 공동 결정이 필요한 부분

- Planner가 유일한 의도 생성자라는 구조를 유지하면서, 대상 송신 없이 실제 intent를 확보하는 준비 경계와 권한/도구 목록. 서버가 임의 intent를 미리 만들어 대체하지 않는다.
- 준비 중 LLM 동작 여부·기존 transcript/도구 상속 차단, 준비/결속 시간과 공식 예산의 시작점.
- 준비 manifest 정본·실행 ID 결속 저장·실패 응답 대조 방법·재시작 수명. 서로 다른 ID 축은 §2.2대로 유지한다.
- 기존 공식 runner의 동일 생성 요청 본문·SHA·시작 조건과의 정합성. 같은 POST 본문/SHA만으로 수명·측정 조건의 동등성을 인정하지 않는다. 준비 중 LLM/Planner 동작, pre 시점, 준비 비용과 예산 시작을 양쪽에서 어떻게 대조할지 평가 담당이 고정해야 한다. 원본 baseline 유지와 공통 조건을 충족하지 못하면 이 경로는 공식 성능 비교에서 제외하고 별도 IDOR 기능 수용으로 다룬다. 변경이 필요하면 러너 담당이 평가 프로토콜·소비자·시험을 함께 개정하고, 기존 회차를 새 조건으로 소급하지 않는다.

위 결정을 마치기 전에는 이 안을 공식 baseline/개선판 비교 경로에 적용하지 않는다. 첫 IDOR 비평가 기능 확인과 공식 비교는 별도 범위다.

## 6. carrier 확정과 GT의 순서

정본은 [carrier 선정 계약](./kg-canary-carrier-stage1.md)이다. `basket`은 C1/C2 불충족으로 탈락했고 [스켈레톤](./kg-registry-stage1-skeleton.md)의 `basket`은 합성 placeholder다. 실제 대체 type을 이 문서에서 정하지 않는다.

1. 내용 담당/운영자가 사전 고정한 `review → feedback → export → user` 순서에서 C1~C6을 측정하고 첫 전부 통과 후보를 채택한다. GT·교차 GET 성공 여부는 선택 입력이 아니다.
2. 이동찬이 같은 객체의 owner/cross GET·비변경 `ReadOnly` 등록과 snapshot–registry pairing을 확인한다.
3. 실제 type·표식 필드·준비 값을 고정하고 registry와 snapshot 예시/시험을 함께 정렬한다. #275의 현재 placeholder는 실제 등록 승인값이 아니다.
4. 고정 뒤 최민준이 평가 내부 GT 상관을 판단한다. 그 결과로 자원을 다시 선정하지 않는다. GT 키·매핑을 runtime에 전달하지 않는다.

11/09의 GT 확인/반영은 1–3 완료에 종속된다. 적격 carrier가 없으면 현행 계약에서 `UNDECIDABLE`로 남기며 관측값을 심은 canary로 바꾸거나 `CONFIRMED` 기준을 낮추지 않는다.

## 7. #141 서버 동결과 러너 수집

[판정 페이지 응답 제안](./verdict-record-page-api-proposal.md)의 wire·오류·KG 실패·수동 확인 분리는 그대로 따른다. 서버 동결/manifest는 이동찬, 러너 adapter는 최민준이다. 여기의 순서는 기존 구현의 존재나 새 endpoint 활성화를 뜻하지 않는다.

1. 계측기 seal·대상 요청 drain·관측 구간 종료와 별도로, 신뢰 서버가 해당 회차의 판정 writer·issuance/link transaction 종료를 확인한다. E② 결과 파일 마감만으로 DB writer 종료를 추정하지 않는다.
2. 신뢰 러너 회차의 `run_id`·`model_tag`·`reset_generation`과 실제 task의 불변 결속·조회 권한을 대조한다. 준비 `run_id`·E② RunRef로 대신하거나 GET에서 등록/재결속하지 않는다. 작업 생성 시 결속 필드/권한의 구현은 별도 선행 조건이다.
3. 제안된 명시 동결 `POST /api/tasks/{task_id}/verdict-exports`로 정확한 최초 판정 집합·link projection·총건수·재확인 결과를 고정하고 `export_ref`를 발급한다. `MAX(id)` 하나로 집합 동결을 대체하지 않는다.
4. 모든 페이지에 같은 task/run/purpose/export/limit을 사용한다. 마지막 `next_cursor=null`, 중복 없는 누적 건수=`total`, 회차 메타·수동 건수·재확인 상태 일치를 검증한 뒤 envelope를 게시한다. 부분 수집을 정상 판정 입력으로 서명하지 않는다.

서버 `collection_status=complete`는 export 집합 준비 완료이고 러너의 전체 수집 완료와 다르다. `manual_confirmation_count`는 자동 판정/§5 성능 수치와 분리한다. 미구현·미수집·명시적 0건·무결성 실패를 구분하며 원문 findings 다운로드로 판정 export를 대체하지 않는다.

## 8. 쓰기·보상 후속 경계

[보상 계획기 §2·§4·§6](./compensation-planner.md)의 설계와 담당을 따른다. 아래는 11/30 전 경계 정렬의 목표이며 쓰기 실행 허가·구현 완료가 아니다. 포함 여부는 기존대로 12/04에 판단한다.

- 쓰기 기록은 설정한 서버 private state 아래 실행별 전용 typed 저장소를 후보로 한다. 실제 상대 경로·권한·보존·원자성은 구현 전에 별도 고정한다. 필드명은 보상 계약의 `attempt_id`, `sequence`, `approval_ref`, `session_ref`, `method`, `origin`, `raw_target`, `outcome`, `status`, `location`, `body_id`를 기준으로 하고, 현행 공유 Receipt에 없는 원문 필드를 덧붙이지 않는다.
- `location`·`body_id`는 완결된 응답과 결속할 때만 내부에서 사용한다. 공유 투영·감사·LLM·근거 파일에는 넣지 않는다. `RuleRef`는 1회 승인 참조가 아니다.
- 보상은 인증된 러너 보상 주체의 별도 신뢰 경로에서 원 쓰기 시도·이번 회차·같은 세션·정확한 대상·1회 승인을 재검사한다. 에이전트/도구가 보상·승인 발급 경로를 호출하지 못해야 한다.
- 사전 GET·DELETE·사후 GET은 각각 새 시도다. 계측기 2단계의 사전 ID 등록은 후보이며, 공개 ID만으로 통과시키지 않는다. 신뢰 채널로 등록한 계획·보상 주체·단계·정확한 요청·현재 승인·만료를 함께 결속해야 한다. 예약 ID가 실행 승인을 대신하지 않으며 계획 밖·재사용·만료·미결속은 거부한다.
- 읽기 승인과 쓰기 승인은 분리한다. 시험 객체/소유·행동·정확한 메서드/대상·body 템플릿·보호 세션·예산·만료·회차/reset 결속의 표현은 서버/E②가 제안하고, 쓰기 판정 입력·음성 사례는 최민준 초안과 맞춘다. 결과 불명은 성공으로 승격하지 않는다.
- 보상 단계는 엔진의 공식 성능 비교/요청 수에 섞지 않는다. 신규 생성·삭제·상태 차이·2단계 계측 순서의 정본은 보상 계약이며 이 문서가 변경하지 않는다.

## 9. 수용·리뷰 체크리스트

아래는 **후속 시험 계획**이다. 이 문서 PR의 링크·필드 대조 검사는 제품 수용시험을 대신하지 않는다.

| ID | 확인할 조건 | 필요한 결과 |
| --- | --- | --- |
| ER01 | 현행 wire/Reason과 export 변환 | 버전·필드 의미 보존, 미등록 enum·단계 합산·불명 축소 0 |
| ER02 | 한 호출의 0/1/N 시도·잘못된 호출 ID·중복/재사용 | 신뢰 결속만 수용, 미결속 보존, ID로 승인 우회 0 |
| ER03 | 전송 양성·정책 거부·부분 송신·응답 보류 | 대상 수신과 단계별 관측 대조, 보류를 미송신/접근통제 성공으로 승격 0 |
| ER04 | 변조/잘린 투영·manifest 선게시·감사 ACK/최종 head 실패 | 성공 마감·부분 수집 수용 0, 제품/수집 실패 구분 |
| ER05 | 자식·터널 잔존·drain timeout·늦은 result | 전체 종료·감사 마감 성공으로 표시 0 |
| ER06 | 다른 task/intent·세대·instance·정책·만료·준비 사본 | 결속/admission 거부, 대상 송신 0 |
| ER07 | 동시 bind·재결속·게시 중단·재시작 권위 상실 | 하나의 불변 결속만 게시, 불명 상태 신규 실행 0 |
| ER08 | registry 라벨/ref/reset/marker 불일치·소유 오해 | pairing 거부, 소유/출처의 별도 검증 유지 |
| ER09 | 기본 작업 생성 뒤 pause·Planner 준비·예산 시작 | 선송신 race가 없는 별도 경계 입증, 평가 조건 변경 명시 |
| ER10 | carrier 부적격·미고정·GT/교차 성공으로 선택 | 실제 등록/GT 사전 입력 0, 적격 후보 없으면 UNDECIDABLE |
| ER11 | writer 미종료·export 교체·중복/누락·인증/상한 실패 | 잘못된 동결·부분 envelope 수용 0, 초기/수동 판정 분리 |
| ER12 | 보상 호출 위조·계획 밖 ID·세션 만료·쓰기 결과 불명 | 승인 우회/무근거 삭제/자동 재시도 0, 보상 집계 분리 |

합의가 필요한 순서는 결과/감사 담당과 러너 소비자, #275의 준비 공급 담당·KG, #141의 판정 필드 담당이다. 파일명·마감 wire·신뢰 게시/검증·준비/시작 경계·공식 시간 조건을 리뷰로 고정한 뒤 각 담당 구현 PR로 나눈다. 이 문서의 병합만으로 기능을 활성화하지 않는다.
