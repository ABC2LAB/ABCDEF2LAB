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

감사가 완결된 정상 정책 차단·미지원 경로는 실행 마감 실패와 구분한다. 일반 HTTP 감독자는 Planner의 후속 계획 세대를 기다리고, 최종 call·프로세스·프록시 drain 및 감사 결과를 대조한다. 정책/미지원 제한 intent는 기존 `blocked` 상태와 프로그램이 기록한 `e2_http_policy_limited` / `e2_http_unsupported` 사유로 남으며, 정상 마감이 이를 진단 성공이나 취약점 없음으로 바꾸지 않는다. 일부 요청만 허용된 호출도 실제 송신 건수와 제한 분류를 보존한다. 결과 불명·감사 실패·종료 미완료·근거 없는 blocked는 계속 실패다. 이 경로의 검증은 사전 정책 제한에 대한 것이며, 요청별 사용자 승인 API/UI 구현이나 사용자 거절의 end-to-end 수용을 대신하지 않는다.

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

이 연결 단위는 실제 첫 기동 경로인 #304와 공통 보고서/조치 본체 #316의 코드에 의존한다. 두 변경이 dev에 들어오기 전에는 통합 branch의 ancestry를 유지해 검토해야 하며, #314의 요청별 승인 경로를 이 변경이 대신 구현한 것으로 보지 않는다.

## 실제 자료 요청과 검증 범위

실제 자료는 모드별로 구분한다.
- 공통: 실제 대상·실행 범위와 승인, 모델/profile·local endpoint, 기존 인증 자료와 분리된 DB 역할, 실제 task/intent 및 해당 회차의 target instance/reset 근거.
- 일반 HTTP: 위 `policy.json` / `authority.json`에 필요한 승인·계정/세션·요청 예산, 격리 helper와 네트워크 경계, 보호된 CA/감사키/credential 자료. 등록 GET 전용 A/B carrier·KG registry·snapshot·LocalBinding은 공통 선행 조건이 아니다.
- 등록 GET/IDOR: 승인된 A/B 세션/carrier, KG registry와 같은 reset/generation의 snapshot, RootTrust/LocalBinding·epoch anchor·키 mapping, E③ Source 및 저장 권위 자료가 추가로 필요하다.

전체 VM·DB·키·marker를 함께 되돌리는 복원의 독립 antirollback 권위는 이 구현이 제공하지 않는다. 첫 M01 수용은 fresh-only 기동, 취소 후 이전 승인 재사용 거부, 동시 lease 거부에 한정된다. 실제 운영의 외부 root/backup 복원 경계 확인은 별도 설치자 자료와 수용이 필요하다.

설치자는 선택한 모드의 M01 namespace·전용 보호 경로, 기존 JWT/로그인 자료와 별도 PostgreSQL 역할을 공급한다. 등록 GET은 기존 키 mapping/epoch anchor와 제한된 권위 DSN도 공급한다. 이 자료의 제작이나 실제 대상/모델 회차 실행은 합성 시험 성공으로 대체하지 않는다.

owned loopback·synthetic provider·실제 PostgreSQL race 시험은 각각 검사한 경계만 증명한다. 실제 Juice Shop·실제 모델·같은 회차의 권한/세션/취소/저장 수용은 해당 자료로 별도 실행해야 한다. 제품 코드의 공급·기동·종료 구현이 남으면 그것을 팀원 자료 대기로 분류하지 않는다.
