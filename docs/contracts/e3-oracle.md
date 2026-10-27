# E③ 오라클 스펙 v0.1 — 수평 IDOR (설계 계약 초안)

작성: 2026-10-27 · 최민준 · 팀 검토용 초안
상태: **설계 초안. Go 구현·테스트 미작성.**
소스 스펙: `ABC2LAB/abc2lab-dast` 의 `packages/judge/src/abc2lab_judge/core/{oracle.py,model.py}` · `tests/test_oracle.py`.
**포팅 대상이지 드롭인 아님** — CLAUDE.md상 Python 사이드카는 미채택, 이 설계를 Go·ARTEX 증거 모델로 옮긴다.
연결: [평가 프로토콜](../evaluation/evaluation-protocol.md) · [엔진 개선 초안(이동찬)](../engine-improvement-draft.md)

## 0. 범위
- **v0.1 = 수평(horizontal) IDOR 조회 전용.** "B가 A의 등록 자원을 조회했을 때, 애플리케이션 정책상 금지인가"를 구조화 증거로 판정한다.
- **수직 인가는 범위 밖(후속 수직 오라클).** 평가 코어 GT 9개 중 수직 4개 — `adminSectionChallenge`·`feedbackChallenge`·`changeProductChallenge`·`registerAdminChallenge` — 는 이 오라클 대상이 아니다. v0.1 대상은 수평 5개(`basketAccessChallenge`·`basketManipulateChallenge`·`forgedFeedbackChallenge`·`forgedReviewChallenge`·`dataExportChallenge`).
- **평가 GT 채점(스코어보드)은 이 오라클과 무관.** baseline·개선 후 점수는 평가 프로토콜대로 Juice Shop 챌린지 실제 해결 상태로 매긴다. 이 오라클은 **우리 엔진의 확정 판정 정밀도**를 위한 것이지 채점기가 아니다.

## 1. 판정 모델
순수 함수 `Evaluate(case CaseInput, observedAt time.Time) OracleVerdict`. 네트워크·DB·원문·비밀키 없음. 저장된 비교결과·정책 snapshot·신원결과를 `CaseInput`으로 distill하는 **어댑터는 분리**(이동찬 E²·이경준 KG 경계).
세 체크 (a)정책 (b)신원 (c)데이터. 보수적 — 확인 못 한 것은 기본 `UNDECIDABLE`(fail-closed).

## 2. 입력 계약 (Go 포팅, model.py 기준)
```go
type CheckState string    // "yes" | "no" | "unknown"
type VerdictValue string  // "confirmed" | "suspected" | "undecidable" | "allowed"
type ExpectedAccess string  // "allow" | "deny" | "unknown"
type ResolutionState string // "resolved_none" | "resolved_allow" | "unknown"  (조회 안 함 ≠ 없음)
type SubjectStatus string   // "verified" | "unknown" | "expired" | "mismatch"

type PolicyAssertion struct {
    ExpectedAccess       ExpectedAccess
    Sharing, Delegation  ResolutionState
    HasRegisteredSource  bool
    ValidFrom, ValidUntil time.Time
    HintMatchesCanonical bool
}
type SubjectContext struct {
    OwnerStatus, EvaluatedStatus SubjectStatus
    OwnerRef, EvaluatedRef       string // "" == 미검증(None과 동일 취급)
}
type ComparisonFacts struct {
    SameResource, OwnerNormalData, CrossNormalData,
    NontrivialDataMatch, ReflectionExcluded, TemplateExcluded CheckState
}
type CaseInput struct {
    Policy   PolicyAssertion
    Subjects SubjectContext
    Facts    ComparisonFacts
}
type OracleVerdict struct {
    Value       VerdictValue
    PolicyCheck, IdentityCheck, DataCheck CheckState
    ReasonCodes []JudgeReason
}
```

이동찬 draft §5.1 필수증거 → 필드 / 생산자(경계):

| §5.1 필수증거 | oracle 필드 | 생산자(경계) |
|---|---|---|
| 시험자료(A 소유·비민감 표식) | (전제) canary 등록 | 운영자/fixture·KG(이경준) |
| A 정상 대조 | `Facts.OwnerNormalData` | 비교기(E² capture+compare) |
| B 정상 대조 | `Facts.CrossNormalData` + `Subjects.EvaluatedStatus=verified` | 비교기 + 신원(이동찬) |
| 기대 권한(B→A deny) | `Policy.*` (DENY + RESOLVED_NONE + 등록출처 + 힌트일치 + 유효창) | KG 정책 reader(이경준) |
| 교차 조회(실행 영수증) | 실행층 + `Facts.CrossNormalData` | E² gateway(이동찬) |
| 자료 일치(표식·반사/템플릿/오류 제외) | `Facts.NontrivialDataMatch` + `ReflectionExcluded` + `TemplateExcluded` | 비교기 |
| 비교 조건(작업·reset세대·정책버전·엔드포인트·자료상태) | CaseInput 바인딩 + `observed_at` + 유효창 | 서버 바인딩 / 어댑터(§5) |

## 3. 세 체크
### (a) check_expected_policy
등록 정책이 평가 주체 B를 **명시 DENY**하고 sharing·delegation이 모두 `RESOLVED_NONE`인가. 유효성은 **근거 관찰시각 `observed_at`** 기준(판정시각 TTL 경과만으로 당시 유효했던 근거를 버리지 않음). 등록출처 없음→`POLICY_MISSING`(UNKNOWN), 힌트≠정본 또는 관찰시각에 유효창 밖→`POLICY_INVALID`(UNKNOWN), 허용(직접 ALLOW / sharing·delegation RESOLVED_ALLOW)→NO, 공유·위임 불명→`SHARING_OR_DELEGATION_UNKNOWN`, 기대접근 불명→`EXPECTED_ACCESS_UNKNOWN`, 그 외 해소된 DENY→`POLICY_DENY_RESOLVED`(YES).

### (b) check_identity
A(소유자)·B(평가) 모두 `VERIFIED`이고 ref가 비어있지 않으며 **서로 다른** 주체인가. 만료·불명·빈 ref→UNKNOWN(`SUBJECT_NOT_VERIFIED`), 같은 주체→NO(`SUBJECTS_NOT_DISTINCT`), 아니면 YES.

### (c) check_data — **canary 표식 매칭으로 재정의 (의도적 분기, silent port 아님)**
oracle.py는 "소유자 객체의 **비자명 실데이터**"가 교차응답에 나타나는지 본다. **우리는 이를 `A의 등록 canary 표식` 매칭으로 바꾼다.** 실제 민감 데이터를 조회·저장하지 않기 위함이다(CLAUDE.md 절대규칙 8 완전 로컬·외부 유출 금지, DLP, 이동찬 draft §6 "비민감 표식 대안"). 따라서:
- `NontrivialDataMatch=YES` 의미 = **"A 소유로 등록된 비민감 canary가 B의 교차응답 자료 필드에 나타남"**(반사·템플릿·오류 배제 후).
- 여전히 불충족: HMAC 일치·`owner_id` 단독·id 반사·HTTP 200·공통 템플릿·오류 템플릿.
- **이것은 oracle.py "실데이터"로부터의 의도적 의미 분기다. 그대로 포팅하지 말 것**(§7 참조). 민감 응답을 받아 매칭하는 구현은 절대규칙 8 위반.

세 required 신호(`SameResource`·`OwnerNormalData`·`CrossNormalData`·`NontrivialDataMatch`·`ReflectionExcluded`·`TemplateExcluded`) 중 하나라도 NO→data NO, UNKNOWN 있으면→`DATA_UNVERIFIABLE`(UNKNOWN), 전부 YES→`DATA_NONTRIVIAL_MATCH`(YES).

## 4. 판정 규칙 (결정표)
| 정책 사유 | (b) identity | (c) data | 판정 |
|---|---|---|---|
| `POLICY_DENY_RESOLVED` | YES | YES | **CONFIRMED** |
| `POLICY_DENY_RESOLVED` | 그 외 | 그 외 | UNDECIDABLE |
| `POLICY_ALLOWS` / `SHARING_OR_DELEGATION_ALLOWS` | — | — | **ALLOWED** (B 검증 시) / 아니면 UNDECIDABLE |
| `EXPECTED_ACCESS_UNKNOWN` / `SHARING_OR_DELEGATION_UNKNOWN` | YES | YES | **SUSPECTED** |
| `POLICY_MISSING` / `POLICY_INVALID`(부재·무효·힌트 불일치) | — | — | UNDECIDABLE |
| 그 외·미확인 | — | — | UNDECIDABLE (fail-closed) |

- `ALLOWED`는 **B(평가주체) 단독 검증 축**(`_evaluated_subject_verified`)으로 결정 — (b) check_identity(A·B 구별)와 별개. 사유는 허용 1개만 싣는다.
- 사유 순서 = 정책 → 신원 → 데이터.
- 저장 시(§8-①) `ALLOWED`는 취약점 판정이 아니라 **"위반 미관찰"** 검사결과로 분해 예정.

## 5. 전제조건 (어댑터 책임, 순수 오라클 밖)
- **`CaseInput.Facts`는 전부 동일 대상 reset 세대 + 동일 정책버전의 근거여야 유효.** 서로 다른 reset 세대·정책버전이 섞이면 어댑터가 `Evaluate` 호출 **전에 `UNDECIDABLE`로 떨군다**(교차 비교의 통제변수가 깨지므로 판정 불가). 정책 유효성 자체는 `observed_at` 기준(§3 (a)).
- 같은 작업·의도·엔드포인트·자료상태에 속하는 근거만 한 case로 묶는다.
- 어댑터는 ARTEX 증거(traffic 증거 ref·신원/세션·KG 정책 snapshot)를 `CaseInput`으로 distill. 원문·비밀키는 순수 오라클에 넘기지 않는다.

## 6. 테스트 케이스 스펙 (table-driven, oracle.py 테스트 이식)
기준 `_CASE` = DENY+RESOLVED_NONE 정책, A≠B 모두 verified, facts 전부 YES. `(변형 → 기대 value · 핵심 reason)`:

| # | 변형 | 기대 value | 핵심 reason |
|---|---|---|---|
| 1 | (기준 그대로) | CONFIRMED | DENY_RESOLVED · SUBJECTS_DISTINCT_VERIFIED · DATA_NONTRIVIAL_MATCH |
| 2 | expected_access=ALLOW | ALLOWED | POLICY_ALLOWS (단일) |
| 3 | delegation=RESOLVED_ALLOW | ALLOWED | SHARING_OR_DELEGATION_ALLOWS |
| 4 | ALLOW + sharing=UNKNOWN | ALLOWED | 검증된 직접 허용은 UNKNOWN이 무효화 못함 |
| 5 | ALLOW + **A(owner) 미검증·ref 없음 + facts 전부 UNKNOWN**, B는 verified | ALLOWED | POLICY_ALLOWS. **identity_check=UNKNOWN(A 미검증이라 A·B 구별 미확정), data_check=UNKNOWN. ALLOWED는 check_identity가 아니라 B 단독 검증 축으로 결정 — identity/data UNKNOWN과 모순 아님** |
| 6 | ALLOW + **B(평가주체) expired/unknown/""** | UNDECIDABLE | SUBJECT_NOT_VERIFIED (→ ALLOWED는 B 검증을 요구함을 확인) |
| 7 | sharing=UNKNOWN (내용 불명) | SUSPECTED | SHARING_OR_DELEGATION_UNKNOWN |
| 8 | expected_access=UNKNOWN (내용 불명) | SUSPECTED | EXPECTED_ACCESS_UNKNOWN |
| 9 | has_registered_source=false | UNDECIDABLE | POLICY_MISSING (부재는 SUSPECTED 아님) |
| 10 | valid_until 과거 / valid_from 미래 | UNDECIDABLE | POLICY_INVALID (observed_at 기준) |
| 11 | hint_matches_canonical=false (강한 DENY라도) | UNDECIDABLE | POLICY_INVALID (정본≠힌트) |
| 12 | A==B (같은 주체) | ¬CONFIRMED | SUBJECTS_NOT_DISTINCT |
| 13 | reflection_excluded=NO / template_excluded=NO / nontrivial=NO | ¬CONFIRMED, data=NO | DATA_REFLECTION/TEMPLATE_PRESENT / DATA_NO_MATCH |
| 14 | nontrivial_data_match=UNKNOWN | UNDECIDABLE | DATA_UNVERIFIABLE |
| 15 | **전수 조합**: ExpectedAccess × Resolution² × registered × 유효창 | 불변식 | 부재·무효→UNDECIDABLE / CONFIRMED는 DENY+RESOLVED_NONE에서만 / SUSPECTED는 내용불명 두 사유에서만 / ALLOWED는 policy_check=NO |

→ Go: #1~#14 table test, #15 조합/property test. (test_oracle.py 23케이스 전수 대응)

## 7. Python → Go 포팅 주의
- **check_data는 canary 분기(§3 (c)) — oracle.py "실데이터"를 그대로 포팅하지 말 것.** 민감 응답 매칭 구현은 절대규칙 8 위반.
- `StrEnum` → typed string const(매직스트링 금지). `frozen dataclass` → 값 struct. tz-aware datetime → `time.Time`(UTC 강제). `str|None` 주체 ref → `""`와 nil 동일하게 미검증 취급. `tuple` 사유 → `[]JudgeReason`.
- **fail-closed 기본값(UNDECIDABLE) 반드시 보존.** 집합에 없는 정책 사유·미확인은 전부 UNDECIDABLE.

## 8. Open — 리뷰에서 정렬 (pending: 이동찬 / 이경준)
이 PR 리뷰에서 페어로 정렬할 포인트. 조용히 다르게 가지 않는다.
1. **ALLOWED 분해안** — 내부 value 4종 유지 vs 저장 시 `vuln_verdict∈{3종, nullable}` + `check_result="위반 미관찰"`. (이동찬)
2. **정책버전 ↔ 유효창 / 정본(canonical) reader** 매핑. (이동찬 ↔ 이경준)
3. **KG의 sharing/delegation 해소 상태·기대권한 계약** — "조회 안 함"을 "없음"으로 주지 말 것. (이경준)
4. **ComparisonFacts 비교기 스펙** — 비자명 canary 일치·반사/템플릿 배제 로직은 오라클 밖(실제 난이도). (이동찬 E² capture + 최민준 compare 규칙)
5. **구조화 판정 레코드 테이블명·마이그레이션** — `findings`/`finding_retests`와 분리. (최민준 ↔ 이동찬)
