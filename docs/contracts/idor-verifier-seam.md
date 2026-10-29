# IDOR 검증기 ↔ 엔진 seam 계약 v0.1 (설계 초안)

작성: 2026-10-29 · 최민준 · 팀 검토용 초안
상태: **설계 초안. 실 전송·실 정책 미구현(stub + TODO).** 임시확정값은 민준·경준, E² 파트는 동찬 복귀 후 리뷰·소유.
연결: [E③ 오라클](./e3-oracle.md) · [KG→판정·집행 경계](./kg-policy.md) · [판정 레코드](./verdict-record.md) · 코드 `idor/verifier.go`

## 0. 범위
수평 IDOR 검증기(`idor/verifier.go`, 최민준)가 **엔진과 만나는 경계 두 개**만 고정한다.
- **Transmitter** = 실제 전송 통제(E②, 이동찬). 검증기는 요청을 *제안*만 하고 전송 여부는 E²가 정한다(절대규칙 3).
- **PolicyResolver** = 소유·권한 KG(이경준). 정책·canary를 검증기가 *읽기*만 한다.

비범위: 오라클 판정 로직(e3-oracle), KG 내부 저장(kg-policy), 평가 채점(평가 프로토콜).
**이 문서는 코드·실행을 추가하지 않는다** — 규약(resource_ref)과 ARTEX 실물 seam 지점 대조, 그리고 임시확정값 기록이 전부다.

## 1. resource_ref 규약 = `type:id` (임시확정 · 민준·경준)
검증기 `AccessRequest.ResourceRef`·`Candidate.ResourceRef`, 판정 레코드(verdict-record), GT 상관(correlation)이 **같은 규약**을 쓴다.

- 형식: `type:id` — **마지막 콜론** 앞이 `type`, 뒤가 `id`.
- `type` = 안정 정규 자원종류(한 reset 세대를 넘어 유효, 상관·정책·canary 키). 콜론을 품을 수 있다(예: `rest:basket:42` → type `rest:basket`, id `42`).
- `id` = 한 reset 세대 안에서만 유효한 관찰 인스턴스. **상관·정책 매칭에 쓰지 않는다**(correlation은 type 접두부 + vuln_class로만 매칭 — PR #19).
- `owner`는 ref에 섞지 않고 **별도 인자**(`Candidate.Owner`, `ResolvePolicy(resourceRef, owner, evaluated)`). 소유자는 ref가 아니라 주체다.
- raw URL 아님 — `type:id` → 대상 스코프 URL 해석은 E²의 몫(절대규칙 1·3).

## 2. 검증기 입력 (엔진 → 검증기가 요구하는 것)

### 2.1 actor 신원 / 세션 대여 — `AccountRef`
검증기는 "이 actor로 보내 달라"만 말하고 자격 자체는 쥐지 않는다.
- `Label` — 비민감 이름, 오라클이 보는 주체 ref로도 쓰임(절대규칙 2: 이름만).
- `Handle` — E²의 **불투명 세션 참조**(자격 원문 아님). 세션 대여·반납·만료는 E²가 관리.
- `Status`(`oracle.SubjectStatus`) — 신원 검증 결과를 **E²가 채운다**(kg-policy §3). 미설정(zero)=미검증(fail-closed). 검증기는 Handle 유무로 추론하지 않음(PR #17).

**ARTEX 실물 seam**: 엔진은 acting 주체를 `intercept.Decision.ProfileID int64`로 식별한다. `Handle`은 이 프로파일/세션에 대한 검증기 쪽 불투명 참조로, E² 배선 시 ProfileID(또는 그 상위 세션 식별자)에 바인딩된다. 신원 status↔ref 결합은 kg-policy Open #5 — 동찬과 E² 배선에서 정렬.

### 2.2 전송 요청 — `AccessRequest` → `Transmitter.Send`
```
Transmitter interface { Send(req AccessRequest) (Capture, bool) }
AccessRequest{ As AccountRef; Method string; ResourceRef string; StateChanging bool; Inputs []string }
```
- 검증기는 요청을 제안; E²가 허용/차단, 네트워크 전송, 본문 스코핑, Normal/ErrorTemplate 분류(상태코드 아닌 본문 모양 — 랩은 오류에도 HTTP 200)를 수행.
- `StateChanging=true`(setup: A 자신 자원에 등록 canary 심기)는 E²가 **승인+리셋 시험환경** 뒤로 게이트. 교차조회는 GET(읽기 한정, 절대규칙 6).
- 허용·완료 → `ok=true`+`Capture`. 거부·판단불가 → `ok=false`, 검증기는 그 단계를 **검증불가로** 두고 성공으로 기록하지 않음(절대규칙 5).

**ARTEX 실물 seam**: 전송은 **승인 게이트 `guard.Guard`**(preToolUse 훅 → `applyIntercept`)와 **`intercept.Interceptor`**(`Match`/`Judge`/`HandleAsk`/`Decide`)를 통과한다. `intercept.Decision.Action ∈ {allow, deny, ask}`가 곧 `Send`의 `ok`에 대응(allow→ok=true 전송, deny/ask-미승인→ok=false). 우회 전송 경로 없음(절대규칙 3).

## 3. 검증기 출력 (검증기 → 오라클이 먹는 증거 구조)
검증기는 캡처를 모아 `Compare`로 비교신호를 만들고 `oracle.Evaluate`에 넘긴다 — **오라클 입력 구조가 곧 출력 계약**이다.
```
oracle.Evaluate(oracle.CaseInput{ Policy, Subjects, Facts }, observedAt) → oracle.OracleVerdict
```
- `Policy oracle.PolicyAssertion` — `PolicyResolver.ResolvePolicy`에서. 미해결(`ok=false`)이면 zero값(=등록 출처 없음)→UNDECIDABLE, 묵시 allow/deny 금지.
- `Subjects oracle.SubjectContext` — owner/evaluated의 status·ref(§2.1).
- `Facts oracle.ComparisonFacts` — canary 마커 신호만(원문·HMAC키 없음, 절대규칙 2/8). canary는 `PolicyResolver.Canaries`에서(KG 등록·분류, 검증기는 소비만 — kg-policy §3.1).
- `observedAt` — 판정 대상 증거의 관찰 시각(교차 read 우선, 없으면 owner read, 없으면 zero=정책 무효 fail-closed). 오라클은 판정 시각이 아니라 이 시각에 정책 유효성을 본다.

**ARTEX 실물 seam**: 전송 응답 증거는 `traffic`(MITM 기록)→`evidence.Store`(`Record(ctx, RecordFindingInput, []db.TrafficRef)`, `ReadEvidence→EvidenceExchange`)에 저장·바인딩된다. 검증기의 `Capture`/`ResponseView`는 이 저장 증거에서 **스코핑된 비민감 본문**만 받는 얇은 뷰다. 저장 레코드의 발행·감사·개정은 어댑터 몫(verdict-record §2, e3-oracle §8-⑤) — 이 패키지 아님.

## 4. 임시확정값 (민준·경준 — 동찬 리뷰 대기)
| 항목 | 임시확정 | 비고 |
|---|---|---|
| resource_ref 형식 | `type:id`(마지막 콜론 split) | §1. correlation·verdict-record 공통 |
| owner | ref와 분리, 별도 인자 | `ResolvePolicy(resourceRef, owner, evaluated)` |
| 판정 레코드 물리 형식 | **JSON 덤프** | DB 쿼리 vs JSON은 동찬 결정(verdict-record §5, #18 열린 질문) |
| 상태변경 허가 정책 | **비움** | E²(동찬) 소유. `Transmitter` stub의 TODO |
| 실 전송 | **비움(stub)** | E²(동찬) 소유. 플럼빙·인터페이스까지만 |
| `type` 명명 규칙 | **미정** | E²(동찬) 확정 후 correlation `types`·레코드가 채움. 짐작 금지 |

## 5. Open / 핸드오프 (동찬)
- **실 `Transmitter`** — `idor/verifier.go`의 seam은 fake(테스트)뿐. 실 구현은 §2.2 ARTEX 경계(guard+intercept) 배선 = E²(동찬). 금요일 복귀 후 전송만 끼우면 교차조회 1건이 end-to-end로 도는 자리까지 맞춰 둠.
- **상태변경 허가 정책** — setup(POST)·destructive-last의 승인+리셋 게이트 정책은 E²(동찬). 검증기는 `StateChanging` 플래그만 올린다.
- **`type` 명명** — resource_ref의 type을 E²가 어떻게 뽑을지 확정되면 평가 쪽 GT 상관 데이터(`scripts/eval/`)의 `types`와 레코드가 채워진다(지금은 전부 비어 out-of-list).
- **신원 status↔ref 결합** — kg-policy Open #5, E² 배선에서 정렬.
