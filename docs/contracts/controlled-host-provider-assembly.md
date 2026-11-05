# B: controlled host 공급자의 수명 연결과 C 조립 경계

상태: **공급자 수명 어댑터 구현, 실제 배치 공급은 미완료.** `controlledhost`는 이미 인증된 실제 공급자를 받는 연결부다. 신원·권한·key/epoch·복원 anchor를 발급하지 않고, 누락 공급자는 `ErrUnavailable`로 거부한다. 합성 시험의 root/권위는 운영 근거가 아니다.

이 문서의 A/B/C는 첫 IDOR 통합의 **실제 입력 어댑터 / host 수명 어댑터 / 서버 기동·종료 통합**을 뜻한다. 기존 기동·종료 통합(C)은 이동찬 담당이다.

## 기준과 고유 범위

2026-11-04 재개 기준: `dev` `3db9cc1a678ffd36fd67dc970e912aaecaff8fac`에 선행 #159 최종 head
`40b26d010d1620f3c4f03d788c714de3aed5ca18`가 포함됨을 확인해 정상 merge했다. 기존 공급자 어댑터의
Go 파일 3개는 그대로 보존했다. 아래 SHA·검증 서술은 최초 작성 당시의 기록이며 재개 검증은 PR 본문에 구분한다.

dev `893d1d771a10764591ee209814800f46420e0dc9`와 #159 `32bd87f77460953f2a60a5de3cdc391cc0b75758`를 분리 브랜치에서 정상 merge한 `82baaacc5a6b9f66c0845dc6e1c2bf4e299a19f0`가 기준이다. #159 안에 #143 `1dfc4a0`·#149 `8cc0d55`·#151 `edbfc80`와 #156 `6844513`→#157 `9c079d9`→#158 `e6af233`의 조상 포함을 확인했다. 열린 선행 head 변경 시 영향 분석·필요 동기화·재검증한다.

고유 수정은 신규 `controlledhost/`와 이 문서다. A의 실제 입력 어댑터, 기존 core/server/engine, Source 결합·strict 생성자·main·운영 기동은 수정하지 않는다. 패키지는 `server`를 import하지 않는다. C는 아래 기존 타입을 통해 주입한다.

게시 전 #151의 후속 `3312a07ca9bc14b1e326250f86ae289326114f97`(기존 서버 시험 17줄 추가, production/공급 인터페이스 변화 없음)와 dev `2f069b9cbc3420b8197a053dc207327c3364434a`를 정상 merge했다. 재검증 기준 통합 commit은 `71be06e1f92c11a95e9e472ecec7e29960322ccd`다. 기존 파일의 diff는 선행 통합분이며 B의 고유 신규 파일은 4개다.

## 확인한 공급 근거와 아직 필요한 입력

검토한 tracked 배치 파일은 평가용 compose이며 첫 실제 비평가 진단의 supervisor/secret 공급자 설정이 아니다. 코드·서비스 이름 조회에서 이 진단에 연결된 host 서비스·검증된 배치 참조를 확인하지 못했다. 다른 위치에 서비스가 존재하지 않는다고 단정하지 않는다. 비밀 파일·계정 원문·운영 DB를 읽거나 만들지 않았다.

[#146의 AH01~06](./audit-state-host-auth-proposal.md)은 문서 병합 뒤에도 제안 상태다. 이번 구현을 막는 구체 입력은 다음과 같다.

| 경계 | 이번에 연결한 의미 | 실제 공급 전 필요한 근거 |
|---|---|---|
| AH01 인증 | 실제 provider의 typed handle만 받으며 nil/typed-nil 거부 | server/operator/model 주체, socket/service handle과 검증된 peer/instance, endpoint FD 비위임 경계. kernel credential 코어 존재를 배치 인증으로 대신하지 않음 |
| AH02 권한 | checkpoint Access·key TrustRequest·revision Access·descriptor op를 그대로 전달 | namespace/instance/operation별 실제 현재 권한·lease·writer 범위. 요청 문자열로 권한을 만들지 않음 |
| AH03 root/복원 | 장기 RootTrust.Anchor를 빌려 쓰고 pending acquisition과 사용 lease 종료 전 Release 금지 | 확인된 store/epoch/root·외부 복원 anchor·배타 writer·부모 namespace. 새 경로 재열기나 inode 일치만으로 복원 연속성 생성 금지 |
| AH04 secret | 실제 Authority/Host를 위임하고 active admission과 역사 검증 요청을 합치지 않음 | 불변 identity/version·alias 방지·현재 신뢰 상태·기존 key 사본 수명. 파일 존재·MAC·metadata active로 대체하지 않음 |
| AH05 수명 | admission 폐쇄, 취소, pending/held lease/callback drain, exactly-once 해제 | 실제 감독 주체의 planner·예약 worker·chat·자식 handle과 완전한 추적 범위, 회수와 DB 작업 수명 연결 |
| AH06 게시 | 기존 checkpoint Host/Store와 Publisher root 타입을 사용 | 실제 게시 서비스·snapshot framing/한도·secret 공급 연결. 이 패키지는 새 RPC·snapshot 프로토콜을 만들지 않음 |

위 자료의 제공 주체·보호 경로·service/handle/ref가 주어지기 전까지 B를 운영 공급 완료로 표시하지 않는다. 실제 key·epoch·UID·DB role/GRANT를 생성하지 않는다.

## C가 주입할 기존 타입

`controlledhost.New(parent, Sources{...})`의 Root는 기존 실제 supervisor가 보유한 `RootTrust`다. `Context/Anchor/Directory`와 빠른 `CheckRoot/CheckTaskQuiescent`, `Release`를 제공한다. Context는 lease 전체 수명에 고정돼야 하며 New에서 한 번만 확보한다. gate 잠금 안에서 외부 provider를 호출하지 않는다. nil Root나 검증 실패는 생성 실패다. **New는 nonnil Root를 진입 시 소유하며 실패에도 한 번 해제한다.** 나머지 Sources가 없으면 해당 기능만 거부하며 임의 성공 provider는 없다.

| C의 기존 주입 지점 | B 연결부 |
|---|---|
| `server.ControlledTaskModeHost` | `RootPath: life.RootPath()`, `RootAnchor: life.RootAnchor()`, `CheckRoot: life.CheckRoot`, `CheckTaskQuiescent: life.CheckTaskQuiescent` |
| `auditkeys.New(..., authority)` | `life.Keys()`; 최초 Store 연산 context는 반드시 `life.Bind(requestCtx)`로 만든 뒤 defer cancel |
| `auditcheckpoint.New(..., host, limits)` | `life.Checkpoints()`; 실제 host는 정확한 scope/epoch/KeyRef·현재 권한·key mapping을 검사해야 함 |
| `server.ControlledReadPublisherPlan.RootScope` | `life.PublisherRoot(ctx, acquire)`로 실제 factory의 인증된 RootScope 획득을 감싼다. 획득 중에도 root 소유권을 보존함 |
| `server.ControlledReadRevisionHostOptions.Scope` | `life.Revisions()` |
| `server.ControlledReadRevisionHostOptions.DescriptorRoot` | `life.DescriptorRoots()` |

Publisher acquire closure는 **정확한 server 요청을 캡처해 실제 기동기/factory에 전달**해야 한다. B의 배치 root를 execution별 writer lease로 바꾸는 closure를 만들면 안 된다. factory가 반환한 epoch/publisher/limits는 C가 그대로 확인해 기존 Plan에 넣고, Host는 Sources.Checkpoints로 구성한 수명 어댑터를 사용한다. 실제 factory가 없으면 nil을 유지한다. 획득 실패의 부분 자원은 공급자 계약대로 정리하고, 반환된 root를 성공적으로 전달한 뒤에는 server/Publisher의 Release 소유권 규칙을 따른다. B wrapper는 중복 Release를 한 번으로 제한한다.

## C에 필요한 수명 hook과 해제 순서

1. 실제 배치/복원/권한·secret lease를 먼저 확보한다. namespace/identity domain이 다른 공급자를 편의상 한 Lifetime에 섞지 않는다. 이 실제 fencing은 B의 active 수로 생기지 않는다.
2. 그 root를 Life에 넘기고 **strict 생성자 이전에** 기존 mode/publisher/revision 공급부를 구성한다. New 실패는 기동 실패로 처리하며 legacy 생성자/ordinary Worker로 fallback하지 않는다. A의 입력과 최종 Source 결합은 C 소유다.
3. `auditkeys.Authority.WithTrust`의 consume에는 context 인자가 없다. 따라서 C는 key Store의 원래 연산 context를 `life.Bind`로 결속해야 한다. unbound context는 거부한다. private marker는 조립 실수 검사용이며 인증 토큰이 아니다. `context.WithoutCancel` 등으로 취소를 제거하면 안 된다.
4. 실제 authority·mapping·epoch 변경은 먼저 `life.Revoke()`로 새 admission을 닫고 기존 context를 취소한 뒤, 외부 잠금 밖에서 실제 연산·자식을 drain한다. 원래 authority는 해당 실제 lease가 끝날 때까지 유효하게 fence돼야 한다. B는 협조하지 않는 provider를 강제로 정지시키지 않는다. 이미 commit했거나 결과 불명인 기록을 rollback으로 표현하지 않는다.
5. 실제 supervisor의 quiescence 증명은 task ID별 planner/예약 worker/chat/자식의 전체 추적 범위를 포함해야 한다. `CheckTaskQuiescent`는 **이미 확보한 근거를 빠르게 확인**하며 drain을 시작하거나 기다리지 않는다. B의 resource active=0이나 단일 HTTP transmitter inflight=0을 이 근거로 바꾸지 않는다.
6. `server/controlled_read_publisher.go`는 한 HTTPTransmitter의 실제 StopAndWait와 core CloseWitness를 이미 조합한 뒤 실제 Host에 위임한다. 이것을 제거하거나 모든 자식 종료로 승격하지 않는다. 실제 Host의 배치·key·epoch·writer 검사는 여전히 필요하다.
7. `server/controlled_read_e3.go`의 collector finalize(`revisionSource`/Source → `e3record.Prepare` → 저장)는 실제 전체 읽기 trust scope가 필요한 구간이다. C가 그 구간의 원래 context·권위 lease를 유지할 hook을 확보해야 한다. Source callback이 돌아오거나 Store.Head의 짧은 lease가 해제됐다는 이유로 Prepare/저장 전에 전체 trust를 종료하면 안 된다. B는 Source를 중복 조립하거나 reader를 새로 만들지 않았다.
8. 읽기 Host는 **현재 권위**에서 정확한 Query의 epoch·Scope/JournalID/KeyRef/publisher와 sealed Head를 확인한다. 과거 receipt·동일 MAC·같은 문자열을 현재 권한으로 승격하지 않는다. 신규 journal Register의 active 검사와 과거 검증용 소비 상태는 실제 key 공급자가 목적별로 구분한다.
9. 신규 admission 폐쇄 뒤 C가 서버·planner·worker·chat·자식을 종료하고 기존 Publisher/Source/DB 연산을 완료·해제한다. 이어 **SQL row/engine/Publisher 잠금 밖에서** `life.Close(ctx)`를 호출한다. drain을 기다리다 Close의 context가 끝나면 root를 해제하지 않고 소유권을 보유한다. drain이 이미 완료된 호출은 context 취소보다 해제를 우선하고 실제 RootTrust.Release가 반환한 뒤 nil을 반환한다. 이미 해제된 경우의 반복 호출도 nil이다. 대기 중 종료된 경우에는 drain 뒤 다시 Close해야 한다. 모든 ticket이 끝난 뒤 장기 RootTrust.Release가 정확히 한 번 호출된다.

Sources의 기존 계약은 유지한다. Acquire가 block하면 Close도 성공을 꾸며 먼저 root를 닫지 않는다. key Authority가 callback을 늦게/중복/비동기로 부르는 계약 위반은 거부하며, 이미 실행 중인 callback의 ticket은 끝날 때까지 유지한다. 이 방어가 위반한 외부 권위를 정상으로 만드는 것은 아니다.

## 검증 범위와 남은 실제 수용시험

전용 시험은 합성 authority·key metadata·temp root와 실제 파일 descriptor만 사용한다. 정상 전달/해제, 누락·typed-nil·expired/canceled scope, acquisition 중 회수, 중복 Release/Close, Source epoch·binding 불일치, key callback 오류/중복/지연/조기 반환, root 경로 교체, supervisor의 quiescence 거부를 확인한다. 임시 root의 inode 시험은 실제 배치/복원 연속성 검증이 아니다. 실제 key·권한 발급·DB·UID 분리·Source 조립·strict 서버 기동·진단은 실행하지 않았다.

실행한 `go test -race ./controlledhost -count=1 -timeout=30s`, 관련 `go vet`, 전체 `go build ./...`, gofmt/diff 검사는 통과했다. 독립 리뷰에서 gate 내부 외부 Context 재조회에 의한 잠금 역전 가능성 1건을 수정하고 단일 Context 확보 회귀를 추가했으며, 재리뷰 차단 지적은 0건이었다. 새 #151/dev 통합 뒤 race·vet·전체 build를 다시 통과했다. 합성 시험에는 skip 경로가 없으며 실제 OS 근거는 소유한 temp root의 FD/경로 교체 시험에 한정된다.

C의 실제 배치 뒤에는 위 미제공 근거를 연결하고 전체 read/DB 작업 중 회수·secret 만료·epoch/root 변경, 예약 worker/chat/자식의 늦은 시작/FD 위임·종료, key active admission 대 과거 journal 검증, commit 불명·재시작의 거부를 관측해야 한다. 정확한 실제 근거·handle·수용시험 결과가 없으면 초기 실제 진단 준비 완료가 아니다.
