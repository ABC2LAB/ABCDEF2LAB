# 엔진 내부 효율 관측 집계

`cmd/engine-metrics`는 최적화 전후 비교를 준비하기 위한 **엔진 내부 관측 도구**다.
평가 러너·공식 평가 지표·run-record·채점기를 대체하지 않는다. E① 종료·재계획과
E④ 중복·재사용 로직, 엔진·서버·DB 구현 및 schema는 변경하지 않는다.
초기 확인 dev는 `88415fff88f1c66c4307ba370c360e6ad338efcc`였다. 작업 중 갱신된
`1adc767d11e77de7052950ef92a9d037c7aa7fa6`를 전용 브랜치에 반영했으며 미병합 코드 의존성은 없다.

## 실행

저장소 루트에서 실행한다. 기존 PostgreSQL 드라이버 pgx/v5를 직접 사용한다.
명시적으로 준비한 `ARTEX_ENGINE_METRICS_DSN` 환경 변수가 필수이며, DSN을 명령행 인자로
받거나 출력하지 않는다. 환경 변수에도 실제 계정·비밀값이 있으므로 공유하지 않는다.

```sh
# ARTEX_ENGINE_METRICS_DSN은 사전에 보호된 환경에서 설정한다.
go run ./cmd/engine-metrics --task-id 42 --timeout 10s
go run ./cmd/engine-metrics --exploration-id 17 --timeout 10s
```

두 selector 중 **정확히 하나의 양수 ID**만 지정한다. task는 현재 결속된 exploration으로
조회하며, exploration 선택은 task가 없는 exploration도 지원한다(`task_id: null`).
존재하지 않는 범위는 오류이며 정상 빈 보고서로 바꾸지 않는다. 존재하지만 intent/activity가
없는 범위는 건수 0과 `null` 통계를 반환한다. ID는 JSON에서 십진 문자열이다.

기존 ARTEX의 **public schema**만 지원한다. 다른 schema를 자동 탐색하거나 migration하지 않는다.
고정 SELECT만 수행하며 payload·summary·detail·metadata·요청/응답·계정 원문은 읽지 않는다.
초기화 시 쓰는 `db.Open`을 import/호출하지 않는다. 서버의 `ARTEX_PG_DSN`도 자동 사용하지 않는다.

## 조회 경계

하나의 실제 연결에서 read-only repeatable-read transaction을 열고 scope→intent→activity를
조회한 뒤 rollback한다. 연결의 search_path는 `pg_catalog`로 고정하고 관계는 `public`로 한정한다.
집계 전에 실제 적용된 search_path도 확인하고 다르면 실패한다.
연결 기본 transaction도 read-only로 요청한다. 이 설정은 해당 도구 연결에만 적용하며
운영/개발 DB role·설정·데이터를 바꾸지 않는다.

`--timeout`은 연결/조회에 적용하는 context deadline이다(기본 10초, 최대 1분, 양수).
caller 취소도 적용한다. rollback/네트워크 정리까지 포함한 절대 벽시계 SLA는 아니다.
실패 시 stdout에는 보고서를 내보내지 않고 stderr에 고정 JSON 오류와 nonzero exit를 반환한다.
driver 원시 오류·DSN·입력 인자는 출력하지 않는다. schema 누락/권한 거부/조회 실패를
0이나 정상 완료로 바꾸지 않는다. stdout 자체의 I/O 실패 때는 이미 전달된 bytes를 되돌릴 수 없다.

## 읽을 수 있는 값

| JSON 영역 | 의미 |
|---|---|
| `intents.recorded_count`, `states` | 선택 exploration의 현재 `kind='intent'` 행 수. open/running/paused/done/blocked/exhausted/stopped/deleted 및 NULL·알 수 없는 상태를 별도 계수 |
| `creation_to_completion_including_wait` | 유한한 생성·완료 시각이 있고 완료≥생성인 현재 행의 count/min/max/mean/sum 초. **대기를 포함하며 실제 실행시간이 아님** |
| 시각 누락·역전·비유한 건수 | created/completed NULL, terminal(done/blocked/exhausted/stopped)의 completed NULL, 역전 또는 무한 timestamp를 구분. 통계에서 제외한 이유를 보존 |
| `all_recorded_activity` | 선택 exploration의 모든 기록 activity 수, `worker='planner' AND kind='round'` 기록 수, 4개 token 필드별 합계/NULL 건수/음수 건수 |
| `recorded_result_activity` | 같은 집계의 `kind='result'` 부분집합. result **기록 수**이며 provider 호출 수가 아님 |

token의 `recorded_sum`은 NULL을 제외한 **기록값 합계**다. 일부만 기록됐다면 전체 사용량이
아니다. 기록값이 하나도 없으면 `null`이며, 기록된 0의 합은 0이다. NULL은 미기록이며 0으로
치환하지 않는다. 음수는 숨기거나 clamp하지 않고 음수 건수와 실제 기록 합에 그대로 반영한다.
원본은 일반적으로 terminal result activity에 token을 기록하므로 비-result의 NULL이 많다고
그 자체로 수집 장애라고 판단하지 않는다. all/result 영역은 겹치는 집합이므로 더하지 않는다.

완료 시각 통계는 done만의 통계가 아니다. blocked/stopped 등도 완료 시각을 가질 수 있다.
sum은 각 행의 소요시간 합이며 동시 실행의 전체 벽시계 시간이나 순수 실행시간이 아니다.
terminal 시각 누락과 전체 시각 누락은 부분집합 관계이며 독립적으로 더하지 않는다.

## 복원할 수 없는 값

- 정확한 LLM 총 호출·재시도·동시성·dedup/reuse 횟수는 반환하지 않는다.
  [`server/engine.go`](../server/engine.go)의 `llmCalls`는 현재 진행 중인 호출 수이며 누적 호출 수가 아니다.
- round/result 수는 **관측된 기록 수**다. 기록 누락·중복·오류가 있는 실제 실행을 보정하지 않는다.
- 재개/재실행 시 `completed_at`이 초기화되거나 다시 찍힐 수 있어 과거 attempt별 시간을 복원할 수 없다.
  삭제된 이력, 상속·복제 activity의 원래 실행 경계, resume 전후의 정확한 호출 수도 추정하지 않는다.
- 시각·token이 없는 범위, 아직 실행 중이거나 중단·차단된 범위를 정상 완료로 표시하지 않는다.
- 출력은 승인/신원/KG 출처 인증이나 실제 진단 성공을 뜻하지 않는다.

## 검증

```sh
go test -race ./cmd/engine-metrics
go vet ./cmd/engine-metrics
go build ./...
```

실제 PG 회귀는 `ENGINE_METRICS_TEST_DSN`과 폐기용 컨테이너 guard를 명시한 전용 환경에서만 실행한다.
guard는 사고 방지 표식이며 운영 권위 증명이 아니다. fixture의 DB 생성/삽입/삭제는 그 소유
폐기용 cluster에만 한정하고 도구의 읽기 경로와 구분한다. DSN이 없으면 DB 시험은 skip이며
단위시험 통과와 실제 DB 시험 통과를 구분해 보고한다. 합성 비밀값의 stdout/stderr 부재,
다른 범위 혼입, NULL/빈 결과/시간 누락, 취소·기한, 실제 read-only transaction을 검증한다.
기한 시험은 폐기용 DB의 실제 blocker PID·query 앞부분·Lock 대기를 대조하여 intent SELECT가
서버에서 기다리는 사실을 확인한다. query 원문이나 runtime DB 설정을 출력·변경하지 않는다.
실제 진단 자료가 없는 현재 완료 범위는 **집계 도구 검증**이며 baseline 측정 완료가 아니다.

guard 환경 변수는 `ENGINE_METRICS_OWNED_TEST_PG=codex-owned-pg-v1:<검증한 64자리 컨테이너 ID>`다.
DB 시험의 `ENGINE_METRICS_TEST_DSN`은 별도 변수이며 운영 CLI의 DSN 변수와 구분한다.
이 표식을 임의로 채우거나 운영/개발 cluster를 가리켜 DB 생성 시험을 실행하지 않는다.

이번 개발 검증은 정확한 CID/name/task-label을 확인한 소유 PG16 폐기용 컨테이너에서 수행했다.
최종 race 결과는 **8 pass / 0 fail / 0 skip**(단위 2, 실제 DB 6), gofmt·관련 vet·전체 build를
확인한다. 독립 리뷰의 잠금 조회 도달 증명 및 query 길이 제한 지적을 수정·재검토했다.
fixture는 위 dev schema에서 필요한 컬럼의 최소 투영이며 nullable/비정상 legacy 행도 별도로
포함한다. 실제 운영 migration·실제 진단 데이터·최적화 전 baseline 측정을 검증한 것은 아니다.
