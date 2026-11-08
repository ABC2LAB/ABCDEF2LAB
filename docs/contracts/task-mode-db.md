# 실행·저장 공통 mode DB — 구현 초안

2026-10-30 · 이동찬 · 검토/피드백용. [순수 모드 판별](../../taskmode/mode.go)

상태: **실제 PostgreSQL reader·단방향 등록·DB 통합 시험 구현. Engine/최초 finding 저장 연결과 기존 파일 marker 이관은 후속.** 팀 최종 저장 규격 확정이나 기존 ARTEX 전체 통신/저장 강제 완료를 뜻하지 않는다.

## 1. 소유와 초기화

- [`db/task_mode.go`](../../db/task_mode.go): DB reader·등록 API.
- [`db/task_mode_schema.sql`](../../db/task_mode_schema.sql): 별도 mode 테이블과 전이/삭제/archive guard.
- [`db/db.go`](../../db/db.go): 기존 schema migration 잠금 안에서 새 DDL 적용·deadlock 재시도.
- [`db/task_mode_test.go`](../../db/task_mode_test.go): 등록·조회·경합·오류의 PostgreSQL 통합 시험.

기존 server/Worker·감사 CLI·평가·UI·KG/idor 코드는 수정하지 않는다. 새 의존성·루트 설정 변경도 없다. 스키마 재적용은 멱등이며 **기존 task를 legacy로 backfill하지 않는다.**

## 2. 정본과 단방향 전이

가칭 `task_control_modes(task_id, mode, version)`를 실제 별도 테이블로 둔다. mode가 모델 편집 task metadata에 섞이지 않는다. task_id는 양수 PK, mode는 `explicit_legacy/controlled`, version은 양수다. cascade FK가 없으며 mode 행 삭제/비우기를 금지한다.

| 현재 | 요청 | 결과 |
|---|---|---|
| 미등록 | explicit_legacy | marker clear 검증 후 version 1 |
| 미등록 | controlled | marker보다 먼저 fence version 1 커밋 가능 |
| legacy | legacy | marker clear 재검증, 같은 version 반환 |
| legacy | controlled | version +1 |
| controlled | controlled | 같은 version 반환 |
| controlled | legacy | 거부, zero Record와 오류 |

Go API와 DB trigger가 다운그레이드·task_id/version 재설정·mode 행 DELETE/TRUNCATE를 거부한다. 이 trigger는 mode 전이 제약이며 직접 SQL의 모든 올바른 전환을 reader와 직렬화하는 기능은 아니다. **정상 writer는 반드시 아래 Go 등록 API를 사용한다.** 같은 DB 권한의 관리자 SQL/trigger 해제를 제어하는 배포 권한 분리는 이번 범위가 아니다.

task 삭제 후 mode 행만 남기는 것으로 finding 출처가 보존되는 것은 아니다. 따라서 immutable provenance와 archive 지원이 완성되기 전에는 **controlled task의 hard/soft delete·archive를 DB에서 거부**한다. 기존 archive 진행/삭제 상태의 작업에는 새 mode를 등록하거나 실행 경로를 허용하지 않는다.

관리자 SQL의 `TRUNCATE tasks RESTART IDENTITY`도 허용된 초기화 절차가 아니다. `task_control_modes`에는 cascade FK가 없고 mode 행 삭제도 차단되므로, tasks만 비우고 ID를 재사용하면 새 task가 이전 task의 mode/version fence를 물려받을 수 있다. 테스트·운영 초기화는 전용 데이터베이스를 폐기·재생성하는 방식으로 수행하며, 공유 DB에서 이 truncate를 실행하지 않는다.

## 3. 등록 API

```go
record, err := pg.RegisterTaskMode(ctx, taskID, mode, markerReader)
// 또는 caller가 가진 READ COMMITTED transaction에서:
record, err := db.RegisterTaskModeTx(ctx, tx, taskID, mode, markerReader)
```

첫 메서드는 transaction을 생성하고 **commit 성공 뒤에만** Record를 반환한다. begin/조회/갱신/commit 실패는 zero Record와 오류다. Tx 함수의 반환값은 아직 미커밋이며 caller가 commit하기 전에 승인 파일·실행 lease를 발행하면 안 된다. rollback은 등록을 취소하고 retry는 기존 mode/version을 보존한다.

서버는 legacy 작업을 **task 생성 시점에 미리(eager) 등록**한다. 미등록 작업을 admission 시점에 뒤늦게(lazy) legacy로 등록하지 않는다. 상태 디렉터리가 유실되어 marker가 clear로 읽혀도, 미등록 controlled 작업을 legacy로 강등해서는 안 된다. 이 생성·admission 배선은 후속 구현이다.

등록은 실제 task row를 `FOR UPDATE`로 잠그고 deleted/archived/archive 진행 상태를 확인한 뒤 mode를 읽고 쓴다. 미존재 task/잘못된 mode·ID·취소는 거부한다. canonical controlled fence가 있다고 실제 marker·실행 허가까지 준비된 것은 아니다.

legacy 등록은 server-owned marker reader가 같은 task의 **정상 확인된 clear**를 반환해야 한다. unknown/present/잘못된 ID·enum·조회 오류·nil reader면 거부한다. controlled 등록은 marker가 없어도 fence부터 설치할 수 있지만 판별 결과는 marker가 준비될 때까지 Block이다. 기존 실행의 중지/일시정지까지 이 API가 대신하지 않으며 서버 admission 배선에서 수명주기를 맞춰야 한다.

## 4. reader와 잠금 수명

```go
reader := db.TaskModeReader(tx, markerReader)
decision, err := taskmode.Resolve(ctx, taskID, reader)
// err == nil이어도 route가 Block일 수 있다.
// tx는 admission 또는 저장 동작이 끝날 때까지 유지한다.
```

reader는 caller의 살아 있는 **READ COMMITTED** transaction에서 같은 task row를 잠근 뒤, **별도 SQL 명령**으로 mode를 읽고 marker를 확인한다. 누락은 legacy가 아닌 zero Record, 조회 오류/없는 테이블/닫힌 transaction/marker 오류는 Block이다. 기존 순수 판별의 legacy+clear / controlled+present 조건을 바꾸지 않는다.

이 잠금·별도 조회 규약은 READ COMMITTED의 명령별 새 snapshot을 이용한다. REPEATABLE READ 계열에서 task는 잠금만 있고 값은 안 바뀌면, mode가 별도 행이라 대기 뒤에도 옛 snapshot을 읽을 수 있다. 따라서 다른 isolation은 명시적으로 거부한다. 이는 PostgreSQL 문서에 근거한 프로토콜 선택이며 [격리 수준](https://www.postgresql.org/docs/16/transaction-iso.html)·[행 잠금](https://www.postgresql.org/docs/16/explicit-locking.html)을 참조한다.

저장 caller는 **기존 evidence advisory lock → task row → 후속 finding/판정** 순서를 지킨다. marker callback은 task 잠금 안에서 실행되므로 evidence/global advisory lock·새 transaction·등록 완료를 역순으로 기다리면 안 된다. DB mode 등록과 실제 파일 marker 변경도 같은 task 등록 동기화를 지켜야 한다. 현재 live marker 파일 어댑터는 연결하지 않았다.

반환된 Decision을 tx 종료 후 캐시해서 새 동작에 재사용하지 않는다. Controlled는 통제 파이프라인 선택이며 요청·취약점 확정 권한 자체가 아니다.

## 5. 오류와 검증

일반 오류 문자열/JSON은 DB·marker 원문을 포함하지 않는다. `errors.Is/As` 진단 원인은 보존하지만 원인 Unwrap 결과를 모델/공유 로그로 내보내지 않는다. mode unknown·DB 장애를 일반 경로 허용으로 바꾸지 않는다.

시험은 공유 개발 DB 대신 전용 PostgreSQL16 컨테이너에서 실행했다. `ARTEX_TASK_MODE_TEST_DSN`이 없는 경우 시험은 명시적 skip이고, 값이 있지만 연결/초기화가 실패하면 fail이다. fixture는 두 DSN을 `pgx.ParseConfig`로 파싱해 database 이름이 같고 primary/fallback host·port 집합이 겹치는지 비교하며, scheme 별칭, 기본 포트, host 대소문자·trailing dot, localhost/loopback 표기, 사용자·SSL·기타 query 순서는 DB identity에서 무시한다. 파싱 실패는 fail이다. 이 identity guard는 DNS 조회나 네트워크 연결을 하지 않으며, 라우터 뒤의 물리 cluster가 같은지 증명하지 않는다. Unix socket 경로는 대소문자를 보존하므로 별도 표기 차이는 보수적으로 구분한다. 실제 검증에서는 일반 DB와 다른 전용 DSN을 주입하고 skip 0을 확인한다.

시험 범위: 정상/멱등 등록·역전환/marker 오류 거부, 미등록/없는 task·테이블·닫힌 tx·취소 시 Block, rollback/commit 실패, schema 재적용, 삭제/archive guard, 등록→조회/조회→등록 경합, 서로 다른 task 독립성, 20개 동시 등록, legacy/control 양 순서 경합, callback 동안 task 잠금 유지. 실패 모드는 실제 DB 변화량과 backend lock wait로 관측한다. 테스트용 marker 함수는 실제 파일 어댑터의 검증 완료를 뜻하지 않는다.

**확인한 결과:** Go 1.26.3에서 등록/조회 대상 최하위 사례 59개와 race 59개, 기존 task/finding/retest 회귀 21개가 통과했고 모두 skip 0이다. 기존 순수 mode 코어 37개도 다시 통과했다. vet/build·gofmt/diff 검사를 확인했다. 20개 동일 controlled 등록은 성공 20·mode 행 1·version 1이었다. 독립 리뷰의 archive 재지정/삭제 지적 1건을 실제 실패 2건으로 재현 후 수정했고, 재리뷰는 actionable 0건이었다. 이 수치는 mode DB 범위이며 최초 finding 확정 게이트나 전체 E③ 시험 완료 건수가 아니다.
