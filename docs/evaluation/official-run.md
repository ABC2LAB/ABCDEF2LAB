# 평가 회차 실행 절차 (공식 측정 — 탐색 분리 배치)

규칙의 정본은 [평가 프로토콜](evaluation-protocol.md) §5.1(반복·순서·예산·무효 회차·지표)과 §6(모델·제공자·샘플링), 그리고 [공식 측정 프로토콜 고정 초안](official-protocol-fixation-draft.md)의 확정 항목이다. 이 문서는 그 규칙대로 회차를 돌리는 **순서와 명령**만 적는다. 러너 설정 칸·사유 코드·근거 파일은 [러너 안내](runner.md)에 있다. 둘이 다르면 프로토콜이 이긴다.

공식 측정은 **탐색 분리 배치**로 돈다(2026-11-17·18 결정). B는 원본 엔진(`artex-direct/v1`, LLM 게이트웨이 경유), O는 제품(`exploration-split/v1`, 제품 안 E②·탐색 경계)이다. 둘 다 같은 계측기·관측기·대상(Juice Shop v15.0.0)을 쓴다. 이전 e2-legacy 배치 절차는 [이력 판](../archive/official-run-e2-legacy.md)에 옮겼다.

## 0. 공식 세션 시작 조건

아래가 하나라도 안 됐으면 공식 세션을 시작하지 않는다. 시범·확인 회차는 돌릴 수 있다.

| 조건 | 상태를 보는 곳 |
|---|---|
| 러너·계측기·채점기 변경 머지: 계측기 하드 상한(#734), 끊긴 호출 처리(#735), B 전용 CLI 관측 줄(#736), 엔진 끝 전 최장 무활동(#737), O E③ 상태 수집(#742), O v1.1 러너 기록(#745), 채점기 분리 배치(#746) | GitHub PR |
| 제품 E③ 감독자 연결(#738)과 E② Transport·Journal 연결. 연결 전 빌드는 O가 늘 `e3_official_not_disabled`로 무효다([러너](runner.md) 사유표) | GitHub PR |
| 멈춤 무효 T 확정: **180초**(2026-11-19 확인 쌍, 사용자 동의). O 엔진 끝 전 최장 공백(활동 사건 = 요청 시작·응답 완료) 8.6초·B 13.6초로 §16 조건(O ≤ 90초)을 만족했다 | 고정 초안 §16·§19 |
| **멈춤 무효 판정이 켜져 있음**(#756): 러너는 설정 `run.fault_stall_seconds`(T)가 있으면 B·O 회차를 같은 T로 판정해 멈춤이면 `provider_fault_stall`로 무효로 하고, 세션 집계는 판정하지 않은 완료 회차·T 섞임을 받지 않으며 쪽별 2회 이상이면 측정 전체 실패(종료 코드 3)다. 시작 전 확인: 4절 7번 `runner.json`에 `run.fault_stall_seconds: 180`(위 줄의 확정값)을 넣었고, 7절 집계를 `--allow-unjudged-fault-stall` 없이 돌린다 | 고정 초안 §2·§3·§16 |
| 이 문서·공식 분리 배치 프로파일·러너 설정이 태그 커밋에 있다(2절) | 레포 |

## 1. 역할

| 사람 | 맡는 것 |
|---|---|
| 윤재상 | **B·O 회차 전부 실행.** 자기 PC(평가 머신)에서 아래 6절 명령으로 돌리고, 그 PC의 세션 준비(이미지 빌드·키 파일·제공자 재조회·사전 점검)를 맡는다. 같은 머신에서 돌려야 시간 예산 안의 진척·API 지연 차이가 결과에 섞이지 않는다 |
| 최민준 | 절차와 입력 파일 준비, 시도·무효 집계, 채점, 보관, 결과 문서 |

## 2. 결과를 보기 전에 고정하는 값

| 항목 | 값 | 출처 |
|---|---|---|
| 요청 예산 | 300건(쪽마다 같음) | 프로토콜 §5.1, 고정 초안 §1·§10 |
| 시간 예산 | 1500초 | 같음 |
| 먼저 걸릴 상한 | 러너 요청 예산. 엔진 끝 신호는 기록만 한다 | 프로토콜 §5.1 |
| 계측기 하드 상한 | 요청 예산과 같은 값. R번째 전달 뒤 새 요청은 503으로 거부하고(`rejected_over_cap`), 대상에는 정확히 R건 이하만 닿는다 | #734 |
| 모델 호출 사후 검증 | 조회 40번 × 3초, 조회 한 번 상한 10초, 러너 대기 300초 | 고정 초안 §15, #732 |
| 경계 원자료 상한 | 4 MiB(4,194,304바이트) | 고정 초안 §16 |
| 멈춤 무효 T | **180초** — 4절 7번 `runner.json`의 `run.fault_stall_seconds` | 고정 초안 §16·§19 |
| 멈춤 무효 비율 | 쪽별 `provider_fault_stall` 무효가 그 쪽 시도 중 2회 이상이면 측정 전체 실패 | 고정 초안 §16 |
| O 모델 예산 | 2000건·$5(승인 manifest) | 고정 초안 §16 |
| `O-nomcp` | 돌리지 않는다 | 고정 초안 §16 |
| 선언된 차이 | 브라우저 MCP 노출(B 0·O 21), LLM 스트리밍(B 원본 기본값 스트리밍·O 제품 transport 비스트리밍), 엔진 설정·규칙·도구 면. 결과 보고의 통제 비용 줄에 MCP와 스트리밍을 같은 줄로 적는다 | 고정 초안 §18 |
| 반복·순서 | 쪽마다 유효 N=3, `B O O B B O` | 프로토콜 §5.1 |
| 모델·제공자·샘플링 | `qwen/qwen3.6-35b-a3b`, `primeintellect/fp8`, 프로토콜 §6 일곱 값 | 프로토콜 §6 |
| E③ | 비활성(`disabled`/`declaration_missing`만 받음) | 프로토콜 §5.1 |
| 경로 가림 키 | B·O 6회 전체가 같은 키 파일. 동결 목록에는 키 지문(`path_key_ref`)만 적는다 | 2026-11-18 결정 (a) |

- 프로파일은 [공식 분리 배치 프로파일](profiles/official-split-35b.json)이다. 공식 프로파일과 같고 예산(300·1500초)과 개선 후 승인 메서드 5개(GET·HEAD·POST·PUT·PATCH, 프로토콜 §5.1)만 다르다(시험이 묶는다).
- **작업 요청 지문은 쪽마다 하나다.** B는 엔진 작업 생성 본문(목표는 제품 공통 목표 문장으로 렌더), O는 제품 설치·등록 조건 지문이다. 세션 집계가 쪽마다 하나인지 보고, B·O 목표가 같은지는 채점기가 서명된 공통 목표 SHA로 본다.
- 시범 뒤에는 예산만 **한 번** 바꿀 수 있다(§5.1). 바꾼 커밋에 근거를 쓰고 태그(`eval-official-v1`, annotated)를 붙인다. 세션의 이미지와 **평가 도구·프로파일·compose·설정 예시 전부**를 그 태그 커밋의 작업 트리에서 쓴다(3절 1번). 태그 뒤에는 규칙·프로파일·작업 문구를 바꾸지 않는다.

## 3. 세션마다 머신 준비

평가 머신은 윤재상 PC다(2026-11-09 결정). 아래 버전은 개발 머신(WSL2 Ubuntu 24.04, 네이티브 dockerd)에서 분리 배치 회차를 실제로 돌린 값이다. 그 PC에서 처음 돌리기 전에 실측해 세션 기록에 적고, 다르면 첫 세션 전에 알린다(무료 합성 회차로 먼저 확인한다).

| 도구 | 개발 머신 실측 |
|---|---|
| Docker Engine(서버·클라이언트) | 29.1.3 |
| Docker Compose | 2.40.3 |
| buildx | 0.30.1 |
| Go | 1.26.3 |
| Python | 3.12.3 |
| OpenSSL | 3.0.13 |

1. **동결 작업 트리(가장 먼저):** 이미지뿐 아니라 평가 도구·프로파일·compose·설정 예시도 태그 커밋에서 쓴다.

   ```bash
   BASE=$(git rev-parse 'e53a79f^{commit}')
   OURS=$(git rev-parse '<세션 태그>^{commit}')   # 시범 때는 태그 대신 시범에 쓰는 커밋
   W=$(mktemp -d)
   git worktree add -q --detach "$W/baseline" "$BASE"
   git worktree add -q --detach "$W/ours" "$OURS"
   T="$W/ours"                                  # 평가 도구·프로파일·compose·설정 예시의 출처
   test "$(git -C "$T" rev-parse HEAD)" = "$OURS" && test -z "$(git -C "$T" status --porcelain)" && echo tools-frozen
   ```

   `BASE`·`OURS`는 항상 `^{commit}`으로 커밋 SHA를 구한다(annotated 태그는 태그 객체 SHA가 나온다). `tools-frozen`이 나오지 않으면 시작하지 않는다. 아래 명령은 전부 `cd "$T"`에서 실행한다.
   **환경 점검(읽기만, 세션마다):** 동결 작업 트리에서 아래를 돌린다. 이미지 빌드·pull, 컨테이너·망·볼륨 생성, 파일 쓰기를 하지 않는다. 항목마다 `PASS`/`FAIL`과 사유를 한 줄씩 내고, 하나라도 `FAIL`이면 종료 코드 1이며 세션을 시작하지 않는다(2번 '다른 일 끄기'를 마친 뒤 한 번 더 돌린다).

   ```bash
   cd "$T" && python3 -I -B scripts/eval/env_check.py --config <4절 7번 runner.json>
   ```

   - 확인 항목: Docker 서버·클라이언트 판(29.0 이상), 네이티브 dockerd(Docker Desktop 아님), Compose(2.40 이상), Go(go.mod와 같은 판), Python(3.12 이상), OpenSSL(3.0 이상), git, 메모리, Docker 루트의 남은 공간, 실행 중 컨테이너 0개, 허용 목록 밖 리슨 없음, 회차가 쓸 호스트 포트 비어 있음, 분리 배치 대역 겹침 없음, buildx, 기본 이미지를 받을 수 있음.
   - 기준값은 위 표의 개발 머신 실측과 레포 설정이다. 메모리·남은 공간 기준은 개발 머신에서 회차를 실제로 돌린 값과 공식 회차 이미지 합계라서 바닥값이다(빌드 캐시·회차 기록은 더 든다).
2. **다른 일 끄기:** 다른 Claude Code 세션·개발 서버·시연 컨테이너를 끈다. **Docker Desktop을 종료하고 WSL 통합을 끈다.** 통합을 켰다 끄면 WSL dockerd 소켓이 사라지므로 `sudo systemctl restart docker.socket docker`로 되살린다.
3. **버전·컨테이너·리슨 확인:**

   ```bash
   docker version --format '{{.Server.Version}} {{.Client.Version}}'
   docker compose version --short
   docker buildx version; go version; python3 --version; openssl version
   docker ps -q | wc -l                                         # 0
   ss -ltnH | awk '{print $4}' | grep -v '^127\.\|^\[::1\]'    # 설정 허용 목록(host_listen_allowlist)에 있는 것만
   ```

   loopback 밖 리슨이 허용 목록과 다르면 회차가 `pre_host_condition_violation`으로 무효다. WSL DNS 리졸버(`10.255.255.254:53`)처럼 그 PC에 늘 있는 리슨은 세션 전에 사유와 함께 허용 목록에 넣고 세션 기록에 적는다. 회차가 무효가 난 뒤에 허용 목록을 넓혀 통과시키지 않는다.
4. **제공자 재조회(§6, 세션마다):** 메타데이터가 고정 때와 같아야 세션을 시작한다. 응답은 새 빈 디렉터리에 받는다.

   ```bash
   D=$(mktemp -d) && curl -sS --max-time 20 -o "$D/endpoints.json" \
     https://openrouter.ai/api/v1/models/qwen/qwen3.6-35b-a3b/endpoints
   cd "$T" && python3 -I -B scripts/eval/provider_recheck.py "$D/endpoints.json"
   ```

   종료 코드 0이면 출력의 응답 sha256을 세션 기록에 남긴다. 1이면 **그 세션은 시작하지 않고** 팀에 알린다. 응답의 `primeintellect/fp8` 단가는 5절 manifest 단가 상한에 쓴다.

## 4. 입력 준비 (세션 전 1회)

레포 밖에 러너 uid 소유 0700 디렉터리를 만든다(`/srv/abc2lab-eval`, 키는 `/srv/abc2lab-eval-keys`). 세션 디렉터리는 `SESSION=/srv/abc2lab-eval/official-<날짜>`(시범은 `…/pilot`)이고, **`$SESSION/runs`도 0700으로 미리 만든다**(아니면 러너가 `runs_root: NOT_PRIVATE`로 시작하지 않는다). 회차 결과·로그·키는 레포에 넣지 않는다.

```bash
install -d -m 700 /srv/abc2lab-eval /srv/abc2lab-eval-keys "$SESSION" "$SESSION/runs"
```

1. **이미지 빌드(동결 작업 트리에서):**

   ```bash
   # B: 원본 엔진 + 도구 판 핀 패치(제품과 같은 MCP·CLI 판, 탐색 분리 배치 결정안 §4)
   git -C "$W/baseline" apply "$T/deploy/eval/baseline-tool-pins.patch"
   (cd "$W/baseline" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/amd64/artex ./cmd/artex)
   docker build -q -t "abc2lab-eval-engine:baseline-${BASE:0:12}" "$W/baseline"
   # 러너 설정 images.engine_ours 칸이 필수라 개선 후 엔진도 만든다(분리 배치 O는 제품 이미지로 돌고 이 이미지는 쓰지 않는다)
   (cd "$T" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/amd64/artex ./cmd/artex)
   docker build -q -t "abc2lab-eval-engine:ours-${OURS:0:12}" "$T"
   # O: 제품·탐색 브라우저·준비 fixture
   for target in product explore-browser acceptance-fixture; do
     case $target in product) repo=abc2lab-product;; explore-browser) repo=abc2lab-explore-browser;; acceptance-fixture) repo=abc2lab-product-fixture;; esac
     docker build -q -f "$T/deploy/product/Dockerfile" --target $target --build-arg VCS_REF=$OURS -t $repo:${OURS:0:12} "$T"
   done
   # 계측기: 빌드 커밋을 넣는다. 러너가 회차 전에 `-role describe`로 선언의 개선 후 커밋과 대조한다(다르면 시작 안 함)
   ctx=$(mktemp -d); (cd "$T" && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-X main.sourceRevision=$OURS" -o "$ctx/eval-meter" ./cmd/eval-meter)
   docker build -q -f "$T/deploy/eval/meter.Dockerfile" -t "abc2lab-eval-meter:${OURS:0:12}" "$ctx"
   docker pull -q bkimminich/juice-shop:v15.0.0@sha256:fe8cecfec60f7856a02b52072c879b1baf73f5a97faafdeb12ed9e8eaccc0c3d
   docker pull -q postgres:16-alpine
   docker image inspect --format '{{.Id}}' <위 태그들>   # runner.json images.*.id·split.image_ids에 넣는다
   ```

2. **도구 면 측정과 B 전용 CLI 목록:** B 엔진과 제품 샌드박스의 CLI·MCP 목록을 재고, B에만 있는 CLI 이름을 러너 설정 `split.cli_usage_names`에 넣는다(B 회차가 그 이름의 호출 수를 센다).

   ```bash
   cd "$T" && python3 -I -B scripts/eval/tool_surface.py --baseline "abc2lab-eval-engine:baseline-${BASE:0:12}" \
     --ours "abc2lab-product:${OURS:0:12}" --ours-browser "abc2lab-explore-browser:${OURS:0:12}" --out "$SESSION/tool-surface.json"
   ```

   보고서의 `cli_differences.baseline_only`(원본에만 있는 CLI) 목록을 그대로 옮긴다. MCP 원 목록·판이 B·O 같은지는 `common.mcp_raw_same`·`common.mcp_version_same`으로 본다(둘 다 참이어야 한다).
3. **러너 서명 키:** 엔진 감사 키와 **별도로** 만든다. 세션 사이에 유지한다.

   ```bash
   cd "$T" && go run ./cmd/e2-audit-keygen --key-file /srv/abc2lab-eval-keys/runner.key   # 0600, runs_root 밖
   ```

4. **경로 가림 키:** 세션마다 **새 디렉터리**에 32바이트 무작위 키를 0600으로 만든다. 그 세션의 B·O 6회 전체가 같은 파일을 쓴다. 이전 세션의 키는 그 세션 기록의 키 지문(`path_key_ref`)을 재현하는 데 필요하므로 덮어쓰지 않는다. 디렉터리가 이미 있거나 파일이 이미 있으면 아래 명령이 멈춘다(`mkdir`·`set -C`). 값은 어디에도 옮기지 않는다.

   ```bash
   PATH_KEY_DIR=/srv/abc2lab-eval-keys/path-$(basename "$SESSION")   # 예: …/path-official-20261127
   ( umask 077 && mkdir "$PATH_KEY_DIR" && set -C && head -c 32 /dev/urandom > "$PATH_KEY_DIR/path.key" )
   ```

5. **비밀 파일(세션 시작 때 만들고 끝나면 지운다):**
   - 비밀 env: `deploy/eval/eval.secrets.env.example`을 `/srv/abc2lab-eval/eval.secrets.env`로 복사하고 0600으로 둔다. 관리자·DB 비밀번호는 무작위로 만든다. **API 키는 본인 터미널에서만 넣는다**(채팅·명령 인자·셸 기록에 남기지 않는다).

     ```bash
     read -rs K; printf 'EVAL_LLM_API_KEY=%s\n' "$K" >> /srv/abc2lab-eval/eval.secrets.env; unset K
     ```

   - B 관리자 비밀번호 파일: 0600, runs_root 밖, 키 파일들과 다른 파일. B 회차에 `--admin-password-file`로 준다(O는 묻지 않는다).

     ```bash
     ( umask 077 && python3 -c "import secrets; print(secrets.token_urlsafe(24))" > /srv/abc2lab-eval/admin.password )
     ```

   - O 승인 manifest(0600): **O 회차마다 새 승인 ID로 다시 만든다**(보정·확인 쌍과 같은 방식). 칸은 아래 값 그대로이고, 단가 상한은 3절 4번 재조회 응답의 `primeintellect/fp8` 단가 이상이어야 한다(낮으면 첫 호출이 거절돼 모델 호출 없이 `model_usage_unknown`). `expected_provider_name`은 러너 설정 `llm_gateway.expected_provider_name`과 글자까지 같아야 한다.

     ```json
     {"version": 3, "profile_id": 1, "format": "openai", "base_url": "https://openrouter.ai/api/v1", "model": "qwen/qwen3.6-35b-a3b",
      "provider_slug": "primeintellect/fp8", "expected_provider_name": "PrimeIntellect", "approval_id": "<회차마다 새 값>",
      "expires_at": "<UTC, 회차 끝 뒤 몇 시간>", "strict_privacy_ack": true, "account_spend_cap_ack": true,
      "max_requests": 2000, "max_input_tokens": 80000000, "max_output_tokens": 8000000, "max_cost_usd_micros": 5000000,
      "max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 8192, "timeout_seconds": 120, "max_concurrent": 1,
      "max_input_price_usd_micros_per_million": <재조회 단가 이상>, "max_output_price_usd_micros_per_million": <재조회 단가 이상>,
      "max_consecutive_transient_errors": 8,
      "sampling": {"temperature": 1.0, "top_p": 0.95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repetition_penalty": 1.0, "seed": 42}}
     ```

6. **사전검사 선언:** 손으로 쓰지 않고 공식 분리 배치 프로파일에서 만든다.

   ```bash
   cd "$T" && python3 -I -B scripts/eval/official_preflight.py docs/evaluation/profiles/official-split-35b.json \
     --baseline-sha "$BASE" --ours-sha "$OURS" --out "$SESSION/preflight.json"
   ```

7. **`runner.json`:** `$T/docs/evaluation/runner.example.json`([예시](runner.example.json))을 복사해 `images`의 태그·내용 ID와 경로를 채우고, 아래 칸을 더한다. 그 밖의 값은 바꾸지 않는다.
   - `runs_root`: `$SESSION/runs`(세션마다 새 디렉터리). `path_key_file`: 4번 키(`$PATH_KEY_DIR/path.key`, 이 세션 것). `purpose`: `official`.
   - `llm_gateway.verify_attempts` 40, `verify_interval_seconds` 3, `run.llm_verify_timeout_seconds` 300(2절).
   - `run.fault_stall_seconds`: `180`(멈춤 무효 판정 T, 0절·2절). 없으면 러너가 판정하지 않고 7절 집계가 그 회차를 받지 않는다.
   - `split` 블록(제품 배치):

     ```json
     {"product_tag": "<OURS 12자리>", "product_source_sha": "<OURS>", "image_ids": {"product": "sha256:…", "explore_browser": "sha256:…", "fixture": "sha256:…"},
      "manifest_path": "/srv/abc2lab-eval/manifest.json", "target_name": "app.eval.test", "product_port": 28787, "dns_control_port": 29083,
      "explore_port": 8082, "evidence_max_bytes": 4194304, "install_valid_hours": 4, "max_runs": 2, "registration_valid_hours": 2,
      "wide_writes": true, "profile_id": "product-model-openrouter", "task_wait_seconds": 120, "poll_interval_seconds": 2,
      "boundary_stop_seconds": 30, "cli_usage_names": [<2번 cli_differences.baseline_only 목록>]}
     ```

   - `preservation` 블록(분리 배치는 공식 회차여도 필수): `{"format": "abc2lab.preservation-options/v1", "series_ref": "official-<날짜>", "comm_mode": "external_allowed", "write_mode": "approved_writes", "mcp_version": "0.0.84", "browser_image_id": "<탐색 브라우저 이미지 ID>", "execution_source": "model_run", …}` 나머지 칸은 보정·확인 쌍 설정과 같은 값으로 둔다.

## 5. 사전 점검 (첫 유료 회차 전)

모두 통과해야 첫 회차를 시작한다.

1. **설정·선언·키 파일:** 러너가 띄우기 전에 보는 것과 같은 검사를 먼저 돌린다. 오류가 나오면 그 칸을 고친다.

   ```bash
   cd "$T" && python3 -I -B -c "
   import sys; sys.path.insert(0, 'scripts/eval')
   from runner_config import load_runner_config; from runner import check_key_files
   from preflight import load_preflight, check_preflight
   c = load_runner_config('/srv/abc2lab-eval/runner.json'); check_key_files(c)
   check_preflight(load_preflight('$SESSION/preflight.json')); print('config-ok')"
   ```

2. **계측기 빌드 커밋:** 아래 출력의 `build.source_revision`이 `$OURS`와 같아야 한다(러너가 회차 전에 같은 명령으로 다시 보고, 다르면 모델을 부르기 전에 무효로 끝낸다).

   ```bash
   docker run --rm --pull never --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true \
     <계측기 이미지 ID> -role describe
   ```
3. **리슨·컨테이너:** 3절 3번을 다시 본다.
4. **키 사용량 기준:** OpenRouter 키 사용량(`GET /api/v1/key`)을 세션 기록에 적는다. 세션 중단선은 PM이 정한 금액이다.

## 6. 회차 실행

**운영 규칙: 측정 중 같은 머신에서 다른 Docker 작업 금지(2026-11-19 사용자).**
- 회차(유료·합성 모두)를 시작하기 전에, 같은 머신을 쓰는 다른 작업자(사람·자동 세션)에게 "측정 구간 시작"을 알린다.
- 회차 결과를 읽고 나면 "끝"을 알린다.
- 그 사이에는 `docker run`·`compose`, Docker를 쓰는 시험·빌드, 망·볼륨 조작을 하지 않는다.
- 이유: 구간 감시는 회차 밖 컨테이너의 생성·시작을 `window_boundary_changed`로 무효 처리한다. 2026-11-19 O 진단 회차 1건이 다른 세션의 망 없는 조사용 컨테이너 때문에 무효였다.

```bash
cd "$T"
# B(원본)
python3 -I -B scripts/eval/runner.py --config /srv/abc2lab-eval/runner.json --preflight "$SESSION/preflight.json" \
  --side baseline --condition "원본@35b" --repeat 0 --admin-password-file /srv/abc2lab-eval/admin.password
# O(개선 후): manifest를 새 승인 ID로 다시 만든 뒤
python3 -I -B scripts/eval/runner.py --config /srv/abc2lab-eval/runner.json --preflight "$SESSION/preflight.json" \
  --side ours --condition "우리@35b" --repeat 0
```

- 조건 이름은 쪽마다 고정이다: B `원본@35b`, O `우리@35b`. `--repeat`는 그 쪽의 유효 회차 번호(0부터)다.
- 종료 코드 0 완료(서명됨), 1 무효(사유는 `runner-result.json`), 2 설정·입력 오류(회차를 만들지 않음 — 시도로 세지 않는다).
- 회차 중에는 아무것도 하지 않는다. 끝나면 `docker ps -q | wc -l`이 0인지 본다.
- 순서(유효 기준, N=3):

| 순서 | 1 | 2 | 3 | 4 | 5 | 6 |
|---|---|---|---|---|---|---|
| 쪽 | B | O | O | B | B | O |
| `--repeat` | 0 | 0 | 1 | 1 | 2 | 2 |

**무효가 나오면:** 결과 디렉터리를 지우지 않는다. 같은 칸을 같은 `--repeat`로 다시 돌린 뒤 다음 칸으로 간다. 한 쪽 시도가 2N(=6)에 닿았는데 유효가 N개 미만이면 7번째를 시작하지 않고 원인부터 고친다(그 조건은 미완). 쪽별 `provider_fault_stall` 무효가 2회 이상이면 측정 전체 실패다(2절).

| 무효 사유(예) | 먼저 볼 것 |
|---|---|
| `image_id_mismatch`, `tool_surface_image_mismatch` | `runner.json` 이미지 ID와 `docker image inspect` 값. 다시 빌드했으면 ID가 바뀐다 |
| `meter_*`(빌드·표식·경로 키·하드 상한) | 5절 2번 계측기 빌드 커밋, 경로 가림 키 파일. `meter_request_cap_*`는 계측기 상한과 선언 예산이 다른 것이다 |
| `pre_host_condition_violation`, `pre_layout_violation`, `window_*` | 3절 2·3번(다른 컨테이너·리슨). 허용 목록을 넓혀서 통과시키지 않는다 |
| `llm_*`(제공자·샘플링·미검증) | 3절 4번 재조회. 미검증이면 `llm-summary.json`의 `unverified_last`를 먼저 본다(`http_404`·`no_provider` 통계 지연, `http_401`·`http_403` 키·권한, `network_error` 연결, `deadline` 조회 시한). 끊긴 호출로 증명된 미확인은 따로 센다(#735). 검증 설정을 바꿔 통과시키지 않는다 |
| `model_usage_unknown`(O) | manifest 단가 상한이 재조회 단가보다 낮은지, 제공자 이름 글자 |
| `e3_official_not_disabled`, `e3_status_not_collected`(O) | 제품 빌드에 E③ 감독자·Transport·Journal 연결이 있는지(0절). 연결 전 빌드로는 공식 O 회차를 돌릴 수 없다 |
| `observer_count_mismatch`, `record_build_failed`, `record_sign_failed` | 관측기 GET이 러너 것 외에 더 있었는지, 선언·설정 칸, 러너 서명 키 |
| `provider_fault_stall` | 제공자 장애(429·위쪽 끊김) 뒤 다음 활동 사건(요청 시작·응답 완료)까지 T초 이상, 그 사이 엔진 끝 신호 없음(진행 중 요청·스트리밍 조각은 활동 아님, #756). 같은 칸 재실행. 한 쪽이 2회 이상이면 7절 집계가 측정 전체 실패(종료 코드 3)로 멈춘다(2절) |
| `fault_stall_unjudgeable` | T가 설정됐는데 판정 재료(호출 행·시각)를 읽지 못함. 멈춤이 없다고 할 수 없어 무효다. 같은 칸 재실행 |
| `drain_timeout`, `seal_failed`, `interrupted` | 같은 칸 재실행. 반복되면 원인 수정 |

## 7. 집계·채점

```bash
cd "$T"
python3 -I -B scripts/eval/session_runs.py "$SESSION/runs" --repeats 3 \
  --runner-key-file /srv/abc2lab-eval-keys/runner.key --manifest "$SESSION/manifest.json"
python3 -I -B scripts/eval/score.py "$SESSION/manifest.json" --runner-key-file /srv/abc2lab-eval-keys/runner.key
```

- `session_runs.py`는 결과·회차 기록 서명을 먼저 검증하고, 쪽별 시도·유효·무효(사유별)·무효율·순서를 낸다. 분리 배치 세션은 작업 요청 지문이 쪽마다 하나여야 하고, 분리 배치와 엔진 직접 배치 회차가 섞이면 멈춘다. 완료 회차가 모두 같은 T로 멈춤 판정됐는지 보고(아니면 멈춤), 쪽별 `provider_fault_stall` 무효 수를 0이어도 출력하며, 한 쪽이라도 2회 이상이면 측정 전체 실패로 종료 코드 3을 내고 manifest를 쓰지 않는다(그때는 채점하지 않고 결과 문서에 측정 실패로 적는다).
- `score.py`의 첫 표가 주 지표(회차당 엔진 귀속 해결 수, 코어 GT 9·BAC 10, 평균 ± SD)다. 분리 배치 표는 엔진 설정·규칙 파일·도구 트리·LLM 스트리밍(선언된 차이)을 조건 안에서만, 샘플링·머신 사양·머신은 표 전체에서 대조한다. 통제 검증 표에서 O는 "대조 미수집 — 통제 주장 보류"다(E② 시도 대조 평가 내보내기 전). 판정 칸은 판정 미수집(판단불가)이다.
- 결과 표에는 조건별 시도 수·유효 수·무효 사유별 수를 함께 싣고, 무효율은 쪽별로 따로 쓴다.

## 8. 보관·정리

- 세션이 끝나면 비밀 env·관리자 비밀번호·manifest 파일을 지운다(`shred -u` 또는 `rm`). 필요하면 키를 폐기·재발급한다.
- runs_root(서명된 회차 기록·근거 파일)와 manifest·채점 출력을 레포 밖에 그대로 보관하고, 디렉터리별 SHA-256 목록을 함께 둔다. **레포에 커밋하지 않는다**(절대 규칙 2).
- 러너 서명 키·경로 가림 키는 채점 재현에 필요하므로 회차 기록과 **다른 곳**(`/srv/abc2lab-eval-keys`, 0700)에 보관한다. 경로 가림 키는 세션별 디렉터리(`path-<세션 이름>/path.key`)에 두고, 끝난 세션의 키 파일에는 다시 쓰지 않는다. 다음 세션은 4절 4번대로 새 디렉터리에 새 키를 만든다. 세션 기록에는 그 세션의 키 지문(`path_key_ref`)과 키 디렉터리 이름을 적어, 어느 키로 어느 기록을 재현하는지 잇는다. 러너 서명 키는 세션 사이에 유지한다(`e2-audit-keygen`은 있는 파일을 덮어쓰지 않는다).
- 세션 기록(날짜, 도구 버전 실측, 제공자 재조회 응답 sha256, 이미지 ID, `tool-surface.json`, 태그 커밋, `tools-frozen` 확인, 키 지문, 키 사용량 전후, 집계 출력)을 결과 문서에 옮긴다.

## 부록 A. e2-legacy 배치 고정값(이력)

e2-legacy 시범·이력 재현에만 쓴다. 절차 전체는 [이력 판](../archive/official-run-e2-legacy.md)에 있다. 시험이 아래 값을 예시 설정·템플릿과 묶는다.

```
official_task_request_sha256 = 0e968b84bb712bd64ceabbc8583932c1e52dca87ee10a8d8e616d4d68abc3b8b
official_e2_policy_sha256 = 285ab34ccccdc966200f3c1afcf557c73612bf34be7dc0148efdad100a8e4fd5
official_e2_policy_fingerprint_sha256 = abb0a92df713f0d4aedd1dffb2ae794e517ade234a14f8726d23e49169abe70d
official_e2_declared_differences_sha256 = af6c46a57d06ce3890d978a8f67d35987e1ae9389aac706064232ddba3d77363
```
