# Workbench URL 진단 제품

루트 `docker compose`는 이 제품 구성을 포함한다. 공식 비교 평가는 기존 루트 `Dockerfile`·`docker-compose.bench.yml`과 [평가 절차](../../docs/evaluation/official-run.md)를 사용한다. 제품 이미지·합성 수용을 공식 baseline·실모델 평가로 계산하지 않는다.

## 최초 설치

Linux Docker Engine 26 이상과 Compose 2.35 이상이 필요하다. UID 1000의 user/PID/mount/net/IPC/UTS namespace 생성이 허용되어야 한다. 준비 서비스의 고정 probe가 실패하면 운영자가 호스트 정책을 확인한다. 호스트 정책을 자동 변경하지 않는다. privileged, SYS_ADMIN, Docker 소켓, host network는 사용하지 않는다.

저장소 밖의 0700 디렉터리에 `install.json`과 임의 PostgreSQL 비밀번호 파일을 0600으로 준비한다. API 키는 설치 JSON에 넣지 않는다. 다음 주소·모델·비밀번호는 실제 승인된 값으로 바꾼다.

```json
{
  "format": "abc2lab.product-install/v1",
  "database_host": "172.31.252.10",
  "database_name": "abc2lab",
  "admin_password": "REPLACE-WITH-A-PRIVATE-ADMIN-PASSWORD",
  "target_origin": "https://approved-target.example",
  "target_ip": "192.0.2.20",
  "path_prefixes": ["/approved-readonly"],
  "allow_private_target": false,
  "nonmutating_scope_acknowledged": true,
  "valid_for_hours": 24,
  "max_runs": 3,
  "local_model": {
    "model": "operator-approved-model",
    "base_url": "http://192.0.2.30:8080/v1",
    "addresses": ["192.0.2.30"],
    "context_window_k": 128,
    "max_tokens": 2048
  }
}
```

`192.0.2.*`는 문서용 주소다. 실제 연결 가능한 고정 IP를 지정한다. 사설 대상은 `allow_private_target: true`가 필요하다. `nonmutating_scope_acknowledged`는 해당 GET/HEAD 경로가 상태를 바꾸지 않는다는 운영자의 확인이다. 자동 탐색으로 범위를 늘리지 않는다. 대상 origin은 HTTPS이며 사용자 정보·query·fragment를 포함하지 않는다.

```bash
export PRODUCT_SOURCE_SHA="$(git rev-parse HEAD)"
export PRODUCT_INSTALL_FILE=/srv/abc2lab-install/install.json
export PRODUCT_POSTGRES_PASSWORD_FILE=/srv/abc2lab-install/postgres-password
docker compose build
docker compose up -d --wait
```

Workbench는 `http://127.0.0.1:8787/`, 사용자명은 `ABC2LAB`, 비밀번호는 설치 입력의 값이다. 원격 접근은 운영자의 HTTPS 역방향 프록시로 제공한다. 기본 loopback 바인딩을 평문 LAN에 공개하지 않는다.

로그인 이름은 `ABC2LAB`이다. 기존 설치의 비밀번호와 저장된 진단을 그대로 사용하도록 JWT subject·승인 정책의 내부 관리자 식별자 `ARTEX`는 유지한다. 이전 로그인 이름 `ARTEX`는 받지 않는다.

설치자는 빈 전용 DB, 최소 권한 역할, 로그인 해시·JWT 키, E② CA·감사 키·helper pin, 대상·모델 정책, 위임·private IPC를 생성한다. 설치와 헬스체크는 모델 completion·대상 HTTP를 호출하지 않는다. 재기동은 기존 installation을 검증하고 재사용한다. API는 `product-state`의 `api` 하위 경로와 private IPC만 읽기 전용으로 마운트한다. 투영에는 API launch·설치 메타데이터·모델 launch·JWT·API 및 model-writer DSN만 있으며 CA 개인키·HMAC 감사 키·runtime DSN·실행 저널은 없다. 기존 설치도 installer 재실행 시 원본을 검증해 투영을 생성하며, 불일치한 기존 투영은 덮어쓰지 않는다. [Compose의 volume.subpath](https://docs.docker.com/reference/compose-file/services/#volumes)를 사용한다. 부분 실패의 `.installing`은 자동 삭제하지 않는다. 사용하지 않은 전용 설치인지 운영자가 확인한 뒤 새 볼륨으로 다시 설치한다.

## 사용 흐름과 외부 LLM

**새 점검 → URL 입력 → 외부 LLM 사용하기(선택) → 모델명·API 키 → 모델 연결 설정 저장 → 승인 범위 확인 → 명시적 동의 → 진단 시작.** 로컬 승인 프로필이 하나면 기본 선택한다. 키 저장과 범위 확인만으로 진단을 실행하지 않는다.

OpenRouter는 운영자가 `openrouter_manifest`에 [기존 보호 승인 JSON 형식](../../docs/openrouter-first-diagnosis.md)을 추가한다. `local_model`은 생략할 수 있다. 현재 모델은 `qwen/qwen3.5-35b-a3b`, endpoint는 `https://openrouter.ai/api/v1`로 고정된다. installer가 profile ID를 생성된 DB ID에 바인딩한다. provider slug/응답 이름, privacy/ZDR, 계정 spend cap, 요청·토큰·실제 USD 상한, 가격 상한, 만료·승인 ID를 운영자가 확인한다. 키 입력으로 이 설정을 바꿀 수 없다.

키는 인증된 `POST /api/product/model-credentials`에서 설치된 프로필의 `api_key` 열만 갱신한다. 별도 writer 역할에는 키 읽기·모델·endpoint·예산 변경 권한이 없다. 비제어 password 입력은 전송 즉시 비우며 React 상태·URL·진단 초안·local/session storage·보고서에 키를 넣지 않는다. 시작 시 외부 전송·비용 동의는 별도로 필요하다. 키 변경·응답 유실·사용량 불명은 기존 fail-closed 계약을 따른다.

유료 승인은 단회다. 한도·만료·누적 예산 소진 후에는 기존 운영자 재승인이 필요하다. marker·journal 삭제로 재승인하지 않는다. 사용자 모델 입력은 설치된 승인 모델에 한정된다.

## 실행 경계와 영속 상태

```mermaid
flowchart LR
  B[Workbench 브라우저] --> F[정적 UI + 고정 API proxy]
  F --> A[인증 + URL v2 + 키 등록]
  A -->|private Unix IPC| P[보호 preparation]
  P --> W[개량 Planner / Worker]
  W --> E[E② 격리 Bash + 전송 통제]
  E --> T[승인된 HTTPS 대상]
  W --> D[(PostgreSQL 후보·판정·보고서)]
  P --> V[(authority·receipt·audit)]
  A -->|저장 보고서 bridge| P
```

workbench에는 DB·키·제품 상태 볼륨이 없다. api는 상태 볼륨을 읽기 전용으로 사용한다. preparation만 회차와 영속 상태를 쓴다. 세 서비스는 기존 loopback API 계약 때문에 preparation의 network namespace만 공유한다. PID·capability·rootfs는 분리한다. PostgreSQL과 시험 대상은 내부 control/target 망에 있으며 DB 포트를 공개하지 않는다. 외부 모델 연결은 명시적으로 설치된 보호 정책만 허용한다.

런타임은 UID 1000, read-only rootfs, cap_drop ALL, no-new-privileges다. [준비 서비스 seccomp](seccomp-preparation.json)는 [Docker 기본 정책](https://docs.docker.com/engine/security/seccomp/)의 deny 기본값에 필요한 namespace 조합과 내부 mount·pivot만 추가한다. 원본 Moby profiles commit은 `4b8214fc4971e06172938a1f1554ccff6647ee80`; [라이선스](seccomp-preparation.LICENSE)를 포함한다. Bash helper의 기존 capability 제거·추가 seccomp·네트워크 격리는 유지한다. AppArmor가 별도로 막는 호스트에서는 운영자의 정책 검토가 필요하다.

`postgres-data`(DB)와 `product-state`(키·정책·authority·receipt·audit)를 함께 백업하고 권한을 보존한다. `target-tls`는 소유 시험 전용이다. 재기동은 `docker compose down` 후 `docker compose up -d --wait`로 수행한다. `down -v`는 영속 상태를 삭제하므로 복구 절차에 사용하지 않는다. 실행 중 강제 종료는 unknown으로 남으며 자동 재실행하지 않는다. 완료된 회차는 재기동 후 저장 보고서로 조회한다.

## 화면별 실제 API

| 화면 | 연결 |
|---|---|
| 로그인 | `/api/auth/status`, `/api/auth/login` |
| 대시보드 | URL v2 catalog·현재 receipt/status. 전체 이력 미제공 안내 |
| 새 점검 | `/api/diagnoses/v2/catalog`, `.../drafts`, `/api/product/model-credentials` |
| 자산 탐색 | 현재 진단의 서버 승인 scope. 별도 인벤토리·자동 크롤링 미지원 안내 |
| 범위 확인 | URL v2 validate·scope·budget·명시적 start |
| 점검 진행 | URL v2 status/receipt·stop, 실제 task request-approvals |
| 보고서 | 저장 run·remediation·HTML download bridge |

404/501·조회 실패·unknown은 실행을 막는다. 일반 `POST /api/tasks`로 전환하지 않는다. 일반 HTTP 관찰은 검토 후보이며 지원되는 E③ 판정이 없으면 확정 취약점으로 표시하지 않는다.

## 소유 로컬 Docker 수용

`compose.acceptance.yaml`은 시험 overlay다. Juice Shop **v15.0.0**(digest `fe8cecfec60f7856a02b52072c879b1baf73f5a97faafdeb12ed9e8eaccc0c3d`), HTTPS front, 합성 OpenAI 호환 모델만 추가한다. 제품의 개량 엔진·URL v2·E②·DB·보고서는 실제 경로다.

전용 설치에서만 `fixture_tls: true`, target origin `https://juice.test:9443`, IP `172.31.253.20`, path `/rest/products`, local model `http://172.31.252.30:8080/v1`, addresses `["172.31.252.30"]`를 지정한다. 내부망 CIDR이 호스트 네트워크와 충돌하면 운영자가 Compose와 설치 주소를 함께 조정한다.

```bash
docker compose -f deploy/product/compose.yaml -f deploy/product/compose.acceptance.yaml build
docker compose -f deploy/product/compose.yaml -f deploy/product/compose.acceptance.yaml up -d --wait
```

모델 통계는 loopback 49082 `/stats`, 대상 통계는 49081 `/stats`다. 대상 readiness는 TCP만 확인한다. `scripts/product-diagnosis-browser.cjs`는 별도 Chrome CDP와 보호된 합성 설치 입력을 사용해 실제 root Workbench를 조작한다. 시작 응답 유실 시험은 실제 서버가 수락한 응답만 버린다. 모델·진단 응답을 브라우저에서 위조하지 않는다. 실모델 수용은 실제 USD 승인과 운영 키가 별도로 준비된 뒤 수행한다.
