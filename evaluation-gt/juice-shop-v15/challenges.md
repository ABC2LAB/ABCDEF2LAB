# Juice Shop v15.0.0 평가 정답(GT) — 챌린지 선정 결과

> 평가 정답이다. **채점·평가 집계에만** 쓴다. 엔진·탐색·판정·시나리오·스크립트 계획·모델 입력에 넣지 않는다(CLAUDE.md 절대 규칙 7).
> 이 디렉터리는 저장소 루트 `.ignore`로 ripgrep 기반 검색(세션의 Grep·Glob, 탐색 에이전트)에서 빠진다. 그래도 일반 `grep`·`git grep`은
> 읽을 수 있으니, 평가 정답이 필요한 작업이 아니면 이 경로를 열거나 검색하지 않는다.

선정 출처·기준·축 정의는 [평가 프로토콜 §3](../../docs/evaluation/evaluation-protocol.md#3-챌린지-선정-정답-역할-gt-제작-아님)이다.
채점기가 읽는 기계 판은 같은 디렉터리의 `challenges.json`이고(`scripts/eval/ground_truth.py`가 읽는다), 아래 표와 같은 목록이다
(시험이 대조한다). 실제 정답이 필요한 시험은 `evaluation-gt/tests/`에 있고, 평가 스크립트 시험(`scripts/eval/tests`)은 합성 정답 목록(`scripts/eval/tests/data/synthetic-gt.json`)으로 돈다.

### 표 1 — Broken Access Control category 전체 10개 (부차 지표 기준)
| key | name | diff | 축 | 코어 GT | 비고 |
|---|---|---|---|---|---|
| adminSectionChallenge | Admin Section | 2 | V | ○ | 관리영역 접근 |
| basketAccessChallenge | View Basket | 2 | H | ○ | **IDOR: 타인 장바구니 조회** (핵심) |
| feedbackChallenge | Five-Star Feedback | 2 | V | ○ | 5성 피드백 전체 삭제 = 관리기능(BFLA) |
| basketManipulateChallenge | Manipulate Basket | 3 | H | ○ | IDOR: 타인 장바구니 변조 |
| changeProductChallenge | Product Tampering | 3 | V | ○ | 상품 설명 변조 = 관리기능(BFLA) |
| csrfChallenge | CSRF | 3 | — | ✗ | category상 BAC이나 **범위 밖(CSRF 클래스)** |
| forgedFeedbackChallenge | Forged Feedback | 3 | H | ○ | 타인 명의 피드백 작성 |
| forgedReviewChallenge | Forged Review | 3 | H | ○ | 타인 명의 리뷰 작성/수정 |
| easterEggLevelOneChallenge | Easter Egg | 4 | — | ✗ | **범위 밖(숨김파일 탐색, 인가 아님)** |
| ssrfChallenge | SSRF | 6 | — | ✗ | **범위 밖(SSRF 클래스)** |

- ○ 7개 = 코어 GT, ✗ 3개 = 명시적 제외. **BAC category 전체(10) 해결 수는 §5에서 부차 지표로 병기**(cherry-pick 방지).

### 표 2 — 다른 category의 인가 클래스 (코어 GT 추가)
| key | name | category | diff | 축 | 비고 |
|---|---|---|---|---|---|
| registerAdminChallenge | Admin Registration | Improper Input Validation | 3 | V | 관리자 권한으로 가입 = 권한상승 |
| dataExportChallenge | GDPR Data Theft | Sensitive Data Exposure | 4 | H | 타인 개인정보 탈취(수평) |

> **코어 GT = 표1의 ○ 7개 + 표2의 2개 = 9개 (수평 5 · 수직 4).**

### 별도 축 (후속) — 코어 GT 아님
우리 scope 헌장엔 "업무흐름 우회"가 있으나, 아래는 **단일 계정 로직·크립토 결함**이라 현재 탐지 기계(다계정 IDOR 오라클·authz matrix)가 겨냥하는 대상이 아니다. **코어 GT에 넣으면 겨냥 안 한 항목이 인위적 미탐으로 찍히므로 제외하고 후속 축으로 남긴다.** 코어 미탐 계산에 포함하지 않는다.

| key | name | category | 축 | 성격 |
|---|---|---|---|---|
| negativeOrderChallenge | Payback Time | Improper Input Validation | BF | 음수 주문 = 단일계정 업무로직 |
| freeDeluxeChallenge | Deluxe Fraud | Improper Input Validation | BF | 결제 우회 = 단일계정 업무로직 |
| deprecatedInterfaceChallenge | Deprecated Interface | Security Misconfiguration | BF | 폐기 B2B 인터페이스 = 설정/업무흐름 |

> **제외 클래스 메모(검토함, 의도적 제외 — 인가 아님)**: `Broken Authentication` 전체(로그인·비번재설정·2FA = 신원/인증),
> `loginAdmin/Bender/Jim`·`noSqlOrders`(Injection 경유 사칭/접근), `jwtUnsigned/Forged`(토큰 위조),
> `nullByte`·`emailLeak`·백업·문서·로그 노출류(입력검증/민감정보 노출). 주 클래스가 인가가 아니라 GT에서 뺐다.

> ⚠️ 이 목록은 **평가 정답(GT)으로만** 쓴다 — 스캐너·탐색·판정·시나리오 입력 금지(CLAUDE.md 절대규칙 7).
