# 첫 비평가 진단 기동

명시적으로 승인한 비민감 개발용 외부 추론은 [OpenRouter 첫 진단 계약](openrouter-first-diagnosis.md)을 따른다. 로컬 기본값·등록 GET/E③ 경로는 유지하며, 외부 모드는 별도 정책·제공사·개인정보·예산 입력과 실제 수용 검증이 필요하다.

이 문서는 `cmd/first-controlled-diagnosis`의 실제 공급·기동 절차다. 일반 HTTP와 등록 GET은 각각 별도 모드이며, 한 실행 설정에서 동시에 선택할 수 없다. 운영자·팀원에게 받은 실제 입력을 검증해 소비한다. 이 명령은 키·epoch·승인·계정·KG 세대·세션을 발급하거나 운영 자료를 합성하지 않는다. 신규 LocalBinding/실행 전 metadata 파일 형식은 팀 검토용 미채택 초안이다. 보수적인 타입·승인 pin·실제 수명 검사를 구현했지만 계약 채택이나 실제 운영 수용을 뜻하지 않는다.

## 첫 통합 진단의 고정 범위와 구현 기준

첫 통합 진단은 승인된 읽기 전용 일반 HTTP 범위에서 원본 Planner/Worker와
지원 Bash/E② 경로를 연결한다. 저장소의 통합 수용시험은 소유 loopback 시험 대상과
결정적 합성 provider를 사용하며 실제 모델·연습 대상 진단 성공과 구분한다. 확인 모드는 실제 요청마다 허용/거절하고,
자동 모드는 사전 승인 범위 안에서 실행하되 사용자 중지를 제공한다.
일반 관찰은 검토 필요 후보로 저장하며 등록 GET/IDOR의 확정 finding으로 승격하지 않는다.
등록 GET의 A/B·KG·snapshot 준비와 E③ 검증은 별도 경로로 유지한다.

기동 계약은 공식 CLI의 `prepare-task → prepare-intents → check → run`이다.
UI는 새 실행 명령이나 대상 URL을 제출하지 않는다. 첫 화면의 시작 동작은 운영자가
위 CLI로 준비·승인된 한 실행을 시작한 뒤 해당 task에 연결하는 방식이다.
이번 범위에 HTTP 시작 버튼은 없으며 UI 작업자가 새 시작 API를 설계하는 후속 과제도 아니다.
향후 HTTP 시작 기능은 별도 승인·설계 범위다. `serve`는 `run`의 선행 단계가 아니다.

**2026-11-08 확인 시점의 코드 스냅샷**은 `dev` `d2bfec57f34dae1c17b6026366e747f3a588750b`다.
이 SHA는 상시 최신 실행 버전을 지정하지 않는다. 새 실행 전 최신 `origin/dev`의 정확한 SHA를 확인·기록하고
해당 버전의 CLI·API와 이 문서의 기동 조건을 다시 대조한다.
[#317](https://github.com/ABC2LAB/ABCDEF2LAB/pull/317)의 첫 진단 통합과
[#323](https://github.com/ABC2LAB/ABCDEF2LAB/pull/323)의 OpenRouter 연결은 이 기준에 병합되어 있다.
[#325](https://github.com/ABC2LAB/ABCDEF2LAB/pull/325)의 평가 러너 승인 모드 검사도 포함하지만
비평가 CLI의 확인 모드와 평가 배치의 승인 조건은 서로 대체하지 않는다.

### 통합 이력 (현재 checkout 지침 아님)

아래 SHA는 병합 전 추적 기록이다. 당시 작업 시작 기준은 dev `2e096607bc387d8896c7623bcde5fa14cf04d596`와 아래 정확 head다.
당시 게시 전 dev `399da3ed4b71c0bc8b96ed7d6db487cc99d32ba1`를 다시 결합했다.
당시 dev 스냅샷에는 팀이 병합한 #304/#310/#312/#313/#318이 포함된다.
#311/#314/#315/#316/#317은 당시 통합 브랜치에서 함께 검증했다. #314는 별도 병합 없이 닫혔지만 해당 구현은 #317을 통해 dev에 포함됐다.

- [#317](https://github.com/ABC2LAB/ABCDEF2LAB/pull/317) `aa3ebf542fdf824cc9520dfda24895ed5f9520aa`: #304/#315/#316의 기동·후보·보고서·조치 연결 포함.
- [#313](https://github.com/ABC2LAB/ABCDEF2LAB/pull/313) `c2f13795664f3213f4a648ff576170b4a5101900`: 최신 dev를 통해 등록 GET 확정 상세보고서를 별도 결합.
- [#314](https://github.com/ABC2LAB/ABCDEF2LAB/pull/314) `707ba1a1d812de3bb0078538fa63f8037523016d`: #304/#311의 요청별 승인 포함.
- [#312](https://github.com/ABC2LAB/ABCDEF2LAB/pull/312) `726da9426f45333fec4d41987b9adf2c5265da7a`: E② Worker 지침 및 적용 내용 버전.
- [#318](https://github.com/ABC2LAB/ABCDEF2LAB/pull/318) `c81f6c26a21b71419fa1385f8a7093ffb2390621`: 게이트웨이 시험의 취소 동기화.

당시 UI 연결 검증 브랜치는 `feature/dongchan-first-report-routes-20261107`,
검증 커밋은 `64121b38eb4269c6530a5b10c078645b353d2dc4`였다.
현재 작업은 최신 `origin/dev`를 재확인한 뒤 시작하며 이 과거 브랜치로 되돌아가지 않는다.

### 구현과 수용 검증의 구분

개발용 실제 모델의 endpoint·정확한 profile/manifest는 별도 설치 입력이다.
이 통합의 결정적 합성 provider 시험을 실제 모델 연결·진단 능력 검증으로 세지 않는다.
실제 모델·보호된 실행 설정·대상 승인 회차가 확인되지 않으면 **UI 연결만 남았다고 판정하지 않는다**.
공식 GPU 비교나 대규모 모델 다운로드는 첫 개발 진단의 조건으로 추가하지 않는다.

## 공통 준비

Linux M01의 서버 하나·동시 실행 하나와 별도 설치가 끝난 PostgreSQL 16을 사용한다. 첫 실행 프로세스는 비 root 전용 UID로 실행한다. 보호 디렉터리는 그 UID 소유 `0700`, 비밀/승인 입력은 일반 파일 `0600`이어야 한다. symlink·파일 교체·부모 namespace 변경·공유 쓰기 경로는 거부한다. JWT 키는 DataDir 밖의 KeyDir에 설치자가 이미 준비해야 하며, DB에는 기존 로그인 비밀번호 해시와 실제 모델 프로필이 있어야 한다. 런타임은 schema migration·기본 데이터 seed·인증 키 자동 생성·예약·자동 복원·다른 task 실행을 하지 않는다.

모델 manifest는 `llmpolicy`의 기존 형식이며 실제 DB 프로필의 ID·format·base URL·model과 정확히 맞아야 한다. 첫 모드는 실제 task에 단일 ready 프로필 chain을 고정한다. goals/planner/worker 역할의 별도 프로필은 비어 있거나 그 task/launch의 정확한 같은 ID여야 한다. 역할별 우선순위로 다른 모델을 선택하거나 프로필 없는 global fallback을 사용하면 기동·준비·실행을 거부한다. Source.ModelTag는 승인된 metadata 값을 유지하며 새 태그를 만들지 않는다. 모델 서비스와 대상 origin은 설정으로 받는다. 아래 주소와 모델명은 비민감 자리표시자이며 실제 승인 endpoint를 대신하지 않는다.

```json
{
  "format": "abc2lab.first-diagnosis-launch/v1",
  "mode": "general_http",
  "listen_addr": "127.0.0.1:8080",
  "data_dir": "/srv/first-diagnosis/data",
  "key_dir": "/srv/first-diagnosis/keys",
  "skill_dir": "/srv/first-diagnosis/skills",
  "local_llm_manifest_path": "/srv/first-diagnosis/model-policy.json",
  "model_profile_id": 1,
  "model_format": "openai",
  "model_base_url": "http://127.0.0.1:11434/v1",
  "model_ref": "OPERATOR_APPROVED_MODEL",
  "runtime_dsn_file": "/srv/first-diagnosis/runtime.dsn",
  "shutdown_timeout_seconds": 60,
  "general_http": {
    "policy_file": "/srv/first-diagnosis/http/policy.json",
    "authority_file": "/srv/first-diagnosis/http/authority.json",
    "authority_root": "/srv/first-diagnosis/http"
  }
}
```

`runtime.dsn`과 등록 GET의 `authority.dsn`은 보호 파일에서 읽는다. DSN 값·세션 헤더·키·원본 감사 파일은 stdout·PR·공유 근거에 넣지 않는다. runtime pool은 최대 연결 8개를 사용하며, 등록 GET의 별도 호스트 권위 pool은 실제 epoch/key 잠금과 checkpoint 트랜잭션이 동시에 필요하므로 최소 2개 연결이 필요하다.

## 실제 task와 intent 준비

첫 단계는 LLM이나 대상을 호출하지 않고 실제 task/exploration 행을 paused 상태로 원자 생성한다. `task.json`은 보호 파일이며 다음 형태다.

```json
{"description":"Perform one approved read-only diagnosis","goal":"Verify the approved case using registered evidence","timeout_seconds":900}
```

```sh
go build -o /srv/first-diagnosis/bin/first-controlled-diagnosis ./cmd/first-controlled-diagnosis
/srv/first-diagnosis/bin/first-controlled-diagnosis prepare-task   -config /srv/first-diagnosis/launch.json -task /srv/first-diagnosis/task.json
/srv/first-diagnosis/bin/first-controlled-diagnosis prepare-intents   -config /srv/first-diagnosis/launch.json -task-id ACTUAL_TASK_ID
```

`prepare-task` 출력은 실제 `task_id`·`exploration_id`뿐이다. `prepare-intents`는 명시적인 실제 모델 호출이다. 대상 도구를 차단한 원본 Planner가 실제 Store에 intent를 만들며 실행과 대상 요청을 시작하지 않는다. 임의 양수 intent나 미리 만든 seed intent를 실제 Planner 결과로 대체하지 않는다. 명령과 합성 시험 근거만으로 실제 모델 호출을 실행했다고 기록하지 않는다. 성공 출력의 `intent_ids`는 실제 Store에 저장된 open intent의 비민감 ID다. 등록 GET 자료를 만드는 담당자는 이 실제 ID와 기존 결과를 확인해 snapshot·승인 입력에 참조한다.

## 일반 HTTP

`policy.json`은 `server.E2HTTPFilePolicy`의 폐쇄 형식 `abc2lab.e2-http-host-policy/v1`이다. 실제 approval/run/reset/case/policy/budget 참조, 승인 기간·한도, 계정/세션, CA 인증서·개인키·감사키·credential 보호 파일과 각 SHA256, 실제 격리 helper·state 경로를 공급한다. 실제 task/intent는 정책 파일이 임의로 정하지 않고 기동/Planner/Store 경로에서 확인한다.

`authority.json`은 `abc2lab.first-http-authority/v1`이며 `approval_ref`, 실제 `operator_uid`, 실제 `task_id`·`exploration_id`, 정책 파일 원문의 `policy_sha256`, `valid_from`·`valid_until`을 포함한다. 운영자가 실제 행과 보호된 정책 바이트를 확인하고 승인한다. 승인 구간은 최대 15분이며 정책 유효 구간을 포함해야 한다. 같은 전용 authority root에서 단회 시작 marker와 실제 FD·배타 잠금을 유지한다. 이 M01 lease는 해당 보호 namespace와 단일 writer의 근거이며, 같은 UID의 모든 외부 프로세스에 대한 독립 인증이나 전체 호스트 격리 수용의 증거가 아니다. 종료·실패 뒤 기존 승인으로 재기동하지 않는다. 새 회차는 새 승인과 새 전용 경로가 필요하다.

일반 HTTP에는 등록 GET snapshot/KG/Receipt/E③ 권위를 필수로 넣지 않는다. 허용·거부·대상 응답·도구 오류·전송 불명을 구분하고, 통제 구성이 없으면 Planner/Worker가 무통제 Bash로 떨어지지 않는다. 격리 helper와 실제 namespace 검증이 필요하며 proxy 환경변수만으로 우회 차단을 주장하지 않는다.

### 일반 HTTP 요청별 사용자 확인

`policy.json`에는 `approval_mode`를 반드시 명시한다. `confirm_every_request`는 GET을 포함한 지원 요청을 전송 전에 각각 보류한다. `preauthorized_scope`는 이미 운영자가 승인한 정책 범위에서 자동 실행한다. 누락·알 수 없는 값, 확인 모드의 처리기 부재는 오류이며 자동 모드로 바뀌지 않는다. 이는 상태 변경 위험 자동 분류가 아니다.

확인 모드는 현재 기존 단일 관리자 신원 `ARTEX`만 지원하므로 정책 `owner_ref`도 `ARTEX`여야 한다. 첫 기동이 기존 DB와 요청별 승인 서비스를 연결하고, factory가 실제 task/run을 결속한다. 다른 JWT subject나 task/run에는 조회·결정 권한을 주지 않는다. 다중 사용자 역할 체계를 새로 구현한 것은 아니다.

다음은 **기존 전체 정책 안에 넣는 필드 예시**이며 실행 가능한 전체 설정이나 실제 승인 자료가 아니다.

```json
{
  "approval_mode": "confirm_every_request",
  "owner_ref": "ARTEX",
  "rules": [{
    "approval_display": {
      "public_path": true,
      "query_keys": ["page"],
      "json_fields": ["action", "quantity"]
    }
  }]
}
```

각 rule의 `approval_display`는 운영자가 공개해도 되는 입력을 검토하여 지정한다. `public_path`가 false면 승인된 path prefix 밖의 경로를 숨긴다. query와 JSON은 지정한 키만 보여주며 JSON은 단순 값만 지원한다. password/token/session 등 민감 키와 중첩 객체·배열은 공개하지 않는다. 헤더와 body 원문은 DB·UI에 저장하지 않는다. 공개 키로 지정된 값 자체가 비밀이 아닌지 운영자가 확인해야 하며 이 설정은 자동 민감정보 판별기가 아니다. 표시되지 않은 입력과 예상 영향이 있다는 안내를 유지한다. 판단 정보가 충분하지 않으면 허용하지 않는다.
`input_preview` DTO는 위 서버 allowlist를 거친 비민감 공개 필드만 포함한다. UI는 이 값을 그대로 텍스트로 표시하므로 마스킹 책임은 서버에 있다. 운영자는 allowlist 값의 비민감성을 먼저 검토해야 하며 민감 키·중첩 값·비공개 경로와 원문 요청을 DTO에 추가하지 않는다.

설치자는 기존 `db.Open` migration으로 `request_approvals` 테이블을 설치하고 해당 실행 역할에 SELECT/INSERT/UPDATE만 부여한다. 일반 HTTP 기동 검사는 세 권한을 각각 확인하며 누락을 첫 승인 요청 때까지 미루지 않는다. 등록 GET과 보고서 전용 역할에는 이 권한을 공통 필수 조건으로 추가하지 않는다. 첫 실행 runtime은 DDL이나 grant를 수행하지 않는다. 승인 생성·결정·소비 저장 실패는 전송 허용이 아니다. 이 운영 오류는 정상 사용자 거절과 달리 `AuditFailed`와 runner의 기존 `transient_failure`로 분류하며, 감사 실패 latch로 후속 전송과 정상 마감을 차단한다. 호출 문맥 취소로 Insert/소비 CAS가 실패하면 해당 취소를 보존하고 실행 전체의 감사 실패로 바꾸지 않는다. 취소와 경합한 별도 저장 장애는 계속 감사 실패다. 잠금을 점유하는 저장소 작업은 2초 제한을 적용한다. 정상 거절·만료는 미전송 정책 제한을 유지한다. private 요청 원문은 실행 메모리에만 보류하고 DB에는 요청 지문·회차/task/intent/call/attempt·계정/세션 참조·공개 projection·결정/소비/결과만 남긴다.

기존 승인 화면의 요청별 HTTP 카드에서 실제 task를 선택해 보류 요청을 확인한다. API는 다음과 같고 기존 JWT middleware 안에 등록된다.

- GET `/api/tasks/{id}/request-approvals`, GET `/api/tasks/{id}/request-approvals/{approvalID}`
- POST `/api/tasks/{id}/request-approvals/{approvalID}/decision`: `{ "decision": "allow", "request_sha256": "상세에서 읽은 지문" }` 또는 `deny`
- POST `/api/tasks/{id}/request-approvals/stop`: 현재 결속된 E② 전송 경계 중지. 실행 재개나 전체 변경 API를 열지 않는다.

승인 API는 Authorization Bearer JWT의 HS256·현재 비밀번호 결속·만료·subject를 다시 검사한다. 쿠키/query token만으로 결정할 수 없어 ambient cookie에 의한 CSRF를 허용하지 않는다. 목록과 상세도 task binding을 요구하며 다른 task의 ID를 바꿔 접근할 수 없다. 서버가 이미 가진 task의 종료 상태와 live waiter/기한/지문을 확인하고 CAS로 단회 결정·소비한다. 중복·늦은 결정은 추가 송신을 만들지 않는다.

`allowed`는 송신 성공이 아니다. 소비 이후에도 정책·예산·감사·TLS와 현재 실행 상태를 재검사한다. 결과는 `not_sent`, `awaiting_dispatch`, `dispatch_pending`, `completed`, `unknown`으로 분리한다. 서버가 재시작되면 과거 DB allowed/pending만으로 waiter를 복원하지 않으며, live 실행을 잃은 소비 기록의 미확정 결과는 unknown으로 조회한다. 네트워크 전체의 exactly-once를 보장하지 않는다. 현재 프로세스에 task 권한 결속이 없으면 재시작 후 과거 승인 기록의 결정 권한을 복원하지 않는다. 실행 등록 시 승인행과 독립적으로 실제 task/run을 결속하므로 자동 모드와 첫 Worker 획득 전에도 같은 중지 API를 사용한다. 중지는 신규 송신을 차단하고 감독자의 취소·drain·마감을 요청한다. 이미 전송된 요청이나 대상 변경의 롤백을 뜻하지 않는다. 중복 중지는 종료 상태를 다시 실행 중으로 바꾸지 않는다.

이 연결은 합성 PostgreSQL·인증 API·통제된 수신기로 검증한다. 실제 모델/연습 대상 회차·운영 공급·Bash 전체 배치·공식 성능 측정 완료를 뜻하지 않는다. UI 클릭 실기동 검증과 단위 렌더링 시험도 별도로 구분한다.

## 등록 GET

공통 필드와 `mode`를 `registered_get`으로 바꾸고 `general_http` 대신 다음 객체를 둔다.

```json
{
  "registered_get": {
    "authority_dsn_file": "/srv/first-diagnosis/authority.dsn",
    "host_binding_file": "/srv/first-diagnosis/root-binding.json",
    "metadata_file": "/srv/first-diagnosis/case-inputs.json",
    "snapshot_dir": "/srv/first-diagnosis/snapshots",
    "state_dir": "/srv/first-diagnosis/state",
    "registry_file": "kg-registry.json"
  }
}
```

`root-binding.json`은 `server.ControlledReadLocalBinding`의 version 1 정규 JSON 원문이다. 설치자가 그 원문의 SHA256을 **기존** `audit_checkpoint_epochs.anchor_digest`에 미리 결속해야 한다. 실제 store/epoch/generation/publisher, 최대 15분 기한, state/journals/descriptors/keys 디렉터리의 실제 device/inode, 기존 active key의 namespace/참조/secret version/identity/revision/파일 SHA256, 실제 task/intent/case/reset/policy를 포함한다. `files`에는 실제 snapshot·registry·metadata·모델 manifest·JWT 키의 경로와 원문 SHA256을 핀한다. 설정 문자열이나 파일 소유 검사만으로 RootTrust/epoch/verified 주체를 새로 만들지 않는다.

`case-inputs.json`은 `abc2lab.first-controlled-read-inputs/v1`의 `format`, `snapshot_ref`, `target_origin`, `source` 객체다. `source`는 기존 `e3record.Source`의 실행 전 projection이다. `Expected`에는 실제 task/intent, owner/cross의 인증된 identity, resource/read approval, KG policy version을 제공한다. 실제 KG ref/digest/generation/instance/reset, model tag, severity, policy, canary field 이름, 현재 exploration/mode version을 포함한다. 신규 paused task에 아직 mode 행이 없으면 실행 전 ModeVersion은 실제 부재를 뜻하는 0이며, 최종 Source에는 등록이 발급한 실제 controlled version만 사용한다. `RunRef`는 비어 있어야 하며 실제 런타임만 채운다. 완료·finalized·revision·evidence digest·traffic ID·final checkpoint 사실을 미리 넣으면 거부한다. 세션 헤더와 canary 값은 여기에 넣지 않는다.

등록 시 snapshot·registry의 실제 읽은 바이트를 다시 승인 pin과 대조하고 KG pairing을 확인한다. 실행에는 그때 동결된 **같은** snapshot 객체를 사용한다. Source는 captured snapshot과 현재 task/intent/exploration/mode를 재확인하며 다른 generation이나 일반 HTTP 결과를 E③ 증거로 바꾸지 않는다. 소유/교차 두 GET, 감사 마감, E③ revision/finding SQL 저장, 기존 조회까지가 이 모드의 흐름이다. 비평가 `EvalRunID`는 비어 있으며 SQL에는 NULL로 저장한다.

## PostgreSQL 역할 분리

설치자 역할은 schema·키 registry·epoch·모델/로그인 자료를 미리 설치하고 런타임 DSN으로 재사용하지 않는다. 프로그램이 운영 role을 만들거나 grant하지 않는다. 실제 DB 카탈로그에서 superuser·CREATEDB·CREATEROLE·replication·BYPASSRLS와 이를 얻을 수 있는 직·간접 role membership(상속 및 SET ROLE 경로), 광범위한 pg_read_all_data/write_all_data·서버 파일/프로그램·monitor 권한, session_replication_role 변경 권한, DB CREATE/TEMP, schema/object 소유권·CREATE, 실행 가능한 SECURITY DEFINER, 임시 schema와 모호한 search path를 거부한다. 설치 schema의 일반 테이블과 실제 필요한 권한을 확인한다.

실행 역할은 task/exploration·원본 Store·판정 저장에 필요한 읽기/삽입/갱신 권한과 해당 INSERT sequence USAGE를 받는다. `task_categories`, `task_relations`, `task_scope` 조회도 실제 task 복원 조회에 필요하다. 원본 목표 분해의 `set_constraints`는 task_constraints SELECT/INSERT, `add_task_scope`는 task_scope SELECT/INSERT를 사용한다. 두 테이블에 UPDATE/DELETE는 요구하지 않는다. 원본 Planner의 scoped 자산 조회는 assets/companies/company_scope/task_asset_links SELECT와 기존 try_inet(text) EXECUTE, 실제 asset 참조 intent는 asset_intercept_rules/task_intercept_rules SELECT와 exploration_anchors SELECT/INSERT를 필요로 한다. INSERT 대상의 serial/identity sequence USAGE도 확인한다. asset/company 내용의 일반 갱신 권한은 추가하지 않는다. 보고서 동시성 잠금에 필요한 한 열의 권한은 아래 저장 보고서 절에 구분한다. 등록 모드는 E③/revision 저장 권한과 checkpoint/epoch SELECT를 추가로 받는다. checkpoint SQL 쓰기는 별도 호스트 권위 역할에만 필요하다. E③의 기존 immutable manifest/issuance 행 잠금에는 UPDATE(id) 열 권한이 필요하며, 기존 immutable trigger가 갱신 자체를 거부하는지도 확인한다. task_archives SELECT는 mode 등록 검사에 필요하다. **epoch/key registry에는 열 단위 권한을 포함한 쓰기 권한이 없어야 한다.** 최소 목록의 실행 검사는 `CheckFirstDiagnosisHTTPRuntimeDatabase`/`CheckFirstDiagnosisRuntimeDatabase`가 담당한다.

등록 GET의 호스트 권위는 별도 로그인 역할이다. 기존 epoch·active key·history 행의 `FOR SHARE`를 위해 PostgreSQL이 요구하는 UPDATE를 아래 키 열에만 허용한다. 이는 runtime의 권한 확대가 아니다. 실제 제품 어댑터는 이 세 테이블에 UPDATE/INSERT/DELETE를 실행하지 않으며 lease가 끝날 때까지 실제 행 잠금을 유지한다.

```sql
-- 아래 이름은 설치자가 실제 전용 role/schema에 맞춰 적용할 자리표시자다.
GRANT SELECT ON audit_checkpoint_epochs,
  audit_key_registry_keys, audit_key_registry_history TO host_authority_role;
GRANT UPDATE(store_ref) ON audit_checkpoint_epochs TO host_authority_role;
GRANT UPDATE(key_ref) ON audit_key_registry_keys TO host_authority_role;
GRANT UPDATE(key_ref) ON audit_key_registry_history TO host_authority_role;
GRANT SELECT, INSERT ON audit_checkpoint_history,
  audit_checkpoint_operations TO host_authority_role;
GRANT SELECT, INSERT, UPDATE ON audit_checkpoint_heads TO host_authority_role;
```

권위 연결도 설치자/소유자·전체 UPDATE·추가 열 UPDATE·키 명령 receipt 쓰기를 거부한다. 두 pool의 실제 DB/서버/schema identity를 대조한다. checkpoint commit 뒤 ACK를 검증하고, 같은 DB의 runtime 조회가 최종 sealed head를 확인한다. 권한 오류를 피하려고 runtime에 epoch UPDATE를 주거나 관리자 DSN으로 조용히 바꾸지 않는다.

## 기동·실행·마감

`serve`와 `run`은 둘 중 하나를 선택하는 대안이다. 아래 명령을 같은 승인/root로 순차 실행하지 않는다. 실제 회차는 `run`이 같은 엄격 조립과 조회 API를 연 뒤 실행·마감까지 수행한다. `serve`는 기동·조회만 제공하며, 종료 뒤 같은 단회 승인으로 `run`하면 재사용을 거부한다. 그 경우 별도 새 승인과 새 전용 authority root가 필요하다. 사전 점검에는 marker를 소비하지 않는 `check`를 사용한다.

`check -config /srv/first-diagnosis/launch.json -database`는 보호 경로·실제 프로필·역할을 읽기 검사한다. 시작 marker나 권위를 소비하지 않으며 `providers:NOT_ACQUIRED`, `diagnosis:NOT_RUN`을 명시한다. 기존 flat 준비 설정의 check도 유지한다.

```sh
# 공급 검증·초기화를 마친 뒤 loopback API만 연다.
/srv/first-diagnosis/bin/first-controlled-diagnosis serve   -config /srv/first-diagnosis/launch.json
# 같은 조립을 통과해 승인된 실제 task의 한 진단을 시작하고 결과·마감을 기다린다.
/srv/first-diagnosis/bin/first-controlled-diagnosis run   -config /srv/first-diagnosis/launch.json
```

`service_ready`는 서버 준비이며 call/execution 준비나 판정 완료가 아니다. 일반 HTTP에서 호출·감사·종료가 확인돼도 기존 goal은 open, task는 paused로 남을 수 있다. 이는 제한된 실행의 마감이며 목표 충족·task done·E③ 판정 또는 공식 평가 완료가 아니다. 일반 모드 출력은 execution/closure와 `e3:NOT_APPLICABLE`을 사용한다. `run`의 최종 PASS는 실제 Run과 마감이 모두 확인된 경우에만 출력한다. 신규 admission 차단 → task/call/대상 drain → 소유 자식·터널·소켓 종료 확인 → 감사/저장 확인 → host/키/FD/SQL lease 반환 순서를 지킨다. drain·자식 종료가 불명하면 자원을 반환해 안전 종료로 꾸미지 않는다. 실제 drain이 확인된 뒤 남은 판정/저장 오류는 오류로 영구 보존하면서 안전하게 자원을 반환할 수 있다. timeout·감사 실패·저장 불명·취소를 성공 완료로 바꾸지 않는다.

감사가 완결된 정상 정책 차단·미지원 경로는 실행 마감 실패와 구분한다. 일반 HTTP 감독자는 Planner의 후속 계획 세대를 기다리고, 최종 call·프로세스·프록시 drain 및 감사 결과를 대조한다. 정책/미지원 제한 intent는 기존 `blocked` 상태와 프로그램이 기록한 `e2_http_policy_limited` / `e2_http_unsupported` 사유로 남으며, 정상 마감이 이를 진단 성공이나 취약점 없음으로 바꾸지 않는다. 일부 요청만 허용된 호출도 실제 송신 건수와 제한 분류를 보존한다. 결과 불명·감사 실패·종료 미완료·근거 없는 blocked는 계속 실패다. 사전 정책 제한 시험과 요청별 사용자 거절 시험은 구분한다. 통합 수용시험은 실제 생성자·인증 Handler·격리 Bash를 거쳐 허용/거절 이후 같은 관찰의 후보와 저장 보고서까지 별도로 확인한다.

M01의 같은 운영자 UID 배치에는 평가 UDS 인증 전제를 확대하지 않는다. 이 CLI의 첫 비평가 실행은 평가 제어 채널을 자동 활성화하지 않으며 알 수 없는 control/eval 설정을 폐쇄 파서에서 거부한다. 같은 UID의 embedded 제어 채널을 독립 runner 인증으로 조합하지 않으며, 독립 runner UID/mount 인증을 갖춘 별도 배치의 typed 제어 어댑터가 필요하다. manifest 마감이나 공식 판정 export 전체를 완료했다고 주장하지 않는다.

## 저장 보고서와 수정 프롬프트

두 실행 모드의 기존 인증·수명 경계를 통과한 서버에서 다음 경로만 추가로 연다.
신뢰한 기동 과정에서 고정한 task ID를 query에 정확히 한 번 지정해야 하며 다른 task나 미결속 서버는 거부한다.

- `GET /api/report/diagnostic/remediation?task=ID`: 후보·finding·판정과 저장 조치 목록.
- `POST /api/report/diagnostic/remediation?task=ID&audience=internal`: 현재 원본 digest를 대조한 조치·AI 수정 프롬프트 생성과 새 저장본 추가.
- `GET /api/report/diagnostic/remediation/prompt?task=ID&audience=internal&entry=ENTRY&record=RECORD&digest=DIGEST`: 요청한 현재 유효 저장본의 프롬프트만 복사.
- `GET /api/report/diagnostic?task=ID&download=1`: 같은 저장본을 사용하는 종합 HTML 내보내기.

생성 POST는 명시적인 Bearer 인증과 application/json만 받으며 쿠키·query token만으로 저장할 수 없다.
기본 공개 범위는 기존 공유 마스킹이며 담당자 상세는 `audience=internal`을 명시한다.
이 API들은 모델·대상 요청, 로그인 재현, 실행 admission을 시작하지 않는다.
수정 프롬프트는 고객의 개발용 AI가 실제 고객 저장소에서 코드 경로와 기술을 확인하고
수정·테스트·리뷰하도록 지시하는 자료다. 언어별 예시 코드 목록이나 자동 패치 실행기가 아니다.
고객 프롬프트를 생성·복사해도 고객 코드가 실행되거나 수정되지는 않는다.
준비 서버·만료/닫힌 runtime, 채팅·재진단·수정 실행 API의 차단은 그대로다.
실행 중 기존 웹 화면은 같은 공통 API를 사용한다. 실행 종료 후 아래 report 명령도 같은 서버 보고서 handler·생성기·저장 형식을 재사용한다.

일반 HTTP runtime에도 저장 보고서가 조회하는 `e3_evidence_manifests`,
`e3_oracle_verdicts`, `e3_verdict_issuances`, `e3_verdict_links`의 SELECT가 필요하다.
조치 저장 시 원본 verdict의 동시 변경을 막는 행 잠금에는
`e3_verdict_issuances`의 UPDATE(id) 열 권한만 추가한다.
기존 `exploration_nodes` INSERT와 sequence USAGE를 재사용하며 조치 저장 때문에
판정 INSERT, 다른 E③ 열 UPDATE, epoch/key 권위 쓰기 또는 관리자 권한을 추가하지 않는다.
원본 증거 잠금의 보관 상태 확인을 위해 task_archives SELECT도 필요하다.
자동 연결한 기술·컴포넌트가 저장 도중 바뀌지 않도록 assets(id), task_asset_links(task_id),
exploration_anchors(node_id)에도 각 한 열의 UPDATE 권한을 부여해 FOR SHARE 잠금을 허용한다.
이는 PostgreSQL의 실제 열 UPDATE 권한이며 별도 읽기 전용 권한이라고 주장하지 않는다.
제품 조치 생성 경로는 원본·자산·연결 행을 수정하지 않으며 다른 열과 전체 UPDATE를 기동 검사에서 거부한다.
기동 검사는 이 SELECT·잠금 권한과 E③의 기존 immutable trigger를 확인한다.
설치자가 실제 schema/role에 아래 최소 추가 권한을 공급하며 제품이 자동 grant하지 않는다.

```sql
GRANT SELECT ON task_archives, e3_evidence_manifests, e3_oracle_verdicts,
  e3_verdict_issuances, e3_verdict_links TO runtime_role;
GRANT UPDATE(id) ON e3_verdict_issuances, assets TO runtime_role;
GRANT UPDATE(task_id) ON task_asset_links TO runtime_role;
GRANT UPDATE(node_id) ON exploration_anchors TO runtime_role;
```

### 실행 종료 후 저장 자료만 열기

진단 `run`은 마감 뒤 자신의 HTTP 서버와 DB를 닫는다. 만료된 진단 lease를 늘리거나 같은 단회 승인을 다시 사용하지 않고, **같은 공식 CLI의 별도 `report` 명령**으로 이미 저장된 task만 연다.

```sh
/srv/first-diagnosis/bin/first-controlled-diagnosis report \
  -config /srv/first-diagnosis/report-launch.json -task-id ACTUAL_TASK_ID
```

`report-launch.json`은 위 launch 설정의 폐쇄 형식을 유지하되, `runtime_dsn_file`을 설치자가 공급한 보고서 전용 최소 역할의 보호 DSN 파일로 지정한다. 기존 KeyDir의 JWT 키와 DB 로그인 해시를 재사용하며 키·계정·기본 데이터를 만들지 않는다. 설정의 모델/승인 필드는 형식만 파싱한다. 모델 manifest·정책·승인·등록 metadata/snapshot 파일은 열지 않고, 모델 프로필·host 권위·Supervisor·진단 lease·실행 복구를 만들지 않는다. 보고서 전용 DSN은 실행 역할이나 관리자 DSN으로 자동 대체되지 않는다.

출력의 `mode:saved_report`, `execution:DISABLED`는 저장 자료 서비스만 준비됐다는 뜻이다. loopback으로만 열고 종료 시 HTTP 요청을 drain한 뒤 DB를 닫는다. 종료 drain 실패는 성공으로 바꾸지 않는다.

이 후속 모드는 **인증 API와 보고서 API·HTML export 전용**이다. `/`의 기존 웹 UI, 로그인 화면, 브라우저 복사 버튼은 제공하지 않는다. 허용한 인증 경로는 `GET /api/auth/status`, 기존 계정의 `POST /api/auth/login`이며, 발급된 Bearer로 위 API를 사용한다. 별도 프런트엔드 화면의 연결이나 완료 후 브라우저 버튼 조작을 검증했다고 주장하지 않는다. 건강 확인 외 일반 task·채팅·업데이트·비밀번호 변경·실행 API는 열지 않는다.

고정한 하나의 존재하는 task만 조회·저장할 수 있다. `done` task의 저장본뿐 아니라 paused/open 등 미완료 task의 **현재 저장된 중간 자료**도 제공한다. report 시작이나 프롬프트 생성은 완료 판정이 아니며 task 상태·실행 이력·원본 finding/판정/자산을 수정하지 않는다. 조치 생성 시에만 기존 exploration_nodes에 새 보고서 저장본을 추가한다. 원본 digest가 달라지면 기존 저장본은 stale로 표시되고 복사는 거부된다.

보고서 전용 역할의 최소 권한은 다음과 같다. 설치자가 실제 전용 schema의 USAGE와 아래 권한을 공급하며, 제품은 DDL·migration·seed·role 생성·GRANT를 실행하지 않는다. 일반 runtime 권한 전체를 부여할 필요가 없다. 카탈로그의 관리자/소유자/권위 쓰기/넓은 잠금 열 권한 차단과 E③ immutable 검사는 유지된다.

```sql
GRANT SELECT ON tasks, task_archives, findings, exploration_nodes, settings,
  assets, task_asset_links, exploration_anchors, e3_evidence_manifests,
  e3_oracle_verdicts, e3_verdict_issuances, e3_verdict_links TO report_role;
GRANT INSERT ON exploration_nodes TO report_role;
GRANT USAGE ON SEQUENCE exploration_nodes_id_seq TO report_role;
GRANT UPDATE(id) ON tasks, findings, exploration_nodes, assets,
  e3_verdict_issuances TO report_role;
GRANT UPDATE(task_id) ON task_asset_links TO report_role;
GRANT UPDATE(node_id) ON exploration_anchors TO report_role;
```

위 UPDATE는 PostgreSQL 행 잠금에 필요한 실제 열 권한이다. 데이터베이스 자체의 임의 SQL 실행 권한을 읽기 전용이라고 부르지 않는다. 제품의 노출된 report API는 원본 갱신 SQL을 실행하지 않으며 판정 추가, 모델 프로필 읽기, task 생성, 계정 변경 권한을 요구하지 않는다.

저장 보고서의 기동(#304)·공통 보고서/조치(#316)·요청별 승인(#314) 연결은 #317을 통해 현재 dev에 포함되어 있다. 위 과거 SHA는 구현 추적용이며 현재 실행 기준을 대신하지 않는다.

## 실제 자료 요청과 검증 범위

실제 자료는 모드별로 구분한다.
- 공통: 실제 대상·실행 범위와 승인, 모델/profile·local endpoint, 기존 인증 자료와 분리된 DB 역할, 실제 task/intent 및 해당 회차의 target instance/reset 근거.
- 일반 HTTP: 위 `policy.json` / `authority.json`에 필요한 승인·계정/세션·요청 예산, 격리 helper와 네트워크 경계, 보호된 CA/감사키/credential 자료. 등록 GET 전용 A/B carrier·KG registry·snapshot·LocalBinding은 공통 선행 조건이 아니다.
- 등록 GET/IDOR: 승인된 A/B 세션/carrier, KG registry와 같은 reset/generation의 snapshot, RootTrust/LocalBinding·epoch anchor·키 mapping, E③ Source 및 저장 권위 자료가 추가로 필요하다.

전체 VM·DB·키·marker를 함께 되돌리는 복원의 독립 antirollback 권위는 이 구현이 제공하지 않는다. 첫 M01 수용은 fresh-only 기동, 취소 후 이전 승인 재사용 거부, 동시 lease 거부에 한정된다. 실제 운영의 외부 root/backup 복원 경계 확인은 별도 설치자 자료와 수용이 필요하다.

설치자는 선택한 모드의 M01 namespace·전용 보호 경로, 기존 JWT/로그인 자료와 별도 PostgreSQL 역할을 공급한다. 등록 GET은 기존 키 mapping/epoch anchor와 제한된 권위 DSN도 공급한다. 이 자료의 제작이나 실제 대상/모델 회차 실행은 합성 시험 성공으로 대체하지 않는다.

owned loopback·synthetic provider·실제 PostgreSQL race 시험은 각각 검사한 경계만 증명한다. 실제 Juice Shop·실제 모델·같은 회차의 권한/세션/취소/저장 수용은 해당 자료로 별도 실행해야 한다. 제품 코드의 공급·기동·종료 구현이 남으면 그것을 팀원 자료 대기로 분류하지 않는다.

## UI 연결 계약: 실행·승인·저장 결과

### 주소와 인증

- 진단 API 주소는 launch의 `listen_addr`다. 예: `http://127.0.0.1:8787`.
- 종료 후 `report`는 report-launch의 주소를 사용한다. 같은 loopback 포트를 재사용하면 프런트의 목적지를 바꾸지 않아도 된다. 두 서버를 같은 포트에 동시에 띄우지 않는다.
- 기존 Next 개발 서버는 `AUTOPENTEST_API=http://127.0.0.1:8787`, `NEXT_PUBLIC_MOCK=0`으로 `/api/*`를 기존 rewrite에 연결한다. 이는 주소 예시이며 운영·외부 공개 설정이 아니다.
- `POST /api/auth/login`에 기존 설치 계정의 `username`/`password`를 JSON으로 전송하고 반환 `token`을 Bearer로 사용한다. 실제 비밀값은 문서·URL·PR·로그에 넣지 않는다. 종료 후 report도 같은 KeyDir와 DB 로그인 해시를 쓰므로 유효한 JWT를 재사용할 수 있다. 만료/비밀번호 변경 때는 재로그인한다.
- 승인 결정·중지·실행 결과 조회는 명시적 Bearer의 현재 비밀번호 결속과 `ARTEX` subject를 재검사한다. 쿠키나 query token만으로 대체하지 않는다. 모든 호출은 운영자가 연결한 실제 task ID를 사용한다.

### 각 화면 동작

| 화면 동작 | 실제 호출 | 표시 기준 |
|---|---|---|
| 준비된 실행 연결 | 운영자 공식 CLI `run`, UI는 `GET /api/tasks/{id}/request-approvals` | 승인행 0건이어도 `run`을 표시한다. 실행은 임의 POST로 새로 시작하지 않는다. |
| 실행 상태·승인 대기 | 같은 GET을 폴링 | `run.phase`, `run.outcome`, 승인행의 `can_decide`·기한·실행 상태를 그대로 사용한다. |
| 승인 상세 | `GET /api/tasks/{id}/request-approvals/{approvalID}` | 서버의 공개 projection과 숨김 안내를 표시한다. |
| 허용/거절 | `POST .../{approvalID}/decision` | `{"decision":"allow","request_sha256":"상세에서 받은 지문"}` 또는 deny. 도구 전체 허용으로 확대하지 않는다. |
| 중지 | `POST /api/tasks/{id}/request-approvals/stop`, body `{}` | `stopped:true`는 신규 송신 차단·마감 요청이다. `run.drain_complete`가 실제 마감 확인이며 대상 변경 롤백이 아니다. |
| 종료 결과 재조회 | `GET /api/tasks/{id}/diagnosis-run` | 진단과 report 서버의 같은 경로. 저장된 실제 종료 상태만 반환한다. |
| 결과·개별 보고서 | `GET /api/report/diagnostic/remediation?task=ID&audience=internal` | entry의 판정·검증·근거·보고서 필드를 사용한다. 단순 성공 응답으로 확정하지 않는다. |
| 조치·AI 프롬프트 생성 | 같은 경로 POST | `entry_id`와 방금 읽은 `expected_source_digest`, 필요한 명시 보완만 body에 보낸다. |
| 현재 프롬프트 복사 | `GET /api/report/diagnostic/remediation/prompt?task=ID&audience=internal&entry=ENTRY&record=RECORD&digest=DIGEST` | 서버가 돌려준 `ai_prompt`만 복사한다. stale 저장본을 클라이언트에서 복구하지 않는다. |
| 종합 보고서 다운로드 | `GET /api/report/diagnostic?task=ID&audience=internal&download=1` | 인증된 fetch의 HTML blob을 다운로드한다. Bearer를 링크/query에 넣지 않는다. 공유본은 audience 생략 또는 shared. |

라이브 목록의 형태는 `{"approvals":[...],"run":{...}}`다. 실행을 아직 획득하지 않았다면
`run.execution`은 없다. 없는 실행 정보를 프런트가 합성하지 않는다.
`run_ref`는 해당 task에 신뢰 기동이 결속한 회차이며 요청 body로 바꾸지 않는다.

`run` 상태는 다음과 같다.

- `phase:prepared`: 실제 task/run 결속, 아직 감독 실행 전.
- `phase:running`: 감독자 실행 중. 승인 대기는 해당 행의 `state:pending`과 `can_decide:true`로 표시한다.
- `phase:stopping`: 사용자 중지와 drain 진행 중.
- `phase:closed, outcome:completed, drain_complete:true`: 이번 실행·감사 마감 확인. 목표 달성·취약점 없음·E③ 확정을 뜻하지 않는다.
- `phase:closed, outcome:cancelled, drain_complete:true`: 사용자 중지 후 마감 확인.
- `phase:failed`: 실행 또는 마감 실패. `closure_failed`로 자원 마감 실패를 별도 표시한다. `context_cancelled`만 보고 성공·실패를 추정하지 않는다.
- `execution.state:active|blocked`는 전송 경계 상태이며 감독 실행의 phase와 다르다. counts의 `forward_attempted`·`responses`·`unknown`도 각각 따로 표시한다.

저장 종료 조회는 다음 구조다. 숫자·참조·시각은 실제 응답값을 사용한다.

```json
{
  "version": 1,
  "task_id": 42,
  "saved_at": "2026-11-08T00:00:00Z",
  "run": {
    "run_ref": "operator-run-reference",
    "phase": "closed",
    "outcome": "cancelled",
    "stop_requested": true,
    "context_cancelled": true,
    "drain_complete": true,
    "closure_failed": false
  }
}
```

CLI는 실제 마감 뒤 기존 exploration 저장소에 프로그램 소유 종료 기록을 한 번 저장하고
DB pool을 닫는다. 동일 결과 재저장은 같은 기록을 사용하며 다른 결과로 덮어쓰지 않는다.
저장 실패를 최종 PASS로 출력하지 않는다. report 조회는 과거 승인·lease·모델·Worker를 복원하지 않는다.
이 저장 상태 경로는 위 일반 HTTP 첫 사례 범위이며 등록 GET에 일반 HTTP 상태를 합성하지 않는다.
OpenRouter의 계획 단계 기록만 있으면 `run:null`, `execution:NOT_RUN`과
`model_preparation` 목록을 반환할 수 있다. 최종 종료 기록과 구분하며, 실행 `model` 및
계획 단계 집계의 실패·예약량·실제 보고 사용량 해석은 [OpenRouter 안내](openrouter-first-diagnosis.md)를 따른다.

### 오류·재시도와 갱신

- 라이브 갱신은 GET 폴링이다(예: 화면 표시 중 1초 간격). 첫 진단 라우터에 SSE를 새로 지원한다고 가정하지 않는다.
- 401: 인증을 갱신한다. 결정/중지 POST를 자동 재전송하지 말고 먼저 상태를 GET으로 확인한다.
- 403: 해당 task/run/경로의 권한 또는 실행 가능 상태가 아니다. 다른 task ID로 대체하지 않는다.
- 승인 결정 409: 중복·만료·취소·이미 소비된 결정 등이다. 최신 상세를 다시 읽는다. 늦은 허용으로 새 송신을 만들지 않는다.
- 저장 종료 조회 404: 유효한 종료 기록도 계획 단계 모델 기록도 없다. 서버 연결이 끊겼다는 이유만으로 완료를 표시하지 않는다.
- 조치/복사 409: 근거가 변경되어 stale이다. 목록을 새로 읽고 사용자가 명시적으로 재생성한다.
- 422: 범위·자료·출력 한도 등 요청을 점검한다. 503/연결 오류는 저장·마감 결과가 불명일 수 있다. 불명 대상 요청을 자동 재전송하지 않는다.
- `run` 종료로 진단 HTTP 서버가 닫히는 동안은 연결 종료를 표시한다. 운영자가 report 명령을 띄운 뒤 저장 종료·결과를 다시 조회한다. report 서버에 승인 결정/중지/재시작을 요청하지 않는다.

결과 entry의 `outcome`은 `confirmed`, `review`, `not_executed`, `unknown` 등을
서버 값 그대로 사용한다. 일반 HTTP의 후보는 `review`이며 보고서에 도달했다는 이유로
`confirmed`로 바꾸지 않는다. 공유 응답은 자유문장·프롬프트·원본 digest를 숨긴다.
내부 상세와 복사·HTML은 동일한 현재 유효 저장본을 사용하며 생성 과정에서 모델·대상 요청을 보내지 않는다.

TypeScript 연결은 `web/src/abc2lab-ui/lib/request-approvals.ts`의
`readRequestApprovalState`, `readDiagnosisRun`, 기존 결정/중지 함수와
`RequestApprovalRunState`/`DiagnosisRunRecord`를 사용한다.
보고서/조치는 `lib/remediation.ts`의 `readRemediation`, `generateRemediation`,
`readCopyPrompt` 및 기존 인증 blob 다운로드를 재사용한다.
타입에는 실제 서버의 `status`, `verification`, `run`, `intent_id`,
`evidence_digest`, `report_version`, `observed_at`을 포함한다.

### 통합 수용 근거와 남은 외부 입력

`TestFirstDiagnosisCombinedApprovalCandidateStoredReportOwnedPostgres`는 실제 생성자와
Supervisor, 원본 Worker/Planner, namespace Bash/E②, 전용 PostgreSQL의 분리 LOGIN,
실제 HTTP 클라이언트와 소유 loopback 수신기를 사용한다. 같은 Bash 안의 두 요청을
각각 허용/거절하여 실제 송신 1건, 관찰에 연결된 검토 후보 1건, 확정 finding 0건을 확인한다.
종료 뒤 최소권한 report 서버에서 저장·조회·복사·HTML 동일 본문, stale와 타 task 차단,
보고서 처리 중 추가 모델·대상 호출 0을 검사한다. 최종 finding 삽입이나 완성 응답 재생은 사용하지 않는다.

모델은 결정적 합성 provider이며 초기 open intent 1개는 시험 준비 자료다.
이 시험을 실제 모델의 계획·도구 선택 능력이나 실제 연습 대상 전체 진단의 완료로 해석하지 않는다.
실제 모델 profile/manifest, 설치자 인증·보호 실행 입력과 승인된 대상의 실제 회차가 남아 있다면
그것은 비UI 차단 사항이며 UI 담당자에게 환경 구성을 넘기지 않는다.
