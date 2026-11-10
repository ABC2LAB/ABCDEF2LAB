# E②–러너 연결·준비 결속 계약 초안

2026-11-07 · 이동찬 · **팀 검토용 제안. 새 전달 형식·서버 준비/시작 API는 미구현이며 운영 비활성이다.**

기준 소스는 `dev` `d28f7f3fa2db05f939aff349b3237f256f834e36`다. 이 문서는 E②가 러너에 넘길 비민감 결과와 마감 근거, IDOR 준비 자료의 실행 결속, 판정 export의 호출 순서를 검토하기 위한 서버 측 초안이다. 기존 wire·평가 프로토콜·판정 계약을 자동으로 개정하지 않는다.

병합된 [#275](https://github.com/ABC2LAB/ABCDEF2LAB/pull/275)의 [A/B 준비 절차](./ab-session-prep-stage1.md)를 참조한다. 그 문서의 범위는 `EvalRunID`가 없는 비평가 통합 회차이며, 열린 항목 2의 평가용 결속은 후속 검토안이다. #275 병합을 아래 평가용 서버 경계의 구현·활성화나 세부 계약 확정으로 해석하지 않는다.

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

HTTP 개발 측정의 정책은 현재 [API 개발 프로파일](../evaluation/profiles/official-35b.json)에서 고정하며 값을 코드에 복제하지 않는다. 파일명과 달리 이 파일은 현재 GPU 공식 프로파일이 아니다. 공식 GPU 프로파일은 평가 프로토콜 §6에 따라 비교 시점에 별도 커밋·고정한다. HTTPS 검사·실행별 CA·대상 인증서 검증·검사 실패 거부는 별도 제품 수용을 따른다. HTTP 수용으로 HTTPS 수용을 대신하지 않는다. 무검사 터널·우회 출구·미지원 경로의 처리도 공통 계약의 경로별 수용으로 확인한다.

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

일반 HTTP `e2proxy` 경로의 `X-ABC2LAB-E2-Attempt`는 E②가 발급한 무작위 128비트 소문자 hex 32자를 쓴다. 호출자 값은 신뢰하지 않고 교체하며, 계측기는 유효 ID를 기록한 뒤 대상 전달 전에 제거한다. 형식 유효성은 출처 인증이 아니고, 실행·시도·정책 결속과 중복/재사용은 마감 후 대조한다. ID 누락·오류 행도 계측 총량에서 제거하지 않는다. 등록 GET의 기존 `Receipt.Attempt`/sequence 체계를 이 헤더 계약으로 바꾸지 않는다.

### 2.3 제품 E② 기동 형태 제안

제품 E②는 [가짜 E② overlay](../../deploy/eval/compose.e2-stub.yaml)의 **위치**를 대체하는 별도 `eval-e2` 서비스로 제안한다. stub 바이너리를 제품 구현으로 이름만 바꾸지 않는다. 아래 배치·제어 프로토콜과 실제 제품 entrypoint는 미구현이며, compose·러너 변경은 담당자 구현 PR로 검토한다.

| 준비 항목 | 제안 |
| --- | --- |
| 이미지 | 제품 E② entrypoint를 포함한 소스 빌드 이미지 digest를 회차 전에 고정. 기존 `EVAL_METER_IMAGE`의 `e2-stub` 역할을 사용하지 않음 |
| 망·대상 포트 | `eval-e2`는 `engine_net`·`e2_net`에만 참여. `engine_net`의 대상 별칭 `eval-meter`는 E②가 받고, 실제 계측기는 `e2_net`의 `eval-meter-upstream`으로 분리. 대상 포트는 기존 러너 설정을 사용하고 호스트에 publish하지 않음 |
| UID·권한 | 초기안은 E②에 기존 overlay의 `EVAL_RUNNER_UID/GID`(비root)를 쓰고 engine/도구 UID와 분리. runner 준비/결과 디렉터리의 실제 소유·mount를 대조하고 read-only rootfs·cap_drop·no-new-privileges 적용. host UID가 같아도 runner 서명 키 등 다른 host 경로는 mount하지 않으며 실제 namespace와 파일 접근 음성 시험 필요 |
| 설정·키 | 회차별 runner 소유 `0700` 준비 디렉터리의 `0600` 불변 설정·감사 키를 E②에 읽기 전용 mount. 감사 키·서명 키·Docker socket을 engine/도구에 주지 않음. E②에 러너 서명 키도 주지 않음 |
| 결과 | 별도의 runner 소유 `0700` 디렉터리를 E②의 `/evidence`에 쓰기 가능 mount. 계측기 근거 디렉터리와 분리하며 engine에는 mount하지 않음 |
| runner 제어 | runner와 E②만 접근하는 별도 `0700` 디렉터리의 Unix 제어 socket 제안. ready·현재 head·close 요청/ACK를 인증된 회차에 결속. Docker exec/컨테이너 재기동으로 제어하지 않음 |
| 호출 제어 | 신뢰 engine launcher↔E②의 별도 mTLS 제어 listener 제안. 포트·서버/클라이언트 identity는 설정에 고정하고 공개하지 않음. launcher 자격은 도구 UID/mount namespace에서 읽을 수 없어야 하며, 도구의 데이터 프록시 접점으로 제어 RPC를 받지 않음 |

**2026-11-10 결정(Bash 실행 위치):** 위 표의 호출 제어 행과 이 절·§2.4의 OpenCall/CloseCall(엔진 쪽 launcher)은 채택하지 않는다. Bash는 `eval-e2`의 샌드박스에서 `E2StandaloneBackend.Execute`로 돈다. 시험된 Runner와 호출별 proxy를 그대로 쓰고, 모델이 고른 명령이 엔진 컨테이너에서 돌지 않으며, proxy와 샌드박스 launcher가 같은 PID namespace에 있어 `LivePeer` 결속이 유지되기 때문이다. `eval-e2` 이미지는 엔진 이미지 위에 서비스와 helper만 얹어 도구 계층을 같게 한다([`deploy/eval/e2.Dockerfile`](../../deploy/eval/e2.Dockerfile)). 도구의 바깥 uid는 러너와 같으므로 제어 소켓 보호는 uid가 아니라 샌드박스의 `pivot_root`(새 루트에 소켓·설정 경로가 없음)에 의존하고, 이 음성 시험은 실행 경로를 붙일 때 실제 컨테이너에서 확인한다. 엔진이 `Execute`를 부르는 경로와 그 인증은 아직 없다. 아래 문단은 채택하지 않은 안의 기록이다.

호출 제어는 실제 실행·task/intent·정책에 묶인 OpenCall/CloseCall의 typed 요청으로 `command_ref`와 E② 발급 `call_ref`를 연결한다. 이 이름은 새 버전 제어 프로토콜의 후보이지 기존 registered-read ACK 확장이 아니다. 임의 caller가 두 참조를 선택·재사용하거나 raw 요청·명령을 제어 RPC에 넣지 못하게 한다.

준비 완료 신호는 단순 listen 성공과 구분한다. `service_ready`는 이미지·UID·설정 digest·header mode·회차·감사 키/출력 경로·제어 identity의 일치를 runner가 대조한 서비스 상태이며 아직 대상 송신 권한은 없다. `execution_ready`는 실제 task·양수 intent와 승인·Ledger·정책·감사 기록을 갖춘 `NewExecution`이 준비된 상태다. `call_ready`는 각 실제 `command_ref`에 대해 OpenCall이 발급한 새 `call_ref`까지 결속된 상태다. 앞 단계로 뒤 단계의 권한을 대신하지 않으며, 도구 호출은 매번 해당 call 준비 성공 뒤에만 기동한다.

### 2.4 일반 HTTP 서비스 선기동과 실제 task 결속

runner는 작업 생성 전에 별도 불투명 `reservation_ref`를 준비하되, 이를 `Scope.TaskRef`나 `Budget.Key.TaskRef`로 쓰지 않는다. **TaskRef는 실제 생성된 서버 task ID로 유지**하고 `server task.ID == Scope.TaskRef == Budget.Key.TaskRef`를 검사한다. 평가 run·reservation·실제 task·E② RunRef의 대응은 manifest에 구분해 남긴다. 참조 예약은 승인·예산 발급 권한이 아니고, 실제 task에 대해 검증된 신뢰 발급자의 Ledger grant와 기존 BudgetAuthority/BindRun 검사가 별도로 필요하다.

E② 서비스는 POST 전에 `service_ready`가 될 수 있지만 코어 `NewExecution`의 양수 `IntentID` 검사는 유지한다. 신뢰 서버는 아래 §7의 사전 회차 등록을 실제 task 생성에 1회 소비하고 launch 전에 task 매핑을 게시한다. 이후 Planner의 실제 intent가 생기면 신뢰 launcher가 해당 intent의 권한·결속을 검사해 execution을 준비한다. 추가 intent는 기존 `RegisterIntent` 등 인증된 경계를 통과한 것만 claim/실행 가능하게 하며, 한 intent의 성공으로 다른 intent 전체를 허용하지 않는다. 예시용 양수 intent나 0/wildcard를 미리 넣지 않는다.

각 실제 도구 호출마다 `command_ref`를 확인해 `BeginCallForIntent`에 대응하는 OpenCall을 수행한 뒤 그 호출만 기동한다. 새 task 매핑이 없는 요청·미등록 intent·만료/회수·control 채널 오류는 거부한다. 서버 초기 설정·실제 BudgetAuthority와의 연결·다중 intent의 공유 Ledger 수명은 후속 구현의 수용 조건이다. 서비스 선기동은 task/intent 없는 Execution 생성과 다르므로 기존 TaskRef 의미를 회차 ID로 바꿀 필요가 없다. 실제 task 결속·NewExecution·intent/call 등록 비용은 기존 POST 직전부터의 실행 예산 안에 포함한다.

### 2.5 Bash 밖 도구와 공식 시범 전 동결

OpenCall의 단위는 Bash 전용이 아니라 **실제 지원 도구 호출**이다. 도구를 노출했다는 사실과 그 송신을 E②로 집행했다는 사실을 구분하며, [NE07](./e2-network-enforcement.md#7-수용시험과-활성화-조건)의 도구별 출구 검증을 유지한다.

| 도구/경로 | 공식 일반 HTTP 회차의 연결·제외 조건 |
| --- | --- |
| Bash와 자식·손자 | 호출별 OpenCall·기동·CloseCall과 E② → 계측기 경유를 요구. 자식의 직접 소켓도 별도 출구가 없어야 함 |
| WebFetch | Bash와 같은 OpenCall·호출 참조·E② → 계측기 경로를 요구. redirect·재시도도 새 시도와 현재 gate를 거침. 구현·출구 음성 시험이 없으면 도구를 비활성화하고 선언된 차이로 고정 |
| Search·MCP·브라우저·custom tool | 각각의 실행 위치·대상/외부 송신 경로와 OpenCall 결속을 목록화. 같은 집행 경로와 별도 출구 차단을 입증한 지원 경로만 활성화하고, 나머지는 비활성화. 원격 MCP 서버의 대상 송신을 로컬 프록시 설정만으로 통제됐다고 보지 않음 |

비활성은 프롬프트에서 사용하지 말라고 지시하는 것이 아니라 프로그램의 실제 도구 binding·호출 경로에서 집행한다. 우회 송신 음성 시험과 지원 도구의 양성 대조를 함께 남기며, proxy 환경변수만으로 수용하지 않는다.

공식 시범 전에 도구별 지원/비활성 목록·버전·출구·헤더 차이를 평가 담당과 고정하고 정책 지문과 회차 기록에 포함한다(평가 프로토콜 §2의 선언된 차이). WebFetch가 baseline에 있는데 개선 후에서 빠진 경우도 공개하며, 결과를 본 뒤 도구를 켜거나 빼지 않는다. 미고정·목록 불일치·미검증 경로가 활성화된 구성은 시작을 거부한다. 이 절은 연결·비활성 구현이 완료됐다는 선언이 아니다.

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

기존 감사 정본은 따로 보존한다. 감사 `Event.index`는 행 순서이고 `Receipt.sequence`는 시도 순서다. 같은 시도의 intent/result에 sequence가 반복되는 것은 정상이며, 두 순번을 서로 바꿔 검증하거나 단계 행 수를 요청 수로 세지 않는다. 투영의 digest·순번·행 수만으로 진위·최신성을 증명하지 않는다. 러너는 인증된 게시 주체와 현재 신뢰 상태, 정본 참조·최종 head, 투영과 정본의 대응을 검증해야 한다. 아래 평가용 검증안은 manifest 독립 서명 대신 회차 전용 감사 키·인증된 최신 close ACK·runner 서명으로 결속하는 제안이며, 그 경계가 미구현인 임의 파일은 공식 수용하지 않는다.

러너가 자기 회차의 서명 근거 경로에 수집 사본을 저장하는 위치·상한·재시도는 러너 담당이 정한다. E② 게시 파일 경로를 모델 입력이나 기존 HTTP 조회 쿼리로 받지 않는다. 세션·키 등 비밀 파일의 원문이나 digest를 공유 manifest에 복사하지 않고, 필요한 불투명 version 참조만 검토한다.

러너 소비자가 구현할 **후보 필드 집합**은 다음으로 구체화한다. 아래 행·manifest의 실제 encoder/strict parser는 새 구현이며 현재 runner wire로 주장하지 않는다.

| 산출물 | 후보 필드·검증 |
| --- | --- |
| 단계 행 | 현재 `e2proxy.Event`의 `version`, `scope`, `intent_id`, `call_ref`, `command_ref`, `attempt_id`, `sequence`, `policy_revision`, `index`, `stage`, `decision`, `reason`, `method`, `at`, 관측 비트 `forward_attempted`·`request_written`·`response_received`·`response_complete`·`not_sent`, `audit_complete`, 선택적 `rule_ref`·`status_code`. scope의 키 표기는 기존 Go 타입 직렬화를 보존 |
| manifest 형식 | `format=abc2lab.e2-runner-manifest/v1` 후보. unknown/중복 키·다른 버전·다른 회차·불완전 파일을 거부 |
| 회차/실행 | `run_ref`는 E② `Scope.RunRef`. 별도의 `eval_run_id`, `model_tag`, `reset_generation`, `reservation_ref`, `engine_task_id`, `scope`, `policy_revision`, `policy_fingerprint_sha256`를 runner 선언·서버 1회 결속·검증한 감사 scope와 대조. scope/예산의 TaskRef는 실제 task ID |
| 전달 모드 | `header_mode`와 `evaluation` 아래 [`EvaluationCallerContext`](../../e2proxy/evaluation_credentials.go)의 `run_ref`, `llm_backend`, `target_kind`를 이름 그대로 보존. 일반 모드에서 evaluation 없음과 빈 선언은 다르게 취급 |
| 파일/마감 | `state=complete|incomplete`, `attempts_file`, `attempts_sha256`, `rows`, `last_event_index`, `last_attempt_sequence`, `audit_journal_file`, `audit_checkpoint_file`, `audit_checkpoint_sha256`, `close_request_ref`. 경로는 해당 실행 아래 고정 상대 이름만 허용하고 링크/경로 이동을 거부 |

단계 행의 **합성 예시 한 줄**이다. 실제 인증 journal이나 전송 결과가 아니며, MAC을 포함하는 감사 정본은 별도 보존한다. `scope`의 대문자 키는 기존 `transmit.Scope`의 JSON 형식이다.

```json
{"index":2,"stage":"result","intent_id":22,"version":"e2-http-v1","scope":{"TaskRef":"17","CaseRef":"case-example","ResetRef":"reset-example","PolicyRef":"policy-example","RunRef":"run-example"},"call_ref":"call_00112233445566778899aabbccddeeff","command_ref":"tool-call-example","attempt_id":"00112233445566778899aabbccddeeff","policy_revision":"policy-revision-example","rule_ref":"rule-example","sequence":1,"method":"GET","decision":"allowed","reason":"complete","forward_attempted":true,"request_written":true,"response_received":true,"response_complete":true,"not_sent":false,"audit_complete":true,"status_code":200,"at":"2026-11-07T00:00:00Z"}
```

같은 시도의 intent/result는 `attempt_id`·sequence가 같고 `index`·stage가 다르며 각각의 `at`을 보존한다. 시각이 같거나 시계가 역행해도 단계 시각으로 감사 순번을 재정렬하지 않는다. 시도 전 거절/관리 행의 빈 decision·없는 ID는 시도별 대조에 억지로 넣지 않고 별도 건수로 센다. envelope의 버전·scope·정책 결속은 모든 행에서 대조한다.

`header_mode`와 세 선언값은 E②의 실제 `Snapshot()`/`Policy()`에서 동결한 값으로 내보내고, runner가 기동 입력과 일치하는지 확인해 서명 회차 기록에 보존한다. `evaluation.run_ref == scope.RunRef`를 확인하며 평가 `eval_run_id`를 그 자리에 넣지 않는다. 불일치는 설정 위반으로 시작 거부 또는 기술적 무효다. 단순 설정 파일 복사나 manifest 자기 선언으로 실제 모드 적용을 증명하지 않는다. 이 수집·대조는 러너 담당이 구현한다.

**정책 내용 지문 제안:** `policy_fingerprint_sha256`는 신뢰 E② 생산자가 검증·동결한 **정책 내용 전용 투영**에서 계산한다. `Snapshot()`/`Policy()` 전체를 직렬화하지 않는다. 현재 view에는 origin·경로 prefix·헤더 제약·한도 전체가 없고, 남은 예산·만료 같은 동적 값도 섞이므로 지문 입력을 대신할 수 없다. 새 투영·encoder·지문 노출은 후속 구현이다.

| 지문에 넣을 고정 내용 | 정규화·결속 기준 |
| --- | --- |
| 형식·정책 변환 | `abc2lab.e2-policy-content/v1` 후보와 모드, 프로파일의 정적 경로 템플릿 → prefix 변환 규칙·버전. 등록 GET과 일반 HTTP 모드를 혼합하지 않음 |
| 대상·규칙 | origin의 scheme/host/유효 port, 경로 prefix·매칭 의미, 허용 메서드, 일반 헤더의 비민감 제약, 자격 헤더의 허용 이름·처리 방식. 첫 일치 규칙을 쓰므로 규칙 배열의 평가 순서를 보존 |
| 주소·전송 조건 | 승인된 논리 대상과 IP pin 결속 규칙, TLS 검사·대상 인증서 검증. redirect/retry의 지원 의미는 전달 계약·집행 버전의 고정 프로토콜 값이며 현재 Config의 새 설정 필드가 아님. 회차별 실제 IP·CA identity는 아래 별도 결속으로 확인 |
| 예산·한도 | 실제 승인/기동 정책의 요청 상한과 `Config.Limits`의 실행/호출/동시성·본문 크기·request/call timeout·거부/일시 오류/불명 제한 등 고정값. 남은 예산이나 실행 건수가 아님. v1 후보는 `ExpiresAt`에서 임의로 상대 유효기간을 계산해 넣지 않음 |
| 전달·평가 모드 | `header_mode`, evaluation 유무와 `llm_backend`·`target_kind`. 회차별 `evaluation.run_ref`는 제외 |
| 도구 범위·선언된 차이 | §2.5의 지원/비활성 도구·버전·집행/출구 규칙과 프로토콜 §2의 선언된 차이. 서비스 ready 전 동결 |

canonical 입력은 닫힌 필드·고정 타입의 UTF-8 JSON으로 정의하고 객체 키는 사전순, 숫자/불리언·문자열 escape·단위·기본값 표현을 encoder/parser 계약에서 하나로 고정한다. unknown/중복 키·미정 값은 거부한다. 배열은 동결된 순서를 보존하고, 특히 규칙을 정렬해 우선순위를 바꾸지 않는다. 정규화한 정확한 바이트의 SHA-256을 소문자 hex 64자로 낸다. 원문 URL·경로·헤더 제약을 포함할 수 있는 내부 canonical 입력은 공유 결과에 복사하지 않는다.

`Scope`의 RunRef/TaskRef/CaseRef/ResetRef/PolicyRef, reservation·실제 task·intent·call·attempt 참조, 정책 revision/규칙의 불투명 발급 참조, BudgetRef·잔여량·실시간 카운터, 발급/만료 시각, 감사 index/MAC·키/세션 참조와 자격 원문·그 digest는 내용 지문에서 제외한다. revision 식별자만 제외하며 실제 유효 규칙 내용은 항상 재산출에 반영한다. 한도 값·모드·규칙 의미를 제외해 서로 다른 정책을 같은 지문으로 만들지 않는다. 참조와 절대 만료는 기존 manifest·감사·현재 gate에서 별도로 대조한다.

회차마다 달라지는 IP·실행별 CA·mount 경로를 와일드카드로 허용하거나 실제 검사에서 빼는 것은 아니다. 내용 지문에는 고정된 논리 역할/검증 규칙을 넣고, 실제 origin→IP pin·CA/배치 매핑은 인증된 회차 설정과 망 근거에 별도 결속해 `service_ready` 및 실행 준비 때 검사한다. 고정 내용과 동적 매핑을 구분·검증하지 못한 구성은 공식 수용하지 않는다.

E②는 검증된 서비스 정책으로 산출한 지문을 인증된 `service_ready`에서 제공한다. runner는 설정에 사전 동결한 예상 지문과 **작업 생성 전** 대조하고, 누락·불일치면 POST/실행을 시작하지 않는다. `execution_ready`도 실제 유효 규칙의 순서·메서드·grant 한도·전달 모드로 지문을 재산출해 반환하며, runner는 서비스 ready·동결값과 일치해야 실행을 허용한다. manifest의 같은 지문을 ready 결과·서명 근거와 대조한다.

**예상 지문의 최초 산출·동결:** 운영/평가 담당이 검토한 회차 독립 정책 입력 파일만 읽는 오프라인 지문 도구를 후속 구현한다. E② 생산자와 같은 버전의 입력 검증·정규화·canonical encoder를 사용하되, 실행 서비스·`service_ready`·manifest·회차 결과를 조회하거나 그 값을 예상값으로 복사하지 않는다. 대상 송신·LLM 호출·승인 발급·서비스 기동도 하지 않는다. 같은 encoder를 쓴다는 사실은 검토된 입력의 신뢰를 대신하지 않는다.

운영/평가 담당은 시범 전에 입력의 검토된 버전, encoder/스키마 버전과 오프라인 산출 예상 지문을 확인하고, 공유 가능한 버전 참조·예상 지문을 커밋해 동결한다. 원문 설정·비밀값·회차 자료는 그 커밋에 넣지 않는다. runner는 이 동결값을 설정으로 받아 ready 값과 대조하며, 불일치를 없애려고 실행 중 관측값으로 예상 지문을 자동 갱신하지 않는다. 정책/encoder가 바뀌면 변경 검토와 오프라인 재산출·시범 전 재동결을 거치며 기존 회차를 새 조건으로 소급하지 않는다. 도구 이름·CLI 형식·산출물 검증은 구현 PR에서 고정한다. 현재 도구나 실제 예상값을 생성했다는 뜻은 아니며, 구현·독립 산출 근거가 없으면 이 경로로 공식 회차를 시작하지 않는다.

공식 회차에서 `Restrict`를 포함한 정책 변경 경로는 적용 직전의 새 유효 내용으로 다시 계산·대조하고, 동결값과 다르면 적용과 신규 admission을 거부한다. 서비스 ready의 캐시만으로 변경 뒤 정책을 인증하지 않는다. 이미 다른 내용이 적용된 경우는 프로토콜의 설정 위반으로 기록한다. 승인 만료·회수·정상 예산 소진·종료는 별도의 동적 gate/결과이며 내용 변경으로 바꾸지 않는다. 현재 절대 `ExpiresAt`는 지문 밖에서 해당 승인·회차 결속 및 실제 시각으로 재검사하고, 없는 duration 필드를 추가했다고 주장하지 않는다. 예상 지문 설정·ready/manifest 검증은 러너 담당, 유효 정책 재산출·변경 전 gate는 E② 담당이 구현한다.

**평가용 키/마감 검증 제안:** runner는 회차마다 새 32바이트 감사 키를 만들고 E②에만 읽기 전용 mount하며, 운영 키와 섞지 않는다. runner는 종료 뒤 자체 보호 경로의 키로 [`e2-audit-verify`](./e2-audit-verification-cli.md)의 `--journal-format e2-http-v1 --journal … --checkpoint … --key-file …`를 실행해 종료 코드 0과 완전한 `JournalSummary`를 확인한다. `--timeline`은 일반 HTTP에서 지원하지 않으므로 시도 행 수집기로 사용하지 않는다.

성공 검증의 실제 `JournalCheckpoint` 필드 `Version`·`JournalID`·`Sequence`·`MAC`·`Scope`를 원래 실행과 대조한다. 최종 head는 이 checkpoint의 `(Sequence, MAC)`으로 식별하며, 인증된 runner 제어 채널의 최종 close ACK가 고정한 최신 checkpoint와 같아야 한다. 이 checkpoint에는 `PolicyRevision`·`KeyRef`·`Index`·`Closed` 필드가 없다. 정책 revision·키 version·ACK 전달 성공·호출 종료는 각 원래의 신뢰 경계에서 별도로 확인하며 checkpoint 일치로 대신하지 않는다.

E②가 임의로 제공한 과거 checkpoint와 journal의 일치만으로 최신성을 인정하지 않는다. 투영 행은 검증된 정본 Event와 빠짐없이 대조하고, manifest의 파일 digest·행 수·마감도 검사한다. 이후 runner가 이 검증 결과와 정확한 manifest/파일 digest를 자기 서명 근거에 결속한다.

이 안은 manifest 파일에 독립 HMAC을 새로 요구하는 대신, 회차 전용 키·검증된 정본·인증된 최신 close ACK·engine이 쓰지 못하는 산출물 경계와 runner 서명을 함께 요구한다. 제어 인증/최신 head 고정이 구현되지 않았거나 engine이 키·결과에 접근할 수 있으면 공식 수용하지 않는다. 검증 키를 runner가 보유한다는 사실과 E②의 정책 승인 발급 권한은 별개다.

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

### 4.3 runner 종료 순서와 제어 신호 제안

실제 E② 모드의 정상 종료 후보 순서는 **예산 종료 → runner의 E② close 요청 → E② 신규 admission 닫기·대상 요청/호출 drain·감사 마감·complete 게시 → 계측기 seal/drain → 필요 정착 대기 → post**다. E②가 허용한 진행 요청이 먼저 봉인된 계측기에서 503으로 끊기지 않도록 E②를 먼저 닫는다. 기존 none/stub 러너 동작을 이 문서로 변경하지 않는다.

runner는 §2.3의 전용 제어 socket에 회차·execution·고유 `close_request_ref`를 넣은 typed close 요청을 보낸다. ACK는 같은 결속과 신규 admission 폐쇄 여부를 확인하고, 최종 close ACK/manifest는 그 참조·감사 head와 연결한다. engine/도구의 공개 HTTP나 모델 출력으로 close·complete를 선언하지 않는다. ACK 대기는 설정한 별도 유한 시한이며 메트릭의 실행 예산과 구분해 기록한다.

시한 초과·연결/ACK 유실·`incomplete`이면 원인과 미완료를 보존하고 성공 마감을 만들지 않는다. runner는 추가 실행을 허용하지 않고 supervisor에 admission 폐쇄/종료를 요구하며, 무한히 E② 마감을 기다리지 않고 계측기 seal/drain과 실패 시 post 수집으로 넘어간다. 취소·늦은 응답·부분 송신을 보존하며 독립 격리/관측 요건을 충족한 근거만 사용한다.

**정상 설정·환경에서의 E② 집행점 장애·감사 불명은 제품 실패인 유효 회차다.** runner는 실패 시각·사유·관측 한계와 E② 결과의 미완료/부재를 남기고 **회차 기록에 서명한다**. 생산자 manifest를 받지 못했으면 그 부재를 기록하며 `complete`나 가짜 성공 manifest를 만들어 채우지 않는다. E② 통제 주장 칸은 보류하고 근거가 모자란 판정 칸은 판단불가로 두며, 제품 실패 회차 수를 별도로 집계한다. 서명은 실패 기록의 출처를 보존하는 것이지 안전한 마감·실행 성공·무취약을 인증하는 것이 아니다. 이 회차를 분모에서 빼거나 미수집 지표를 0·상한 시간으로 임의로 채우지 않는다.

러너 수집 경로 장애·정책 지문 불일치·배치/망 격리 위반은 기존대로 시작 거부 또는 기술적 무효다. E② 자체 장애로 요약·manifest가 없는 경우를 수집 경로 장애로 바꾸지 않는다. 사전 고정한 외부 환경 원인(tmpfs 상한·호스트 운영 조건·Docker 데몬 신원 변경)이 확인되면 기술적 무효가 우선한다. 무효 회차도 사유와 남은 근거를 보존하며, 분류·집계와 사유 코드의 정본은 평가 프로토콜 §2·러너 안내로 유지한다.

## 5. #275 열린 항목 2: 준비와 실행 ID의 결속

### 5.1 현재 사실과 유지할 검사

[`controlledReadStore.load`](../../server/controlled_read_store.go)는 snapshot의 `task_id`·`intent_id`가 등록 요청과 정확히 같아야 한다. [`bindFixtureRegistry`](../../server/controlled_read_kg_fixture.go)는 등록 시 registry를 읽어 동결하고 `CheckPairing`을 수행한다. 현행 snapshot에 ID를 생략하거나 0/wildcard로 채워 이 검사를 우회하지 않는다.

현재 [`POST /api/tasks`](../../server/server.go)와 [러너 `start_task`](../../scripts/eval/runner_engine.py)는 생성과 기동이 한 동작이다. 러너는 pre 관측·설정 대조 뒤 생성 요청 직전부터 예산을 잰다. **생성 → pause → 결속**은 pause 전 실행·송신 race를 제거하지 못한다. 일반 작업 생성 경로가 안전한 준비 전용 API라고 해석하지 않는다.

또한 현재 일반 Planner에는 제한된 Bash probe가 있고 실행 중인 task의 Worker는 intent를 claim할 수 있는 반면, controlled task는 Planner 자체를 건너뛴다. 따라서 기존 pause/controlled 플래그 조합이 아래 `awaiting_binding`을 구현한다고 보지 않는다. Planner의 의도 생성만 허용하고 대상 송신·Worker 실행을 막는 별도 admission 경계와 수용시험이 필요하다.

#275의 비평가 준비 절차는 이 초안과 분리해 소비한다. [평가 담당 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/278#pullrequestreview-5802842983)에 따라 **현재 공식 비교는 아래 IDOR 준비/결속 경로를 쓰지 않는다.** 현행 프로파일 fixture는 익명이며 첫 비교는 IDOR 개선 효과를 주장하지 않는다. IDOR 기능 수용은 #275의 비평가 경로로 진행하고, 아래 상태 전이는 12/18 필수 구현이나 공식 비교 선행 조건으로 추가하지 않는다. 일반 HTTP 서비스의 선기동/실제 task 매핑(§2.3–§2.4)은 별도 문제다.

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
2. 신뢰 러너 회차의 `run_id`·`model_tag`·`reset_generation`과 실제 task의 불변 결속·조회 권한을 대조한다. 준비 `run_id`·E② RunRef로 대신하거나 GET에서 등록/재결속하지 않는다. 아래 사전 신뢰 등록은 후속 서버 구현이며 기존 `POST /api/tasks` 본문에 회차별 필드를 추가하지 않는다.
3. 제안된 명시 동결 `POST /api/tasks/{task_id}/verdict-exports`로 정확한 최초 판정 집합·link projection·총건수·재확인 결과를 고정하고 `export_ref`를 발급한다. `MAX(id)` 하나로 집합 동결을 대체하지 않는다.
4. 모든 페이지에 같은 task/run/purpose/export/limit을 사용한다. 마지막 `next_cursor=null`, 중복 없는 누적 건수=`total`, 회차 메타·수동 건수·재확인 상태 일치를 검증한 뒤 envelope를 게시한다. 부분 수집을 정상 판정 입력으로 서명하지 않는다.

서버 `collection_status=complete`는 export 집합 준비 완료이고 러너의 전체 수집 완료와 다르다. `manual_confirmation_count`는 자동 판정/§5 성능 수치와 분리한다. 미구현·미수집·명시적 0건·무결성 실패를 구분하며 원문 findings 다운로드로 판정 export를 대체하지 않는다.

**사전 회차 등록안:** compose 기동 뒤 작업 생성 전에, 개선 후 서버에 인증된 runner 전용 경로로 `run_id`·`model_tag`·`reset_generation`과 reservation_ref·정책 결속을 한 번 등록한다. 새 API/IPC의 위치·인증은 구현 검토 대상이며 일반 task 생성 본문·쿼리·모델 도구로 등록하지 않는다. 공개 참조만으로 등록 권한을 만들지 않는다.

서버는 인증된 runner identity·서버 인스턴스·만료와 별도 비밀 단회 capability에 묶인 **다음 한 task**에서 이 등록을 transaction/CAS로 소비하고, 실제 task ID와의 불변 대응을 `launchTask` 전에 확립한다. runner는 POST 직전에 새로 로그인하므로 JWT 사용자 이름만으로 연결을 추정하지 않는다. capability는 기존 본문 밖의 보호 제어 문맥으로 전달하며 값은 모델·로그·공유 manifest에 넣지 않는다. 해당 전달 경계/인증이 구현되기 전에는 이 안을 활성화하지 않는다. 회차마다 DB가 새것이라는 조건만으로 다른 caller의 선점·동시 생성을 허용하지 않는다. 두 번째 생성/다른 caller·미등록·만료·세대 불일치·결속 저장 불명은 실행하지 않는다. 재시작은 검증된 저장 연속성이 없으면 대기 등록을 폐기하고 다시 인증받는다.

baseline·개선 후 모두 기존 여섯 task 필드의 같은 정규 본문과 `task_request_sha256`을 유지한다. 사전 등록의 비민감 값·인증된 소비 결과·설정 digest는 별도 runner 서명 근거에 넣는다. 이 서버 경계와 수집기는 미구현이며, POST 본문을 유지해도 실제 기동·계측·시간 조건의 검증은 생략하지 않는다.

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
| ER13 | 동적 ID/시각/잔여량만 변경, 고정 내용·규칙 순서/Restrict 변경, ready 지문 누락/불일치 | 전자는 같은 내용 지문과 별도 회차 결속 유지, 후자는 재산출·시작/변경 전 거부. 규칙 정렬·이전 ready 캐시로 변경을 숨기는 수용 0 |
| ER14 | WebFetch 우회·Search/MCP 등 별도 출구·지원 목록 불일치 | 지원 경로의 OpenCall/계측 대조, 미지원의 실제 비활성·출구 차단과 선언된 차이. 미고정 구성의 시작 0 |
| ER15 | E② 장애/ACK 불명과 러너 수집 실패·외부 환경 위반을 각각 주입 | 내부 장애는 서명된 유효 제품 실패·E② 미완료·근거 부족 판단불가, 수집/설정/외부 환경 위반은 기술적 무효. 유효 제품 실패의 분모 제외·성공 마감 위조 0 |
| ER16 | 동일 정책/encoder의 오프라인 산출, 정책/encoder 변경, ready 값으로 예상 지문 덮어쓰기 | 같은 입력의 재현과 검토된 버전·커밋 동결, 불일치 시작 거부. 실행 서비스 의존·자동 자기 일치 갱신·실제 송신/발급 0 |

합의가 필요한 순서는 결과/감사 담당과 러너 소비자, #275의 준비 공급 담당·KG, #141의 판정 필드 담당이다. 파일명·마감 wire·신뢰 게시/검증·준비/시작 경계·공식 시간 조건을 리뷰로 고정한 뒤 각 담당 구현 PR로 나눈다. 이 문서의 병합만으로 기능을 활성화하지 않는다.
