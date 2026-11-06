# E② 러너 제어 채널 제안

상태: **미합의 후보, 구현 없음.** [E②–러너 연결 제안](./e2-runner-integration-proposal.md) §2.3·§4.1·§4.3의 "runner 제어"를 메시지 수준으로 좁힌 문서다. 러너 쪽 구현 전에 형식을 먼저 맞추려고 쓴다. 담당: E② 쪽 이동찬, 러너 쪽 최민준.

범위 밖: 신뢰 launcher↔E²의 호출 제어(OpenCall/CloseCall, §2.3의 mTLS)와 서버의 사전 회차 등록(§7). 이 채널은 대상 송신 권한을 주지 않고, 모델·도구·엔진 API에는 열리지 않는다.

## 1. 전송

- **위치:** 러너가 회차마다 만든 `0700` 디렉터리의 Unix stream socket `e2-control.sock`. 이 디렉터리는 `eval-e2`에만 bind mount한다. E②가 listen하고 러너가 connect한다.
- **형태:** 연결 하나에 요청 한 줄과 응답 한 줄. UTF-8 JSON에 `\n`으로 끝나고 각 8 KiB 이하다. unknown·중복 키, NaN, 다른 `format`은 거부한다.
- **인증:** 경로 접근 제한과 `SO_PEERCRED`를 쓴다. 양쪽 모두 상대 uid가 `EVAL_RUNNER_UID`인지 확인한다. 별도 토큰은 두지 않는다. 엔진·도구는 uid가 다르고 이 mount가 없다는 §2.3 배치 음성 시험이 전제다.
- **환경:** bind mount된 socket을 쓸 수 없는 환경은 시작을 거부한다.
- **재시도:** 모든 요청은 같은 값으로 다시 보내도 결과가 같아야 한다. 연결이 끊기면 러너는 같은 `close_request_ref`로만 다시 묻는다.

## 2. 메시지

모든 요청과 응답에 `format=abc2lab.e2-runner-control/v1`, `type`, `eval_run_id`, `reservation_ref`가 있다. 값이 회차와 다르면 E²는 `error`로 답하고, 러너는 응답을 버린다.

| 요청 `type` | 추가 필드 | 응답 필드 |
|---|---|---|
| `ready` | 없음 | `state`=`starting`·`service_ready`·`failed`, `reason`, `config_sha256`, `header_mode`, `evaluation`, `policy_fingerprint_sha256` |
| `head` | 없음 | `state`=`no_execution`·`running`·`closing`·`complete`·`incomplete`, `execution_ref`, `run_ref`, `engine_task_id`, `policy_fingerprint_sha256`, `checkpoint` |
| `close` | `close_request_ref` | `close_request_ref`, `execution_ref`, `admission_closed` |
| `close_status` | `close_request_ref` | `state`=`closing`·`complete`·`incomplete`·`no_execution`, `reason`, `close_request_ref`, `execution_ref`, `manifest_sha256`, `checkpoint` |
| (거부) | | `type=error`, `code`(고정 코드) |

- `evaluation`은 `llm_backend`·`target_kind`만 담는다. `evaluation.run_ref`는 실행이 생긴 뒤 manifest에서 대조한다(§4.1, 지문에서도 제외됨). 평가 전달 모드가 아니면 `null`이다.
- `checkpoint`는 `{"sequence": 0 이상의 정수, "mac": 소문자 hex 64자}`이고 없으면 `null`이다.
- `state=service_ready`이면 `config_sha256`·`header_mode`·`policy_fingerprint_sha256`이 전부 있어야 한다. `starting`·`failed`이면 `null`이다.
- `state=complete`이면 `execution_ref`·`manifest_sha256`·`checkpoint`가 전부 있어야 하고, 응답은 manifest를 원자적으로 게시한 뒤에만 나간다(§4.2의 4번).
- `reason`과 `code`는 고정 어휘이고 원문 오류·경로·값을 담지 않는다. 비밀값·요청 내용을 담는 필드는 없다.

## 3. 러너 동작

| 단계 | 러너가 하는 일 | 실패할 때 |
|---|---|---|
| 작업 생성 전 | `ready`를 시한까지 물어 `service_ready`를 받고, 자기가 쓴 설정 파일 digest·선언한 전달 모드·동결 지문과 대조한다 | POST 전 시작 거부(기술적 무효) |
| 작업 생성 뒤(선택) | `head`로 실행 결속·지문을 근거에 남긴다 | 지문·task 불일치는 기술적 무효, 응답 없음은 기록 |
| 예산 종료 | 새 `close_request_ref`로 `close`를 보내고 `admission_closed=true`를 받은 뒤 `close_status`를 시한까지 반복해 묻는다 | 연결·시한 실패는 제품 실패로 기록하고 계측기 seal로 넘어간다(§4.3) |
| 수집 | `state`·`checkpoint`를 수집기(#288)의 close ACK로 넘긴다. `manifest_sha256`을 사본과 대조한다 | §4.3 갈래를 따른다 |

이미지·uid·망은 E² 자기 신고로 받지 않고 러너가 Docker 쪽에서 직접 확인한다. 시한은 설정값(`e2.ready_timeout_seconds`, `e2.close_ack_timeout_seconds`, `e2.close_timeout_seconds`)이고 실행 예산과 따로 기록한다.

## 4. 수용 시험 후보

| ID | 경우 | 기대 |
|---|---|---|
| CT01 | 다른 회차 값·다른 `format`·unknown 키·8 KiB 초과 | `error`, 상태 변화 0 |
| CT02 | 상대 uid가 다름 | 어느 쪽도 메시지를 처리하지 않음 |
| CT03 | `service_ready` 전 `close`, 같은 ref 재전송, 다른 ref로 재요청 | 첫째는 거부, 둘째는 같은 답, 셋째는 거부 |
| CT04 | `close` 뒤 신규 시도 | 대상 송신 0, 거부 기록 |
| CT05 | 게시 전 `close_status` | `closing`만, `complete` 없음 |
| CT06 | 감사 마감 실패 | `incomplete`와 고정 `reason`, `checkpoint`로 성공 head를 말하지 않음 |
| CT07 | 러너 시한 초과·연결 끊김 | 러너는 제품 실패로 기록하고 진행, E²는 추가 실행 없음 |

## 5. 합의할 것

1. **회차당 실행 수.** 러너와 수집기는 `execution_ref` 하나를 가정한다. 여러 intent는 같은 실행의 `RegisterIntent`로 처리한다는 제안이다. 실행이 여럿이면 `head`·`close_status`와 수집을 목록으로 바꿔야 한다.
2. **실행이 생기지 않은 회차.** 모델이 도구를 안 썼으면 `no_execution`으로 답하고 manifest를 만들지 않는다는 제안이다. 러너는 이를 차단 0건이나 완료로 채우지 않고 따로 기록한다.
3. **인증.** 토큰 없이 `SO_PEERCRED`와 mount 제한으로 충분한지.
4. **`head`.** 작업 생성 뒤 실행 결속을 러너가 볼 다른 경로가 있으면 뺀다.
