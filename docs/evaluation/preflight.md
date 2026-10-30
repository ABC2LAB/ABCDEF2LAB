# 평가 조건 사전검사

`scripts/eval/preflight.py`는 [평가 프로토콜](evaluation-protocol.md)에 따라 합의한 조건과 baseline·개선 후의
설정 선언을 검사한다. 열린 수집기·러너 PR에 의존하지 않으며 표준 라이브러리만 사용한다.

```bash
python3 -I -B scripts/eval/preflight.py /private/evaluation/preflight.json
```

성공은 종료 코드 0, 오류는 stderr의 고정 필드명·이유 코드와 종료 코드 2다. 입력값과 모르는 필드명은 출력하지 않는다.
최대 64 KiB의 UTF-8 JSON 파일 1개만 읽으며 네트워크·환경변수·프로세스 실행·파일 쓰기를 사용하지 않는다. `.env`나 스캔 결과를 입력하지 않는다.

## 입력

최상위는 `pins`, `baseline`, `ours` 객체만 허용한다. 아래는 형식 설명용 합성 값이다. 실제 모델 태그·SHA·fingerprint를
확인해 대체해야 하며, 이 예시의 통과를 실제 실행 준비 완료로 기록하지 않는다. 실제 설정 파일은 레포 밖에 보관한다.

```json
{
  "pins": {
    "baseline_source_sha": "1111111111111111111111111111111111111111",
    "target_image": "example/target:15.0.0"
  },
  "baseline": {
    "source_sha": "1111111111111111111111111111111111111111",
    "target_image": "example/target:15.0.0",
    "model_tag": "example-model:fixed-tag",
    "llm_base_url": "http://127.0.0.1:11434/v1",
    "temperature": 0,
    "seed": 42,
    "target_origin": "http://127.0.0.1:3000",
    "worker_count": 1,
    "time_budget_seconds": 1200,
    "request_budget": 100,
    "fixture_fingerprint": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "environment_fingerprint": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "scope_fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  },
  "ours": {
    "source_sha": "2222222222222222222222222222222222222222",
    "target_image": "example/target:15.0.0",
    "model_tag": "example-model:fixed-tag",
    "llm_base_url": "http://127.0.0.1:11434/v1",
    "temperature": 0,
    "seed": 42,
    "target_origin": "http://127.0.0.1:3000",
    "worker_count": 1,
    "time_budget_seconds": 1200,
    "request_budget": 100,
    "fixture_fingerprint": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "environment_fingerprint": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "scope_fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  }
}
```

`pins`는 평가 담당자가 합의한 값을 프로토콜에서 옮긴다. 현행 프로토콜의 대상 이미지는
`bkimminich/juice-shop:v15.0.0`, 원본 소스는 프로젝트에서 고정한
`fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`다. CLI는 입력된 pins와 대조하며 합의나 입력 출처를 인증하지 않는다.

| 필드 | 검사 |
|---|---|
| `source_sha` | 소문자 16진수 40자리. baseline은 `pins.baseline_source_sha`와 일치. 양쪽 엔진의 SHA는 달라도 됨 |
| `target_image` | 명시적 태그 또는 `@sha256:` digest, digest와 함께 적은 태그도 `latest` 금지. 양쪽 모두 `pins.target_image`와 일치 |
| `model_tag` | 명시적 `name:tag`, `latest`·미설정·TODO 거부. 양쪽의 정확한 문자열이 일치 |
| `llm_base_url` / `target_origin` | HTTP(S) loopback IP 또는 localhost. 인증정보·query·fragment 금지. target은 origin만, LLM은 base path 허용 |
| `temperature` / `seed` | 전자는 0 이상 유한수, 후자는 0 이상 정수. null·bool·암묵적 기본값 금지 |
| `worker_count` / `request_budget` | 1 이상 정수 |
| `time_budget_seconds` | 양의 유한수 |
| `fixture_fingerprint` | 비민감 준비 절차·역할·자료 조건의 SHA-256(소문자 16진수 64자리) |
| `environment_fingerprint` | 공통 비민감 환경 프로파일의 SHA-256. 비교하는 소스 SHA는 제외 |
| `scope_fingerprint` | 공통 검사 대상 범위 프로파일의 SHA-256 |

소스 SHA 이외 조건은 같아야 한다. URL도 선언 문자열을 정확히 비교하며 localhost/IP나 끝 slash 차이를 자동으로 같은 값으로 취급하지 않는다.
fingerprint는 이미 합의한 비민감 프로파일을 결정적으로 직렬화해 계산한 값을 전달한다. 인증정보나 실제 계정 원문의 해시는 금지다.
회차마다 다른 run/reset/instance/execution ID, 실제 자원 ID나 실측 pre 상태는 이 설정에 넣지 않으며 실행 후 신뢰 기록에서 검증한다.

## 경계와 검증

통과는 선언의 일관성만 뜻하며 실제 모델 존재, sampling 적용, image digest, checkout SHA, 대상 초기화, 예산 집행을 증명하지 않는다.
pins 자체의 정확성, 실환경과 선언의 일치, pre/post·해결 귀속·서명, baseline 출력 어댑터·실측 비교는 별도 검증한다.
이 CLI의 출력을 실행 허가나 E²로 사용하지 않는다. 초기 지원은 loopback과 명시적 sampling 값에 한정한다.

합성 JSON으로 누락·불일치·bool/비유한수·중복 key·잘못된 UTF-8·과대 입력·민감값 미출력·CLI 종료 코드를 검증한다.
실제 대상 앱·모델·스캔은 사용하지 않는다.
