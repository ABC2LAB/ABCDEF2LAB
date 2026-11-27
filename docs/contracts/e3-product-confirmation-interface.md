# E③ 제품 확정 경로 인터페이스 계약

상태: **계약 초안**(2026-11-18, 최민준 작성, 리뷰 이동찬·이경준). [개발 계획 v4.2](../development-plan.md) "E③ 인터페이스 PR 완료 기준"의 공통 계약이다. 무엇을 왜 하는지는 [E③ 확정 경로 결정](../e3-confirm-path-decision.md)(#596)이 정본이고, 이 문서는 세 갈래(판정·E② 연동·화면)가 만나는 모양만 고정한다. 코드는 `e3contract` 패키지, 가짜 구현은 `e3contract/e3fake`다.

이 계약이 바꾸지 않는 것: 오라클 판정 규칙(`oracle.Evaluate`)과 사유 목록, 평가 경로(등록 GET `transmit`·`evidencebind`·`e3record`), 모델 단계의 E② 동작. 선언·시험 계정이 없는 회차에서는 확정 단계가 아무 요청도 보내지 않는다(§7 `disabled`).

## 1. 갈래와 담당

| 갈래 | 이 계약에서 맡는 것 | 담당 |
|---|---|---|
| 판정 | `Run`(확정 단계 순서), 판정 행, 정책 선언 저장·동결(`DeclarationReader`), 리셋 확인(`ResetGate`) | 최민준 |
| E② 연동 | `Transport`(프로그램 요청 셋·표식 등록), `Journal`(봉인된 저널 v3 읽기) | 이동찬 |
| 화면 | `RunResult`의 상태·사유·후보 표시, 정책 선언 입력 | 이경준·윤재상 |

각 갈래는 상대 쪽을 `e3fake`로 두고 만든다. 실제 구현은 같은 계약 시험(§10)을 통과해야 한다.

## 2. 정책 출처와 실행 결속

- `PolicySource{kind, ref, digest_sha256, target_approval_revision}`. 종류는 `kind`로만 정하고 `policy_version` 문자열 모양으로 추정하지 않는다.
  - `product_declaration`: 운영자 정책 선언의 동결본(제품 경로).
  - `kg_snapshot`: 평가 fixture 레지스트리 snapshot(평가 경로).
- **제품 확정 단계는 `product_declaration`만 받는다.** `kg_snapshot`이면 모양이 맞아도 확정 단계를 켜지 않는다(`disabled`/`policy_kind_not_product`, 요청 0건).
- `RunBinding{job_ref, run_ref, task_ref, e2_policy_ref, generation_ref, instance_ref, policy}`는 서버가 실행마다 발급한다. `run_ref`·`task_ref`·`e2_policy_ref`는 E² 실행 범위(`transmit.Scope`)와 같은 값이다. `e2_policy_ref`는 실행 승인 정책이고 E③ 정책 정본(`policy`)과 다르다.
- 확정 단계의 모든 관찰에 같은 결속이 들어간다. 칸 하나라도 다르면 진위 실패(`binding_mismatch`)다.

## 3. 정책 선언 레코드

[결정 §4.1](../e3-confirm-path-decision.md#41-선언하는-것-자원-종류마다)의 칸을 그대로 쓴다. 형식 판은 `e3-product-declaration/v1`이다.

| 칸 | 뜻 |
|---|---|
| `target_ref`·`target_approval_revision` | 선언이 붙은 대상과 승인 판. 다시 승인하면 무효 |
| `declared_by`·`declared_at` | 누가·언제(감사) |
| `identity{path, subject_id_path}` | 신원 확인 GET. 대상마다 하나(결정 §5.1이 대상 단위로 확인하므로) |
| `types[]` | 자원 종류마다 `type`·`sharing`·`owner_only_fields`·`create{path, method(POST·PUT), body_kind(json·form), body_template, marker_field, id_path 또는 id_field}`·`read{path_template, data_path, status_path?, normal_value?}` |
| `owner_account_label`·`cross_account_label`·`accounts_same_privilege` | 확정 단계 계정 역할(선택, 2026-11-19 사용자 결정). 소유·생성 시험 계정(A), 교차 조회 시험 계정(B), 두 계정이 같은 권한 등급이라는 운영자 확인. §4 |

- `owner_only`이고 `marker_field`가 `owner_only_fields`에 있는 종류만 시험한다. `shared` 종류는 요청 칸을 두지 않고, 확정 단계는 요청도 판정 행도 만들지 않고 `skipped`로 보인다. 판정 값은 오라클만 만들기 때문에, 사유만으로 UNDECIDABLE 행을 꾸미지 않는다.
- 경로는 승인 범위 안 절대 경로다. query·조각·점 조각·공백을 받지 않는다. `read.path_template`에는 `{id}` 조각이 정확히 하나다. 승인 범위 안인지는 E²가 요청마다 다시 본다.
- 정본 바이트는 칸 순서를 고정한 JSON이다(HTML 이스케이프 없음, 시각은 UTC 마이크로초, 종류·필드는 오름차순, 빈 목록은 null). `digest_sha256`은 이 바이트의 SHA-256이다. `e3record`의 정본 함수가 비공개라 이 형식 판에 규칙을 따로 둔다.
- `Freeze(decl, ref)`가 실행 시작 때 정책 출처를 만든다. `VerifyFrozen(source, decl)`은 읽은 동결본의 digest와 대상 판을 대조한다.
- **자격은 선언에 넣지 않는다.** 확정 단계 요청의 자격은 등록된 시험 계정 세션(C14 인증 준비)에서만 E²가 붙인다. 저장 API는 본문 틀(`body_template`)이나 다른 칸 값에 인증 헤더·쿠키·토큰·비밀번호 모양이 있으면 `400 {"error": "invalid_declaration", "field": "<칸 이름>"}`으로 거부하고, 거부한 값은 오류 응답·로그에 원문으로 남기지 않는다(정책 선언 저장 PR).
- **저장 API의 대상 판 결속(2026-11-18 사용자 결정).** 화면은 자기가 본 대상 판을 기대값으로 보낸다. 대상이 승인 상태가 아니면 `409 target_not_approved`, 지금 판과 다르면 `409 declaration_target_revision_mismatch`이고, 같을 때만 그 판에 결속해 저장한다. `declared_by`·`declared_at`은 서버가 인증 주체와 저장 시각으로 채우고, 요청에 들어오면 거부한다. 종류·필드 순서는 서버가 정렬한 뒤 검사한다.
- **저장 API(구현).** 대상 등록 보호 저널(`declarations/`)에 선언을 통째로 둔다. 저널 줄이 감사 기록(누가·언제·어느 대상 판·digest·선언)이다.
  - `GET /api/product/targets/{id}/declaration` → `200 {"state": "none"|"active"|"invalid", "invalid_reason"?, "target_revision"?, "declared_by"?, "declared_at"?, "declaration": {…}|null}`. `invalid_reason`은 `target_reapproved`(대상 판이 바뀜 — 재승인·철회)·`target_expired`(승인 만료)다. 선언이 없으면 `{"state": "none", "declaration": null}`이다(라우트가 없으면 404라서 구분된다).
  - `PUT /api/product/targets/{id}/declaration` 본문 `{"target_revision": N, "declaration": {"identity": {…}, "types": [{…}]}}` → 저장한 선언의 같은 모양. 서버가 채우는 칸(`version`·`target_ref`·`target_approval_revision`·`declared_by`·`declared_at`)이 오면 `400 {"error": "invalid_declaration", "field": "<그 칸>"}`이다.
  - 형식 오류·자격 모양·승인 범위 밖(경로 접두·정규 경로·승인 메서드)은 `400 {"error": "invalid_declaration", "field": "…"}`다. 칸 이름의 종류 순번(`types[i]`)은 서버가 이름순으로 정렬한 뒤의 순번이다.
  - 다른 운영자의 대상은 계정 API와 같이 `404 target_not_found`다(존재를 드러내지 않는다). 지금 권한 모델에서는 대상 승인자 = 등록 운영자 한 사람이라 별도 403은 없다.
  - 철회(DELETE)는 최소 기능에 없다(405). 바꾸려면 PUT으로 교체하고, 대상을 다시 승인하면 무효가 된다.
  - 크기 상한: 종류 16개, 소유자 전용 필드 16개, 본문 틀 2KiB, 선언 인코딩 24KiB(준비 프로세스 IPC 한 프레임 64KiB 안).
- **실행 시작 때 동결(구현).** 회차를 발급할 때(`runs/<job>` 생성) 지금 대상 판에 유효한 선언이 있으면 회차 디렉터리에 두 파일을 0600으로 쓴다. 선언이 없으면 쓰지 않고, 그 회차의 확정 단계는 꺼진다.
  - `e3-declaration.json`: 선언 정본 바이트 그대로. 이 파일의 SHA-256 = 정책 출처 digest(`policy_version`).
  - `e3-policy.json`: 정책 출처(`kind=product_declaration`, `ref=declaration-<job>`, digest, 대상 승인 판)와 유효창(대상 승인 기간과 실행 기간이 겹치는 구간). 확정 단계가 `RunBinding.Policy`와 `PolicyValidFrom·Until`을 여기서 만든다.
  - 준비 저널은 압축될 수 있어 옛 선언 줄을 나중에 다시 찾을 수 없으므로 회차마다 사본을 둔다. 저장된 판정을 다시 읽을 때도 이 사본을 읽고 현재 대상 등록의 선언으로 대신하지 않는다(OD06).
- `DeclarationReader.ReadFrozen`은 저장된 동결본을 그대로 돌려준다(제품: `declaration-<job>` 참조의 회차 사본). 읽을 수 없으면 `ErrPolicyUnavailable`이다(시작 거절, 진위 실패 아님). 읽었는데 digest·대상 판이 다르면 진위 실패(`policy_digest_mismatch`)다.

## 4. E② 프로그램 요청

- 목적 셋: `e3_identity`(신원 GET), `e3_create`(A 세션 생성, POST·PUT), `e3_read`(A·B 세션 조회 GET). 인증 준비처럼 목적 규칙으로만 열리고 모델 요청의 규칙 찾기에서는 빠진다.
- E② 정책을 읽을 때 목적 규칙 전체를 같은 실행의 Worker 승인 규칙 하나에 결속한다. 스킴·호스트·포트·연결 IP·TLS pin·메서드가 같고 해당 규칙이 경로를 덮어야 한다. 조회 틀은 `{id}` 앞의 고정 접두 범위가 필요하다. `/`, 끝 `/`, `/{id}`는 목적 규칙 경로로 받지 않는다. 제외 경로가 목적 경로 전체를 덮으면 정책을 거절하고, 일부 ID만 제외하는 경우 채운 경로를 요청마다 대조한다.
- `confirm_every_request`의 E③ 요청별 확인 대상은 생성이다. 신원과 조회는 송신을 열기 전에 동결 선언과 실행 승인 범위에 결속해야 한다. 현재 v2 저널에서는 세 목적의 호출을 송신 전에 차단한다.
- **계정 역할은 선언이 정한다(2026-11-19 사용자 결정).** 소유·생성 계정(A)은 `owner_account_label`, 교차 조회 계정(B)은 `cross_account_label`이고, 운영자가 두 계정이 같은 권한 등급이라고 확인한다(`accounts_same_privilege: true`). 셋은 선언과 함께 동결본에 들어가 digest로 묶인다. 근거: 서버가 인증된 시험 계정을 이름순 등으로 고르면 관리자 계정이 교차 조회자(B)가 될 수 있다. 관리자는 남의 객체를 정상 권한으로 읽을 수 있어서, 그 조회에 표식이 보이면 접근통제 결함이 없는데도 CONFIRMED가 난다. 어느 계정이 같은 등급인지는 대상을 아는 운영자만 안다.
  - 역할 둘은 함께 정하거나 함께 비운다. 같은 계정을 두 역할에 둘 수 없다. 역할이 없으면 등급 확인도 둘 수 없다(저장 API는 그 칸 이름으로 400).
  - 확정 단계는 선언에 역할이 없으면 `account_roles_undeclared`, 등급 확인이 참이 아니면 `accounts_privilege_unconfirmed`, 선언한 두 계정 중 하나라도 그 실행의 인증 준비 결과가 `authenticated`가 아니면 `test_accounts_unavailable`로 꺼진다(요청 0). 인증된 다른 계정으로 대신하지 않는다.
  - 칸은 선택 칸이다. 역할 없이 저장한 선언은 정본 바이트가 칸을 넣기 전과 같아서(형식 판 그대로) 이미 저장한 선언과 화면 파서가 그대로 읽는다.
- 서버는 선언한 라벨에 C14 인증 결과(`authenticated`)를 붙인다. 관찰에는 E²가 실제로 붙인 라벨이 들어가고, 요청한 라벨과 다르면 진위 실패다.
- 요청 하나는 영수증 하나(`CallRef{call_ref, attempt_id, sequence}`)다. 같은 요청을 다시 보내지 않는다(결과 불명 쓰기 포함).
- **객체 ID 출처(2026-11-19 사용자 결정).** 선언의 `create`는 둘 중 하나만 준다. 둘 다 주면 `create.id_field`, 둘 다 없으면 `create.id_path` 칸 이름으로 저장 API가 400이다.
  - `id_path`(생성 응답 JSON의 ID 경로, 지금 방식): 객체 ID 원문은 E² 밖으로 나오지 않는다. 생성 관찰은 실행별 키 digest만 주고, 조회 요청은 생성 관찰의 `CallRef`를 가리키면 E²가 그 ID로 경로를 채운다.
  - `id_field`(생성 요청 본문의 ID 칸, 클라이언트가 ID를 정하는 API — 예: VAmPI 책은 제목이 ID이고 생성 응답에 ID가 없다): 확정 단계가 회차·종류마다 새 난수 ID(`e3id-` + 16진 32자)를 만든다. 표식과 다른 난수에서 따로 뽑고 표식에서 파생하지 않으며, 표식과 같거나 한쪽이 다른 쪽에 들어 있으면 쓰지 않는다. **ID는 비밀이 아니다**: 대상으로 보내는 조회 경로에 들어가고, E③ 결과(`RunResult.Writes[].object_id`)와 쓰기 기록(`confirmation_writes`)에 원문이 남는다(리셋 확인 때 그 ID로 조회하려고). 다만 E²의 v3 이벤트·영수증·공유 로그에는 지금처럼 객체 ID 원문을 넣지 않고 digest만 둔다([v3 계약](e3-e2-v3-journal-contract.md) §2.2 그대로). 사용자 승인 모드의 승인 미리보기(`input_preview`)는 보낼 생성 본문을 그대로 보여 주므로(표식만 가림) 그 본문 칸의 ID가 보인다. **표식은 계속 비밀이다.** ID 칸은 표식 칸과 같은 칸 경로 규칙이고(json은 점 경로, form은 점 없음), 표식 칸과 같거나 상·하위 칸일 수 없다. "본문 틀에 이미 있으면 안 된다"는 표식 칸과 같은 범위다: 저장 검사는 json이면 맨 위 칸만 보고(점 경로는 안쪽까지 보지 않음), form이면 그 키를 본다. 점 경로 안쪽에 이미 값이 있으면 저장은 되지만 확정 단계가 본문을 만들 때 덮어쓰지 않고 멈춘다(`product_create_request_unavailable`, 요청 없음). `owner_only_fields`와는 상관없다(들어가도 되고 안 들어가도 된다). 판정은 표식 칸만 보고, 표식 칸은 지금처럼 `owner_only_fields` 중 하나여야 한다. 생성은 그 ID를 `CreateRequest.ObjectID`로, 조회는 `ReadRequest.ObjectID`로 싣는다. 서버 자격 모양 검사(`CredentialField`)에 `create.id_field`도 들어간다.
  - 생성이 ID 충돌 등으로 정상 응답이 아니면 그 종류는 UNDECIDABLE(`product_create_failed`)이다. 다른 ID로 다시 보내지 않는다. ID를 만들 수 없거나 본문에 넣을 수 없으면 보내지 않고 `product_create_request_unavailable`(새 사유)이다.
  - `id_field`가 없는 선언의 정본 바이트는 칸을 넣기 전과 같다(형식 판 그대로). 이미 저장한 선언은 바뀌지 않는다. 그래서 `id_path` 칸은 빈 값이어도 정본에 늘 실리고(공유 종류의 빈 `create` 포함, `id_field` 선언이면 빈 값), `id_field`만 있을 때 실린다.
  - **호환·배포 순서.** 지금 화면 파서(`web/src/workbench/live/declaration.ts`의 `parseStoredDeclaration`)는 `create` 칸을 정확히 검사하고 `id_path`를 필수로 본다. 그래서 서버만 먼저 `id_field` 선언을 저장·응답하면 화면이 그 선언을 깨진 응답으로 잠근다. 이를 막으려고 구현은 **한 dev 변경(PR 하나)**에 서버와 웹 파서를 같이 넣는다: 서버(선언 타입·`Validate`·`CredentialField`·본문 조립·요청 칸·쓰기 기록)와 웹(`CREATE_KEYS`를 `id_path` XOR `id_field`로, 저장 본문 `toRequestBody`·`draftFromStored` 보존, `validateDraft`의 둘 중 하나 규칙, `credentialFields`에 `create.idField`). 화면 입력 칸("ID 출처: 응답 경로 / 요청 본문 칸" 고르기와 칸 이름)은 이경준님이 뒤이어 붙인다. `id_path`만 쓰는 지금 선언은 응답 모양이 그대로라 어느 순서에서도 깨지지 않는다.
- **생성 본문은 확정 단계가 만든다(③(가) 확장: 표식을 만드는 쪽이 본문에도 넣는다).** 확정 단계가 본문 틀에 표식과(`id_field`면) ID를 넣어 본문을 만든다. json은 틀의 값·수 원문을 그대로 두고 칸을 더하고, form은 키를 더해 다시 인코딩한다. 만든 본문은 표식과 같은 방식으로 문자열·JSON·텍스트·바이너리·gob·slog 출력을 막은 `RequestBody`로 E²에 넘긴다(`CreateRequest.Body`). E²는 본문을 바꾸지 않고 `create.path`·`create.method`로 보낸다(Content-Type은 `body_kind`). 본문 속 표식은 그 회차에 등록한 보호 값이라 E²의 저널·영수증·승인 미리보기에서 가려진다.
- **E²에서 바뀌는 것(이동찬 확인).** 동찬님 쪽 변경을 최소로 두려고 E²는 아래만 맞춘다.
  1. 생성 본문을 조립하지 않고 받은 `RequestBody`를 그대로 보낸다(지금 계약의 "E²가 본문 틀의 표식 칸에 표식을 넣는다"를 대신함).
  2. `id_field`면 조회 경로 틀의 `{id}`에 `ReadRequest.ObjectID`를 넣고(단일 경로 조각이 아니면 거부), 생성 관찰의 `object_id_digest`는 응답 대신 `CreateRequest.ObjectID`로 만든다(digest 키는 그대로).
  3. 목적 규칙은 그대로다: 생성은 선언한 생성 경로, 조회는 경로 틀에 그 ID가 들어간 경로만 허용한다.
  4. 리셋 게이트의 잔존 점검이 `id_field` 쓰기 기록의 ID로 남은 객체를 조회한다(ID를 아는 객체가 늘어남).
- 조회 ID는 비어 있지 않은 단일 경로 조각이어야 한다. `/`·`;`·`?`·중괄호가 포함된 ID는 이 규칙으로 조회할 수 없다.
- 보내지 않은 것이 확실하면 `sent=false`와 사유(`approval_denied`·`approval_timeout`·`confirmation_budget_exhausted`·`policy_denied`)다. 보냈는지 모르면 오류를 돌려주거나 `sent=true, response_complete=false`다.
- 실행 권한이 철회됐으면 `ErrAuthorityRevoked`다(진위 실패).

## 5. 표식

- `Marker`는 소문자 영숫자 32자이고 확정 단계마다 새로 만든다. 값은 비공개 칸에만 있다. 문자열(`%v` 등 모든 동사)·slog로 내보내면 `[REDACTED]`가 되고, JSON·텍스트·바이너리·gob으로 내보내면 오류다. `Reveal()`은 E²가 본문에 넣고 응답을 검사하고 보호 값으로 등록할 때만 쓴다.
- **이 타입은 값 직렬화만 막는다.** 요청 본문에 들어간 표식은 일반 바이트라 저널·영수증·승인 미리보기(`input_preview`)와 그 저장 기록·로그·평가 내보내기로 샐 수 있다. 그래서:
  - 확정 단계를 시작할 때 `Transport.RegisterMarker`로 표식을 그 실행의 보호 값으로 올린다(`AddProtectedValues`와 같은 방식). 등록 뒤 E²는 그 기록들에서 표식의 원문과 지원 인코딩 모양(`MarkerForms`: 원문·대문자·JSON `\u`·URL `%XX`·HTML 10진·16진 엔티티, 대소문자 무시)을 가린다.
  - 등록이 실패하면 확정 단계를 시작하지 않는다(`start_refused`/`marker_registration_failed`). 등록하지 않은 표식으로 생성·조회를 부르면 E²는 보내지 않고 `ErrMarkerNotRegistered`다.
  - 확정 단계 한 회를 돈 뒤 저널·영수증·승인 미리보기·로그·평가 내보내기·결과 JSON 어디에도 표식이 없어야 한다. `LeakingSinks(marker, sinks)`가 이것을 확인한다. 실제 E² 구현은 실제 기록들에 대해 같은 확인을 통과해야 한다.
- **프로세스 경계.** E③와 E²는 지금 같은 서버 프로세스다(`server/e2_http_factory.go`의 `e2proxy.NewExecution`). 표식은 Go 값으로만 넘어가고 직렬화되지 않는다. 평가용 E² 서비스(`cmd/e2-eval-service`)는 별도 프로세스지만 확정 단계는 제품 경로에서만 돈다. 둘을 다른 프로세스로 나누면 그 전달 경로를 이 절에 적고 따로 시험한다.
- 다음 회차 보호(결정 §6.1)는 §8의 리셋 게이트가 맡는다.

## 6. 관찰과 저널 대조

`Observation`은 요청 하나의 구조화 관찰이다. URL·본문·표식·객체 ID·사용자 식별자 원문은 없다.

| 칸 | 규칙 |
|---|---|
| `marker.status` | `complete`·`partial`·`unsupported`·`failed`. 검사하지 못한 것을 "표식 없음"으로 적지 않는다 |
| `marker.presence` | `complete`일 때만 `present`·`absent`. 그 밖에는 비움 |
| `marker.method` | `present`일 때만 `exact`·`case_insensitive`·`decoded_json`·`decoded_url`·`decoded_html_numeric`·`decoded_html_named` |
| `marker.field` | 검사한 선언 칸. 선언의 `marker_field`와 다르면 진위 실패 |
| `body_digest_sha256`·`body_complete` | `complete`는 전체 본문일 때만이다. 부분 digest를 전체 근거로 쓰지 않는다 |
| `normal` | 선언한 상태 칸(없으면 E²의 HTTP 상태 판정)으로 본 정상 여부. 확정은 여전히 표식 일치가 필요하다 |
| `request_marker_free` | 조회·신원 요청에서 E²가 보낸 요청에 표식이 없음을 확인했는지 |
| `object_id_digest`·`subject_digest` | 생성·신원 관찰만. 실행별 키 digest |

- **저널 대조(① (다)).** `Transport`가 돌려준 관찰을 받을 때마다 `Journal.Sealed(binding, ref)`로 봉인된 저널 이벤트를 읽어 같은지 본다(시각은 순간으로 비교). `Sealed`는 저널과 따로 보관한 신뢰 체크포인트까지 검증한 스냅샷에서만 돌려준다([v3 계약](e3-e2-v3-journal-contract.md) §5). 검증된 스냅샷에 이벤트가 없으면 `journal_event_missing`, 저널을 검증하지 못하면(체크포인트 없음·불일치, MAC·체인·형식 오류, 그 밖의 읽기 오류) `journal_integrity_failed`, 다르면 `journal_event_mismatch`다. 셋 다 진위 실패이고 판정 행을 만들지 않는다.
- 같은 `CallRef`가 두 번 오면 `observation_reused`(진위 실패)다.
- `Journal`은 무결성 검증을 마친 이벤트만 돌려준다. E² 저널 v3의 새 칸도 무결성 범위에 든다(결정 §7).

## 7. 결과와 실패 처리

`RunResult`(형식 판 `e3-confirmation-run/v1`)의 `status`는 회차 상태·결과 화면·공식 회차 run-record에 그대로 싣는다.

| status | 언제 | 판정 행 | 요청 |
|---|---|---|---|
| `disabled` | 쓰는 조건(결정 §3)이 모자람: `not_wired`·`declaration_missing`·`binding_invalid`·`policy_kind_not_product`·`write_not_consented`·`test_accounts_unavailable`·`policy_window_invalid`·`no_confirmable_type` | 없음 | 0 |
| `start_refused` | 시작 점검에서 멈춤: `policy_source_unavailable`·`target_changed`·`reset_confirmation_missing`·`previous_markers_present`·`reset_state_unavailable`·`marker_generation_failed`·`marker_registration_failed` | 없음 | 0 |
| `completed` | 끝까지 돎 | 종류마다 하나(UNDECIDABLE 포함) | - |
| `interrupted` | 회차 중지로 끊김 | 끝낸 종류만 | - |
| `confirmation_integrity_failed` | 결속·진위가 깨짐(아래) | **없음** | 그때까지 |

**비활성 사유 구분(2026-11-18 사용자·이동찬 합의).** 공식 Juice Shop 측정은 E③ 비활성(선언·시험 계정 없음)으로 돈다. 그래서 "선언 없음"(`declaration_missing`)·"시험 계정 없음"(`test_accounts_unavailable`)과 "연결 미구현"(`not_wired`)을 다른 사유로 내보낸다. `not_wired`를 먼저 판정하므로 연결이 빠진 이미지는 선언이 없어도 `not_wired`로 드러나고, 최종 제품 이미지에서는 `not_wired`가 나오면 안 된다(판정 쪽 PR 시험). 결과의 `binding`은 검증을 통과한 실행 결속만 싣고, 결속이 틀려 거부했으면 생략한다. 후보 참조도 모양이 맞는 것만 옮긴다.

**진위 실패(② (다)).** 사유는 `policy_digest_mismatch`·`policy_target_mismatch`·`observation_invalid`·`binding_mismatch`·`observation_mismatch`·`observation_reused`·`journal_event_missing`·`journal_integrity_failed`·`journal_event_mismatch`·`authority_revoked`다.
- 그때까지 만든 판정(이미 CONFIRMED였던 종류 포함)을 버리고 판정 행을 만들지 않는다.
- 회차 상태와 결과 화면에 `confirmation_integrity_failed`와 사유를 그대로 보인다. "발견 없음"·"판정 없음"으로 보이면 안 된다.
- 그 회차 후보는 모두 `level=suspected`, `reason=confirmation_integrity_failed`이고 CONFIRMED로 올라가지 않는다. 후보의 검토 상태(`manual_review_required`)는 그대로다.
- 공식 회차에서 나오면 러너가 run-record에 `status`·`reason`을 옮겨 결과 보고에 보인다(O 공식 run-record v1.1, 러너 담당).

**증거 부족.** 판정 행은 만들고(값은 오라클 결과, 대개 UNDECIDABLE) 제품 사유 코드를 `product_reason_codes`에 따로 둔다. 오라클 사유(`oracle_reason_codes`)는 그대로다. 코드는 [#692 §9.1](workbench-finding-verdict-detail-proposal.md#91-제품-확정-경로-추가-시-undecidable-세부-사유와-추가-칸)의 9개에 셋을 더했다.

| 코드 | 뜻 | 뒤 요청 |
|---|---|---|
| `product_identity_unverified`·`product_subjects_identical` | 신원 확인 실패·A·B 같음 | 생성·조회 0 |
| `product_create_approval_denied`·`product_create_approval_timeout` | 사용자 승인 모드에서 생성 거절·만료 | 그 종류 0(다른 종류는 계속, 결정 §7) |
| `product_confirmation_budget_exhausted` | 확정 단계 전용 몫(요청·시간) 소진 | 전체 0, 끝낸 판정은 보존 |
| `product_owner_marker_unobserved` | A 조회에서 표식이 안 보임 | 그 종류 B 조회 0 |
| `product_data_partial_response`·`product_data_encoding_unsupported`·`product_data_check_failed` | 검사 못 함(부분·지원 밖·실패) | - |
| `product_request_result_unknown`(추가) | 보냈는지·결과를 모름 | 다시 보내지 않음 |
| `product_request_timeout`(추가) | 확정 요청 전용 시간 상한으로 끊김(결과 불명의 한 갈래, 상한 값 근거로 따로 셈) | 다시 보내지 않음. 생성이면 쓰기 기록(ID 모름) |
| `product_request_denied`(추가) | E² 정책이 프로그램 요청을 거부 | 그 종류 0 |
| `product_create_failed`(추가) | 생성 응답이 정상이 아니거나 객체 ID를 못 꺼냄 | 그 종류 0 |
| `product_data_check_contradiction`(추가, 2026-11-20) | B 조회가 거부 응답(401·403·404)인데 표식 검사가 `present` | - |
| `product_session_expired`(추가, 2026-11-20) | B 조회 거부 뒤 B 신원 재확인 응답도 401·403(세션 만료일 수 있음) | - (다시 로그인해 다시 조회하지 않음) |
| `product_cross_denial_unverified`(추가, 2026-11-20) | B 조회 거부 뒤 B 신원을 다시 확인하지 못함(보내지 못함·결과 불명·시간 초과·몫 끝·중지·처음과 다른 주체·그 밖의 비정상 응답) | - |

**B 조회 해석 순서(사용자 결정 2026-11-20).** B 조회 응답은 이 순서로 본다.
1. 결과 불명·시간 초과·보내지 않음은 위 사유 코드다.
2. 상태가 거부(401·403·404)면 표식 검사보다 상태를 먼저 본다.
   - `present`면 거부와 데이터 노출이 함께 나온 모순이라 판정하지 않는다(`product_data_check_contradiction`, UNDECIDABLE).
   - 표식 검사가 `present`가 아니면(`absent`·`failed`·`unsupported`·`partial`) 거부가 접근 통제 때문인지 세션 만료 때문인지 가리려고 B 신원을 한 번 더 확인한다(사용자 결정 2026-11-20 S21 (나), 확정 몫의 종류당 1건, 거부일 때만 씀).
     - 처음 신원과 같은 주체가 정상 응답하면(살아 있음) `cross_normal_data = no`로 오라클에 넘긴다. 오라클 사유는 `data_no_match`이고 판정 값은 UNDECIDABLE 그대로다.
     - 재확인 응답이 401·403이면 `product_session_expired`, 그 밖에 확인하지 못하면 `product_cross_denial_unverified`다. 둘 다 B 신원을 미확인(`evaluated_status = unknown`)·`cross_normal_data = unknown`으로 넘겨 UNDECIDABLE이다. 원인을 모르는 거부를 접근 거부(`data_no_match`)로 적지 않는다.
     - 만료여도 다시 로그인해서 다시 조회하지 않는다. 확정 직전에 이미 다시 로그인했으므로(§7.1) 확정 단계 몇 초 사이에 만료되는 것은 수명이 비정상적으로 짧은 대상뿐이고, 사유만 정확히 남긴다.
3. 그 밖의 상태(2xx 등)는 표식 검사가 `partial`·`unsupported`·`failed`면 위 사유 코드다(`product_data_check_failed` 등).
4. 표식 검사가 `complete`면 아래 데이터 사실로 오라클이 정한다.

A(소유) 조회는 이 순서를 쓰지 않는다. A 조회가 정상이 아니면 그대로 `product_data_check_failed`다.

**오라클 입력.** `Run`은 사실만 모으고 값은 `oracle.Evaluate`가 정한다.
- 정책: `owner_only` 선언 → B 기대 접근 거부, 공유·위임 없음. 동결본 대조를 통과한 뒤에만 `has_registered_source`·`hint_matches_canonical`이 참이다. 유효창은 대상 승인 기간과 실행 기간이 겹치는 구간(입력)이다.
- 신원: C14 인증 성공과 신원 확인이 둘 다 섰을 때만 `verified`이고, 참조는 실행별 키 digest다.
- 데이터:
  - `same_resource`: 두 조회가 같은 생성 관찰을 읽음.
  - `owner_normal_data`·`cross_normal_data`: A·B 조회의 정상 여부. B가 거부 응답(401·403·404)이고 B 신원 재확인에서 살아 있으면 `cross_normal_data`는 no, 재확인을 못 했거나 만료면 unknown이다.
  - `nontrivial_data_match`: B 검사 `complete`에서 `present`면 yes, `absent`면 no.
  - `reflection_excluded`: B 요청에 표식이 없음을 E²가 확인했을 때만 yes.
  - `template_excluded`: 이 회차에 새로 만든 무작위 표식을 A 객체 한 칸에만 넣었으므로 yes.
  - 확인하지 못한 사실은 모두 unknown이다.

### 7.1 확정 단계 몫과 꺼진 회차(2026-11-18 사용자 결정)

- 확정 단계 몫(요청·시간)은 실행 시작 때 정하고, 모델 단계는 그 몫을 뺀 범위에서 돈다(결정 §7 예산).
- **확정 단계가 꺼질 회차는 몫이 0이다.** 실행 시작 때 쓰는 조건(결정 §3)이 모자라면 꺼진다: 유효한 선언 동결본이 없음, 시험 계정 A·B 인증이 없음, 쓰기 동의가 없음, 제품 경로가 아닌 정책 출처(`kg_snapshot`). 이런 회차는 요청 몫과 시간 몫을 모두 0으로 두고 모델 단계가 실행 예산 전부를 쓴다. 꺼졌는데 몫만 떼면 모델 몫이 이유 없이 줄어든다.
- 몫은 E² 실행 정책 `limits.e3_confirmation_requests`·`limits.e3_confirmation_timeout_ms`에 명시한다(0 포함, 둘이 같이). 칸이 있으면 0도 정책 내용 지문에 들어가 "칸 없음"과 구분된다. 러너는 이 두 칸으로 꺼진 회차의 몫이 0인지 확인한다.
- 몫 계산(사용자 결정 2026-11-18 ②, 2026-11-20 (가)·S21 (나)): 요청 몫 = 고정 4(확정 직전 다시 로그인 A·B 2 + 신원 확인 2) + 종류×4(생성 1 + A 조회 1 + B 조회 1 + B 거부 뒤 신원 재확인 1), 시간 몫 = 요청 몫 × 확정 요청 전용 시간 상한. 요청·시간 모두 실행 상한의 1/4 안에 드는 만큼만 종류를 받는다(`Input.AdmittedTypes`). 넘치는 종류는 요청을 보내지 않고 UNDECIDABLE(`product_confirmation_budget_exhausted`)이다. 하나도 들지 않으면 몫은 0이고 표식·신원 요청도 보내지 않는다.
- **몫 집행(2026-11-20).** 확정 몫은 확정 단계 요청만 쓴다: E③ 프로그램 요청(신원·생성·조회)과 확정 단계가 연 다시 로그인 호출(`e2proxy.BeginE3ReloginCall`). 그 밖의 요청(모델 단계 Worker, 인증 준비, Worker `refresh_session`)은 확정 단계 전이든 뒤든 대상 예산의 마지막 확정 몫만큼을 쓰지 못한다(`budget_exhausted`). 몫을 "여는" 단계가 없으므로, 확정 단계가 돌기 시작한 뒤 늦게 도착한 모델 단계 요청(취소 중인 Worker·재시도)도 몫을 쓰지 못해 예산 통제를 우회할 수 없다. 남은 예산 조회(`BudgetRemaining`)도 확정 단계가 아직 쓰지 않은 몫을 빼서, 모델 단계는 확정 몫을 남긴 채 예산 소진(`budget_limited`)으로 마감한다. 확정 단계 요청이 몫을 쓸 때마다 떼어 둘 몫이 줄고, 몫을 다 쓰면 확정 단계 요청도 막힌다. 몫이 0인 회차(꺼짐)는 아무 것도 떼지 않고 확정 단계 다시 로그인도 보낼 몫이 없다.
- **확정 직전 다시 로그인(사용자 결정 2026-11-20 (가)).** 몫에 든 종류가 있고 시작 검사를 통과하면, 표식을 등록한 뒤 신원 확인 직전에 A·B를 한 번씩 다시 로그인시킨다(`Deps.Relogin`). 경로는 Worker의 `refresh_session`과 같고(E② 인증 준비 규칙, 같은 계정 줄 세우기, 계정별 연속 실패 3회·간격 10초 상한) 확정 몫을 쓰는 호출로 연다. 보낸 다시 로그인은 확정 몫으로 센다. 실패하거나 보내지 못해도(간격 상한 등) 멈추지 않고, 바로 뒤의 신원 확인이 판정 근거를 정한다(만료면 `product_identity_unverified`). 모델 단계가 길어 대상이 준 세션이 만료돼도 확정 단계가 새 세션으로 돌게 하려는 것이다. 꺼진 회차·몫에 든 종류가 없는 회차는 다시 로그인하지 않는다.
- 확정 요청 전용 시간 상한(`Input.RequestTimeout`, 요청마다 `Timeout`)은 E² 요청 시간 상한과 따로다. 기본 10초이고, VAmPI 파일럿에서 확정 요청의 실제 시간을 잰 뒤 정한다. 상한이 없거나 몫이 음수면 `confirmation_share_unavailable`로 꺼진다.
- 사용자 승인 모드에서 생성 요청의 운영자 승인 대기는 이 상한에 넣지 않는다(사용자 결정 2026-11-21). 대기는 승인 기한(E② 호출 기한)과 확정 단계 시간 몫 안에서만 하고, 상한은 승인을 받은 뒤 송신부터 센다. 결정 없이 기한에 닿으면 보내지 않고 `approval_timeout`(`product_create_approval_timeout`)이다. 자동 모드와 확정 단계가 꺼진 회차는 그대로다.
- 확정 요청(신원·생성·조회)이 이 상한으로 끊기면 결과 불명이다(`TimedOut`). 다시 보내지 않고 판정은 UNDECIDABLE(`product_request_timeout`)이다. 신원 요청이 끊기면 그 대상의 모든 종류가 이 사유이고 생성·조회는 보내지 않는다(다른 계정이 다른 이유로 실패해도 시간 초과를 남긴다).
- 시험: "disabled → 예약 0"(실행 정책 칸·지문), 상한 전달, 시간 초과, 몫 밖 종류, 몫 0.

### 7.2 쓰기 기록

- `RunResult.Writes`는 이 회차 생성 요청이 대상에 남겼을 수 있는 쓰기다. 보낸(또는 보냈을 수 있는) 생성마다 하나이고, 보내지 않은 것이 확실한 생성(NotSent)은 넣지 않는다.
- 객체 ID를 꺼낸 정상 응답만 `created`(ID digest 있음)이고, 시간 초과·결과 불명·비정상 응답·ID 없음은 `unknown`이다. 객체가 생겼다고 본다. `id_field`면 보내기 전에 ID를 알므로 결과와 관계없이 `object_id`(그 ID 원문)가 실린다. 쓰기 기록 예약(`WriteRecorder.Reserve(…, objectID)`)부터 그 ID와 함께 남고, 정산은 같은 ID로만 받는다.
- 상태와 관계없이 싣는다. 진위 실패로 판정을 버려도 대상에는 이미 쓰기가 있었다.
- 서버는 이것으로 대상의 쓰기 기록(`confirmation_writes`, ID 모름은 `id_unknown`)을 남긴다. 그 대상은 운영자 리셋 확인 전까지 다음 회차 확정 단계를 시작하지 않는다(§8). 잔존 점검은 ID를 아는 객체만 읽는다(`id_field` 쓰기는 결과 불명이어도 ID를 안다). ID 모르는 객체(`id_path` 대상의 결과 불명)는 리셋 확인에만 기댄다(표식이 회차마다 새 난수라 남은 객체가 다음 판정을 확정으로 만들지 못한다).

### 7.3 마감 경로와 확정 단계(2026-11-20 사용자 결정)

기본 규칙: 모델 단계가 **스스로 끝난 경우**(목표 달성·시간 상한·예산 소진 등 실행 상한)는 확정 단계가 돌고, **강제로 멈춘 경우**(중지·취소·일시정지·실패)는 확정 단계가 돌지 않고 중지 기록이 `interrupted`(비활성 근거가 있으면 그 사유)다.

E②를 닫는 순서는 이렇다(사유별로 E②를 닫지 않는다).
- 엔진 취소(`cancelExec`, 사유 무관)는 E② 실행을 닫지 않고 **모델 단계만 마감**한다(`CloseModelStage`). 그 뒤 E②는 확정 단계 요청(E③ 프로그램 요청·확정 단계가 연 다시 로그인)만 받고, 진행 중 모델 단계 요청은 끊으며 새 모델 단계 호출·인증 준비·Worker `refresh_session`은 열지 않는다.
- 자연 마감은 감독자가 모델 단계를 마감하고(`CloseModelStage`) → 확정 단계 훅 → **감독자가 E② 실행을 닫는다**(`FenceTask`) → drain·감사 마감 순이다.
- 강제 마감(운영자 중지·초안 중지·대상 철회·결속 계정 변경·문맥 취소·실패)은 그 중지 경로가 E② 실행을 끝내고(요청 승인 중지·작업 문맥 취소), 감독자는 중지 기록만 남긴다.

| 마감 경로 | 기록 결과 | 모델 단계 마감 | E② 실행 닫기 | 확정 단계 |
|---|---|---|---|---|
| 목표 달성(Planner met·goalless done) | completed | 엔진 목표 달성 취소 또는 감독자 마감 판정 | 확정 단계 뒤 감독자 | 돈다 |
| 시간 상한(유예 안에 끝남) | completed, budget_limited | 감독자 마감 판정 | 확정 단계 뒤 감독자 | 돈다 |
| 시간 상한 유예 초과 | completed, budget_limited | 유예 끝 취소(`time_budget_drain_timeout`) — 진행 중 모델 요청 끊김 | 확정 단계 뒤 감독자 | 돈다 |
| 예산 소진·실행 상한(요청·도구·계획 라운드·모델 예산) | completed, budget_limited | 감독자 마감 판정 | 확정 단계 뒤 감독자 | 돈다 |
| 자연 마감인데 E② 실행이 한 번도 없음(no_intents 등). 흐름 모드도 마지막 계획이 오류 없이 끝났고 의도가 0이면 이 줄이다(2026-11-22) | completed, no_intents 등 | — | — | 요청 없이 기록: 꺼진 회차는 실제 사유, 켜진 회차는 후보와 관계없이 `start_refused`/`e2_execution_unavailable`(2026-11-22 F12. 인증 준비를 하지 않았으니 계정 인증 결과 없음으로 끄지 않는다. `no_confirmation_candidates`는 어휘에 남아 있지만 지금 만들지 않는다) |
| 흐름 모드에서 계획 호출이 실패해 의도가 0(진단 Worker가 한 번도 없음) | failed(감독 실패, 진단 로그 사유 `closure_workflow_not_ready`) | 취소 | 마감 정리 | 중지 기록(E② 실행이 없으면 `start_refused`/`e2_execution_unavailable`). 모델 고장을 '시도한 계획 없음'으로 숨기지 않는다 |
| 운영자 중지(요청 승인 중지 API) | cancelled | 중지 | 중지 경로 | 중지 기록 `interrupted`. 단 첫 E② 실행 전에 중지됐으면(lease_missing) `start_refused`/`e2_execution_unavailable`이다. 회차가 운영자 중지라는 것은 회차 상태(cancelled)에 남는다 |
| 운영자 일시정지(작업 control pause) | cancelled(운영자 중지와 같음, 엔진 일시정지를 거치지 않음) | 중지 | 중지 경로 | 중지 기록 `interrupted`. 재개는 409 `resume_unsupported`. 화면의 일시정지·다시 시작은 아래에 설명한 워크플로 API를 사용한다. |
| 회차 도중 운영자 밖 엔진 일시정지(아래 원인 표) | failed(감독 실패, 진단 로그 사유 `engine_paused`·서버 로그에 원인 코드) — 운영자 중지와 따로 | 취소 | 마감 정리 | 중지 기록 `interrupted`. 첫 진단 서버에서 생기는 경로는 없다(방어 경로) |
| 초안 중지·대상 철회·결속 계정 변경·hard 기한·서비스 종료 | cancelled(사유는 각 경로) | 문맥 취소 | 작업 문맥 취소 | 중지 기록 `interrupted` |
| 실패(엔진 실패·마감 판정 실패·준비 실패) | failed | 취소 | 마감 정리 | 중지 기록 `interrupted` |
| 마감 판정 뒤 실패(감사 마감 실패 등) | failed | 감독자 마감 판정 | 확정 단계 뒤 감독자 | 이미 돌았다 |

- **엔진 일시정지 원인 표**(`Engine.Pause`를 부르는 곳 전부). 첫 진단 서버(E② HTTP)에서 회차 도중 생기는 것은 운영자 일시정지뿐이고, 그것은 엔진 일시정지가 아니라 운영자 중지로 받는다.

  | 원인 코드 | 부르는 곳 | 첫 진단 회차 도중 | 막는 것(시험) |
  |---|---|---|---|
  | `paused_by_user` | 작업 control pause·일괄 control(`applyTaskControl`) | 단일 control은 운영자 중지로 바뀜, 일괄은 없음 | 단일 control → `firstDiagnosisControl`(통합 pause 모드), 일괄 라우트 403(`TestFirstDiagnosisClosesEnginePauseEntries`) |
  | `llm_unavailable_queued` | 동시 실행 조정(`reconcileConcurrency`) | 없음 | 동시 실행 제한이 꺼져 있어야 동작하지 않음: 설정 라우트 403, 설치·러너가 쓰지 않음, 실제 서버에서 꺼짐 확인(통합 engine-pause 모드). 스케줄러는 자동 허가 끈 서버에서 시작 안 함 |
  | `queued_for_admission` | 재개·재실행 허가(`admitTask`) | 없음 | 첫 진단 허가는 회차 시작 때 한 번(이미 준비 일시정지 상태), 재개 라우트 403·control 재개 409 |
  | `paused_by_orchestrator` | 통제 조회 대기(`parkControlledReadTask`), 오케스트레이터 `pause_task` 도구 | 없음 | E② HTTP 서버는 통제 조회 런타임이 없음(통합 engine-pause 모드), 진단 프로필에 `pause_task` 없음(`TestDiagnosisProfilesHaveNoTaskPauseTool`), 대화 라우트 403 |
  | `paused_on_reload` | 재시작·보관 복원 | 없음(재시작은 회차를 끝냄) | 첫 진단 기동은 일시정지 표시만(엔진 `Pause` 아님), 보관 복원 라우트 403 |
  | `task_deleted`(삭제 장벽) | `BeginDelete` | 없음 | 삭제 라우트 403 |
  | `paused_race_guard` | 감독자 자신(준비·마감·종료) | 회차 마감 뒤에만 | 감독자는 실행 문맥 취소를 엔진 일시정지보다 먼저 본다(서버 종료 때 경쟁 방지 일시정지가 `engine_paused`로 잘못 기록되지 않게) |

  모든 통합 마감 경로에서 회차가 `engine_paused`로 끝나지 않음을 고정한다(engine-pause 모드만 일부러 건다). LLM 준비 판정(`ReadyFor`)은 첫 진단에서 모델 결속·프로필 설정만 보고 제공자 오류(429·5xx·끊김)로 바뀌지 않는다. 바뀌어도 엔진은 일시정지하지 않고 Planner·Worker가 모델 없이 기다린다.
- 워크플로 일시정지(`/api/product/workflows/{job}/pause`)는 마감 경로가 아니라 모델 요청 관문이다. 그 상태로 시간 상한에 닿으면 시간 상한 행과 같고, 관문은 모델 요청에만 걸려 확정 단계 요청은 지난다. 화면의 일시정지 · 다시 시작 버튼은 이 흐름 API만 쓴다(2026-11-24 사용자 결정, [개발 계획](../development-plan.md) 2단계 전 항목 — 11/20 '종료만' 결정을 바꿈). 점검 제어 API(`/api/tasks/{id}/control`)의 일시정지(=회차 중지)는 화면이 부르지 않는다. 일시정지 중에도 회차 시간 상한은 흐르므로 화면 안내는 시간이 멈춘다고 쓰지 않는다.
- 시험: 감독자 통합 시험(`firstDiagnosisCheckClosingPath`)이 모든 모드에서 확정 단계 훅과 중지 기록 가운데 정확히 하나가 한 번 불리고, 확정 단계 훅 때 E② 연결이 살아 있고 모델 단계가 마감돼 있거나(사유 없음) 실행이 한 번도 없었는지(`lease_missing`), 그 뒤 모델 단계 송신이 0인지 본다.
- `RunResult.Requests`는 "보낸 요청"이 아니라 확정 단계가 **보내려고 시도한 수**다(E②가 몫 끝·정책으로 보내지 않은 것 포함, 다시 로그인은 실제로 보낸 것만).

### 7.4 회차 상태 응답(2026-11-27)

화면이 판정 목록을 따로 부르지 않고도 회차 상태에서 확정 단계 결과를 읽게, 제품 설치의 URL v2 회차 상태 응답(`GET /api/diagnoses/v2/drafts/{id}/status`) 최상위에 `confirmation`을 싣는다. 값은 판정 목록 `policy_source`와 **같은 판독기**(`readConfirmationState`, `runs/<job>/e3-confirmation.json`)에서 온다. 두 조회가 같은 파일 상태를 읽고 판정 목록 조회도 성공하면 상태·사유가 같다. 파일 작성 중이나 두 조회 사이에 결과가 기록되면 값이 일시적으로 다를 수 있다. 판정 목록은 정책 동결본을 먼저 읽으므로 동결본을 읽지 못하면 확정 단계 상태도 반환하지 않는다. 회차 소유는 그 Status가 이미 확인한다(운영자 주체·자기 회차만). 판독기가 하는 검사(형식 판본·상태·사유·건너뛴 종류 모양·0600 등)보다 약하게 읽지 않는다.

```json
{"version":"diagnosis-api.v2","owner_context":"…","draft":{"state":"completed","…":"…"},
 "confirmation":{"record":"recorded","status":"confirmation_integrity_failed","reason":"journal_integrity_failed"}}
```

| `record` | `status` | `reason` | 뜻 | 판정 목록 `confirmation` |
|---|---|---|---|---|
| `recorded` | 위 표의 5개 중 하나 | 사유 코드 또는 `null` | 결과 파일을 읽음 | `{status, reason}` 같은 값 |
| `absent` | `null` | `null` | 결과 파일이 없음. 실행 중이면 아직 기록 전, 끝난 회차면 확정 단계까지 가지 않았거나 이 기록이 생기기 전 회차 | `null` |
| `unreadable` | `null` | `null` | 파일은 있는데 못 읽음·형식·값이 계약 밖 | `status: "unreadable"` |

- `absent`를 `disabled`(꺼짐)로 추정하지 않는다. `unreadable`은 조회 상태이지 확정 단계 결과 상태가 아니다(그래서 `record`로 나눴다).
- 회차 `draft.state`의 `completed`와 `confirmation.status`의 `completed`는 다른 칸이다. 확정 단계 `completed`는 취약점 확정이 아니고, 판정 결과는 판정 목록·상세가 말한다. `confirmation_integrity_failed`는 사유와 함께 그대로 보이고 "발견 없음"으로 보이면 안 된다(§7).
- 칸이 없으면 제품 설치가 아니거나 이전 서버다. Status 밖의 응답(시작·중지·검증·조회)에는 없다. 보호 IPC 검사는 `confirmation`을 Status의 회차 응답에만, 계약 모양으로만 받는다.
- 공유 예시 `run-confirmation.status.example.json`(실행 중 기록 전·다섯 상태·못 읽음·제품 아님)을 Go handler와 웹 파서(`parseURLConfirmation`)가 함께 확인한다.

## 8. 리셋 게이트

`ResetGate.Check`는 `{previous_writes, reset_confirmed, residue}`를 돌려준다(결정 §6.1). `CheckRunStart`는 다음 경우 모두 거절한다:
- 읽기 실패, 또는 오류와 함께 온 상태
- `residue`가 `present`면 `previous_markers_present`, `unknown`(점검이 있는데 모름·실패)이면 `reset_state_unavailable`. `absent`와 `not_checked`(잔존 점검이 없음, 2026-11-20 사용자 결정)는 시작한다
- 지난 쓰기 뒤 리셋 확인 기록이 없음

서버는 회차 시작 때(모델 단계 전) 이것으로 회차를 거절하고, `Run`은 확정 단계 시작 때 다시 본다. 리셋 확인이 없으면 잔존 점검 결과와 관계없이 `reset_confirmation_missing`이다.

**기록과 API(구현).** 선언 저널에 두 종류의 줄을 더 남긴다.
- `confirmation_writes`: 확정 단계가 대상에 만든 객체(종류·ID digest, 원문 없음)와 회차. 행위자는 프로그램(`e3-confirmation`)이다. 한 회차는 쓰기 기록 줄을 하나만 남긴다(같은 회차의 두 번째 줄은 받지 않는다). 새 쓰기는 앞선 리셋 확인을 덮는다. 줄마다 고유 참조(`writes_ref` = 회차·시각·객체 집합의 SHA-256)가 있다.
- `reset_confirmed`: 대상 승인자가 리셋 진술에 동의함(진술 문장의 SHA-256·누가·언제·동의한 쓰기 기록의 회차와 고유 참조). 동의는 운영자가 본 쓰기 기록 줄에만 묶인다.
- `GET /api/product/targets/{id}/reset-confirmation` → `{"reset": {"reset_required", "last_writes_job"?, "last_writes_at"?, "last_writes_objects"?, "last_writes_ref"?, "confirmed_by"?, "confirmed_at"?, "statement"}}`. 화면은 `statement`를 그대로 보이고 동의를 받는다.
- `POST …/reset-confirmation` `{"statement_confirmed": true, "writes_ref": "<GET에서 본 last_writes_ref>"}` → 같은 모양.
  - 진술 동의가 없으면 400 `statement_required`, `writes_ref`가 없거나 모양(소문자 16진 64자)이 틀리면 400 `invalid_reset_request`다.
  - 조회와 동의 사이에 새 쓰기 기록이 들어와 지금 기록이 본 것과 다르면(또는 기록이 없으면) 409 `reset_record_changed`이고 반영하지 않는다. 같은 기록을 이미 확인했으면 줄을 더 남기지 않는다.
  - 리셋은 더 안전한 쪽이라 대상 승인 상태와 관계없이 받는다.
- 게이트는 지난 쓰기가 있으면 리셋 확인과 별개로 지난 객체를 읽어 표식 잔존을 본다(결정 §6.1 (b), E² 쪽 점검). 점검이 실패하면 잔존은 `unknown`이라 시작하지 않는다.
- **잔존 점검은 아직 없다(미구현 한계, 2026-11-20 사용자 결정).** 점검이 연결되기 전에는 잔존이 `not_checked`이고, ID를 아는 객체도 ID 모르는 객체와 같이 운영자 리셋 확인만으로 다음 회차를 시작한다(§12 근거). 그러지 않으면 같은 대상을 두 번째 회차부터 점검할 수 없다. 점검(`previous_markers_present`)은 측정 뒤 개선으로 E② 조회(이동찬)와 함께 연결한다.

**감독자 연결(구현).**
- 회차 시작: 실행 준비가 회차를 대상에 묶은 직후(모델 단계 전) 지금 대상 판에 유효한 선언이 있는 대상만 `CheckRunStart`로 본다. 거절이면 회차는 `failed`이고 `reason_code`가 거절 사유(`reset_confirmation_missing`·`previous_markers_present`·`reset_state_unavailable`)다. 거절한 회차도 회차 디렉터리에 확정 단계 결과 파일(`start_refused`, 그 사유, 요청 0, 결속·판정·후보·쓰기 없음)을 남긴다(2026-11-20, 화면·집계가 거절 사유를 보게). 선언이 없는 대상은 게이트가 없다. 잔존 점검이 없는 동안은 리셋 확인만으로 시작한다(위).
- 확정 단계: 일반 HTTP 감독자가 모델 활동이 끝난 뒤(진행 중 작업·모델 호출 0, 실행 허가 닫힘) E② 마감 전에 확정 단계를 한 번 부른다. 등록 GET(평가) 경로는 부르지 않는다. 확정 단계가 실제로 대상 요청을 시작할 때만(`Deps.Started`, 표식을 만들기 직전) 엔진을 정리 상태로 두고(새 계획·intent 없음) 진단 로그에 `e3_confirmation` 단계를 남긴다. 꺼진 회차(선언 없음·연결 없음·계정 없음·시작 거절·몫에 든 종류 없음)는 감독자 흐름(사건·단계·상태 전이 순서)이 E③가 없는 실행과 같다(통합 시험으로 고정). 확정 단계가 실패해도 모델 단계 실행 결과는 그대로다.
- 중지 기록: 확정 단계를 부르지 못하고 실행이 끝나면(사용자 중지·실행 문맥 취소·실패·시간 초과 상태·준비 거부) 감독자가 중지 기록 훅을 한 번 부른다(#738 이동찬 리뷰). 요청 없이(`e3contract.RunStopped`) 비활성 근거가 있으면 `disabled`와 그 사유, 동결본을 읽지 못하는 등 비활성인지 확인할 수 없으면 `start_refused`(사유), 비활성 근거가 없어 돌 수 있었으면 `interrupted`(사유 없음)를 같은 결과 파일에 쓴다. 단 그 회차가 E② 실행을 한 번도 잡지 않았으면(연결 사유 lease_missing: 중지·실패·취소 어느 쪽이든) `e3contract.RunWithoutExecution`으로 `start_refused`/`e2_execution_unavailable`을 쓴다(2026-11-22 F12). 계정 인증 결과가 없어도 `test_accounts_unavailable`로 가리지 않는다. 그래서 시간 상한·중지로 끝난 회차도 결과 파일이 있고, 파일이 없으면 비활성으로 추정하지 않는다.
- 쓰기 기록은 **기록하고 나서 보낸다**(사용자 결정 2026-11-19). 생성 요청마다 보내기 전에 그 종류를 ID 모름으로 대상 선언 저널에 먼저 남기고(`confirmation_writes` 줄, `e3contract.WriteRecorder.Reserve`), 남기지 못하면 보내지 않고 그 종류는 UNDECIDABLE(`product_write_record_unavailable`)이다. 응답을 받으면 갱신한다(`confirmation_write_settled` 줄): created면 객체 ID digest를 채우고, 보내지 않은 것이 확실하면 그 쓰기를 거둔다(`not_sent`, 남은 쓰기로 세지 않음). 거두는 것은 E²가 대상으로 연결을 시도하기 전에 거부한 경우(sent=false와 §4의 연결 전 거부 사유 넷)와 회차가 멈춰 생성 요청을 부르지 않은 경우뿐이다. 연결 뒤 실패·결과 불명(E② C02의 Unknown·TransportFailed 등)은 오류나 sent=true 미완료로 돌려줘야 하고 기록은 ID 모름으로 남는다(그 밖의 사유로 sent=false를 주면 관찰 계약 위반으로 진위 실패). 결과 불명이면 ID 모름 그대로다. 갱신이 실패해도 먼저 남긴 기록이 리셋 확인 전까지 다음 회차를 막는다. 지금 쓰기 기록의 회차만 줄을 더 남길 수 있고(지나간 회차는 받지 않음), 기록이 바뀌면 참조(`last_writes_ref`)가 바뀌어 바뀌기 전 기록을 본 리셋 동의는 반영되지 않는다.
- 결과: `RunResult`를 `runs/<job>/e3-confirmation.json`(0600, 한 번만)에 쓴다. 실행 결속의 `run_ref`·`e2_policy_ref`·확정 몫은 승인에 결속된 실행 정책에서 읽고, `generation_ref`·`instance_ref`는 실행 시작 때 서버가 확인한 대상 상태로 발급해 `runs/<job>/e3-target-refs.json`(0600, 한 번만)에 동결한 값이다([v3 계약](e3-e2-v3-journal-contract.md) §6). 세대는 대상 ID·origin·승인 판·승인 시각과 마지막 확정 쓰기 기록·리셋 확인, 그리고 그 회차(job)에서, 인스턴스는 세대와 실행이 고정한 접속 주소(고른 IP·주소 집합·포트)·TLS 공개 키(https)에서 나온다(회차 사이에는 재사용하지 않음). 입력을 하나라도 확인할 수 없으면 실행마다 새 난수 참조(`gen-x-`·`inst-x-`)를 쓴다(모르면 새로 발급). 확정 단계를 시작할 때 리셋 게이트가 지금 대상으로 다시 계산해, 동결값과 다르거나 지금 상태를 확인할 수 없으면 `ResetState.TargetChanged`로 요청 없이 `start_refused`·`target_changed`다. 파일이 없거나 깨졌으면 결속이 비어 `disabled`·`binding_invalid`다. 대상 리셋·인스턴스 교체를 서버가 무엇으로 아는지는 [세대·인스턴스 참조 규칙안](../e3-target-refs-rules.md)(이동찬 리뷰)이다. A·B는 지금 구현에서 인증 준비가 `authenticated`인 시험 계정의 이름순 앞 둘이다. 이 배정 규칙은 판정 의미에 닿아 아직 확정하지 않았다(열린 결정, E② Transport 연결 전에 정한다). 판정 레코드 저장은 다음 단계다.

## 9. 후보 연결

후보(메서드·E² 시도 경로 패턴, `pathmask.Readable` 모양)를 시험한 종류의 조회 경로 틀과 조각마다 맞춘다. 가린 조각(`{…}`)은 가린 조각끼리만 맞는다. GET이고 정확히 한 종류와 맞을 때만 잇고, 0개면 `link_none`, 둘 이상이면 `link_ambiguous`다. 이어진 판정이 CONFIRMED일 때만 후보 `level`이 `confirmed`이고, 그 밖에는 `suspected`다. 판정에는 검증된 연결의 후보 참조만 들어간다(`candidate_refs`).

## 10. 계약 시험

`e3contract/run_test.go`·`types_test.go`. 실패 상태마다 판정·후보에 CONFIRMED가 없음을 공통으로 단언한다. 양성 대조로 정상 흐름은 CONFIRMED가 나와야 한다(아래 시험들이 공허하지 않게).

| 시험 | 고정하는 것 |
|---|---|
| `TestRunConfirmsWhenCrossSeesMarker` | 양성 대조. 판정 값이 같은 입력의 `oracle.Evaluate`와 같음, 공유 종류 요청 0, 후보 연결 |
| `TestRunDisabledSendsNothing`·`TestRunProductRejectsKGSnapshot` | 선언·시험 계정·쓰기 동의·연결이 없거나 `kg_snapshot`이면 요청 0 |
| `TestRunNotImplementedTransportDoesNotStart` | 미구현 E²(`ErrNotImplemented`)면 시작 거절, 요청 0 |
| `TestRunBindingMismatchIsIntegrityFailure` | 결속 칸 10개 각각, 앞서 CONFIRMED였던 종류도 버림 |
| `TestRunObservationMismatchIsIntegrityFailure`·`TestRunInvalidObservationIsIntegrityFailure` | 계정·역할·종류·생성 참조·검사 칸 불일치, "complete 아닌데 표식 없음" 등 모양 위반 |
| `TestRunJournalCrossCheck` | 저널 이벤트 없음·다름(표식 유무·시각·digest)·참조 재사용·권한 철회 |
| `TestRunPolicySourceChecks` | 동결본 digest·대상 판·대상 불일치는 진위 실패, 읽기 실패는 시작 거절 |
| `TestRunResetGate` | 리셋 확인 없음·표식 잔존·잔존 모름·게이트 오류는 시작 거절 |
| `TestRunIdentityFailureStopsWrites` | 신원 실패·A·B 같음이면 생성·조회 0 |
| `TestRunApprovalDenialStopsThatType` | 승인 거절·만료면 그 종류 UNDECIDABLE·이후 그 종류 요청 0 |
| `TestRunBudgetExhaustionStopsAllRequests` | 몫 소진(요청·시간)이면 이후 요청 0, 끝낸 판정 보존 |
| `TestRunCreateUnknownIsNotResent` | 결과 불명·생성 실패·정책 거부는 다시 보내지 않음 |
| `TestRunMarkerCheckNotCompleteIsUndecidable`·`TestRunCrossBlockedOrAbsentIsNotConfirmed` | 검사 상태별 사유, A 미관찰이면 B 조회 0, 차단·반사는 확정 아님 |
| `TestRunLeavesNoMarker`·`TestLeakingSinksFindsEveryForm`·`TestMarkerNeverSerializes` | 표식이 결과·로그·가짜 E² 기록에 없음, 찾기 함수가 모든 모양을 찾음, 직렬화 막힘 |

변이 60개를 모두 잡는다(가드마다 하나, 2026-11-18).

## 11. 갈래별 다음 일

- **E② 연동(이동찬)**: `Transport`·`Journal`의 실제 구현. 목적 규칙 셋, E² 안 표식 검사와 정규화기, 영수증·저널 v3, 요청별 계정 라벨, 표식 보호 값 등록. 실제 구현으로 §10 시험(특히 저널 대조·표식 누출 확인)을 다시 돌린다.
- **판정(최민준)**: 정책 선언 저장·동결과 `DeclarationReader`·`ResetGate` 실제 구현, 서버의 확정 몫 예약·모델 단계 뒤 호출, 판정 레코드 저장(정책 출처·후보 참조·제품 사유), #599 상세 API.
- **화면(이경준·윤재상)**: `status`·`reason`과 후보 `level`·`reason` 표시. `confirmation_integrity_failed`를 "발견 없음"으로 보이지 않게 한다. 정책 선언 입력 화면은 §3 칸을 쓴다.
- **러너**: 공식 회차 run-record에 확정 단계 `status`·`reason`을 싣는다.

## 12. 결정문과 다르게 읽힐 수 있는 곳

- 사용자 승인 모드의 생성 거절·만료는 결정 §7대로 **그 종류만** 멈춘다. 다른 종류는 계속 시험한다(2026-11-18 사용자 확인). 확정 단계 전체를 멈추려면 운영자가 기존 회차 중지를 쓰고, 그때 상태는 `interrupted`다.
- 신원 확인 선언은 대상마다 하나다(결정 §4.1 표는 종류마다 칸에 두었지만 §5.1은 대상 단위로 확인한다).
- `shared` 종류는 판정 행 없이 `skipped`로 보인다(결정 §4.1의 "UNDECIDABLE"을 판정 행으로 꾸미지 않음).
- 확정 단계 시간 몫은 `Run`에 넘기는 문맥의 시한이다. 시한이 지나면 몫 소진과 같게 다룬다.
- 결정 §6.1은 리셋 확인 뒤에도 지난 객체를 읽어 표식 잔존을 보게 한다. 생성 요청이 시간 초과·결과 불명이라 **ID를 모르는 객체**는 읽을 수 없으므로 잔존 점검에서 빼고, 운영자 리셋 확인만으로 다음 회차 확정 단계를 연다(2026-11-18 사용자 확인). 근거: 표식은 회차마다 새로 만든 무작위 값이라, 지난 회차 객체가 대상에 남아 있어도 그 안의 표식은 다음 회차 표식과 다르다. 따라서 남은 객체가 다음 회차 교차 조회에서 표식 일치를 만들 수 없고, 다음 판정을 CONFIRMED로 만들 수 없다. ID를 아는 객체는 잔존 점검이 연결되면 그것을 거친다(§7.2·§8). 점검이 없는 동안은 ID를 아는 객체도 같은 근거로 리셋 확인만으로 다음 회차를 연다(2026-11-20 사용자 결정, §8).
