# 감사 키 registry 영속 코어 — 팀 검토용 구현

상태: KLD01~05의 **metadata 전이 코어 구현 제안**.
실제 secret provider·운영자 인증·DB 역할·host anchor·키 공급·회전·파기 실행과
서버 기동은 연결하지 않는다. 이 문서나 migration 실행이 운영 권한을 발급하지 않는다.

## 기준과 소유 범위

기준 `dev d68d78e3d69be5a4d029a1ae7b274d1e6bf599ab`의
[KLD 수명 제안](e2-audit-key-generation-cli.md)을 사용한다.
게시 전 최신 dev `88415fff88f1c66c4307ba370c360e6ad338efcc`를 정상 merge했다. 추가 평가 계측 범위는 이 코어/계약과 충돌하지 않으며 merge 뒤 전체 Go build도 통과했다.
코드는 열린 PR의 병합에 의존하지 않는다.
신규 `auditkeys/`와 이 문서만 변경하며 journal v1, 기존 CLI, shared server/DB,
checkpoint registry, 평가 러너 키에는 자동 적용하지 않는다.

`revision`과 `policy_revision`은 양의 signed 64-bit 범위이며 신규 prepared는 1이다.
이는 이번 DB 구현의 보수적인 범위 제안으로, KLD 전체 운영 정책의 확정을 뜻하지 않는다.

## API

```go
store, err := auditkeys.New(sqlDB, auditkeys.Config{
    Namespace: "engine-audit", PolicyRef: "authenticated-host-policy",
}, authority)
err = auditkeys.Migrate(ctx, sqlDB) // 명시 호출, global db.Open에는 등록하지 않음
out, err := store.Prepare(ctx, keyRef, secretVersionRef, predecessorRef, operationRef)
out, err = store.Change(ctx, auditkeys.Command{
    KeyRef: keyRef, OperationRef: operationRef,
    ExpectedRevision: revision, Next: auditkeys.Active,
})
metadata, err := store.Inspect(ctx, keyRef)
```

이 패키지는 원문 key 또는 key 파일 경로를 받지 않고 반환하지도 않는다.
`Record`와 `Outcome`은 metadata/연산 영수증이며 journal 생성·admission 권한이 아니다.
실제 admission이 status를 한 번 읽은 뒤 실행하는 방식으로 이 API를 사용하면 안 된다.
키 해석부터 등록·실제 admission까지 retire/compromise를 막는 host 경계는 후속이다.

## Authority.WithTrust 제안

호스트가 설치한 `Authority`만 `Attestation`을 발급한다.
모델 JSON·일반 API의 actor/active 문자열, 파일명, 32바이트 길이는 attestation이 아니다.
실제 RPC/인증 방식은 이 패키지가 정하지 않는다.

1. registry는 현재 mapping/revision/state를 row lock 없이 읽고 정확한
   `TrustRequest`를 보낸다. 이 DB projection 자체가 신뢰 anchor는 아니다.
2. provider가 실제 인증된 namespace/policy/operator와 불변 secret mapping을
   검증하고, 유효한 신뢰 scope를 획득한 후 `consume`을 정확히 한 번 동기 호출한다.
3. scope는 `consume`이 끝날 때까지 유효해야 한다. 외부 provider/secret/drain I/O는
   consume 진입 전이며 SQL row lock을 잡은 채 기다리지 않는다.
4. registry는 scope 안의 트랜잭션에서 mapping/revision/state를 다시 확인한다.
   이 재확인은 CAS 경합 검사다. 두 번 같은 결과가 나왔다는 사실로 lease,
   ABA 방지, 복원 epoch 또는 최신 정본을 증명하지 않는다.

코어는 요청의 exact echo, 저장 identity와의 일치, 필수 근거의 형식·존재 및
재시도 digest를 검사한다. actor·현재 policy revision·근거의 진위와 발급 권한은
Authority의 외부 책임이며 코어가 문자열에서 판별하지 않는다. 불일치·누락·provider
오류는 거부하고, 누락 시 허용하는 기본 provider/fallback은 제공하지 않는다.
실제 scope·현재 정책·복원 anchor의 구현과 검증은 공급자의 외부 전제다.
완료된 commit 뒤 provider 오류/취소는 rollback이라고 주장하지 않고
`audit_key_registry_outcome_unknown`으로 보고한다.

### 불변 identity와 필수 근거

`Identity`는 공급자가 신뢰 경계 안에서 발행한 **opaque key identity**다.
공개 key fingerprint·raw key·hex/base64·파일명으로 대체하지 않는다.
공급자는 namespace 안에서 실제 같은 키, byte-identical secret version 또는
복구 사본이 같은 identity가 되며 다른 키와 충돌하지 않는 정본 결속을 증명해야 한다.
DB는 `UNIQUE(namespace, secret_identity)`와
`UNIQUE(namespace, secret_version_ref)`로 alias를 거부한다.
공급자가 같은 키에 새 identity를 잘못 발급하면 DB만으로 이를 탐지할 수 없다.

모든 작업에는 인증된 `ActorRef`, 양의 `PolicyRevision`, `MappingRef`와
`AuthorizationRef`가 필요하다. mapping은 secret version과 실제 키의 결속을
확인한 근거여야 한다. 다음 전이는 추가적인 공급자 근거를 요구한다.

| 대상 상태 | 추가 필수 근거 |
|---|---|
| active | ActivationRef: 실제 결속 키의 현재 가용성·정확한 해석 및 활성 승인 |
| retired | DrainRef: 새 사용 차단 뒤 추적된 실행·사본의 종료 확인 |
| destroyed | DrainRef·DependencyRef·CopyLifetimeRef·DisposalRef: 종료·감사/backup 의존 검토·사본 수명·파기 확인 |
| prepared에서 destroyed | 위 근거와 UnusedRef: 미사용·미연결 확인 |

근거 ref 문자열의 모양은 진위 검증이 아니다. 실제 발급·검증은 provider 책임이다.
registry가 이 근거를 스스로 만들거나 키를 삭제하지 않는다.
특히 prepared 생성 성공과 active 승격은 별도다.

## 원자성·전이·재시도

정상 전이는 `prepared → active → retiring → retired`다.
`prepared/active/retiring/retired → compromised`를 허용한다.
이미 `compromised`인 상태에서 같은 상태로 다시 전이하지 않는다.
`prepared/retired/compromised → destroyed`만 허용하며 이전 상태로 돌아가지 않는다.
retiring·compromised로 metadata를 바꾸는 것만으로 이미 복사된 HMAC 키를 회수하거나
추가 서명을 기술적으로 막았다고 주장하지 않는다.

READ COMMITTED 트랜잭션 안에서 operation별 advisory lock과 현재 key row lock을
획득한다. provider 획득 전 읽었던 mapping/revision/state 및 expected revision을
다시 확인하고 key state/revision, append-only history, operation receipt를 함께 저장한다.
secret version·opaque identity·predecessor와 KeyRef는 수정할 수 없다.
새 predecessor는 같은 namespace의 기존 ref이며 자기 자신일 수 없다.

namespace 안의 operation ref는 하나의 명령과 인증 근거에 결속한다.
내용 digest는 키가 아닌 command/mapping/actor/policy/근거 metadata만 해시한다.
같은 operation의 정확한 재시도는 기존 영수증을 돌려주고,
같은 operation의 다른 내용·근거나 새 operation으로 과거 revision을 재생하면 충돌한다.
정확한 재시도에서는 provider가 같은 연산의 actor·policy revision과 전체
attestation 근거 metadata를 안정적으로 재현해야 한다. 현재 정책이 재시도를
허용하지 않거나 신뢰를 잃으면 과거 영수증이 있어도 provider가 차단해야 한다.

`Outcome.State/Revision`은 **그 연산의 과거 결과**다.
`Outcome.Current`는 재시도 시점의 DB projection을 별도로 담는다.
과거 active 영수증을 다시 읽어도 현재 compromised/retired를 active로 바꾸지 않는다.
조회 실패·키 신뢰 상실을 active/retired 상태 전이로 추정하지 않는다.

DDL과 key/history/operation 쓰기 트랜잭션은 풀의 기본값과 관계없이
`SET LOCAL synchronous_commit=on`을 요구한다. 세션 기본값이나 운영 DB 역할·설정을
바꾸지 않는다. PostgreSQL의 실제 fsync/WAL·복제/장애조치 정책과 저장장치·전원실패
내구성은 외부 운영 전제이며 이 설정이나 합성 시험으로 검증했다고 주장하지 않는다.

중간 SQL 실패는 부분 key/history/operation을 남기지 않는다.
commit ACK 유실은 결과 불명으로 보존하고 같은 operation으로 재조정한다.
이 코어는 클라이언트가 결과 불명 작업을 새 operation으로 무조건 재시도하도록 하지 않는다.

## 저장·운영 한계

DDL trigger는 mapping 재결속·역방향 전이·revision 건너뛰기,
history/operation UPDATE·DELETE·TRUNCATE 및 key 삭제를 거부한다.
API는 세 레코드의 원자성을 보장하지만, DB owner가 trigger를 바꾸거나
독자적인 SQL로 state/history를 조작하는 공격까지 막는 권한 분리는 아니다.
실제 DB 서비스 역할·읽기/쓰기 권한·backup/복원 anchor·보존/폐기 절차는 팀 결정 후 별도다.

키 원문·그 인코딩·공개 fingerprint를 registry/history/공유 로그에 저장하지 않는다.
SQL 오류는 외부 Error 문자열에서 고정 사유로 숨긴다.
운영 로그가 내부 unwrap 원인이나 provider의 private 값을 별도로 출력하지 않아야 한다.
구체적인 socket·UID·기동기 정책은 별도 host-auth 제안에서 검토하며 이 API로 확정하지 않는다.

## 검증 범위

합성 authority의 명시적 mapping/신뢰 scope와 독립 PostgreSQL16 시험 DB를 사용한다.
허용/금지 전이, activation·drain·폐기 근거 누락, immutable mapping·alias,
동일 operation·내용 충돌·동시 CAS, provider I/O 시점 row lock,
중간 rollback·commit 후 ACK 유실, 과거 revision 재생·과거 영수증과 현재 상태 구분,
키 identity 교체·신뢰 장애를 검사한다.
mapping 시험은 나머지 state/revision을 합법적으로 유지하고 SQLSTATE `23514`와
정확한 trigger 사유를 확인한다. initial insert·revision 건너뛰기·retired 재활성화,
신뢰 획득 중 현재 revision/state 변경·Inspect 재확인, 동일 operation의 actor/policy/근거
변경, 비동기 consume 조기 반환과 실제 commit 시점 실패를 별도로 검사한다.

실제 secret provider, 운영 key, 실제 인증/anchor, admission·키 회수·폐기·배포 또는
Juice Shop 회차를 검증한 결과가 아니다.
