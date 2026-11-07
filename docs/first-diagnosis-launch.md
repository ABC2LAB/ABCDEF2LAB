# 첫 비평가 진단 기동

이 문서는 `cmd/first-controlled-diagnosis`의 실제 공급·기동 절차다. 일반 HTTP와 등록 GET은 각각 별도 모드이며, 한 실행 설정에서 동시에 선택할 수 없다. 운영자·팀원에게 받은 실제 입력을 검증해 소비한다. 이 명령은 키·epoch·승인·계정·KG 세대·세션을 발급하거나 운영 자료를 합성하지 않는다. 신규 LocalBinding/실행 전 metadata 파일 형식은 팀 검토용 미채택 초안이다. 보수적인 타입·승인 pin·실제 수명 검사를 구현했지만 계약 채택이나 실제 운영 수용을 뜻하지 않는다.

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

`prepare-task` 출력은 실제 `task_id`·`exploration_id`뿐이다. `prepare-intents`는 명시적인 실제 모델 호출이다. 대상 도구를 차단한 원본 Planner가 실제 Store에 intent를 만들며 실행과 대상 요청을 시작하지 않는다. 임의 양수 intent나 미리 만든 seed intent를 실제 Planner 결과로 대체하지 않는다. 이 개발 검증에서는 실제 모델 호출을 실행하지 않는다. 성공 출력의 `intent_ids`는 실제 Store에 저장된 open intent의 비민감 ID다. 등록 GET 자료를 만드는 담당자는 이 실제 ID와 기존 결과를 확인해 snapshot·승인 입력에 참조한다.

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

설치자는 기존 `db.Open` migration으로 `request_approvals` 테이블을 설치하고 해당 실행 역할에 SELECT/INSERT/UPDATE만 부여한다. 첫 실행 runtime은 DDL이나 grant를 수행하지 않는다. 저장 실패는 전송 허용이 아니다. private 요청 원문은 실행 메모리에만 보류하고 DB에는 요청 지문·회차/task/intent/call/attempt·계정/세션 참조·공개 projection·결정/소비/결과만 남긴다.

기존 승인 화면의 요청별 HTTP 카드에서 실제 task를 선택해 보류 요청을 확인한다. API는 다음과 같고 기존 JWT middleware 안에 등록된다.

- GET `/api/tasks/{id}/request-approvals`, GET `/api/tasks/{id}/request-approvals/{approvalID}`
- POST `/api/tasks/{id}/request-approvals/{approvalID}/decision`: `{ "decision": "allow", "request_sha256": "상세에서 읽은 지문" }` 또는 `deny`
- POST `/api/tasks/{id}/request-approvals/stop`: 현재 결속된 E② 전송 경계 중지. 실행 재개나 전체 변경 API를 열지 않는다.

승인 API는 Authorization Bearer JWT의 HS256·현재 비밀번호 결속·만료·subject를 다시 검사한다. 쿠키/query token만으로 결정할 수 없어 ambient cookie에 의한 CSRF를 허용하지 않는다. 목록과 상세도 task binding을 요구하며 다른 task의 ID를 바꿔 접근할 수 없다. 서버가 이미 가진 task의 종료 상태와 live waiter/기한/지문을 확인하고 CAS로 단회 결정·소비한다. 중복·늦은 결정은 추가 송신을 만들지 않는다.

`allowed`는 송신 성공이 아니다. 소비 이후에도 정책·예산·감사·TLS와 현재 실행 상태를 재검사한다. 결과는 `not_sent`, `awaiting_dispatch`, `dispatch_pending`, `completed`, `unknown`으로 분리한다. 서버가 재시작되면 과거 DB allowed/pending만으로 waiter를 복원하지 않으며, live 실행을 잃은 소비 기록의 미확정 결과는 unknown으로 조회한다. 네트워크 전체의 exactly-once를 보장하지 않는다. 현재 프로세스에 task 권한 결속이 없으면 재시작 후 과거 기록 조회도 열지 않는다. 이 단위의 UI/API 중지는 사용자 확인모드로 결속된 실행에 한정하며, 자동 모드의 사용자 중지 연결은 후속이다(현재 이 경로는 403).

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

실행 역할은 task/exploration·원본 Store·판정 저장에 필요한 읽기/삽입/갱신 권한과 해당 INSERT sequence USAGE를 받는다. `task_categories`, `task_relations`, `task_scope` 조회도 실제 task 복원 조회에 필요하다. 원본 목표 분해의 `set_constraints`는 task_constraints SELECT/INSERT, `add_task_scope`는 task_scope SELECT/INSERT를 사용한다. 두 테이블에 UPDATE/DELETE는 요구하지 않는다. 원본 Planner의 scoped 자산 조회는 assets/companies/company_scope/task_asset_links SELECT와 기존 try_inet(text) EXECUTE, 실제 asset 참조 intent는 asset_intercept_rules/task_intercept_rules SELECT와 exploration_anchors SELECT/INSERT를 필요로 한다. INSERT 대상의 serial/identity sequence USAGE도 확인한다. asset/company 내용의 쓰기 권한을 추가하지 않는다. 등록 모드는 E③/revision 저장 권한과 checkpoint/epoch SELECT를 추가로 받는다. checkpoint SQL 쓰기는 별도 호스트 권위 역할에만 필요하다. E③의 기존 immutable manifest/issuance 행 잠금에는 UPDATE(id) 열 권한이 필요하며, 기존 immutable trigger가 갱신 자체를 거부하는지도 확인한다. task_archives SELECT는 mode 등록 검사에 필요하다. **epoch/key registry에는 열 단위 권한을 포함한 쓰기 권한이 없어야 한다.** 최소 목록의 실행 검사는 `CheckFirstDiagnosisHTTPRuntimeDatabase`/`CheckFirstDiagnosisRuntimeDatabase`가 담당한다.

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

감사가 완결된 정상 정책 차단·미지원 경로는 실행 마감 실패와 구분한다. 일반 HTTP 감독자는 Planner의 후속 계획 세대를 기다리고, 최종 call·프로세스·프록시 drain 및 감사 결과를 대조한다. 정책/미지원 제한 intent는 기존 `blocked` 상태와 프로그램이 기록한 `e2_http_policy_limited` / `e2_http_unsupported` 사유로 남으며, 정상 마감이 이를 진단 성공이나 취약점 없음으로 바꾸지 않는다. 일부 요청만 허용된 호출도 실제 송신 건수와 제한 분류를 보존한다. 결과 불명·감사 실패·종료 미완료·근거 없는 blocked는 계속 실패다. 이 경로의 검증은 사전 정책 제한에 대한 것이며, 요청별 사용자 승인 API/UI 구현이나 사용자 거절의 end-to-end 수용을 대신하지 않는다.

M01의 같은 운영자 UID 배치에는 평가 UDS 인증 전제를 확대하지 않는다. 이 CLI의 첫 비평가 실행은 평가 제어 채널을 자동 활성화하지 않으며 알 수 없는 control/eval 설정을 폐쇄 파서에서 거부한다. 같은 UID의 embedded 제어 채널을 독립 runner 인증으로 조합하지 않으며, 독립 runner UID/mount 인증을 갖춘 별도 배치의 typed 제어 어댑터가 필요하다. manifest 마감이나 공식 판정 export 전체를 완료했다고 주장하지 않는다.

## 실제 자료 요청과 검증 범위

실제 자료는 모드별로 구분한다.
- 공통: 실제 대상·실행 범위와 승인, 모델/profile·local endpoint, 기존 인증 자료와 분리된 DB 역할, 실제 task/intent 및 해당 회차의 target instance/reset 근거.
- 일반 HTTP: 위 `policy.json` / `authority.json`에 필요한 승인·계정/세션·요청 예산, 격리 helper와 네트워크 경계, 보호된 CA/감사키/credential 자료. 등록 GET 전용 A/B carrier·KG registry·snapshot·LocalBinding은 공통 선행 조건이 아니다.
- 등록 GET/IDOR: 승인된 A/B 세션/carrier, KG registry와 같은 reset/generation의 snapshot, RootTrust/LocalBinding·epoch anchor·키 mapping, E③ Source 및 저장 권위 자료가 추가로 필요하다.

전체 VM·DB·키·marker를 함께 되돌리는 복원의 독립 antirollback 권위는 이 구현이 제공하지 않는다. 첫 M01 수용은 fresh-only 기동, 취소 후 이전 승인 재사용 거부, 동시 lease 거부에 한정된다. 실제 운영의 외부 root/backup 복원 경계 확인은 별도 설치자 자료와 수용이 필요하다.

설치자는 선택한 모드의 M01 namespace·전용 보호 경로, 기존 JWT/로그인 자료와 별도 PostgreSQL 역할을 공급한다. 등록 GET은 기존 키 mapping/epoch anchor와 제한된 권위 DSN도 공급한다. 이 자료의 제작이나 실제 대상/모델 회차 실행은 합성 시험 성공으로 대체하지 않는다.

owned loopback·synthetic provider·실제 PostgreSQL race 시험은 각각 검사한 경계만 증명한다. 실제 Juice Shop·실제 모델·같은 회차의 권한/세션/취소/저장 수용은 해당 자료로 별도 실행해야 한다. 제품 코드의 공급·기동·종료 구현이 남으면 그것을 팀원 자료 대기로 분류하지 않는다.
