# Vercel 배포 한도와 프런트엔드 CI

## 적용할 정책

Vercel은 시연 UI를 배포하고 GitHub Actions는 PR을 검증한다. 프로젝트의
Production Branch는 `dev`, Root Directory는 `web`이다.
`web/vercel.json`은 다음 Git 배포 정책을 사용한다.

| Git 브랜치 | 자동 Vercel 배포 | Actions |
| --- | --- | --- |
| `dev` | 유지(Production) | 기존 Go·평가 검사 + 변경에 맞는 Web 검사 |
| `preview/**` | 허용(Preview) | PR이 있으면 동일한 검사 |
| 그 밖의 브랜치 | 중지 | 열린 PR은 동일한 검사 |

기존 PR을 새로 만들거나 브랜치를 바꿀 필요는 없다. 일반 프런트엔드 PR도
Actions에서 시험·Next.js 빌드·타입 검사를 수행한다. 실제 Preview URL이
필요한 기존 브랜치는 담당자와 합의한 뒤 정확한 브랜치 이름을
`deploymentEnabled`에 `true`로 추가할 수 있다. `preview/**`는 선택적인
미리보기 전용 이름이며 기존 브랜치 이름 변경을 요구하지 않는다.
이 정책은 PR base와 관계없이 **push된 source branch 이름**으로 평가된다.

`**: false`가 없으면 명시하지 않은 브랜치도 기본 허용된다.
여러 패턴에 일치하면 하나라도 `true`인 규칙이 허용하므로, 허용 예외를
좁게 관리한다. `*`는 슬래시를 포함하는 브랜치 전체를 표현하지 못한다.
Production Branch를 바꾸기로 합의하면 배포 허용 목록도 함께 검토한다.
현재 Production Branch는 `main`이 아니며 이를 바꾸는 작업은 포함하지 않는다.

## 방법별 효과와 한계

| 방법 | 실제 효과 | 배포 rate limit 관점 |
| --- | --- | --- |
| Ignored Build Step / `ignoreCommand` | Git 이벤트 후 빌드 취소. 종료 0은 취소, 1은 빌드 진행 | 취소한 빌드도 배포·동시 빌드 한도에 집계된다 |
| `git.deploymentEnabled` | 브랜치별 자동 Git 배포 트리거 제한 | 비활성 브랜치의 자동 배포 요청을 줄이는 용도. 파일 경로 조건은 지원하지 않는다 |
| Git 연결 해제 | 해당 프로젝트의 Git 연동 자동 배포 중지 | Production도 중지하므로 현재 권장안에 포함하지 않는다 |
| Skip unaffected projects | workspace 그래프와 lockfile로 영향 없는 앱을 자동 건너뜀 | 2026-10-21 공식 변경 이후 자동으로 건너뛴 배포는 rate limit에서 제외된다 |
| Actions `paths` / concurrency | GitHub workflow 실행량 제어 | 독립적인 Vercel Git 연동의 배포 생성은 제어하지 못한다 |

현재 저장소에는 루트 JavaScript workspace 정의가 없고 `web`과 Go 코드가
공존한다. 프로젝트의 `enableAffectedProjectsDeployments`가 이미 켜져 있어도
이를 근거로 백엔드 변경을 무료 건너뛰기한다고 판단할 수 없다.
Vercel 자동 건너뛰기는 GitHub 연동과 npm/yarn/pnpm/Bun workspace 등을
요구하며 workspace 밖의 변경은 전역 변경으로 취급할 수 있다.
이를 위해 Go 저장소를 JS workspace로 재편하는 일은 최소 변경안에서 제외한다.

Hobby의 공개 배포 생성 한도는 owner 범위에서 하루 100건(86,400초)이다.
빌드 시간 절약, 알림 숨기기, GitHub deployment 이벤트 생성을 끄는 설정은
이 한도 해결과 구분한다. 특히 이미 만들어진 배포와 실패 상태를 이 파일이
취소하거나 성공으로 바꾸지는 않는다.

Hobby에는 조직 소유 Git 저장소 연결 제한도 있으므로, 현재 살아 있는 연결을
해제했다가 다시 연결할 수 있다고 가정하지 않는다. 기존 연결을 유지한다.

## Actions 검증

기존 `go-static`, `go-test`, `eval` job과 모든 이벤트·권한·concurrency는
유지한다. 새 `Web build·단위 시험` job도 모든 PR 및 dev push에서 생성한다.

1. `scripts/ci/web-changed.py`는 PR base와 체크아웃된 merge commit, 또는
   push의 before/after 전체 범위를 비교한다. 마지막 한 커밋만 보지 않는다.
2. `web/**`, Web 검증 workflow/분류기, 루트 패키지·배포·런타임 설정이 바뀌면
   Node 26에서 `npm ci`, Vitest, 기존 Node `.test.mjs` 시험,
   `NEXT_PUBLIC_MOCK=1 npm run build`를 수행한다.
3. 백엔드·문서·평가 시험만 바뀌면 같은 job의 명시적 생략 단계로 성공 마감한다.
   Go·평가 시험은 계속 실행한다.
4. SHA/이력이 없거나 diff를 계산하지 못하면 Web 검증을 수행한다.
   삭제·이동·여러 커밋·개행 파일명도 감지한다.
5. `pull_request`의 base 필터와 workflow 전체 `paths` 필터를 추가하지 않는다.
   기존 스택 PR 검사 범위와 미래 필수 체크의 완료 상태를 유지한다.

새 Web job은 해당 변경에 대한 검증이며 Vercel 배포·실제 백엔드 연결·대상 스캔
시험이 아니다. CI에 배포 토큰이나 API 비밀을 추가하지 않는다.
새 공유 프런트엔드 의존 경로가 `web` 밖에 생기면 분류 입력도 추가한다.

## 대시보드에서 확인할 값

이번 변경을 준비하는 동안 설정은 수정하지 않는다. 담당자가 적용할 때:

| 메뉴 | 확인/권장값 |
| --- | --- |
| abc-2-lab → abc2lab → Settings → Git → Connected Git Repository | `ABC2LAB/ABCDEF2LAB` 연결 유지 |
| Settings → Build and Deployment → Root Directory | `web` 유지. 설정 파일도 `web/vercel.json`에 둔다 |
| 같은 Root Directory → Skip deployment | 현재 Enabled 유지. workspace 요건 미충족을 보완하는 해결책으로 보지 않는다 |
| Settings → Build and Deployment → Ignored Build Step | 현재 사용자 명령 없음/Automatic 유지. 한도 해결용 diff 명령을 추가하지 않는다 |
| Settings → Build and Deployment → Node.js Version | 현재 `26.x` 유지. Actions도 26 사용 |
| Settings → Environments → Production → Branch Tracking | 현재 `dev` 유지 |
| Settings → Environment Variables | 기존 데모 모드 설정 유지. 이번 작업에 비밀값 복사나 변경은 필요 없다 |
| 팀 abc-2-lab → Usage | 배포 한도와 남은 재시도 시간을 담당자가 확인. 불필요한 반복 Redeploy 금지 |
| GitHub 저장소 → Settings → Branches / Rules → Rulesets | 현 dev 보호·필수 체크 없음. 이번 작업에서는 바꾸지 않는다 |

계속 실패하는 과거 `Vercel` 상태를 수동 성공 처리하지 않는다.
향후 관리자가 필수 검사를 구성한다면 항상 완료되는 Actions 검사들을 검토하고,
정책상 실행하지 않는 Vercel 배포 검사를 모든 PR에 필수로 요구하지 않는다.
GitHub App 자체를 제거하거나 팀 전역 설정을 바꾸는 방식은 권하지 않는다.

## 승인 후 원격 적용 순서

1. GitHub 관리자와 UI 담당자가 PR Preview 기본 중지와 선택적 예외를 검토한다.
   이 문서는 dev→main 병합 규칙, 리뷰 책임, Draft를 변경하지 않는다.
2. 최신 dev를 이 작업 브랜치에 안전하게 통합하고 로컬 검증을 다시 확인한다.
   CI 파일은 관리자 리뷰를 받는다. **원격 push/PR은 별도 명시적 승인 후에만** 한다.
3. 일반 PR로 변경을 dev에 반영한다. Production Branch가 dev이므로 이 적용
   자체도 기존 연동에 의해 Production 배포를 일으킬 수 있음을 승인 범위에 포함한다.
4. 기존 열린 PR은 작성자가 정상 merge 등 팀 절차로 최신 dev를 반영한다.
   아직 정책 파일이 없는 과거 브랜치까지 dev의 설정만으로 통제된다고 가정하지 않는다.
   최초 정책 도입 push부터 배포가 한 건도 생성되지 않는다고 보장하지 않는다.
5. 한도 만료 후 자연스럽게 발생하는 후속 커밋의 SHA와 배포 기록을 대조한다.
   검증용 빈 커밋, 테스트 PR, 강제 재배포를 만들지 않는다.
6. 백엔드 전용 PR은 Actions의 Web 생략 완료와 Vercel 배포 미생성을,
   프런트엔드 PR은 Web 시험·빌드 성공을 확인한다.
   실제 미리보기가 합의된 브랜치는 추가로 Preview 생성도 확인한다.
7. 문제가 있으면 관리자 검토를 거쳐 설정을 되돌린다.
   `git.deploymentEnabled: true`는 배포량을 다시 늘리므로 무분별하게 되돌리지 않는다.

이 안은 일반 PR에서 반복되는 Preview 생성을 줄인다.
`dev`에 병합된 백엔드·문서 변경은 여전히 Production을 트리거할 수 있고,
허용된 Preview 브랜치도 파일 경로와 관계없이 배포될 수 있다.
CLI/API/Deploy Hook 수동 배포를 차단하는 기능도 아니다.
그 잔여 배포까지 파일 단위로 제한하려면 별도의 배포 workflow·권한·팀 합의가
필요하며 이번 최소 변경안에는 포함하지 않는다.

## 공식 근거

- [Vercel Git 설정과 브랜치 규칙](https://vercel.com/docs/project-configuration/git-configuration#git.deploymentenabled)
- [GitHub 연동의 기본 push/PR 배포](https://vercel.com/docs/git/vercel-for-github#a-deployment-for-each-push)
- [모노레포 자동 건너뛰기 요건과 Ignored Build Step](https://vercel.com/docs/monorepos#skipping-unaffected-projects)
- [자동으로 건너뛴 모노레포 배포의 rate limit 제외(2026-10-21)](https://vercel.com/changelog/skipped-monorepo-deployments-no-longer-count-against-limits)
- [Hobby 배포 한도와 집계 범위](https://vercel.com/docs/limits#deployments-per-day-hobby)
- [Production Branch 설정](https://vercel.com/docs/git#customizing-the-production-branch)
- [GitHub의 생략 workflow/job 상태 차이](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)
