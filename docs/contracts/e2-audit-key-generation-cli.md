# E② 감사 키 생성 CLI

상태: 독립 Linux CLI 구현. 기존 감사 저장기·오프라인 검증기·서버가 사용하는 **32바이트 raw 바이너리 HMAC 키**를 생성한다. 서버 설정 등록·키 회전·KMS·체크포인트 보존·OS 권한 분리를 대신하지 않는다.

## 실행

운영자가 레포 밖에 준비한 전용 `0700` 디렉터리와 아직 존재하지 않는 파일 경로를 지정한다.

```sh
go run ./cmd/e2-audit-keygen --key-file /srv/e2-secrets/audit.key
```

- 성공 stdout은 `audit_key_created` 한 줄, stderr는 비어 있다. 키 값·hex/base64·경로·원시 OS 오류는 출력하지 않는다. 키를 인자로 받거나 stdout으로 내보내는 모드는 없다.
- `--key-file` 이름은 영숫자로 시작하는 최대128바이트의 영숫자·`_`·`.`·`-` 단일 구성요소다. 상대 경로도 가능하지만 실제 부모 디렉터리가 아래 조건을 만족해야 한다. stdout 별칭 `-`와 경로 끝 슬래시는 거부한다.
- 부모 디렉터리는 이미 존재하고 현재 유효 UID 소유의 정확한 `0700`이어야 한다. setuid/setgid/sticky 비트도 허용하지 않는다. CLI가 디렉터리를 자동 생성하거나 기존 권한·소유자를 변경하지 않는다.
- 마지막 부모 구성요소의 심볼릭 링크를 거부하고 연 디렉터리 fd의 소유자·권한을 검사한다. 같은 fd에 상대적인 배타 생성으로 경로 재조회 경쟁을 줄인다. **그보다 상위 경로·마운트의 신뢰, 다른 동일 UID 프로세스의 접근, ACL·관리자 권한·실제 격리는 운영자 책임**이다. 모든 조상 링크를 거부한다는 의미는 아니다.

## 생성과 성공 경계

1. 문법·취소를 확인하고 `crypto/rand.Reader`에서 `auditlog.KeyBytes`만큼 전체 난수를 확보한다. 난수 확보 실패나 사전 취소 시 파일을 만들지 않는다.
2. 기존 부모 fd에서 `O_CREAT|O_EXCL|O_NOFOLLOW`로 새 파일을 연다. 기존 일반 파일·심볼릭 링크·FIFO·디렉터리는 거부하며 내용을 읽거나 덮어쓰지 않는다.
3. 새 파일 fd에만 `0600`을 적용하고 일반 파일·현재 UID 소유·정확한 모드를 재확인한다. 엄격한 umask도 기존 검증기가 요구하는 정확한 `0600`으로 맞춘다. 기존 파일에는 chmod하지 않는다.
4. 32바이트 전체 쓰기 → 파일 fsync → 파일 close → 디렉터리 fsync → 디렉터리 close를 모두 마친 뒤 성공을 출력한다. 일반 파일 I/O를 취소 시 백그라운드에 버리지 않는다.
5. 임시 키 버퍼는 반환 전에 clear한다. 프로세스·OS의 모든 복사본에 대한 물리 메모리 삭제 보장은 아니다.

**생성 시작 이후 저장/취소/출력 오류가 나면 빈 파일·부분 파일·완성된 키가 남을 수 있다.** 자동 삭제·재개·수정·덮어쓰기하지 않는다. 성공 메시지를 못 받았다는 이유로 새 키로 교체하면 기존 journal 검증 키를 잃을 수 있다. 운영자는 결과와 사용 여부를 확인하고 별도의 새 파일명을 선택한다. 같은 경로의 재시도는 존재 오류로 끝난다.

## 종료 코드와 고정 사유

| 종료 | 의미 / 출력 |
|---|---|
| 0 | 생성 완료, 또는 `--help`/`-h` 사용법 출력 |
| 2 | 인자·파일명 문법 오류. stderr에 고정 usage만 출력 |
| 1 | `audit_key_platform_unsupported`: Linux 외 플랫폼 |
| 1 | `audit_key_input_invalid`: 잘못된 내부 입력 |
| 1 | `audit_key_entropy_unavailable`: 전체 난수 확보 실패 |
| 1 | `audit_key_private_directory_required`: 부모 열기·권한·소유자 조건 실패 |
| 1 | `audit_key_exists`: 생성 대상이 이미 존재 |
| 1 | `audit_key_storage_unavailable`: 파일·디렉터리 생성/메타데이터/쓰기/동기화/닫기 실패 |
| 1 | `audit_key_generation_canceled`: 실행 취소. 생성 후에는 파일이 남을 수 있음 |
| 1 | `audit_key_output_unavailable`: 출력 실패. 생성된 키는 유지 |

오류에 키/경로/raw error를 붙이지 않는다. 출력 장치의 부분 쓰기까지 되돌릴 수는 없다. 정상 생성 출력은 고정 코드뿐이고 키는 포함하지 않는다.

## 기존 코드 연결

- 결과는 [감사 저장기](./e2-audit-journal.md)의 `Config.Key` 및 [오프라인 검증기](./e2-audit-verification-cli.md)의 `--key-file`과 같은 길이·형식이다. hex/base64·개행을 더하지 않는다.
- [서버 snapshot](./e2-server-runtime.md)의 `key_file`은 운영자가 배치한 비밀 파일의 이름만 참조한다. 이 CLI가 JSON이나 서버 등록을 수정하지 않는다. `KeyRef`와 키의 대응·기존 journal 검증에 필요한 키 보존은 운영자 책임이다.
- 키는 에이전트가 접근할 수 없는 경로에 두고 레포·LLM·전달 JSON·공유 로그·근거 파일로 옮기지 않는다. JWT·대상 세션·canary 키와 재사용하지 않는다. 파일 권한 검사만으로 에이전트와 키 소유 프로세스의 OS 권한 분리가 완료되는 것은 아니다.

## 검증 범위

합성 임시 경로에서 키 길이·정확한 파일 권한·저장기/검증 호환·덮어쓰기 및 링크/FIFO 거부·부모 권한·동시 생성·엄격한 umask·인자/난수/쓰기/fsync/close/취소/출력 실패와 메모리 버퍼 clear를 검사한다. 실제 운영 키나 대상 서비스·DB·LLM을 사용하지 않는다. 타 플랫폼은 명시적 비지원 경계를 유지한다.

## 감사 키 수명·KeyRef 결속 — 팀 리뷰 제안

2026-11-03 · 이동찬 · **팀 리뷰용 권장안, 미확정·미구현**

기준은 `dev` `f2c062bd596940027b184a683e69b414cf0cbb0c`다. 현재 CLI는 키만 생성하며 KeyRef를 발급/등록하지 않는다. [서버 snapshot loader](../../server/controlled_read_store.go)는 `key_ref`와 `key_file`을 받아 파일의 32바이트를 읽고, [`Journal.Create`](../../transmit/auditlog/journal.go)는 그 키를 복사해 header/이벤트 HMAC에 쓴다. `Journal.Record`에는 키 수명 상태를 조회하는 경로가 없다. 파일을 바꾸거나 원래 버퍼를 clear해도 이미 복사된 키가 자동 폐기되는 것은 아니다. 아래는 이 남은 관리 경계를 정하기 위한 제안이다.

**적용 범위는 엔진 E② 감사 키에 한정한다.** 평가 러너도 같은 `e2-audit-keygen` 도구로 run-record 서명 키를 생성하지만, 엔진 감사 키와는 **별도 키·별도 보관**이며 키를 재사용하지 않는다. 러너 서명 키의 수명·교체·보존은 [평가 프로토콜](../evaluation/evaluation-protocol.md) 소관으로 별도 다룬다. 같은 생성 도구나 raw 키 형식이 같다는 이유로 이 엔진 키 수명안을 러너 키에 자동 적용하지 않는다.

이 초안은 현재 dev의 생성 CLI·journal v1·snapshot·검증기를 기준으로 독립 검토할 수 있다. 열린 PR의 외부 checkpoint/모드 활성화 코드·계약 병합에 의존하지 않는다. UID/컨테이너/기동기·예산 소켓 구조와 운영 권한 변경은 해당 담당 범위이며 여기서는 감사 키를 공급/회수하는 쪽의 의무만 제안한다.

### 팀 결정표

| ID | 권장안·대안 | 확인 담당 · 수용 근거 |
|---|---|---|
| KLD01 | **감사 namespace 안에서 KeyRef를 한 키에 영구 결속.** 파일명이나 수정 가능한 secret alias를 정본으로 삼지 않고, 검증된 불변 secret version과 KeyRef를 연결한다. 같은 KeyRef의 키 교체·재사용과 같은 키를 다른 KeyRef로 alias 등록하는 방식도 거부한다. 같은 active KeyRef를 여러 journal이 쓰는 것과 구분한다. | 이동찬(구현 및 운영 책임자 겸임 권장): 같은 ref/다른 키·version 재결속 및 같은 키/새 ref 우회 거부, 예전 journal의 키 선택 재현. 생성/백업 경로만으로 결속을 추정하지 않음. |
| KLD02 | **수명 상태는 인증된 host metadata로 관리.** 준비→활성→교체 중→보존 전용을 구분하고, 유출 의심은 별도 불신 상태로 둔다. 파일 존재·32바이트 길이·모델 문자열은 활성 근거가 아니다. | 이동찬(구현·소유): 동시 상태 변경·재시도·metadata 유실·이전 revision 재생에서 신규 journal/admission 차단. 실제 registry/API는 후속 구현. |
| KLD03 | **한 journal 안의 키는 고정.** 정상 교체는 이전 키의 새 admission을 닫고 실행을 drain/마감한 뒤, 새 KeyRef/새 키로 새 실행을 등록한다. 기존 파일 덮어쓰기·실행 중 key swap은 채택하지 않는다. | 이동찬: pending/활성 키 사본·등록/취소·교체 경합 시험. 새 키 생성/활성화가 새 실행 승인·예산을 자동 발급하지 않음. |
| KLD04 | **과거 검증용 키를 보호된 경계에 보존.** 보존 전용 키는 신규 journal에 쓰지 않는다. 파기는 감사 자료/백업 의존성을 검토한 별도 승인 절차로 다루고, 합의 전 자동 TTL 파기를 두지 않는다. | 이동찬(운영 책임자 겸임 권장): 보존 책임·기간·백업 접근·복구/파기 증거, 키 상실 시 검증 불가 표시. 고정 회전 주기·보존 일수는 이 문서로 정하지 않음. |
| KLD05 | **MAC 일치와 키 신뢰 상태를 별도로 읽기.** HMAC 키 보유자는 같은 키로 새 MAC도 만들 수 있으므로 보존 전용은 운영 정책상의 제한이다. 공개 검증키처럼 분리됐다고 주장하지 않는다. | 이동찬(구현), 최민준(KLD05·과거 무결성 해석·consumer 표시 리뷰어): 유출 키로 재서명한 합성 journal이 MAC 검증에 성공해도 정상 출처로 승격되지 않는 소비 경계 시험. 기존 CLI 종료 코드 의미는 유지. |

**담당 제안:** #131·#134와 같이 12/18 설계검토 범위의 운영 책임자는 PM·통합 담당 **이동찬이 겸임하는 안을 권장**한다. 실제 운영 역할·권한·팀 합의를 확정하거나 키 보존/backup 실행을 허가한 것은 아니며 담당과 수용 근거의 팀 확인은 남아 있다. KLD04의 보존 책임·기간·backup 접근·복구/파기 증거는 운영 책임자가 확인할 범위다. 최민준은 그 실행 책임을 맡지 않고 KLD05·과거 무결성 해석·consumer 표시를 리뷰어로 확인한다.

### KeyRef 정본과 상태

새 관리 레코드의 제안 필드는 **비민감** `key_ref`, 불변 `secret_version_ref`, `state`, 양의 `revision`, 이전 키를 가리키는 선택적 `predecessor_ref`, 서버 인증 `actor_ref`, 서버 시각, 제한된 사유 코드·승인 근거 참조다. 키 원문·hex/base64·HMAC 키의 공개 fingerprint를 journal/레코드/공유 로그에 추가하지 않는다. secret version 참조만 있어도 바이트의 진위가 입증되는 것은 아니므로, 공급자는 신뢰 경계 안에서 KeyRef에 고정한 정확한 키가 로드됐는지 확인해야 한다. `key_file` 이름이나 경로만 비교하는 것으로 대체하지 않는다.

| 제안 상태 | 신규 journal/admission | 기존 자료의 취급 |
|---|---|---|
| `prepared` 준비 | 금지 | 생성·보관·불변 mapping 확인 중. 생성 성공만으로 활성 상태가 되지 않음 |
| `active` 활성 | 별도 실행 승인·정본 revision 확인 후에만 가능 | 현재 결속의 키로 MAC 검증. MAC 일치 자체는 실행 승인/진단 성공이 아님 |
| `retiring` 교체 중 | 금지 | 이미 추적된 실행의 중단·종료/마감만 처리. 새 등록으로 키 사본을 늘리지 않음 |
| `retired` 보존 전용 | 금지 | 보호된 verifier에서 과거 MAC 검증 가능. 그 키를 읽을 권한에는 기술적으로 MAC 생성 능력도 따름 |
| `compromised` 유출 의심/확인 | 금지 | 원본·기록을 보존하고 제한된 포렌식에서 MAC 일치를 확인할 수는 있으나 정상 출처/신뢰 복구로 표현하지 않음 |
| `destroyed` 파기 확인 | 금지 | KeyRef와 상태 이력은 보존. 필요한 키를 얻을 수 없으므로 새 키로 대체 검증하지 않고 검증 불가로 표시 |

정상 전이는 `prepared → active → retiring → retired`를 권장한다. 어느 비파기 상태에서든 유출 대응으로 `compromised`에 들어갈 수 있고, `prepared/retired/compromised → destroyed`는 별도 승인·의존성 검토 뒤에만 허용한다. prepared 파기는 미사용·미연결과 키 사본 수명까지 확인한다. 이전 상태로 되돌리거나 destroyed ref를 재활성화하지 않는다. 키 조회/registry 장애는 `retired`나 `active`로 추정하는 상태 전이가 아니라 **조회 실패**다. 상태 변경은 expected revision과 연산 참조를 결속해 원자 처리하고 같은 요청의 재시도만 멱등으로 수용한다. 같은 ref/revision의 다른 내용은 충돌이다.

새 registry의 실제 인증·저장 API/DDL·상태 코드와 revision 범위는 팀 확인 후 구현한다. 현재 KeyRef에는 이 수명 정보가 없으며 journal header에 상태 필드를 추가하지 않는다. 모델·일반 API는 `active=true`나 actor 문자열로 이 레코드를 발급/수정할 수 없어야 한다.

### 생성·등록·정상 교체의 제안 순서

1. 운영자가 생성 CLI로 **새 파일명**에 키를 만든다. 실패/출력 유실이면 기존 생성물과 사용 여부부터 확인하고 덮어쓰기하지 않는다. 32바이트라는 사실만으로 안전한 생성 출처가 증명되는 것은 아니므로 생성/보관 근거와 운영자 확인을 남긴다.
2. 신뢰 공급자가 새 KeyRef와 불변 secret version을 `prepared`로 결속한다. 실제 키 접근 경계·백업/복구·대상 snapshot의 비민감 KeyRef와 secret mapping을 검증한 뒤 권한 있는 운영자가 활성화한다. 기존 journal이나 다른 KeyRef를 자동 재결속하지 않는다.
3. 신규 실행은 snapshot이 지정한 KeyRef의 상태·revision·정확한 key mapping을 검증한 뒤에만 journal을 만든다. 생성 전 확인과 실제 admission 사이의 retire/compromise 경합도 host가 차단해야 한다. HMAC 라이브러리의 길이 검사를 수명 확인으로 취급하지 않는다.
4. 정상 교체는 이전 ref를 먼저 `retiring`으로 전이시켜 신규 journal/등록/admission을 막고, 그 ref를 가진 pending 등록을 회수한다. 활성 실행·예약 작업·키 사본의 소유자를 추적해 모든 잠금 밖에서 중단·drain하고 journal의 Close/최종 checkpoint 결과를 확인한다. [현재 runtime](../../server/controlled_read_runtime.go)의 key clear 경로만으로 모든 프로세스/복사본의 제거를 입증했다고 보고하지 않는다.
5. 추적된 실행의 종료를 확인한 뒤 `retired`로 전이하고 과거 검증에 필요한 키와 KeyRef 이력을 보호된 보존 경계로 넘긴다. 교체 중 오류·취소·drain 불명확이면 retiring 상태를 유지하며 자동 active 복귀·성공 응답·파기를 하지 않는다. 새 키로 실행하려면 별도 snapshot 검증·등록·실행 승인을 다시 확인한다. 키 교체가 RunRef·승인·예산·기존 journal을 복원하는 수단이 되지 않게 한다.

새 키를 미리 `prepared`로 만들 수는 있지만, 어떤 실행에 쓸지와 활성화 권한은 별도다. 교체를 이유로 같은 journal의 `KeyRef`/HMAC을 바꾸거나 과거 journal을 새 키로 재서명하지 않는다. 서로 다른 키의 수명 상태를 실행 허가 토큰으로 사용하지 않는다.

### 유출·유실·복구와 과거 검증

유출 의심/확인은 새 사용을 즉시 막고 pending/활성 실행의 키 사용을 회수하는 host 조치를 요구한다. 레코드를 `compromised`로 바꾸기만 해도 이미 복사된 키가 서명 불가능해지는 것은 아니다. 적용 완료는 새 admission 차단과 추적된 실행의 종료/키 사본 수명 확인을 함께 보고하며, 이미 송신된 요청을 취소로 되돌릴 수 있다고 표현하지 않는다. 유출 대응에서 기존 journal/checkpoint를 지우거나 새 키로 재서명해 신뢰를 회복하지 않는다.

MAC 검증에 성공한 과거 자료라도 해당 키가 유출됐다면 원래 작성자의 기록인지 새로 서명한 기록인지 HMAC만으로 판별할 수 없다. 관찰 시각이나 유출 발견 시각만 보고 “이전 기록은 안전”으로 자동 판정하지 않는다. 그 시점 전 별도로 신뢰한 checkpoint 등 출처 근거의 범위는 감사 검토자가 따로 평가한다. 이 초안은 과거 E③ 판정·finding을 자동으로 바꾸는 정책을 정하지 않는다.

키 유실·registry 불명확은 신규 사용/새로운 신뢰 확인을 차단한다. 검증용 backup을 복구할 때도 **같은 KeyRef의 같은 불변 키와 현재 수명 이력**을 확인한다. 파일 복원 성공이나 오래된 active metadata를 현재 활성 근거로 삼지 않는다. destroyed ref의 키가 backup에서 발견되면 파기 확인 불일치로 다루고 재활성화하지 않는다. 암호화 backup의 실제 수단·보존 기간·파기 대상/책임자와 복원 이력의 신뢰 anchor는 운영 책임자 겸임 권장안의 이동찬이 확인 근거를 준비할 사항이며 팀 확인 전 미확정이다. 최민준의 역할은 복원·과거 자료의 무결성 해석과 consumer 표시 리뷰이며 보존·backup 실행 책임이 아니다. 키 보존은 [최신 checkpoint의 신뢰](./e2-audit-journal.md)를 대신하지 않는다.

[오프라인 CLI](./e2-audit-verification-cli.md)의 현재 종료 코드 0은 운영자가 제공한 키·checkpoint와 journal 전체가 일치한다는 의미다. 수명 registry 조회 기능은 없으므로 이 초안만으로 CLI가 compromised/destroyed 상태를 자동 거부한다고 주장하지 않는다. 향후 consumer/운영 wrapper는 MAC 일치, key lifecycle 확인 결과, 최신 checkpoint 확인 결과를 구분해 표시하고, 키/정본을 못 얻은 상태를 무결성 성공·취약점 없음으로 바꾸지 않아야 한다. 보존 전용 키를 verifier 밖으로 배포하는 대안은 HMAC 생성 능력도 전달하므로 별도 권한 검토가 필요하다.

### 후속 수용시험 계획

아래 KLT01~10은 **미실행 계획**이며 기존 키 생성·journal/CLI 시험의 통과 수에 합산하지 않는다. 합성 키·임시 저장소만 쓰고 운영 키/권한은 변경하지 않는다.

| ID | 조건 | 수용 기준 |
|---|---|---|
| KLT01 | 같은 KeyRef에 다른 바이트/secret version, 파일 경로만 같은 교체, 이전 키를 새 KeyRef로 alias 등록 | mapping 재결속·유출 상태 우회 거부, 기존 journal/키 이력 불변 |
| KLT02 | 32바이트지만 출처/metadata/상태 불명, malformed/unknown KeyRef | 신규 journal/admission 0, 모델의 active/actor 선언으로 활성화 0 |
| KLT03 | 동시에 active/retiring/compromised 전이, 같은 요청 재시도/다른 내용 | revision 선후관계 확정·동일 요청 멱등·다른 내용 충돌 |
| KLT04 | 키 해석 후 journal 생성/등록/admission 직전 retire/compromise | 새 사용 차단, 이미 복사된 키를 가지고 우회한 실행 0 |
| KLT05 | pending/예약/활성 실행이 같은 키 사본을 가진 정상 교체 | 새 admission 0, pending 회수·전체 drain, old journal key 고정, 승인/예산 자동 재발급 0 |
| KLT06 | 교체 drain·Close·최종 checkpoint 실패 또는 취소 | retiring 유지·실패 노출, active 자동 복귀·retired 성공·파기 0 |
| KLT07 | 유출된 합성 키로 재서명해 MAC 검증은 성공 | MAC 일치를 정상 출처/신뢰 회복으로 표시 0, 원본/사유 보존 |
| KLT08 | retired 과거 검증, destroyed/유실 키, 같은 ref에 대체 키 주입 | retired의 보호된 검증만 가능, 나머지는 검증 불가/실패, 키 교체로 과거 성공 0 |
| KLT09 | backup 복원·오래된 active 이력·destroyed 키 재발견 | 현재 이력/동일 mapping 확인 전 사용 0, destroyed 재활성화 0 |
| KLT10 | 기존 CLI·생성물·오류/모델/공유 로그 회귀 | 기존 출력/종료 경계 유지, 키/경로/secret fingerprint/원시 오류 0, 실 운영 키 접근 0 |

팀 리뷰는 KLD01~05마다 채택/수정/보류·확인 담당·수용 증거를 남기는 방식으로 제안한다. 이동찬이 KeyRef 공급·host 회수 경계를, 최민준이 KLD05·과거 무결성 해석·consumer 표시를 리뷰하고, 운영 책임자 겸임 권장안의 이동찬이 키 보존/backup/파기 근거를 준비한다. 합의 뒤 별도 구현하며 이 문서의 게시가 키 교체·파기·새 권한 발급을 승인하지 않는다.
