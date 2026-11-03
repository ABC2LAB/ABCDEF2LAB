# 작업 모드 marker 읽기 어댑터 초안

2026-11-02 · 이동찬

범위: [marker 판정표](./task-mode-marker.md)의 FM01~08을 합성 파일로 확인하는 **읽기 전용 어댑터**다. 기존 JSON 형식과 [DB reader callback](./task-mode-db.md) 계약을 유지한다. 실제 root 신뢰 설정·등록 잠금 증명 공급자, 파일 writer의 DB 잠금 참여, 이관·admission·finding/retest 저장 연결은 별도 후속이다. 기존 runtime은 이 reader를 호출하지 않는다.

## 입력과 수명

[`newControlledTaskMarkerReader`](../../server/controlled_read_marker_reader.go)는 다음 host-owned 입력을 받아 `db.TaskModeMarkerReader` callback을 만든다. 모델·API 요청의 값으로 구성하지 않는다.

| 입력 | 요구 조건 |
|---|---|
| `RootPath` | 절대·정규 경로. reader가 자동 정규화하거나 생성·복구하지 않음 |
| `RootAnchor` | 신뢰된 초기화가 확보한 열린 root fd. 호출이 끝날 때까지 유지하는 borrowed handle |
| `CheckScope(ctx, taskID)` | caller가 이미 보유한 같은 task 등록 동기화와 저장소 신뢰가 유지되는지 검사. 누락·오류면 차단 |
| `MaxEntries` | 명시적 1~4096. marker 외 항목도 포함한 전체 root 목록 상한 |

현재 경로를 매번 새로 열어 anchor로 넣으면 교체 이전 root와의 연속성을 증명하지 못한다. fd identity는 같은 프로세스에서 보유한 anchor와의 비교일 뿐이며, 재기동·복원·rollback 이전 상태의 인증을 대신하지 않는다. 상위 경로·영속 저장소·허용 writer는 host의 신뢰 경계다.

`CheckScope`는 callback 입구, 첫 검증 뒤, 마지막 반환 전에 호출된다. 새 transaction·잠금·등록 완료 대기를 만들면 안 된다. DB reader가 이미 보유한 task row 잠금과 writer가 같은 동기화 규약에 참여하는지 host가 증명해야 한다. 검사 함수를 제공했다는 사실만으로 실제 SQL 잠금이 인증되지는 않는다. 이번 합성 시험의 scope 함수는 실제 DB 경합 검증이 아니다.

## Linux 파일 확인

[`Linux 구현`](../../server/controlled_read_marker_reader_linux.go)은 root를 `O_DIRECTORY|O_NOFOLLOW`로 읽기 전용으로 열어 exact `0700`과 anchor inode를 비교한다. fd 기준으로 전체 목록을 상한 내에서 읽고, marker를 검증한 뒤 두 번째 목록과 각 파일의 inode·mode·size·mtime, 마지막 root 경로를 다시 확인한다. 목록마다 별도 directory open description을 사용하므로 동시 reader가 offset을 공유하지 않는다. reader 소유 fd의 close 오류도 결과를 무효화하며 borrowed anchor는 닫지 않는다. [기존 private reader](../../server/controlled_read_private_linux.go)의 `readPrivateAt`, `markerFile`, ID 규칙을 재사용하고, [reader 전용 JSON 검사](../../server/controlled_read_marker_json.go)로 marker 필드를 확인한다.

예약 namespace는 대소문자 무시 기준 `controlled-`로 시작하거나 `.marker`로 끝나는 이름이다. 실제 허용 이름은 소문자 `controlled-<task>.task.marker`, `controlled-<task>-<intent>.marker`이며 ID는 선행 0 없는 양의 int64 십진수다. 불명확한 이름을 무시해 `clear`로 만들지 않는다.

보수적인 초안으로 **다른 task의 예약 marker도 이름·파일·JSON 검증을 마친 뒤** 대상에서 제외한다. 다른 task의 정상 파일은 대상의 존재 근거가 아니며, 손상된 예약 파일은 대상과 무관해 보여도 조회를 차단한다. 이 선택은 가용성을 줄일 수 있다. 일반 비marker 항목은 목록 상한에만 포함하고 snapshot·journal·checkpoint·시크릿 본문을 열지 않는다.

각 marker는 regular file exact `0600`, 최대 4096 bytes, 링크/FIFO 거부, strict JSON·filename/JSON task/intent 일치·`controlled=true`를 요구한다. 필드명은 JSON escape 해석 뒤 정확한 `task_id`·`intent_id`·`controlled`만 허용한다. 중복·unknown·대소문자 변형·null·다른 타입을 거부한다. `task_id`와 boolean `controlled`는 필수이며 `intent_id`는 int64 범위의 음수가 아닌 정수다. task marker의 누락 `intent_id`는 기존 호환대로 0으로 읽는다. task marker 뒤 intent가 손상돼도 부분 `present`를 반환하지 않는다.

| 전체 검증 성공 시 대상 파일 | 결과 |
|---|---|
| task + intent | `present` (FM01) |
| task만 | `present` (FM02) |
| intent만 | zero marker + 오류 (FM03) |
| 둘 다 없음 | `clear` (FM04) |

root·파일·목록·scope·취소 오류(FM05~08)는 위 성공보다 우선하며 zero marker와 오류를 반환한다. 취소는 표준 context 오류를 유지하고 다른 오류는 경로·원문·하위 오류를 포함하지 않는 고정 범주다. 비 Linux에서는 고정 unsupported 오류로 거부한다.

두 번의 조회는 관찰된 교체·소멸을 탐지할 뿐, 중간에 바꿨다 되돌리는 ABA나 잠금에 참여하지 않는 writer를 배제하지 못한다. 실제 활성화에는 등록 직렬화와 저장소 신뢰의 별도 증명이 필요하다. 파일은 실행 lease·새 run·승인·취약점 확정 권한이 아니다.

## 검증 경계

[`합성 파일 시험`](../../server/controlled_read_marker_reader_linux_test.go)은 FM01~08, 명시적 입력 누락, 목록 상한·오류, 파일 파싱·권한·타입·읽기/close 실패, root/file 교체와 취소·동시 reader를 다룬다. per-call private ops로 오류를 주입하며 전역 hook이나 실제 state 파일을 사용하지 않는다. 정상 대조군과 오류 시 zero marker를 함께 확인한다.

실제 DB 등록과 reader의 양방향 경합(MT06), crash·이관 멱등성·실행/저장 통합(MT07~09), 재기동 root 신뢰 회복은 이번 시험에 포함되지 않는다. 비 Linux 검증은 대표 target 컴파일이며 실행 시험과 구분한다. 기존 [판정표](./task-mode-marker.md)의 이관·활성화 기준은 유지한다.

**확인한 결과:** 신규 reader 최하위 사례 67개, 기존 private/store 38개와 taskmode 37개가 race에서 통과했고 실패·skip은 0이다. 최신 dev 반영 뒤 server 대상 105개·vet·전체 build를 재확인했으며 reader와 taskmode 소스는 이전 검증본과 동일하다. Darwin server 테스트 바이너리의 cross-compile도 통과했다. 비 Linux 실행이나 실제 DB/file 등록 경합을 시험한 결과는 아니다.

**리뷰 후속 검증(2026-11-02):** `controlled` 대소문자 별칭의 마지막 값으로 손상된 marker가 통과함을 수정 전 재현했다. reader 전용 필드/타입 검사와 신규 JSON 사례 10개를 추가했고 marker reader·private/store 대상 race, 전체 Go 시험·vet·build·gofmt/diff 및 Darwin server cross-build를 통과했다. 기존 JSON 형식의 task marker `intent_id` 생략과 단일 escaped key·필드 순서 변경은 정상 처리한다. 비 Linux 실행·DB/file 등록 경합·운영 배선은 여전히 미실행이다.
