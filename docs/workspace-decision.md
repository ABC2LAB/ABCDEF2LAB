# 진단 작업 공간과 큰 출력 — 결정안 (C29)

상태: **결정안(2026-11-12, 최민준 결정·이동찬 리뷰).** 리뷰로 확정되기 전에는 구현하지 않는다. E② 샌드박스 실행 경로를 바꾸므로 **이동찬 페어 리뷰가 필수**다. 일정은 적지 않는다. 측정 전 수치는 적지 않는다.

[개선 목록](control-restore-backlog.md) C29("호출마다 작업 디렉터리를 새로 만들고 지움, `/work` 32MiB·`/tmp` 16MiB 실행 불가, 출력 32KiB 넘으면 버림(원본은 파일로 넘겨 다시 읽음)")의 결정안이다. [루프 복원 결정안](loop-restore-decision.md)의 파일 도구(7단계)와 지난 응답 다시 보기(열린 항목 1 결과)가 이 결정에 걸려 있다.

## 1. 지금

- 진단 Bash 호출마다 러너(`e2runner/runner_linux.go` `initialize`)가 호스트에 빈 호출 디렉터리를 만들고, 샌드박스(`e2sandbox/helper_linux.go`)는 그 안의 `/work`에 새 tmpfs(`ScratchBytes`, 기본 32MiB, `noexec`)를, `/tmp`에 새 tmpfs(`TempBytes`, 기본 16MiB, `noexec`)를 올린다. 호출이 끝나면 둘 다 사라지고 호스트 호출 디렉터리도 지운다.
- 그래서 같은 intent 안에서도 앞 호출이 받은 응답·만든 스크립트·중간 결과를 다음 호출이 볼 수 없다. 원본은 Worker 데이터 디렉터리에 파일로 남기고 다음 명령이나 Read로 다시 읽었다.
- 도구 출력은 가린 뒤(`RedactOutput`) `OutputLimit`(제품 기본 32KiB)에서 자르고 나머지는 버린다(`OutputTruncated`만 남는다).
- 지금 구조의 보안 성질: **임의 명령은 호스트에 쓸 수 있는 파일 시스템이나 fd를 보지 못한다.** 호스트 작업 디렉터리는 수명 표식일 뿐이고 helper는 명령 실행 전에 그 fd를 닫는다.

## 2. 결정

1. **보안 성질은 그대로 둔다.** 명령이 실행되는 동안 호스트 쓰기 경로·fd를 열어 주지 않는다(호스트 디렉터리 bind mount를 쓰지 않는다). `/work`·`/tmp`는 지금처럼 크기 상한 있는 tmpfs이고 `noexec`다.
2. **intent 작업 공간을 호출 사이에 이어 준다(복사해 넣고 빼기).**
   - 러너가 intent마다 호스트 보관소(실행 디렉터리 아래, 디렉터리 0700·파일 0600)를 둔다.
   - 호출 시작: 명령을 실행하기 전에 보관소 내용을 새 `/work` tmpfs에 넣는다.
   - 호출 끝: 샌드박스 안의 **모든 프로세스가 끝난 뒤**(명령의 프로세스 그룹만이 아니라 분리된 세션·이중 fork로 남은 것까지 없어진 뒤) `/work` 내용을 보관소로 뺀다. 지금의 `Drained`는 helper 종료·namespace 정리·출력 drain 뒤의 보장이라, 그때는 tmpfs도 사라진다. 그래서 어느 helper/namespace를 언제까지 살려 두고 복사하는지는 구현 PR에서 나눠 정하고 시험으로 확인한다(열린 항목 1). 빼는 통로는 명령이 물려받거나 `/proc`로 열 수 없어야 한다. 구현(#443)은 helper를 띄울 때 만든 pipe(fd 9)를 helper만 들고(close-on-exec), helper는 dumpable이 아니며, 명령 쪽 프로세스는 capability 집합이 모두 비어 있어(bounding 포함, no_new_privs·seccomp) `/proc/1/fd/9`를 열 수 없다. 설정·상태 pipe도 같은 근거로 보호된다. 이 조건은 시험으로 고정한다(명령의 `Cap*` 모두 0, `/proc/1/fd/9` 쓰기·상속 실패).
   - 넣고 빼는 것은 **일반 파일과 디렉터리만**이다. 심볼릭 링크·장치·FIFO·소켓·하드 링크는 버린다. 권한은 0600/0700으로 다시 쓰고 소유자·시간은 옮기지 않는다. 경로는 `/work` 기준 상대 경로만 받고 `..`·절대 경로·NUL은 버린다.
   - 크기는 tmpfs 상한(`ScratchBytes`)이 그대로 상한이다. 보관소도 그 크기를 넘으면 빼지 않고 그 호출 결과에 사실을 남긴다(작업 공간 보존 실패, 실행 실패가 아니다).
   - `/tmp`는 이어 주지 않는다(호출마다 비운다).
   - 수명: intent가 끝나면(완료·제한·불완전·불명·중지 모두) 보관소를 지운다. 실행이 끝날 때 남은 보관소도 지운다.
3. **큰 출력은 보관소 파일로 넘긴다.** `OutputLimit`을 넘는 출력은 지금처럼 모델에는 앞부분만 주고, 가린 전체 출력(`RedactOutput` 뒤)을 보관소의 `/work/.outputs/<call_ref>.txt`로 남긴다. 모델에 돌려주는 결과에 그 경로를 적는다. 이 파일은 2의 크기 상한 안에 든다(넘으면 저장하지 않고 그 사실을 적는다).
4. **파일 도구(Read·Write·Edit·LS)는 보관소만 본다.** 호스트 쪽 도구가 보관소 디렉터리 안에서만 열고(심볼릭 링크를 따라가지 않고 디렉터리 밖으로 나가지 못하게 여는 방식), `..`·절대 경로·링크를 거부한다. 다음 호출이 그 파일을 `/work`에서 다시 본다. 파일 도구는 대상에 요청을 보내지 않는다. **모델에 돌려주는 내용은 그 실행의 출력 가림(`RedactOutput`)을 거친다**: Read가 돌려주는 파일 내용과 LS가 돌려주는 이름 모두다. Bash 결과와 같은 가림이라 `cat`으로 읽든 Read로 읽든 같은 값만 보인다.
5. **보관소에 들어가는 것.** 대상 응답에서 온 데이터와 모델이 쓴 파일이다. 우리 제어 비밀(규칙 2)은 샌드박스에 들어가지 않으므로 보관소에도 없다. 그러나 **E②가 요청에 넣은 세션 자격값(`Cookie`·`Authorization`)을 대상이 응답에 되돌려 주면**(요청 헤더를 보여 주는 디버그·오류 페이지 등) 명령이 저장한 원문 파일에 그 값이 남을 수 있다. 그래서 4의 가림을 파일 도구에 건다. 큰 출력 파일은 가린 뒤의 출력이다. 보관소는 감사 근거가 아니다(근거는 지금처럼 E② trusted 관찰과 감사 저널이다). 지금의 수용시험 S09(심은 비밀번호·토큰을 모델 입력에 넣지 않음)는 계정 분류와 수용시험 개정이 함께 들어오기 전까지 그대로 적용한다(절대 규칙 2).
6. **크기 값은 설치 설정이다.** `ScratchBytes`·`TempBytes`·`OutputLimit`은 [C32 결정안](install-limits-decision.md)의 `runtime_limits` 칸으로 올리고, 상한은 지금 코드 상한(`e2sandbox/limits_linux.go`·러너 `maxOutputBytes`)을 그대로 쓴다. 처음의 "측정 전에는 기본값을 바꾸지 않는다"는 [C32 결정안](install-limits-decision.md) §7에서 철회했다(기본값은 근거와 함께 바꾼다).
7. **`noexec`는 유지한다.** 원본은 받은 바이너리를 바로 실행할 수 있었지만, 진단 샌드박스에서 받은 실행 파일을 돌리는 것은 격리 위험에 비해 얻는 것이 작다. 스크립트는 인터프리터로 실행하면 된다(`bash x.sh`, `python3 x.py`). 이 차이는 원본 대비 남는 차이로 적는다.

## 3. 원본과 다르게 남는 것

- 작업 공간은 intent 단위다(원본은 task 데이터 디렉터리). 다른 intent의 파일은 보이지 않는다.
- `/work`·`/tmp`에서 실행 파일을 직접 실행할 수 없다.
- 작업 공간 크기에 상한이 있다.

## 4. 구현 순서

1. 러너 보관소와 복사해 넣고 빼기(크기·파일 종류·경로 검사), 명령이 통로를 물려받지 못하는 것을 시험으로 고정한다.
2. 큰 출력 파일(`.outputs`)과 모델 결과의 경로 안내.
3. 호스트 파일 도구 4개(보관소 안만)와 진단 Worker 프로필 등록(루프 복원 7단계).
4. 크기 값의 설치 설정 칸(C32 구현과 같이).

## 5. 완료 기준 시험

- 같은 intent의 두 호출: 첫 호출이 `/work`에 쓴 파일을 둘째 호출이 읽는다. 다른 intent에서는 보이지 않는다.
- 명령 실행 중에는 호스트 보관소 경로·fd에 접근할 수 없다(`/proc/*/fd` 포함).
- 명령이 남긴 분리 프로세스(`setsid`·이중 fork)가 있어도 빼기 전에 모두 끝나고, 그 프로세스가 빼는 통로나 보관소를 건드릴 수 없다(명령 프로세스 그룹 종료만으로 모두 끝났다고 가정하지 않는다).
- 대상이 세션 자격값을 되돌려 준 응답을 명령이 파일로 저장한 뒤, Read(내용)·LS(이름) 결과에 보호 값이 없다.
- 심볼릭 링크·FIFO·장치·하드 링크·`..` 경로를 만든 호출 뒤 보관소에 그것들이 없다.
- 크기 상한을 넘긴 호출 뒤 보관소는 이전 상태 그대로이고, 결과에 보존 실패가 남고, 실행은 계속된다.
- 큰 출력: 모델에는 앞부분, `.outputs/<call_ref>.txt`에는 가린 전체(보호 값 없음).
- 파일 도구가 보관소 밖 경로(`..`, 절대 경로, 링크)를 거부한다.
- intent가 끝나면 보관소가 없다.

## 6. 열린 항목

1. **빼는 통로와 수명의 구현**(이동찬과 페어): 처음에는 helper가 샌드박스 안 모든 프로세스 종료 뒤 새로 만든 파이프로 보내는 방식을 1안으로 뒀다. #443은 대신 helper를 띄울 때 만든 pipe(fd 9)를 쓴다. 종료 뒤에 새 fd를 helper에 건네려면 fd를 넘길 수 있는 소켓 통로가 새로 필요하고, helper가 명령 동안 들고 있는 설정·상태 pipe가 이미 같은 근거(close-on-exec, helper dumpable 아님, 명령의 capability 없음)로 보호되기 때문이다. 묶기는 PID namespace init인 helper가 `kill(-1)`·`wait4`로 모든 프로세스를 회수한 뒤 한다. 이 방식은 이동찬 페어 리뷰로 확정한다.

## 7. 회차별 Bash 동시 실행 (1-D, 2026-11-16)

### 실행 슬롯과 신뢰 경로

회차의 Bash 슬롯은 `min(승인된 E② Limits.MaxConcurrent, 실제 Worker 수)`다. HTTP 동시 송신 제한과 Bash 프로세스 제한은 별개이며, Bash 슬롯을 늘려도 E②의 `MaxConcurrent`나 요청·호출·시간·모델 비용 예산을 바꾸지 않는다.

`Engine.Run`은 `Manager.Workers()`가 읽은 DB `workers` 설정(누락 시 기존 기본값 3)을 한 번 읽어 Worker 실행과 E② task admission에 같은 값을 쓴다. 제품의 조기 admission도 Manager 경로로 같은 값을 결속한다. 회차 도중 설정이 달라지면 재입장을 거절하며 기존 실행의 수를 바꾸지 않는다. `E2HTTPRequest.WorkerCount`는 JSON에서 읽거나 내보내지 않는 Go 내부 값이다. 실제 intent를 획득할 때 결속한 수를 factory로 전달하고, factory가 검증한 정책 상한과의 최솟값을 `e2runner.Config.BashSlots`에 넣는다. 모델 출력·HTTP·환경 변수는 이 값을 정하지 않는다.

| 승인 HTTP 동시성 | 실제 Worker | Bash 슬롯 |
|---|---|---|
| 16 | 3 | 3 |
| 2 | 3 | 2 |
| 1 | 3 | 1 |
| 4 | 2 | 2 |

기존 Go 호출자가 Worker 수나 Bash 슬롯을 생략하면 1개다. 음수·정책 상한 밖 슬롯·결속 뒤 다른 Worker 수는 오류로 닫는다. 슬롯 계산은 승인 정책 바이트와 fingerprint를 수정하지 않는다. 새 URL 준비 로더의 `MaxConcurrent == 1` 검사는 제품 기본 정책(16)도 지나는 공통 경로였으므로, 양수 동시성과 이를 수용하는 프록시 연결 수 검사로 바꿨다. 정책의 전체 유한 상한과 지문 검증은 기존 factory·제품 정책 검증을 계속 지난다. 평가·구형 호출은 Worker 결속이 없으면 1개를 유지한다.

### intent 직렬화와 마감

- 서로 다른 intent는 슬롯 수 안에서 별도 call_ref·namespace·프록시·결과·감사를 가진다. 예산은 회차의 기존 누적 예산을 함께 사용한다.
- 같은 intent는 **intent 잠금 → 회차 슬롯** 순서로 확보한다. 같은 intent의 대기자가 다른 Worker의 슬롯을 차지하지 않으며, 앞 호출의 최종 cleanup·보관소 저장이 끝난 뒤에만 다음 호출이 `/work`를 읽는다. 파일 도구는 마지막 저장까지 `workspaceBusy`에 의해 쓰기·수정을 거부한다.
- 호출은 프로세스·분리 자식·프록시·감사 마감과 보관소 저장 처리가 끝난 뒤 active 목록과 슬롯에서 빠진다. 마감이 불명하면 슬롯과 intent 소유권을 보유한다. 보관소 저장 실패는 기존 계약대로 `not_saved`로 표시하고 검증된 이전 보관본을 유지하며, 실행 마감 성공으로 파일 저장 성공을 추정하지 않는다.
- intent 잠금·전체 Bash 슬롯 대기는 명령 시간 상한에 포함한다. 대기 뒤 새 시간 상한을 주지 않는다. 프로세스·Call을 만들기 전에 만료·취소·Drain으로 빠진 명령은 기존 `not_sent` 영수증의 제어 이벤트로 ACK를 남기고, `admission_not_sent` 신뢰 필드로 돌려준다. call_ref·attempt_id·송신 예약·전송/결과 불명 계수는 만들지 않는다. 감사 ACK를 못 받으면 미전송 확정으로 취급하지 않고 기존 감사 실패로 닫는다. Worker는 이를 집행 장애/결과 불명으로 멈추지 않으며 같은 명령을 남은 도구 호출 예산 안에서 재시도할 수 있다. 재시도 없이 끝나면 그 intent는 완료되지 않은 것으로 남는다.
- 대기 중 취소·실행 철회·만료·Close를 확인한다. 전체 Drain과 DrainIntent는 기존 대기 세대를 무효화한다. 마감 후 다시 열린 슬롯으로 옛 대기 호출이 시작되지 않는다. 불완전 drain은 barrier를 유지하며, 이후 마감 재시도로 확인된 경우에만 새 세대를 받는다.
- intent 보관 파일은 회차의 보호 디렉터리 아래 intent ID마다 분리한다. intent 마감 뒤 해당 보관본을 지우며 다른 intent의 실행·보관본은 유지한다. 전체 Close는 모든 호출의 마감 뒤 남은 보관본을 지운다.

### 자원 용량과 남는 한계

호출당 기본 `/work`는 32MiB, `/tmp`는 16MiB다. 다음 값은 **tmpfs 설정 상한의 합계이며 실제 RSS 측정값이 아니다**.

| Bash 슬롯 | `/work` 합산 상한 | `/tmp` 합산 상한 |
|---|---|---|
| 1 | 32MiB | 16MiB |
| 2 | 64MiB | 32MiB |
| 3 | 96MiB | 48MiB |

설치가 다른 자원 프로필을 지원하면 합산식은 각각 `BashSlots × ScratchBytes`, `BashSlots × TempBytes`다. **현재 제품 설치 입력·정책에는 ScratchBytes/TempBytes 선택 칸이 없으므로 이 경로는 기존 기본값을 사용한다.** §2-6의 크기 설정화는 별도 미완료 항목이며, 새 설정 칸을 임의로 허용하거나 입력을 조용히 무시하지 않는다. `OutputLimit` 등 이미 지원하는 보호 정책 값은 그대로 전달된다.

동시에 호출별 프록시/TLS 버퍼, 가린 출력(보존 시 캡처 상한 4MiB), workspace tar 복사본, helper·도구·자식 프로세스 메모리가 더 생긴다. 제품 preparation의 `pids_limit: 256`은 엔진과 모든 호출이 함께 쓴다. 호출별 task 상한은 아래 식으로 나눠 다른 호출이 엔진 몫까지 쓰지 못하게 한다. 보관소 교체는 기존 mutex로 보호하며 짧은 저장 구간은 차례로 처리한다. 처리량·최고 RSS·장시간 부하는 측정하지 않았다.

### 호출별 PID 예산 (#598 P1 수정)

`CallTasks = min(128, floor((256 − 64) / BashSlots))`를 신뢰 호스트가 계산해 helper의 `RLIMIT_NPROC` soft/hard 양쪽에 넘긴다. 이 수는 helper·Bash·자식의 **스레드까지 포함한 task** 상한이다. 슬롯 1/2/3이면 호출별 128/96/64다. 병렬 호출은 이 안에서 helper에 16 task 여유를 남겨, 명령 자식에만 별도 hard/soft NPROC 80(2슬롯)/48(3슬롯)을 건다. 명령 8개와 helper 여유16도 배정할 수 없는 9슬롯 이상은 시작 전에 거부한다. 1슬롯은 기존 NPROC128을 유지한다. 같은 intent 직렬화와 마감 불명 슬롯 보유는 그대로다.

엔진 몫 64는 실제 제품 Compose 이미지 `ab50cb1f`의 preparation에서 Worker3+Planner, 3개 호출의 준비·TLS 송신·마감 중 PID 1의 `/proc/1/status`를 20ms 간격으로 읽어 측정한 최고 **18개 스레드**에 근거한다. 실측의 2배(36)를 다음 2의 거듭제곱(64)으로 올려 여유를 둔다. 관찰용 Python은 별도 1 task이며 엔진 스레드에는 포함하지 않았다. 제한 없는 전체 부하에 대한 최대 엔진 task 수를 증명한 값은 아니다. 다른 배치의 PID 한도가 256보다 작으면 이 조건을 그대로 적용할 수 없다. Runner 생성 때 보이는 cgroup v2 루트의 `/sys/fs/cgroup/pids.max`를 읽어 256 미만이면 시작 전에 거부한다. 파일이 있는데 읽기 실패·형식 오류면 거부하며, `max`/256 이상은 허용한다. 이 파일이 없는 기존 비컨테이너/v1 호스트 경로는 유지한다. 숨겨진 조상 cgroup의 한도를 찾아내는 검사는 아니다.

최초 최대 부하에서 명령이 공유 NPROC64까지 채우자 helper가 5개 스레드 상태에서 다음 스레드를 만들지 못하고 종료되는 것을 관찰했다. 엔진 cgroup 고갈은 없었으나 호출의 relay가 실패했으므로 수용 성공으로 세지 않았다. 병렬 호출에서는 실측 helper 스레드5의 2배(10)를 다음 2의 거듭제곱(16)으로 올려 helper 여유를 둔다. helper는 호출 상한64를 유지하고, 신뢰한 `/usr/bin/prlimit`(기존 util-linux)이 자식의 hard/soft NPROC만 48로 낮춘 뒤 `/bin/bash`를 exec한다. 자식은 hard 한도를 다시64로 올릴 수 없고 helper의 여유는 명령의 fork 폭증으로 소비되지 않는다. 실제 제품 이미지에는 util-linux가 이미 포함돼 있다.

새 권한·cgroup 쓰기 마운트 없이 기존 user namespace별 `RLIMIT_NPROC`를 사용한다. 제품 cgroup은 읽기 전용으로, 호출별 자식 cgroup 위임을 이 변경에서 추가하지 않는다. `pids_limit: 256`·슬롯3·cap_drop ALL·no-new-privileges·seccomp는 유지한다.

factory는 첫 Bash 실행 전 보호 상태 디렉터리에 `runtime-conditions.json`을 배타 생성·fsync한다. 실행 scope·정책 판·결속 Worker 수·Bash 슬롯·컨테이너 task256·엔진 예비64·호출별 상한·명령별 상한·helper 여유·대기의 명령 시간 포함 여부를 남긴다. 기록 실패·기존 파일 재사용은 시작 실패다. 각 Runner 신뢰 결과에도 `process_limits`를 남긴다. 승인 정책 바이트와 기존 평가 manifest 형식은 바꾸지 않는다.

실제 제품 수용은 [PID 전용 Compose 오버레이](../deploy/product/compose.pids-acceptance.yaml), [제어 클라이언트](../scripts/product-pids-acceptance.py), [읽기 전용 관찰기](../scripts/product-pids-observe.py)를 쓴다. 새 1회용 local-only 설치에 소유 fixture DNS/TLS CA를 공급하고 기본 제품 Compose와 기존 acceptance 오버레이 뒤에 PID 오버레이를 붙인다. `pids-model`은 유료/외부 모델 대신 세 intent에 고정 Python fork 명령을 돌려주는 로컬 합성 모델이다. `pids-target`은 두 무거운 호출이 도착한 뒤 세 번째의 가벼운 요청을 돌려주며, 이어 세 호출 모두 자기 상한에서 `fork`의 EAGAIN을 확인한 채 GET 응답을 보류한다. 제어기는 그동안 실제 제품 health를 읽고 두 단계를 해제한 뒤 6개 호출·응답·감사·drain을 대조한다.

```bash
# 별도 터미널에서 먼저 시작(외부 파일에 저장, preparation에는 파일을 쓰지 않음)
docker compose -f deploy/product/compose.yaml -f deploy/product/compose.acceptance.yaml -f deploy/product/compose.pids-acceptance.yaml exec -T preparation python3 - < scripts/product-pids-observe.py > pids-observed.jsonl
# 위와 같은 새 프로젝트/설치와 fixture 이미지로 1회만 실행
docker compose -f deploy/product/compose.yaml -f deploy/product/compose.acceptance.yaml -f deploy/product/compose.pids-acceptance.yaml run --rm --no-deps pids-controller
```

컨테이너 `pids.current`/`pids.peak`와 `pids.events:max`를 읽으며 관찰기 1 task를 명시한다. 커널 계수 의미는 [Linux cgroup v2 PID 문서](https://docs.kernel.org/admin-guide/cgroup-v2.html#pid)에 따른다. 빈 프로젝트·새 fixture 상태에서만 수용 수치를 남기고, 기존 보호 저널을 지워 재시도하지 않는다.

`noexec`, namespace·seccomp·capability 제한, helper FD/호스트 쓰기 경로 보호는 유지한다. 임의 모델 슬롯 설정, 같은 intent의 병렬 Bash, 임의 호스트 파일 접근과 비지원 송신 경로는 지원하지 않는다.

### 검증 기록

검증 환경: Linux 비특권 UID, 실제 소스 빌드 `e2-bash-sandbox`, 소유 loopback HTTP 수신기, 실제 E② 보호 저널/누적 예산, 격리 PostgreSQL 16 Unix 소켓. 수신 보류 채널과 call cleanup/workspace 잠금으로 겹침과 저장 순서를 확인했다. 단순 sleep 경과를 병렬성 근거로 쓰지 않는다.

- `TestRunnerParallelIntentSlotsAndAudit`: 슬롯 1·2·3의 실제 수신 중첩, 초과 실행 거부, call/intent별 송신·응답·마감 감사, 누적 예산 보존.
- `TestRunnerParallelSameIntentSerializesWorkspace`: 같은 intent 직렬성·파일 이어쓰기, 다른 intent의 진행·파일 격리, 실행 중 파일 도구 변경 거부.
- `TestRunnerParallelQueuedLifecycle`: 대기 중 사용자 취소·철회·전체 Drain·Close, 새 세대만 재시작 허용. 기존 intent drain 대기 세대 회귀시험도 실행했다.
- `TestRunnerParallelDrainOneIntent`, `TestRunnerParallelSlotHeldThroughWorkspaceCommit`: 다른 intent 실행 유지, 최종 보관소 저장 전 active/슬롯 보유.
- `TestRunnerParallelCleanupFailuresRetainSlots`, `TestRunnerParallelWorkspaceStoreFailureAndInitFailure`: 실제 helper 실행의 프록시·감사 마감 장애 및 보관소 읽기·원자 저장 실패 주입, 미확인 마감 슬롯 보유와 오류 분류.
- `TestRunnerParallelDetachedChildrenAndTimeout`: setsid 자식과 다른 intent가 실제 수신한 뒤 정상 종료/timeout에서 namespace·프록시 drain 및 슬롯 반환.
- `TestE2HTTPFactoryDerivesBashSlotsFromWorkersAndPolicy`: 실제 제품 factory→Runner→helper에서 표의 네 구성과 Worker 미지정 1개를 대조. 승인 정책 바이트·HTTP 상한 보존.
- `TestE2HTTPBashWorkersFollowActualEngineManagerOwnedPostgres`: 실제 Engine/Manager/intent 획득의 Worker 결속과 실행 중 설정 변경 격리.

초기 구현 검증: E② 세 패키지 전체 race **617 PASS / FAIL 0 / SKIP 0**(test/subtest), 서버·모델 예산/1-E 및 실제 Manager PostgreSQL race **107 PASS / FAIL 0 / SKIP 0**. 전체 빌드·vet 통과. 전용 DB 없이 실행한 서버·제품 전체 시험은 **2193 PASS / FAIL 1 / SKIP 309**였으며 실패는 기존 모델 목록 시험의 DB 미설정이었다. 해당 시험은 일회용 DB 재실행에서 통과했다. 최종 통합 SHA·검증은 PR에 기록한다.

아래 초기 1-D 검증 당시 Docker 제품 이미지의 새 종단 회차는 NOT_RUN이었다. P1 후속에서는 아래처럼 실제 제품 Compose의 새 설치·로컬 합성 모델 회차를 추가했다. 유료 Qwen3.6·외부 모델 회차, 운영 대상 송신, 성능/RSS 측정은 여전히 **NOT_RUN**이다.

최신 dev 통합 검증(기능 코드 `39cfe792`, 추가 예산 시험 `b7cfe815`, dev `83c3cb66`):

| 검사 | PASS | FAIL | SKIP |
|---|---:|---:|---:|
| E② Runner·프록시·샌드박스 전체, 일반/race 각각 | 691 | 0 | 0 |
| 추가 실제 Bash 3개 × 11회 구성의 공유 요청 예산 소진, race | 1 | 0 | 0 |
| agent 전체 일반 시험 | 336 | 0 | 17 |
| server 전체, 격리 PostgreSQL 16·race | 2266 | 0 | 54 |
| 제품 명령 전체, 격리 PostgreSQL 16·race | 404 | 0 | 1 |
| diagnosisprep 전체, 격리 PostgreSQL 16·race | 220 | 0 | 1 |
| Docker 실제 Runner/helper의 1-D 수용 | 21 | 0 | 0 |
| 별도 Go overlay의 가드 변이 검출 | 5/5 | 미검출 0 | 0 |

건수는 개별 test/subtest 사건 수다. 정상 전체 시험과 독립 반례·변이는 별도 실행이며 합산 성공률로 쓰지 않는다. 전용 DSN/명시적 opt-in이 없는 기존 수용시험은 건너뜀으로 남겼다. 첫 PostgreSQL 실행에서 URL을 요구하는 기존 시험 4건은 keyword DSN을 거부했고, CI와 같은 URL DSN으로 고쳐 위 전체 결과를 얻었다. 최종 전체 build·vet·darwin/arm64·windows/amd64 크로스빌드와 gofmt·diff 검사를 통과했다.

Docker 수용 이미지는 `sha256:20da572366b86dc99605d1afaee7d2a0fefc02741a28d75e6b803955dbff3ffe`다. 로컬 브라우저 기반 이미지에 curl을 넣은 **시험용 런타임**이며 완성 제품 이미지 종단 수용과 구분한다. 실행한 helper는 같은 소스 빌드 SHA256 `52408311128b8dbdbbe0ff058bbb4fedb52031003fc4772aee7b4685ff7edfbe`다. UID1000, read-only rootfs, cap_drop ALL, no-new-privileges, 기존 `seccomp-preparation.json`, PID256·메모리2GiB·`/tmp`64MiB, 외부망 없음으로 실행했다. 소스·Go 캐시·제어 비밀을 컨테이너에 넣지 않고 시험 바이너리/helper만 읽기 전용으로 넣었다.

최초 Docker 시험은 helper를 `/tmp`에 빌드해 **20 FAIL / 1 PASS**였다. 단일 Bash 대조에서 `process_start`의 EACCES를 확인했다. 제품의 `/tmp` noexec를 유지하고, **시험 fixture의 helper 선택만** 같은 소스의 읽기 전용 실행 위치로 바꾼 Go overlay로 재실행해 21건이 통과했다. 생산 코드·helper·격리 정책을 완화한 결과가 아니다.

가드 변이는 전체 슬롯을 1로 되돌림, 동일 intent 동시 허용, 최종 저장 전 슬롯 반환, drain 대기 세대 검사 제거, factory의 Worker 최솟값 무시를 각각 독립 시험이 검출했다. 정상 코드를 커밋한 뒤 저장소 밖 overlay에서만 바꿨다.

#571 최신 소스의 기존 반례·코퍼스·두 30초 fuzz는 통과했지만, 별도 Unicode 헤더 이름 반례는 실제 `record_fact` 작성기에서 실패했다. [병합된 #571의 후속 리뷰](https://github.com/ABC2LAB/ABCDEF2LAB/pull/571#pullrequestreview-5914635014)에 인계했으며 이 PR에서 다른 담당의 가림 구현을 바꾸지 않았다. 위 1-D 회귀 통과를 모든 자격 가림 입력의 안전성 완료로 해석하지 않는다.


### #598 P1 후속: 실제 제품 PID256 수용 (2026-11-16)

검증 소스는 `2ec38bbe09d7ce4d69e3466b797ddb39205fdbd3`다. 최신 dev `14e0c817`의 #597 fixture 이름 정비와 #600 제품 로더 수정·시험을 일반 merge로 포함했다. 로더 변경은 1-D와 같은 검사였고, fixture 충돌은 대상 이름/업스트림 처리와 PID 수용 모드를 모두 보존했다. 뒤 문서 커밋은 실행 코드를 바꾸지 않는다.

실제 `deploy/product/Dockerfile`의 product-runtime 이미지 ID는 `sha256:7093e5b12f64a9da20a1b5cc89584c0fa2eb5f8f5547eafb3e675fde36b7e125`, 실행 helper SHA256은 `62c9c8ce82ca4a1dd1c245479298fe3edb90c4f8d7df5fc1fba4b01678cce1bc`다. Go 시험 바이너리로 제품 엔진을 대체하지 않았다. 제품 installer→preflight→prepare-service→API/workbench를 새 PostgreSQL16·볼륨·내부망에서 시작했다. preparation은 UID1000, read-only rootfs, cap_drop ALL, no-new-privileges, 기존 seccomp, PID256·메모리2GiB 그대로다. 제어기/합성 서버는 별도 수용 서비스이며 모델·Bash에 설치 비밀번호나 제어 토큰을 주지 않았다.

실행 환경은 Docker 29.9.3, 커널 `6.18.58.1-microsoft-standard-WSL2`, 논리 CPU14다. 실측 범위는 다음과 같다.

| 관찰 | 결과 |
| --- | --- |
| Worker/슬롯 | 실제 Manager3 / Bash3, HTTP 동시성16 |
| 회차 조건 파일 | task256·엔진 예비64·호출64·명령 hard/soft48·helper 여유16, queue_consumes_command_timeout=true |
| 첫 단계 | 두 무거운 명령이 한도에서 대기한 뒤 가벼운 세 번째가 응답·종료하고 다음 명령에 진입 |
| 다음 단계 | 세 명령 모두 NPROC48에서 fork EAGAIN을 확인한 채 동시에 GET 응답 대기. 자식 수42~43(기존 helper/부모 task 수에 따라 다름) |
| 엔진 스레드 최고 | 20. 초기 실측18보다 높지만 2배40을 다음 2의 거듭제곱으로 올려도 예비64는 동일 |
| cgroup 최고 | pids.current 관찰 최고와 pids.peak 모두 **167/256**(관찰용 Python1 포함), pids.events:max 증가 **0** |
| 결과 대조 | 실제 target 수신/송신/응답/호출 **6**, unknown/transient/unsupported **0** |
| 저장 마감 | 6개 tool_result 모두 completed·Final·ProcessDrained·ProxyDrained, 실행 audit_complete, 저장 run completed·drain_complete |

이보다 앞선 동일 실행 코드 `fed2dee6`의 새 제품 설치에서도 같은 6개 호출·응답·마감을 확인했고 최고163/256·엔진15·고갈0이었다. 실제 최대 한도까지 채운 짧은 두 단계 수용이며, 장시간 처리량/RSS 수용으로 확대하지 않는다. 유료/외부 LLM과 운영 대상은 사용하지 않았다.

추가 회귀: 코어 변경 패키지 전체 race **1120 PASS / 0 FAIL / 17 SKIP**, 실제 PG16 서버 경로·마감 집중 race **80 PASS / 0 FAIL / 0 SKIP**, 두 hard/soft 한도 실측 factory race **6 PASS / 0 FAIL**, dev 병합 제품 로더·제품 진입 race **124 PASS / 0 FAIL / 0 SKIP**. 전체 build/vet·gofmt·diff 검사와 평가 Python **1840개**가 통과했다. 코어 SKIP은 기존 DB 미공급/선택 환경 시험이며 필수 서버 Worker 결속·제품 생성자와 실제 제품 수용은 SKIP 없이 실행했다. 전체 저장소 PG16/race·크로스빌드는 최종 PR CI에서 확인한다.

외부 Go overlay 변이 6개(intent 잠금 반환, 이미 막힌 대기자의 Drain 깨움, 서버 admission Worker 결속, 제품 조기 admission Worker 결속, 명령의 helper 여유 전달, helper 전체 task 상한 전달)를 각각 **컴파일 성공 후 시험 실패**로 검출했다. 마지막 helper 전체 상한 변이는 처음 명령 ulimit만 읽던 시험에서 살아남아, 실제 PID1의 /proc/1/limits까지 읽는 시험을 추가한 뒤 검출했다. 원본 정상 커밋은 변이 중 수정하지 않았다.

실패도 별도로 남긴다. 최초 CONNECT 시험 클라이언트는 Python의 CONNECT 형식 때문에 미지원이었고, HTTP/1.1로 고쳤다. 그 뒤 호출 상한64만 적용한 제품 수용은 엔진 고갈 없이 helper 스레드 생성 실패로 끝났으며 **수용 실패**다. helper 여유16을 분리한 뒤 새 설치로 위 두 회차를 통과했다. 로컬 PG16 임시 설치의 LLVM/JIT 의존성 누락으로 전체 서버 시험 중 두 시험이 실패했으나, 같은 소스에서 라이브러리를 보완하고 해당 시험 4개(test/subtest)를 모두 재검증했다. 보호 저널 삭제·실패 회차 덮어쓰기로 수용 결과를 만들지 않았다.


### 재검토 비차단 후속: 실제 배치 PID 한도 검사

윤재상의 `9fe333d6` 재승인 때 온 비차단 제안도 반영했다. Runner 생성자가 실제로 보이는 cgroup v2 루트의 `pids.max`를 확인해, 배포 한도를 128/255로 낮춰 놓고 내부 배분은256인 채 시작하는 것을 막는다. 모델·설정·환경 변수로 이 검사나 기대 예산을 바꿀 수 없다. 파일 없음만 기존 호스트 경로로 두고 권한 오류·빈 값·잘못된 값·정수 넘침은 시작 실패다.

Runner 전체 race **98 PASS / 0 FAIL / 1 SKIP**(별도 Docker 입출력 수용 전용 시험), 실제 보호 구성을 사용한 생성자/경계 및 기존 압력 집중 시험 **13 PASS**, 제품 런타임 이미지 안의 실제 PID128(거부)·PID256(허용) 읽기 전용 Docker 시험 **2 PASS**다. 후자는 새 검사 코드를 담은 별도 정적 시험 바이너리로 커널 파일 I/O만 검증했고, 위 `2ec38bbe`의 제품 엔진 전체 3슬롯 수용과 구분한다. 최초 단위 fixture의 `/usr/bin/true`가 호스트에서 심볼릭 링크여서 거부된 실패는 소유 일반 파일 fixture로 바로잡아 같은 코드를 재검증했다.


### 원격 E②의 미전송 근거 연결

원격 엔진 lease에는 로컬 Execution 포인터가 없으므로, 미전송 확인도 로컬 포인터에만 의존하지 않게 연결했다. 원격 producer는 실제 저널 ACK와 exporter 검증을 끝낸 뒤 RPC 결과를 보낸다. 소비자는 인증된 RPC에서 실제 받은 not_sent 영수증의 값 사본을 보관하고, 같은 실행·intent의 관찰 callback에서 정확히 한 번 대조·소비한다. 현재 원격 scope/정책/헤더도 다시 대조한다. callback이 바꾼 값·RPC 근거 없음·중복은 거부하며, RPC 응답 자체가 없으면 미전송으로 추정하지 않는다. 새 RPC op나 외부 입력 칸은 추가하지 않았다.

실제 standalone backend·보호 저널·인증 RPC를 통해 Runner 시작 전 거부를 만들고, producer의 감사 ACK가 있음·실제 Call/대상 수신이 0임·소비자의 닫힌 호출 투영도 0임을 확인했다. 정상·위조·RPC 미수신·중복을 포함한 미전송 집중 race 시험17개와 원격 전체 회귀 race46개가 통과했다. 별도 원격 재전송 권한을 만들거나 모델의 미전송 주장을 신뢰하지 않는다.
