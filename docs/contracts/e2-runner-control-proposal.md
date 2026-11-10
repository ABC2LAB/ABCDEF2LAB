# E② 러너 제어 채널 제안

상태: **서비스 쪽 구현됨(`cmd/e2-eval-service`, 2026-11-10). 실제 컨테이너에서 러너 클라이언트로 `ready`·`bind`·`head`·`close`·`close_status`를 통과했고 실행 권한은 아직 없다.** [E②–러너 연결 제안](./e2-runner-integration-proposal.md) §2.3·§4.1·§4.3의 "runner 제어"와 준비 설정을 메시지 수준으로 좁힌 문서다. 러너 쪽 구현 전에 형식을 먼저 맞추려고 쓴다. 담당: E② 쪽 이동찬, 러너 쪽 최민준.

범위 밖: 신뢰 launcher↔E²의 호출 제어(OpenCall/CloseCall, §2.3의 mTLS)와 서버의 사전 회차 등록(§7). 이 채널은 대상 송신 권한을 주지 않고, 모델·도구·엔진 API에는 열리지 않는다.

## 1. 전송

- **위치:** 러너가 회차마다 만든 `0700` 디렉터리의 Unix stream socket `e2-control.sock`. 이 디렉터리는 `eval-e2`에만 bind mount한다. E②가 listen하고 러너가 connect한다.
- **형태:** 연결 하나에 요청 한 줄과 응답 한 줄. UTF-8 JSON에 `\n`으로 끝나고 각 8 KiB 이하다. unknown·중복 키, NaN, 다른 `format`은 거부한다.
- **인증:** 경로 접근 제한과 `SO_PEERCRED`를 쓴다. 양쪽 모두 상대 uid가 `EVAL_RUNNER_UID`인지 확인한다. 별도 토큰은 두지 않는다. 엔진은 이 mount가 없는 다른 컨테이너에 있고, 도구는 `eval-e2`의 샌드박스에서만 돈다. 도구의 바깥 uid는 러너와 같아서 `SO_PEERCRED`로는 구분되지 않고, 샌드박스 루트에 소켓 경로가 없다는 점이 전제다(연결 제안 §2.3의 2026-11-10 결정). 이 음성 시험은 실행 경로를 붙일 때 실제 컨테이너에서 확인한다.
- **인증 범위:** 평가 배치에서 엔진·도구를 막는 장치다. 같은 uid로 도는 다른 호스트 프로세스는 구분하지 못한다. 그래서 프로토콜 §2의 전용 호스트 운영 조건이 함께 있어야 하고, 평가 밖에서 쓰려면 별도 역할 uid나 회차 자격이 필요하다.
- **환경:** bind mount된 socket을 쓸 수 없는 환경은 시작을 거부한다.
- **시한:** 러너는 단계 전체 시한을 연결·보내기·받기마다 남은 시간으로 적용한다. 조금씩 오는 답이 시한을 늘리지 못하고, 시한 뒤 도착한 답은 받지 않는다.
- **재시도와 조회:** `ready`·`head`·`close_status`는 상태 변화 없이 **현재 상태를 돌려주는 조회**라서, 같은 요청에도 답이 `starting`→`service_ready`, `closing`→`complete`처럼 바뀐다. 상태를 바꾸는 요청은 `bind`와 `close`뿐이고 둘 다 멱등이다. 같은 값의 `bind`는 같은 답을 돌려주고, 다른 값은 `error`다. 같은 `close_request_ref`의 `close`를 다시 보내도 종료 절차는 한 번만 돌고 답도 같다. 연결이 끊기면 러너는 같은 값으로만 다시 묻는다.

## 2. 메시지

모든 요청과 응답에 `format=abc2lab.e2-runner-control/v1`, `type`, `eval_run_id`, `reservation_ref`가 있다. 값이 회차와 다르면 E²는 `error`로 답하고, 러너는 응답을 버린다.

| 요청 `type` | 추가 필드 | 응답 필드 |
|---|---|---|
| `ready` | 없음 | `state`=`starting`·`service_ready`·`failed`, `reason`, `config_sha256`, `header_mode`, `evaluation`, `policy_fingerprint_sha256` |
| `bind` | `reset_generation` | `reset_generation` |
| `head` | 없음 | `state`=`no_execution`·`running`·`closing`·`complete`·`incomplete`, `execution_ref`, `run_ref`, `engine_task_id`, `policy_fingerprint_sha256`, `checkpoint` |
| `close` | `close_request_ref` | `close_request_ref`, `execution_ref`, `admission_closed` |
| `close_status` | `close_request_ref` | `state`=`closing`·`complete`·`incomplete`·`no_execution`, `reason`, `close_request_ref`, `execution_ref`, `manifest_sha256`, `checkpoint` |
| (거부) | | `type=error`, `code`(고정 코드) |

- `bind`는 `service_ready` 뒤에만 받는다. `reset_generation`은 대상 컨테이너 ID라서 compose 기동 전에 쓰는 설정 파일에 넣을 수 없다. E②는 이 값을 고정해 manifest에 그대로 쓴다. 이 값으로 대상 송신 권한이 생기지 않는다.
- `evaluation`은 `llm_backend`·`target_kind`만 담는다. `evaluation.run_ref`는 실행이 생긴 뒤 manifest에서 대조한다(§4.1, 지문에서도 제외됨). 평가 전달 모드가 아니면 `null`이다.
- `checkpoint`는 `{"sequence": 0 이상의 정수, "mac": 소문자 hex 64자}`이고 없으면 `null`이다.
- `state=service_ready`이면 `config_sha256`·`header_mode`·`policy_fingerprint_sha256`이 전부 있어야 한다. `starting`·`failed`이면 `null`이다.
- `state=complete`이면 `execution_ref`·`manifest_sha256`·`checkpoint`가 전부 있어야 하고, 응답은 manifest를 원자적으로 게시한 뒤에만 나간다(§4.2의 4번).
- `reason`과 `code`는 아래 고정 어휘이고 원문 오류·경로·값을 담지 않는다. 비밀값·요청 내용을 담는 필드는 없다. 어휘 밖 값이 오면 러너는 값을 옮기지 않고 형식 오류로 처리한다.

| 필드 | 어휘 |
|---|---|
| `ready.reason` | `config_rejected`, `policy_rejected`, `audit_unavailable`, `startup_failed` |
| `close_status.reason` | `e2proxy.Reason` 값 전부, `drain_timeout`, `audit_incomplete`, `publish_failed` |
| `error.code` | `other_run`, `bad_request`, `not_ready`, `bind_conflict`, `close_ref_conflict`, `closed`, `unavailable` |

## 3. 러너 동작

| 단계 | 러너가 하는 일 | 실패할 때 |
|---|---|---|
| 작업 생성 전 | `ready`를 시한까지 물어 `service_ready`를 받고, 자기가 쓴 설정 파일 digest·선언한 전달 모드·동결 지문과 대조한다. 그 뒤 `bind`로 `reset_generation`을 넘긴다 | POST 전 시작 거부(기술적 무효) |
| 작업 생성 뒤(선택) | `head`로 실행 결속·지문을 근거에 남긴다 | 지문·task 불일치는 기술적 무효, 응답 없음은 기록 |
| 예산 종료 | 새 `close_request_ref`로 `close`를 보내고 `admission_closed=true`를 받은 뒤 `close_status`를 시한까지 반복해 묻는다 | 연결·시한 실패는 제품 실패로 기록하고 계측기 seal로 넘어간다(§4.3) |
| 수집 | `state`·`checkpoint`를 [수집기](../../scripts/eval/e2_collect.py)의 close ACK로 넘긴다. `manifest_sha256`을 사본과 대조한다 | §4.3 갈래를 따른다 |

이미지·uid·망은 E² 자기 신고로 받지 않고 러너가 Docker 쪽에서 직접 확인한다. 시한은 설정값(`e2.ready_timeout_seconds`, `e2.close_ack_timeout_seconds`, `e2.close_timeout_seconds`)이고 실행 예산과 따로 기록한다.

## 4. 설정 파일

러너가 회차마다 `0700` 준비 디렉터리에 `0600`으로 쓰고 `eval-e2`에 읽기 전용으로 mount한다. `config_sha256`은 이 파일의 정확한 바이트 digest다. 닫힌 JSON이고 unknown 키는 거부한다.

| 필드 | 뜻 |
|---|---|
| `format` | `abc2lab.e2-runner-config/v1` |
| `eval_run_id`, `reservation_ref`, `model_tag` | 회차 값. manifest에 그대로 쓴다 |
| `header_mode`, `evaluation` | 전달 모드와 평가 선언(`llm_backend`·`target_kind`, 아니면 `null`) |
| `policy_file` | 준비 디렉터리 안의 정책 입력 파일 이름. 검토·동결된 파일을 러너가 복사하고 회차 감사 키의 `audit_key` pin을 채운다 |
| `audit_key_file` | 준비 디렉터리 안의 회차 감사 키 파일 이름(32바이트, 값은 설정에 넣지 않음) |
| `listen`, `upstream` | 엔진 쪽 listen 주소와 계측기 upstream(`http://eval-meter-upstream:<포트>`) |
| `evidence_dir`, `control_dir` | 컨테이너 안 경로(`/evidence`, `/run/e2-control`) |

회차마다 달라지는 값을 담으므로 이 digest는 정책 지문과 다르다. 지문은 정책 내용만 본다(연결 제안 §4.1).

## 5. 수용 시험 후보

| ID | 경우 | 기대 |
|---|---|---|
| CT01 | 다른 회차 값·다른 `format`·unknown 키·8 KiB 초과 | `error`, 상태 변화 0 |
| CT02 | 상대 uid가 다름 | 어느 쪽도 메시지를 처리하지 않음 |
| CT03 | `service_ready` 전 `close`, 같은 ref 재전송, 다른 ref로 재요청 | 첫째는 거부, 둘째는 같은 답, 셋째는 거부 |
| CT08 | `service_ready` 전 `bind`, 같은 값 재전송, 다른 값 | 첫째는 거부, 둘째는 같은 답, 셋째는 거부. manifest의 값은 처음 고정한 값 |
| CT04 | `close` 뒤 신규 시도 | 대상 송신 0, 거부 기록 |
| CT05 | 게시 전 `close_status` | `closing`만, `complete` 없음 |
| CT06 | 감사 마감 실패 | `incomplete`와 고정 `reason`, `checkpoint`로 성공 head를 말하지 않음 |
| CT07 | 러너 시한 초과·연결 끊김 | 러너는 제품 실패로 기록하고 진행, E²는 추가 실행 없음 |

## 6. 정한 것

- **회차당 실행 하나.** 여러 intent는 같은 실행의 `RegisterIntent`로 처리한다. 러너·수집기가 `execution_ref` 하나를 가정한다. 다중 실행은 descriptor와 수집기 계약을 목록으로 바꾼 뒤 따로 지원한다.
- **실행이 생기지 않은 회차.** `no_execution`으로 답하고 manifest를 만들지 않는다. 러너는 이를 완료나 차단 0건으로 해석하지 않고 따로 기록한다.
- **인증.** §1의 범위로 한정해서 `SO_PEERCRED`와 mount 제한만 쓴다.
- **`head` 유지.** `ready`는 작업 생성 전, `close`는 사후라서 현재 task·실행·정책의 조기 결속 확인을 대신하지 못한다.
