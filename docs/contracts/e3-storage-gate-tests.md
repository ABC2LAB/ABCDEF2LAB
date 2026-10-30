# E③ 저장 게이트 — 회귀 시험 명세

상태: **아래 전체 finding 저장/통합 시험 58건은 계획이다. 별도 모드 코어 단위 시험과 DB reader/등록 통합 시험을 부분 구현했다.**
연결: [저장 계약 초안](./e3-storage-gate.md) · [오라클 규칙](./e3-oracle.md) · [판정 레코드](./verdict-record.md)

부분 진행: 별도 [PR #43](https://github.com/ABC2LAB/ABCDEF2LAB/pull/43)의 `taskmode/` 단위 시험과 DB reader/단방향 등록에서 E3G24~28의 mode 판별·정본 등록/조회·경합·실패 기준 일부를 검증했다. 아래 58건은 전체 finding 저장 경계의 명세이며 그 시험이 완료된 것은 아니다. 실제 marker 이관·Engine/RecordFindingTx 연결은 아직 없다.

## 1. fixture와 관측값

임의의 소유 synthetic fixture와 비민감 비교 신호만 사용한다. 실제 계정/키/응답/스캔 출력은 읽지 않는다. positive fixture는 같은 task·intent·execution·case·resource·reset·승인·KG snapshot·계정/세션·근거 revision에 속하는 owner 정상 대조와 cross 응답, 완결된 영수증/감사, 서버 발급 `CONFIRMED` 판정을 가진다.

각 시험은 문자열 verdict 외에 다음 **DB 변화량/수신 건수**를 검증한다.

- `N`: 새 finding 노드 수. `kind='finding'`에 한정하고 fact를 제외.
- `F`: 새 standalone finding 수. triage confirmed 수와 따로 셈.
- `L`: 새 정본 판정→finding 연결 수. 불변 판정 내용은 수정되지 않아야 함.
- `CN`: 새 confirmed finding 노드 수.
- `CT`: 새 triage confirmed 행 수. 수동 출처를 E③ 결과에 합산하지 않음.
- `EV`: 정본 issuance/판정과 같은 근거에 유효하게 연결된 E③ 검증 finding 수.
- `R`: 저장 동작 중 target에 도달한 요청 수. 모든 저장 시험에서 **0**.
- 기존 행·유효 연결·triage가 보존/변경되었는지, 재시도 반환 ID가 같은지 확인.

신규 정상 저장은 `N=1,F=1,L=1,CN=1,CT=0,EV=1,R=0`이다(triage 초기 pending). 아래 표의 `N/F/L/EV=0` 거부는 CN/CT 증가도 0이라는 뜻이며 이미 보존한 비확정 판정 레코드는 삭제하지 않는다. 변경/재검증 시험은 기존 행의 상태도 별도로 assert한다. 사람이 수동 triage하는 시험에서는 CT만 변할 수 있고 CN/EV는 변하지 않는다. notification은 기존 best-effort 정책을 유지하며 필수 E② 감사의 성공 지표로 쓰지 않는다.

## 2. 계획된 사례

| ID | 조건/호출 | 필수 기대 결과 |
|---|---|---|
| E3G01 | 정상 발급 CONFIRMED, 모든 연결 일치 | N/F/L/CN/EV 각 1, CT=0, 동일 판정 참조, R=0 |
| E3G02 | confirmed 문자열이지만 세 체크/필수 facts가 불일치 | 거부, 서버 발급 무결성 오류, N/F/L/EV=0 |
| E3G03 | SUSPECTED | 판정 보존, N/F/L/EV=0 |
| E3G04 | UNDECIDABLE | 판정/부족 사유 보존, N/F/L/EV=0 |
| E3G05 | ALLOWED, B만 검증·owner/facts 부족 | 기존 oracle 의미 유지, 확정/fixed/접근 성공으로 승격 0 |
| E3G06 | zero/알 수 없는 판정 enum | 명시적 거부, N/F/L/EV=0 |
| E3G07 | 판정 참조 없음 또는 존재하지 않는 ID | record_missing, N/F/L/EV=0 |
| E3G08 | caller가 만든 결과 JSON/ID·발급자/내용 변조 | 신뢰 정본으로 인정하지 않음, N/F/L/EV=0 |
| E3G09 | 다른 task의 실제 판정 ID | 거부, 타 작업 행 변화 0 |
| E3G10 | 같은 task지만 intent/exploration 불일치 | 거부, N/F/L/EV=0 |
| E3G11 | 다른 execution, 또는 평가 run_id를 execution으로 대입 | 거부, N/F/L/EV=0 |
| E3G12 | case/resource 전체 참조 불일치, 부분 ID만 일치 | 거부, N/F/L/EV=0 |
| E3G13 | 계정 바꿈/순서 바꿈·세션 참조/검증 버전 불일치 | 거부, N/F/L/EV=0 |
| E3G14 | 실행 승인과 KG policy version 혼동/불일치 | 거부, N/F/L/EV=0 |
| E3G15 | owner/cross/판정의 reset 세대가 다름 | 거부, N/F/L/EV=0 |
| E3G16 | 근거 revision/digest 또는 finding evidence_version 연결 불일치 | 거부, 이전 판정 내용 불변 |
| E3G17 | owner 정상 대조나 cross 관찰/영수증이 없음 | evidence_incomplete, N/F/L/EV=0 |
| E3G18 | 승인/intent만 기록, 또는 blocked 요청 | 송신/응답 근거로 인정하지 않음, N/F/L/EV=0 |
| E3G19 | 부분 송신·응답, send_unknown, 응답 미분류 | 성공/확정 승격 0 |
| E3G20 | 필수 result 감사/close/checkpoint 실패 | 확정 0, 오류 노출. 저장된 판정/감사 성공으로 보고하지 않음 |
| E3G21 | HTTP 200 + ID 반사/공통 화면/표식 비교 미확인 | N/F/L/EV=0, HTTP 200만으로 확정 금지 |
| E3G22 | 관찰 시각에는 유효, 저장 시점에는 정책 만료 | 동일 완료 실행·근거라면 저장 시각만으로 거부하지 않음 |
| E3G23 | 관찰 전/관찰 시점에 정책 무효 또는 만료 | N/F/L/EV=0 |
| E3G24 | DB 모드 metadata 없음/unknown | legacy로 취급하지 않음, N/F/L/EV=0 |
| E3G25 | mode reader/DB 오류 | store_unavailable, fallback/확정 0 |
| E3G26 | 서버가 명시적으로 분류한 legacy 작업 | 기존 legacy 저장 회귀 유지. oracle 검증 완료로 표시하지 않음 |
| E3G27 | 파일 marker만 있음·DB fence만 커밋 후 파일 실패·양쪽 모순 | 모두 controlled/unknown으로 유지, legacy 확정 우회 0 |
| E3G28 | 통제 등록과 저장 경합 | task 잠금으로 선후관계 확정. fence 뒤 저장은 반드시 게이트 적용 |
| E3G29 | recorder nil/실패 + 빈 traffic refs | 통제 작업의 기존 fallback 성공 0 |
| E3G30 | 도구를 거치지 않고 RecordFindingTx 직접 호출 | 같은 mode/판정 검증, 미검증 N/F/L/EV=0 |
| E3G31 | AddNode(finding, confirmed) 직접 호출 | 판정 없는 통제 finding 노드 생성 0 |
| E3G32 | AddFinding 직접 호출 후 pending→confirmed | 통제 직접 생성/무근거 승격 차단, 새 유효 확정 0 |
| E3G33 | 출처 없는 SetFindingStatus/WithNotify/Tx 직접 confirmed 승격 | controlled bare setter 거부, E③ 검증으로 승격 0 |
| E3G34 | 기존 finding의 SetNodeState(confirmed), 일반 갱신 우회 | 미검증 confirmed 승격 0; 호출 가능한 kind 변경 경로도 조사 |
| E3G35 | task_id=0, node/task 연결 삭제·바꿈, task 삭제 후 잔존 finding | 출처 세탁 금지, 모드/검증 출처 상실 시 새 유효 확정 0 |
| E3G36 | record_fact의 AddNode(fact, confirmed) | fact 1, finding N/F/L/EV=0. 일반 관찰을 취약점으로 세지 않음 |
| E3G37 | 동일 저장 연산을 순차 2회 재시도 | 첫 결과 ID 그대로, 누적 N/F/L/CN/EV 각 1, CT=0 |
| E3G38 | 동일 연산 동시 20회 | 최대 3회 bounded retry 후 20개 성공 응답의 ID 동일, 최종 N/F/L/CN/EV 각 1, CT=0 |
| E3G39 | 동일 키+다른 불변 입력/이미 다른 finding에 연결된 판정 | save_conflict, 두 번째 finding/연결 생성 0 |
| E3G40 | node/finding/link/근거 연결 각각 실패 주입 | 전체 rollback, 고아 행/성공 반환 0. 판정 발급 실패도 finding 0 |
| E3G41 | 확정 뒤 Bind/EditFindingTraffic/evidence 직접 수정 | 초기 범위는 변경 거부, 근거 버전·현재 연결 불변. stale 근거 확정 0 |
| E3G42 | 같은 finding의 정상 새 retest CONFIRMED | reproduced + 해당 판정 연결, 새 finding N/F=0 |
| E3G43 | retest ALLOWED/SUSPECTED/UNDECIDABLE/차단을 fixed로 요청 | fixed/confirmed 승격 0, 의미에 맞는 inconclusive/오류 |
| E3G44 | staged fixed 뒤 stopped/failed, 동일 retest 저장 재시도 | triage fixed 변경 0, 결론 덮어쓰기/새 송신 0 |
| E3G45 | 판정 저장·finding 저장 재시도 중 네트워크 감시 | Verify/VerifyRead/Send 호출 0, R=0 |
| E3G46 | 정상/거부/DB 오류 결과의 JSON·모델 응답·export | 비밀 원문 0, enum/필드명/ref만. N·triage 상태·검사 결과 혼동 0 |
| E3G47 | controlled archive/restore, mode/provenance 없는 구버전 archive | 초기 범위는 mutation 전 거부/격리, 부분 복원 0. explicit_legacy archive 회귀는 유지 |
| E3G48 | E③-linked finding의 vulnclass를 일반 PATCH/직접 setter로 변경 | 초기 변경 거부, 판정·유형·digest 불변. 기존 판정의 다른 유형 재사용 0 |
| E3G49 | 최초 저장 ↔ status confirmed 교차 transaction | 잠금 순서 유지, deadlock/timeout·중복/고아 link 0 |
| E3G50 | evidence bind/edit ↔ retest finish 교차 transaction | 안정적 거부/완료, stale 근거 확정·부분 triage 변경 0 |
| E3G51 | task archive ↔ 판정/finding link 교차 transaction | controlled archive 거부/fence 검증과 저장 선후관계 명확, 고아/부분 snapshot 0 |
| E3G52 | finding delete ↔ retest finish 교차 transaction | 잠금 순서 유지, 보존 미구현일 때 controlled 삭제 거부, 삭제 뒤 새 link/triage 변경 0, deadlock/timeout 0 |
| E3G53 | owner/cross 사이 reader가 다른 KG version 또는 동일 version/다른 digest 반환 | snapshot 혼합 거부, N/F/L/EV=0 |
| E3G54 | 관찰 완료 뒤 판정 발급 전에 최신 KG 변경 | 동결 snapshot ID/version/digest로 역사적 판정, 최신 상태를 조용히 대입하지 않음 |
| E3G55 | 인증된 운영자가 기존 finding을 수동 confirmed로 triage | source=human·actor/reason 보존, CT만 증가 가능, CN/EV·평가 oracle 결과 변화 0 |
| E3G56 | 모델/일반 호출자가 source=human·actor 문자열만 주입 | 수동 권한으로 인정하지 않음, CT/EV 증가 0 |
| E3G57 | task/node FK가 NULL이 된 잔존 finding의 출처 조회 | immutable provenance/mode 유지, 자동 legacy 재분류 0. 보존 미구현이면 사전 삭제 거부 |
| E3G58 | issuance/link 저장 재시도와 finding_id 조회/export | 판정/issuance 본문 불변, finding_id는 link projection, 중복 발급/두 번째 finding 연결 0 |

표의 조건별 나열은 구현 시 각각 subtest로 나눈다. 위 58건은 명세 행 수이며 실행된 Go 테스트/최하위 사례 수가 아니다. E3G49~52는 barrier로 두 transaction을 교차 진행하고 전용 timeout으로 종료를 확인한다. 작업 전제에 따라 둘 다 선형적으로 완료할 수 있으면 그 결과도 허용하되, 최종 불변조건 위반은 0이어야 한다. E3G38의 3회는 시험의 재시도 상한이며 제품 설정 기본값이 아니다.

## 3. 시험 계층과 실행 순서

1. **순수 검증 단위 시험:** 불변 binding·enum·발급자/정본 조회의 음성 사례, 관찰 시각 정책 유효성. fake reader는 DB의 원자성/발급 권한을 입증하지 않는다.
2. **격리 PostgreSQL 통합:** 실제 RecordFindingTx 및 일반 삽입/갱신, 유일 제약, task/finding 잠금, 동시 20회, 실패 주입·rollback. 실행 전 전용 DB를 확인하고 팀 공유 DB에 파괴적인 fixture 정리를 하지 않는다. DB 미설정으로 skip한 것은 통과 근거에서 제외한다.
3. **소유 synthetic 통합:** 실제 등록 전송/비민감 projection→판정 발급→최초 저장, retest 연결. 먼저 수행한 owner/cross 요청과 저장 단계의 R=0을 분리 집계. 대상 앱 특화 경로를 제품 코드에 하드코딩하지 않는다.
4. **관측·회귀:** 일반 legacy/fact·인증된 human triage 유지, 노드·triage·E③ 검증 세 축과 export 검사. 필수 감사 실패와 notification 실패는 다른 의미로 시험한다.

전체 저장 게이트 구현 뒤 범위에 맞춰 `gofmt`, 관련 `go test`, `go vet`, 소스 build, 동시성 경로의 `-race`를 실행한다. 별도 PR #43에서 `taskmode/` 단위 검증과 DB reader·등록 통합 시험을 수행했지만, 위 58개 finding 저장 시험을 완료로 기록하지 않는다. 문서에서는 링크·실제 심볼/라인·사례 ID 유일성도 확인한다.
