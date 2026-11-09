# E② 감사 파일 저장기

> **현재 구현과의 구분 (2026-11-08):** 본문의 2026-11-03 CPD 제안과 publisher 미구현 표기는 당시 단계 기록이다. 현재 개발 구현은 [checkpoint publisher](audit-checkpoint-publisher.md)와 [동기 CPD 서버 연결](e2-durable-audit-server-wiring.md)을 함께 확인한다. 로컬 journal 형식·외부 정본 요구는 유지하며 실제 host 권한 분리·운영 수용 완료를 뜻하지 않는다.

상태: `transmit/auditlog`의 로컬 파일 저장·무결성 검증 구현. [등록 HTTP 전송기](./e2-http-transmitter.md)의 `transmit.Recorder`를 구현한다. PR #30 위 후속 브랜치에서 추가한 별도 패키지이며 기존 전송기·DB·UI 코드는 변경하지 않는다.

## 범위와 사용

운영자가 관리하는 Linux 전용 `0700` 디렉터리, 새 파일명, `Scope`, 비밀 저장소의 32바이트 임의 HMAC 키와 비민감 `KeyRef`로 `Create`한다. 키는 Config에서 복사하고 파일/JSON에 기록하지 않는다. 파일명은 영숫자로 시작하는 단일 구성요소만 받는다. 경로·슬래시·역슬래시를 거부하며 `O_EXCL`로 기존 파일·심볼릭 링크를 덮어쓰지 않는다. 새 파일은 `0600`이고 기존 파일 재개·복구·회전·여러 프로세스의 동일 파일 쓰기는 지원하지 않는다. 디렉터리의 생성·소유자·접근권한 설정은 운영자의 책임이다.

`Create`는 인증된 헤더를 쓰고 파일과 디렉터리를 동기화한 뒤 성공한다. 각 `Record(ctx, event)`는 전체 이벤트 쓰기와 파일 `Sync`가 성공한 뒤에만 nil을 반환한다. 전송기는 기존 계약대로 intent 저장 실패 시 송신하지 않고, 결과 저장 실패 시 이미 발생했을 수 있는 송신 상태를 보존한 채 후속 실행을 막는다. 파일과 디렉터리를 따로 동기화하는 근거는 Linux의 [fsync 동작](https://man7.org/linux/man-pages/man2/fsync.2.html)이다.

```go
journal, err := auditlog.Create(privateDir, newFileName, auditlog.Config{
    Scope: executionScope, KeyRef: keyReference, Key: secretKey,
})
// err를 처리한 뒤 journal을 transmit.Config.Recorder에 지정한다.
// 작업 종료 시 journal.Close() 결과를 처리하고 checkpoint를 별도 보존한다.
```

영수증의 실행·작업·검사·reset·집행 정책 범위가 저장기와 같아야 한다. 계정/세션/자원/승인 ref, 단계·동작·사유 enum·시각·송신 관찰 플래그를 검사하며 임의 메서드·원시 오류 문자열·URL을 이벤트로 받아 기록하지 않는다. 참조는 서버가 발급한 비민감 ID라는 전제가 필요하다. 정규식만으로 ID의 실제 의미나 비밀 포함 여부를 판별하지 않는다.

## 실패와 취소

- 기록은 인스턴스 안에서 순차 처리하며 대기 중 context 취소는 쓰기를 시작하지 않는다.
- 쓰기·부분 쓰기·동기화 실패 이후에는 저장기를 영구 실패 상태로 두고 다시 쓰거나 기존 내용을 잘라 복구하지 않는다. 부분 파일은 운영자 확인을 위해 남는다.
- 일반 파일 IO를 context로 강제 중단하지 않는다. 쓰기 전후/동기화 뒤에 취소를 확인하며, 파일 쓰기 goroutine을 남기고 먼저 반환하지 않는다. 커널의 Write/Sync가 정지하면 반환도 지연될 수 있다. 엄격한 IO 시간 제한은 별도 프로세스/저장 서비스 경계가 필요하다.
- 동기화 뒤 취소된 이벤트는 이미 남았을 수 있다. 이때 오류를 반환하고 후속 기록을 막지만 `Checkpoint()`는 동기화가 확인된 마지막 prefix를 보존한다. intent 기록이 존재한다고 실제 요청이 송신됐다는 뜻은 아니다.
- `Close`는 진행 중 기록과 순차 처리하며 내부 키 사본을 지운다. 이전 실패·파일 닫기 실패를 안전한 오류 코드로 반환하고 삭제하지 않는다. 호출자 원본 키 사본의 수명은 호출자가 관리한다.

## 체인과 검증

버전 1 형식은 개행으로 끝나는 정형 JSON이며 한 줄은 최대 16KiB다. 임의 journal ID·범위·키 ref·생성 시각을 헤더에 인증한다. 이벤트마다 순번·직전 MAC·구조화 영수증을 HMAC-SHA256으로 인증하고 헤더/이벤트 도메인을 분리한다. MAC 비교는 [Go HMAC 문서](https://pkg.go.dev/crypto/hmac@go1.26.3)가 지정한 `hmac.Equal`을 사용한다. 버전 1은 현재 `transmit.Event` 필드 형식에 고정되며 필드 변경 시 이전 형식의 검증 호환성과 버전 변경을 함께 검토해야 한다.

`Checkpoint()`는 journal ID·범위·키 ref·동기화된 최종 순번/MAC를 제공한다. **최신 checkpoint는 반드시 에이전트가 수정할 수 없는 별도 저장소에 보존해야 한다.** `Verify(ctx, reader, key, trustedCheckpoint)`는 스트리밍으로 인증·범위·순번·체인·최종 checkpoint를 검사한다. 키 변경, 내용 변경, 삽입, 순서 변경, 중간 삭제, 불완전한 마지막 줄, 정상 prefix로의 rollback, 다른 journal 교체를 거부한다. 미등록 필드·중복 키·비정형 JSON도 거부한다.

전체 파일 검증이 끝나야 단계별 건수와 checkpoint만 반환하고, 오류 시 부분 요약·원문·경로·원시 OS 오류는 반환하지 않는다. Verify의 reader 또한 임의 blocking Read를 강제로 취소하는 것은 아니다. 검증 중 파일이 변경되지 않는 사본을 호출자가 제공해야 한다.

**최신 checkpoint의 외부 보존이 없으면 정상 끝부분 삭제/전체 파일 교체를 완전히 식별할 수 없다.** 키까지 탈취한 공격자의 재서명도 방어하지 못한다. HMAC 체인·파일 권한만으로 변조불가·권한 분리·외부 seal 저장 완료를 주장하지 않는다. 레코드 무결성 검증은 요청의 승인 적정성·실행 완료·취약점 판정의 정당성을 재판정하지 않는다.

## 통합 경계

남은 작업은 서버의 키 관리·권한 분리, 최신 checkpoint의 외부 내구성 저장, 실제 실행별 저장기 생성/수명 관리, 판정 레코드 연결과 UI다. 이번 구현은 DB 마이그레이션·GUARD/Interceptor·셸/MCP 통신 통제를 추가하지 않는다. 합성 HTTP/파일 테스트로 저장 성공 시 수신 1건, intent 저장 실패 시 수신 0건과 체인 훼손 탐지를 검증한다.

## 최신 checkpoint 외부 게시 — 계약 확정 초안

2026-11-03 · 이동찬 · **팀 리뷰용 제안, 미확정·미구현**

기준은 `dev` `f2c062bd596940027b184a683e69b414cf0cbb0c`다. 현재 [서버 runtime](../../server/controlled_read_runtime.go)의 `controlledCheckpointRecorder`는 각 `Journal.Record` 뒤 [saveCheckpoint](../../server/controlled_read_store.go) 성공까지 기다린다. 파일은 journal과 같은 `stateDir/journals`에 atomic rename + directory Sync로 갱신된다. 이는 로컬 내구성 경계이며 **에이전트와 분리된 외부 최신 정본 저장소가 이미 있다는 뜻은 아니다.** 이 절은 그 남은 경계를 구체화한다. 현재 journal v1·`Checkpoint` 직렬화·오프라인 CLI·서버 동작은 변경하지 않는다. 열린 PR의 코드나 병합을 선행 조건으로 삼지 않는다.

### 팀이 선택할 경계

| ID | 권장안·대안 | 확인 담당 · 수용 근거 |
|---|---|---|
| CPD01 | **분리 저장 서비스에 동기 게시.** header·각 이벤트·최종 마감마다 내구성 ACK를 기다리는 안을 권장한다. 회차 종료 후 운영자가 한 번 복사하는 대안은 수동 보관용이며 실행 중 최신 prefix 보장의 대체가 아니다. | 이동찬(구현), 최민준(무결성·평가 회귀 리뷰): 의도 ACK 전 송신 0, 결과/마감 ACK 실패의 실행 오류 전파, 별도 저장소 장애 주입. |
| CPD02 | **검증된 publisher·reader와 저장소의 권한을 분리.** host가 등록한 writer 참조·실행 범위·KeyRef만 수용하고 모델/일반 API는 등록·head 수정·복구 권한을 받지 않는다. UID/컨테이너/DB 역할의 실제 구성은 배포 담당의 별도 검증 대상이다. | 이동찬(구현 및 운영 책임자 겸임 권장): 모델 경계에서 게시/삭제 자격·HMAC 키 접근 거부, publisher 인증·scope 교체 거부. 같은 UID와 0600만으로 통과 처리하지 않음. |
| CPD03 | **불변 이력+현재 head+멱등 결과를 한 transaction에 저장.** revision CAS로 직렬화하고 store commit의 내구성 확인 뒤 ACK한다. 단순 last-writer-wins 파일 덮어쓰기는 채택하지 않는다. | 이동찬: 같은 journal 동시 게시·응답 유실·저장 재시작·고아 이력/부분 head 시험. 특정 DB 제품·DDL은 이 문서로 확정하지 않음. |
| CPD04 | **키·체인 검증은 신뢰 verifier가 수행.** candidate와 기존 head가 같은 불변 journal의 연속 prefix임을 확인한다. HMAC 키는 요청에서 받지 않고 별도 비밀 저장소의 검증된 KeyRef 매핑으로 선택한다. | 이동찬(구현), 최민준(무결성 리뷰): candidate 전체 MAC·스키마 검증과 기존 prefix 포함 검증, 입력 크기/이벤트 수/시간 상한을 등록 설정으로 필수 지정. 총 상한 값은 최대 합성 실행의 비용을 측정해 확정. |
| CPD05 | **마감 상태와 최신 조회는 별도 정본 metadata.** checkpoint MAC만으로 Close·실행 성공을 추정하지 않는다. store epoch·복원 정책과 읽기 자격을 검증한 조회만 최신 근거로 사용한다. | 이동찬(구현 및 운영 책임자 겸임 권장), 최민준(무결성 리뷰): 마감/불완전 구분, 오래된 조회/ACK 재생, 저장소 복원·유실 시험. E③ 발급/평가 판정은 해당 계약에서 추가 검증. |

**담당 정렬:** [task-mode MD02 검토안(PR #131)](https://github.com/ABC2LAB/ABCDEF2LAB/pull/131)와 같이 12/18 설계검토 범위의 운영 책임자는 PM·통합 담당 **이동찬이 겸임하는 안을 권장**한다. 실제 운영 역할·권한·활성화가 확정된 것은 아니며 배치·키/복원 anchor·보존 책임과 수용 근거의 팀 확인은 남아 있다. 최민준은 무결성·마감 의미·E③ 소비 경계·회귀의 리뷰어이며, 운영 책임자나 publisher/adapter 구현 결정자가 아니다.

**평가 후속:** CPD01의 이벤트별 외부 동기 ACK를 ours에서 켜면 `elapsed_seconds`와 요청 처리 시간에 비용이 생긴다. 활성화 연결 PR에서 외부 게시 on/off를 평가 환경 조건으로 고정하고 프로파일·회차 기록에 남기는 방법을 평가 담당 최민준과 검토해야 한다. 현재 닫힌 평가 schema에 임의 필드를 추가하거나 비교 결과를 이미 측정한 것으로 보지 않는다. 기록 형식·비교 조건 정렬과 비용 측정은 미실행 후속이다.

아래 상세안은 CPD01~05를 채택할 경우의 제안이다. 운영 활성화나 새 네트워크 경로·자격 발급을 허용하는 문서가 아니며, 서비스 transport/인증 방식·보존 기간·상한 값·운영 책임자의 확정은 팀 리뷰에 남는다.

### 정본 레코드와 동기 게시

기존 [`auditlog.Checkpoint`](../../transmit/auditlog/format.go)의 `Version/JournalID/Scope/KeyRef/Sequence/MAC`를 그대로 사용하고, 다음 정보는 **바깥 저장 레코드**에 둔다. journal v1에 임의 필드를 추가하거나 기존 CLI의 checkpoint JSON에 envelope를 섞지 않는다.

| 항목 | 제안 의미 |
|---|---|
| store identity·epoch | 운영자가 검증한 저장소와 복원 세대 참조. caller가 고른 새 epoch로 기존 정본을 초기화하지 않음 |
| journal·실행 결속 | 등록된 publisher 참조와 전체 Scope/KeyRef/JournalID의 불변 결속. 같은 실행 RunRef에 다른 journal을 덧씌우지 않음 |
| revision·previous revision | 저장소가 원자적으로 발급하는 양의 revision과 직전 revision. 시각·파일 mtime 대신 순서 결정에 사용 |
| checkpoint·phase | 현재 인증 prefix와 `open/sealed/incomplete/quarantined`의 저장 상태. phase는 HTTP/오라클 판정 enum이 아님 |
| operation ref·payload | 서버가 발급해 재시도 동안 유지하는 비민감 연산 참조와 불변 요청 내용. 같은 ref·다른 내용은 충돌 |
| receipt ref·published at | 내구성 커밋된 연산 결과의 참조와 저장소 시각. 시각은 진단용이며 정렬/최신성 권한이 아님 |

이력·head·연산 결과는 같은 transaction에서 갱신한다. ACK에는 store identity/epoch, operation/receipt ref, revision, 정확한 checkpoint와 phase가 결속돼야 한다. 임의 JSON이 ACK라는 이유로 신뢰하지 않고 인증된 서비스 응답으로 검증한다. ACK 재조회도 같은 인증·범위 검사를 사용한다. 키·원문 URL/요청/응답/표식 값·OS/DB 원시 오류는 레코드/공유 로그에 넣지 않는다.

1. **등록:** 신뢰 host가 실행 Scope·publisher·KeyRef와 운영 상한을 등록한다. `Journal.Create`가 동기화한 **header-only snapshot과 sequence 0**을 검증한 뒤 실행→journal 결속과 초기 head를 한 번 생성한다. 등록 중복은 같은 불변 내용에만 멱등 성공이며 다른 JournalID/범위/키 ref로 교체하지 않는다. 초기 ACK 전에는 전송기를 실행 가능 상태로 공개하지 않는다. 생성 실패로 생긴 파일/실행은 보존하고 새 RunRef로 자동 재시도하지 않는다.
2. **의도/결과:** journal 이벤트 전체 Write+Sync 후 candidate checkpoint를 게시한다. 호출은 기존 정본 epoch/revision/checkpoint와 같은 operation ref를 유지해 재시도한다. **신뢰 저장소의 ACK 성공 뒤에만 Recorder 성공**을 반환한다. 의도 게시 실패면 실제 송신 0이다. 결과 게시 실패는 이미 송신됐을 수 있으므로 그 불확실성을 유지하며 해당 recorder를 실패 상태로 두고 후속 송신을 막는다.
3. **마감:** 실제 전송기의 `StopAndWait`와 진행 Record 종료 확인 → `Journal.Close` 성공 → 같은 최종 prefix 검증 → `sealed` 전이 ACK 순서다. 마감은 sequence가 그대로여도 별도 revision을 발급한다. Close/이전 Record/게시 실패가 있으면 `sealed` 성공으로 처리하지 않고 `incomplete` 또는 `quarantined`로 남기며 실행 오류를 전파한다. 그 상태 자체도 store 장애로 보존하지 못했다면 로컬 오류 상태만 확인됐다고 보고한다.

`Checkpoint`에는 Close 여부·이전 오류·전송기 drain 증명이 없다. `sealed`는 **신뢰 host의 수명 종료 증언과 검증된 prefix를 결속한 외부 상태**다. MAC 검증만으로 파일 writer가 닫혔거나 실행/취약점 판정이 성공했다고 주장하지 않는다. host의 마감 증언을 인증·검증하는 방법은 CPD02/05의 구현·운영 수용 조건이며 caller의 `closed=true` 문자열만 받지 않는다.

### 연속 prefix 검증·CAS·재시도

| 입력 | 제안 결과 |
|---|---|
| 최초 등록·검증된 header·등록된 실행 슬롯 비어 있음 | revision 1과 sequence 0 생성 |
| 같은 phase/sequence/MAC·같은 결속의 새 중복 게시 | 현재 head와 expected revision이 일치할 때만 head 변경 없는 no-op. 연산 결과는 내구성 보존 |
| sequence 증가·기존 prefix 포함·expected head 일치 | 이력 추가와 head 전진을 원자 커밋 |
| 더 낮은 sequence, 같은 sequence의 다른 MAC, Scope/KeyRef/JournalID 변경 | 충돌 거부, head/이력 삭제·덮어쓰기 0 |
| 다른 epoch/revision, 없는 기존 정본, 기존 prefix를 포함하지 않는 candidate | 차단/충돌. 로컬 파일에서 정본을 재구성해 허용하지 않음 |
| 이미 sealed/incomplete/quarantined인 journal에 새 이벤트/재개 | 거부. 같은 저장 연산의 정확한 재시도 결과 조회만 허용 |

verifier는 caller가 검증 중 바꿀 수 없는 **같은 불변 snapshot**에 대해 candidate 전체의 MAC·범위·스키마·끝부분과 기존 정본 prefix의 Sequence/MAC를 함께 확인한다. 현재 [`auditlog.Verify`](../../transmit/auditlog/verify.go)는 **하나의 expected checkpoint와 전체 입력**만 확인하며, 새 candidate를 넣어 성공했다는 사실만으로 기존 head 포함이 증명되지는 않는다. 후속 adapter는 같은 snapshot에서 기존 순번의 인증된 MAC을 비교하거나, 정확히 그 개행 경계에서 끝나는 불변 prefix도 기존 head로 별도 검증해야 한다. live 파일을 잘라 수정하거나, 기존 head를 넘긴 전체 reader를 그대로 Verify해 prefix 검증을 했다고 보고하지 않는다.

**별도 검증 코어:** [`auditlog.VerifyExtension`](../../transmit/auditlog/extension.go)은 명시적 byte/event/timeout 상한 아래 입력을 한 번 읽어 소유하는 불변 bytes를 만들고, 같은 bytes의 candidate 전체와 기존 head 순번의 정확한 개행 경계 prefix를 각각 `Verify`한다. context가 임의의 차단 Reader를 강제 해제하지는 못한다. header-only head와 정확한 재시도를 지원하며 유효 MAC의 다른 분기·추가 tail·상한 초과·취소·무진행 Reader는 결과 없이 거부한다. 이 순수 검증 코어는 이전 head의 신뢰·publisher 인증·epoch/revision/phase·CAS/보존·외부 snapshot 공급과 키 수명을 구현하지 않는다. 아래 동기 게시/정본 store 활성화 완료로 해석하지 않는다. 실제 공급/소유 경계는 [첫 회차 입력 대장](./first-idor-host-inputs.md)에 구분한다.

**호출·공급 현황:** 통합 기준 dev `88415fff88f1c66c4307ba370c360e6ad338efcc`와 검증 코어
`b3652a180668c2752e1f8fb6130e0c3d298b9496`에서 각각 `git grep -n VerifyExtension -- '*.go'`로
확인한 비테스트 호출처는 0건이다(함수 선언·주석 제외). 이 기준에서 코어는 시험에서만 호출하며,
CPD publisher adapter와 외부 불변 snapshot 공급은 미구현이다. 기존 Recorder/server에 자동 연결하지 않는다.

`MaxExtensionBytes=64 MiB`·`MaxExtensionEvents=40,000`·`MaxExtensionTimeout=2분`은
최대 합성 실행 비용을 측정해 확정한 값이 아닌 **잠정 검증 자원 상한**이다.
64 MiB는 snapshot 입력 바이트의 보수적 상한이고, 40,000은 intent/result 쌍 위에 여유를 둔 독립적인
전체 이벤트 제한이다. 거부 result는 request 예산을 쓰지 않을 수 있어 이 수치를 실행 최대값으로 유도하지 않는다.
2분은 현재 서버 `maxControlledTimeoutMS`와 맞춘 시간 상한이며, 실행 승인/예산을 대신하지 않는다.
연속 빈 읽기 100회 제한은 Go `bufio.maxConsecutiveEmptyReads`의 관례를 따른 무진행 방어이며
진행이 있으면 카운터를 초기화한다.
`TODO(이동찬): CPD04에 따라 최대 합성 실행의 메모리·검증 비용을 측정하고 담당 리뷰로 운영 상한을 확정한다.`

byte 상한은 초과 여부 확인용 한 바이트를 포함해 실제 소비를 `MaxBytes+1`까지 제한한다.
timeout/cancel은 각 Reader 호출 전후와 검증 경계에서 확인하므로, 진행하는 Reader의 후속 호출을 중단한다.
context를 따르지 않고 현재 호출 안에서 막히는 Reader나 정규 파일의 kernel I/O를 강제로 끊지는 못한다.
`n<0` 또는 `n>len(p)`처럼 Reader 계약을 어긴 길이는 panic 대신 고정 `ErrRead`로 거부한다.

연산 ref별 동일 요청은 저장된 같은 결과를 반환하고 같은 ref·다른 요청은 거부한다. 응답 유실/commit 불명확 시 새 journal·operation ref·승인을 만들지 않고 그 ref의 결과와 **현재 head를 별도로** 인증 조회한다. 과거 연산 ACK 재조회는 그 연산의 성공 증명일 뿐 현재 최신 head라는 뜻이 아니다. journal별 게시를 직렬화하고, 동시 CAS 충돌을 자동 overwrite로 해결하지 않는다. 크기/이벤트 수/시간 상한 초과나 검증 취소도 성공으로 처리하지 않는다.

진행 중 `Record`가 실패한 뒤의 `Checkpoint()`는 Sync가 확인된 prefix만 반환할 수 있다. 실제 파일에는 부분 끝줄·그 이상의 불확실한 바이트가 남을 수 있어, 그 checkpoint가 있다는 이유로 전체 파일을 잘라 정상화하거나 sealed로 승격하지 않는다. 원본은 보존하고 불완전 상태를 별도로 남긴다.

### 최신 조회·내보내기·복원

운영자 조회는 신뢰 저장소의 **현재 epoch/head/phase**를 인증 경로로 읽는다. 애플리케이션이 가져온 checkpoint 파일·과거 ACK·같은 쓰기 권한의 백업은 최신 정본 조회를 대신하지 않는다. 기본 내보내기는 `sealed`인 journal만 허용하고, 불완전 자료의 포렌식 출력은 그 상태를 명시해 별도 경로로 다룬다. 내보내는 checkpoint는 현재 타입의 `json.Marshal` JSON으로 제공하고 외부 receipt는 sidecar로 보존한다. [오프라인 검증 CLI](./e2-audit-verification-cli.md)는 기존대로 운영자가 제공한 파일 쌍을 검증하며 최신성/마감 상태를 새로 인증하는 기능이 아니다.

저장소가 유실되거나 epoch 연속성을 확인할 수 없으면 새 게시·정상 마감·최신성 인증을 중단한다. 복구 입력은 운영자가 검증한 이력/head/epoch 근거여야 하며 journal이나 로컬 checkpoint만으로 복원하지 않는다. 보존 이력·head·epoch가 한꺼번에 오래된 백업으로 돌아가도 별도 신뢰 anchor가 없다면 그 rollback을 완전히 탐지할 수 없다는 한계를 명시한다. 복원 세대 발급과 외부 anchor·보존/삭제·키 수명 정책은 운영 책임자 겸임 권장안의 이동찬이 운영 근거를 준비하고 최민준이 무결성·회귀를 리뷰한다. 관련 팀 확인과 수용시험 전에는 활성화하지 않는다.

이 계약은 에이전트가 외부 정본을 수정하지 못하는 범위에서 rollback/교체 탐지를 다룬다. HMAC 키를 가진 신뢰 publisher/verifier 또는 저장소 자체가 침해된 경우까지 방어하지 않으며, 승인 적정성·실제 송신·E③ 판정 정당성을 대신 증명하지 않는다. E③ manifest가 어느 외부 receipt를 소비할지는 해당 저장 계약에서 별도 결속하며, 이 초안의 게시를 선행 PR 병합/구현 완료로 간주하지 않는다.

### 후속 구현 수용시험 계획

아래 CPS01~12는 **미실행 계획**이다. 기존 journal/CLI 합성 시험의 통과 수에 합산하지 않는다. 전용 저장소·합성 journal/key·수신 계측 target을 사용한다.

| ID | 조건 | 필수 수용 결과 |
|---|---|---|
| CPS01 | header 등록·정상 의도/결과·Close·sealed | phase/revision과 전체 Checkpoint 결속, 의도 ACK 전 수신 0, 최종 외부 receipt 조회 가능; 외부 게시 on/off의 평가 프로파일·회차 기록 조건을 최민준과 연결 PR에서 확인(미실행) |
| CPS02 | 같은 journal의 정상 증분·정확한 연산 재시도·ACK 유실 | 이력/head 중복 증가 0, 같은 연산 결과 반환, 현재 head 별도 확인 |
| CPS03 | 낮은 sequence, 같은 sequence/다른 MAC, 같은 실행/다른 JournalID·Scope·KeyRef | 거부, 기존 정본과 실행 결속 불변 |
| CPS04 | 새 candidate 전체 MAC은 유효하지만 기존 head와 다른 체인 분기 | 기존 prefix 포함 검증에서 거부, candidate 검증만 하는 변이 검출 |
| CPS05 | 두 writer의 같은 expected revision CAS·검증 중 snapshot 변경 | 한 전진만 성공/다른 충돌, 변경 가능 snapshot 거부, partial ACK 0 |
| CPS06 | 의도 게시·결과 게시·마감 게시 각각 저장/commit 장애 | 의도 실패 수신 0, 결과 실패의 이미 송신 가능성 유지, 후속 송신·sealed 성공 0 |
| CPS07 | Record 취소/부분 쓰기·Sync 실패 뒤 Checkpoint 반환, Close 실패 | 원본 보존·incomplete/quarantined, 자동 truncate·sealed 승격 0 |
| CPS08 | head/이력/연산 결과 사이 실패 주입·재시작 | 원자성·멱등 결과, 고아 head/이력·commit 전 ACK 0 |
| CPS09 | sealed 뒤 append, 마감 metadata/ACK 재생, incomplete 내보내기 | 새 게시 거부, 과거 ACK를 최신으로 인정 0, 기본 정상 내보내기 거부 |
| CPS10 | 미인증 publisher/reader·다른 실행·요청에 Key/closed=true 주입 | 권한/결속 거부, caller 키·마감 선언만으로 신뢰 0, 원문/비밀 노출 0 |
| CPS11 | store 유실·epoch 불일치·오래된 백업/로컬 cp로 재구성 | 새 게시/마감 차단, 명시 복구 근거 없이 최신성 성공 0 |
| CPS12 | 등록 상한 초과·비정형 JSON·후반 스키마 오류·검증 취소 | head 전진/성공 ACK 0, 고정 오류 범주, 기존 CLI 회귀 유지 |

팀 리뷰는 CPD01~05별 **채택/수정/보류·확인 담당·수용 증거**를 남긴다. 이동찬이 publisher/adapter·저장소 CAS/장애 전파를, 최민준이 무결성·마감 의미와 E③ 소비 경계를 검토하고, 운영 책임자 겸임 권장안의 이동찬이 권한·키/복원 anchor·보존의 확인 근거를 준비하는 안이다. [최민준 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/134#issuecomment-6599817462)의 Ready 허용은 운영 책임·권한 확정이나 CPD01 활성화 승인이 아니다. 팀 확인 후 별도 구현 PR로 진행한다.
