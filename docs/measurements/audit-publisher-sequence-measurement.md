# Publisher 전체 게시 순서의 누적 비용 — 합성 수동 측정

## 범위와 코드 기준

측정 조건과 관측값의 재현 기록이다. 표의 기준은 [7396f78 fixture](https://github.com/ABC2LAB/ABCDEF2LAB/blob/7396f7816e2013d1a1230d27493b0cee2b9685a2/auditpublisher/sequence_resource_benchmark_linux_test.go)이며, 기존 12회 값을 유효숫자 3자리로 표시한다. 후속 DSN 가드·동일값 상수 정리의 검증은 순수 파싱 회귀로 구분한다.

실제 `auditpublisher.Open`(Register) → 유효한 intent/result N쌍의 `Record`(Append 2N회) → `Seal`을 실행한다. 이벤트마다 증가하는 journal snapshot의 읽기·검증·SQL 게시·ACK 재조회를 포함한 **구현 전체의 누적 비용**이며 내부 검증시간을 별도로 추정하지 않는다. production 알고리즘·운영 상한은 바꾸지 않는다.

## 수동 실행과 경계

Linux 전용 `auditpublisherresource` build tag로 기본 시험·CI에서 제외한다. `-benchtime=1x`가 아니거나 N이 1/8/32/128/512 중 하나가 아니면 실패한다. 최대 512쌍(1024 events), snapshot 8 MiB, 검증당 10초, sequence context 45초다. 이는 합성 fixture의 실행 상한이며 운영 권장값이 아니다. 이번에는 128쌍까지만 측정했다. 512쌍은 미측정이며 느린 환경에서는 45초 context 상한을 넘을 수 있으므로 작은 N부터 완료·시간초과를 다시 확인한다.

DB는 **직접 만든 폐기용 인스턴스**만 사용한다. `ARTEX_AUDIT_SEQUENCE_DISPOSABLE=yes`와 전용 `ARTEX_AUDIT_SEQUENCE_DSN`을 모두 요구하며 다른 앱/test DSN을 참조하지 않는다. DSN은 `postgres://`·127.0.0.1·명시적 port·DB명 `audit_sequence_bench`만 수용한다. 이 형태 검사는 자원 소유권을 인증하지 않으므로 실행자가 자신의 인스턴스인지 확인해야 한다. DSN 미제공/오류는 명시적 benchmark 실행 실패이며 skip·성공 표본이 아니다.

준비 예시: 세션 전용 이름·port를 고르고 DB명/합성 비밀번호를 보호된 환경 파일로 제공한다. DSN 원문은 출력·커밋하지 않는다.

```sh
docker run --rm -d --name <owned-sequence-pg> --memory=256m --cpus=1 \
  --env-file /private/sequence-pg.env -p 127.0.0.1::5432 postgres:16-alpine
go test -tags auditpublisherresource -c ./auditpublisher -o /tmp/sequence-bench.test
# 보호된 환경에서 ARTEX_AUDIT_SEQUENCE_DSN을 자신의 port/전용 DB로 설정한다.
# 한 CPU에 affinity를 고정하고 동일 제한으로 작은 N부터 실행한다.
ARTEX_AUDIT_SEQUENCE_DISPOSABLE=yes ARTEX_AUDIT_SEQUENCE_N=1 \
GOMAXPROCS=1 GOMEMLIMIT=256MiB \
taskset -c <허용-CPU> prlimit --as=3221225472 --cpu=120 --nofile=128 timeout 150 \
  /tmp/sequence-bench.test -test.run '^$' \
  -test.bench '^BenchmarkPublisherSequenceResource$' \
  -test.benchtime=1x -test.count=3 -test.timeout=120s -test.v
```

N=1 성공과 SQL/파일 검증부터 확인한 뒤 8→32→128로 늘린다. 각 N은 새 프로세스이며 `-count=3`의 반복마다 새 schema·journal을 만든다. timeout/오류/불완료/정리 실패가 나오면 이후 확대를 멈추고 실패 표본으로 보존한다. OS 제한으로 종료돼 JSON이 없더라도 프로세스 exit를 실패로 기록하고 누락 표본을 성공으로 보충하지 않는다. 종료 뒤 자신이 만든 container·바이너리·합성 자료만 제거한다.

## 측정 방법과 정확성

- 준비·DB 연결·schema 생성/migration·합성 epoch/key/root·이벤트 배열 생성·GC는 타이머 밖이다. 동일한 Scope/ref 문자열, 고정 UTC 시각, 6자리 attempt 번호로 intent/result 입력 폭을 유지한다. journal의 sequence 숫자 폭은 실제 증가를 따른다.
- Open, 반복 Record 전체, Seal, 전체 sequence의 wall time을 각각 잰다. 전체에는 단계 사이 타이머·루프·성공 ACK 계수 비용도 들어간다. Record 평균은 `records_ms / (2N)`이며 전체 시간과 구별한다. 준비·최종 Verify/SQL 대조·정리·MemStats 조회는 sequence 시간에 포함하지 않는다.
- 결과의 `record_successes`는 nil 오류로 ACK된 Record 수다. 실패한 호출도 파일/SQL을 일부 진행했을 수 있으므로 ACK 수를 실제 진행의 전부로 추정하지 않는다.
- 성공은 현재 Store.Head와 Seal 반환 checkpoint/revision/phase/epoch 일치, 최종 `sealed`, sequence=2N, revision=2N+2, heads=1, history=receipt=distinct receipt=2N+2를 확인해야 한다. 최종 0600 일반 journal을 같은 key/checkpoint로 독립 Verify하며 root·authority lease 회수도 대조한다.
- `sequence_sample` JSON의 `completed_repetition`·`accounting_verified`가 모두 true이고 프로세스/정리가 성공한 표본만 성공 통계에 넣는다. `open_measured`/`records_measured`/`seal_measured`/`allocation_measured`가 false인 해당 수치 0은 **미측정 placeholder**다. accounting_verified=false의 최종 파일/head/DB 숫자도 검증된 0이 아니다. 실패 수·stage·exit는 별도 보존한다.
- `cumulative_alloc_bytes`/`cumulative_mallocs`는 sequence 전후 프로세스 MemStats의 TotalAlloc/Mallocs 차이다. PG 드라이버 등 같은 Go 프로세스의 작업도 포함한다. 누적 할당이며 live heap·RSS·peak가 아니고 PostgreSQL 프로세스 메모리를 포함하지 않는다. RSS/peak는 이번에 측정하지 않았다.
- 임시 디렉터리는 0700, journal은 0600이다. 생성 성공이 확인된 schema만 정리한다. CREATE 결과 불명은 setup 실패이며 소유한 DB/container를 점검·폐기해야 한다. raw journal·key·원문 로그·덤프는 공유 산출물에 넣지 않는다.

## 실제 결과

WSL2 Linux amd64, Intel i5-8265U, Go 1.26.3. benchmark 프로세스 CPU affinity=0, GOMAXPROCS=1. Go `GOMEMLIMIT=256MiB`는 Go runtime 메모리 soft limit이고 OS `RLIMIT_AS=3 GiB`는 가상주소 공간 hard limit이다. CPU 시간 120초·FD 128·test 120초·외부 wall timeout 150초를 별도로 적용했다. PostgreSQL 16.15는 소유한 Docker container에서 memory hard 256 MiB·CPU quota 1, 기본 비-tmpfs data volume을 사용했다. 모든 연결에서 synchronous_commit=on, fsync=on을 확인했고 pool은 최대 2개 연결이다. 호스트의 다른 부하·cache·WAL checkpoint 주기는 완전히 통제하지 않았다. SQL migration과 epoch는 반복마다 새로 만든다. 측정 전 heads/history/operations 0건과 모든 intent/result 입력의 각 고정 폭을 실제 확인한다.

시간 단위는 ms이며 유효숫자 3자리로 표시한다. 할당 bytes와 구조 건수는 관측 정수 그대로다. 각 조건은 반복 3회이며 아래 최소–최대 범위의 변동이 있으므로 µs 수준의 정확도를 주장하지 않는다. 하드웨어·동시 부하·cache 상태에 의존하는 값으로 해석한다. 각 행은 한 번의 완전한 sequence다. 모든 행에서 Register/Seal 각 1회, 실패 0, 최종 sealed head 1개와 journal Verify가 확인됐다.

| N쌍 | 반복 | Open | Record 합계 | Seal | 전체 | 누적 할당 bytes |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1 | 25.7 | 36.5 | 16.5 | 78.8 | 481920 |
| 1 | 2 | 29.2 | 38.9 | 14.7 | 82.9 | 458736 |
| 1 | 3 | 31.1 | 39.4 | 15.4 | 85.9 | 458752 |
| 8 | 1 | 30.5 | 299 | 16.5 | 345 | 4229280 |
| 8 | 2 | 27.4 | 264 | 13.3 | 304 | 4200296 |
| 8 | 3 | 29.3 | 288 | 19.8 | 337 | 4200360 |
| 32 | 1 | 27.6 | 1.41e+3 | 26.8 | 1.47e+3 | 42241232 |
| 32 | 2 | 28.6 | 1.48e+3 | 20.2 | 1.53e+3 | 42218104 |
| 32 | 3 | 24.7 | 1.36e+3 | 20.4 | 1.40e+3 | 42218072 |
| 128 | 1 | 29.0 | 6.70e+3 | 32.2 | 6.76e+3 | 579883912 |
| 128 | 2 | 29.4 | 7.00e+3 | 33.0 | 7.07e+3 | 579861336 |
| 128 | 3 | 29.7 | 7.02e+3 | 31.2 | 7.09e+3 | 579861712 |

| N쌍 | 실제 events/sequence | journal bytes | sealed revision/history/receipts | 완료 반복 | 전체 중앙값 [최소–최대] ms | Record당 평균의 중앙값 ms |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 2 | 1644 | 4 / 4 / 4 | 3/3 | 82.9 [78.8–85.9] | 19.5 |
| 8 | 16 | 10590 | 18 / 18 / 18 | 3/3 | 337 [304–345] | 18.0 |
| 32 | 64 | 41286 | 66 / 66 / 66 | 3/3 | 1.47e+3 [1.40e+3–1.53e+3] | 22.1 |
| 128 | 256 | 164226–164227 | 258 / 258 / 258 | 3/3 | 7.07e+3 [6.76e+3–7.09e+3] | 27.4 |

표는 같은 최종 측정 조건에서 N별 3회씩 수행한 12회 완료 표본을 사용한다. 초기 검토 코드의 탐색 13회는 코드 버전 차이로 통계에서 제외했다. DB 미제공 시험 2회는 모두 exit=1·setup 실패·completed=false였고 SKIP은 없었다. 측정 matrix 12회의 실패·timeout/상한 도달은 0이며 512쌍·강제 timeout/OOM 조건은 미실행이다.

전체 시간·누적 할당은 N과 함께 증가했다. Record당 평균은 작은 조건의 고정비·변동 때문에 단조 증가하지 않았고 N=8/32/128의 중앙값은 18.0/22.1/27.4ms였다. 이 범위/반복 수로 O(n²)를 실증하거나 운영 상한·실환경 처리량을 확정하지 않는다. #152의 단일 VerifyExtension 결과와 같은 지표로 합치거나 직접 비교하지 않는다. host·epoch·key·Seal 허용은 합성 fixture다. 운영 권위 공급·키 registry·서버 lifecycle·실제 대상 요청·Juice Shop·LLM 비용과 운영 내구성/격리의 검증 결과가 아니다.

## 검토·검증

tagged compile과 관련 vet·기본 tag 제외·gofmt/diff를 확인한다. 선행 관계·원격 SHA·CI 결과는 PR 본문에서 확인한다. 표의 12회는 실제 DB·파일 benchmark 실행 횟수이며 아래 순수 파싱 12case와 별도로 집계한다.

## DSN 가드 보완 (리뷰 반영)

연결 전에 URL의 명시 포트와 `pgx.ParseConfig`의 실제 Host·Port·Database를 함께 대조한다. Host는 `127.0.0.1`, Database는 `audit_sequence_bench`, Port는 URL의 1~65535 포트와 같아야 하며 Fallbacks는 0개여야 한다. query의 `dbname`·`host`·`port` 덮어쓰기로 다른 연결 대상이 되거나 SSL/다중 호스트 fallback이 생기면 고정 오류로 거부한다. 예시에는 `sslmode=disable`을 사용한다. 이 검사는 대상 형식의 제한이며 DB 소유를 인증하지 않는다. 실제 폐기용 인스턴스의 소유 확인은 실행자의 필수 전제다.

순수 파싱 회귀는 연결·schema 생성 없이 정확 대상 2건과 query 덮어쓰기·fallback 등 거부 10건을 검사한다. 실제 PG와 전체 sequence 벤치마크는 이 보완 검증에서 재실행하지 않는다.
