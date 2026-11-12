# 샌드박스 이미지 자료 — 결정안 (C30)

상태: **결정안(2026-11-12, 최민준 결정·이동찬 리뷰).** 리뷰로 확정되기 전에는 구현하지 않는다. 일정은 적지 않는다.

[개선 목록](control-restore-backlog.md) C30("이미지 데이터 미탑재(워드리스트·Playwright·skills). 도구 동등성 79개 중 57개 일치")의 결정안이다. 11/11 경계(탐색은 E② 밖, 진단만 E②)를 기준으로, 원본 이미지에 있던 자료를 어느 실행 단계에 둘지 정한다.

## 1. 지금

[도구 동등성 실측](evaluation/e2-tool-equivalence.md)에서 진단 샌드박스(E② Bash)와 원본 이미지가 다른 22개 중 이미지 자료로 남은 것은 셋이다.

- 브라우저 도구: `npm`·`npx`·`playwright`·`playwright-cli`·`/usr/lib/node_modules`·`/root/.cache/ms-playwright`.
- 도구 자료: `/usr/share/nmap`·`/usr/share/vim`.
- skills: `/app/skills`(`api-recon`·`playwright-cli`·`scopesentry`).

원본 이미지(루트 `Dockerfile`)에는 따로 받은 워드리스트가 없다. 개선 목록의 "워드리스트"는 실측 항목에 없어 이 결정에서 다루지 않는다(실제 차이가 확인되면 이 문서에 더한다).

나머지 차이(프록시 밖 네트워크·구성 파일·환경 변수)는 E② 경계와 비밀 보호 때문에 생기는 것이라 C30이 아니다.

## 2. 결정

1. **브라우저 도구는 진단 샌드박스에 넣지 않는다.** 브라우저는 탐색 단계의 탐색 브라우저 컨테이너(`explore-browser`)가 맡고 모델은 허용 목록의 브라우저 MCP 도구로만 쓴다([탐색 계약](contracts/exploration-boundary.md) §5, 규칙 3). 진단 샌드박스에 브라우저를 넣으면 진단 단계에 E②를 거치지 않는 두 번째 HTTP 클라이언트(번들 안의 fetch·WebSocket 등)가 생긴다. 탐색 직접 샌드박스(경계 직접 입구)에도 넣지 않는다. 브라우저 입구와 직접 입구를 나눈 계약 §5의 배치가 깨지기 때문이다.
2. **skills는 단계별로 나눈다.**
   - `api-recon`: **탐색 프로필**에 준다. 지시문(`SKILL.md`·`reference.md`)과 파이썬 스크립트(`harvest_static.py`·`extract_route_map.py`·`spider_mpa.py`·`build_perm_tree.py`)를 탐색 직접 샌드박스에 읽기 전용으로 둔다. 스크립트의 요청은 경계 직접 입구로 나간다. 브라우저가 필요한 런타임 수집(`runtime_harvest.js`·`preload.js`)은 직접 샌드박스에서 돌리지 않고 브라우저 MCP 도구로 대신한다. 이 차이는 탐색 에이전트 지침에 적는다(탐색 에이전트 루프 PR).
   - `playwright-cli`: 넣지 않는다. 브라우저를 직접 띄우는 skill이라 1과 같은 이유다. 같은 일은 브라우저 MCP 도구가 한다.
   - `scopesentry`: 넣지 않는다. 외부 플랫폼 MCP를 쓰는 skill이라 완전 로컬 모드(규칙 8)와 맞지 않고, 우리 제품 흐름에 그 플랫폼이 없다.
   - 진단 프로필: 지금 있는 skill 중 진단 목적의 것은 없어 넣지 않는다. [루프 복원 결정안](loop-restore-decision.md) 2절의 skills 줄을 이 결정에 맞춘다. 진단용 skill이 생기면 지시문만 읽고 명령은 E② Bash로 돌리는 원래 규칙을 쓴다.
3. **`/usr/share/nmap`·`/usr/share/vim`은 넣지 않는다.** `nmap`은 진단 샌드박스(빈 netns, HTTP 프록시만)에서 대상에 닿지 않고, `vim`은 비대화형 실행에서 쓸 일이 없다.
4. **평가 선언 차이는 이번에 바꾸지 않는다.** [선언 차이 목록](evaluation/profiles/official-e2-declared-differences.json)은 해시로 회차에 묶여 있다. 위 1·2의 이유(브라우저·skills는 탐색 단계 몫)는 목록을 다음에 고칠 때 이유 문장에 반영하고, 항목 자체는 그대로다.

## 3. 구현 순서

1. 루프 복원 결정안 skills 줄 정정(이 PR).
2. 탐색 직접 샌드박스에 `api-recon` 지시문·파이썬 스크립트 읽기 전용 마운트와 탐색 프로필의 skills 도구(탐색 에이전트 루프 PR과 같이).

## 4. 열린 항목

1. **`api-recon` 파이썬 스크립트의 의존성**: 직접 샌드박스의 파이썬 환경에 스크립트가 쓰는 패키지가 있는지 확인한다(구현 PR에서 실제 실행으로).
