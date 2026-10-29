# 평가 프로토콜 — ABC2LAB vs baseline(원본 ARTEX)

> baseline(원본 ARTEX, 윤재상 구동)과 개선 후(우리 포크)를 **같은 조건**으로 비교하기 위한
> 측정 조건의 **단일 원본**. 조건이 바뀌면 다른 데 손대지 말고 **이 파일만** 고친다.
> 챌린지 목록은 **평가 정답(GT)으로만** 쓴다 — 스캐너·탐색·판정·시나리오 입력에 넣지 않는다
> (CLAUDE.md 절대규칙 7).

## 1. 목적
- baseline vs 개선 후 공정 비교. 측정 조건의 단일 원본.
- 조건(버전·모델 태그·판정 기준·매트릭스)이 바뀌면 이 파일만 수정한다.

## 2. 평가 대상 고정
- 대상: **OWASP Juice Shop v15.0.0** (첫 과녁).
- Docker 이미지 태그: **`bkimminich/juice-shop:v15.0.0`** (`latest` 금지 — 버전 고정).
- 인스턴스 리셋: **스캔 1회마다 컨테이너 재생성**하여 이전 스캔 상태를 격리한다
  (Juice Shop은 인메모리 상태라 재시작으로 초기화).
  ```bash
  # 스캔 1회 = 아래 한 세트 (반복마다 재생성)
  docker rm -f juice-shop 2>/dev/null
  docker run -d --name juice-shop -p 3000:3000 bkimminich/juice-shop:v15.0.0
  # 헬스 확인 후 스캔 → 스코어보드 스냅샷 저장(아래) → 컨테이너 제거
  ```
- **스코어보드 스냅샷 저장 (스캔 종료 직후, 컨테이너 제거 전)**: `GET /api/Challenges` 응답을 저장한다.
  Juice Shop은 인메모리라 컨테이너를 지우면 해결 상태가 사라진다. 두 벌로 둔다:
  - **원본** — 응답 본문 그대로 (재채점·감사용).
  - **정규화** — `{챌린지 key: solved(bool)}`. 원본에서 결정적으로 만들고, 채점은 이것만 읽는다.
    GT 챌린지 key가 정규화본에 없으면 **채점 에러**(미해결로 간주하지 않는다).
  - 둘 다 결과물이라 커밋 금지(절대규칙 2) — `.gitignore` 대상 결과물 디렉토리에 둔다.
- 대상 origin은 설정(`.env`)으로 고정, 코드 하드코딩 금지.

## 3. 챌린지 선정 (정답 역할, GT 제작 아님)
- **출처(고정)**: juice-shop 레포 태그 **`v15.0.0`** 의 `data/static/challenges.yml` (추출일 2026-10-26, 전체 102개).
- 참고 문서: https://help.owasp-juice.shop/part2/
- 선정 기준: **우리 탐지 기계가 겨냥하는 인가(Authorization) 클래스만** — 다계정 IDOR 오라클 + authz matrix(수평+수직)가 판정 가능한 접근통제.
- **축 태그(Authorization Matrix thesis = 수평+수직 통합 진단)**:
  - **수평(H)**: 동일 권한에서 타 사용자 객체 접근/변조 (IDOR/BOLA, 다계정 비교로 판정)
  - **수직(V)**: 상위 권한·관리자 전용 기능 접근 (권한상승/BFLA)

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

## 4. 판정 기준
1. 제목(이름) 매핑만으로 성공 판정하지 않는다.
2. 실제 챌린지 해결 상태 + 요청·응답 증거를 확인한다. **증거 있음 = 아래 3조건 모두 충족**,
   하나라도 확인 못 하면 확정이 아니라 **판단불가**:
   - (a) 판정 레코드의 `traffic_evidence_refs`가 비어 있지 않다.
   - (b) 그 ref가 같은 회차 traffic 기록에 실재한다.
   - (c) 그 증거 요청의 대상이 해당 챌린지 자원이다(GT 상관과 같은 기준).

   증거 조건은 **확정 경로에만** 적용한다. confirmed 주장인데 챌린지가 미해결이면 증거 유무와 무관하게 **오탐**이다
   (증거 부족을 오탐 회피 수단으로 쓰지 않는다).
3. 선정 목록에 매핑 안 된 finding = 즉시 오탐 처리하지 않는다(별도 분류: out-of-list).
4. 미해결 챌린지 = 자동으로 미탐 처리하지 않는다(범위 밖 제외와 구분). GT 챌린지에 엔진 판정이 없고 미해결이면,
   엔진이 그 챌린지 자원을 실제로 검사했다는 **커버리지 기록이 있을 때만 미탐**, 없으면 **판단불가**.
   커버리지 기록은 판정 레코드와 별개 출처(커버리지 지도 등)여야 하며, 출처가 없으면 = 커버리지 없음.
5. 스캔마다 Juice Shop 인스턴스를 리셋한다(§2와 동일).
6. GT 챌린지에 대한 엔진 판정이 **ALLOWED뿐**이면 **판단불가 + 경고로 표면화**한다. 코어 GT는 전부 실제 취약이라
   ALLOWED는 KG·정책 오류 신호다 — 미탐·취약점없음으로 기록하지 않는다.

**판정표** (챌린지별. 레코드가 여럿이면 confirmed > suspected > undecidable > allowed로 가장 강한 것 하나):

| 엔진 판정 | 해결(스냅샷) | 증거 3조건 | 커버리지 | → 칸 |
|---|---|---|---|---|
| confirmed | 해결 | 충족 | — | 확정 |
| confirmed | 해결 | 미충족·확인불가 | — | 판단불가 |
| confirmed | 미해결 | (무관) | — | 오탐 |
| suspected / undecidable | (무관) | — | — | 판단불가 |
| allowed뿐 | (무관) | — | — | 판단불가 + 경고 |
| 없음 | 미해결 | — | 있음 | 미탐 |
| 없음 | 미해결 | — | 없음 | 판단불가 |
| 없음 | 해결 | — | (solve가 증거) | 미탐 ¹ |

¹ 해결(solve) 자체가 엔진이 그 챌린지를 실제로 건드렸다는 커버리지 증거라 **귀속 가능한 미탐**이다(해결시키고 보고 안 함).
평가 하네스(계정 provisioning 등)가 유발한 solve로 식별되면 그 solve는 엔진 귀속에서 제외한다(엣지).

> E③ 오라클의 CONFIRMED/SUSPECTED/UNDECIDABLE과 정합: 증거 부족은 UNDECIDABLE(확정 아님).
> 표에 없는 조합·확인 못 한 값은 전부 판단불가다(유리한 쪽으로 해석하지 않는다). baseline·개선 후에 **같은 표**를 적용한다.

## 5. 측정 매트릭스 (값은 비움, 틀만)
- 각 조건은 LLM 비결정성 때문에 **N회 반복(권장 3~5회)**, 각 셀은 **평균 ± 표본 표준편차(SD, n−1)**. 1회 숫자로 결론 금지.
- **확정 수는 (a) 코어 GT(인가 9개) 기준과 (b) BAC category 전체(10개) 기준을 둘 다 기록**한다(cherry-pick 방지).
- **총 요청 수 = 실제로 대상에 전송된 HTTP 요청만**(traffic 기록분). E² 차단 시도·LLM 호출은 제외한다. **차단 시도 수는 별도 지표**로 기록한다.

**통제변수 = 단일 모델 태그 하나.** 비교는 **같은 태그**에서 원본 vs 우리로만 성립한다(§6).

| 조건 | 확정까지 시간 | 총 요청 수 | 확정 수 | 오탐 수 | 미탐 수 | 판단불가 수 |
|---|---|---|---|---|---|---|
| 원본 @35b | — | — | — | — | — | — |
| 우리 @35b | — | — | — | — | — | — |

- **(후순위 — 현재 측정 대상 아님)** 소형 모델 축 `원본 @7B` / `우리 @7B`: 로컬 35B급으로 충분함을 확인해 7B급까지 좁히는 최적화는 뒤로 미룸(CLAUDE.md 로컬 충분성). 측정 재개 시 같은 틀로 두 행 추가.
- 반복 원자료는 별도 집계 스크립트가 수집(최민준, 평가 스크립트 뼈대).

## 6. 모델·환경 핀
- LLM provider: **Ollama** (완전 로컬, 외부 API로 대상 데이터 유출 금지 — 절대규칙 8).
- `ARTEX_LLM_BASE_URL` = `<TODO: Ollama 엔드포인트, 예 http://127.0.0.1:11434>`
- **통제변수 = 단일 모델 태그 하나.** baseline(원본 ARTEX)과 개선 후(우리)가 **똑같은 태그 문자열**을 써야 비교가 성립한다. 서로 다른 태그로 재면 통제변수가 깨진다. 태그는 **정확한 문자열**로 고정, 추측 금지:
  - `model_main` = `TODO(최민준): baseline 확정 태그(35b급)` — 로컬 **35B급** 모델. 프로그램 구동은 35b로 확인됨. 정확한 Ollama 태그 문자열은 baseline 첫 실행 때 고정한다(아직 baseline 미실행이라 실태그 없음 — 지어내지 않음).
- **(후순위) 소형 모델 축** — 현재 측정 대상 아님. 35B급으로 충분함을 확인해 7B급까지 좁히는 최적화는 뒤로 미룬다. 측정 재개 시 `model_small`(7B급 정확한 태그)로 별도 축 추가.
- 샘플링 고정값(가능하면): `temperature` = `<TODO, 권장 0>`, `seed` = `<TODO 고정>`.
- baseline·개선 후 **동일 태그·동일 샘플링**으로만 비교 성립.

## 7. 특화점은 측정 후 결정 (미리 못박지 않음)
- **(A) 효율**: 같은 모델에서 확정까지 시간·총 요청 수 감소.
- **(정밀도)**: 오탐 감소 + 판정이 증거로 방어가능(UNDECIDABLE 남용 아님).
- **(B) 경량 생존** (후순위, 현재 측정 대상 아님): 7B급으로도 35B급 품질 유지 → "7B 생존 + 오탐↓". 지금은 35B 단일 태그 측정이 우선, 소형 모델 축은 뒤로.

> ⚠️ 특화점 지표는 **취약점 개수(recall)가 아니다.** 우리 엔진 개선(E①②③④)은 통제·판정 정밀도·효율을
> 높이는 것이지 탐지 개수를 직접 늘리지 않는다. recall로 자랑하지 않는다.
