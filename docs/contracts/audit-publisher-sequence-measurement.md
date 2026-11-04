# Publisher 전체 게시 순서의 누적 비용 — 합성 수동 측정

## 범위와 코드 기준

실제 `auditpublisher.Open`(Register) → 유효한 intent/result N쌍의 `Record`(Append 2N회) → `Seal`을 실행한다. 이벤트마다 증가하는 journal snapshot의 읽기·검증·SQL 게시·ACK 재조회를 포함한 **구현 전체의 누적 비용**이며 내부 검증시간을 별도로 추정하지 않는다. production 알고리즘·운영 상한은 바꾸지 않는다.

2026-11-04 측정 시작 시 확인: dev `88415fff88f1c66c4307ba370c360e6ad338efcc`, 열린 #145 `ddf596ce1b9d41e40186ab9f898f7216d6b6619a`, #148 `cf0a3220b39f822eadae6465d6b360caef4f8eb5`, #149 `8cc0d5588fb6e8b6313434006224cccd8968bab2`. #149가 두 선행 head를 조상으로 포함함을 `git merge-base --is-ancestor`로 확인했고 이 벤치마크는 #149에서 분기했다. 통합 순서는 #145 → #148 → #149 → 이 변경이다. 게시 전 #145·#148의 병합과 동일 head 유지, #149의 열린 상태·동일 head를 재확인했다. head가 바뀌면 입력/게시/fixture 영향 분석과 필요한 동기화·재검증을 수행한다.

고유 변경은 `auditpublisher/sequence_resource_benchmark_linux_test.go`와 이 문서뿐이다. [#152](https://github.com/ABC2LAB/ABCDEF2LAB/pull/152)의 단일 VerifyExtension 측정과 [#151](https://github.com/ABC2LAB/ABCDEF2LAB/pull/151)의 서버 연결은 필수 코드 선행이 아니다. #152 벤치마크는 단일 검증이며 같은 전체 순서 측정이 아님을 확인했다. 실제 Publisher 코드를 복사하지 않고 [기존 게시자](./audit-checkpoint-publisher.md)·checkpoint Store를 사용한다. 다른 세션의 `cmd/engine-metrics/`·효율 집계·서버·revision·평가 파일은 수정하지 않는다.

## 수동 실행과 경계

Linux 전용 `auditpublisherresource` build tag로 기본 시험·CI에서 제외한다. `-benchtime=1x`가 아니거나 N이 1/8/32/128/512 중 하나가 아니면 실패한다. 최대 512쌍(1024 events), snapshot 8 MiB, 검증당 10초, sequence context 45초다. 이는 합성 fixture의 실행 상한이며 운영 권장값이 아니다. 이번에는 128쌍까지만 측정했다.

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

단위는 ms, 할당은 누적 bytes다. 각 행은 한 번의 완전한 sequence다. 모든 행에서 Register/Seal 각 1회, 실패 0, 최종 sealed head 1개와 journal Verify가 확인됐다.

| N쌍 | 반복 | Open | Record 합계 | Seal | 전체 | 누적 할당 bytes |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1 | 25.693 | 36.525 | 16.541 | 78.760 | 481920 |
| 1 | 2 | 29.215 | 38.949 | 14.699 | 82.863 | 458736 |
| 1 | 3 | 31.063 | 39.432 | 15.389 | 85.885 | 458752 |
| 8 | 1 | 30.488 | 298.538 | 16.458 | 345.484 | 4229280 |
| 8 | 2 | 27.411 | 263.588 | 13.302 | 304.303 | 4200296 |
| 8 | 3 | 29.317 | 288.201 | 19.758 | 337.276 | 4200360 |
| 32 | 1 | 27.632 | 1412.152 | 26.850 | 1466.634 | 42241232 |
| 32 | 2 | 28.644 | 1478.112 | 20.150 | 1526.907 | 42218104 |
| 32 | 3 | 24.705 | 1357.598 | 20.450 | 1402.754 | 42218072 |
| 128 | 1 | 29.044 | 6695.282 | 32.190 | 6756.516 | 579883912 |
| 128 | 2 | 29.375 | 7003.864 | 33.014 | 7066.253 | 579861336 |
| 128 | 3 | 29.671 | 7024.691 | 31.230 | 7085.592 | 579861712 |

| N쌍 | 실제 events/sequence | journal bytes | sealed revision/history/receipts | 완료 반복 | 전체 중앙값 [최소–최대] ms | Record당 평균의 중앙값 ms |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 2 | 1644 | 4 / 4 / 4 | 3/3 | 82.863 [78.760–85.885] | 19.475 |
| 8 | 16 | 10590 | 18 / 18 / 18 | 3/3 | 337.276 [304.303–345.484] | 18.013 |
| 32 | 64 | 41286 | 66 / 66 / 66 | 3/3 | 1466.634 [1402.754–1526.907] | 22.065 |
| 128 | 256 | 164226–164227 | 258 / 258 / 258 | 3/3 | 7066.253 [6756.516–7085.592] | 27.359 |

리뷰 보완 전 1쌍 smoke(전체 99.702ms)와 precheck 추가 전 탐색 matrix 12회는 위 최종 통계에서 제외했다. 독립 리뷰의 schema 소유권·미측정 표시와 명시적 초기 DB/입력 폭 검사를 반영한 최종 코드로 위 12회를 다시 수행했다. 성공 표본의 재선택이 아니라 코드 버전 차이를 구분한 것이며 앞선 13회에도 게시 실패는 없었다. DB 미제공 시험은 각 matrix 뒤 총 2회 실행했고 모두 exit=1, 각각 setup 실패 1건, completed=false, measured/verified=false였고 skip은 없었다. 측정 matrix는 12회 완료·실패 0·timeout/상한 도달 0이며 512쌍·강제 timeout/OOM 조건은 미실행이다.

전체 시간·누적 할당은 N과 함께 증가했다. Record당 평균은 작은 조건의 고정비·변동 때문에 단조 증가하지 않았고 N=8/32/128의 중앙값은 18.013/22.065/27.359ms였다. 이 범위/반복 수로 O(n²)를 실증하거나 운영 상한·실환경 처리량을 확정하지 않는다. #152의 단일 VerifyExtension 결과와 같은 지표로 합치거나 직접 비교하지 않는다. host·epoch·key·Seal 허용은 합성 fixture다. 운영 권위 공급·키 registry·서버 lifecycle·실제 대상 요청·Juice Shop·LLM 비용과 운영 내구성/격리의 검증 결과가 아니다.

## 검토·검증

독립 코드 리뷰에서 CREATE 실패 후 소유하지 않은 schema 정리 가능성과 실패 표본의 미측정 0 표현을 지적받아 수정했고 재리뷰에서 추가 차단 지적이 없었다. tagged compile은 실제 DB 시험과 구별한다. 최종 코드의 tagged compile, 기본 tag에서 benchmark 제외 확인, 관련 tagged vet, 전체 Go build, gofmt/diff 검사를 통과했다. 최종 선행 head·원격 SHA·CI는 PR/작업 결과에서 별도로 확인한다. 여기의 12회는 실제 DB·파일을 사용한 benchmark 실행이며 일반 단위시험 12개라는 뜻이 아니다.
