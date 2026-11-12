# 평가 전용 caller 자격 원문 통과

상태: 평가 프로토콜 §2의 11/06 결정에 따른 **프록시 코어 opt-in 구현**.
실제 개선 후 평가 회차 활성화·배치 수용·정책 지문 동결은 아직 완료하지 않았다.
보호 세션 주입·등록 GET/IDOR 경로·운영 대상 통과 모드와 분리한다.

## 설정과 불변 경계

신뢰 host는 `HeaderEvaluationCallerPassthrough`
(`evaluation_caller_passthrough`)와 `Config.Evaluation`을 함께 전달한다.
`EvaluationCallerContext`는 다음 세 비민감 선언값만 갖는다.

| 필드 | 검사 |
|---|---|
| `run_ref` | 기존 승인 Scope.RunRef와 정확히 같음 |
| `llm_backend` | `EvaluationLLMBackendLocal`(`local`) 또는 `EvaluationLLMBackendAPI`(`api`) |
| `target_kind` | `public_practice` |

이는 신뢰 host의 **선언**이며 공개 연습 대상·승인·격리·tmpfs·삭제를
프록시가 실제 확인했다는 증명이 아니다. target origin/IP·메서드·경로와
승인 예산·감사 ACK·시간/중지 조건은 기존 E② 검사를 통과해야 한다.
일반/보호 세션 모드에 이 선언을 섞거나 원문 모드에
`Rule.CredentialHeaders`(custom 포함)를 넣으면 생성 자체를 거부한다.
입력과 조회 projection은 별도로 복사하므로 호출자가 사후 변경할 수 없다.

기본 `preserve`와 `protected_session_proposal`의 caller
Authorization/Cookie 송신 전 거부는 유지한다. 새 모드는 caller의 두 헤더 값을
주입·치환하지 않고 전달한다. 빈/nil 값·대소문자 중복 map·Connection이
자격 헤더를 hop-by-hop으로 지정하는 요청은 익명 요청으로 고쳐 보내지 않고 거부한다.
HTTP 파싱과 hop-by-hop 정리 등 기존 표현 정규화 범위는 동일하다.

## 모델과 기록

대상 응답 본문의 신규 토큰은 도구에 보일 수 있다. API 회차에서는 제공자로
갈 수 있으며, 이를 가렸다고 표현하지 않는다. 우리가 공급한 보호 자격을
모델에 주는 기능은 추가하지 않는다. `Accept-Encoding`은 일반 모드와 같이
`identity`로 바꿔 보내고([전달 범위](./e2-proxy-transfer-scope.md)), Set-Cookie 등 자격
응답의 전체 보류/unknown 한도는 그대로다.

`Policy()`와 `Snapshot()`은 모드와 위 선언값을 비민감 projection으로
노출한다. 이 값은 정책 지문 그 자체가 아니다. C/러너 연결 담당은 모드·세 필드·
기존 정책/도구 범위를 고정 정책 지문 및 서명 회차 기록에 결속하고,
선언과 실제 설정을 대조해야 한다. 이 코어는 지문·회차를 발급하거나 러너를 바꾸지 않는다.

기존 audit Event·CallResult에는 원문 요청 헤더·본문을 추가하지 않는다.
엔진 내부 tool_input/transcripts의 토큰은 이 코어가 마스킹하지 않으며,
기존 결정대로 회차 tmpfs 격리와 수집 제외·종료 정리가 필요하다.

## 검증과 남은 활성화 조건

합성 loopback HTTP·검사 HTTPS에서 local/api 선언 각각에 대해 로그인 응답
→ caller Authorization/Cookie 재송신을 검증한다. 실제 모델/API·Bash·보존 C·
실대상·평가 러너는 실행하지 않는다. 정책 범위·예산·감사 실패·응답 보류와
기본 모드의 거부 회귀를 함께 확인한다. 합성 토큰이 코어의 audit/결과 projection에
없다는 검증은 **회차 디렉터리 전체의 canary 0건**을 대신하지 않는다.

공식 회차 전에는 신뢰 host/C 연결, 모드·백엔드의 정책 지문/회차 결속,
도구 동등성·전용 망·tmpfs 확인, 개선 후 실제 회차의 canary 0건 및
컨테이너·볼륨·망 0개 정리를 모두 확인해야 한다. 이 조건 전 활성화하지 않는다.

근거: [평가 프로토콜 §2](../evaluation/evaluation-protocol.md#2-평가-대상-고정),
[전달 지원 범위](e2-proxy-transfer-scope.md),
[DLP 경계](dlp-projection.md). 외부 LLM 정책은 병합된 #254의 사전 공지·승인을
따른다. 로컬 모델 도입 방향은 병합된 #257을 반영한
[LLM 도입·개선 계획](../llm-adoption-and-improvement-plan.md)을 따르며, 이 코어가 모델을 선택하거나
실행·서빙 검증을 대신하지 않는다.
