# 동기 감사 checkpoint 게시자 — 비활성 통합 코어

2026-11-03 · 이동찬 · 구현 경계

`auditpublisher`는 새 로컬 HMAC journal의 header, 각 intent/result, 최종 Close를 [CPD 저장 코어](../../auditcheckpoint/store.go)의 현재 head에 동기 게시한다. 이 패키지는 실제 `transmit.Recorder`이지만 서버 실행 경로에는 아직 연결하지 않았다. 실제 인증 서비스·store epoch/복원 anchor·키 공급·모델과 저장소 권한 분리·운영 활성화는 포함하지 않는다.

## 기준과 API

- 기준: `dev` `88415fff88f1c66c4307ba370c360e6ad338efcc`, 감사 확장 검증 `9ec769ccd66f4e7ed1ee32aaed772c05fb30c023`, CPD 저장 코어 `8bf68804592a7b8e71c54e907a6ee391cde55f37`. 마지막 코어의 `Host/Lease`, `Publish`, `Head`가 이 구현의 직접 의존이다. 별도 인증 권한 초안 [#146](https://github.com/ABC2LAB/ABCDEF2LAB/pull/146)은 코드 선행 조건이나 확정된 운영 정책이 아니다.
- `auditpublisher.Open(ctx, Config)`은 host가 준 `Store`, `Epoch`, `PublisherRef`, 전체 `Scope`, `KeyRef`, HMAC 키, 필수 `RootScope` lease(철회 context·장기 root fd·절대 directory 경로·배타 writer 수명), 새 filename, snapshot 상한을 요구한다. `auditlog.Create`가 새 header 파일/디렉터리를 Sync한 뒤 상한 있는 원본 snapshot으로 CPD `Register`와 **별도 현재 `Head` 조회**를 완료해야 recorder를 반환한다. 실패한 journal 파일은 남기고 새 RunRef/파일로 자동 재시도하지 않는다.
- `(*Publisher).Record`는 호출을 직렬화해 `Journal.Record`의 전체 event Write+Sync → root에 묶인 파일의 불변·상한 적용 snapshot → CPD `Append` CAS/내구 ACK → 별도 현재 `Head` 대조를 수행한다. 어느 단계든 실패하면 sticky 오류로 이후 송신에 성공 ACK를 주지 않는다. 이전 연산 receipt는 현재 head가 아니다.
- 성공한 `Open`/`Record` 뒤 `AcknowledgedHead()`는 그 인스턴스가 마지막으로 확인한 checkpoint·revision의 **메타데이터 사본**을 준다. 서버는 이를 기존 보호된 로컬 checkpoint sidecar에 저장할 수 있지만, 이 사본은 독립된 현재 정본 조회/권한이 아니다. sidecar 저장 실패나 `NewHTTP` 생성 실패 시 `Abort()`로 이후 Record를 막고 journal/key/fd를 닫으며 원본을 보존한다. 실패 후 `Seal`을 호출하지 않는다.
- `(*Publisher).Seal(ctx)`은 호출자가 실제 전송기의 `StopAndWait`를 마친 **후** 호출한다. Journal Close 성공 뒤 같은 최종 prefix를 `Seal`하고 현재 sealed head를 대조한다. Store의 **실제 Host/Lease**는 Close·drain·이전 실패 없음의 독립 근거를 확인해야 한다. MAC, `Seal` 호출, caller의 주장만으로 마감 권위를 만들 수 없다. `Abort`는 로컬 fd/키 사본만 닫고 파일을 보존하며 외부 sealed 상태를 만들지 않는다.

## 신뢰와 실패 경계

`RootScope`는 운영자가 확인한 배치·실행에서 **이미** 발급한 lease여야 하며, revoke 시 context가 취소돼야 한다. 패키지는 현재 경로와 fd의 동일 inode, 디렉터리 소유 UID·정확한 `0700`, 파일 fd/경로의 동일 inode·소유 UID·link count 1·정확한 `0600`·일반 파일·상한, `O_NOFOLLOW`를 재확인한다. 이 검사가 root/배타 권한을 발급하지는 않는다. 같은 namespace의 다른 writer를 배제하는 실제 host root lease와 모델 프로세스·키·DB 역할 분리 없이는 이 패키지를 운영 경로에 연결하지 않는다. 테스트의 임시 root와 synthetic Host는 그 증거가 아니다.

SQL commit 결과가 불명확할 때는 **같은 operation ref·Request·소유 snapshot 바이트**로만 한 번 재확인한다. 그래도 결과를 확정하지 못하거나 현재 head가 다른 경우 실패 상태를 유지한다. 원본을 truncate/교체하지 않고 새 RunRef·epoch·키·operation ref로 성공을 꾸미지 않는다. 이전 Record 오류나 Close/Seal 장애를 sealed로 승격하지 않으며 외부 head가 open/불명확하게 남으면 실제 Host가 별도 조회·격리 절차로 처리해야 한다. 일반 파일/저장소의 blocking I/O를 취소 context가 강제로 중단하지는 못한다. 현재 전체 snapshot을 매번 검증하므로 비용은 journal 길이에 따라 커진다. 상한·지연과 평가 프로파일 조건은 활성화 전에 측정·고정한다.

## 검증과 남은 연결

전용 PostgreSQL의 임시 schema·합성 Host/키·소유 loopback 대상만 사용하는 package 시험은 header/current head, intent ACK 거부 시 수신 0, result/Seal 장애의 비승격, 부분 journal 보존, 같은 연산 재확인, 취소·동시 Record·hardlink 거부를 확인한다. 실제 모델·대상·권한자·키 서비스·전원 장애는 시험 대상이 아니다.

운영 연결은 별도 PR에서 `server/controlledCheckpointRecorder`를 교체하고 `NewHTTP` 전에 `Open` ACK를 요구해야 한다. 서버가 실제 `StopAndWait`와 Host의 마감 증언을 연결하고, 권한·epoch/anchor·키·파일 root 수명·보존/복원·평가 on/off 조건을 검증하기 전에는 이 패키지의 존재를 외부 최신성 또는 실행 통제 완료로 해석하지 않는다. [CPD 계약](./e2-audit-journal.md#최신-checkpoint-외부-게시--계약-확정-초안)의 CPD01~05와 CPS01~12는 전체 운영 수용 조건으로 남는다.
