# E② 서버의 동기 CPD 감사 연결

2026-11-03 · 이동찬 · **개발 구현·팀 검토용 host 경계, 운영 비활성**

기준은 dev `88415fff88f1c66c4307ba370c360e6ad338efcc`, 열린 #145 `b3652a180668c2752e1f8fb6130e0c3d298b9496`,
#148 `f5515077eda692be4b212d379303d8fddcb036d8`, #149 `3e31a33da2156c137f8ef37d3dfb4599ba89f738`,
#142 `abd0c2646b5ddc2d19bfbed95997b3fd17d66e81`, #143 `abbebd56de7f79122cd085f0c6bd8a4af01e53ff`다.
선행은 정상 merge했으며 원본 PR head는 수정하지 않는다. 통합 순서는 145→148→149와 142→143 뒤 이 후속이다.
선행 변경 시 실제 고유 diff·계약 영향을 확인하고 정상 동기화한다.

## 사용 경계

[`NewWithDurableControlledReadHost`](../../server/controlled_read_host.go)는 기존 mode/finding host와
필수 `ControlledReadPublisherFactory`를 신뢰 Go bootstrap에서 설치한다. 기존 `newServerConfigured`의
초기화 hook을 사용하므로 restored task·scheduler·background admission보다 먼저 구성된다.
nil factory는 시작 오류이고, factory 실패나 잘못된 plan은 해당 실행을 차단한다. local-only recorder나
일반 Worker로 fallback하지 않는다. 기존 `NewWithControlledReadHost`는 기존의 명시적 local-only 호환 경로다.

Factory는 서버가 선택한 Scope·IntentID·KeyRef·새 Filename을 받아 다음 Plan을 공급한다.

| 필수 입력 | 책임 |
|---|---|
| 실제 `auditcheckpoint.Host` | 실제 principal/현재 epoch·복원 anchor·key 상태·namespace와 writer 권위의 수명 lease |
| `Epoch`, `PublisherRef` | 운영자가 검증한 기존 정본과 주체. DB가 비었다고 새로 발급하지 않음 |
| `auditpublisher.RootScope` | 실제 private journals 디렉터리의 long-held anchor·배타 writer scope·철회 context |
| 명시적 `ExtensionLimits` | byte/event/timeout 자원 상한. 실행 승인이나 누적 요청 예산을 발급하지 않음 |

factory 오류 시 부분 자원은 factory가 정리한다. 성공한 Plan만 RootScope 소유를 서버로 이전한다.
서버는 Open 전 plan 검증 실패를 정리하며, nonnil RootScope를 `auditpublisher.Open`에 넘긴 순간부터는
Open의 성공/실패 모두 publisher가 소유한다. 이후 정상 Seal 또는 Abort가 정확히 한 번 Release한다.

서버는 실제 journal 디렉터리·long anchor의 일치도 검사하지만 이것만으로 RootScope를 인증하지 않는다.
Key는 기존 보호된 등록 입력에서 서버 내부로만 전달한다. 실제 Host의 독립된 KeyRef mapping/MAC와
일치해야 등록된다. factory 요청에 key bytes나 model/HTTP JSON 권한을 추가하지 않는다. 공개 Go 옵션과
Plan/Request의 capability 필드는 JSON에서 제외한다. 새 endpoint·권한·epoch·키·승인을 발급하지 않는다.

## 동기 순서와 private 증거

1. 짧은 runtime 예약 뒤, runtime/work/coordinator lock 밖에서 factory와 Publisher.Open을 호출한다.
   실제 header Sync→CPD Register ACK/current-head 확인→기존 보호된 header sidecar 저장이 끝나야
   **실행용** NewHTTP와 모델 tool을 공개한다. 기존 `validateControlledReadSpec`의 항상 거부하는
   validation-only NewHTTP는 실행/외부 등록 경로가 아니다.
2. Record는 local Sync→소유 snapshot→CPD Append ACK/current-head 확인→보호된 sidecar 저장까지
   성공해야 nil을 반환한다. 어느 단계든 실패하면 recorder는 sticky 실패이며 후속 요청·정상 마감·finding을 막는다.
3. 마감은 실제 **같은 HTTPTransmitter.StopAndWait 반환 뒤에만** 서버 private drain witness를 설정한다.
   그 witness는 zero/다른 transmitter/다른 Scope를 거부한다. task 전체·legacy 자식·다른 프로세스 종료의
   증거가 아니며 기존 `CheckTaskQuiescent`를 채우거나 대체하지 않는다.
4. Publisher.Seal이 실제 Journal.Close 성공으로 생성한 구체 `CloseWitness`와 private HTTP drain을
   scope-bound Host wrapper가 모두 확인한다. 그 뒤에도 **실제 Host.Acquire 위임이 필수**다.
   실제 인증·현재 epoch/key/root 권위를 library witness나 caller의 true로 대신하지 않는다.
5. Seal ACK/current head→최종 보호 sidecar/checkpoint가 성공한 뒤 기존 private completion claim과
   finding 최종화로 넘어간다. 취소가 claim보다 먼저 이기면 finding은 없다. claim 뒤 task Pause는
   기존 규칙대로 알려진 저장 성공을 stopped로 바꾸지 않으며 저장 오류를 성공으로 승격하지 않는다.

RootScope 철회는 HTTP 전용 child context를 취소해 실제 HTTP 전송을 중지한다. 이 context는 모델/엔진
work context와 분리된다. 정상 Seal의 RootScope.Release가 writer context를 닫아도 이미 끝난 HTTP만
취소하며 completion claim·fresh SourceReader·finding 저장을 거짓 취소하지 않는다. 서버 lifecycle 취소는
마감 context에도 계속 적용하며 `Background`로 게시/저장을 이어가는 우회 경로는 만들지 않는다.
Seal의 별도 게시 시한은 factory Plan의 `Limits.Timeout`을 사용하며 HTTP `RequestTimeout`과 구분한다.

## reader와 실패 의미

Publisher.AcknowledgedHead는 성공한 Open/Record 뒤 local sidecar를 쓸 때만 쓰는 인스턴스 metadata다.
독립 최신 authority가 아니다. #143의 SourceReader는 실제 current epoch·sealed head·현재 key trust를
검사하는 독립 provider와 보호 파일을 **최종화마다 새로** 읽는다. claim 전 결과를 캐시해 뒤에 Bind하지 않는다.
KG/신원/fixture/reset 자료와 E③ 저장 권한은 기존 host 공급을 유지한다.

cleanup은 Abort이며 실패한 journal을 잘라 수리·삭제하거나 강제 Seal하지 않는다. 의도 ACK/sidecar 실패는
실제 송신 0, 결과 실패는 이미 송신됐을 수 있다는 의미를 보존한다. Seal commit 뒤 sidecar·descriptor 정리·
SourceReader가 실패하면 원격 head는 이미 sealed일 수 있다. 이때 finding/완료 성공은 반환하지 않으며
원격 commit을 rollback했다고 주장하지 않는다. 불명 commit에 새 ref·승인을 만들어 자동 재실행하지 않는다.
실패한 자료와 CPD 상태의 reconciliation/incomplete/quarantine 전이는 실제 host/운영 후속이다.
factory·Open·각 sidecar·Seal·Abort 오류는 고정된 사유 category만 로그로 남기며 원시 provider/파일 오류나 입력 값은 기록하지 않는다.

Record 직렬화 lock은 동기 ACK까지 유지한다. 잠금 순서는 recorder→publisher→짧은 witness/host lease→SQL이다.
StopAndWait는 recorder lock 밖에서 먼저 끝낸다. 실제 host/RootScope callback은 publisher/recorder 재진입,
drain 대기 또는 역순 SQL 잠금 획득을 해서는 안 된다. lifecycle/키/epoch 교체는 lease 취소·drain을 SQL row
잠금 전에 처리해야 한다. E③ SourceReader와 sink는 recorder/runtime/work lock 밖에 유지한다.

## 검증·남은 운영 입력

전용 PG·소유 loopback·fake LLM·합성 metadata/authority/epoch/key로 2 GET→CPD sealed→동일 sidecar→
finding 1/done, header/intent/result/seal/sidecar/source 실패, root/key/epoch 상실, claim 전 취소/claim 뒤 Pause,
실제 진행 중 result Record의 StopAndWait barrier와 Abort-only cleanup, opaque 증거/JSON 경계를 검증한다.
정상 root Release가 writer context를 취소하는 fixture도 사용한다. 합성 권위는 실제 인증을 증명하지 않는다.
이 positive는 Expected와 Source의 `EvalRunID`가 모두 빈 **non-eval** 시험이다. 실제 첫 통합도 non-eval이며,
평가 실행에는 선행 대장의 팀 결정대로 러너 서명 v1.1 reset/model 원천이 필요하다. 이 시험은 그 실제 자료를
생성하거나 평가 회차·서명 검증 완료로 계산하지 않는다. fresh SourceReader 내부의 source 정합 검사는 유지한다.

실제 service transport/UID/auth/namespace·root 및 자식 quiescence producer, 기존 epoch·독립 복원 anchor,
운영 key registry·배포 역할, KG/fixture/실제 A·B 신원과 승인 공급은 여전히 별도 필수 입력이다.
이 연결은 전역 shared budget·셸/MCP·UI·평가 러너나 실제 Juice Shop 활성화를 구현하지 않는다.
공유 `server/server.go`, `db/db.go` 및 다른 팀의 모듈은 변경하지 않는다.
