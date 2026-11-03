# 평가 조건 사전검사

`scripts/eval/preflight.py`는 [평가 프로토콜](evaluation-protocol.md)에 따라 합의한 조건과 baseline·개선 후의
설정 선언을 검사한다. 열린 수집기·러너 PR에 의존하지 않으며 표준 라이브러리만 사용한다.

```bash
python3 -I -B scripts/eval/preflight.py /private/evaluation/preflight.json
```

성공은 종료 코드 0, 오류는 stderr의 고정 필드명·이유 코드와 종료 코드 2다. 입력값과 모르는 필드명은 출력하지 않는다.
최대 64 KiB의 UTF-8 JSON 파일 1개만 읽으며 네트워크·환경변수·프로세스 실행·파일 쓰기를 사용하지 않는다. `.env`나 스캔 결과를 입력하지 않는다.

## 입력

최상위는 `pins`, `baseline`, `ours` 객체만 허용한다. 아래는 형식 설명용 합성 값이다. 실제 모델 식별자·제공자·SHA·fingerprint를
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
    "network_mode": "dedicated_network",
    "llm": {
      "backend": "api",
      "base_url": "https://llm-api.example/api/v1",
      "model": "example-org/example-model",
      "provider": "example-provider",
      "fallbacks": false
    },
    "temperature": 0,
    "seed": 42,
    "target_origin": "http://eval-meter:8080",
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
    "network_mode": "dedicated_network",
    "llm": {
      "backend": "api",
      "base_url": "https://llm-api.example/api/v1",
      "model": "example-org/example-model",
      "provider": "example-provider",
      "fallbacks": false
    },
    "temperature": 0,
    "seed": 42,
    "target_origin": "http://eval-meter:8080",
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
`bkimminich/juice-shop:v15.0.0`이다. baseline 빌드 소스는 e53a79f(base/artex-import). upstream fdfb7a17에서 비빌드 파일 49개만 삭제했고 코드 차이는 0.
`pins.baseline_source_sha`에는 전체 SHA `e53a79fe4df0c95a5b09274de8db321fa038b00b`를 쓴다. CLI는 입력된 pins와 대조하며 합의나 입력 출처를 인증하지 않는다.

| 필드 | 검사 |
|---|---|
| `source_sha` | 소문자 16진수 40자리. baseline은 `pins.baseline_source_sha`와 일치. 양쪽 엔진의 SHA는 달라도 됨 |
| `target_image` | 명시적 태그 또는 `@sha256:` digest, digest와 함께 적은 태그도 `latest` 금지. 양쪽 모두 `pins.target_image`와 일치 |
| `network_mode` | `dedicated_network`만 지원(러너 v1, 평가 프로토콜 §2). 양쪽 일치 |
| `target_origin` | 엔진이 보는 계측기 origin: `http://<서비스 이름>:<포트>`. 서비스 이름은 소문자 DNS label 하나(`localhost`·숫자·IP 아님), 포트 명시, 경로는 없음 또는 `/`. 인증정보·query·fragment 금지 |
| `llm` | 백엔드별로 정확한 키 집합. 섞이거나 빠지면 거부. 양쪽 객체가 정확히 같아야 함(평가 프로토콜 §6) |
| `llm`(api) | `backend:"api"`, `base_url`(https, 점 있는 DNS 이름, base path 허용), `model`(제공자 모델 ID `org/model[:variant]`, `latest`·TODO 거부), `provider`(고정할 제공자 slug. OpenRouter 엔드포인트 tag처럼 `slug/변형`(예: `deepinfra/fp8`)을 한 단계까지 받으며, 변형을 붙이면 그 정밀도 엔드포인트 하나로 고정되고 기본 slug는 그 제공자의 모든 변형에 매칭된다. 공식 비교는 변형 slug 권장), `fallbacks:false`(bool `false`만) |
| `llm`(local) | `backend:"local"`, `base_url`(호스트가 `hostname`과 같음), `model`(`name:tag`, `latest` 거부), `hostname`(DNS label), `host_ip`(정규 표기의 사설 IP — `llmpolicy` 사설 대역 중 loopback(127/8·::1)을 제외한 10/8·172.16/12·192.168/16·fc00::/7. 컨테이너에서 호스트 loopback으로는 닿지 않으므로 제외) |
| `temperature` / `seed` | 전자는 0 이상 유한수, 후자는 0 이상 정수. null·bool·암묵적 기본값 금지 |
| `worker_count` / `request_budget` | 1 이상 정수 |
| `time_budget_seconds` | 양의 유한수 |
| `fixture_fingerprint` | 비민감 준비 절차·역할·자료 조건의 SHA-256(소문자 16진수 64자리) |
| `environment_fingerprint` | 공통 비민감 환경 프로파일의 SHA-256. 비교하는 소스 SHA는 제외 |
| `scope_fingerprint` | 공통 검사 대상 범위 프로파일의 SHA-256 |

소스 SHA 이외 조건은 같아야 한다. baseline과 ours의 `source_sha`가 같은 것도 허용하지만, 이는 선언된 출처가 같다는 뜻일 뿐 엔진 구현의 차이·실행 분리·비교의 유효성을 증명하지 않는다. URL·LLM 객체도 선언 값을 정확히 비교하며 끝 slash 차이 등을 자동으로 같은 값으로 취급하지 않는다. LLM 백엔드 종류가 다르면 비교를 거부한다.
fingerprint는 이미 합의한 비민감 프로파일을 결정적으로 직렬화해 계산한 값을 전달한다. 인증정보나 실제 계정 원문의 해시는 금지다.
회차마다 다른 run/reset/instance/execution ID, 실제 자원 ID나 실측 pre 상태는 이 설정에 넣지 않으며 실행 후 신뢰 기록에서 검증한다.

## 경계와 검증

통과는 선언의 일관성만 뜻하며 실제 모델 존재, sampling 적용, image digest, checkout SHA, 대상 초기화, 예산 집행을 증명하지 않는다.
pins 자체의 정확성, 실환경과 선언의 일치, pre/post·해결 귀속·서명, baseline 출력 어댑터·실측 비교는 별도 검증한다.
이 CLI의 출력을 실행 허가나 E²로 사용하지 않는다. 지원 범위는 전용 망 모드(계측기 origin), api·local LLM 백엔드, 명시적 sampling 값이다. API 키는 이 설정에 넣지 않는다(`.env`, 절대규칙 2).

합성 JSON으로 누락·불일치·bool/비유한수·중복 key·잘못된 UTF-8·과대 입력·민감값 미출력·CLI 종료 코드를 검증한다.
실제 대상 앱·모델·스캔은 사용하지 않는다.
