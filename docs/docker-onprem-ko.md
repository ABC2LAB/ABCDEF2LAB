# Docker 온프레미스 설치

이 절차는 Linux amd64에서 현재 checkout의 웹 UI, Go API, PostgreSQL 16을 실행한다.
평가용 `deploy/eval/`과 격리 실행 계약 `deploy/engine-isolated/`,
`deploy/e2-network-boundary/`을 운영 Compose로 대신하지 않는다.

## 준비

- Docker Engine과 Compose 플러그인, 이미지 빌드·PostgreSQL·브라우저 도구용 디스크 공간.
- 호스트의 `127.0.0.1:8787` 포트. 다른 장치에서 접속할 때는 인증·TLS를 갖춘 운영자 리버스 프록시를 앞에 둔다.
- checkout한 정확한 소스 SHA. 이미지 태그와 이미지 `org.opencontainers.image.revision`, `/api/health` 버전을 함께 기록한다.
- 로컬 LLM을 쓰려면 모델 서비스, DB의 활성 profile, 정확히 일치하는 운영자 pin manifest가 필요하다. 모델 서버와 대상 서비스의 주소·접근 권한은 설치자가 정한다.

빌드에는 Go 1.26.3과 Node 26 이미지가 쓰인다. 런타임에는 Bash 진단 도구와 Playwright CLI/Chromium이 들어간다.
amd64 빌드·실행을 우선 검증한다. arm64의 Go+UI 교차 빌드 단계는 확인했지만
런타임 이미지·Compose 실기동은 검증하지 않았으므로 지원 완료로 취급하지 않는다.

## 보호 설정

```sh
cp .env.example .env
chmod 600 .env
git rev-parse HEAD
```

`.env`에서 `ABC2LAB_SOURCE_SHA`를 위 SHA, `ABC2LAB_IMAGE_TAG`를 같은 SHA로 설정한다.
`POSTGRES_PASSWORD`에는 URL의 사용자 정보 위치에 안전한 영숫자 비밀번호를 설정한다. 현재 Go DSN은
Compose가 이 값을 URL에 조립하므로 예약문자를 그대로 쓰면 파싱이 달라진다.
`ARTEX_SETUP_TOKEN`에는 별도의 충분히 긴 무작위 값을 설정한다.
`ABC2LAB_POLICY_DIR`은 레포 밖 보호 디렉터리의 절대 경로로 설정한다.
이 파일과 보호 디렉터리는 Git에 추가하지 않는다.

기본 서버 정책은 `local-only`이고 manifest가 없거나 불신 파일이면 서버가 기동하지 않는다.
실제 로컬 모델을 준비했다면 [manifest 형식](contracts/local-llm-manifest.md)으로
`$ABC2LAB_POLICY_DIR/model-policy.json`을 작성한다. 아래는 **UI 기동 시험만을 위한**
예시다. 이 값으로 모델 추론이나 진단을 수행할 수 없다.

```sh
install -d -m 700 "$HOME/abc2lab-private"
cat > "$HOME/abc2lab-private/model-policy.json" <<'JSON'
{"version":1,"profiles":[{"profile_id":1,"format":"openai","model":"local:configure-before-diagnosis","base_url":"http://127.0.0.1:11434/v1","addresses":["127.0.0.1"]}]}
JSON
chmod 600 "$HOME/abc2lab-private/model-policy.json"
```

앱 컨테이너는 UID/GID 1000으로 실행한다. bind mount된 보호 디렉터리와 manifest의
호스트 소유자가 UID 1000이어야 하며 모드는 각각 정확히 0700/0600이어야 한다.
호스트 계정의 UID가 다르면 설치 관리자가 이 두 경로의 소유권을 1000:1000으로 맞춘다.
모델을 실제로 연결할 때는 예시 profile의 ID·형식·모델·URL·IP를 실제 DB profile과
로컬 서비스에 맞춰 교체하고 재시작한다. 컨테이너에서 `127.0.0.1`은 컨테이너 자신이므로
별도 모델 컨테이너/호스트 서비스에는 설치자가 연결 가능한 내부 주소를 사용한다.
manifest의 [권한·신뢰 조건](contracts/local-llm-manifest-file.md)을 그대로 지킨다.

## 빌드와 시작

```sh
docker compose config -q
docker compose build artex
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8787/api/health
curl -fsS http://127.0.0.1:8787/api/auth/status
```

브라우저에서 `http://127.0.0.1:8787/setup`을 열고 `.env`의 설정 토큰으로
최초 관리자 비밀번호를 만든다. 이후 관리자 사용자명 `ARTEX`로 로그인하여 실제 task·API를 확인한다.
내장 UI는 Go 서버와 같은 origin이므로 별도 CORS나 프런트엔드 서버가 필요 없다.
제품 빌드는 `NEXT_PUBLIC_MOCK=0`과 정적 export를 강제한다. `NEXT_PUBLIC_MOCK=1`
예시 재생 빌드와 실제 진단을 혼동하지 않는다. 작업대 화면의 데이터 소스도 확인한다.

PostgreSQL 포트와 트래픽 기록 프록시 포트는 호스트에 공개하지 않는다.
`/api/health`는 프로세스의 HTTP 응답이며 DB·모델·격리 실행의 종합 수용 판정은 아니다.
SQLite/전사 등 앱 데이터는 `appdata`, 수정 가능한 skill은 `skills`,
JWT 서명 키는 작업 자료에서 분리한 `keys`,
PostgreSQL은 `pgdata` Docker named volume에 남는다.
로그는 `docker compose logs --tail=100 artex postgres`로 확인한다.
Docker 로컬 로그 드라이버는 컨테이너가 존재하는 동안 호스트 디스크에 회전 저장한다.
컨테이너 교체 전에 아래 백업 절차로 로그를 별도 보관한다.
출력에 비밀이 섞일 수 있으므로 공유 전 마스킹한다.

## 재시작·업데이트

```sh
docker compose restart artex
docker compose stop
docker compose up -d
```

업데이트 전 볼륨을 백업하고 승인된 새 소스 커밋을 checkout한 뒤 SHA/태그를 `.env`에 반영한다.
`docker compose build artex && docker compose up -d --no-deps artex`로 이미지를 교체한다.
`latest`만으로 운영 버전을 지정하지 않는다. `docker compose down`은 컨테이너와
네트워크를 제거하지만 named volume은 유지한다. `down -v`는 데이터를 삭제하므로 사용하지 않는다.
컨테이너 안의 자체 업데이트 버튼은 이미지 기반 업데이트 절차를 대신하지 않는다.
`skills` 볼륨은 최초 기동 때 이미지의 기본 skill을 복사하지만, 이후 이미지 교체에서는
기존 볼륨이 새 파일을 가린다. 새 릴리스의 기본 skill 변경은 운영자가 백업본과
새 이미지의 `/app/skills`를 대조해 반영한다. 이 검토 없이 skill이 갱신됐다고 보지 않는다.
현재 CI는 이미지를 빌드·검사하지만 GHCR에 발행하지 않는다. 조직 패키지 권한과 실제
`ghcr.io/abc2lab/...` 경로를 검증하기 전에는 registry URL을 설치값으로 사용하지 않는다.
향후 발행 시에는 정확한 소스 SHA 또는 명시적 릴리스 태그와 이미지 digest를 함께 기록하고,
고객 내부망으로의 반입·설치는 운영자가 별도로 수행한다. GitHub Actions가 내부망 서버에 접속하지 않는다.

## 백업·복구

일관된 복사본을 위해 앱을 중지하고 DB dump와 앱 데이터 볼륨을 같은 정비 시간에 보관한다.
백업 파일은 레포 밖의 접근 제한된 디렉터리에 둔다.

```sh
docker compose stop artex
docker compose logs --no-color --timestamps artex postgres > /secure/backup/container.log
docker compose exec -T postgres sh -c 'exec pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > /secure/backup/abc2lab.sql
docker compose cp artex:/app/data /secure/backup/app-data
docker compose cp artex:/app/keys /secure/backup/keys
docker compose cp artex:/app/skills /secure/backup/skills
docker compose up -d artex
```

복구는 같은 이미지 버전의 **새 빈 Compose 프로젝트**와 빈 PostgreSQL/앱 볼륨에서 한다.
앱을 시작하지 않은 채 DB만 올려 아래처럼 SQL을 공급하고, 앱 데이터·키·skill을
각 볼륨의 원래 경로에 UID/GID 1000으로 복사한다.

```sh
docker compose up -d postgres
docker compose exec -T postgres sh -c 'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$POSTGRES_DB"' < /secure/backup/abc2lab.sql
docker compose create --no-deps artex
docker compose cp /secure/backup/app-data/. artex:/app/data/
docker compose cp /secure/backup/keys/. artex:/app/keys/
docker compose cp /secure/backup/skills/. artex:/app/skills/
docker compose up -d artex
```

복사 뒤 소유권·모드를 확인한다. 이 복구 예시는 운영 데이터로 실증하지 않았으므로
실제 도입 전 빈 프로젝트에서 버전 호환성과 로그인·데이터 일관성을 시험한다.

## 문제 확인과 진단 경계

- `docker compose config -q`: 필수 env 값과 문법 확인. 출력 전체에는 비밀번호가 들어갈 수 있다.
- `docker compose ps`: DB health와 앱 health 확인. 앱이 재시작하면 manifest 소유권·권한,
  DB DSN, 앱 볼륨 소유권을 먼저 확인한다.
- `docker image inspect abc2lab:<SHA>`: 소스 revision label 확인.
- `/setup`, 로그인, `/api/health` 확인은 진단 실행 성공을 뜻하지 않는다.

첫 통합 진단은 [기존 공식 CLI](first-diagnosis-launch.md)의
`prepare-task → prepare-intents → check → run → report`와 실제 승인 자료를 사용한다.
이 기본 Compose는 별도의 `cmd/first-controlled-diagnosis` 실행 컨테이너,
보호된 역할별 DB 권한, E② 네트워크 경계와 격리 helper의 컨테이너 배치,
실제 대상·모델 수용을 제공하지 않는다. Docker 소켓이나 호스트 특권을 앱에 주지 않는다.
따라서 기본 Compose로 UI/API/DB를 실행한 결과를 E② 전수 통제, E③ 실제 판정,
첫 진단 성공으로 보고하지 않는다. 운영 진단 전에는
[E② 배치 계약](../deploy/e2-network-boundary/README.md)과
[격리 실행 조건](../e2sandbox/README.md)을 별도로 충족·검증해야 한다.
앱 네트워크는 DB와 연결되어 있으므로 제품 전체의 외부 송신 차단을 보장하지 않는다.
