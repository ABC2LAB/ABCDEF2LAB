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
| 측정 PC(최민준 PC) 확인([개발 계획](../development-plan.md#공식-측정-gono-go-조건) Go 조건 4): 이 PC 환경 점검(공식 runner.json) + 통합 뒤 파일럿. **환경 점검 통과(2026-11-20)**: 공식 분리 배치 예시(4절 7번)로 3절 환경 점검 17/17(`power` 포함). 같은 날 앞선 실행은 `containers`만 FAIL(그때 돌던 VAmPI 검증 회차 컨테이너, 측정 때 0). **통합 뒤 파일럿 한 쌍 남음(조건 4는 아직 다 채우지 않음)**. 이전 측정 PC(윤재상 PC) 점검은 부록 B 기록 | 3절·5절 |
| 이 문서·공식 분리 배치 프로파일·러너 설정이 태그 커밋에 있다(2절) | 레포 |
| **시작 시간대·제공자 상태(2026-11-22 사용자, 운영 절차 — 평가 의미는 안 바꿈)**: **공식 세션은 한국 시간 14~18시에 시작**한다(`--start-window-kst 14-18`). 파일럿은 시작 시간대 제한을 걸지 않고 준비되는 대로 돌린다. 파일럿 시작 조건은 남은 PR이 전부 머지된 최종 dev와 동결 기록 준비다. 동결·파일럿·공식 세션의 순서는 아래 "동결과 파일럿의 순서"를 따른다(2026-11-22의 "오전 파일럿 → 동결 → 14시 공식 측정" 당일 순서는 2026-11-23 결정으로 대체됐다). 파일럿·공식 세션 모두 시작 직전 같은 모델·제공자로 짧은 호출 10번을 보내 **429가 하나라도 나면 30분 뒤 다시 점검하고 시작을 미룬다**(`--provider-probe`). 11/21~22 밤 OpenRouter 429가 10번 중 8번까지 나 회차가 `planning_failed`로 끝났다. 점검 결과는 세션 기록에 남는다 | 6절 세션 실행 스크립트 |
| **머신 여유(2026-11-22 사용자)**: 회차마다 시작 전 메모리 여유 점검(`--heavy-check <heavy_ok.sh>`: Linux available 3GB 이상 + Windows 커밋 여유 5GB 이상)이 통과해야 시작한다. 11/22 밤 Windows 호스트 메모리 고갈로 WSL이 멈춰 회차가 날아갔다 | 6절 |

**동결과 파일럿의 순서(2026-11-23 사용자 결정):** 동결과 파일럿은 공식 세션 전날에 하고, 공식 세션은 다음 날 한다. 이번 공식 측정은 12/8에 ①~④, 12/9에 ⑤다.

0. **Go/No-Go 1단계 판정**(①보다 먼저, 12/8 오전): [개발 계획](../development-plan.md#공식-측정-gono-go-조건)의 조건 1·2·5·6과 조건 4의 환경 점검을 최종 SHA로 확인한다. Go면 ①로 간다. ③ 파일럿이 조건 3과 조건 4의 "통합 뒤 파일럿"을 함께 채우는 2단계 판정이다(실패하면 No-Go). No-Go면 고쳐서 12/10 오전에 마지막으로 다시 판정한다(Go면 12/10에 ①~④, 12/11에 ⑤). 12/10 재판정이 No-Go이거나 12/11 공식 세션을 마치지 못하면 VAmPI 예비 측정으로 발표한다.

1. **코드 고정**: 그날의 dev 머리를 동결 SHA로 정한다. 이 뒤로 공식 세션이 끝날 때까지 dev에 머지하지 않는다(긴급 수정이 필요하면 멈추고 ①부터 다시 한다).
2. **이미지 빌드·설정 준비**: 동결 SHA로 4절의 이미지·사전검사 선언·러너 설정을 만든다. 보존율 제외 규칙처럼 결과를 보기 전에 고정할 입력도 이때 만든다.
3. **Juice Shop 파일럿 한 쌍(장비 검증용)**: 공식과 같은 이미지·같은 설정·같은 시작 명령으로 B·O 한 쌍을 돈다. 결과 세부는 보지 않고 개발에 쓰지 않으며, 파일럿 뒤에는 이미지·설정 값·하네스·보존율 입력을 바꾸지 않는다([개발 계획](../development-plan.md) 원칙). **예외는 화면 관찰 설정 하나뿐이다**(2026-11-25 사용자 결정, [동결 기록 §6.1](freeze-record.md)): 관찰 켬·끔은 12/7 밤 VAmPI 6회차 결과로 먼저 정해 파일럿과 공식에 같이 쓴다. 12/7에 O 세 번이 모두 관찰되지 않았으면 파일럿·공식 모두 끔(`OBSERVE=0`, 러너 설정에 `split.observer_password_file` 칸 없음)이다. 모두 관찰됐으면 파일럿을 켜고 돌린다. 파일럿의 O 관찰이 실패하면 파일럿을 다시 돌리지 않고 공식만 관찰을 끄며, 동결 기록에 그 사실과 이유를 한 줄 남긴다. 관찰은 회차 조건이 아니고 서명 설정 지문 밖이라(`runner_split_config.py` 머리말) 이 차이만 허용한다.
4. **동결 기록·선언**: [동결 기록](freeze-record.md)에 SHA·이미지 digest·지문·설정 sha256·파일럿 회차 ID를 채우고, 정본과 대조한 뒤 사용자 확인을 받아 동결을 선언한다.
5. **공식 세션**: 다음 날 한국 시간 14~18시에 시작한다. 이미지는 다시 빌드하지 않고 파일럿과 같은 것을 쓴다(4절 1번 "이미지 재사용").

2026-11-23에 돈 Juice Shop 파일럿은 그날의 dev(`dd3385a2`)로 한 장비 검증이고, 12/8에 최종 이미지로 도는 파일럿과 다른 것이다. 공식 세션의 근거가 되는 파일럿은 12/8의 것이다(동결 기록이 둘을 따로 적는다).

## 1. 역할

| 사람 | 맡는 것 |
|---|---|
| 최민준 | **B·O 회차 전부 실행**(측정 PC = 최민준 PC, 2026-11-20 결정·팀 합의). 그 PC의 세션 준비(이미지 빌드·키 파일·제공자 재조회·환경 점검·사전 점검), 6절 공식 회차 실행 스크립트, 시도·무효 집계, 채점, 보관, 결과 문서. 같은 머신에서 돌려야 시간 예산 안의 진척·API 지연 차이가 결과에 섞이지 않는다 |
| 윤재상 | (2026-11-20까지 측정 머신 담당이었다. 그 PC 점검 기록은 부록 B) |

## 2. 결과를 보기 전에 고정하는 값

| 항목 | 값 | 출처 |
|---|---|---|
| 요청 예산 | 300건(쪽마다 같음) | 프로토콜 §5.1, 고정 초안 §1·§10 |
| 시간 예산 | 1800초(2026-11-25 사용자 결정, 1500초에서 바꿈) | 같음, 고정 초안 §10 |
| 먼저 걸릴 상한 | 러너 요청 예산 | 프로토콜 §5.1 |
| 끝 신호 뒤 무활동 유예 | 정상 완료 끝 신호 뒤 T(180초) 동안 모델 호출·대상 요청이 없으면 러너가 회차를 끝낸다(B·O 같음, O는 제품 정리 끝 필요). 끝낸 시각에 진행 중이던 모델 호출이 있으면 무효 | 프로토콜 §5.1, 고정 초안 §4 |
| O 설치 회차 실행 시간 | 2100초(시간 예산 + 300초, 설치 입력 `runtime_limits.workflow.run_seconds`) | 고정 초안 §10 |
| 계측기 하드 상한 | 요청 예산과 같은 값. R번째 전달 뒤 새 요청은 503으로 거부하고(`rejected_over_cap`), 대상에는 정확히 R건 이하만 닿는다 | #734 |
| 모델 호출 사후 검증 | 조회 60번 × 10초(계측기 조회 시한 610초), 조회 한 번 상한 10초, 러너 대기 700초 | 고정 초안 §15, #732, [러너](runner.md) 11/25 근거 |
| 경계 원자료 상한 | 4 MiB(4,194,304바이트) | 고정 초안 §16 |
| 멈춤 무효 T | **180초** — 4절 7번 `runner.json`의 `run.fault_stall_seconds` | 고정 초안 §16·§19 |
| 멈춤 무효 비율 | 쪽별 `provider_fault_stall` + `provider_fault_end` 무효가 그 쪽 시도 중 2회 이상이면 측정 전체 실패 | 고정 초안 §16·§26 |
| 러너 상한 전에 일찍 끝난 회차 | 제공자 장애 끝 = 무효(멈춤 상한에 같이 셈), 설정 오류 = 시도 아님·세션 멈춤, 그 밖 = 유효(끝 상태 기록). 세션 요약에 회차 목록 | 평가 프로토콜 §5.1, 고정 초안 §26 |
| O 모델 예산 | 2000건·$5(승인 manifest) | 고정 초안 §16 |
| `O-nomcp` | 돌리지 않는다 | 고정 초안 §16 |
| 선언된 차이 | 브라우저 MCP 노출(B 0·O 21), LLM 스트리밍(B 원본 기본값 스트리밍·O 제품 transport 비스트리밍), 엔진 설정·규칙·도구 면. 결과 보고의 통제 비용 줄에 MCP와 스트리밍을 같은 줄로 적는다 | 고정 초안 §18 |
| 반복·순서 | 쪽마다 유효 N=3, `B O O B B O` | 프로토콜 §5.1 |
| 모델·제공자·샘플링 | `qwen/qwen3.6-35b-a3b`, `primeintellect/fp8`, 프로토콜 §6 일곱 값 | 프로토콜 §6 |
| E③ | 비활성(`disabled`/`declaration_missing`만 받음) | 프로토콜 §5.1 |
| E② v3 통제 대조 | **끔(선언된 한계)**: 4절 7번 `runner.json`에 `split.e2_v3`를 넣지 않는다. O 회차의 E② v3 저널 검증(감사 키·봉인 head 대조)은 수집·채점하지 않는다 | 고정 초안 §27, 2026-11-21 사용자 |
| 경로 가림 키 | B·O 6회 전체가 같은 키 파일. 동결 목록에는 키 지문(`path_key_ref`)만 적는다 | 2026-11-18 결정 (a) |

- 프로파일은 [공식 분리 배치 프로파일](profiles/official-split-35b.json)이다. 공식 프로파일과 같고 예산(300·1800초)과 개선 후 승인 메서드 5개(GET·HEAD·POST·PUT·PATCH, 프로토콜 §5.1)만 다르다(시험이 묶는다).
- **작업 요청 지문은 쪽마다 하나다.** B는 엔진 작업 생성 본문(목표는 제품 공통 목표 문장으로 렌더), O는 제품 설치·등록 조건 지문이다. 세션 집계가 쪽마다 하나인지 보고, B·O 목표가 같은지는 채점기가 서명된 공통 목표 SHA로 본다.
- 시범 뒤에는 예산만 **한 번** 바꿀 수 있다(§5.1). 바꾼 커밋에 근거를 쓰고 태그(`eval-official-v1`, annotated)를 붙인다. 세션의 이미지와 **평가 도구·프로파일·compose·설정 예시 전부**를 그 태그 커밋의 작업 트리에서 쓴다(3절 1번). 태그 뒤에는 규칙·프로파일·작업 문구를 바꾸지 않는다.

## 3. 세션마다 머신 준비

평가 머신은 **최민준 PC**(개발 머신, WSL2 Ubuntu 24.04, 네이티브 dockerd)다(2026-11-20 결정·팀 합의). 근거: 측정값(멈춤 무효 T, 예산, 조회 시한)을 전부 이 PC의 보정·확인 회차로 정했고, 측정 중 결함을 바로 고칠 수 있다. 이전 결정(윤재상 PC, 2026-11-09)과 그 PC 점검 결과는 부록 B에 기록으로만 둔다. 아래 버전은 이 PC에서 분리 배치 회차를 실제로 돌린 값이다. 세션마다 환경 점검으로 다시 확인하고 세션 기록에 적는다.

| 도구 | 이 PC 실측 |
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
   **환경 점검(읽기만, 세션마다):** 동결 작업 트리에서 아래를 돌린다. 이미지 빌드·pull, 컨테이너·망·볼륨 생성, 파일 쓰기를 하지 않는다. 항목마다 `PASS`/`FAIL`/`SKIP`과 사유를 한 줄씩 낸다. 하나라도 `FAIL`이면 종료 코드 1이며 세션을 시작하지 않는다. `SKIP`(종료 코드 2)은 설정이 모자라 점검하지 못한 것이라 안내대로 다시 돌린다(2번 '다른 일 끄기'를 마친 뒤 한 번 더 돌린다).

   ```bash
   cd "$T" && python3 -I -B scripts/eval/env_check.py --config <4절 7번 runner.json(공식 분리 배치 예시를 채운 파일)>
   ```

   - 확인 항목: Docker 서버·클라이언트 판(29.0 이상), 네이티브 dockerd(Docker Desktop 아님), Compose(2.40 이상), Go(go.mod와 같은 판), Python(3.12 이상), OpenSSL(3.0 이상), git, 메모리, Docker 루트의 남은 공간, 절전·Windows 업데이트(`power`: 전원 연결 때 절전·최대 절전 '안 함', 재부팅을 기다리는 업데이트 없음, 업데이트 일시 중지 중), Docker 자동 업데이트(`docker_updates`: Windows의 Docker Desktop 꺼짐, 이 배포판 dockerd 엔진 패키지 고정 또는 자동 업그레이드 꺼짐), 실행 중 컨테이너 0개, 허용 목록 밖 리슨 없음, 회차가 쓸 호스트 포트 비어 있음, 분리 배치 대역 겹침 없음, buildx, 기본 이미지를 받을 수 있음.
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

   **이미지 재사용:** 같은 동결 SHA로 파일럿 때 빌드한 이미지를 공식 세션에 재사용할 수 있다. 다시 빌드하지 않고 `runner.json`의 이미지 ID를 파일럿과 같은 값으로 둔다. 러너가 회차마다 설정의 이미지 ID와 로컬 이미지를 대조하고 다르면 `image_id_mismatch`로 멈춘다. 파일럿과 공식 세션이 같은 이미지여야 파일럿이 공식 조건을 확인한 것이 된다. 동결 SHA가 바뀌면 다시 빌드한다.

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

   - O 승인 manifest(0600): **O 회차마다 새 승인 ID로 다시 만든다**(보정·확인 쌍과 같은 방식). 칸은 아래 값 그대로이고, 동시 모델 호출 `max_concurrent`는 제품 OpenRouter 기본값과 같은 4다(2026-11-25 사용자 결정 — 1은 11/19 이 절차를 다시 쓰며 넣은 예시 값이라 근거가 없고 통제 이유도 아니다. 모델 예산 게이트는 호출마다 예약·정산해서 동시 호출을 넓혀도 회계가 갈리지 않는다. 11/24 VAmPI 6회차에서 1로 묶인 O만 모델 호출이 한 번에 하나였고 B는 동시에 불렀다, [동결 기록 §2](freeze-record.md)), 모델 호출 시간 상한 `timeout_seconds`는 170초다(사용자 2026-11-22: 원본 B는 스트리밍이라 호출 시간 상한이 없고, PrimeIntellect가 느릴 때 초당 22토큰까지 떨어져 120초면 2,640토큰이 넘는 O 호출만 잘렸다. 러너 멈춤 기준 180초보다 짧게 둔다). **가격 상한은 고정 숫자가 아니라 규칙이다: 세션 시작 직전 재조회 단가 × 1.5**(사용자 2026-11-22, 제공자 PrimeIntellect 유지. 3절 4번 '재조회 단가 이상' 절차를 구체화한 것이다). `official_session.py --provider-probe`가 세션 시작 때 엔드포인트 표에서 `primeintellect/fp8` 단가를 다시 읽어 상한(올림 정수)을 정하고, 그 세션의 O manifest마다 그 값을 쓴다. 단가와 정한 상한은 세션 기록 `price_caps`에, 회차마다 쓴 상한은 `o_manifest_caps`에 남는다. 예: 11/22 단가 0.19/1.70 → 285000 / 2550000. 단가보다 낮은 상한이면 첫 호출이 거절돼 모델 호출 없이 `model_usage_unknown`이 된다. `expected_provider_name`은 러너 설정 `llm_gateway.expected_provider_name`과 글자까지 같아야 한다.

     ```json
     {"version": 3, "profile_id": 1, "format": "openai", "base_url": "https://openrouter.ai/api/v1", "model": "qwen/qwen3.6-35b-a3b",
      "provider_slug": "primeintellect/fp8", "expected_provider_name": "PrimeIntellect", "approval_id": "<회차마다 새 값>",
      "expires_at": "<UTC, 회차 끝 뒤 몇 시간>", "strict_privacy_ack": true, "account_spend_cap_ack": true,
      "max_requests": 2000, "max_input_tokens": 80000000, "max_output_tokens": 8000000, "max_cost_usd_micros": 5000000,
      "max_input_tokens_per_call": 262144, "max_output_tokens_per_call": 8192, "timeout_seconds": 170, "max_concurrent": 4,
      "max_input_price_usd_micros_per_million": <세션 시작 때 재조회 단가 × 1.5로 바뀜>, "max_output_price_usd_micros_per_million": <세션 시작 때 재조회 단가 × 1.5로 바뀜>,
      "max_consecutive_transient_errors": 8,
      "sampling": {"temperature": 1.0, "top_p": 0.95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repetition_penalty": 1.0, "seed": 42}}
     ```

6. **사전검사 선언:** 손으로 쓰지 않고 공식 분리 배치 프로파일에서 만든다.

   ```bash
   cd "$T" && python3 -I -B scripts/eval/official_preflight.py docs/evaluation/profiles/official-split-35b.json \
     --baseline-sha "$BASE" --ours-sha "$OURS" --out "$SESSION/preflight.json"
   ```

7. **`runner.json`:** `$T/docs/evaluation/runner.official-split.example.json`([공식 분리 배치 예시](runner.official-split.example.json))을 `$SESSION/runner.json`으로 복사하고 0600으로 둔다. `<…>`로 표시된 칸만 채우고 그 밖의 값은 바꾸지 않는다(예시를 채우면 러너 설정 검사를 통과한다는 것을 시험이 고정한다).
   - **채우는 칸(세션마다)**
     - `images`의 태그·내용 ID와 `split.product_tag`·`product_source_sha`·`image_ids`, `preservation.browser_image_id`: 1번에서 빌드한 이미지(`docker image inspect --format '{{.Id}}' <태그>`). 동결 커밋은 `OURS`다.
     - `path_key_file`: 4번 키 경로(`$PATH_KEY_DIR/path.key`). `preservation.series_ref`: `official-<세션 날짜>`.
     - `split.cli_usage_names`: 2번 도구 면 측정의 `cli_differences.baseline_only` 이름 목록.
     - 경로(`runs_root`·`secrets_env_file`·`runner_key_path`·`findings.vocabulary_path`·`split.manifest_path`): 예시는 `/srv/abc2lab-eval…`이다. 그 PC에서 다른 디렉터리를 쓰면 이 경로 칸만 바꾼다. `runs_root`는 세션마다 새 디렉터리다.
   - **선택(O 회차를 화면으로 지켜볼 때)**: `split.observer_password_file`에 회차 관리자 로그인 파일 경로를 적는다(예: `/srv/abc2lab-eval-keys/observer/admin.txt`, 부모 디렉터리는 0700으로 미리 만든다). 러너가 O 회차마다 그 회차의 아이디·비밀번호를 0600으로 쓰고 회차가 끝나면 지운다([평가 러너](runner.md)). 화면은 Windows 브라우저에서 `http://localhost:<split.product_port>`(예시 28787)이다. 로그인은 O 회차가 시작된 뒤(러너가 제품 작업을 시작한 뒤) 한다. 로그인 실패 제한은 접속 주소별이라 러너와 같은 주소로 잡히므로, 실패하면 파일을 다시 열어 확인하고 되풀이하지 않는다. **공식 O 3회는 모두 관찰하거나 모두 관찰하지 않는다**(2026-11-20 사용자). 세션 중에 이 칸을 넣거나 빼지 않는다. 회차 결과(`runner-result.json`)의 `observer_login_file`(예/아니오만, 경로·값 없음)로 확인한다.
   - **넣지 않는 것**: API 키, 관리자·DB 비밀번호, 경로 가림 키·러너 서명 키의 값, 승인 manifest 내용. 이것들은 회차 시작 때 따로 만드는 파일(3번·4번·5번)이고, `runner.json`에는 그 **경로만** 둔다.
   - `preservation`의 0으로 채운 지문 셋(`mcp_tools_sha256`·`exploration_policy_sha256`·`declared_model_budget_sha256`, 64자리 `0`)은 그대로 둔다. 앞의 둘은 분리 배치(v3) 기록에서 쓰지 않고 측정값(도구 면 측정)과 B `null`로 대신한다. `declared_model_budget_sha256`은 B 기록에 그대로 옮겨지며, 원본 엔진과 평가 게이트웨이에는 모델 예산이 없어서 0×64는 **B 모델 예산 선언 없음**을 뜻한다(O는 제품 설치 예산 값에서 러너가 계산한다).
   - 예시에 이미 들어 있는 공식 값: `purpose: official`, E③ 비활성 요구(`split.e3_disabled_required: true`, 공식 회차는 켜야만 설정 검사를 통과한다), 검증 창(`llm_gateway.verify_attempts` 60·`verify_interval_seconds` 10, `run.llm_verify_timeout_seconds` 700 — 2026-11-25 사용자 결정), 멈춤 무효 T(`run.fault_stall_seconds` 180), 분리 배치 포트(`split.product_port` 28787·`dns_control_port` 29083)와 그 밖의 `split`·`preservation` 칸.
   - 채운 뒤 3절 환경 점검을 이 파일로 다시 돌린다. 엔진 직접 배치 예시(`runner.example.json`)로 돌리면 분리 배치 포트가 없어 `ports`가 `SKIP`(종료 코드 2)이다.

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

**운영 규칙: 측정 중 같은 머신에서 다른 Docker 작업 금지(2026-11-19 사용자, 2026-11-20 측정 PC를 개발 PC로 바꾸며 다시 확인).**
- 회차(유료·합성 모두)를 시작하기 전에, 같은 머신을 쓰는 다른 작업자(사람·자동 세션)에게 "측정 구간 시작"을 알린다. **측정 PC가 개발 PC이므로, 같은 PC에서 도는 다른 개발 세션(자동 세션 포함)도 예외가 아니다.**
- 회차 결과를 읽고 나면 "끝"을 알린다.
- 그 사이에는 `docker run`·`compose`, `docker exec`, Docker를 쓰는 시험·빌드, 망·볼륨 조작을 하지 않는다.
- 이유: 구간 감시는 회차 밖 컨테이너의 생성·시작과 회차 컨테이너 안 exec를 `window_boundary_changed`로 무효 처리한다. 2026-11-19 O 진단 회차 1건이 다른 세션의 망 없는 조사용 컨테이너 때문에 무효였다.

**운영 규칙: 측정 PC가 멈추지 않게(2026-11-20 사용자).**
- 세션 전 3절 환경 점검의 `power` 항목이 PASS여야 한다. 전원 연결(AC) 때 절전(`STANDBYIDLE`)·최대 절전(`HIBERNATEIDLE`)이 '안 함'(0)이다. 측정은 전원을 연결한 채로 한다(배터리 값은 사유에만 나온다).
- 측정 구간 동안 PC를 덮거나(덮개 동작이 절전이면), 로그아웃·재시작·Windows 업데이트 재부팅을 하지 않는다.
- **측정 날은 Windows 업데이트를 일시 중지한다**(설정 > Windows 업데이트 > 업데이트 일시 중지, 세션이 끝난 뒤까지). 재부팅을 기다리는 업데이트가 있으면 세션 전에 재부팅으로 끝낸다. `power` 항목이 재부팅 대기 표시(Windows 업데이트 `RebootRequired`·구성 요소 서비스 `RebootPending`)와 일시 중지 만료 시각을 읽어 본다(2026-11-20 사용자).
- 화면 잠금은 WSL·Docker를 멈추지 않지만, 잠금 뒤 절전으로 이어지는 설정이면 위 절전 '안 함'으로 막힌다. 세션 기록에 `power` 줄을 그대로 옮긴다.

**운영 규칙: 측정 중 Docker가 스스로 업데이트·재시작하지 않게(2026-11-20 사용자).**
- 이 PC의 엔진은 WSL 배포판의 dockerd(`docker.io`·`containerd`·`runc`)다. Ubuntu 자동 보안 업데이트(`unattended-upgrades`, 매일 `apt-daily-upgrade.timer`)가 켜져 있고 세 패키지는 보안 저장소에 있다. 측정 중에 업데이트되면 패키지 스크립트가 dockerd·containerd를 다시 띄워 도는 회차의 컨테이너가 멈춘다.
- 세션 전에 고정하고, 세션이 끝나면 푼다.

  ```bash
  sudo apt-mark hold docker.io containerd runc      # 세션 전
  sudo apt-mark unhold docker.io containerd runc    # 세션 끝난 뒤(보안 업데이트를 미루지 않게)
  ```

- Windows의 Docker Desktop은 종료한다(3절 2번). 스스로 업데이트·재시작하고, WSL 통합이 켜지면 이 배포판의 Docker 소켓을 가져간다(2026-11-02 이 PC에서 겪음). `docker_updates` 항목이 Docker Desktop 프로세스와 엔진 패키지 고정을 본다.

**운영 규칙: 관찰자는 화면을 읽기만 한다(2026-11-20 사용자).**
- O 회차를 제품 화면으로 지켜볼 수 있다. 화면에서는 읽기만 하고, 일시정지·재개·중지·승인·거부·설정 저장·대상 등록처럼 제품 상태를 바꾸는 조작을 하지 않는다. 조작이 회차에 들어가면 B와 같은 조건이 깨진다.

```bash
cd "$T"
# B(원본)
python3 -I -B scripts/eval/runner.py --config /srv/abc2lab-eval/runner.json --preflight "$SESSION/preflight.json" \
  --side baseline --condition "원본@35b" --repeat 0 --admin-password-file /srv/abc2lab-eval/admin.password
# O(개선 후): manifest를 새 승인 ID로 다시 만든 뒤
python3 -I -B scripts/eval/runner.py --config /srv/abc2lab-eval/runner.json --preflight "$SESSION/preflight.json" \
  --side ours --condition "우리@35b" --repeat 0
```

**세션 실행 스크립트(2026-11-20):** 아래 순서·무효 처리를 `scripts/eval/official_session.py`가 한 번에 한다. 먼저 점검 모드로 순서·무효 처리를 확인하고, 그다음 실제로 돌린다.

```bash
cd "$T"
# 점검 모드: 러너·Docker·네트워크를 부르지 않고 결과를 흉내 내 순서·무효 처리만 본다(시도마다 차례로 쓰고, 모자라면 complete)
python3 -I -B scripts/eval/official_session.py --session-dir "$SESSION/dry" --dry-run \
  --outcomes "complete,invalid:window_boundary_changed,complete,invalid:provider_fault_stall"
# 실제 세션
python3 -I -B scripts/eval/official_session.py --session-dir "$SESSION" --tree "$T" \
  --config /srv/abc2lab-eval/runner.json --preflight "$SESSION/preflight.json" \
  --admin-password-file /srv/abc2lab-eval/admin.password --o-manifest-template "$SESSION/manifest-template.json" \
  --run-timeout 3600 --grace 120 --post-timeout 300 \
  [--key-file <키 파일> --usage-cap-usd <세션 상한> --min-remaining-usd 0.30] \
  --start-window-kst 14-18 --provider-probe --heavy-check /srv/abc2lab-eval/heavy_ok.sh
```

- **시작 전 운영 점검(2026-11-22 사용자, 평가 의미는 바꾸지 않는다)**. 어느 것도 시도로 세지 않는다.
  - 시작 시간대(`--start-window-kst 14-18`): 세션을 띄운 시각(한국 시간)이 창 밖이면 시작하지 않고 `outside_start_window`로 멈춘다. 결과는 세션 기록 `start_window`에 남는다.
  - 제공자 단가와 가격 상한(`--provider-probe`를 켜면 먼저): 공개 엔드포인트 표(`{base_url}/models/{model}/endpoints`)에서 manifest `provider_slug`와 같은 tag의 단가를 manifest와 같은 단위(USD 마이크로/100만 토큰, 정수)로 읽는다(`provider_price`). 표에서 못 찾으면 `provider_price_unknown`으로 멈춘다(유료 점검 호출도 보내지 않는다). **상한 규칙(사용자 2026-11-22)**: 새 세션은 상한을 그 단가 × `--price-cap-factor`(기본 1.5, 올림)로 정하고, 세션 기록 `price_caps`(조회 단가·규칙 배수·정한 상한·`source: rule`)에 남긴다. 그 세션의 O manifest는 모두 그 상한을 쓴다(`o_manifest_caps`). **시작 점검**: 상한이 지금 단가보다 `--price-min-headroom`배(기본 1.2, 20% 높음) 이상이면 통과하고, 아니면 `provider_price_headroom`으로 멈춘다. 새 세션은 규칙상 늘 통과한다. 이 점검은 `--resume` 때 의미가 있다: 이어 돌릴 때는 시작 때 기록한 상한을 그대로 쓰고(`source: session_log`, 세션 안 조건을 같게), 그사이 단가가 20% 넘게 올라 여유가 없으면 멈춘다. 1.5배 여유를 시작 조건으로 요구하면 단가가 조금만 올라도 세션이 시작하지 못해서(11/22 하루에 단가가 두 번 움직였다) 시작 조건은 20%다. 배경: B에는 가격 상한이 없어, 상한을 단가에 딱 맞추면 측정 도중 단가가 조금만 올라도 O만 404로 막힌다(11/22 PrimeIntellect 단가가 0.176/1.578로 올라 상한 0.15/1.5를 넘자 OpenRouter가 404 "No allowed providers"를 주고 제품이 첫 호출에서 회차를 `unknown`/`model_usage_unknown`으로 끝냈다. 그날 S01-final7은 상한 1.6에서 회차 도중 실제 출력 단가가 1.6을 넘었다). 기록된 상한이 있는 세션을 `--provider-probe` 없이 이어 돌리면 입력 오류다(`resume_needs_provider_probe`).
  - 비용 보호 먼저(#848 리뷰): 단가 점검을 통과하면 유료 429 점검 호출 전에 키 사용량 상한·잔액 하한을 확인하고 세션 사용량 기준(`usage_base`)을 기록한다. 상한·잔액이 모자라면 호출을 보내지 않고 그 사유로 멈춘다. 점검 호출 비용은 세션 상한에 들어간다.
  - 제공자 429 점검(`--provider-probe`, `--key-file` 필요): 시작 직전 O manifest 틀(없으면 `split.manifest_path`)의 `model`·`provider_slug`·`base_url`로, 제품 OpenRouter 전송과 같은 provider 칸(`only` 하나·대체 없음·ZDR·데이터 수집 거부·가격 상한)을 붙여 출력 1토큰 호출을 10번 보낸다.
    - 429가 하나라도 나면 `--probe-wait`(기본 1800초) 뒤 다시 점검한다(`--probe-tries`, 기본 4번). 다시 점검하기 전에 시작 시간대도 다시 본다.
    - 끝내 429면 `provider_rate_limited`로 멈춘다. 429가 아닌 오류나 다른 제공자 이름이 나오면 `provider_probe_failed`로 멈춘다.
    - 매 점검의 호출 수·성공·429·그 밖·상태 코드별 수·응답 제공자 이름은 세션 기록 `provider_probe`에 남긴다(키·응답 본문은 남기지 않는다).
  - 호스트 포트: 회차마다 시작 전에 회차가 호스트에 열 고정 포트(3절 환경 점검의 `ports`와 같은 넷: `ports.engine_api_host`·`ports.obs_host`·`split.product_port`·`split.dns_control_port`)를 이미 누가 리슨하면 `ports_busy`로 멈추고 포트 이름·번호를 기록한다(`/proc/net/tcp`를 못 읽으면 `ports_unknown`). 2026-11-22 재부팅 뒤 VS Code 원격 서버가 `127.0.0.1:39291`(`ports.obs_host`)을 잡아 `compose_up_failed`로 회차가 무효가 됐다. 세션 도중 포트를 자동으로 바꾸지는 않는다(설정·사전 점검·회차 조건이 세션 안에서 같아야 한다). 막히면 그 프로세스를 끄거나 세션 전에 `runner.json` 포트를 바꿔 3절 점검을 다시 돌린 뒤 `--resume`한다.
  - 메모리 여유(`--heavy-check`): 회차마다 시작 전에 점검 명령이 0으로 끝나야 시작한다. 아니면 `--heavy-interval`(60초)마다 다시 보며 `--heavy-wait`(1800초)까지 기다리고(`heavy_wait` 기록), 넘으면 `heavy_not_ok`로 멈춘다.

- 칸마다 러너를 프로세스 그룹으로 띄우고 **PID로** 기다린다. 대기 시한(`--run-timeout`)을 넘으면 SIGINT → 유예(`--grace`) → SIGTERM → 유예 → SIGKILL을 보내고 `run_wait_timeout`으로 멈춘다. 러너는 SIGINT·SIGTERM을 같은 정리 경로(내리기·보호 입력 삭제)로 받는다. 세션을 뒤에서 띄우면(`… &`·`nohup … &`) 셸이 SIGINT를 무시 상태로 넘기고 그 상태가 자식 러너까지 물려가 SIGINT가 먹지 않는다. 그래서 세션과 러너는 시작할 때 SIGINT를 기본 처리로 되돌린다(2026-11-21: 기존 세션 하네스에서 상한 멈춤이 안 먹음, `test_run_signals.py`가 뒤에서 띄운 경우를 고정). 이름 패턴(`pgrep -f`)으로 기다리지 않는다(2026-11-20 사용자 규칙).
- **회차마다 시작 전에 실행 중 컨테이너가 0개인지 본다**(`--resume`의 첫 회차 포함). 남아 있으면 시도로 세지 않고 `containers_present`로 멈춘다(못 읽으면 `containers_unknown`). 남은 컨테이너 때문에 다음 회차가 격리 검사에서 무효가 되어 시도 한 번이 날아가지 않게 한다(2026-11-20 사용자). 이어서 평가 compose 망(compose 프로젝트 라벨이 `eval-`로 시작)이 남아 있으면 같은 방식으로 `networks_present`(못 읽으면 `networks_unknown`)로 멈춘다. 앞 회차 망이 남은 채로 다음 회차가 같은 고정 대역의 망을 만들지 않게 막는 예방 조치다. 2026-11-20에 관측한 `compose_up_failed`는 이것으로 설명되지 않는다(시작 전 망 0이었던 회차에서도 났다). 유력한 원인 후보는 제품 postgres healthcheck가 초기화 임시 서버 동안 통과한 것이다(#833에서 새 볼륨으로 재현하고 고침, 실패 회차 자체의 install 로그는 없다).
- 회차가 끝나면 **완료 파일**(새 회차 디렉터리의 `runner-result.json`)을 읽는다. 회차가 어떻게 끝나든(시한 넘김·결과 없음 포함) 그 뒤 실행 중 컨테이너가 0이 될 때까지 기다리고(`--post-timeout`), 안 내려가면 멈춘 사유에 `containers_left`를 같이 적는다. 컨테이너가 내려가면 평가 compose 망이 다 지워질 때까지 같은 시한으로 기다리고, 남으면 `networks_left`로 멈춘다.
- **새 회차 디렉터리가 생겼으면 무슨 일이 있어도 시도 1회로 센다**(2026-11-20 사용자). 결과 파일이 있으면 그 상태로, 없으면 무효(사유 = 멈춘 사유: `run_wait_timeout`·`result_missing`·`result_unreadable`)로 기록하고 멈춘다. 종료 코드와 상태가 어긋나면 무효(`result_status_mismatch`)로 센다. 컨테이너가 남으면 그 회차의 실제 상태로 세고 `containers_left`로 멈춘다(끝까지 간 회차가 버려지지 않는다). 회차 디렉터리가 안 생긴 경우(러너 종료 코드 2 등)만 시도로 세지 않는다. 멈춘 회차를 다시 돌려 기록에서 빼는 길(측정 결과를 골라 버리는 길)을 막고, 시한 넘김이 되풀이되면 2N에 걸리게 한다.
- O 회차마다 manifest 틀(`--o-manifest-template`, 4절 5번 manifest에서 `approval_id`·`expires_at`만 빼고 같은 칸, 0600)로 새 승인 ID·만료 시각의 manifest를 `split.manifest_path`에 다시 쓴다(못 쓰면 `manifest_write_failed`로 멈춘다).
- 키 사용량 상한(`--key-file`·`--usage-cap-usd`는 같이 준다)은 회차마다 시작 전에 본다. 세션 첫 확인의 사용량이 기준이고, 그 값을 세션 기록(`usage_base`)에 남겨 `--resume` 뒤에도 세션 전체 기준으로 건다. 넘으면 `usage_cap`, 남은 한도가 하한 이하면 `remaining_floor`, 조회 실패·값을 모름(한도 없는 키 포함)이면 `usage_read_failed`·`usage_unknown`·`remaining_unknown`으로 멈춘다(확인 못 한 채 유료 회차를 시작하지 않는다). 키·승인 ID는 출력하지 않는다.
- 기록: `$SESSION/session-log.jsonl`(시도마다 쪽·`--repeat`·상태·사유·회차 ID, 마지막 요약). 멈춤 무효(`provider_fault_stall`) 시도 줄에는 멈춤으로 판정된 장애 사건마다 그 순간 진행 중이던 요청 수(`inflight_at_fault`)와 그중 모델 호출 수(`inflight_model_calls_at_fault`)를 같이 남기고(`fault_stall`), 요약에 쪽별 '멈춤 사건 때 모델 호출이 진행 중이던 멈춤 무효' 수(`fault_stalls_inflight_model_call`)를 낸다(사용자 2026-11-22: B는 스트리밍 조각을 활동으로 세지 않아, 긴 스트리밍 호출 하나로 B만 멈춤 무효가 나는 경우를 공정성 기록에서 가른다. 판정·상한 셈은 그대로). 러너 출력은 `$SESSION/<쪽><번호>-<시각>.stdout|stderr`.
- 종료 코드: 0 쪽마다 유효 N회 완료, 3 측정 실패(쪽별 `provider_fault_stall` 2회 이상), 4 멈춤(시도 상한·러너 설정 오류·대기 시한·결과 없음·컨테이너 남음·사용량 상한), 2 입력 오류. 멈추면 사유를 고친 뒤 같은 명령에 `--resume`을 붙여 잇는다. 같은 세션 기록의 완료·무효 시도를 다시 세어 남은 칸부터 돌린다(회차 디렉터리가 생긴 뒤 멈춘 시도도 그대로 센다. 회차 디렉터리가 안 생긴 멈춤·설정 오류 줄과 `config_end` 회차는 시도로 세지 않는다). 세션 기록 마지막 요약 줄의 `early_end_runs`에 러너 상한 전에 일찍 끝난 회차(제공자 장애 끝·설정 오류·유효로 둔 회차)의 회차 ID·끝 상태·제품 사유·근거 호출이 모인다. 측정 뒤 하나씩 검토한다(고정 초안 §26). 기록만으로 이미 시도 상한·멈춤 무효 상한에 닿았으면 잇지 않는다. 기록이 있는데 `--resume` 없이 시작하면 거부한다.
- 측정 구간 알림(아래 운영 규칙)은 스크립트를 띄우기 직전·끝난 직후에 사람이 보낸다.

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
| `provider_fault_stall` | 제공자 장애(429·위쪽 끊김) 뒤 다음 활동 사건(요청 시작·응답 완료)까지 T초 이상, 그 사이 엔진 끝 신호 없음(진행 중 요청·스트리밍 조각은 활동 아님, #756). 같은 칸 재실행. 세션 기록에 멈춤 순간 진행 중이던 모델 호출 수가 남는다(6절, 공정성 기록용). 한 쪽이 2회 이상이면 7절 집계가 측정 전체 실패(종료 코드 3)로 멈춘다(2절) |
| `provider_fault_end` | 러너 상한 전에 O가 `failed`·`unknown`(B는 `failed`)으로 끝났고 끝 직전 마지막 모델 호출이 제공자 오류(끊김·429·5xx), 그 뒤 성공 호출 없음. `early-end.json`의 근거 호출을 본다. 같은 칸 재실행. `provider_fault_stall`과 합쳐 한 쪽이 2회 이상이면 측정 전체 실패(종료 코드 3) |
| `config_end` | 설정 오류: 모델 호출 401~404(키·크레딧·권한·모델 이름), 승인 예산 상한에 닿은 `budget_limited`(manifest 요청·비용 상한), 시작 거부(`start_refused:<제품 사유>`). **시도로 세지 않고 세션이 멈춘다**(`config_end:<쪽>`). `early-end.json`의 근거를 보고 키·manifest·대상 승인을 고친 뒤 `--resume`으로 같은 칸부터 잇는다 |
| `product_not_executed` | 제품이 러너 상한 전에 준비 단계에서 끝나 실행 기록이 없음(`diagnosis-run` `NOT_RUN`·404). `early-end.json`의 끝 상태·제품 사유를 본다. 같은 칸 재실행(시도로 셈, 2N). 유효로 세지 않는다(사용자 확정 2026-11-21, 고정 초안 §26) |
| `fault_stall_unjudgeable` | T가 설정됐는데 판정 재료(호출 행·시각)를 읽지 못함. 멈춤이 없다고 할 수 없어 무효다. 같은 칸 재실행 |
| `drain_timeout`, `seal_failed`, `interrupted` | 같은 칸 재실행. 반복되면 원인 수정 |

## 7. 집계·채점

```bash
cd "$T"
python3 -I -B scripts/eval/session_runs.py "$SESSION/runs" --repeats 3 \
  --runner-key-file /srv/abc2lab-eval-keys/runner.key --manifest "$SESSION/manifest.json"
python3 -I -B scripts/eval/score.py "$SESSION/manifest.json" --runner-key-file /srv/abc2lab-eval-keys/runner.key
```

- `session_runs.py`는 결과·회차 기록 서명을 먼저 검증하고, 쪽별 시도·유효·무효(사유별)·무효율·순서를 낸다. 분리 배치 세션은 작업 요청 지문이 쪽마다 하나여야 하고, 분리 배치와 엔진 직접 배치 회차가 섞이면 멈춘다. 완료 회차가 모두 같은 T로 멈춤 판정됐는지 보고(아니면 멈춤), 쪽별 `provider_fault_stall`·`provider_fault_end` 무효 수를 0이어도 출력하며(둘을 합쳐 셈), `config_end` 회차는 시도에서 빼고 경고로 따로 적고, 러너 상한 전에 일찍 끝난 회차 목록을 낸다. 한 쪽이라도 2회 이상이면 측정 전체 실패로 종료 코드 3을 내고 manifest를 쓰지 않는다(그때는 채점하지 않고 결과 문서에 측정 실패로 적는다).
- `score.py`의 첫 표가 주 지표(회차당 엔진 귀속 해결 수, 코어 GT 9·BAC 10, 평균 ± SD)다. 분리 배치 표는 엔진 설정·규칙 파일·도구 트리·LLM 스트리밍(선언된 차이)을 조건 안에서만, 샘플링·머신 사양·머신은 표 전체에서 대조한다. 통제 검증 표에서 O는 "대조 미수집 — 통제 주장 보류"다(E② 시도 대조 평가 내보내기 전). 판정 칸은 판정 미수집(판단불가)이다.
- 결과 표에는 조건별 시도 수·유효 수·무효 사유별 수를 함께 싣고, 무효율은 쪽별로 따로 쓴다.
- 결과 표에는 회차별 모델 호출 429 비율(전달 호출 중 429 수, 회차의 `llm-summary.json`)도 싣는다. `session_runs.py`가 시도마다 한 줄과 쪽별 유효 회차 합을 낸다. 완료 회차는 서명된 근거 해시와 맞을 때만 값을 쓰고, 무효 회차는 "서명 밖"으로 표시한다. 시간 상한 회차에서 429가 쓴 시간은 결과에 바로 닿지만, 보조 기록이라 집계·판정·무효 규칙에는 쓰지 않는다.

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

## 부록 B. 윤재상 PC 점검 기록(이력)

2026-11-20 측정 머신을 최민준 PC로 바꾸기 전, 윤재상 PC를 측정 머신으로 준비하며 한 점검이다. 지금 절차에는 쓰지 않는다.

- 2026-11-19 첫 점검(엔진 직접 배치 예시 `runner.example.json`): 13/16. 버전은 개발 머신과 같고, 메모리 15.4 GiB, 디스크 1 TB 여유. FAIL은 `containers`(다른 프로젝트 스택 2개)·`host_listen`(:3000 개발 서버)·`ports`(예시에 분리 배치 블록이 없어서 생긴 안내 문제)였다.
- 안내 문제를 고쳤다(#803): 공식 분리 배치 예시 `runner.official-split.example.json`, 분리 배치 블록이 없으면 `ports`를 SKIP으로 안내.
- 2026-11-20 재점검(공식 분리 배치 예시): 14/16, `ports` 통과. 남은 FAIL 둘은 측정 때 끄기로 했다.
- 재상님께 드린 안내 요지: 예시를 세션 디렉터리로 복사해 `<…>` 칸만 채우고, API 키·관리자·DB 비밀번호·경로 가림 키·러너 서명 키 값·승인 manifest 내용은 넣지 않으며(회차 시작 때 따로 만드는 파일, `runner.json`에는 경로만), 다른 스택·개발 서버를 끈 뒤 다시 점검한다.
