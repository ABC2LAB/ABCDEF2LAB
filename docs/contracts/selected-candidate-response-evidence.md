# 선택 후보 응답 구조 근거와 판정 게이트

**상태: 1단계 구현(2026-11-27).** [선택 LLM 검증 계약](selected-candidate-llm-verification.md)의 `selected/llm-v1` 흐름·승인·E② 경계는 그대로다. 이 문서는 그 위에 더한 응답 구조 근거(`jindo.candidate-response-evidence/v1`), 비교(`jindo.candidate-response-comparison/v1`), 최종 판정 게이트(`selected/evidence-gate-v1`)만 정한다.

## 왜 필요한가

#950의 저장 관찰(`StoredObservationV2`)은 상태 코드·미디어 범주·인증 범주·요청 조건 digest만 담는다. 응답 본문은 저장하지 않는다. 그래서 오류 노출·데이터 노출·업무 규칙처럼 본문의 의미가 중요한 후보는 최종 모델이 재현 응답의 내용을 보지 못한 채 원본 후보의 도구 출력만으로 판단했고, 프로그램은 근거의 충분성을 거의 검사하지 못했다(#925 별도 실측: 근거 부족 5건 모두 잘못된 확정 권고).

## 흐름

1. **추출** — E② 프록시(`e2proxy`)가 **완결·비보류** 응답(본문을 끝까지 받았고, 자격 반사·쿠키 응답 등으로 표현을 보류하지 않은 응답)에서만 `responseevidence.Extract`를 부른다. 결과는 `CallResult.response_evidence[]`에 `attempt_ref`·`sequence`로 그 시도의 관찰에 묶인다. 근거 하나는 `Fit`으로 1024바이트 이하, 한 실행 전체는 2 MiB 이하다(원격 실행 조회 봉투 8 MiB 보호). 넘으면 근거를 붙이지 않는다.
2. **워커 모델과 분리** — 원본 진단 워커 모델의 도구 결과(`encodeE2HTTPResult`)와 후보 근거 원문(`observeDiagnostic`)에서는 `response_evidence`를 뺀다. 원본 ARTEX 진단의 모델 입력·토큰은 바뀌지 않는다.
3. **저장** — 진단 종료·후속 실행 종료 때 `ProjectTrustedSelected`가 같은 시도의 근거를 **정확히 하나** 찾아 저장 관찰 행의 `response_evidence` 봉투로 옮긴다. 없거나 둘 이상이거나 상태 코드·미디어 범주가 모순되면 `state=not_recorded`를 명시한다(소급 생성 없음). 기존 관찰이 `+json`·XHTML을 `other`로 묶는 차이는 허용한다.
4. **비교·게이트** — 최종 모델 단계(새 E② 실행·저장 관찰 평가 공통)에서 `reviewCandidateEvidence`가 원본↔재현, 원본↔정상 대조, 재현↔정상 대조를 비교하고 결정적 권고 허용 여부를 정한다. 결과는 모델 입력 `program_evidence_review`와 판정 게이트에 같이 쓴다.
5. **재조회** — 이력·보고서는 저장 문맥으로 게이트를 다시 계산한다. API에는 선택 필드 `verifications[].evidence_review`로 나간다.

## 근거 형식 (`jindo.candidate-response-evidence/v1`)

값 원문·본문 글·쿠키 이름·헤더 값은 없다. 예시는 [합성 fixture](fixtures/selected-candidate-response-evidence.json).

| 칸 | 내용 |
|---|---|
| `state` | `complete`, `limited`(상한으로 일부만, `limits`에 이유), `unsupported_media`(XML·이진·압축·HEAD·형식 미상), `truncated`, `parse_failed`, `not_received`, `incomplete`, `not_recorded`(저장 쪽: 근거 없음). `complete`·`limited`만 구조가 있다 |
| `media_class`, `status_code`, `body_bytes`, `empty` | 본문 해석 범주(`+json`→json, `xhtml`→html), 상태, 처리 길이, 빈 응답. 빈 응답과 '필드 없음'은 다르다 |
| `signals` | 고정 오류 신호 코드: `stack_trace`, `stack_trace_field`, `sql_error`, `debug_page`, `server_path`, `server_error_text`, `exception_name`. HTML은 `script`·`style` 밖 글만 본다 |
| `headers` | 고정 보안 헤더 목록의 존재(`present`), 중복(`repeated`), `content_type_options`·`frame_options`·`cors_allow_origin`의 범주, `csp_frame_ancestors`, `cache_no_store`, `cors_credentials`, Set-Cookie 속성 개수(`secure`·`http_only`·SameSite별) |
| `json` | `root_type`, `root_length`, `error_shape`(최상위 error/errors/exception/fault), `duplicate_keys`, `fields[]`(경로·자료형·횟수·배열 길이·빈 값·문자열 모양·bool 값·값 digest·보호 표시), `fields_omitted` |
| `html` | `doctype`, `title`(있음만), 요소 수, 고정 태그 수, 얕은 구조 digest, `password_input`, 경고 영역 수, 폼(메서드·경로 모양·입력 이름/유형) |
| `text` | 줄 수, 빈 줄 아닌 줄 수 |

- **경로** — `a.b`, 배열 원소는 `items[].id`, 최상위 스칼라는 `$`. 숫자·이메일·점이 든 키는 `{key}`, 보호 값이 든 키는 `{protected}`로 바꾼다.
- **값 digest** — 한 번만 나온 256바이트 이하 스칼라에만 붙는 64비트 비교용 SHA-256 접두다. 원본·재현의 **값 같음**만 알려 준다. 이름에 token·password·secret·session·cookie·auth·credential·jwt·bearer·apikey가 들어 있거나 jwt 모양인 값, 보호 값(제어 비밀·E③ 표식·출력 가림 값)에는 붙이지 않는다. 짧은 값의 digest는 사전 대입으로 추정될 수 있으므로 공개 보고서·외부 공유용이 아니다(저장 관찰은 내부 기록).
- **폼 경로** — 경로만, query·fragment·호스트 없음(절대 URL은 `absolute:` 접두). 자원 모양 조각은 `{segment}`.
- **상한** — 본문 해석 1 MiB(넘으면 구조 없이 `limited/body_bytes`), JSON 깊이 32·필드 48·배열당 원소 64(길이는 끝까지 셈)·문자열 256, HTML 토큰 5만·폼 4·폼당 입력 12, 신호 검사 256 KiB. 호출자가 더 큰 상한을 지정해도 기본 상한을 넘지 않는다. 문법 검사용 JSON 재귀 깊이가 256을 넘으면 `parse_failed`로 멈춘다. 압축 본문은 풀지 않는다.
- **결정성** — 같은 응답은 같은 근거다(키 순서 무관, 시각·난수 없음). `Validate`가 형식·범위를 다시 검사하고, 손상 근거는 비교·모델 입력에 쓰지 않는다.

## 저장 결속과 신뢰 범위

`StoredObservationV2`(revision 2)에 선택 필드 `response_evidence`를 더했다: `attempt_ref`, `sequence`, `observation_digest`(= `source.digest`), `evidence`, `digest`(앞 넷의 SHA-256). `ValidSelectedStoredObservation`이 결속·digest·형식·상태 코드 일치를 검사한다.

- **기존 의미 불변** — `source.digest`(관찰 digest)는 `HTTPObservation`만으로 계산하고, 관찰 구조체에는 필드를 더하지 않았다. 옛 행(봉투 없음)은 그대로 유효하고 `not_recorded`로 읽는다. 행 전체 digest·회차의 관찰 묶음 digest는 봉투를 포함하므로 저장 뒤 바꿔치기는 기존처럼 503이다.
- **행 상한** — 4096바이트 안에서 근거를 먼저 줄인다(태그 수 → 깊은 JSON 경로 → 폼 입력 → 폼 → 구조 전체, 매번 `limited/encoded_size`). 그래도 안 되면 `not_recorded`. **Host 증빙(`selected_proof`)은 근거 때문에 버리지 않는다.** 증빙 혼자 상한을 넘는 기존 경우만 기존 규칙대로 증빙 없는 관찰을 남긴다.
- **한계** — E② Host 감사 저널은 시도·결정·상태·응답 완료를 증명하지만 **응답 본문 구조는 독립적으로 증명하지 않는다.** 근거는 Runner의 E② 프록시가 만든 관찰이라 신뢰 수준이 같은 관찰의 미디어 범주·기술 배너와 같다. 결속·digest는 저장 뒤 변조와 다른 관찰로의 이동을 막을 뿐이다. 공용 감사 스키마는 바꾸지 않았다.

## 비교 (`jindo.candidate-response-comparison/v1`)

`responseevidence.Compare`는 두 근거의 사실 비교이고 취약점 판정이 아니다. 역할은 `original`·`reproduction`·`normal_control`이며 정상 대조는 재현이 아니다.

- `relation`: `equivalent`, `equivalent_after_normalization`, `different`, `incomparable`(한쪽이 `not_recorded`·`truncated`·`incomplete`·`parse_failed`·`not_received`·형식 손상). `incomparable`은 '차이 없음'이 아니다.
- `differences[]`(최대 32, 넘으면 `differences_omitted`): 상태, 미디어, 빈 응답, 오류 신호, 보안 헤더 존재·속성, JSON 루트 자료형·배열 길이·오류 구조·필드 존재·자료형·배열 길이·빈 값·bool·문자열 모양·값, 비밀번호 입력, 경고 영역, 제목, 폼 수·메서드·경로·입력, HTML 구조, 텍스트 줄 수. 응답 길이 차이는 차이로 세지 않는다(`body_bytes`로만 표시).
- **정규화** — 이름(csrf·xsrf·nonce·token·timestamp·time·date·created·updated·modified·expires·issued·trace·span·etag·request_id·correlation, exp·iat·nbf·ts)이나 모양(datetime·jwt)으로 요청마다 바뀌는 값의 **값 차이만** `normalized`에 `경로:기준`으로 남긴다. 존재·자료형 차이는 정규화하지 않는다. ID는 정규화하지 않는다(다른 계정·자원이 돌아온 것은 중요한 차이). 자격 이름 값은 비교하지 않고 `not_compared: credential_value`로 남긴다.
- `not_compared`: 상한(`limit:*`), 필드·폼·입력 생략(상대가 필드를 생략했으면 부재를 주장하지 않음), digest 없음, 중복 키, 보호 값, 본문 구조.
- 헤더 비교는 반복 헤더와 Set-Cookie의 Secure·HttpOnly·SameSite Strict/Lax/None/누락 개수 차이를 남긴다. HTML 태그 수가 직렬화 상한으로 빠졌으면 다른 구조 digest를 단순 태그 순서 차이로 정규화하지 않는다. 형식이 손상된 근거는 `incomparable`이다.
- `similarity`: JSON 경로 일치율, HTML 고정 태그 수 유사도(구조 digest가 같고 태그 수가 있으면 100, 태그 수가 같아도 digest가 다르면 99와 구조 차이 기록). 태그 수를 버렸으면 유사도를 계산하지 않는다.

## 최종 판정 게이트 (`selected/evidence-gate-v1`)

프로그램은 관찰 사실을, 모델은 의미를 맡는다. 기존 #950 검사(출처 triple·원본/후속 인용·인증·계정·입력 digest·헤더 모드·정확한 기능)는 그대로 먼저 돈다. 게이트는 **저장 관찰 중 하나라도 `response_evidence` 봉투를 가진 문맥**에서만 켜진다. 봉투가 전혀 없는 옛 문맥은 입력·판정이 #950과 같다(이미 저장된 판정의 재조회 결과가 바뀌지 않음).

근거 요구 범주(`requirement`)는 후보 유형으로 정한다: `security_headers`·`session_cookie`·`cors`·`cors_misconfiguration`·`clickjacking`과 기존 nosniff·Server 배너 주장은 `response_headers`(헤더 사실 필요), 나머지는 `response_structure`(본문 구조 필요). 모델 `confirmed`/`negative`는 다음이면 `insufficient`와 고정 사유로 바뀐다.

| 사유 | 조건 | 막는 판정 |
|---|---|---|
| `response_evidence_unavailable` | 원본이나 재현의 근거가 없거나(옛 행·`not_recorded`), 요구 범주의 근거가 없음(구조 없음·헤더 없음), 재현 관찰이 없음 | 확정·반박 |
| `response_evidence_incomparable` | 비교 `incomparable`, 또는 구조 범주인데 본문 구조를 비교하지 못하거나 JSON 중복 키·필드/폼/입력·깊이/배열/HTML 토큰 상한·차이 목록 상한으로 구조 비교 범위가 빠짐 | 확정·반박 |
| `reproduction_auth_context_lost` | 재현이 401/407/403(원본과 다름)·3xx 이동, 또는 원본에 없던 비밀번호 입력 화면 | 확정·반박 |
| `reproduction_inconsistent` | 상태·미디어·빈 응답·JSON 루트 자료형·오류 구조·비밀번호 입력이 원본과 다름(확정만). 미디어·루트 자료형 차이는 반박도 | 확정(·반박) |
| `reproduction_unsuccessful` | 재현 상태가 원본과 다르고 2xx가 아님(4xx·5xx 등) — 요청 실패는 반박이 아님 | 반박 |

목록 길이·값 변화처럼 정상 데이터 변동일 수 있는 차이는 막지 않고 모델이 해석한다. 상태 200, 오류 응답, 길이 변화, 헤더 하나, 필드가 많다는 사실만으로는 결정하지 말라고 모델 지시에 명시한다. 정상 대조는 재현을 대신하지 않는다(기존 규칙). `model_supported`는 프로그램 확정이나 E③ `confirmed`가 아니며 보고서 집계는 기존대로 분리한다.

### 모델 입력

게이트 문맥에서만 각 관찰에 `response_evidence`(옛 행은 `not_recorded` 명시)와 `program_evidence_review`(비교·허용 여부·차단 사유)를 넣고, 시스템 지시에 근거 해석 규칙을 덧붙인다. 입력 상한은 게이트 문맥 64 KiB, 옛 문맥 48 KiB 그대로다. 대상 응답의 글은 구조 근거로 들어가지 않는다(응답 속 지시문은 경로 이름 외에는 모델에 닿지 않는다). 원본 후보의 도구 출력(`original_evidence`, 12 KiB)은 #950 그대로 신뢰하지 않는 데이터로 들어간다.

## 판정 게이트 v2 (`selected/evidence-gate-v2`, 2026-11-27)

v1이 "구조가 있고 재현이 일관되는가"를 봤다면 v2는 **이 주장에 필요한 종류의 사실이 원본과 모든 재현에 실제로 관찰됐는가**와 **결정적 권고가 실제 재현을 인용했는가**를 더 본다. 프로그램은 사실의 유무·출처·비교 조건만 보고, 사실의 보안상 의미와 업무 해석은 모델이 한다. 범용 취약점 판정기가 아니다.

### 판본과 기존 기록

- 새 검증(`ReadCandidateAssessmentContext`·저장 관찰 평가 문맥)은 `candidateEvidenceGateLatest`(= v2)를 쓴다. 판정 JSON에 `evidence_gate: "selected/evidence-gate-v2"`를 프로그램이 적는다(모델이 정하지 않음, 모델이 다른 값을 내면 판정 거부). 판정 digest(`result_digest`)가 이 칸을 덮는다.
- 재조회는 저장된 판정의 `evidence_gate`로 같은 판본을 다시 적용한다. 칸이 없으면(#950·#999 판정) v1 이전 의미 그대로다: 근거 봉투가 없으면 #950 규칙, 있으면 v1 게이트. 새 정책이 옛 판정·보고서를 재조회만으로 바꾸지 않는다. 알 수 없는 판본은 판정 손상으로 읽는다.
- v2는 근거 봉투가 없는 옛 회차를 새로 검증할 때도 아래 **재현 인용** 규칙은 적용한다. 근거 게이트(사실 검사)는 v1과 같게 봉투가 있는 문맥에서만 켜진다(옛 회차 모델 입력은 v2 지시 한 단락 말고는 그대로).

### 주장 범주와 필요한 사실

범주는 후보의 `vulnclass`·`verification_claim`(저장된 분류 필드)만으로 정한다. 이름·요약·응답 문장은 읽지 않는다. 낱말 경계로 맞추고 처음 맞는 범주를 쓴다(`server/candidate_verification_claim_evidence.go`).

| 범주 | 예 (vulnclass 낱말) | 확정 권고에 필요한 사실 (하나 이상 묶음) | 정상 대조 |
|---|---|---|---|
| `security_header` | header(s)·clickjacking·cors·hsts·csp·frame, nosniff·Server 배너 주장 | `response_headers` | 불필요 |
| `cookie_attribute` | cookie | `issued_cookie` (반박에도 필요) | 불필요 |
| `error_disclosure` | error·stack·exception·debug·verbose·trace | `specific_error_signal` | 유용 |
| `data_exposure` | exposure·disclosure·leak·sensitive·pii·information·banner | `specific_error_signal` 또는 `disclosure_header` 또는 (`structured_content` + `business_rule_statement`) | 유용 |
| `auth_context` | auth·authentication·session·login·credential·jwt | `structured_content` | 유용 |
| `business_rule` | business·logic·access_control·privilege·authorization·mass_assignment·workflow | `structured_content` + `business_rule_statement` | 유용 |
| `input_handling` | sql·sqli·injection·xss·traversal·ssrf·ssti·xxe·open_redirect·rce·command·template 등 | `specific_error_signal` 또는 `input_contrast` | 필요 |
| `unknown` | 그 밖 | `specific_error_signal` 또는 `structured_content` | 유용 |

사실 정의:

- `response_headers`: 보안 헤더 구조를 읽음. `issued_cookie`: Set-Cookie가 실제로 옴(E②가 자격 응답 표현을 보류하면 근거 자체가 없어 관찰되지 않는다). `disclosure_header`: `Server`·`X-Powered-By`가 옴.
- `specific_error_signal`: 고정 오류 신호 중 `stack_trace`·`stack_trace_field`·`sql_error`·`debug_page`·`server_path`·`exception_name`. `server_error_text`("Internal Server Error" 같은 일반 문구)는 포함하지 않는다.
- `structured_content`: 2xx이고 비어 있지 않은 본문 구조(JSON·HTML·텍스트), JSON 오류 구조(`error`/`errors`/…)가 아님.
- `business_rule_statement`: 운영자가 업무 질문에 답함(문맥 사실). 대상 응답에 적힌 문장은 업무 규칙이 될 수 없다.
- `input_contrast`: 같은 기능 경로·메서드에서 입력(Query digest)이 다른 정상 대조와 재현이 둘 다 성공 응답이고, 그 비교에 상태·미디어·빈 응답·오류 신호·오류 구조·헤더 말고 구조 차이(필드·길이·폼·HTML 구조 등)가 있음. 다른 기능의 정상 대조는 입력 효과로 세지 않는다. 정규화된 값 변화와 스칼라 값·불리언 값·문자열 모양·필드 빈값만의 변화도 세지 않는다.

관찰 사실은 원본과 **모든** 재현에 공통이어야 한다(재현 하나라도 없으면 재현되지 않은 사실). 4xx·5xx인데 구체적 신호가 없는 관찰은 `generic_error`로 따로 표시한다.

### 추가 차단 사유

v1 사유(위 표)가 먼저고, 그다음 v2 사유가 붙는다. 모두 `insufficient`와 고정 사유로 바꾼다.

| 사유 | 조건 | 막는 판정 |
|---|---|---|
| `generic_error_only` | 필요한 사실이 없고 원본·재현 중 일반 오류 응답(구체 신호 없는 4xx·5xx)이 있음. 헤더 범주가 아니면 반박도 막음(요청 실패는 반박이 아님) | 확정(·반박) |
| `business_rule_unstated` | 빠진 사실이 업무 진술(데이터 노출·업무 규칙) | 확정 |
| `input_effect_unobserved` | 입력 처리 주장인데 구체 신호도 입력 대비도 없음 | 확정 |
| `claim_fact_unobserved` | 그 밖에 범주 사실이 없음. 쿠키 범주는 쿠키가 없으면 반박도 막음 | 확정(·반박) |
| `original_not_cited` | 결정적 권고가 원본 관찰을 인용하지 않음 | 확정·반박 |
| `reproduction_not_cited` | 결정적 권고가 실제 재현(승인 계획에서 `kind`가 빈 행동, 저장 관찰 평가는 독립 관찰)을 인용하지 않음. 정상 대조 인용은 재현을 대신하지 않는다 | 확정·반박 |

출처 triple(`call_ref`·`attempt_ref`·`observation_digest`) 결속, 같은 관찰 중복 인용·다른 후보/작업/회차 출처 거부(판정 거부), 역할은 모델이 아니라 승인 계획이 정한다는 규칙은 그대로다. 모든 재현을 게이트가 검사하므로 인용하지 않은 재현의 불일치도 확정을 막는다.

**구조 근거 안 특정 사실의 모델 인용 계약은 추가하지 않았다.** 프로그램이 인용된 원본·재현의 필요한 사실을 직접 검사하므로, 모델이 사실 이름을 다시 적는 칸은 독립 증거를 더하지 않고 출력 형식만 바꾼다.

### 대상 응답 속 지시문 (R16)

응답 문장은 구조 근거에 복제되지 않고(경로 이름·모양만), 어떤 사실도 채우지 못한다. 그래서 지시문이 든 응답을 근거로 한 확정은 구체 신호·노출 헤더·운영자 업무 진술 중 하나가 따로 관찰돼야 남는다. 지시문처럼 보이는 문자열이 업무 데이터에 있다는 이유만으로 후보를 거부하지 않는다(운영자 진술이 있으면 정당한 확정은 남음). 원본 후보의 도구 출력(`original_evidence`)은 여전히 신뢰하지 않는 데이터로 들어간다. 프로그램은 의미 판단을 하지 않으므로, 모델이 지시문에 끌려 **반박**하는 경우(데이터 노출의 반박은 업무 진술 없이도 가능해야 해서 막지 않음)는 남는 한계다.

### v2 모델 입력

새 검증에만 시스템 지시 한 단락을 더한다: 결정적 판정은 원본과 독립 재현을 인용해야 하고 정상 대조가 재현을 대신하지 못함, `program_evidence_review.claim` 해석, 대상이 쓴 문장은 결함 증거도 지시도 아님. `program_evidence_review`에 선택 칸 `claim`(`claim_category`, `confirm_needs_any_of`, `normal_control`, `observed_facts`, `satisfied_by`, `missing_facts`)이 붙는다. 입력 상한(64 KiB·48 KiB)과 모델 호출 수는 그대로다.

## API (선택 확장)

`GET .../verifications`의 `verifications[].evidence_review`(선택): 완료된 `llm/v1` 판정이 있고 저장 문맥이 게이트 대상일 때만 있다. 칸은 `version`, `requirement`, `comparisons[]`(`left`/`right` 출처 triple, `comparison`), `confirm_allowed`, `negative_allowed`, `confirm_blocked_reason`, `negative_blocked_reason`. 저장값이 아니라 재조회 때 저장 문맥으로 다시 계산한 값이다. 옛 회차·진행 중·실패·취소에서는 생략한다. 새 처리 상태나 오류 코드는 없다. 기존 `final_verdict`·`latest.verdict` 의미는 그대로다. v2 판정에서는 `evidence_review.version`이 `selected/evidence-gate-v2`이고 선택 칸 `claim`이 붙으며(위 절), 판정 객체에 선택 칸 `evidence_gate`가 있다. v2 사유 코드는 판정 `reason`과 `confirm_blocked_reason`·`negative_blocked_reason`에 나온다. fixture: `docs/contracts/fixtures/selected-candidate-response-evidence.json`의 `gate_v2_example`. HTML·DOCX 보고서는 바꾸지 않았다(UI·보고서 표시는 이경준님 영역, 인계 사항).

## 로그인 재사용과의 호환 (#974·#986·#987)

- 지금 선택 검증의 인증 준비는 설치된 로그인 설정(`diagnosisprep.TargetLogin`) 전체의 digest를 계획 `auth_preparation.login_digest`에 묶고, 실행 직전 현재 정책의 digest와 다시 대조한다. 계정은 `account_ref`(자격 판본)로 묶는다. 이번 단계는 이 방식을 그대로 둔다.
- 로그인 설정 9칸이 폐지되면 `login == nil`이라 인증된 원본의 후속 계획을 만들지 않는다. 인증 원본 후보는 `insufficient`로 남고(안전하게 실패), 무인증 후보와 저장 관찰 평가는 영향이 없다.
- 후속 제안(별도 PR): `auth_preparation`에 `kind`(`configured_login/v1` | `learned_login/v1` | `learned_login/v2`)와 학습 방법 digest를 더해 계획·승인에 묶는다. 실행 직전 현재 학습 방법 digest·대상 승인 판본·계정 자격 판본이 모두 같아야 하고, 다르면 계획 만료(`verification_plan_changed`)로 재승인을 요구한다. 학습 방법의 영속 저장·읽기는 최민준님의 learned 저장(⑤-B)을 재사용하고 중복 구현하지 않는다. 후보 후속 실행은 E③ 확정 목적이 아니므로 E② `LearnedLogin` 목적 값과 권한을 새로 정해야 한다.

## 데이터 보호

DLP 폐기(2026-11-26) 뒤에도 이 근거에는 본문 원문·값·세션·쿠키·자격값을 새로 복제하지 않는다(데이터 최소화). 제어 비밀·E③ 표식·출력 가림 값은 `e2proxy`의 가림 판정으로 이름·값에서 걸러 `{protected}`로 남기고, 그런 값이 본문에 반사된 응답은 E②가 표현을 보류하므로 근거 자체가 없다. 응답 속 지시문은 데이터일 뿐 승인·계획·지시를 바꾸지 못한다.

## 품질 평가

[선택 후보 품질 평가](../evaluation/selected-candidate-quality.md)의 구조화 근거 평가 절을 따른다.

## 지원 범위와 남은 일

- 지원: 기존 GET·HEAD 최대 3개 행동과 인증 준비 POST 경계 안의 보안 헤더, 오류 정보 노출, 인증 문맥 불일치(로그인 화면·인증 요구), 읽기 전용 업무 규칙·데이터 노출 의심(필드 존재·값 같음·bool), 응답 형식 불일치. 확정 가능성을 보장하지 않으며 의미 판단이 필요한 주장은 모델 몫이다.
- v2 한계: 범주 분류는 `vulnclass` 낱말에 기대므로 모호한 유형은 `unknown`으로 떨어진다. 데이터 노출·업무 규칙의 확정은 운영자 업무 진술이 있어야 해서, 분석 단계가 업무 질문을 내지 않으면 `business_rule_unstated`로 남는다. 의미적 오판(응답 지시문에 끌린 반박 등)은 프로그램으로 막지 않는다. 반대로 보수적 차단도 있다: 원본과 재현이 모두 구체 신호 없는 오류 응답이면 오류 노출 주장의 반박도 `generic_error_only`로 막는다(요청 실패를 반박으로 쓰지 않는다는 규칙을 오류 응답이 곧 기능 응답인 경우에도 적용).
- 미지원: XML·이진·압축 본문 구조, 본문 원문 비교, 쓰기 메서드·임의 Body·동적 입력·다단계 재현, Host의 본문 구조 증명, 학습 로그인 기반 후속 인증(위 제안), 보고서 HTML·DOCX의 비교 표시, 분석 단계 모델 입력의 구조 근거.
