# 평가 회차 실행 절차 (API 개발 측정·시범)

규칙의 정본은 [평가 프로토콜](evaluation-protocol.md) §5.1(반복·순서·예산·무효 회차·지표)과 §6(모델·제공자·샘플링)이다. 이 문서는 그 규칙대로 회차를
돌리는 **순서와 명령**만 적는다. 러너 설정 필드·사유 코드·근거 파일은 [러너 안내](runner.md)에 있다. 둘이 다르면 프로토콜이 이긴다.

> **11/05 결정(프로토콜 §6 측정 흐름):** 공식 비교 수치는 파이프라인이 완성된 뒤 비교 시점에 GPU 로컬로 원본·개선 후를 같은 세션에서 잰 회차만 쓴다.
> 이 문서의 OpenRouter(API) 절차는 **API 개발 측정** 세션용이며, API 회차 수치를 공식 비교표에 넣지 않는다. GPU 로컬 공식 세션은 같은 순서·도구를 쓰되
> GPU 프로파일·서빙 서버 준비를 비교 시점에 이 문서에 추가한다(후속).

## 1. 역할

| 사람 | 맡는 것 |
|---|---|
| 윤재상 | **baseline 회차 실행.** 같은 평가 머신에서 아래 5절 명령으로 돌린다(머신이 다르면 시간 예산 안의 진척·API 지연 차이가 결과에 섞인다) |
| 최민준 | 개선 후 회차 실행, 세션 준비(이미지·입력 파일·키·제공자 재조회), 시도·무효 집계, 채점, 보관, 결과 문서 |

두 사람이 한 세션 안에서 번갈아 돈다. 순서는 5절의 교대 순서를 그대로 따른다.

## 2. 결과를 보기 전에 고정하는 것

- 프로토콜 §5.1·§6, [API 측정 프로파일](profiles/official-35b.json)(파일 이름은 이력상 그대로, 프로토콜 §6에서 개발 측정용)과 근거 파일, 이 문서, [러너 설정 예시](runner.example.json)를 시범 회차 전에 dev에 머지한다.
- **작업 문구도 비교 조건이다.** 러너는 `runner.json`의 `task`를 엔진 작업 생성 요청으로 보내고 그 본문 해시를 서명된 결과에 남긴다.
  공식 작업은 예시 설정의 `task` 그대로이며 요청 해시는 아래와 같다(시험이 예시와 이 값을 묶는다). 6절 집계 도구는 회차들의 해시가 하나가 아니면 멈춘다.

  ```
  official_task_request_sha256 = 0e968b84bb712bd64ceabbc8583932c1e52dca87ee10a8d8e616d4d68abc3b8b
  ```

- 시범 뒤에는 예산(`time_budget_seconds`·`request_budget`)만 **한 번** 바꿀 수 있다(§5.1). 바꾼 커밋에 근거를 쓰고, 그 커밋에 태그를 붙인다
  (API 개발 측정 세션은 `eval-api-<YYYYMMDD>`, `eval-official-v1`은 GPU 공식 세션에 남겨 둔다). 세션의 개선 후 엔진·계측기 이미지와 **평가 도구·
  프로파일·compose·설정 예시 전부**를 그 태그 커밋의 작업 트리에서 쓴다(3절 0번). 태그 뒤에는 규칙·프로파일·작업 문구를 바꾸지 않는다.

## 3. 세션마다 머신 준비

평가 머신은 개발 머신(WSL2 Ubuntu, `docker.io` 29.1.3 네이티브 dockerd)이다. 세션을 시작하기 전에 전부 확인한다.

0. **동결 작업 트리(가장 먼저):** 이미지뿐 아니라 평가 도구·프로파일·compose·설정 예시도 태그 커밋에서 쓴다. 호출자의 현재 체크아웃에서
   도구를 실행하면 이미지는 동결 커밋인데 사전검사·실행 구성·채점 규칙은 다른 소스가 될 수 있다.

   ```bash
   BASE=$(git rev-parse 'e53a79f^{commit}')
   OURS=$(git rev-parse 'eval-api-<YYYYMMDD>^{commit}')   # 시범 때는 태그 대신 시범에 쓰는 커밋
   W=$(mktemp -d)
   git worktree add -q --detach "$W/baseline" "$BASE"
   git worktree add -q --detach "$W/ours" "$OURS"
   T="$W/ours"                                  # 평가 도구·프로파일·compose·설정 예시의 출처
   test "$(git -C "$T" rev-parse HEAD)" = "$OURS" && test -z "$(git -C "$T" status --porcelain)" && echo tools-frozen
   ```

   태그는 `git tag -a`(annotated)로 만든다. annotated 태그에 `git rev-parse <태그>`만 쓰면 커밋이 아니라 태그 객체 SHA가 나와
   아래 비교와 `--ours-sha`가 틀어지므로, `BASE`·`OURS`는 항상 `^{commit}`으로 커밋 SHA를 구한다.
   `tools-frozen`이 나오지 않으면 세션을 시작하지 않는다. 아래 명령은 전부 `cd "$T"`에서 실행한다(러너는 자기 파일 위치를 기준으로 compose를
   찾으므로 `$T`의 compose가 쓰인다). 도구 출처 커밋(`$OURS`)과 이 확인 결과를 세션 기록에 남긴다(7절).
1. **다른 일 끄기:** 다른 Claude Code 세션·개발 서버·LAN 시연 컨테이너를 끈다. **Docker Desktop을 종료하고 WSL 통합을 끈다.**
   통합을 켰다 끄면 WSL dockerd 소켓이 사라지므로 WSL 터미널에서 `sudo systemctl restart docker.socket docker`로 되살린다.
2. **Docker·리슨 확인:**

   ```bash
   docker version --format '{{.Server.Version}}'      # 29.1.3 (Desktop이면 다른 버전이 나온다)
   docker ps -q | wc -l                                # 0
   ss -ltnH | awk '{print $4}' | grep -v '^127\.\|^\[::1\]'   # 10.255.255.254:53 하나만(설정 허용 목록)
   ```

3. **제공자 재조회(§6, 세션마다):** 메타데이터가 고정 때와 같아야 세션을 시작한다. 응답은 새 빈 디렉터리에 받는다.

   ```bash
   D=$(mktemp -d) && curl -sS --max-time 20 -o "$D/endpoints.json" \
     https://openrouter.ai/api/v1/models/qwen/qwen3.5-35b-a3b/endpoints
   cd "$T" && python3 -I -B scripts/eval/provider_recheck.py "$D/endpoints.json"
   ```

   샘플링 7개와 엔진이 보내는 파라미터(`tools`·`max_tokens`)가 모두 지원 목록에 있어야 한다(게이트웨이가
   `require_parameters:true`를 넣는다). 종료 코드 0이면 출력의 응답 sha256을 세션 기록에 남긴다. 1이면 무엇이 달라졌는지 stderr에 나온다. **그 세션은 시작하지 않고** 팀에 알린다.

## 4. 입력 준비 (세션 전 1회)

레포 밖에 러너 uid 소유 0700 디렉터리를 만든다(예: `/srv/abc2lab-eval`, 키는 `/srv/abc2lab-eval-keys`). 회차 결과·로그·키는 레포에 넣지 않는다.

1. **이미지 빌드:** 엔진·계측기는 3절 0번의 동결 작업 트리(`$W/baseline`·`$W/ours`)에서 빌드한다. 웹 UI는 넣지 않는다(양쪽 같음, 평가는 API만 쓴다).

   ```bash
   for pair in "baseline $BASE" "ours $OURS"; do
     set -- $pair
     (cd "$W/$1" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/amd64/artex ./cmd/artex)
     docker build -q -t "abc2lab-eval-engine:$1-${2:0:12}" "$W/$1"
   done
   ctx=$(mktemp -d); (cd "$W/ours" && CGO_ENABLED=0 GOOS=linux go build -trimpath -o "$ctx/eval-meter" ./cmd/eval-meter)
   docker build -q -f "$W/ours/deploy/eval/meter.Dockerfile" -t "abc2lab-eval-meter:${OURS:0:12}" "$ctx"
   docker pull -q bkimminich/juice-shop:v15.0.0@sha256:fe8cecfec60f7856a02b52072c879b1baf73f5a97faafdeb12ed9e8eaccc0c3d
   docker pull -q postgres:16-alpine
   docker image inspect --format '{{.Id}}' <위 태그 4개>   # runner.json images.*.id에 넣는다
   ```

2. **`runner.json`:** `$T/docs/evaluation/runner.example.json`([예시](runner.example.json))을 복사해 `images`의 `<…>` 자리(태그·내용 ID)와 경로만 채운다. 그 밖의 값은 바꾸지 않는다.
   `runs_root`는 **세션마다 새 디렉터리**로 둔다(시범: `…/pilot/runs`, 공식: `…/official-<날짜>/runs`, 0700). 6절 집계는 runs_root 하나를 한 세션으로 본다.
3. **사전검사 선언:** 손으로 쓰지 않고 공식 프로파일에서 만든다(사전검사는 양쪽이 서로 같은지만 보므로, 같은 오타는 못 잡는다).

   ```bash
   cd "$T" && python3 -I -B scripts/eval/official_preflight.py docs/evaluation/profiles/official-35b.json \
     --baseline-sha "$BASE" --ours-sha "$OURS" --out /srv/abc2lab-eval/preflight.json
   ```

   시범과 공식은 ours 커밋이 다르므로 선언 파일도 따로 만든다(덮어쓰기는 거부된다).

4. **비밀 env 파일:** `deploy/eval/eval.secrets.env.example`을 `/srv/abc2lab-eval/eval.secrets.env`로 복사하고 0600으로 둔다. 관리자·DB 비밀번호는
   무작위로 만든다. **API 키는 본인 터미널에서만 넣는다**(채팅·명령 인자·셸 기록에 남기지 않는다):

   ```bash
   read -rs K; printf 'EVAL_LLM_API_KEY=%s\n' "$K" >> /srv/abc2lab-eval/eval.secrets.env; unset K
   ```

   관리자 비밀번호는 러너가 회차마다 한 번 묻는다(getpass). 세션이 끝나면 비밀 파일을 지우고, 필요하면 키를 폐기·재발급한다.
5. **러너 서명 키:** 엔진 감사 키와 **별도로** `cd "$T" && go run ./cmd/e2-audit-keygen --key-file /srv/abc2lab-eval-keys/runner.key`(0600, runs_root 밖). 세션 사이에 유지한다.
6. **분류 매핑:** 평가자가 검토한 매핑 JSON 하나를 양쪽이 같이 쓴다(`findings.vocabulary_path`). 원본 SHA-256이 회차 기록에 남는다.

## 5. 회차 실행

한 회차:

```bash
cd "$T" && python3 -I -B scripts/eval/runner.py --config /srv/abc2lab-eval/runner.json --preflight /srv/abc2lab-eval/preflight.json \
  --side baseline --condition "원본@35b" --repeat 0      # 개선 후는 --side ours --condition "우리@35b"
```

- 조건 이름은 쪽마다 고정이다: baseline `원본@35b`, 개선 후 `우리@35b`. `--repeat`는 그 쪽의 유효 회차 번호(0부터)다.
- 종료 코드 0 완료(서명됨), 1 무효(사유는 `runner-result.json`, 사유표는 [러너 안내](runner.md#사유-코드)), 2 설정·입력 오류(회차를 만들지 않음 — 시도로 세지 않는다).
- 회차 중에는 아무것도 하지 않는다. 끝나면 `docker ps -q | wc -l`이 0인지 본다.

**시범(채점 안 함):** baseline 1회 → 개선 후 1회(`--repeat 0`). 러너 요약의 종료 사유·전달 건수·초과분·소요 시간과 비용만 보고 예산을 정한다(§5.1).
시범 결과로 점수를 보지 않는다.

**측정 세션(API 개발 측정, 이후 GPU 공식 세션도 같음):** 조건당 유효 N=3(§5.1에서 N=5로 올리기로 했으면 5). 순서는 짝마다 먼저 도는 쪽을 바꾼다.

| 순서 | 1 | 2 | 3 | 4 | 5 | 6 |
|---|---|---|---|---|---|---|
| 쪽 | baseline | 개선 후 | 개선 후 | baseline | baseline | 개선 후 |
| `--repeat` | 0 | 0 | 1 | 1 | 2 | 2 |

**무효가 나오면:** 결과 디렉터리를 지우지 않는다. 같은 칸을 같은 `--repeat`로 다시 돌린 뒤 다음 칸으로 간다(교대 순서 유지).
한 쪽 시도가 2N(=6)을 넘으면 더 돌리지 않고 원인부터 고친다. 그 조건은 미완으로 보고한다.

| 무효 사유(예) | 먼저 볼 것 |
|---|---|
| `image_id_mismatch` | `runner.json` 이미지 ID와 `docker image inspect` 값. 다시 빌드했으면 ID가 바뀐다 |
| `*_host_condition_violation`, `window_host_*` | 3절 1·2번(다른 컨테이너·리슨). 허용 목록을 넓혀서 통과시키지 않는다 |
| `engine_settings_mismatch` | 엔진 기본 workers·모델이 선언과 다른지, 프록시 설정 |
| `llm_*`(제공자·fallback·sampling·미검증) | 3절 3번 재조회. 제공자 쪽 문제면 세션을 멈춘다 |
| `drain_timeout`, `seal_failed`, `tmpfs_full` | 같은 칸 재실행. 반복되면 원인 수정 |
| `interrupted` | 같은 칸 재실행 |

## 6. 집계·채점

세션이 끝나면 runs_root 하나를 읽어 시도·무효 집계와 채점 manifest를 만든다.

```bash
cd "$T"
python3 -I -B scripts/eval/session_runs.py /srv/abc2lab-eval/official-<날짜>/runs --repeats 3 \
  --runner-key-file /srv/abc2lab-eval-keys/runner.key --manifest /srv/abc2lab-eval/official-<날짜>/manifest.json
python3 -I -B scripts/eval/score.py /srv/abc2lab-eval/official-<날짜>/manifest.json --runner-key-file /srv/abc2lab-eval-keys/runner.key
```

- `session_runs.py`는 완료 회차의 결과 파일과 회차 기록 서명(HMAC)을 먼저 검증한다(채점기는 회차 기록만 검증하므로 결과
  파일의 작업 요청 해시는 여기서 확인된다). 그다음 쪽별 시도·유효·무효(사유별)·무효율, 시작 시각 순서, 유효 순서가 교대 순서와 맞는지를 출력한다.
  2N 초과·유효 부족·순서 어긋남은 `경고[…]`로 알린다. 서명 불일치, 형식이 틀린 결과, 쪽 안의 조건 이름 섞임, 같은 `--repeat`의
  완료 중복, 작업 요청 해시 불일치는 집계를 멈춘다. manifest에는 완료(서명) 회차만 들어간다.
- 결과 표에는 §5.1대로 조건별 시도 수·유효 수·무효 사유별 수를 함께 싣는다. 무효율은 쪽별로 따로 쓴다.
- 주 지표는 회차당 엔진 귀속 해결 수(코어 GT 9·BAC 10), 평균 ± SD다(API 개발 측정, 이후 GPU 공식 비교 공통. API 수치는 공식 비교표에 넣지 않는다). 판정 칸은 판정 레코드가 붙을 때까지 판단불가(판정 미수집)이며,
  붙으면 같은 서명 회차 묶음으로 다시 채점한다(§5.1). 익명 fixture라 IDOR 개선 효과는 주장하지 않는다.

## 7. 보관

- runs_root(서명된 회차 기록·근거 파일)와 manifest·채점 출력을 레포 밖에 그대로 보관하고, 디렉터리별 SHA-256 목록을 함께 둔다. **레포에 커밋하지 않는다**(절대 규칙 2).
- 러너 서명 키는 채점 재현에 필요하므로 회차 기록과 **다른 곳**에 보관한다.
- 세션 기록(날짜, 제공자 재조회 응답 sha256, 이미지 ID, 태그 커밋, **도구 출처 커밋과 `tools-frozen` 확인**, 집계 출력)을 결과 문서에 옮긴다.
