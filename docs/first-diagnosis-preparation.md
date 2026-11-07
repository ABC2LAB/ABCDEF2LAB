# 첫 비평가 진단 준비 체크리스트

이 문서는 실행 전 인계·설치 준비용이다. 실제 진단을 시작하거나 권위를 발급하지 않는다. 이동찬의 환경·엔진 통합 준비와 공급자의 실제 자료를 구분한다. 개인 장비 조사·로컬 경로·세션·DSN·키·원본 감사 기록은 이 문서에 포함하지 않는다.

기준 구현은 [#304의 cae608b](https://github.com/ABC2LAB/ABCDEF2LAB/pull/304), 계약 대조 기준은 dev `2e096607bc387d8896c7623bcde5fa14cf04d596`이다. [해당 head의 기동 문서](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/docs/first-diagnosis-launch.md)를 재사용한다. #304가 바뀌거나 병합되면 **실제로 사용할 merged SHA의 CLI·검사 코드와 다시 대조**한다. 이 체크리스트는 #304 수용·병합 승인이나 신규 입력 형식 채택을 대신하지 않는다.

## 1. 준비 상태와 실행 경계

| 상태 | 의미 |
|---|---|
| READY | 해당 행의 좁은 준비·검증 항목만 완료, 근거 필요 |
| MISSING | 실제 자료·설치값·권한이 미수령 |
| NOT_VERIFIED | 값이나 문서는 있지만 실제 검사·수용 근거 미확인 |
| DEFERRED | 실행 직전에 확정하거나 별도 승인 이후 수행 |

- 일반 HTTP와 등록 GET은 별도 모드다. 일반 HTTP 실행·감사 마감은 E③ 판정·IDOR 검증 완료가 아니다.
- 준비 중 실제 LLM/유료 API·대상 요청, reset·표식 심기·승인 소비를 하지 않는다.
- `prepare-task`는 DB를 변경한다. `prepare-intents`는 실제 모델을 호출한다. `serve`/`run`은 권위를 취득하고 단회 시작 marker를 소비할 수 있다. 네 명령을 check로 취급하지 않는다.
- 모델·UID·DB·권위·정책을 fake, 임시 ID, wildcard, 빈 필수값, 프로그램 기본값으로 채우지 않는다.
- 결과에는 `providers=NOT_ACQUIRED`, `diagnosis=NOT_RUN` 등 실제 한계를 남긴다. 검사를 실행하지 않았으면 PASS 대신 NOT_RUN이다.

## 2. 설치자와 이동찬의 환경 인계

| 항목 | 담당 | 필요한 실제 근거 | 미확인 시 |
|---|---|---|---|
| Linux M01 서버1·동시 실행1 | 설치자/운영자 | 장비·실행 주체·단일 writer·다른 세션과의 경계 | 전용 사용 승인 확인, 공용 설정 변경 금지 |
| 비root 전용 UID | 설치자 | 실제 UID, 보호 경로 owner, 계정/권한 범위 | 관리자 또는 다른 UID로 검사 우회 금지 |
| Go1.26.3·소스 빌드 | 설치자/이동찬 | 실제 도구 버전·merged SHA·빌드 결과 | 기존 승인 범위의 도구 위치/설치 권한 요청 |
| E② helper/namespace/socket | 설치자/이동찬 | exact helper 바이트·owner, 실제 user/mount/network/PID namespace·FD·seccomp 수용 | sysctl 또는 가드 완화 금지. namespace 단독 성공은 helper 수용 아님 |
| 모델/대상/DB 경로·포트 | 운영자 | 승인 endpoint·origin/IP·충돌·격리 경로 근거 | 포트 미관찰을 예약·연결 성공으로 쓰지 않음 |
| DataDir/KeyDir/SkillDir | 설치자 | 실행 UID 소유 0700, KeyDir은 DataDir 밖, 필요한 skill/JWT 설치 | 기존 경로 재귀 chown/chmod·덮어쓰기 금지 |
| snapshot/state/journals/descriptors/authority | 설치자/이동찬 | 정규 절대경로, 보호 namespace, 실제 파일/device/inode/digest pin | 전용 빈 준비 경로와 최종 승인 경로를 구분 |
| PostgreSQL16 | 설치자 | 설치/runtime/등록 GET host authority 역할 분리, schema·권한 근거 | 관리자 DSN을 runtime으로 대체 금지 |
| JWT/로그인·감사 키·epoch | 설치자 | 기존 로그인 hash/JWT, active key mapping·secret version·revision, 기존 epoch/anchor | 임의 키/epoch·권위 발급 금지 |
| 모델 profile/manifest | 모델 운영자/설치자 | 실제 DB ID·format·base URL·model과 manifest의 정확한 일치 | 설정 일치와 실제 추론 성공을 각각 기록 |

보호 입력은 실행 UID 소유 **0600 일반 파일**, hardlink count=1로 받는다. 직계 부모도 0700이어야 한다. 부모는 root 또는 실행 EUID 소유이며 공유 쓰기를 허용하지 않는다(코드의 신뢰 root sticky 예외는 별도). symlink·교체·공유 쓰기 부모 경로를 허용하지 않는다. 디렉터리 문자열만 맞는 것으로 검증 완료를 표시하지 않는다. 런타임은 자동 migration·seed·JWT 생성·예약·복원·다른 task 실행을 하지 않는다.

기동 설정은 `abc2lab.first-diagnosis-launch/v1`, 일반 HTTP 또는 등록 GET 한 모드만 선택한다. CLI는 64KiB 이하 UTF-8, 중복/알 수 없는 키·null·대소문자 별칭 거부, 정규 절대경로, loopback listen 주소, 양수 model_profile_id, shutdown timeout 1~60초를 요구한다. 경로 최대 길이는 4096바이트다. registry_file은 경로가 아닌 파일 basename이다. E② UDS 경로는 `len(root)+len("/call-0123456789/proxy.sock") <= 106`바이트여야 하며 HTTP가 `StateDir/calls`를 넘기는 배치에서는 StateDir 최대73바이트다. 실제 미수령 값은 `.template`의 명시적 자리표시자로 남기며 check/기동 입력으로 쓰지 않는다.

모델은 실제 task의 단일 ready profile chain을 고정한다. goals/planner/worker 역할 profile은 비어 있거나 정확히 같은 ID다. 임의 fallback/다른 역할 모델로 대체하지 않는다. Source.ModelTag는 승인 metadata 값을 유지한다. 모델·GPU·유료 API를 새로 고르지 않는다.

## 3. PostgreSQL 설치 인계

기존 `db/schema.sql`, `db/task_mode_schema.sql`, `db/e3_finding_schema.sql`, `evidencerevision/schema.sql`, `auditcheckpoint/schema.sql`, `auditkeys/schema.sql` 및 실제 설치 호출 경로를 재사용한다. 아래는 새 설치 SQL이 아니라 **권한 대조 체크리스트**다. 운영자가 승인한 전용 DB에서만 설치한다.

- 설치 역할은 schema·로그인·모델/키 registry·epoch 준비를 맡고 runtime DSN으로 재사용하지 않는다.
- 공통 SELECT/INSERT/UPDATE: `tasks`, `explorations`, `exploration_nodes`, `task_llm_profiles`, `findings`.
- 공통 SELECT/INSERT: `activity`, `exploration_edges`, `task_scope`, `task_constraints`, `exploration_anchors`.
- 공통 SELECT: `llm_profiles`, `intercept_rules`, `settings`, `agents`, `agent_prompts`, `task_categories`, `task_relations`, `assets`, `companies`, `company_scope`, `task_asset_links`, `asset_intercept_rules`, `task_intercept_rules`.
- INSERT 대상의 실제 serial/identity sequence마다 USAGE, `try_inet(text)` EXECUTE. 검사기의 실제 목록과 설치 schema를 대조한다.
- `task_categories`, `task_relations`, `task_scope` 조회가 필요하고, `task_archives` SELECT는 등록 GET 추가 요구다. `task_constraints`와 `task_scope`는 SELECT/INSERT이며 UPDATE/DELETE를 추가하지 않는다.
- 원본 Planner는 `assets`, `companies`, `company_scope`, `task_asset_links`, `asset_intercept_rules`, `task_intercept_rules` 조회, `try_inet(text)` EXECUTE, `exploration_anchors` SELECT/INSERT를 사용한다. asset/company 쓰기는 추가하지 않는다.
- 등록 GET 추가 SELECT/INSERT/UPDATE: `task_control_modes`.
- 등록 GET 추가 SELECT/INSERT: `traffic_evidence_snapshots`, `finding_traffic_bindings`, `e3_evidence_manifests`, `e3_oracle_verdicts`, `e3_verdict_issuances`, `e3_verdict_links`, `evidence_revision_initials`, `evidence_revision_operations`.
- 등록 GET 추가 SELECT: `audit_checkpoint_epochs`, `task_archives`, `audit_checkpoint_history`, `audit_checkpoint_heads`, `audit_checkpoint_operations`.
- `e3_evidence_manifests`, `e3_verdict_issuances`의 행 잠금용 UPDATE(id)만 추가한다. 전체 UPDATE/DELETE/TRUNCATE/TRIGGER 금지와 갱신을 거부하는 정확한 기존 `guard_e3_immutable` trigger를 확인한다.
- runtime은 epoch/key registry에 **열 단위도 포함한 쓰기 권한이 없어야 한다**. checkpoint SQL 쓰기는 별도 host authority 역할이다.
- superuser/CREATEDB/CREATEROLE/replication/BYPASSRLS, 상속·SET ROLE을 통한 관리자 권한, 광범위 read/write·server file/program·monitor, session_replication_role 변경, DB CREATE/TEMP, schema/object 소유/CREATE, 실행 가능한 SECURITY DEFINER, 임시 schema·모호한 search path를 검사한다.
- host authority는 별도 LOGIN이다. epoch/key/history SELECT와 행 잠금에 필요한 **epoch UPDATE(store_ref), registry/history UPDATE(key_ref)**만 허용한다. 전체 UPDATE·추가 열 UPDATE·key command receipt 쓰기는 금지다. checkpoint history/operations는 SELECT/INSERT, heads는 SELECT/INSERT/UPDATE다.
- runtime pool 최대8, host authority pool 최소2. 실제 DB/서버/schema identity가 같아야 한다. DSN은 `runtime_dsn_file`/`authority_dsn_file`의 보호 파일로 전달하고 명령 인자·PR·stdout에 원문을 넣지 않는다.

정확 소스: [별도 authority 검사](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/server/controlled_read_assembly_database_authority.go), [런타임 DB 검사](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/server/controlled_read_assembly_database_role.go), [기동 check](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/cmd/first-controlled-diagnosis/launch_check.go). 함수 `CheckFirstDiagnosisHTTPRuntimeDatabase` / `CheckFirstDiagnosisRuntimeDatabase`의 실제 정의와 호출을 따라 최신 목록을 대조한다. 시험 fixture의 fake credentials/roles를 운영 자료로 복사하지 않는다. 검사기는 모든 테이블의 가능한 과잉 권한이 전부 제거됐음을 보증하는 도구가 아니다.

## 4. 실제 자료 인계 대장

수령 상태는 각 행마다 기록한다. 아래 빈 대장을 수령 완료나 공식 데이터 계약으로 간주하지 않는다. 최신 [A/B 준비 계약](contracts/ab-session-prep-stage1.md)도 초안이다. 아래 담당·장비/전달 경로·삭제 확인 방식은 확인 창구와 제안 분담이며 공급 담당의 확정이 필요하다.

| 확인 창구 | 받아야 할 자료 | 보호 전달·수용 조건 | 폐기/재준비 조건 |
|---|---|---|---|
| 윤재상·fixture 운영자, 필요 시 최민준 | 실제 대상 버전/instance/승인 origin/IP, reset/health 성공 근거, 회차/generation/reset | 비밀 없는 참조와 보호 파일을 분리, 같은 회차인지 대조 | reset/instance 변화·준비 실패 |
| 윤재상·fixture 운영자 | 서로 다른 A/B 라벨·인증된 identity, 헤더 종류·만료/회수 조건, 실제 세션 | 기존 snapshot 계약의 0700/0600 경로. 비밀번호/쿠키/토큰을 채팅·문서·명령 인자로 받지 않음 | expiry/revoke/reset 변경, 실행 직전 재확인 |
| 이경준·내용 공급 담당, 윤재상 보조 | 실제 carrier type/resource_ref, owner A/cross B 기대권한, ReadOnly 등록/비변경 조회 근거, 심은 표식이 owner 응답에 그대로 나온 근거 | 같은 객체 owner/cross 조회 참조. 응답/LLM 추정만으로 소유·권한·표식 확정 금지 | carrier/정책/표식/세대 변경 |
| 이경준·KG 공급 담당 | registry version/digest/generation/instance/reset과 snapshot에 정확히 대응하는 계정·자원·정책·표식 묶음 | CheckPairing, 승인·ReadOnly·정책·세대·만료, 실제 로더와 같은 snapshot 객체 결속 | 바이트/pin 또는 문맥 변화 |
| 최민준·관련 공급 담당 | 신규 LocalBinding/실행 전 metadata 채택 상태·형식 인계 가능 여부, 비평가 조건 | 아직 미채택 초안이면 그대로 표시. EvalRunID는 빈 값/SQL NULL | 계약 변경 시 재검토 |

세션 파일은 snapshot 디렉터리 아래 `<label>-session.secret` 형태의 한 단계 basename으로 인계하며 snapshot JSON에 세션 원문을 넣지 않는다. snapshot과 registry 파일명도 구분한다. 실제 task/intent가 만들어진 뒤 snapshot을 만들고 만료는 실제 세션·registry 유효 창 이내로 제한한다.

carrier는 최신 [선정 계약](contracts/kg-canary-carrier-stage1.md)의 review → feedback → export → user 순서에서 C1~C6를 모두 통과한 첫 후보를 따른다. basket은 측정에서 탈락했으며 적격 후보가 없으면 UNDECIDABLE을 유지한다. [registry skeleton](contracts/kg-registry-stage1-skeleton.md)의 basket은 합성 placeholder이며 실제 선정 자료가 아니다. GT/챌린지 정답은 runtime 입력·carrier 선택에 쓰지 않는다. 실제 자료가 없으면 오프라인 CheckPairing도 NOT_RUN이다. 준비 단계의 owner/cross 실제 요청은 금지다.

등록 GET의 LocalBinding 원문 SHA256은 **기존** `audit_checkpoint_epochs.anchor_digest`에 결속한다. 실제 store/epoch/generation/publisher·active key·task/intent/case/reset/policy, state/journal/descriptor/key 디렉터리 device/inode, snapshot/registry/metadata/manifest/JWT 파일 원문 SHA256을 최종 확정한다. 짧은 승인 유효기간은 최대15분이다.

실행 전 metadata는 `abc2lab.first-controlled-read-inputs/v1`과 Source의 실행 전 projection을 따른다. 실제 Expected/task/intent/identity/resource/read approval/KG policy·문맥·ModelTag를 쓴다. `RunRef`는 비우고 runtime만 채운다. 완료/finalized/revision/evidence digest/traffic ID/final checkpoint를 미리 넣지 않는다. 신규 paused task에서 mode 행이 실제로 없을 때만 ModeVersion=0이다. 세션 헤더·canary 값은 metadata에 넣지 않는다.

## 5. check가 확인하는 것과 하지 않는 것

현재 launch `check` 경로는 `checkLaunch`의 검사와 선택 DB read 검사이며, `Start`/provider 취득·단회 시작 marker 생성 경로를 호출하지 않는다. 출력의 `providers:NOT_ACQUIRED`, `diagnosis:NOT_RUN`을 유지한다. 다만 **check PASS는 전체 인계 자료 검증이 아니다**.

- 공통 config·일부 보호 경로/JWT·manifest 및 설정 정합을 검사한다.
- `-database`를 붙이면 실제 runtime schema/역할/모델 profile 등 해당 코드의 조회 검사를 추가한다. 설치/migration/GRANT를 하지 않는다.
- 두 DB pool의 동일 DB/서버/schema identity는 이 check에서 대조하지 않는다. 실제 조립/권위 취득 단계의 별도 검증이 필요하다.
- HTTP policy/authority 원문 내용·실제 권위 취득, 등록 LocalBinding/metadata/registry/snapshot 전체 결속·CheckPairing을 모두 검증했다고 확대하지 않는다. 보호 자료별 검사/실제 공급자 취득의 범위를 별도로 기록한다.
- helper 실배치·모델 추론·대상 health/reset·A/B 조회·감사/판정/저장 성공을 증명하지 않는다.
- 실제 자료·접근 권한이 없거나 자리표시자가 남으면 강행하지 않는다. 원문 비밀이 없는 요약만 공유하고 상세 출력은 보호 경로에 보존한다.

근거: [launch config parser](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/cmd/first-controlled-diagnosis/launch_config.go), [launch check](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/cmd/first-controlled-diagnosis/launch_check.go), [CLI 분기](https://github.com/ABC2LAB/ABCDEF2LAB/blob/cae608b28a8dfdb57c60011f2e925710e0cebcad/cmd/first-controlled-diagnosis/main.go).

## 6. 병합 후 실행 절차 (지금 실행하지 않음)

이 절차는 미래 실행 승인을 대신하지 않는다. 각 단계의 조건이 미충족이면 멈추고 해당 담당에게 필요한 자료를 요청한다. 전체 평가 배치·쓰기 IDOR·보상·RAG·파인튜닝은 이 범위에 추가하지 않는다.

1. #304의 병합 여부·최신 head·리뷰와 실제 merge commit, 최신 dev를 다시 확인한다. 원본/피드백 작업자의 checkout을 전환하지 않고 별도 소스 경로에서 실제 사용할 merged SHA를 고정한다.
2. 승인된 Go1.26.3로 아래 빌드. 실제 설치/runtime/host authority 역할, 기존 JWT·로그인·active key/epoch, DB model profile/manifest, Linux/helper 배치를 확인한다. 모델을 호출하지 않고 설정 일치 여부를 먼저 기록한다.
3. 보호된 launch 설정과 task 입력으로 `prepare-task`. 반환된 실제 task_id/exploration_id만 보존한다. 다음 `prepare-intents`는 **명시적 실제 모델 호출 승인 후** 수행하고 실제 저장된 intent IDs를 확인한다. 준비 중 대상 도구는 차단된 경로를 사용한다.
4. 공급자가 실제 task/intent와 최신 instance/reset/generation에 맞춰 유효 A/B·snapshot/registry/metadata·정책을 인계한다. 오프라인 형식/CheckPairing·ReadOnly·권한/세대/만료·같은 snapshot 결속을 확인한다.
5. 운영자가 새 단회 승인과 최종 bytes/device/inode/digest pin, LocalBinding 및 기존 epoch anchor 결속을 확정한다. 승인 최대15분 및 실제 세션 만료를 확인한다. 임시 ID/wildcard 금지.
6. 보호 설정으로 `check`, `check -database`. 비소비 경로지만 검사 범위 밖 공급 검증을 생략하지 않는다. 입력/권한/승인 바이트가 바뀌면 다시 검사한다.
7. 명시적 실행 승인과 **새 회차의 유효한 미소비 단회 승인**으로 `run` 한 번. check가 소비하지 않았고 바이트/유효기간도 그대로라면 그 새 승인을 사용할 수 있다. 만료·변경 시 재발급/재결속/재검사한다. `serve`는 대안이며 같은 승인/root로 serve 후 run을 순차 실행하지 않는다.
8. 신규 admission 차단 → task/call/대상 drain → 자식/터널/소켓 종료 → 감사/저장 → host/key/FD/SQL lease 반환을 같은 회차로 대조한다. 불명은 실패·불명으로 보존한다. 일반 HTTP는 execution/closure와 `e3=NOT_APPLICABLE`; 등록 GET은 감사 ACK/봉인 → revision/E③/finding(confirmed만) → 기존 인증 조회 대조다. 비평가 EvalRunID는 비워 둔다.

아래 변수는 운영자가 **실제 승인된 절대경로/ID**로 설정한다. placeholder를 그대로 실행하지 않는다. 비밀값은 변수가 아니라 보호 파일 안에만 둔다.

```sh
# 별도 merged-source checkout에서, 승인된 설치 범위로 수행
: "${APPROVED_BIN:?actual protected binary path required}"
: "${APPROVED_LAUNCH_CONFIG:?actual protected config path required}"
: "${APPROVED_TASK_FILE:?actual protected task input path required}"
go version  # go1.26.3 확인
go build -o "$APPROVED_BIN" ./cmd/first-controlled-diagnosis

# 별도 실행 승인 후. DB 변경과 모델 호출을 check로 부르지 않는다.
"$APPROVED_BIN" prepare-task -config "$APPROVED_LAUNCH_CONFIG" -task "$APPROVED_TASK_FILE"
: "${ACTUAL_TASK_ID:?use actual prepare-task result}"
"$APPROVED_BIN" prepare-intents -config "$APPROVED_LAUNCH_CONFIG" -task-id "$ACTUAL_TASK_ID"

# 실제 자료·새 단회 승인·최종 pin/anchor 확정 및 오프라인 대조 후
"$APPROVED_BIN" check -config "$APPROVED_LAUNCH_CONFIG"
"$APPROVED_BIN" check -config "$APPROVED_LAUNCH_CONFIG" -database
# 실행 직전 유효기간·바이트 동일성·미소비 승인 재확인 후, 딱 한 번
"$APPROVED_BIN" run -config "$APPROVED_LAUNCH_CONFIG"
```

명령을 적었다는 사실은 실행 근거가 아니다. 실제 실행 기록에는 exact SHA·각 단계 실행 여부·결과·미검증 범위를 따로 적는다. fresh-only M01의 보호 namespace/단일 writer 근거를 전체 VM·DB·키 동시 복원 antirollback이나 독립 UID 평가 배치 수용으로 넓히지 않는다.
