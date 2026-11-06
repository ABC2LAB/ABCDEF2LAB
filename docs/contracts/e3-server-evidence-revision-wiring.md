# E③ 서버 SQL revision 연결

상태: 개발 통합. 실제 호스트·출처 공급과 운영 배포는 활성화하지 않는다.

## 정확한 선행 기준

- 최초 연결 기준은 #158 `e6af2335fbd0412fccf03df589f95d754c26c41b`(#156 `684451300875251325eb507db30c4d3b45ec4dbc`, #157 `9c079d92e5728411774005cf4760124a55068bb4` 포함)이다.
- #151 `edbfc8015921dd0a8bbf71ee6a6b741e84f35c11` 정상 merge로 #142 `a086b338496a3c2e306856ba294239a8815e408d`, #143 `1dfc4a0fbf9f47db07eacf8df420fcbd9982e9ad`, #149 `8cc0d5588fb6e8b6313434006224cccd8968bab2`를 상속했다.
- 재개 기준은 #158 최종 head `3dcc6de0c88ae25e27d796bccdf47d4692dccf54`를 정상 merge한 상태다. 선행 구현을 이 PR의 고유 변경으로 세지 않는다.

## API와 실행 순서

`NewWithRevisionedDurableControlledReadHost`는 신뢰된 Go 조립만 받는다. 기존 durable 옵션에 실제 `evidencerevision.ScopeProvider`·`DescriptorRootProvider`가 필수다. 서버는 자신의 manager PostgreSQL DB로 `evidencerevision.New`와 `NewDescriptorFiles`를 만들며, 외부 issuer 콜백이나 임의 Store를 받지 않는다. 복원·백그라운드 admission 전 bootstrap에서 설정한다. 기존 생성자의 명시적 호환 동작은 유지한다.

호출자가 revision migration을 별도로 적용해야 한다. 암묵적 DDL·root 생성·key/epoch/신원/승인 발급·fallback은 없다. nil/typed-nil 공급자와 실패한 호스트 콜백은 거부한다.

1. private collector가 실제 owner/cross `SendDetailed` 관찰을 한 번 받는다. `Capture`는 그 입력의 검증된 opaque metadata와 canary 필드 이름만 보존한다. 모델 JSON은 이 상태를 만들 수 없다.
2. 이미 승인된 전체 scope·intent에서 operation ref를 고정한다. RunRef 생성·변경이나 실행 재개가 아니다.
3. 기존 E② drain·durable Seal ACK·보호 sidecar와 성공 완료 claim 뒤 finalizer에 진입한다. Source A는 수집 시점이나 claim 전에 캐시하지 않는다.
4. 새 Source A가 실제 exploration·mode-version을 제공한다. SQL producer도 이를 검사하며, 이 값은 실제 caller/marker/root 권한의 대체물이 아니다.
5. `DescriptorFiles.Put`의 보호된 영속 metadata ACK가 있어야 `Issue`한다. 저장소 부재·root 거부·파일 결과 불명은 발급을 막는다. descriptor는 데이터이며 권한이 아니다.
6. SQL `Issue`는 실제 호스트 scope를 요구한다. receipt의 operation·전체 scope·task/intent/exploration/mode·요청/증거 digest와 양의 bounded revision을 대조한다. Source A의 0이 아닌 revision도 같아야 한다.
7. descriptor/SQL I/O 뒤 새 Source B가 audit/key/KG/신원/fixture 정본을 독립적으로 다시 읽는다. context·exploration/mode 및 0이 아닌 revision은 실제 receipt와 같아야 한다. B와 Prepare reader 반환 사이에 추가 외부 I/O는 없으며 기존 Prepare·최종 DB marker/mode 검사는 유지한다.

Source는 읽는 순간의 정합성이다. A/B 비교만으로 lease·계정 인증·finding commit까지의 권한 수명이 생기지 않는다. 실제 호스트의 `ControlledReadFinalizationScopeProvider`와 `RequireControlledReadFinalizationScope`로 감싼 Findings resolver를 strict 생성 전에 공급해야 한다. 이 hook은 Source A/descriptor/Issue/Source B·Prepare·DB save 반환까지 권위를 유지한다. `NewWithRevisionedDurableControlledReadHost` 자체는 finalization scope 요구를 자동 활성화하지 않으며 공식 회차·실배포 조립에서 명시적 strict 적용이 필수다. 실제 공급·복원 연속성 수용은 아직 후속이다.

현재 host lease의 만료·회수는 신규 저장을 거부하는 축이다. [저장 게이트](./e3-storage-gate.md)의 관찰 시각에 따른 역사적 정책 유효성·오라클 판정을 소급 변경하지 않으며, 결과 불명인 commit을 미저장으로 바꾸지도 않는다. HTTP drain은 해당 전송기만 다루며 legacy child·전체 task 종료를 증명하지 않는다.

## 실패와 복구

revision은 관찰 metadata이며 finding이나 완료 실행이 아니다. 오류·충돌·commit 결과 불명에서는 finding/done을 남기지 않는다. collector는 Capture 단계부터 첫 고정 revision 실패·operation을 private mutex 아래 보존한다. Capture 실패는 상위가 일반 거부로 숨기기 전에 잠금 밖에서 `revision_failed reason=capture_rejected operation_ref=…`를 한 번 기록한다. 이후 collect/Source는 같은 실패를 반환하고 새 operation/RunRef로 자동 재시도하지 않는다. 다른 revision 실패도 기존 finalization 로그로 저장 실패와 구분한다. SQL·출처·경로·관찰·자격 원문은 로그에 넣지 않는다.

실제 SQL commit 뒤 ACK만 잃으면 descriptor·SQL 행은 실패한 실행 뒤에도 남을 수 있다. 실제 file-reader·ReadReceipt 권한을 다시 확인한 운영자만 같은 operation을 명시적으로 `Load`·`Reconcile`한다. 실행 재개·새 revision·현재 저장 허가는 아니다. 운영 CLI/service 조립은 이 범위 밖이며 descriptor 유실을 raw 관찰로 복구한다고 보장하지 않는다.

외부 Source/provider/file 콜백과 hash/descriptor I/O는 collector/runtime/work/coordinator SQL 잠금 밖에서 수행한다. 기존 SQL producer의 짧은 transaction과 최종 finding 저장의 독립 mode 검사는 유지한다. 콜백은 기존 호스트의 재진입·drain 규칙을 지켜야 한다.

## 검증 범위와 남은 제한

소유한 합성 PostgreSQL·보호 임시 파일/anchor·실제 localhost GET·유한 fake LLM·명시적 합성 host/KG/계정을 사용한다. positive마다 sequence를 독립적으로 한 번 소비하고 실제 `Issue` 값이 그보다 큰지 검사하므로 단독 실행도 기본 revision 1을 허용하지 않는다. ACK 유실용 driver와 schema 부재용 pool은 실제 서버 pool 설정에서 파생하며 별도 revision 패키지 시험 DSN을 서버 DB로 오인하지 않는다. strict-bootstrap 시험은 `authPassKey`의 기존 값과 부재 상태를 cleanup으로 복원한다.

기존 HTTP RequestTimeout이 finalization 전체에도 적용되는 경계는 그대로다. 이 시험의 1초 예산만 10초로 늘려 Source·fsync·SQL 지연이 의도한 음성 가드를 가리지 않게 한다. 별도 finalization 예산 API는 도입하지 않는다. descriptor Put의 conflict/unknown/scope 실패는 여전히 `descriptor_unacknowledged`로 분류한다. 닫힌 고정 JSON type의 Marshal은 unsupported 값이나 custom marshaler가 없어 실패하지 않는 기존 근거를 유지한다.

`EvalRunID` 없는 합성 비평가 시험이다. 실제 Juice Shop·승인/계정/KG 출처·OS 신원 인증·운영 root/epoch/key 공급자나 운영 배포를 검증한 결과가 아니다. 실제 입력과 팀 결정은 계속 필요하다.
