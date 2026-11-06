# ABC2LAB 로컬 개발 안내

프로젝트 변경을 확인하려면 현재 저장소의 소스를 실행합니다.
이 안내의 완료 범위는 개발 서버·DB·모델 연결 확인입니다. 전체 진단 성공, 외부 송신 차단, 탐지 성능 측정을 뜻하지 않습니다.

## 준비

- Go **1.26.3**: [go.mod](../go.mod) 기준
- Node.js **22.22.2 이상인 22.x 계열**과 npm: [잠금 파일](../web/package-lock.json)의 개발 의존성 기준. `jsdom`은 `^22.22.2 || ^24.15.0 || >=26.0.0`, `vitest`는 `^22.12.0 || ^24.0.0 || >=26.0.0`을 요구하므로 단순히 22 이상만으로 충분하다고 적지 않습니다.
- PostgreSQL **16** 또는 Docker
- [ABC2LAB 저장소](https://github.com/ABC2LAB/ABCDEF2LAB)의 `dev` 소스

[Dockerfile](../Dockerfile)의 Node 20.x는 미리 빌드한 바이너리를 넣는 실행 이미지의 도구 환경입니다. 이 안내의 프런트엔드 개발·시험 의존성 요구와 구분하며, 이미지 안에서 Next.js를 컴파일한다는 뜻이 아닙니다.

아래 명령은 저장소 루트에서 시작합니다. `.env`·`config.json`·키·실행 기록은 커밋하지 않습니다.
설정 파일 예시는 [config.example.json](../config.example.json), Compose 입력 예시는 [.env.example](../.env.example)입니다.

## PostgreSQL

이미 운영 중인 개발 DB가 있으면 해당 연결 정보를 사용합니다. 새 로컬 DB가 필요하면 다음처럼 실행할 수 있습니다.
비밀번호는 셸에서 입력하며 명령 인자로 직접 적지 않습니다.

```bash
export POSTGRES_USER=artex
export POSTGRES_DB=artex
read -rsp '로컬 개발 DB 비밀번호: ' POSTGRES_PASSWORD
export POSTGRES_PASSWORD
docker run -d --name abc2lab-dev-postgres \
  -e POSTGRES_USER -e POSTGRES_PASSWORD -e POSTGRES_DB \
  -p 127.0.0.1:5433:5432 postgres:16-alpine
```

`config.json`이 없을 때 예시를 복사하고, `database`의 host·port·user·password·dbname을 위 DB와 맞춥니다.

```bash
cp -n config.example.json config.json
```

예시의 `skill_dir`는 실제 로컬 `skills/` 경로로 바꾸거나 비워 기본값을 사용합니다.
`ARTEX_PG_DSN`이 설정되어 있으면 파일의 DB 설정보다 우선합니다. DSN을 사용한다면 사용자명·비밀번호의 URL 예약 문자는 인코딩해야 합니다.
DB 컨테이너를 시작한 사실과 연결 준비 완료를 구분하며 `docker exec abc2lab-dev-postgres pg_isready -U artex -d artex`로 확인합니다.

## 로컬 모델과 표시 언어

OpenAI 호환 로컬 모델 서버의 **모델 식별자와 실제 BaseURL**을 확인한 뒤 같은 셸에서 설정합니다.
Ollama를 호스트에서 실행하고 기본 포트를 쓰는 경우 BaseURL은 `http://127.0.0.1:11434/v1`입니다.
모델 이름은 설치한 값으로 정하며 특정 모델의 적합성·성능을 이 문서에서 보장하지 않습니다.

```bash
export ARTEX_LLM_PROVIDER=openai
read -rp '설치한 로컬 모델 식별자: ' ARTEX_LLM_MODEL
export ARTEX_LLM_MODEL
export ARTEX_LLM_BASE_URL=http://127.0.0.1:11434/v1
export OPENAI_API_KEY=local-placeholder
export ARTEX_LANG=ko
```

`local-placeholder`는 인증을 요구하지 않는 소유 로컬 서버 예시입니다. 인증이 필요한 서버는 로컬 설정에 실제 키를 넣고 공유하지 않습니다.
현재 환경변수 기반 `FromEnv()`는 키가 비어 있으면 LLM 설정을 활성화하지 않습니다. UI의 LLM profile 선택·설정도 함께 확인합니다.
코드에서 `ARTEX_*` 이름을 사용하므로 문서에서도 같은 이름을 유지합니다.

서버의 표시 언어는 한국어 하나입니다. `ARTEX_LANG`과 설정 파일의 `language`, 저장된 표시 언어 설정은 더 따르지 않습니다.
언어 지시는 모델 출력의 일관성을 유도하는 기능입니다. 명령·URL·코드·응답 원문을 번역하지 않습니다.
출력 상한 오류가 나면 해당 profile의 `max_tokens`를 모델 서버가 허용하는 범위에 맞춥니다.

민감한 실배포는 로컬 모델을 운영 기준으로 합니다. 로컬 모델 연결만으로 셸·MCP 등 다른 경로의 외부 송신까지 차단되었다고 판단하지 않습니다.
공개 시험 대상의 외부 API 평가는 [평가 프로토콜](evaluation/evaluation-protocol.md)을 따릅니다.

## 소스 개발 실행

프런트엔드 의존성을 설치합니다.

```bash
cd web
npm ci --include=dev
cd ..
```

DB와 모델 설정을 가진 셸에서 백엔드를 실행합니다.

```bash
export ARTEX_CORS_ORIGINS=http://127.0.0.1:5173
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

별도 터미널에서 저장소의 프런트엔드를 실행합니다.

```bash
cd web
npm run dev -- --hostname 127.0.0.1 --port 5173
```

웹 주소는 <http://127.0.0.1:5173>입니다. [Next 설정](../web/next.config.mjs)이 `/api`를 백엔드 `:8787`로 전달합니다.
개발 웹의 origin을 `ARTEX_CORS_ORIGINS`로 지정했습니다. 주소나 포트를 바꾸면 이 값도 맞춥니다.
프록시 주소는 통신 기록 설정이며, 그것만으로 모든 실행 경로의 요청 통제를 보장하지 않습니다.

두 프로세스를 함께 띄우려면 의존성 준비 후 `PORT=5173 ./dev.sh`도 사용할 수 있습니다. 개발 스크립트는 종료 시 자식 프로세스를 함께 종료하므로 실행 전 별도 셸을 준비합니다.

## 최초 접속

처음 시작하면 백엔드가 `/setup`용 설정 토큰을 로컬 기동 로그에 남깁니다. 화면에서 토큰을 입력하고 관리자 비밀번호를 설정합니다.
`ARTEX_SETUP_TOKEN` 또는 `ARTEX_ADMIN_PASSWORD`를 직접 지정하는 방식도 지원합니다. 토큰·비밀번호와 로그 원문을 공유 문서나 PR에 올리지 않습니다.
관리자 접속 성공과 대상 서비스의 시험 계정 로그인 검증은 별개입니다.

## 단일 바이너리

프런트엔드를 내장하는 빌드는 깨끗한 체크아웃에서 다음 순서로 수행합니다.

```bash
cd web
npm ci --include=dev
npm run build:static
cd ..
mkdir -p server/webui/dist
cp -R web/out/. server/webui/dist/
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex
./start.sh -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788
```

이 경우 웹 주소는 <http://127.0.0.1:8787>입니다. 기존 빌드 결과가 있는 작업 폴더에서는 오래된 페이지가 섞이지 않도록 별도 체크아웃을 사용합니다.
UI의 기반 프로젝트 자동 업데이트 경로를 ABC2LAB 배포 갱신 절차로 사용하지 않습니다. 배포 후보는 소스 SHA와 검증 결과를 고정해 확인합니다.

## Compose의 현재 역할

[docker-compose.yml](../docker-compose.yml)의 `artex` 서비스는 `ghcr.io/jiwoochris/artex-global` 이미지를 사용합니다.
`docker compose up`으로 뜬 화면에는 이 저장소에서 작성한 최신 기능이 들어 있다고 볼 수 없습니다.
현재 개발 변경의 확인은 위 소스 실행으로 진행합니다. 프로젝트 자체 이미지·배포 설정은 별도 작업입니다.

## 검증 기록

- 빌드·기동: 소스 SHA, 도구 버전, DB 연결과 UI/API 응답을 기록합니다.
- 모델 연결: 실제 profile·모델·BaseURL과 도구 호출 가능 여부를 확인합니다. 비밀값은 기록하지 않습니다.
- 진단: 승인 범위·비민감 시험 자료·계정·요청 예산을 준비하고 [통합 설계](engine-improvement-draft.md)를 따릅니다.
- 성능 평가: [고정 조건](evaluation/evaluation-protocol.md)과 실제 반복 실행이 필요합니다. 기동 확인을 baseline 완료로 기록하지 않습니다.
