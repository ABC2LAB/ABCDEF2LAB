# 평가 프로파일 fingerprint 생성기

`scripts/eval/profile_fingerprint.py`는 검토된 비민감 조건 선언에서 fixture·환경·검사 범위의 SHA-256을 생성한다.
[러너 기록](run-record.md)의 `fixture_fingerprint`·`environment_fingerprint`에 사용할 형식이며,
`scope_fingerprint`는 [평가 사전검사기](preflight.md)의 범위 비교용 값으로도 쓸 수 있다.
기존 러너·채점기·수집기·사전검사기 코드를 import하거나 수정하지 않는 독립 평가 도구다.

```bash
python3 -I -B scripts/eval/profile_fingerprint.py /private/evaluation/profile.json
```

성공은 종료0과 format·지문 3개만 포함한 JSON이다. 오류는 고정 필드명·이유 코드와 종료2이며 stdout은 비어 있다.
입력값·알 수 없는 필드명은 출력하지 않는다. 잘못된 CLI 인자도 원문 대신 `cli: INVALID_ARGUMENTS`만 출력한다.
최대 64 KiB의 UTF-8 JSON 파일 하나만 읽으며 입력 파일을 바꾸지 않는다.
환경변수·네트워크·프로세스 실행·파일 쓰기·키·서명·실제 대상/모델 실행은 사용하지 않는다.

## 입력 v1

최상위 `version`, `fixture`, `environment`, `scope`와 아래 필드는 모두 명시해야 한다. 모르는 필드는 거부한다.
아래 예제는 형식 검증용 합성 선언이며 실제 계정·준비 완료·모델·이미지·성능 측정 결과가 아니다.

```json
{
  "version": 1,
  "fixture": {
    "preparation_id": "read_fixture",
    "preparation_revision": 1,
    "accounts": [
      {"label": "owner", "role": "basic"},
      {"label": "peer", "role": "basic"}
    ],
    "resources": [
      {"label": "owned_record", "type": "record", "owner": "owner", "count": 1, "shared": false, "delegated": false},
      {"label": "peer_record", "type": "record", "owner": "peer", "count": 2, "shared": false, "delegated": false}
    ]
  },
  "environment": {
    "model_tag": "example-model:fixed-tag",
    "llm_base_url": "http://127.0.0.1:11434/v1",
    "temperature": 0,
    "seed": 42,
    "target_image": "example/target:15.0.0",
    "worker_count": 1,
    "time_budget_seconds": 1200,
    "request_budget": 100
  },
  "scope": {
    "target_origin": "http://127.0.0.1:3000",
    "methods": ["GET", "HEAD"],
    "paths": ["/api/records/{object}", "/api/status"],
    "vuln_classes": ["idor", "bola"]
  }
}
```

| 프로파일 | 허용 조건 |
|---|---|
| fixture | 비민감 준비 절차 ID/revision, 논리 계정 라벨·역할, 논리 자원 라벨·종류·소유 라벨·개수·공유/위임 여부 |
| environment | 정확한 모델 태그·LLM base URL·temperature/seed·대상 이미지·worker/시간/요청 예산 |
| scope | 대상 origin·명시적 HTTP 메서드·경로 템플릿·검사 분류 라벨 |

준비 절차 ID와 revision은 평가자가 합의한 비민감 준비 명세를 가리킨다. 명세 내용이 바뀌면 revision도 바꿔야 한다.
계정·자원·역할·분류 라벨은 `[a-z][a-z0-9_]{0,63}` 형식의 논리 별칭이다. 실제 username·계정 ID·이메일·자원 ID를 넣지 않는다.
자원의 owner는 이 프로파일에 선언한 논리 계정 라벨이어야 한다. 계정·자원 배열이 모두 빈 익명 준비 조건도 명시적으로 허용한다.
revision·개수·worker·요청 예산은 1 이상 signed int64 범위 정수, seed는 0 이상 같은 범위 정수다. bool·문자열·실수로 대체할 수 없다.
shared/delegated는 bool이다. temperature는 0 이상 유한수, 시간 예산은 양의 유한수이며 null·암묵적 기본값은 허용하지 않는다.

메타데이터의 모델/이미지는 명시적 tag 또는 이미지의 SHA-256 digest를 요구하며 latest·TODO를 거부한다.
URL은 HTTP(S) loopback IP/localhost만 지원하며 인증정보·query·fragment를 거부한다. LLM URL은 base path도 허용한다.
경로는 `/`로 시작하는 비민감 정적 경로나 `{object}` 같은 전체 segment 템플릿이다. 실제 계정/자원 값·query·fragment·퍼센트 인코딩·glob·역참조는 넣지 않는다.
메서드는 GET/HEAD/OPTIONS/POST/PUT/PATCH/DELETE/TRACE 중 명시한 값만 받는다. 프로파일에 적는 행위가 실행 허가를 뜻하지 않는다.

`scope.paths`와 `scope.vuln_classes`는 절대 규칙 7에 따라 평가 정답(챌린지 목록)을 보고 선택하지 않는다. 둘은 평가 선언의
일관성을 나타내고 지문화하는 값이며, 엔진 탐색·프롬프트·도구 입력으로 전달되는 선택 목록이 아니다.

인증정보·쿠키·토큰·비밀번호·계정 원문·canary 값·원문 응답·스캔 결과·artifact 경로를 받는 필드는 없다.
회차별 run/reset/instance/execution ID, 시각·sequence, 엔진별 source SHA도 거부하며 각자의 러너 기록에서 관리한다.
별칭·모델명·경로 등 문자열이 실제로 비민감한지는 이 도구가 판정하지 않는다. 평가자가 검토한 선언만 입력하며 비밀을 별칭으로 위장해 해시하지 않는다.
실제 프로파일과 결과는 레포 밖에 보관하며 커밋하지 않는다. 엔진·LLM이 만든 임의 JSON, `.env`, 계정 파일을 입력하지 않는다.

## 정규화와 SHA-256 v1

1. 입력 전체의 필드·타입·참조를 검사한 후 정규화한다. 잘못된 입력을 일부라도 해시해 출력하지 않는다.
2. 계정·자원 배열은 label로, scope의 methods/paths/vuln_classes는 문자열로 정렬한다. 중복 라벨·항목은 거부한다.
3. temperature와 시간 예산은 정밀도를 보존한 십진 **문자열**로 만든다. 예: `0`, `0.0`, `-0.000`은 `"0"`, `1200`, `1.200e3`은 `"1200"`이다.
   JSON 소수는 Decimal로 읽고 기본 precision에 따른 반올림을 하지 않는다. 0 이외 숫자의 유효 자릿수와 exponent 절댓값은 각각 128 이내로 제한하고 초과는 거부한다.
4. URL·라벨·경로·모델명 문자열은 그대로 보존한다. localhost/IP·대소문자·끝 slash·다른 태그를 같은 값으로 추정하지 않는다.
5. 각 지문은 `{format: "abc2lab-eval-profile-v1", kind: "fixture"|"environment"|"scope", profile: 정규화된 객체}`의 JSON을 사용한다.
   key 정렬, `separators=(",", ":")`, `ensure_ascii=True`, `allow_nan=False`로 직렬화한 ASCII bytes에 SHA-256을 적용한다. 줄바꿈은 포함하지 않는다.
6. scope를 먼저 지문화하고, environment 객체에 `scope_fingerprint`를 추가한 뒤 환경 지문을 계산한다. scope가 바뀌면 환경 지문도 바뀐다.
   fixture는 독립 지문이며, 비교 시 fixture와 environment 모두 확인해야 한다.

이 문서의 합성 입력에 대한 고정 검증 벡터:

```json
{
  "format": "abc2lab-eval-profile-v1",
  "fixture_fingerprint": "6a8961b760c86d504d5b4dbb195667e0201ae81f19bf9d75df65b3d617d814c7",
  "environment_fingerprint": "a7e7b7f4d76df9cf2489eee85a9a3dfc17220788b03f2883e6d94ccdf08be2d9",
  "scope_fingerprint": "da1d4647a93b44dfedfd9418f7dd230d97691acea9fd0ca9a1e24eb7fbaf1f91"
}
```

해시 버전이나 정규화 규칙을 임의로 섞지 않는다. 다른 구현에서 만들면 위 벡터가 일치해야 한다.
중복 JSON key·비유한수·잘못된 UTF-8/JSON·과대 파일은 거부한다. 오류 메시지는 입력값을 담지 않는다.

## 연결과 검증 범위

호출자는 format을 확인하고 필요한 지문 필드만 러너 기록/사전검사기에 복사한다. 기존 기록에 format 필드를 임의 추가하지 않는다.
scope의 origin과 환경의 모델·샘플링·이미지·예산은 실제 실행 설정에도 같은 선언을 사용해야 한다. 자동 적용·서명·실행 승인 기능은 없다.
지문은 이 v1 선언과 준비 절차 revision의 일치를 나타내며 실환경·하드웨어·모델 내용·준비 완료·pre 상태를 증명하지 않는다.
실제 비교에서는 해당 조건을 별도로 확인하고, pre/post·회차 신원·원자료 출처·귀속을 신뢰 러너 기록으로 검증한다.

현재 코드 소비자는 다음과 같다.

| 소비자 | 사용하는 값 | 역할 |
|---|---|---|
| `scripts/eval/preflight.py` | 세 fingerprint와 공통 환경 필드 | baseline/ours 선언의 일관성·지문 비교만 검사하며 경로/분류를 엔진에 전달하지 않음 |
| `scripts/eval/run_record.py`·`score.py` | `fixture_fingerprint`, `environment_fingerprint` | 서명된 러너 기록의 지문 검증과 회차 사이 동일 조건 대조 |
| 평가 엔진 입력 | `scope.paths`, `scope.vuln_classes` | 현재 소비자 없음; profile fingerprint는 이 선언을 검증·해시만 함 |

합성 테스트는 고정 벡터, 정렬 불변성, 숫자 동등성과 정밀도, 조건 변경의 지문 분리, scope→환경 결속,
허용 밖 필드의 해시 전 거부, 소유 참조·중복·타입 오류, 원문 미출력·CLI 종료·파일 보존을 확인한다.
