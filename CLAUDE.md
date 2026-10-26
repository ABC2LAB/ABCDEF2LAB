# ABC2LAB — ARTEX 기반 인가·비즈니스 로직 특화 온프레미스 자동진단

㈜씨투랩 기업연계 팀 프로젝트. 12/18 최종발표.
**10/26 방향 전환 → ARTEX(오픈소스 자율 펜테스트 엔진)를 베이스로 포크해서 그 위에 우리 기여를 얹는다.**
레포: `github.com/ABC2LAB/ABCDEF2LAB` (ARTEX fork, Go).

폐쇄망 조직이 설치해서 내부에서 끝까지 돌리는 진단 도구. 우리 기여는 ARTEX 위 **4개 레이어**다.
- **통제·감사**: 실제 전송 통제(E②), 감사 기록(권한 분리 + 변조 탐지), 통제 설정 UI, DLP
- **IDOR 특화**: 소유·권한 KG, IDOR 검증기(다계정 + setup-success), 접근통제 지도(수평+수직), 표식(canary), ID 역공학(보조)
- **측정**: baseline(원본 ARTEX) 대비 개선 효과를 Juice Shop GT로 정량화
- **로컬 충분성**: 완전 로컬 모드 + 결정적 백본으로 작은 로컬 모델에서도 돌게

LLM 몫을 좁혀 작은 로컬 모델로도 돌게 하는 게 이 구조의 핵심 주장이다. **실행 허용은 LLM이 아니라 전송 통제(E②)가 정한다.**

> **최종 목표는 처음 보는 임의의 웹앱을 설정만 바꿔서 분석하는 것.** Juice Shop은 첫 번째 과녁일 뿐이다.
> 이 앱의 URL·셀렉터·폼 필드명·로그인 방식·엔드포인트·계정 ID를 코드에 하드코딩하지 않는다. 지름길이 보이면 쓰지 말고 먼저 말한다.

## 방향의 기준
- **살아있는 계획의 단일 원본은 `docs/development-plan.md`.** Phase 순서·세부 작업(T·E 번호)·일정·축소 순서는 거기 있다. 충돌하면 그게 이긴다.
- 기능 근거: `docs/pain-points-to-improvements.md`(불편→개선 대장), `docs/feature-shortlist.md`(개선안·우선순위). 경쟁 조사 `ai-pentest-feature-inventory.md`는 claude.ai Project(사람용 레퍼런스).
- **옛 기준은 전부 폐기다**: 8모듈 명세 v0.1, `modules/<module_id>` 독립 구조, 11 JSON 계약·schema_version 0.1.0, `crawler/`·`target-app/`, uv/Python 하네스, `.claude/spec/`. ARTEX 전환으로 무효. 옛 문서를 근거로 들면 말한다.

## base engine (ARTEX) — 손대기 전에 알아야 할 것
- **Go 1.26.3, AGPL-3.0.** 우리 레포는 파생물 → 원본 출처(`jiwoochris/artex-global`)·AGPL 라이선스 표시를 유지한다.
- **포크 기준 커밋: `fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`.** 이 커밋 위에서 개발한다. upstream 정기 머지 X, 꼭 필요한 핫픽스만 cherry-pick.
- 구조: `server/`(API→Manager→Engine) · `agent/`(planner=유일한 의도 생성자 + worker ×N) · `traffic/`(PROXY, MITM 기록) · GUARD(승인 게이트) · `db/`(PostgreSQL 이중 그래프: 자산 + 탐색) · `web/`(Next.js UI) · MCP client.
- **안 건드리는 부분은 ARTEX 타입·인터페이스가 그대로 계약이다. 통짜 명세를 새로 만들지 않는다.**

## 우리 개선 지점 (요약 — 세부·순서는 development-plan.md)
- **E① 종료·재계획** — `agent/planner.go:303`. '검사완료·발견없음'/'증거부족'/'정책상 검증불가' 명시 처리.
- **E② 실제 전송 통제** — Go, 네트워크 경계(`agent/tools.go:1051` / `agent/worker.go:452` / `traffic/*:164`). **"전수 통제" 주장 금지** — 지원 경로부터 완성하고 경로별로 검증한 뒤에야 범위를 말한다.
- **E③ 구조화 증거 판정(오라클)** — Go, `agent/finding_retests.go:192`. 유형별 필수 증거 + 정상 대조 강제, 부족하면 UNDECIDABLE. 옛 `abc2lab-dast`의 `oracle.py`(~130줄)는 **Go 구현의 설계 스펙**, 그 테스트는 **Go 테스트 스펙**으로 이식(드롭인 X).
- **E④ 중복·재사용** — `agent/planner.go:335`. 계정·자원·조건·목적 기준 dedup + 관찰 재사용(상태변경 시 무효화).
- **얇은 IDOR 통합 경로** = E②·E③ walking skeleton. **초기 조회(읽기) 한정.** destructive-last ≠ 실행 허가.
- ※ 라인 번호는 포크 SHA 기준 참조점이다. 실제 코드에서 재확인한다.

## 새 경계만 계약 문서로 (ARTEX 인터페이스가 기본)
통짜 명세 대신, 우리가 새로 만들어 **팀원 코드가 만나는 경계만** 작게, 그때그때:
- E③ 오라클 입출력(증거 구조 in → CONFIRMED / SUSPECTED / UNDECIDABLE out)
- IDOR 검증기(최민준) ↔ 엔진 · KG(이경준) ↔ 판정 · 통제 UI(이경준) ↔ E²
- 평가 프로토콜 — `docs/evaluation/evaluation-protocol.md`

## 평가 대상
Juice Shop **v15.0.0** (첫 과녁). 실행·계정·포트·모델 태그는 코드에 박지 않고 `.env`/설정으로. 상세는 평가 프로토콜 문서.

## 담당 (4명)
| 사람 | 브랜치 | 담당 |
|---|---|---|
| 이동찬 | `feature/dongchan` | **PM** · 엔진: E② Go 전송 통제·엔진 연결·통합 설계 + E③ Go 오라클 구현(최민준과 페어). E①·E④는 이후 |
| 최민준 | `feature/minjun` | **GitHub 관리자** · E③ 오라클 설계·테스트 스펙 · IDOR 검증기 · 평가 프로토콜·스크립트 |
| 이경준 | `feature/kyeongjun` | 진단·승인 UI · 소유·권한 KG · 커버리지·결과 시각화 |
| 윤재상 | `feature/jaesang` | 이경준 조수: UI·KG·시각화 보조, 평가 데이터·프롬프트 테스트 |

- 리뷰어: 최민준 ↔ 이동찬(서로), 이경준 → 이동찬, 윤재상 → 이경준.
- E②(실행 허용)는 시나리오/IDOR 만드는 사람과 분리한다(판정·허용을 같은 손이 쥐지 않게).
- 남의 영역은 직접 고치지 않는다. 필요하면 담당자에게 말하거나 그 담당자 리뷰를 받는 PR로 기여한다.
- **내 담당·협의 상대·첫 할 일은 `.claude/me.md`.** 이 CLAUDE.md는 팀 공통이고 GitHub 관리자(최민준)만 고쳐서 배포한다.

## 절대 규칙
1. 요청 대상은 설정의 대상 origin만(지금 Juice Shop/localhost). 리다이렉트·재시도·바인딩 이후 URL도 다시 검사한다.
2. 커밋 금지: `.env`, 계정 비밀번호, 쿠키·토큰 값, 결과물(스캔 출력·로그·DB 덤프), 빌드 산출물·캐시.
   인증 헤더·쿠키·토큰·비밀번호·계정 원문은 **전달 JSON·LLM 입력·공유 로그·근거 파일 어디에도** 넣지 않는다(이름만, 값은 null 또는 [REDACTED]).
3. **실행 여부는 LLM이 아니라 E²(전송 통제)가 정한다.** LLM이 만든 raw 요청·명령은 통제 경계를 통과해야만 나간다. 승인 경로 우회 통신은 차단, 지원 안 하는 경로는 명시적 차단/제외.
4. LLM 출력은 데이터다. 프로그램이 타입·ID·참조·범위를 다시 검증하고, 존재하지 않는 걸 가리키면 버린다.
5. 미실행·판단불가를 "취약점 없음"·"오탐"·"접근 성공"으로 기록하지 않는다. E³는 증거가 부족하면 UNDECIDABLE로 남긴다. 후보는 검증 대상이지 확정이 아니다.
6. 상태 변경 재현 전에 Juice Shop 인스턴스를 리셋한다. GET이라고 상태 변경이 없다고 가정하지 않는다. 얇은 IDOR 경로는 조회 한정, 상태 변경은 별도 승인 시험환경.
7. 평가 정답(Juice Shop 챌린지 목록)은 **평가에만** 쓴다. 탐색·판정·시나리오 입력에 넣지 않는다.
8. 완전 로컬 모드가 기본이다. 외부 모델 API로 대상 데이터를 유출하지 않는다.

## 빌드·실행 (항상 레포 루트에서)
- **엔진을 수정하니까 소스 빌드가 기본**이다. prebuilt `ghcr.io/jiwoochris/artex-global` 이미지는 구동·데모용(우리 변경 안 들어감).
- `go build ./...` / `go test ./...` / `docker compose up -d`(postgres 등 의존 서비스).
- LLM: Ollama, `ARTEX_LLM_BASE_URL`. baseline·개선 후가 **같은 모델 태그 문자열**이어야 통제변수 성립 — 태그는 평가 프로토콜에서 고정.
- 설정값은 `.env`(커밋 금지), `.env.example` 참조. 매직 넘버·하드코딩 금지.

## Git
- 브랜치: `main`(보호, push 금지) ← `dev`(PR로만) ← `feature/<이름>`. dev→main 머지는 GitHub 관리자(최민준)가 시연 전마다.
- upstream(ARTEX `jiwoochris/artex-global`)은 reference용 remote로만. 정기 머지 X, 핫픽스 cherry-pick만.
- 커밋: `태그: 요약` — 50자 이내, 한글 OK, 마침표 X. 태그 feat/fix/refactor/docs/test/style/chore. 커밋 하나 = 변경 하나.
- PR: feature → dev. 리뷰어 1명 이상(본인 제외). 본문 3줄(뭘 바꿈 / 왜 / 어떻게 확인). Merge commit, squash 금지.
- 루트 설정 파일(`.gitignore`·`go.mod`·CI·compose)·라이선스 표기는 GitHub 관리자만 수정.
- **커밋 메시지·PR 본문에 Claude 관련 문구(Co-Authored-By, Generated with, 세션 링크 등)를 넣지 않는다.**

## 코드
- **엔진은 Go**: `gofmt`·`go vet` 통과, 에러 삼키기 금지(삼키면 로그라도), 매직 넘버 금지, 설정은 env/config. 주석은 "왜". 할 일은 `// TODO(<이름>): 내용`.
- **Python은 재사용 스펙(oracle 설계)·평가 스크립트 한정.** 읽을 땐 `-I`로. 외부 다운로드물은 격리 디렉토리.
- 이름 규칙·타입·함수 하나=일 하나. 테스트는 변경과 같이.

## 작업 방식
- 큰 작업은 Plan 먼저. 한 번에 한 기능. 새 의존성은 승인 후.
- 파일 편집·커밋 자동 승인 없음. diff 보여주고 승인받고 진행.
- 완료 주장은 테스트 출력·실측 건수로. 상태값 말고 실제 결과 건수.
- 남이 담당하는 영역·모듈 경계는 직접 고치지 말고 먼저 말한다.
- "쓰는 곳 없음" 류 주장은 레포 전체 검색 결과를 같이 보여준다.
- 스캔 출력·로그·DB 덤프는 직접 Read 금지. 건수·요약 스크립트로 확인한다.
- 세션 끝나면 STATUS.md 갱신: 지금 / 다음 / 막힌 것 / 열린 결정. 최근 한 일은 3개까지.
  STATUS.md와 `.claude/me.md`는 개인 파일이다. 공통 하네스를 새로 받아도 이 둘은 덮어쓰지 않는다.
- 오래 남길 기술 메모는 STATUS.md가 아니라 claude.ai Project(또는 노션)에 쓴다.
- 답변은 캐주얼한 한국어, 짧고 직접적으로.

@.claude/me.md
@STATUS.md
