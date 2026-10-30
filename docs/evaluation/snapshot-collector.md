# 평가 HTTP 스냅샷 수집기

`collect_snapshot`은 설정된 로컬 HTTP 계측 경로에서 응답 body를 받은 뒤 [스냅샷 저장기](snapshot-writer.md)에 전달한다.
[러너 기록](run-record.md)과 저장기에 의존하는 후속 로컬 구현이다. PR #34·#40의 원격 브랜치는 변경하지 않는다.
현재 검증 대상은 자체 loopback 합성 서버이며 실제 Juice Shop·모델·baseline 실행 결과는 없다.

## 설정과 호출

```python
from snapshot_collector import SnapshotEndpoint, collect_snapshot

endpoint = SnapshotEndpoint(
    origin=target_origin,
    path=registered_scoreboard_path,
    timeout_seconds=socket_timeout,
    max_bytes=snapshot_max_bytes,
)
collected = collect_snapshot(endpoint, run_directory, phase, snapshot_context)
runner_record["artifacts"][phase + "_raw"] = collected.artifacts.raw
runner_record["artifacts"][phase] = collected.artifacts.normalized
```

origin·path·포트·시간·크기 상한은 외부 설정에서 전달한다. URL·대상 앱 경로를 수집기 코드에 고정하지 않는다.
호출자는 이미 비변경 조회로 확인한 챌린지 계측 경로만 등록하며, LLM이 만든 임의 URL이나 일반 탐색 경로를 전달하지 않는다.
GET도 상태를 바꿀 수 있으므로 이 API의 GET 고정만으로 경로의 비변경성을 증명하지 않는다.
이 API는 평가기 전용이다. 응답/GT·수집 결과를 엔진·LLM에 전달하지 않는다.

## 지원 범위와 통신 제한

- 첫 지원은 HTTP loopback IP와 `localhost`다. `localhost` 연결 주소는 `127.0.0.1`로 고정하고 요청 Host는 설정 origin을 유지한다.
- 외부/사설 원격 IP·임의 DNS 이름·HTTPS는 거부한다. HTTPS를 HTTP로 바꾸어 재시도하지 않는다.
- origin에는 사용자명/비밀번호·추가 경로·query·fragment를 넣을 수 없다. path는 `/`로 시작하는 ASCII 조회 경로이며 상대/다른 origin·query·fragment·제어문자를 거부한다.
- 수집은 GET 1회다. 환경 프록시·cookie jar·인증정보·리다이렉트·자동 재시도를 사용하지 않는다.
- HTTP 200, `application/json`, 비압축(identity) 응답만 받는다. chunked는 지원하지만 Content-Length와 함께 오는 framing, chunk trailer, 종료 뒤 잔여 바이트는 거부한다.
- 선언된 길이와 실제 수신 전송 바이트 모두에 상한을 적용한다(chunk framing도 상한에 포함). Connection: close의 EOF까지 확인해 선언 길이 뒤의 잔여 데이터도 거부한다. 잘린 응답·잘못된 길이·상태·Content-Type·압축은 저장하지 않는다.
- `timeout_seconds`는 연결/읽기의 socket 비활성 제한이다. 지속적으로 도착하는 응답까지 포함한 전체 경과시간 예산은 러너에서 별도로 관리해야 한다.

원본 body는 수집기의 API 인증 필드 별칭 검사와 저장기의 JSON/GT/민감 필드 검사를 통과한 후 파일로 기록한다. 응답 헤더·오류 body는 결과물이나 오류 메시지에 저장하지 않는다.
계측 요청 수는 엔진의 전송 HTTP 수와 분리해 기록한다. 계측의 solved 관찰만으로 엔진 해결 귀속이나 실제 검사 커버리지를 인정하지 않는다.

## 저장·수명 경계

송신 전에 회차 신원·pre/post·출력 디렉터리를 검사한다. 이미 있는 단계나 symlink·잘못된 출력 설정에는 송신하지 않는다.
검사 후 다른 프로세스가 같은 단계를 만들면 저장기의 독점 생성이 덮어쓰기를 거부한다.
수집 결과의 `requested_at`·`received_at`은 timezone 포함 실제 계측 시각이다. 벽시각 역행은 오류다.
pre는 엔진 시작 전, post는 신규 송신 중지와 진행 요청 종료 확인 후에 러너가 호출한다. 증가 sequence와 종료 근거는 신뢰 러너가 기록한다.
수집 완료는 엔진 요청 종료·해결 주체·HMAC 서명을 대신하지 않는다.

## 검증

소유한 임시 loopback HTTP 서버와 합성 JSON만 사용한다. 정상 수신 건수, redirect 목적지/proxy 수신 0건, 인증정보 미전달,
송신 전 잘못된 설정 차단, 잘못된 응답·크기·타임아웃 거부, pre/post 저장기 연결을 시험한다.
소켓 생성이 제한된 sandbox에서는 해당 로컬 HTTP 테스트에 실행 권한이 필요하며, 이를 테스트 skip이나 성공으로 처리하지 않는다.
