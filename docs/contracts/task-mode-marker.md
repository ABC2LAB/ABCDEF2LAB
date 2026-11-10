# 실행·저장 공통 marker 조회·이관 판정표

> **현재 구현과의 구분 (2026-11-08):** 본문의 이관·실행·저장 미구현과 기준 SHA는 2026-11-03 단계별 설계 기록이다. 현재 mode/marker 연결은 [`server/controlled_read_mode.go`](../../server/controlled_read_mode.go)와 [첫 IDOR 통합](first-idor-integration.md)을 함께 확인한다. 아래 판정표·잠금·신뢰 공급 요구와 별도 팀 검토 사항은 그대로 유지한다.

2026-11-03 · 이동찬 · 후속 어댑터 기준·서버 연결 팀 리뷰안

상태: **[marker 읽기 어댑터](./task-mode-marker-reader.md)는 별도로 구현됐으며, 이관기·실행/저장 배선과 §8의 활성화 제안은 미구현·팀 검토 대상이다.** 이 문서에서 채택한 규칙과 기존 코드의 동작을 구분한다. 팀의 최종 리뷰·합의를 이미 마쳤다는 뜻이 아니다.

범위는 task의 `Legacy / Controlled / Block` 경로 분류다. 실행 승인·새 run의 유효성·취약점 확정 권한은 부여하지 않는다. 기존 [mode DB 계약](./task-mode-db.md)과 [E③ 저장 게이트 §4](./e3-storage-gate.md#4-통제-작업-판별--선행-조건)의 정본·단방향 전이·잠금 규칙을 유지한다.

## 1. 확인된 현재 구현

- [`markerFile`, `mark`, `taskMarked`, `marked`](../../server/controlled_read_store.go)는 private state의 파일을 사용한다. 기본 디렉터리는 `dataDir/e2-state`, 설정은 `ARTEX_E2_STATE_DIR`다. 경로·권한의 지원 범위는 [서버 runtime 계약](./e2-server-runtime.md#서버가-보유하는-승인-정보)을 따른다.
- task 파일은 `controlled-<task_id>.task.marker`, intent 파일은 `controlled-<task_id>-<intent_id>.marker`다. 생성기는 JSON에 `task_id` 문자열, `intent_id` 정수, `controlled` 불리언을 기록한다. task marker는 `intent_id=0`, intent marker는 양수이며 `controlled=true`여야 한다. 현재 task validator는 누락된 `intent_id`도 0으로 읽는다. 이 호환성은 새 reader에서도 유지하며, intent marker에는 양수 필드가 필요하다. task ID는 선행 0 없는 양의 십진 문자열이다.
- [`mark`](../../server/controlled_read_store.go)는 task marker를 먼저 만들고 intent marker를 만든다. [`registerControlledRead`](../../server/controlled_read_runtime.go)는 그 뒤 snapshot/profile을 읽는다. 뒤 단계가 실패해도 앞선 marker는 남을 수 있다.
- `taskMarked`는 task 경로의 `Lstat` 결과가 `os.ErrNotExist`면 `false, nil`을 반환한다. **부모 디렉터리가 사라진 경우의 ENOENT도 구분하지 않는다.** `marked`는 task marker가 있으면 해당 intent 파일을 검사하지 않고 true를 반환하고, task marker가 없으면 지정한 intent 한 개만 확인한다. 전체 intent marker 목록을 확인하는 API가 아니다.
- 존재하는 파일의 검증은 [`readPrivate/readPrivateAt`](../../server/controlled_read_private_linux.go)와 strict JSON을 사용한다. 마지막 부모 fd, 정확한 디렉터리 `0700`·일반 파일 `0600`, 링크/FIFO 거부, 4KiB 상한·읽기/close 오류를 검사한다. 비 Linux reader는 오류다. 이 검사는 파일이 없을 때 부모부터 확인하는 새 조회 절차를 대신하지 않는다.
- 초기화의 `mkdirPrivate`는 `MkdirAll`로 없는 디렉터리를 만들 수 있다. 다시 만들어진 빈 디렉터리는 이전 marker가 없었다는 증거가 아니다. 현재 runtime의 `r.mu`는 한 프로세스의 등록을 조율하며 DB mode reader와 파일 writer 전체를 직렬화하지 않는다.
- [통합된 marker reader](./task-mode-marker-reader.md)는 host의 장기 `RootAnchor`·등록 동기화 `CheckScope`를 받아 전체 목록·파일·JSON·scope·취소를 검증한다. 합성 파일 회귀는 별도 reader 계약에 기록돼 있으며 실제 DB 등록 경합·이관·서버 admission/저장 연결까지 검증한 것은 아니다.
- [`db.TaskModeReader`](../../db/task_mode.go)는 caller의 `READ COMMITTED` transaction과 task row 잠금을 사용하지만 현재 live marker callback은 미연결이다. 아래 파일 판정표가 기존 `taskMarked`에 이미 구현됐다고 표현하지 않는다.

## 2. 이번 판정표에서 고정하는 결정

1. **DB가 모드 정본이다.** 파일만 있거나 파일이 없어도 DB mode를 추정·수정하지 않는다. `explicit_legacy` 등록은 신뢰 서버가 분류한 새 task 생성 단계 또는 별도 승인된 이관에서만 한다. 미등록 task의 admission이 legacy를 뒤늦게 등록하지 않는다.
2. **`clear`는 전체 조회 성공의 결과다.** 검증한 state root에서 해당 task의 task/intent marker가 모두 없음을 등록과 일관되게 확인한 경우에만 반환한다. 단일 파일 ENOENT, 다른 intent 한 개의 부재, 불완전 목록, 초기화 성공은 그 근거가 아니다.
3. **task marker만 정상이어도 모드 보조 신호는 `present`다.** 등록 중 실패·만료·소비 후 남은 marker를 지우거나 legacy로 내리지 않는다. task marker는 새 미등록 intent도 통제 경로로 묶는다. 유효한 실행 lease가 있다는 뜻은 아니다.
4. **intent marker만 남으면 불일치로 차단한다.** task marker를 자동 생성해 정상으로 간주하지 않는다. 읽기와 복구/이관을 분리한다.
5. **오류는 부재가 아니다.** 조회/파싱/권한/경로/목록/동기화 오류는 `unknown`과 명시적 오류다. 유효 파일 일부를 찾았어도 나머지 조회가 실패하면 부분 성공을 반환하지 않는다.
6. **현재 marker 형식은 유지한다.** 이번 모드 분류에 새 파일 버전·run/reset·snapshot digest·서명 필드를 필수로 추가하지 않는다. DB `Record.Version`은 mode fence revision이며 파일·실행 세대와 동일시하지 않는다. 그 세대·승인·발급 증명은 별도 E②/E③ 경계의 책임이다.

## 3. 파일 조회 → MarkerState

아래는 [통합된 읽기 어댑터](./task-mode-marker-reader.md)가 구현한 **파일 판정 규칙**이다. 신뢰 root·등록 동기화의 실제 공급과 서버 연결은 후속이다. `T`는 대상 task marker, `I`는 대상 task에 속한 모든 intent marker다. 정상 파일은 이름·JSON의 task/intent 결속, `controlled=true`, 기존 private reader 조건을 통과한 파일이다. 과거 intent 파일도 조회 대상이며 snapshot 만료·pending lease 소멸을 파일 부재로 취급하지 않는다.

| ID | root·조회 조건 | T | I | 정규화 결과 |
|---|---|---|---|---|
| FM01 | 신뢰 root·전체 조회 성공 | 정상 | 정상 1개 이상 | `present` |
| FM02 | 신뢰 root·전체 조회 성공 | 정상 | 정상 확인된 부재 | `present` — task-wide 통제 표시 유지 |
| FM03 | 신뢰 root·전체 조회 성공 | 정상 확인된 부재 | 정상 1개 이상 | `unknown` + 오류 — intent-only 불일치 |
| FM04 | 신뢰 root·전체 조회 성공 | 정상 확인된 부재 | 정상 확인된 부재 | `clear` |
| FM05 | root 확인 실패·실종·권한/링크/비지원 환경·연속성 미확인 | 어떤 값 | 어떤 값 | `unknown` + 오류 |
| FM06 | 대상 파일의 파싱/이름·ID/controlled/형식/권한/크기/읽기/close 오류 | 어떤 값 | 어떤 값 | `unknown` + 오류 |
| FM07 | 목록 확인 실패·상한 초과·관련 marker의 task 귀속 판별 불가 | 어떤 값 | 어떤 값 | `unknown` + 오류 |
| FM08 | 조회 중 root/파일 교체·파일 소멸·등록 동기화 확보 실패·취소 | 어떤 값 | 어떤 값 | `unknown` + 오류 |

FM05~08은 FM01~04보다 우선한다. 예를 들어 정상 task marker와 손상된 대상 intent marker가 함께 있으면 `present`로 일찍 반환하지 않는다. 대상 task와 명확히 구분되는 다른 task의 정상 파일은 해당 task의 존재 근거로 쓰지 않는다. 파싱할 수 없어 task 귀속 자체가 불명확한 marker는 목록 완전성을 증명하지 못한 것으로 본다.

조회는 양의 DB task ID를 정규 십진 문자열로 변환해 비교한다. intent ID 목록을 DB에 현재 남은 행만으로 좁히지 않는다. 삭제된 intent의 파일도 남을 수 있기 때문이다. state의 snapshot·journal·checkpoint·시크릿 본문은 이 분류를 위해 읽지 않는다.

`clear`를 판단하려면 검증한 같은 root에서 관련 파일 목록 확인과 파일 검증을 마쳐야 한다. 실제 어댑터는 root fd를 고정하고 관련 파일을 그 fd 기준으로 읽으며, 파일명 경로의 ENOENT만으로 부모의 정상 존재를 추론하지 않는다. root를 자동 생성하거나 바꿔 다시 읽는 복구는 reader에 넣지 않는다. 상한을 넘거나 일관된 조회를 완료하지 못하면 `clear`를 반환하지 않는다.

root의 영속 저장소·허용 writer·등록 잠금 참여를 운영 환경에서 확인할 수 없으면 조회 활성화를 보류한다. 경로명과 권한만 같은 새 빈 root로 과거 상태를 인증할 수는 없다. OS writer 분리, 상위 경로·허용 writer 및 DB와 파일의 동시 rollback에 관한 기존 신뢰 한계를 이 표가 해결하지 않는다.

## 4. DB 정본 × 정규화 marker → 경로

아래 DM01~06은 기존 [`taskmode.Decide`](../../taskmode/mode.go)에서 DB/task ID/revision이 유효하고 marker도 같은 task인 경우의 진리표다. DM07~09는 정본·reader 오류에 대한 기존 차단 규칙이다. `Controlled`는 통제 파이프라인 선택이며 실제 송신·확정 저장 허가가 아니다.

| ID | DB mode | MarkerState | Route | 기존 Reason |
|---|---|---|---|---|
| DM01 | `explicit_legacy` | `clear` | `Legacy` | `explicit_legacy` |
| DM02 | `explicit_legacy` | `present` | `Block` | `legacy_marker_conflict` |
| DM03 | `explicit_legacy` | `unknown` | `Block` | `marker_unknown` |
| DM04 | `controlled` | `clear` | `Block` | `controlled_marker_pending` |
| DM05 | `controlled` | `present` | `Controlled` | `controlled` |
| DM06 | `controlled` | `unknown` | `Block` | `marker_unknown` |
| DM07 | 정본 행 없음 | 어떤 값 | `Block` | `mode_record_missing` |
| DM08 | mode/task ID/revision/marker enum이 잘못됨 | 어떤 값 | `Block` | 기존 코어의 해당 거부 사유 |
| DM09 | DB·marker reader 오류·nil reader·취소 | 어떤 값 | `Block` + 오류 | `Resolve`의 해당 거부 사유 |

DM08은 순수 코어에 잘못된 값을 직접 전달한 경우다. DB reader가 저장된 mode/version 손상을 발견하면 reader 오류를 반환하므로 실제 DB 경유 경로는 DM09로 간다.

DM03/06은 `MarkerUnknown` 값을 판별한 경우다. 실제 새 marker reader가 FM03/05~08의 오류를 반환하면 DM09 경로가 적용된다. 오류 시 reader의 반환 marker는 zero 값이며 caller는 오류와 부분 값을 조합해 허용하지 않는다. 오류의 세부 원인은 진단용으로만 유지하고 모델·공유 로그에는 안전한 오류 범주만 사용한다.

정본이 없으면 marker가 `clear`여도 차단한다. controlled 정본만 있고 파일이 없으면 **등록 미완료로 차단**하며 mode를 legacy로 내리지 않는다. 오류가 없더라도 `Decision.Route()`가 `Block`일 수 있으므로 caller는 error와 Route를 모두 확인한다.

## 5. 기존 task와 파일의 이관 판정표

다음은 **이관기에서 수행할 조치의 규칙**이며 읽기 callback의 부수 효과가 아니다. 모든 작업은 신뢰 서버가 실제 task의 존재·소유·삭제/archive 상태를 확인하고, 관련 실행을 정지·drain한 뒤 수행한다. 이 문서는 실제 task·DB·파일을 이관하거나 정리하지 않는다.

| ID | 기존 상태 | 이관 조치 | 읽기/실행 결과 |
|---|---|---|---|
| MG01 | DB 없음 + 정상 task marker(FM01/02) | 명시적 이관 목록에서 `RegisterTaskMode(controlled)`로 fence부터 커밋 | 이관 전 Block. 이관 후에도 새 실행 승인은 별도 |
| MG02 | legacy DB + 정상 task marker | 충돌 유지. 명시적 이관에서 legacy→controlled 전환 | 자동 승격/legacy 우회 없음. version은 DB 전이 규칙 준수 |
| MG03 | DB 없음 + task/intent 모두 부재 | 빈 디렉터리만 보고 legacy backfill 금지 | 신뢰 분류 근거가 없으면 계속 Block |
| MG04 | intent-only 정상 파일 | 불일치로 차단. task 소유를 확인한 명시적 이관에서 보수적인 controlled fence 설치 가능 | fence를 설치해도 FM03은 Block. task marker/새 등록 복구는 별도 |
| MG05 | controlled DB + marker 부재·손상·root 실종 | fence 유지, mode/version을 초기화하지 않음 | 계속 Block. 검증된 저장소 복구·명시적 재등록 후 재조회 |
| MG06 | 정상 task marker만 있고 snapshot/lease 없음·만료·소비됨 | task-wide 표시 유지 | 모드가 Controlled여도 미등록 실행은 차단 |
| MG07 | 파일 손상·ID 불일치·접근 오류·출처 불명 | 원본 보존·격리 상태로 보고, 자동 삭제/덮어쓰기/legacy 분류 금지 | 계속 Block. 검증 가능한 복구 입력을 별도로 확보 |
| MG08 | DB/commit 실패 또는 파일 생성·Sync·후속 승인 설치 실패 | 실패 노출. 이미 커밋된 controlled fence와 남은 marker 보존 | 부분 등록을 성공으로 보고하지 않음. 일반 경로 fallback 없음 |

기존 task의 explicit legacy 이관은 서버가 검증한 분류 목록과 FM04를 모두 요구한다. 목록은 단순 task metadata·모델 문자열·운영자가 요청에 적은 mode만으로 만들지 않는다. 기존 통제 작업일 가능성을 배제할 근거가 없으면 미등록/차단 상태를 유지한다. 신뢰 서버가 새 task 생성 경로에서 eager legacy를 등록하는 규칙과 기존 task의 이관을 구분한다.

이관은 기존 JSON 형식을 읽기 전용으로 수용하는 단계와 DB fence 등록을 분리한다. marker를 임의 새 형식으로 덮어쓰거나 실행 세대를 추정하지 않는다. 중복 이관은 같은 controlled fence/version을 반환하고, 반복 이관이 새 lease·추가 송신·예산 충전으로 이어지지 않아야 한다. DB controlled→legacy 전환·행 삭제·revision 초기화는 허용하지 않는다.

## 6. 등록·조회 경합과 활성화 조건

현재 `registerControlledRead`는 DB fence 배선 없이 파일 marker부터 기록한다. 아래 fence→marker→lease 순서는 후속 연결의 요구사항이며 현재 런타임에 이미 적용된 순서가 아니다.

- DB reader는 기존 `READ COMMITTED`, task row 잠금 뒤 별도 mode 조회를 유지한다. 저장 경로의 순서는 기존 evidence advisory lock → task row → 후속 finding/판정이다. reader의 transaction은 admission/저장 완료까지 유지하고 Decision을 다음 동작에 캐시하지 않는다.
- **파일 writer와 reader가 같은 task 등록 동기화에 참여해야 한다.** reader callback 안에서 새 transaction을 시작하거나 evidence/global lock·등록 완료를 기다리지 않는다. 기존 `r.mu` 안에 DB task 잠금을 추가하는 식으로 잠금 순서를 역전하지 않는다. runtime 잠금 조립은 후속 코드 경로 검토·경합 시험의 대상이다.
- **후속 어댑터의 등록 규칙:** controlled 등록은 DB fence commit → task 잠금 아래 marker 생성/검증 → 별도 승인/lease 설치 순서다. fence를 먼저 커밋하므로 파일 작업까지의 틈은 DM04/09의 차단 상태다. 파일 작업이 실패해도 fence를 rollback한 것처럼 처리하거나 파일을 지워 legacy로 되돌리지 않는다.
- **후속 어댑터의 marker 생성 규칙:** marker 생성 단계는 fence commit 이후 같은 task row를 다시 잠근 transaction에서 수행한다. 이는 reader callback 안의 중첩 transaction이 아니다. 파일 Sync/후속 DB commit 실패가 파일을 되돌리지 못한다는 전제로 남은 상태를 재조회한다. task marker만 남으면 FM02이며 실행 lease 부재로 실행은 차단한다.
- 기존 active Worker를 멈추지 않은 채 mode를 바꾸거나, DB 잠금에 참여하지 않는 구버전 파일 writer와 새 reader를 함께 활성화하지 않는다. 단일 프로세스 mutex만으로 다른 프로세스와 DB/file 원자성을 주장하지 않는다.
- 다중 서버는 모든 관련 writer/reader가 같은 DB task 잠금과 신뢰 state 저장소를 사용하는 것을 검증한 뒤 지원한다. 파일 존재를 두 번 확인한 사실이나 fd 고정만으로 등록 경합·복원·root 교체를 해결했다고 표시하지 않는다.

원래 실행·소유·승인·run/reset·KG/근거 revision·issuance 검증은 각 후속 어댑터에서 수행한다. 현재 marker에 그 필드가 없다는 이유로 파일에 새 권한을 발급시키거나 이 문서의 mode 분류를 실행 승인으로 확대하지 않는다.

## 7. reader 검증 상태와 후속 필수 시험

아래 MT01~10은 reader와 통합 경계의 수용 기준이다. [reader 계약의 검증 경계](./task-mode-marker-reader.md#검증-경계)에 기록된 합성 파일 시험과 아직 실행하지 않은 DB·이관·admission/저장 통합시험을 구분한다. 이번 문서 수정에서 그 제품 시험을 재실행한 것은 아니다.

| ID | 합성 조건 | 기대 결과 |
|---|---|---|
| MT01 | 정상 T+I / T만 / 둘 다 정상 부재 | FM01/02/04, 정상 대조군으로 present와 clear 모두 확인 |
| MT02 | task 파일 부재 + 다른 intent 파일 잔존 | clear 금지, FM03 |
| MT03 | root 삭제·새 빈 디렉터리 대체·부모 링크·권한 불량·비 Linux | 정상 부재로 바꾸지 않음, FM05 |
| MT04 | task/intent JSON·ID·controlled·파일 타입/크기·읽기/close 각각 오류 | 부분 present/clear 금지, FM06 |
| MT05 | 목록 누락·상한 초과·불명확 파일명·파일 소멸·취소 | FM07/08, 정상 다른 task 파일과 대상 파일을 구분 |
| MT06 | legacy 등록 ↔ marker 생성, 양방향 barrier 경합 | task 잠금 선후관계 보장, legacy/clear 확인 후 뒤늦은 통제 파일로 우회 금지 |
| MT07 | controlled fence commit 뒤 crash·task 파일만 Sync 뒤 실패·승인 설치 실패 | legacy 실행 0, fence 보존, 새 lease 없으면 송신 0 |
| MT08 | 같은 task 이관 반복·동시 이관, 다른 task 독립 진행 | mode/version 멱등, 새 승인·송신·예산 충전 0 |
| MT09 | DB 조회/commit 장애·잘못된 isolation·닫힌 tx·old writer 혼재 | 부분 성공/fallback 0, 혼재 상태 활성화 거부 |
| MT10 | 오류의 문자열/JSON/모델·공유 로그 출력 | 원문 경로·파일·DB 진단·시크릿 0, 안전한 범주만 노출 |

**기구현 범위:** MT01·02·04·05와 MT10의 파일 reader 오류 투영은 합성 파일 회귀로 다뤘다. MT03의 root 교체·권한·경로·비지원 차단도 reader 범위에 포함되지만, 비 Linux 실행·재기동 root 신뢰 회복·운영 저장소의 연속성 증명은 완료하지 않았다. MT06의 callback scope 오류 검사는 있으나 실제 PostgreSQL writer/reader 양방향 barrier는 미실행이다. MT07~09의 crash·이관·DB 장애·구버전 writer 혼재와 MT10의 실제 API/모델·DB 오류 출력은 서버 통합 후속이다.

이번 문서 검증은 기존 `taskmode` 코어와 DM01~06의 일치, 링크·표 ID·변경 범위 확인으로 한정한다. 이미 구현된 reader에 신뢰 root·등록 잠금 scope를 공급하고 DB mode reader → Engine/admission·최초 finding/retest 저장으로 연결하는 작업은 별도 후속이다.

**확인한 결과:** 문서에서 추출한 DM01~06의 Route/Reason 6조합이 실제 `taskmode.Decide`와 일치했고, 기존 `taskmode` 최하위 사례 37개가 실패·skip 0으로 통과했다. 링크 9개와 표 ID 35개의 유일성을 확인했다. 독립 문서 리뷰의 링크·표 범위 설명 보완 후 차단 지적은 없다. 이는 이 문서의 당시 DM 검증 결과이며 이후 별도 reader에 추가된 합성 파일 회귀와 구분한다. MG 이관·MT의 실제 DB 경합/서버 통합을 실행한 결과는 아니다.

## 8. 서버 연결의 미결정 항목 — 팀 리뷰안

2026-11-03 · 이동찬 · **권장 제안, 미확정·미구현**

기준은 `dev` `f2c062bd596940027b184a683e69b414cf0cbb0c`다. 위 FM/DM/MG 판정표를 유지하면서, 아직 없는 root 신뢰 공급자·등록 직렬화·admission 연결을 아래처럼 조립하는 안을 제안한다. [DB 모드 reader](../../db/task_mode.go)와 [파일 reader](../../server/controlled_read_marker_reader.go)는 각각 구현돼 있지만, 기준 dev의 `server/`에는 `RegisterTaskMode`·`TaskModeReader`·`taskmode.Resolve` 호출이 없다. [등록 runtime](../../server/controlled_read_runtime.go)은 현재 `r.mu` 안에서 파일·snapshot·상태 변경을 수행하므로 그 잠금 안에 DB 대기를 단순 추가하면 안 된다.

[Preview REST 수명안](./e2-preview-rest-proposal.md)은 별도 검토 범위이며 이 문서는 전송기 준비 시점·REST 응답·공유 예산 발급을 정하지 않는다. E③ 판정 payload·KG snapshot·보존 기간도 [저장 게이트](./e3-storage-gate.md)의 검토 대상이다. 여기서 결정하는 것은 **어떤 모드 경로로 작업을 보낼 수 있는지와 그 분류를 언제까지 보호하는지**다.

### 8.1 결정표

| ID | 선택과 권장안 | 확인 담당 · 수용 근거 |
|---|---|---|
| MD01 | **초기에는 배타적으로 소유하는 서버 하나에서 연결.** 다중 서버가 같은 task를 admission하는 방식은 분산 실행 소유권을 별도로 검증한 뒤 지원한다. 단일 프로세스 mutex가 DB/file 전체를 보호한다는 주장은 하지 않는다. | 이동찬(서버 전환 구현·결정), 최민준(저장/재검증 경계·회귀 조건 리뷰): 기존 서버/구버전 writer가 정지됐다는 전환 절차와 중복 서버 실행 거부/격리 시험. 단독 실행을 보장할 수 없으면 새 연결 활성화 보류. |
| MD02 | **host가 소유하는 장기 root handle과 재시작 검증 공급자.** 프로세스 동안 같은 열린 root fd를 유지한다. 재시작 때는 운영자가 검증한 DB·state 저장소의 배치/세대 결속과 허용 writer 정보를 받아야 하며, 자동으로 현재 경로를 열어 새 신뢰 anchor로 승인하지 않는다. 검증된 외부 공급자가 없으면 유지보수 상태에서 명시적 재확인 후 시작하는 안을 권장한다. | 이동찬(구현 및 12/18 설계검토 범위의 운영 책임자 겸임 권장): 공급자가 확인할 비민감 배치/저장소 참조, DB identity, 교체/복원 이력과 책임자를 정한다. exact 0700/0600·inode 비교만으로 재시작 연속성이나 모델 UID 분리를 입증한 것으로 보지 않음. |
| MD03 | **coordinator만 DB/file callback을 조립.** caller가 준 `locked=true`·`clear=true`나 임의 `CheckScope`를 받지 않는다. coordinator가 만든 살아 있는 transaction·task ID·root handle에 reader를 결속하고, 모든 marker writer가 같은 DB task 잠금을 사용하도록 전환한다. | 이동찬: callback 내부 새 transaction/잠금 대기 0, 다른 task·닫힌 tx·만료된 scope 거부, 실제 PostgreSQL lock wait로 writer/reader 직렬화 확인. callback 자체는 SQL 잠금의 인증서가 아님. |
| MD04 | **task 전체를 중단·drain한 뒤 controlled 전환.** 해당 intent 하나만 확인하지 않고 planner가 예약한 작업, 아직 시작 전인 Worker, 진행 중인 Worker/자식 실행과 **Main-agent chat**을 함께 추적한다. chat은 ordinary task pause를 무시하고 `chatMu`·`chatBusy`·`inflightCount`로 별도 직렬화하므로, 전환 barrier는 chat admission도 닫고 진행 중 chat의 종료를 확인해야 한다. 중단 완료가 불확실하면 fence/lease 공개를 진행하지 않는다. | 이동찬: admission과 전환의 양방향 barrier 시험, 대기 중 Worker가 뒤늦게 일반 경로로 시작하는 건수 0. HTTP `StopAndWait`만으로 일반 Worker/자식 프로세스·Main-agent chat 종료까지 증명하지 않음. |
| MD05 | **새 task의 명시적 legacy는 생성 transaction에서 eager 등록.** task INSERT와 mode 등록·검증된 FM04를 같은 transaction에 넣고 둘 다 commit된 뒤 scheduler에 공개한다. 기존 task는 §5의 명시적 이관 목록만 사용한다. | 이동찬·최민준: 현재 [task 생성](../../db/tasks.go)·[Manager 공개](../../server/manager.go)의 transaction 경계를 조정하고 rollback/commit 불명확 시 Worker 공개 0 확인. 미등록 task의 admission에서 legacy를 보충하지 않음. |
| MD06 | **장애 시 차단을 유지하고 새 transaction에서 재확인.** DB commit 결과 불명확·root 신뢰 유실·파일 Sync 실패를 성공이나 legacy로 바꾸지 않는다. 같은 요청의 재시도는 기존 mode/version·파일을 먼저 확인하며 새 승인·RunRef·예산을 발급하는 복구로 확대하지 않는다. | 이동찬: 실패 지점별 상태/반환값, 재시작·동시 재시도 시험. 초기화/이관 완료 플래그 하나로 개별 task의 분류 검증을 생략하지 않음. |

12/18 설계검토 범위의 **운영 책임자는 PM·통합 담당 이동찬이 겸임하는 안을 권장**한다. 배치·root 신뢰·허용 writer 확인의 담당 후보를 지정한 것이며 팀 역할·실제 운영 권한의 확정이나 운영 활성화 승인이 아니다. 해당 담당과 수용 근거의 팀 확인은 남아 있다. 최민준은 저장/재검증 경계·회귀 조건 리뷰어이며 서버 전환의 구현 결정자나 운영 책임자가 아니다.

MD02는 새 파일 형식이나 서명·배포 권한의 확정안이 아니다. 현재 marker에는 저장소 세대/신뢰 증명이 없으므로, 운영자가 지정한 경로 문자열만 받는 구현으로 이 조건을 충족했다고 보고하지 않는다. 실제 OS/DB 권한 변경과 운영 시작 승인은 별도다.

### 8.2 coordinator와 전환 순서 제안

여기서 coordinator는 후속 서버 코드의 역할명이며 구현된 타입/API가 아니다. 짧은 서버 mutex로 task별 admission 차단 상태와 현재 전환 소유 token을 관리한다. 등록 token에는 대상 task/intent를 결속한다. token은 서버 내부 동기화용이며 모델/REST에 넘기는 실행 권한이 아니다. 파일·DB I/O·drain 동안 mutex를 계속 잡지 않고, 같은 task의 다른 전환/재개는 대기열에 숨겨 두지 말고 차단 상태를 확인해 거부한다.

1. **시작/전환 준비:** 자동 resume·복구 Worker를 열기 전에 새 admission을 막는다. MD01/02를 확인하고 root handle을 확보한다. 새 DB와 state를 처음 만드는 명시적 bootstrap, 기존 설치의 재시작, 손상 저장소 복구를 구분한다. 기존 설치의 root가 없으면 `MkdirAll`로 빈 root를 만들어 legacy로 해석하지 않는다. 신뢰 확인 실패 시 blocked 상태를 유지한다.
2. **중단 요청:** task를 paused로 확인하고 coordinator에서 해당 task의 새 admission과 **pause를 따르지 않는 Main-agent chat admission**을 함께 닫은 뒤 전환 token을 발급한다. planner·예약/활성 Worker·자식 실행·진행 중 chat의 종료 handle을 수집하고 mutex를 놓는다. `chatMu`·`chatBusy`·`inflightCount`의 별도 직렬화와 barrier를 연결하며, 관리 호출 자신의 in-flight 계수와 실제 실행의 계수를 구분해 종료를 확인한다. pause나 `chatBusy=false` 하나로 drain 완료를 추정하지 않는다. 취소·stop·drain은 DB/task/evidence/registry 잠금 밖에서 끝낸다. timeout·확인 불가는 새 등록 성공으로 처리하지 않는다. 재진입 시 같은 token과 작업 목록이 여전히 유효한지 확인한다.
3. **fence 커밋(transaction A):** `READ COMMITTED`에서 실제 task의 소유·삭제/archive·종료 상태를 확인하고 `RegisterTaskModeTx(controlled)`를 호출한다. commit 성공을 확인한 뒤 다음 단계로 간다. commit 오류는 결과가 불명확할 수 있으므로 pending을 공개하지 않고 새 transaction에서 정본을 읽는다. 이미 controlled라면 기존 version을 유지한다. 이전 단계의 quiescing 상태는 에러로 자동 해제하지 않는다.
4. **marker 생성(transaction B):** 같은 task row를 다시 잠그고 controlled mode/version과 전환 token·root 신뢰를 확인한다. 그 잠금 아래 intent가 실제로 같은 task/exploration에 속하는지 재확인하고, 기존 task/intent marker 형식으로 생성·Sync한 뒤 같은 root의 reader로 FM01/02를 확인한다. 기존 `makeRunnable` 역할인 intent 상태 CAS(open→open 포함)도 **B의 같은 transaction에 결속**해 수행한다. 현재 DB callback을 그대로 mutex 밖에서 부르는 것으로 대체하지 않고 transaction을 받는 경계를 설계해야 한다. callback은 이 transaction/scope를 빌려 쓰며 중첩 transaction을 열지 않는다. 파일 Sync가 성공해도 DB commit/취소가 실패하면 파일과 fence를 보존하고 pending 공개는 하지 않는다. root 신뢰가 사라지면 coordinator의 새 admission도 닫는다.
5. **등록 공개:** B의 commit과 입력 검증이 성공한 뒤에만 짧은 registry 임계구간에서 기존 token·paused/비실행 상태·root 유효성을 확인해 일회 등록을 공개한다. DB/file 작업을 이 임계구간으로 옮기지 않는다. CAS가 commit됐어도 취소된 token으로 pending을 공개하지 않는다. 새 승인·예산·RunRef/전송기의 준비 여부는 각 E② 계약으로 추가 검사하며, 모드 분류 성공이 그 성공을 대신하지 않는다. 불일치면 공개하지 않고 task를 차단 상태로 남긴다.
6. **각 admission:** 별도 `READ COMMITTED` transaction에서 `taskmode.Resolve(..., db.TaskModeReader(tx, ...))`를 호출하고 error와 Route를 모두 검사한다. task 잠금을 유지한 채 짧은 coordinator 임계구간에서 route·세대·시작 전 Worker handle을 예약 목록에 넣고, 임계구간을 벗어나 commit한다. commit 성공 뒤에도 token/차단·취소 상태를 다시 확인해 dispatch한다. 그 사이 전환/취소가 이기면 이미 추적된 handle을 취소하고 실행하지 않는다. commit 실패 시 예약을 취소하며, commit 성공 전 Worker 실행/target 송신은 0이다. DB 잠금은 네트워크 요청·전체 Worker 실행 동안 유지하지 않는다. commit 직후 아직 시작하지 않은 예약 Worker도 coordinator가 추적하며, 다음 controlled 전환의 drain에 반드시 포함한다. 실제 시작 때 취소/차단 상태를 확인해 취소된 예약을 실행하지 않는다.
7. **각 저장:** 기존 evidence advisory lock → task row → mode 판별 → 후속 finding/판정 순서를 유지하고 같은 transaction에서 저장까지 끝낸다. 저장 중 coordinator가 가진 전환 token을 기다리거나 새 admission을 시작하지 않는다. DB/file 오류는 저장 거부이며 E③ payload·발급/연결의 추가 조건은 저장 게이트 계약을 따른다.

위 A/B는 DB와 파일을 원자적으로 묶는 분산 transaction이 아니다. A 뒤 crash는 controlled fence만, B 도중 실패는 task marker만 남을 수 있다. 모두 새 실행 lease가 없는 차단 상태로 수렴시키며 marker 삭제·fence downgrade·예산 환불로 되돌리지 않는다. 등록 요청 context의 취소는 아직 공개되지 않은 등록을 막아야 하고, 이미 종료한 HTTP 요청 context를 장기 root/실행 수명으로 재사용하지 않는다.

**취소·삭제 직렬화:** 등록 교체·대상 intent 취소/삭제는 DB mutation **전에** 짧은 coordinator 임계구간에서 해당 task/intent 등록 token을 무효화하고 pending을 회수한다. 이어 mutex 밖의 transaction에서 task → intent 순서로 잠가 소유·상태를 재검사하고 변경한다. 기존 취소 helper가 intent만 잠그는 경로도 함께 바꿔야 한다. DB 변경이 실패해도 무효화한 token을 자동 복구하지 않는다. B의 CAS commit 직후 취소가 끝나더라도 단계 5는 token 불일치로 공개를 거부한다. 새 등록은 새 token으로 전체 검사부터 시작하며, 이 token은 DB mode/version을 대체하지 않는다.

**잠금 책임:** `r.mu`·`chatMu` 등 서버 mutex를 잡고 DB task/evidence 잠금이나 chat 종료·drain을 기다리지 않는다. coordinator 상태 확인은 짧게 끝내고, DB/file callback은 이미 확보한 scope만 검사한다. 반대로 task/evidence 잠금을 잡은 호출에서 coordinator 전환 완료·drain을 기다리지 않는다. 기존 `cancelPendingWork`처럼 mutex 안에서 DB mutate를 부르는 경로까지 함께 조사·조정해야 하며 등록 함수 하나만 바꿔 잠금 순서 완료로 선언하지 않는다. 저장 경로가 evidence 잠금을 요구하면 항상 task 잠금보다 먼저 얻는다.

**root/소유권 상실:** 새 admission과 등록 공개를 즉시 막고 pending 등록을 무효화한다. 추적 중인 실행에 중단을 요청하고 모든 잠금 밖에서 종료를 확인한다. 이미 나간 요청을 되돌릴 수 있다고 보고하지 않는다. 재시작 후 pending/Worker·Decision을 복원하지 않으며, 검증된 root/정본과 새 등록을 확보한 뒤 개별 admission을 다시 검사한다. 구버전 writer·자동 resume이 이 coordinator를 우회할 수 있으면 전환을 활성화하지 않는다.

### 8.3 구현 전 수용시험 계획

아래 ACT01~10은 **미실행 계획**이며 기존 FM/DM/MT 시험 수에 합산하지 않는다. 실제 전용 PostgreSQL과 합성 파일·예약 Worker를 사용하고, 실 대상·운영 저장소·자격 원문은 쓰지 않는다.

| ID | 교차 조건 | 수용 기준 |
|---|---|---|
| ACT01 | Worker 및 Main-agent chat admission 직전/commit 직후/시작 직전에 controlled 전환 | 양쪽 선후관계마다 일반 Worker·chat의 뒤늦은 시작 0, ordinary pause와 별도인 chat gate 차단 확인, 예약 handle 누락 0, 전환 후 새 송신 0 |
| ACT02 | 활성·예약·자식 실행·Main-agent chat이 있는 task를 pause/전환, drain timeout | chatBusy/inflightCount와 종료 handle로 chat까지 종료 확인 전 marker/새 pending 공개 0, pause만으로 성공하는 변이 검출, mutex·DB 잠금 밖 drain, 불명확하면 blocked 유지 |
| ACT03 | A commit 실패/응답 유실, A 뒤 crash, B task marker만 Sync 뒤 실패 | 정본 재확인 전 공개 0, fence/version·원본 파일 보존, legacy fallback·자동 환불 0 |
| ACT04 | B의 marker writer ↔ TaskModeReader 양방향 DB barrier | 같은 task row lock 대기 관측, 부분 clear/present 성공 0, callback의 새 transaction/역순 잠금 0 |
| ACT05 | 새 task 생성 중 legacy 등록 실패·rollback·commit 불명확 | task/mode 원자성, scheduler/Worker 공개 0, lazy legacy 등록 0 |
| ACT06 | 다른 task·닫힌 tx·취소·이전 전환 token으로 reader 호출 | zero marker/Block 또는 안전한 오류, 살아 있는 다른 task 정상 대조군 유지 |
| ACT07 | 재시작/root 교체·소멸·같은 경로의 빈 root·복원 세대 불일치 | 기존 path 재개방만으로 신뢰 승인 0, 자동 root 재생성·pending 복원 0 |
| ACT08 | 등록 ↔ resume/cancel/delete, 특히 **B의 CAS commit 직후·pending 공개 직전 취소**, 저장 ↔ 전환을 barrier로 교차 | 소유·mode 재검사, 고아 pending·deadlock·무기한 대기 0, generic 저장 우회 0 |
| ACT09 | 구버전 writer 또는 두 번째 서버, root/서버 소유권 상실 | 초기 지원 범위 밖 구성의 활성화 거부, 새 admission 차단과 기존 실행 종료 확인 |
| ACT10 | 오류·이관/전환 결과의 API/모델/로그·반복 재시도 | 고정 범주·비민감 task 참조/단계/건수만, 경로·DB 원문·snapshot/자격 0; 추가 승인·RunRef·예산 발급 0 |

팀 리뷰에서는 MD01~06 각각에 **채택/수정/보류, 확인 담당, 필요한 수용 증거**를 남기는 방식을 제안한다. 이동찬이 coordinator·DB/file 경합 구현을 맡고 최민준이 저장/재검증 경계와 회귀 조건을 검토한다. 운영 책임자 겸임 권장안의 이동찬은 배치·root 신뢰와 writer 권한의 확인 근거를 준비하며, 팀 확인과 실제 운영 활성화는 별도로 남긴다. 모두 확인한 뒤 별도 코드 PR로 연결하며, 이 문서 게시만으로 실행/저장 통제 활성화를 허용하지 않는다.
