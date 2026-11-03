# CPD04 합성 감사 확장 검증 자원 측정

2026-11-03 · 이동찬 · 팀 검토용 단일 표본. 운영 상한 확정이나 서비스 활성화 근거가 아니다.

기준은 `dev` `88415fff88f1c66c4307ba370c360e6ad338efcc`와 열린 선행 #145 `b3652a180668c2752e1f8fb6130e0c3d298b9496`이다. 신규 수동 전용 [`extension_resource_benchmark_test.go`](../../transmit/auditlog/extension_resource_benchmark_test.go)는 `auditresource && linux` 빌드 태그가 없으면 기본 테스트/CI에 들어가지 않는다. 기존 production verifier, 저널 writer, 상한 상수는 바꾸지 않았다.

## 합성 입력과 측정 경계

- 내부 canonical `header`/`eventPayload`/`sign`/`encodeLine`으로 HMAC 체인을 만들어 임시 파일에 썼다. 각 유효 입력은 측정 전에 별도의 `Verify`로 전체 journal과 최종 checkpoint를 검증했다. 실제 `Journal.Record`의 event별 `Sync`와 CPD/Publisher ACK는 실행하지 않았다.
- 최대 사례는 정확히 **40,000 event**: 현재 서버 요청 천장 10,000회에 해당하는 intent/result 10,000쌍(20,000 event)과 요청 예산을 소비하지 않는 `budget_exhausted` result 20,000개를 가정했다. 5,000/20,000 사례도 같은 비율이다. ASCII 참조 9개의 길이를 128자 이하에서 조절하여 구조상 유효한 40,000-event snapshot을 64 MiB 이하에서 최대한 크게 만들었다. 최대 유효 입력은 **67,027,597바이트**, 64 MiB 상한보다 81,267바이트 작았다.
- `events40k-header`는 이전 checkpoint가 header(순번 0)이고 `events40k-near`는 이전 checkpoint가 마지막 event 직전(순번 39,999)이다. 둘 다 candidate 전체를 검증하지만 뒤 사례는 거의 전체 prefix를 한 번 더 검증한다. `ingest64mib-plus-one`은 67,108,865바이트의 **무효 zero input**을 `ErrExtensionLimit`으로 거절하는 읽기/할당 사례다. 유효 journal 검증 비용으로 합산하지 않는다.

각 사례를 새 프로세스에서 `-benchtime=1x`로 한 번 실행했다. Go 1.26.3 linux/amd64의 정적 test binary를 캐시된 `postgres:16-alpine` 이미지(`sha256:721873c34ceb…`)의 별도 Docker 컨테이너에 읽기 전용 마운트했다. 컨테이너는 네트워크 없음, 읽기 전용 rootfs, capability 전부 제거, no-new-privileges, PID 64, CPU 1, 메모리와 swap 합계 768 MiB, 고정 `/tmp` tmpfs 96 MiB, `GOMAXPROCS=1`, `GOMEMLIMIT=384MiB`였다. 전체 실행 상한은 120초였다. DB·대상 서버·LLM·실제 키는 사용하지 않았다. 모든 사례는 exit 0, timeout/OOM 0이었고 소유 컨테이너는 종료 후 부재를 확인했다.

| 사례 | event | 입력 바이트 | ns/op | B/op | allocs/op | HeapAlloc 후 MiB | HeapSys 후 MiB | 자기 프로세스 최대 RSS MiB |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 유효 5k, 끝 prefix | 5,000 | 8,552,614 | 479,106,686 | 162,355,088 | 627,713 | 16.73 | 31.69 | 34.74 |
| 유효 20k, 끝 prefix | 20,000 | 34,227,615 | 2,230,813,421 | 641,021,080 | 2,510,222 | 70.11 | 91.66 | 86.20 |
| 유효 40k, 끝 prefix | 40,000 | 67,027,597 | 5,471,427,712 | 1,291,404,704 | 5,020,231 | 130.60 | 179.70 | 170.10 |
| 유효 40k, header prefix | 40,000 | 67,027,597 | 2,284,175,555 | 728,374,824 | 2,510,240 | 128.70 | 171.70 | 170.10 |
| 64 MiB + 1 거절 | 0 | 67,108,865 | 167,016,963 | 165,356,280 | 55 | 157.90 | 179.70 | 166.40 |

`B/op`와 `allocs/op`는 검증 호출의 **누적 할당량/횟수**로 최대 상주 메모리가 아니다. `HeapAlloc 후`는 호출 직후 Go heap 현재량이고 peak가 아니며, `HeapSys 후`는 Go가 확보한 heap 영역이다. `self_maxrss`는 Linux `getrusage(RUSAGE_SELF).ru_maxrss`의 프로세스 최대 RSS로 **합성 파일 생성·사전 Verify까지 포함**한다. Docker client의 RSS가 아니다. 생성 시간은 `ns/op`/`B/op`에서 제외했다.

이 단일 표본에서는 끝 prefix의 40k 검증이 header prefix보다 약 2.4배 오래 걸리고 누적 할당은 약 1.8배였다. 이는 `VerifyExtension`이 candidate 전체와 기존 prefix를 각각 검증하는 구조와 일치하지만, 환경별 분산·동시 실행·스토리지/키 서비스·DB commit·실제 Publisher의 이벤트별 ACK 비용은 측정하지 않았다. 특히 40,000번의 중간 ACK로 계속 자라는 snapshot을 반복 게시하면 총 읽기/검증량이 O(n²)이므로 이 벤치의 단일 최종 검증 수치로 운영 처리량을 추정하면 안 된다. CPD04 상한 확정은 대표 운영 환경의 재측정과 최민준 무결성 리뷰를 거쳐 별도로 결정한다.
