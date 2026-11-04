# 저장 profile 기반 로컬 LLM 연결시험 — 팀 검토안

상태: **미확정 계약 초안. API·송신 경로·운영 권한을 활성화하지 않는다.** dev `88415fff88f1c66c4307ba370c360e6ad338efcc`의 [서버 로컬 모드](./local-llm-server-mode.md)에서 남겨 둔 guarded 연결시험 경계를 구체화한다. 이동찬은 서버·전송 경계를, 최민준은 수용시험을 검토하고 UI 연결은 이경준과 확인한다. 이름 표기는 합의 완료를 뜻하지 않는다.

## 1. 범위와 결정안

현재 로컬 모드의 `/api/llm/test`·`/api/llm/models`는 요청을 보내지 않고 실패한다. 새 시험을 추가해도 이 legacy 경로의 차단을 유지한다. 이 문서의 후보는 **비스트리밍 completion 한 종류**이며 모델 목록 조회, 설정 저장·활성 profile 변경, task 실행이나 대상 진단은 포함하지 않는다.

| 결정 | 권장안 | 확정에 필요한 확인 |
|---|---|---|
| 요청 출처 | 기존 API 인증을 통과한 사람이 명시적으로 실행. 자동 startup/health/retry/모델 도구 호출에서 실행하지 않음 | 현재 인증은 별도 operator role을 제공하지 않으므로 로그인 사용자 전체에 허용할지 팀 확인. 그 전에는 비활성 |
| 입력 | 양의 저장 profile ID 하나. URL·model·key·proxy·prompt·pin override를 받지 않음 | 서버/화면이 저장 후 시험 순서를 표시하는지 확인 |
| 결속 | DB의 effective Config 한 snapshot과 같은 ID로 resolver·guarded factory를 연속 호출 | [기존 tuple 규칙](./local-llm-profile-policy.md) 유지. 전체 profile revision을 증명하지 않음 |
| 실제 송신 | 전체 기한과 최종 transport의 물리적 HTTP attempt 1회 상한을 모두 집행 | SDK retry·redirect도 상한 소비. 호출 횟수 1회를 송신 횟수 1회로 간주하지 않음 |
| 출력 | 성공 여부·고정 코드·측정 지연만 반환. 모델 응답과 설정 원문은 폐기 | [연결시험 DLP](./llm-connection-output.md)와 일반 요청 기록 분리 검토 |
| 적용 | 기존 API와 별도 route로 구현하고 명시적 권한·상한 검증 후 연결 | 이 문서 단독으로 현재 차단을 해제하지 않음 |

열린 감사·revision·평가 PR의 함수·schema나 병합을 전제로 하지 않는다. [정책 관측 제안](./local-llm-policy-observation-proposal.md)의 관측 API·sidecar 구현도 필요하지 않다. 이 계약은 현재 dev의 `NewLocalProvider`를 사용하는 독립 후속이며, 기존 선언/평가 기록 version은 변경하지 않는다.

## 2. 요청과 성공의 의미

권장 route는 `POST /api/llm/local-test`다. 인증 실패는 기존 인증 HTTP 상태를 사용하고 요청을 파싱·송신하지 않는다. 본문은 아래와 같은 정확한 키 집합의 UTF-8 JSON 객체 하나이며 상한 1 KiB다. 중복 키·모르는 키·trailing JSON·null·bool/실수 ID·int64 overflow는 고정 400 오류로 거부한다.

```json
{"profile_id": 7}
```

로컬 모드가 아니면 409로 거부한다. DB/선택 출처 오류·profile 누락·빈 key·resolver/factory 실패에는 다른 profile/global/env/legacy provider를 사용하지 않는다. 저장된 key만 비공개 경로로 전달하고 원문을 응답하지 않는다. profile ID의 실재 확인은 권한 검사 뒤에 한다.

클라이언트는 이 POST를 자동 재시도하지 않고, 결과 불명 상태를 성공으로 표시하지 않는다. 이 v1 입력에는 재시도 영수증/멱등 키가 없으므로 서로 다른 API 호출까지 합쳐 정확히 1회 송신을 보장하지 않는다. 수동 재실행도 새 시험임을 표시한다.

실제 전송은 **같은 Config snapshot**의 ID/Format/Model/BaseURL 결속을 확인한 guarded provider의 `Complete`를 사용한다. 서버 소유의 고정 비민감 prompt만 보내고 대화·대상 응답·계정 자료·GT·사용자 입력을 섞지 않는다. 변경 가능한 DB를 다시 읽어 factory 입력을 교체하지 않는다. 송신 직전부터 종료까지의 snapshot을 시험한 것이며, 이후 profile 수정·credential 교체나 전체 profile 불변 revision을 보장하지 않는다.

성공은 해당 snapshot으로 제한된 completion이 끝났다는 뜻이다. streaming/tool calling 지원, 모델 정체·품질, task 준비 완료, 전체 앱 망 격리, E② 대상 실행 승인 또는 공식 평가 적격으로 표현하지 않는다.

## 3. 자원·전송 수명

잠정 상한은 호출 전체 10초, 물리적 HTTP attempt 1회, 응답 body 누적 64 KiB, 프로세스 안 동시 시험 1개다. 운영 측정값이 아닌 팀 검토값이다. 대기열은 만들지 않고 이미 실행 중이면 고정 429 오류로 거부한다. 범용 provider의 streaming timeout 기본값을 이 경로의 전체 기한으로 대신하지 않는다.

attempt 상한은 DNS/dial 이전의 guarded 전송 경계에서 원자적으로 소비한다. 최초 요청 뒤 SDK retry·동일 origin redirect도 추가 permit을 얻지 못하며, cross-origin/환경 proxy/TLS 검증 생략은 기존 정책대로 거부한다. 임의 transport로 guarded client를 교체하지 않는다. SDK 내부 재시도를 끈 설정만으로 물리적 상한 집행을 대체하지 않는다. 현 factory가 이 제한을 주입하는 경로는 아직 없으므로 후속 구현·시험 전 새 API를 활성화하지 않는다.

body 상한은 응답을 모두 읽은 뒤가 아니라 읽는 동안 누적 wire 바이트로 적용하고 초과 시 body를 닫고 context를 취소한다. 압축 해제가 있으면 SDK에 전달되는 해제 후 바이트에도 같은 상한을 적용해 압축으로 제한을 우회하지 못하게 한다. 요청 취소·기한·오류에도 body/연결을 닫는다. 진행 중 전송 작업이 종료돼야 동시 시험 슬롯을 반환하며, handler 반환만으로 종료됐다고 판단하지 않는다. 종료를 확인하지 못하면 슬롯을 재사용하지 않고 새로운 시험을 거부한다.

## 4. 응답과 로그

새 route의 결과 키는 `ok`(bool), `code`(고정 enum), `latency_ms`(0 이상 int64)만 제안한다. 성공은 200/`ok:true`/`code:completed`이며 실패의 ok는 항상 false다. 파싱 실패 400/`invalid_request`, 모드·권한 불충족 409/`not_available`, 동시 상한 429/`busy`, 선택 실패 200/`profile_unavailable`, provider 실패 200/`probe_failed`, 기한 만료 200/`deadline`을 제안한다. 기존 인증 실패의 상태·본문은 이 새 DTO의 예외로 유지한다.

`latency_ms`는 guarded completion 직전부터 종료까지이며 송신 전 실패는 0이다. 원격 HTTP 상태와 이 API의 상태를 혼동하지 않는다. 코드에 raw 오류·profile name·endpoint·key hint를 붙이지 않는다. 모델 reply·usage·SSE·partial 결과도 응답하지 않는다. 실패 원인의 세부 내부 구별은 권한·진단 계약을 따로 확인하기 전 공개하지 않는다.

진단 로그는 기존 metadata-only Capture 경계를 사용하고 원문 body getter를 호출하지 않는다. 고정 코드·지연·attempt 건수·wire 상태/바이트 수만 남긴다. raw body를 보관하는 일반 `llmrec`/side-question wrapper에 시험 provider를 자동 연결하지 않는다. 시험 이벤트의 영속 감사 게시가 운영 필수인지와 실패 시 동작은 팀 확인 대상으로 남긴다. 의무가 확정되면 그 감사 경계가 준비되기 전에는 송신을 허용하지 않는다.

## 5. 후속 수용 기준 — 계획, 미실행

| ID | 확인할 기준 |
|---|---|
| LPT01 | 인증·명시적 실행 권한·모드 검증 실패에서 profile 조회와 모델 수신 0 |
| LPT02 | 과대·중복/미등록 키·잘못된 ID/trailing JSON이 고정 오류로 거부되고 원문 미노출 |
| LPT03 | 허용 profile의 성공을 loopback 양성 대조로 확인. pin/tuple/proxy/DB 오류는 fallback·수신 0 |
| LPT04 | 실제 SDK 429·redirect·재시도 응답으로 물리적 수신 최대 1, DNS/dial 전 2번째 attempt 거부 |
| LPT05 | 지연·무한 body·압축 팽창·중단 응답에서 10초/64 KiB 상한, body/전송 종료 확인과 슬롯 반환 |
| LPT06 | 동시 호출·취소·종료 불명에서 추가 송신 없음. 종료 뒤 새 시험의 정상 회복 |
| LPT07 | 성공/실패/partial 결과와 진단·DB 기록에 합성 비밀·reply·URL·key hint가 없음 |
| LPT08 | legacy test/models의 로컬 차단과 external 모드 동작 유지. 관측/평가 schema 변경 0 |

확정 전 남은 결정은 실행 권한의 주체, 자원 상한의 적정성, 새 route의 HTTP/code 매핑, 영속 감사 의무다. 모든 수용 기준은 향후 구현의 계획이며, 이 문서 검증을 실제 모델 호출·보안 설정 변경·통합시험으로 기록하지 않는다.
