# 최초 IDOR host의 최종 감사 Source 연결

## 기준과 범위

선행 [#142](https://github.com/ABC2LAB/ABCDEF2LAB/pull/142)의 확인 head
`28101e583ac8ed088f3aa8c412ac07363c07164e`에서 별도 브랜치로 분기했다.
기준 dev는 `b1dea7458b8ff80744991de5b5c46c54f91465ec`다.
게시 전 최신 dev `d68d78e3d69be5a4d029a1ae7b274d1e6bf599ab`도 확인했다.
추가된 resource type·판정 페이지 API 제안 문서 두 파일은 감사 코드/신뢰 경계를 바꾸지 않는다.
선행 브랜치를 base로 하는 고유 diff를 유지하며, 이 문서·구현에는 해당 제안의 API/type을 활성화하지 않는다.
새 `server/controlled_read_audit_source*.go`만 구현하며 #142의 head·공유 파일·기존 검증 CLI는 수정하지 않는다.
통합 순서는 #142 → 이 후속이다. 선행 head가 바뀌면 고유 diff 영향 분석과 필요한 재검증을 수행한다.

`Server.NewControlledReadAuditSource`는 이미 구성된 strict mode host의 같은 root anchor를 빌린다.
실행을 활성화하거나 새 신뢰 root·계정·승인·KG·하네스·자식 종료 증명을 발급하지 않는다.
`SourceReader(metadata)`는 #142의 `e3record.SourceReader`에 감사 필드만 결합한다.
metadata reader는 여전히 실제 신원·승인·KG·하네스 정본을 공급해야 한다.

## 필수 신뢰 공급자

- `LatestCheckpoint`: 단순 최신 prefix나 sidecar를 정본으로 취급하지 않는다.
  현재 신뢰 store identity/epoch·실행/키 결속·조회 권한을 인증하고 **sealed 상태**일 때만
  정확한 checkpoint와 비민감 정본 참조를 반환해야 한다. Checkpoint에는 Close·drain·phase가 없다.
- `ResolveKey`: 전체 scope·JournalID·KeyRef를 대조하고 불변 key mapping과 현재 E③ 소비에
  허용된 키 신뢰 상태를 확인해야 한다. 파일 존재·길이·경로로 상태를 추정하지 않는다.
  compromised/destroyed·조회 실패를 정상 검증 키로 대체하지 않는다. 보존 키의 허용 여부도
  공급자가 현재 정책에서 확인한다. reader는 키 registry·상태 전이를 구현하지 않는다.
- 두 공급자는 `Read` 동안 유효한 신뢰 읽기 scope를 유지하고 context를 따라야 한다.
  I/O 뒤 재조회는 관측된 변경을 검출한다. 두 번 같은 값이라는 사실만으로 lease·ABA 방지·
  store epoch의 진위를 증명하지 않는다. 공급자의 읽기 scope 보장을 대신하지 않는다.
- mode host의 `CheckRoot`는 기존 [MD01/02](./task-mode-marker.md#81-결정표)대로 배치·허용 writer·
  복원 세대의 신뢰를 확인해야 한다. 0700/0600·inode 비교가 운영 UID 분리나 재시작 연속성을 증명하지 않는다.

[CPD](./e2-audit-journal.md) 분리 저장 서비스·동기 게시·최신 head 정본과
[KLD](./e2-audit-key-generation-cli.md) 키 mapping/수명 registry는 이 변경에서 구현하지 않는다.
실제 공급자가 없으면 생성/읽기가 실패한다. sidecar-only나 keyfile 추정 fallback은 없다.

## 파일 검증과 반환값

1. live mode·lifecycle·root 신뢰를 확인하고, 공급자의 sealed checkpoint와 실제 scope를 대조한다.
2. 해당 키를 빌려 reader 소유 사본만 만든다. 공급자 버퍼는 변경하지 않고 사본은 지운다.
3. borrowed root와 같은 inode의 root를 열고 `journals`를 fd 기준 `O_NOFOLLOW`로 연다.
   root/journals는 exact 0700·같은 소유자, 파일은 exact 0600·같은 소유자·단일 link를 요구한다.
   실제 읽는 fd에서 앞서 확인한 inode·소유자·link 수·크기·mtime을 읽기 전후 대조한다.
4. `<JournalID>.checkpoint`는 supplied checkpoint의 기존 typed JSON+개행과 정확히 같아야 한다.
   `<RunRef>.jsonl` 전체를 명시한 상한 내 불변 byte 사본으로 읽고 `auditlog.Verify`를 실행한다.
   symlink/FIFO·교체·변조·잘림·추가 tail·상한 초과는 부분 성공 없이 거부한다.
5. root·키 신뢰·최신 sealed head를 다시 확인한다. 반환 key bytes/head/ref가 달라지거나
   공급자가 신뢰를 철회하면 결과를 주지 않는다. HMAC 성공만으로 정상 출처를 승격하지 않는다.
6. `Bind`는 동일 execution scope에만 최종 참조·typed checkpoint digest·scope·sequence를 채운다.
   기존 감사 필드가 충돌하면 거부한다. Completed/Finalized·관찰 digest·생성 시각·KG·신원·하네스는
   만들거나 변경하지 않는다. `SourceReader`는 full Expected 문맥도 대조하고 매 호출마다 새로 검증한다.

`ControlledReadCheckpointDigest`는 #142의 typed checkpoint digest다. 외부 정본 저장소의
envelope 서명·epoch 검증이나 별도 canonical payload hash를 대신하지 않는다.

총 byte·이벤트·timeout을 모두 명시해야 한다. 안전 상한은 64 MiB·40,000 events·2분이며
호스트는 더 작은 유효값을 제공할 수 있다. 이것은 reader 자원 상한으로 실행 승인/예산이 아니다.
유효 prefix에서 잘라 성공하지 않는다. context는 정규 파일의 커널 I/O나 따르지 않는 공급자를
강제 중단하지 못한다. Linux 외에는 실패하며 새 OS 권한·DB 역할·배포 설정을 변경하지 않는다.

## 조립 예시

strict host를 구성한 뒤 새 실행을 명시 등록하기 전에 다음처럼 준비한다.
아래 `options`와 `metadata`는 실제 신뢰 공급자가 제공해야 하며 예시가 이를 발급하지 않는다.

```go
auditSource, err := s.NewControlledReadAuditSource(options)
if err != nil { return err }
sourceReader, err := auditSource.SourceReader(metadata)
if err != nil { return err }
// trusted resolver가 반환할 ControlledReadFindingPlan.Source에 sourceReader를 사용한다.
```

## 검증과 남은 연결

선택한 `TestFinalAuditSource*`는 owned 임시 파일·실제 감사 writer/MAC와 **합성 권위**를 쓴다.
실제 PostgreSQL·HTTP 대상·LLM·Juice Shop·운영 키/epoch를 검증한 시험이 아니다.
정상 metadata 결합, 거짓/변경된 공급자, 키 신뢰 철회 뒤 재시도, 정확한 byte/event 경계,
8개 동시 읽기와 보호 파일 위반을 회귀로 다룬다. private provider 진단·키 원문은 출력하지 않는다.

실제 첫 회차에는 CPD/KLD 정본 공급자와 기존 root/배타 writer·자식 종료 증명,
실제 승인·A/B 신원·KG/canary/harness snapshot 및 대상/model 설정이 여전히 필요하다.
그 준비 뒤 실제 회차를 순차 실행하며 이 합성 검증을 실제 진단으로 계산하지 않는다.
