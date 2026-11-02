# E② 서버 실행 컨텍스트와 Worker 연결

상태: 등록된 HTTP GET을 실제 `Engine.runIntent → Worker.Execute`에 연결한 구현. [전송기](./e2-http-transmitter.md)와 [감사 저장기](./e2-audit-journal.md)를 사용한다. 자동 로그인·KG 저장소·IDOR 판정/최초 finding 저장·UI는 이 단계에서 연결하지 않는다. 통제 Worker는 `report_finding`을 제공하지 않는다.

## 서버가 보유하는 승인 정보

기존 자산의 Auth 값·작업 설명·운영자 JWT를 대상 서비스의 검증된 세션으로 취급하지 않는다. 운영자는 대상 로그인을 확인하고 조회만 허용되는 자원·계정·reset 세대·승인 정책·예산을 private snapshot으로 준비한다. `status="verified"`는 이 신뢰 입력의 운영자 확인값이며 서버가 로그인 검증을 대신 수행한 결과가 아니다.

- `ARTEX_E2_SNAPSHOT_DIR`: 승인 JSON과 시크릿 파일의 전용 `0700` 디렉터리. 기본은 `dataDir/e2-snapshots`다. 소스 실행의 `data/`는 Git 제외 대상이다.
- `ARTEX_E2_STATE_DIR`: 통제 표시·journal·체크포인트를 보존할 전용 `0700` 디렉터리. 기본은 `dataDir/e2-state`다. 컨테이너의 기본 `dataDir`는 `/app/data`이며 이 디렉터리의 영속 저장소를 실제 배포에서 유지해야 한다.
- 각 파일은 일반 파일 `0600`이어야 한다. snapshot 참조·시크릿 파일명은 단일 구성요소만 허용하며 경로 이동·심볼릭 링크를 거부한다.
- Linux private reader는 마지막 부모 디렉터리를 fd로 고정하고 정확한 `0700`인지 검사한다. 부모 경로의 끝 `/`를 제거한 뒤 마지막 디렉터리 링크를 거부하며, 자식은 `openat`의 `O_NOFOLLOW`·`O_NONBLOCK`으로 열어 링크·FIFO 대기로 인한 우회를 방지한다. 실제로 연 파일 fd의 일반 파일·정확한 `0600`·크기를 확인하고 상한+1바이트 읽기 및 close 오류도 검사한다. 특수 권한 비트가 있는 파일/디렉터리는 거부한다. 소유자 UID 제한은 추가하지 않는다.
- 상태 디렉터리는 재시작 후에도 같은 저장소를 사용한다. 서버 초기화에서 저장소를 열 수 없으면 시작을 명시적으로 중단한다. 실행 중 표시 읽기 오류는 일반 Worker로 대체하지 않고 차단한다. 이 저장소와 감사 경로는 Linux를 지원한다.

승인 파일명은 `<snapshot_ref>.json`이고 다음 형식을 사용한다. 예시의 식별자·주소·만료·reset/승인은 실제 등록값으로 교체한다. 세션 원문은 이 JSON에 넣지 않는다.

```json
{
  "task_id": "17",
  "intent_id": 22,
  "case_ref": "approved-read-22",
  "reset_ref": "operator-verified-reset",
  "policy_ref": "operator-read-approval",
  "origin": "http://approved-target.example:8080",
  "connect_ip": "192.0.2.10",
  "expires_at": "2026-10-30T12:00:00Z",
  "max_requests": 2,
  "min_interval_ms": 100,
  "request_timeout_ms": 5000,
  "max_body_bytes": 65536,
  "key_ref": "audit-key-reference",
  "key_file": "audit.key",
  "sessions": [{
    "label": "owner", "handle": "verified-session-reference",
    "status": "verified", "expires_at": "2026-10-30T12:00:00Z",
    "header_files": {"Authorization": "owner-session.secret"}
  }],
  "resources": [{
    "ref": "document:example", "url": "http://approved-target.example:8080/document",
    "approval_ref": "approved-read-22", "read_only": true, "accounts": ["owner"]
  }]
}
```

`audit.key`는 32바이트 임의 바이너리 HMAC 키, `owner-session.secret`는 해당 헤더의 원문 값이다. 파일/키를 레포·작업 설명·전달 JSON·모델/공유 로그에 넣지 않는다. GET이라도 상태를 바꿀 수 있으므로 `read_only=true`는 운영자가 확인해 등록한다. 선택적인 `resources[].view`는 기존 `transmit.JSONViewRule` 필드 형식이며 없으면 응답을 미분류하고 본문을 모델에 전달하지 않는다.

## 등록과 실행

1. 종료 상태가 아닌 작업을 일시정지하고 Worker·Planner·작업 주 대화가 모두 끝났는지 확인한다. done/failed/timeout 작업에는 직접 등록할 수 없다.
2. 기존 JWT 인증 아래 `POST /api/tasks/{id}/intents/{iid}/controlled-read`에 `{"snapshot_ref":"approved-read-22"}`만 보낸다. URL·헤더·키·예산·추가 필드는 받지 않는다. 현재 JWT는 운영자 인증이며 별도 다중 역할 RBAC는 아니다.
3. 서버가 통제 표시를 먼저 동기화한 뒤, 정확한 task/intent에 묶인 snapshot을 로드·검증하고 일회성 pending 등록을 설치한다. 실행 중인 의도에는 등록하지 않으며, 등록 의도를 CAS로 open 상태로 준비한다. 등록이 실패해도 이미 만든 통제 표시를 삭제해 일반 실행으로 되돌리지 않는다.
4. 기존 작업 재개 흐름을 사용한다. 이미 설치된 일시정지 경계는 재개 준비 과정에서 다시 취소하지 않아 방금 등록한 승인을 보존한다. `Engine.runIntent`에서 등록을 한 번 소비하고 새로운 RunRef를 발급한다. 모델 오류에 따른 재시도는 같은 전송기·요청 예산·journal을 사용한다. 현재 예산은 **등록 1회당** 적용된다. 같은 snapshot을 다시 등록하면 예산이 새로 시작하므로 운영자는 별도 승인으로 취급해야 한다. 승인 전체의 누적 예산 보장은 후속 구현이다.
5. 재시작·중지·재실행·소비 완료 뒤에는 새 운영자 등록이 필요하다. 같은 작업의 새 미등록 의도도 차단한다. 이 API에는 통제 표시를 제거하는 기능이 없다.

통제로 전환된 작업은 자동 Planner와 timeout의 마지막 Planner, 기존 작업 주 대화·자유 입력 Worker 이어하기를 사용하지 않는다. 등록된 조회를 수행하는 Worker session에는 `controlled_http_get` 하나만 제공한다. SDK 기본 도구·Bash·파일 도구·WebFetch/Search·skills/MCP·background tools·기존 transcript resume를 사용하지 않는다. 임의 tool 이름을 출력해도 실행할 handler가 없다.

승인된 실행과 Worker가 모두 끝나면 task를 영속 paused 상태로 옮겨 다음 승인을 기다리고 동시 실행 슬롯을 반환한다. 단일 조회 종료를 진단 완료(done)로 바꾸지 않는다. Pause·Stop·삭제/보관·서버 종료는 미사용 등록과 키 사본을 폐기한다. 개별 의도의 soft/hard 취소도 해당 미사용 승인을 폐기하며, 등록 상태 CAS와 직렬화해 삭제된 의도의 승인이 고아로 남지 않게 한다. 복원 후 같은 task/intent ID여도 새 등록이 필요하다.

도구 입력은 `account_ref`, `resource_ref`만 받으며 method는 서버가 GET으로 고정한다. 세션 handle/상태·헤더·실제 URL은 서버가 등록값에서 선택한다. 중복 키·추가 필드·임의 URL 입력을 거부한다. 모델에는 안전한 Receipt와 기존 비민감 JSON projection만 반환한다. 미분류·HTTP 오류·거부를 접근 성공/취약점 없음으로 변환하지 않는다. 유효한 조회가 없거나 거부가 발생한 실행은 정상 완료로 마무리하지 않는다.

서버의 초기 경로 상한은 응답 4MiB·요청 10,000건·요청 제한 시간 2분·세션 64개·자원 256개다. 세션당 헤더는 32개/이름 64바이트/값 8KiB 이하이며, 대소문자만 다른 중복 헤더와 HMAC 키 파일/키 값을 헤더로 사용하는 설정은 거부한다. 이 값은 기본값이 아닌 안전 상한이고, 실제 승인은 각 실행의 더 작은 한도를 지정한다. 모델에는 취약점/비취약 판정을 금지하고 실행 증거만 보고하도록 요구하며 E③ 평가는 대기 상태로 명시한다.

## 감사와 수명

생성 직후 header checkpoint를 저장하고, 각 `Journal.Record` 뒤 최신 checkpoint를 동기 저장해야만 intent 기록이 성공한다. checkpoint는 private state에 atomic rename + directory Sync로 갱신한다. 기록 또는 checkpoint 저장 실패는 후속 기록과 송신을 막는다. Worker 종료에서는 `tx.StopAndWait → journal.Close → 최종 checkpoint 확인`을 수행한다. 취소된 송신의 result 기록까지 기다린 뒤 journal을 닫으며, 실패는 intent 오류로 남긴다. pause/cancel 제어는 그 실행 오류와 별개로 상태 변경·삭제를 마친다.

작업 Pause·Stop·삭제·deadline의 기존 work context가 전송기로 전달된다. 이미 송신된 요청은 취소로 되돌리지 못한다. 같은 등록의 모델 재시도에는 예산이 유지되지만 재등록에는 새 예산이 적용된다. 기존 API와 private 입력만 연결한 단계이므로 실제 DB-backed 작업 제어는 별도의 환경 통합 시험이 필요하다.

**권한 분리 조건:** 이 코드가 OS 사용자/컨테이너 분리를 구성하는 것은 아니다. 승인·시크릿·state 디렉터리는 에이전트가 수정할 수 없게 배포해야 하고, 동일 OS 권한으로 동작하는 다른 일반 작업/프로세스까지 파일 삭제를 막는 보장은 없다. 일반 작업·전역 대화·기타 ARTEX 네트워크 경로의 전수 egress 통제도 이번 구현 범위가 아니다. 체크포인트가 공격자에게 수정 가능하면 rollback 탐지의 신뢰 조건이 깨진다.

fd 고정은 파일을 열어 검사한 inode에서 읽도록 보장한다. 부모를 열기 전 디렉터리 교체, 상위 조상 경로의 링크, 허용된 writer의 제자리 내용 변경과 여러 snapshot/키/헤더 파일의 원자적 일관성까지 보장하지 않는다. 마커 생성·존재 확인·체크포인트 쓰기의 별도 경로 경합도 이번 private reader 보강 범위 밖이다.

## 검증 범위

합성 target·실제 Worker·fake LLM으로 수신 1건, 임의 URL/셸/WebFetch/MCP/미등록 참조 0건, 재시도 예산 공유, checkpoint 실패 송신 0건, Pause 취소, 재시작과 새 의도의 일반 Worker 강등 방지를 시험한다. 실제 외부 모델·대상 진단·baseline·KG/판정 저장을 실행한 결과가 아니다.

DB 통합 시험은 등록 API → 작업 재개 → 실제 `Engine.runIntent` → Worker → GET/감사 → 의도 상태 저장을 다룬다. 잘못된 입력의 blocked 처리, soft/hard 취소 후 승인·키 폐기와 영속 paused/슬롯 반환도 시험한다. DB 미설정 환경에서는 명시적으로 건너뛰며, 그 경우 PostgreSQL 통합을 검증했다고 주장하지 않는다.

## 남은 경계

- DB에 통제 모드 정본이 아직 없다. `dataDir/e2-state`의 영속 저장소가 사라지고 DB 작업만 남으면 일반 실행으로 강등될 수 있으므로 외부 DB와 컨테이너를 함께 운영할 때는 동일한 영속 저장소를 보존해야 한다. DB mode fence 연결 전에는 재생성 상황의 fail-closed를 주장하지 않는다.
- `intent.state=done`은 등록된 조회 실행이 끝났다는 뜻이지 진단 완료·발견 없음이 아니다. 평가/커버리지에서 결과로 해석하지 않는다.
- 동일 snapshot 재등록의 누적 예산, 마커 원자성·삭제 CAS, 전역 잠금 아래 파일 I/O, 비 Linux 기동, 설정 예시 추가는 후속 검토 사항이다.
