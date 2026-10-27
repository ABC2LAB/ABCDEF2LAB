# ABC2LAB 로컬 개발 빠른시작 (T1.1)

> 목적: **ARTEX를 로컬에서 돌아가게 만들고, 소형 로컬 모델(예: Ollama)을 `base_url`로 연결**하는
> 최단 경로. 팀 전원(특히 Go를 아직 공부 중인 사람)의 공통 출발점이다.
> 메커니즘 설명은 [README](../README.md)의 *Configuration* 절에 있고, 이 문서는 **그걸 실제로
> 한 번 돌려보는 레시피**만 담는다.

---

## 0. 두 가지 실행 경로

| 경로 | 명령 | 용도 | Go 필요? |
|---|---|---|---|
| **A. 소스 개발** | `./dev.sh` | 엔진(Phase 2)·백엔드를 고치며 핫리로드로 본다 | 예 (Go 1.26.3 + Node) |
| **B. 프리빌트 체험** | `docker compose up` | 코드 안 건드리고 UI/동작만 빨리 본다 | 아니오 (Docker만) |

Go 공부 중이라 일단 "돌아가는 화면"만 보고 싶으면 **경로 B**, 엔진을 만질 거면 **경로 A**.

---

## 1. 사전 준비

- **경로 A**: Go `1.26.3`(`go.mod` 기준), Node 20+, 로컬 Postgres(아래 1-1), `git`
- **경로 B**: Docker / Docker Compose 만

### 1-1. Postgres (경로 A 전용)

경로 B의 `docker compose`는 Postgres를 **컨테이너 내부**에서 자동으로 띄우므로 이 단계가 필요 없다.
경로 A(`dev.sh`)는 호스트에서 접속 가능한 Postgres가 있어야 한다. 한 줄로 띄운다:

```bash
docker run -d --name artex-pg \
  -e POSTGRES_USER=artex -e POSTGRES_PASSWORD=artex -e POSTGRES_DB=artex \
  -p 5433:5432 postgres:16-alpine
```

그리고 백엔드가 이 DB를 보도록 DSN을 지정한다(`config.json`보다 환경변수가 우선):

```bash
export ARTEX_PG_DSN='postgres://artex:artex@127.0.0.1:5433/artex?sslmode=disable'
```

> 포트 5433은 `config.example.json`의 기본값과 맞춘 것이다. 5432가 비어 있으면 `-p 5432:5432`로
> 바꾸고 DSN도 맞춰도 된다.

---

## 2. 소형 로컬 모델 연결 (Ollama 예시)

ARTEX는 `ARTEX_LLM_PROVIDER=openai`일 때 **OpenAI 호환 엔드포인트**로 요청한다
(`agent/provider.go`: openai 포맷은 `base_url` 뒤에 `/chat/completions`를 붙인다). Ollama가 바로 이 호환 API를 제공한다.

```bash
# 모델 받기 (한 번만)
ollama pull qwen2.5:7b        # 또는 llama3.1:8b 등

# ARTEX가 쓸 환경변수
export ARTEX_LLM_PROVIDER=openai
export ARTEX_LLM_MODEL=qwen2.5:7b
export ARTEX_LLM_BASE_URL=http://localhost:11434/v1   # ← 경로 A(소스)
export OPENAI_API_KEY=ollama                          # ← 더미. 비면 LLM 비활성(아래 주의 2)
```

UI에서 설정해도 된다: **시스템 → LLM** 화면에서 provider=openai, base_url·model·아무 api_key.

### ⚠️ 주의 1 — 컨테이너에서 호스트 Ollama 부르기 (경로 B)

`docker compose`(경로 B)로 artex를 컨테이너에서 띄우면 `localhost`는 **컨테이너 자신**을 가리킨다.
호스트의 Ollama에 닿으려면 `.env`에 이렇게 쓴다:

```dotenv
ARTEX_LLM_PROVIDER=openai
ARTEX_LLM_MODEL=qwen2.5:7b
ARTEX_LLM_BASE_URL=http://host.docker.internal:11434/v1
OPENAI_API_KEY=ollama
```

> Linux/WSL2의 Docker에서 `host.docker.internal`이 안 잡히면, compose의 `artex` 서비스에
> `extra_hosts: ["host.docker.internal:host-gateway"]`를 추가하거나 호스트 IP를 직접 쓴다.

### ⚠️ 주의 2 — `OPENAI_API_KEY`는 비우면 안 됨

`agent/provider.go`의 `FromEnv()`는 API 키가 빈 문자열이면 LLM 설정을 **무시**한다(`return …, false`).
Ollama는 키를 검사하지 않지만 ARTEX는 "뭔가 들어있을 것"을 요구하므로 `ollama` 같은 **더미 값**을 넣는다.

### ⚠️ 주의 3 — `max_tokens` 400 에러

OpenAI 호환 경로는 출력 상한 기본이 32,768이라, 그대로 두면 모델에 따라
`400 (max_tokens is too large)`가 난다(README의 *Token-cap pitfall*). **LLM 설정 화면에서 그 프로필의
`max_tokens`를 16,384 이하**(소형 모델이면 4,096~8,192 권장)로 내린다.

---

## 3. 실행

### 경로 A — 소스 개발

```bash
# 1-1의 Postgres와 2의 LLM 환경변수를 export 한 같은 셸에서
./dev.sh
# 백엔드 :8787 / 트래픽 프록시 :8788 / 프런트엔드 http://localhost:5173
```

브라우저에서 <http://localhost:5173> 접속.

### 경로 B — 프리빌트 체험

```bash
cp .env.example .env          # POSTGRES_PASSWORD, 2의 LLM 값, host.docker.internal base_url 채우기
docker compose up             # postgres + artex 함께 기동
# 웹 http://localhost:8787
```

### 3-1. 최초 1회 — 관리자 계정 (/setup)

첫 기동 시 콘솔 로그에 무작위 **설정 토큰**이 찍힌다. `/setup` 화면에서 그 토큰을 입력해 관리자
비밀번호를 만든다. (경로 B: `docker compose logs artex`로 확인.) 미리 정하려면 `ARTEX_SETUP_TOKEN`
또는 `ARTEX_ADMIN_PASSWORD`(8자+)를 환경에 준다.

---

## 4. 솔직한 한계 (기대치 맞추기)

- 이 단계의 목표는 **"파이프라인이 끝까지 돈다"를 확인**하는 것이지, 좋은 탐지 성능을 내는 게 아니다.
  7~8B급 로컬 모델은 자율 펜테스트 추론을 제대로 못 할 가능성이 높다. ARTEX는 내부 추론 프롬프트를
  원문(중국어)으로 유지한 채 프런티어 모델에 맞춰져 있다. **벤치(Phase 측정)는 프런티어 모델로** 돌린다.
- 로컬 모델 + 클라우드 키 없음은 외부 송신을 크게 줄여주지만, 그 자체가 **완전 로컬 모드(T1.3)는 아니다**.
  셸 도구·타깃 트래픽 등 다른 egress 경로 차단은 T1.3에서 별도로 다룬다. 이 문서는 LLM 연결까지만.

---

## 5. 트러블슈팅

| 증상 | 원인/조치 |
|---|---|
| 로그인/LLM 화면에서 LLM이 비활성 | `OPENAI_API_KEY`가 빔 → 더미 값 넣기(주의 2) |
| `400 max_tokens is too large` | 프로필 `max_tokens`를 16,384 이하로(주의 3) |
| 컨테이너에서 Ollama 연결 실패 | `localhost` 대신 `host.docker.internal`(주의 1) |
| 경로 A에서 DB 연결 실패 | `ARTEX_PG_DSN` 미설정/포트 불일치(1-1) |
| 브라우저 CORS 거부 | 내장 UI는 동일 출처라 보통 무관. `next dev`는 `ARTEX_CORS_ORIGINS`에 `http://localhost:5173` 허용 |
