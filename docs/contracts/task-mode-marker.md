# 실행·저장 공통 marker 조회·이관 판정표

2026-11-02 · 이동찬 · 초기 구현 검토를 바탕으로 한 후속 어댑터 기준

상태: **후속 marker 어댑터의 구현 기준 초안. 실제 어댑터·이관기·실행/저장 배선은 미구현이다.** 이 문서에서 채택한 규칙과 기존 코드의 동작을 구분한다. 팀의 최종 리뷰·합의를 이미 마쳤다는 뜻이 아니다.

범위는 task의 `Legacy / Controlled / Block` 경로 분류다. 실행 승인·새 run의 유효성·취약점 확정 권한은 부여하지 않는다. 기존 [mode DB 계약](./task-mode-db.md)과 [E③ 저장 게이트 §4](./e3-storage-gate.md#4-통제-작업-판별--선행-조건)의 정본·단방향 전이·잠금 규칙을 유지한다.

## 1. 확인된 현재 구현

- [`markerFile`, `mark`, `taskMarked`, `marked`](../../server/controlled_read_store.go)는 private state의 파일을 사용한다. 기본 디렉터리는 `dataDir/e2-state`, 설정은 `ARTEX_E2_STATE_DIR`다. 경로·권한의 지원 범위는 [서버 runtime 계약](./e2-server-runtime.md#서버가-보유하는-승인-정보)을 따른다.
- task 파일은 `controlled-<task_id>.task.marker`, intent 파일은 `controlled-<task_id>-<intent_id>.marker`다. 생성기는 JSON에 `task_id` 문자열, `intent_id` 정수, `controlled` 불리언을 기록한다. task marker는 `intent_id=0`, intent marker는 양수이며 `controlled=true`여야 한다. 현재 task validator는 누락된 `intent_id`도 0으로 읽는다. 이 호환성은 새 reader에서도 유지하며, intent marker에는 양수 필드가 필요하다. task ID는 선행 0 없는 양의 십진 문자열이다.
- [`mark`](../../server/controlled_read_store.go)는 task marker를 먼저 만들고 intent marker를 만든다. [`registerControlledRead`](../../server/controlled_read_runtime.go)는 그 뒤 snapshot/profile을 읽는다. 뒤 단계가 실패해도 앞선 marker는 남을 수 있다.
- `taskMarked`는 task 경로의 `Lstat` 결과가 `os.ErrNotExist`면 `false, nil`을 반환한다. **부모 디렉터리가 사라진 경우의 ENOENT도 구분하지 않는다.** `marked`는 task marker가 있으면 해당 intent 파일을 검사하지 않고 true를 반환하고, task marker가 없으면 지정한 intent 한 개만 확인한다. 전체 intent marker 목록을 확인하는 API가 아니다.
- 존재하는 파일의 검증은 [`readPrivate/readPrivateAt`](../../server/controlled_read_private_linux.go)와 strict JSON을 사용한다. 마지막 부모 fd, 정확한 디렉터리 `0700`·일반 파일 `0600`, 링크/FIFO 거부, 4KiB 상한·읽기/close 오류를 검사한다. 비 Linux reader는 오류다. 이 검사는 파일이 없을 때 부모부터 확인하는 새 조회 절차를 대신하지 않는다.
- 초기화의 `mkdirPrivate`는 `MkdirAll`로 없는 디렉터리를 만들 수 있다. 다시 만들어진 빈 디렉터리는 이전 marker가 없었다는 증거가 아니다. 현재 runtime의 `r.mu`는 한 프로세스의 등록을 조율하며 DB mode reader와 파일 writer 전체를 직렬화하지 않는다.
- [`db.TaskModeReader`](../../db/task_mode.go)는 caller의 `READ COMMITTED` transaction과 task row 잠금을 사용하지만 현재 live marker callback은 미연결이다. 아래 파일 판정표가 기존 `taskMarked`에 이미 구현됐다고 표현하지 않는다.

## 2. 이번 판정표에서 고정하는 결정

1. **DB가 모드 정본이다.** 파일만 있거나 파일이 없어도 DB mode를 추정·수정하지 않는다. `explicit_legacy` 등록은 신뢰 서버가 분류한 새 task 생성 단계 또는 별도 승인된 이관에서만 한다. 미등록 task의 admission이 legacy를 뒤늦게 등록하지 않는다.
2. **`clear`는 전체 조회 성공의 결과다.** 검증한 state root에서 해당 task의 task/intent marker가 모두 없음을 등록과 일관되게 확인한 경우에만 반환한다. 단일 파일 ENOENT, 다른 intent 한 개의 부재, 불완전 목록, 초기화 성공은 그 근거가 아니다.
3. **task marker만 정상이어도 모드 보조 신호는 `present`다.** 등록 중 실패·만료·소비 후 남은 marker를 지우거나 legacy로 내리지 않는다. task marker는 새 미등록 intent도 통제 경로로 묶는다. 유효한 실행 lease가 있다는 뜻은 아니다.
4. **intent marker만 남으면 불일치로 차단한다.** task marker를 자동 생성해 정상으로 간주하지 않는다. 읽기와 복구/이관을 분리한다.
5. **오류는 부재가 아니다.** 조회/파싱/권한/경로/목록/동기화 오류는 `unknown`과 명시적 오류다. 유효 파일 일부를 찾았어도 나머지 조회가 실패하면 부분 성공을 반환하지 않는다.
6. **현재 marker 형식은 유지한다.** 이번 모드 분류에 새 파일 버전·run/reset·snapshot digest·서명 필드를 필수로 추가하지 않는다. DB `Record.Version`은 mode fence revision이며 파일·실행 세대와 동일시하지 않는다. 그 세대·승인·발급 증명은 별도 E②/E③ 경계의 책임이다.

## 3. 파일 조회 → MarkerState

아래는 **새 어댑터의 규칙**이다. `T`는 대상 task marker, `I`는 대상 task에 속한 모든 intent marker다. 정상 파일은 이름·JSON의 task/intent 결속, `controlled=true`, 기존 private reader 조건을 통과한 파일이다. 과거 intent 파일도 조회 대상이며 snapshot 만료·pending lease 소멸을 파일 부재로 취급하지 않는다.

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

## 7. 후속 어댑터의 필수 시험

아래는 **계획된 시험**이다. 이 문서 추가나 기존 모드 단위 시험 통과로 실제 파일·DB 경합 검증이 완료된 것으로 세지 않는다.

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

이번 문서 검증은 기존 `taskmode` 코어와 DM01~06의 일치, 링크·표 ID·변경 범위 확인으로 한정한다. 실제 파일 어댑터 → DB mode reader → Engine/admission·최초 finding/retest 저장 연결은 별도 후속이다.

**확인한 결과:** 문서에서 추출한 DM01~06의 Route/Reason 6조합이 실제 `taskmode.Decide`와 일치했고, 기존 `taskmode` 최하위 사례 37개가 실패·skip 0으로 통과했다. 링크 9개와 표 ID 35개의 유일성을 확인했다. 독립 문서 리뷰의 링크·표 범위 설명 보완 후 차단 지적은 없다. FM/MG/MT의 실제 파일·DB·경합 시험을 실행한 결과는 아니다.
