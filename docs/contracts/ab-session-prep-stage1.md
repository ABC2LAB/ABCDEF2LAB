# A/B 세션·fixture 준비 절차 (1단계)

상태: **초안(최민준, 11/09 계약 정렬용)**. 담당과 장비 경로는 공급 담당이 확인하기 전까지 제안이다.

새 형식을 만들지 않는다. 세션 전달은 [E② private snapshot](./e2-server-runtime.md), KG 대응은 [registry §9](./kg-fixture-registry.md#9-e②-private-snapshot과의-대응1단계-준비-순서), 표식은 [canary fixture](./idor-canary-fixture.md)를 그대로 따른다. 이 문서는 셋을 잇는 준비 순서·A/B 예시·실패 처리만 더한다.

범위는 비평가 통합 회차(`EvalRunID` 없음)의 수평 비변경 조회다. 계정 둘(A 소유자, B 교차)과 자원 하나만 다룬다. 평가 회차와 쓰기는 범위 밖이다.

## 담당

| 일 | 담당 |
|---|---|
| 대상 reset, 세대 참조 발급, A/B 계정·세션 확보, 표식 심기 실행, 파일 작성 | 윤재상(fixture 하네스)·운영자 |
| 표식 carrier 선정, 소유·정책·표식의 registry 내용 | 이경준 |
| snapshot 수용·검증·수명, snapshot과 registry의 짝 설정 | 이동찬 |
| 판정 계약, 평가 쪽 GT 상관(type 고정 뒤) | 최민준 |

실제 실행 주체와 장비 경로는 공급 담당의 확인을 받아 입력 대장에 적는다.

## 파일

전부 `ARTEX_E2_SNAPSHOT_DIR` 바로 아래에 둔다. 디렉터리는 정확히 `0700`, 파일은 정확히 `0600`인 일반 파일이어야 한다. 파일명은 경로가 없는 한 단계 이름이고 심볼릭 링크는 거부된다.

| 파일 | 내용 | 규칙 |
|---|---|---|
| `<snapshot_ref>.json` | 승인·예산·세션 참조·자원. [예시](./ab-session-prep-stage1.snapshot.example.json) | 세션 원문을 넣지 않는다. 모르는 필드는 거부된다. 1MiB 이하 |
| registry JSON | 소유·정책·표식. [뼈대](./kg-registry-stage1.skeleton.json) | snapshot 파일명과 달라야 한다. 신뢰 host의 `FixtureRegistries`가 짝을 정하고, JSON·HTTP 요청으로 고르지 않는다 |
| `<label>-session.secret` | 그 세션 헤더의 값 그대로(`Authorization`이면 스킴까지) | 8192바이트 이하, 끝 개행 하나만 제거된다. 헤더 이름 `Host`는 거부된다. 감사 키와 같은 값이면 거부된다 |
| `audit.key` | 32바이트 임의 바이너리 HMAC 키 | 세션 파일로 재사용할 수 없다. 보존은 [감사 저장기](./e2-audit-journal.md) 기준을 따른다 |

예시의 두 JSON은 `server/controlled_read_ab_prep_example_test.go`가 실제 로더·`CheckPairing`·전송기 생성 검사로 대조한다. 값은 전부 자리표시이고, `basket`도 뼈대를 따른 자리표시다. 최종 type이 정해지면 두 파일을 같이 바꾼다. 어긋나면 시험이 실패한다.

### 값의 출처

| snapshot | 출처 | registry 짝 |
|---|---|---|
| `sessions[].label` | 하네스가 정한 계정 라벨 | `principals[].label` |
| `sessions[].status`, `handle` | 운영자가 로그인과 계정을 확인한 값. 서버가 로그인을 검증한 결과가 아니다 | 없음 |
| `reset_ref` | reset 뒤 하네스가 발급한 세대 | `reset_generation` |
| `resources[].ref` | carrier 선정으로 고정한 type과 이 세대의 id | `resources[].resource_ref` |
| `resources[].view.Markers[]` | 심은 표식. 같은 묶음을 양쪽에 그대로 넣는다 | `resources[].canaries[]` |
| `resources[].accounts` | 조회를 허용한 계정 목록. 소유를 뜻하지 않는다 | 없음(소유는 `owner_ref`) |
| `task_id`, `intent_id` | 엔진이 만든 작업과 intent(열린 항목 2) | 없음 |
| `origin`, `connect_ip`, 한도, `approval_ref` | 운영자 승인값 | 없음 |

## 순서

1. **carrier 확인.** [carrier 선정](./kg-canary-carrier-stage1.md)으로 type이 고정됐는지 확인한다. 고정 전에는 실제 등록을 하지 않는다.
2. **reset.** 대상을 reset하고 health를 확인한다. 하네스가 준비 회차 ID(`run_id`)와 세대(`generation_ref`, `reset_generation`)를 발급한다. 준비 회차 ID는 E② 개별 실행 참조와 다른 값이다.
3. **세션 확보.** A·B로 로그인해 두 계정이 서로 다른지 확인한다. 세션 값은 `umask 077` 셸에서 `<label>-session.secret`에만 쓴다. 명령 인자·셸 기록·로그·LLM 입력·근거 파일에 남기지 않는다.
4. **표식 심기.** 승인된 fixture 단계에서 A 소유 자원에 무작위 표식을 심는다. 조회 경로와 분리된 준비 쓰기이고, E② 조회 전송기의 제한을 풀지 않는다.
5. **registry 작성.** 심은 묶음을 같은 단계에 registry에 적는다. 심기와 등록 중 하나만 되면 준비 실패다.
6. **소유자 확인.** A 세션의 자기 조회로 표식이 그 필드에 그대로 돌아오는지 본다. 교차 조회는 여기서 하지 않는다. 교차 조회는 등록 뒤 E②를 통해서만 나간다.
7. **snapshot 작성.** 작업과 intent가 생긴 뒤 `task_id`·`intent_id`를 넣어 쓴다. `expires_at`은 등록 시점에 아직 지나지 않아야 하고, 세션 실제 만료와 registry 정책 유효 구간을 넘지 않게 잡는다.
8. **등록.** `POST /api/tasks/{id}/intents/{iid}/controlled-read`에 `{"snapshot_ref": …}`를 보낸다. 서버가 registry를 동결하고 대조한다. 등록 뒤 파일을 바꿔도 그 실행에는 반영되지 않는다.
9. **정리.** 회차가 끝나면 세션 파일·snapshot·registry 사본을 지우고 입력 대장에 지운 시각을 남긴다. 다음 회차에 재사용하지 않는다.

## 실패와 수명

부분 실패를 고쳐서 이어가지 않는다. 묶음을 버리고 새 세대로 2번부터 다시 한다.

| 상황 | 결과 | 다음 |
|---|---|---|
| 세션 만료·회수 | 신규 송신 차단 | 새 세대로 다시 준비 |
| 대상 reset, 세대 변경 | 이전 묶음 무효, 재사용 금지 | 새 세대로 다시 준비 |
| 앱이 회차 중 세션 값을 바꿈 | 등록 뒤 세션을 갱신하는 경로가 없어 지원하지 않음. 그 회차를 버린다 | 새 세대로 다시 준비 |
| snapshot·registry 불일치 | 등록 거부(`pairing_labels`, `pairing_resources`, `pairing_reset`, `pairing_markers`) | 파일을 고치지 말고 묶음 전체를 다시 만든다 |
| 표식이 등록값과 다름 | 송신 전 거부(`canary_mismatch`) | 위와 같다 |
| 소유자 확인 실패 | 준비 실패, 등록하지 않음 | 위와 같다 |

## 넘기지 않는 것

세션 값·표식 값·키는 레포, 작업 설명, 전달 JSON, 공유 로그, 근거 파일, LLM 입력 어디에도 넣지 않는다. 입력 대장에는 라벨과 시각만 적는다. 대상 로그인 경로·계정·엔드포인트는 입력 자료이고 코드에 박지 않는다.

## 열린 항목

1. **최종 type.** carrier 측정(`review`→`feedback`→`export`→`user`, 첫 전부 통과 채택)으로 고정한다(이경준·운영자 측정, 이동찬 등록 확인). GT 상관은 그 뒤에 평가 쪽에서 한다.
2. **`task_id`·`intent_id` 시점(이동찬).** 지금 로더는 JSON 값이 등록 요청과 같아야 해서 intent가 생긴 뒤에만 쓸 수 있다. 비평가 회차에는 문제없지만, 평가 회차처럼 시작 전에 준비를 봉인하려면 이 결속을 어떻게 할지 정해야 한다.
3. **입력 대장(이동찬·공급 담당).** 실제 실행 주체, 장비 경로, 삭제 확인 방식.
