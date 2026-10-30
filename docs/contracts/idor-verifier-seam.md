# IDOR 검증기 ↔ 엔진 seam 계약 v0.1 (설계 초안)

작성: 2026-10-29 · 최민준 · 2026-10-30 이동찬 검토 반영 · 팀 검토용 초안
상태: **설계 초안. 실 전송·실 정책·판정 저장 어댑터 미구현.** §6은 엔진 연결의 실행·저장 책임을 정리한 제안이며, 구현 완료를 뜻하지 않는다.
연결: [E③ 오라클](./e3-oracle.md) · [KG→판정·집행 경계](./kg-policy.md) · [판정 레코드](./verdict-record.md) · 코드 `idor/verifier.go`

## 0. 범위
수평 IDOR 검증기(`idor/verifier.go`, 최민준)가 **엔진과 만나는 경계 두 개**만 고정한다.
- **Transmitter** = 실제 전송 통제(E②, 이동찬). 검증기는 요청을 *제안*만 하고 전송 여부는 E²가 정한다(절대규칙 3).
- **PolicyResolver** = 소유·권한 KG(이경준). 정책·canary를 검증기가 *읽기*만 한다.

비범위: 오라클 판정 로직(e3-oracle), KG 내부 저장(kg-policy), 평가 채점(평가 프로토콜).
**이 문서는 코드·실행을 추가하지 않는다** — 기존 코드에서 확인한 사실과 새로 연결할 책임을 구분한다. 엔진 설계 방향은 [개발 계획](../development-plan.md)과 [엔진 개선 초안](../engine-improvement-draft.md)을 따른다.

## 1. resource_ref 규약 = `type:id` (임시확정 · 민준·경준)
검증기 `AccessRequest.ResourceRef`·`Candidate.ResourceRef`, 판정 레코드(verdict-record), GT 상관(correlation)이 **같은 규약**을 쓴다.

- 형식: `type:id` — **마지막 콜론** 앞이 `type`, 뒤가 `id`.
- `type` = 안정 정규 자원종류(한 reset 세대를 넘어 유효, 상관·정책·canary 키). 콜론을 품을 수 있다(예: `rest:basket:42` → type `rest:basket`, id `42`).
- `id` = 한 reset 세대 안에서만 유효한 관찰 인스턴스. **평가 상관**은 type 접두부 + vuln_class로만 매칭한다(PR #19). 소유·기대 권한 조회와 실제 전송 대상은 전체 `resource_ref` 및 서버 문맥의 reset 세대로 구분하여 다른 인스턴스를 섞지 않는다.
- `owner`는 ref에 섞지 않고 **별도 인자**(`Candidate.Owner`, `ResolvePolicy(resourceRef, owner, evaluated)`). 소유자는 ref가 아니라 주체다.
- raw URL 아님 — `type:id` → 대상 스코프 URL 해석은 E²의 몫(절대규칙 1·3).

## 2. 검증기 입력 (엔진 → 검증기가 요구하는 것)

### 2.1 actor 신원 / 세션 대여 — `AccountRef`
검증기는 "이 actor로 보내 달라"만 말하고 자격 자체는 쥐지 않는다.
- `Label` — 비민감 이름, 오라클이 보는 주체 ref로도 쓰임(절대규칙 2: 이름만).
- `Handle` — E²의 **불투명 세션 참조**(자격 원문 아님). 세션 대여·반납·만료는 E²가 관리.
- `Status`(`oracle.SubjectStatus`) — 신원 검증 결과를 **E²가 채운다**(kg-policy §3). 미설정(zero)=미검증(fail-closed). 검증기는 Handle 유무로 추론하지 않음(PR #17).

**기존 코드와 연결 책임**: `intercept.Decision.ProfileID`는 LLM 판정 모델의 프로필이다(`intercept/intercept.go`의 `Reviewer`·`JudgeConfig`, `server/intercept.go`의 `wireInterceptReviewer`). 대상 웹 계정 식별자로 사용하지 않는다. 서버/E²는 작업에 등록된 웹 계정에서 `Label`·`Handle`·`Status`를 묶고, 실제 전송 시 해당 세션의 신원·유효성을 확인한다. 모델이 준 status나 Handle 존재만으로 검증됨을 인정하지 않는다. 실 세션 저장소·대여 구현은 미완이며, 신원 status↔ref 결합은 kg-policy Open #5와 정렬한다.

### 2.2 전송 요청 — `AccessRequest` → `Transmitter.Send`
```
Transmitter interface { Send(req AccessRequest) (Capture, bool) }
AccessRequest{ As AccountRef; Method string; ResourceRef string; StateChanging bool; Inputs []string }
```
- 검증기는 요청을 제안; E²가 허용/차단, 네트워크 전송, 본문 스코핑, Normal/ErrorTemplate 분류(상태코드 아닌 본문 모양 — 랩은 오류에도 HTTP 200)를 수행.
- 현재 `idor.Verify`는 setup POST(`StateChanging=true`)를 제안한 뒤 owner GET·cross GET을 제안한다. **초기 조회 통합에서는 운영자/fixture가 비민감 canary를 미리 준비하고 E²가 setup POST를 거부한다.** 이후 owner read로 기존 표식이 확인되어야 비교 증거가 된다. setup POST 허용은 별도 승인·리셋 시험환경에서만 확장한다. GET도 등록된 조회 행동인지 E²가 확인한다.
- 허용·완료 → `ok=true`+`Capture`. 거부·판단불가 → `ok=false`, 검증기는 그 단계를 **검증불가로** 두고 성공으로 기록하지 않음(절대규칙 5).

**기존 코드와 연결 책임**: `guard.Guard`·`intercept.Interceptor`는 도구 호출의 승인 게이트다. `Decision.Action=allow`는 실행 승인일 뿐, 요청 송신·응답 수신이나 `Send`의 `ok=true`를 뜻하지 않는다. 새 E² 전송기는 지원하는 HTTP 경로의 실제 전송 직전에 대상 origin·계정·행동·승인·예산·중지를 검사하고, 리다이렉트·재시도를 지원하면 매번 다시 검사한다. 셸·MCP 등 미지원 경로는 통제 모드에서 차단/제외한다. 기존 프록시 설정만으로 우회가 없다고 주장하지 않으며, 검증된 경로만 통제 범위로 기록한다.

## 3. 검증기 출력 (검증기 → 오라클이 먹는 증거 구조)
`idor.Verify(c Candidate, tx Transmitter, kg PolicyResolver) Result`는 캡처를 모아 `idor.Compare`·`oracle.Evaluate`를 호출하고 `Result{Verdict, Facts}`를 반환한다. 별도의 `idor.Verifier` 타입이나 파이프라인 전체의 `ok` 반환값은 없다.
```
oracle.Evaluate(oracle.CaseInput{ Policy, Subjects, Facts }, observedAt) → oracle.OracleVerdict
```
- `Policy oracle.PolicyAssertion` — `PolicyResolver.ResolvePolicy`에서. 미해결(`ok=false`)이면 zero값(=등록 출처 없음)→UNDECIDABLE, 묵시 allow/deny 금지.
- `Subjects oracle.SubjectContext` — owner/evaluated의 status·ref(§2.1).
- `Facts oracle.ComparisonFacts` — canary 마커 신호만(원문·HMAC키 없음, 절대규칙 2/8). canary는 `PolicyResolver.Canaries`에서(KG 등록·분류, 검증기는 소비만 — kg-policy §3.1).
- `observedAt` — 판정 대상 증거의 관찰 시각(교차 read 우선, 없으면 owner read, 없으면 zero=정책 무효 fail-closed). 오라클은 판정 시각이 아니라 이 시각에 정책 유효성을 본다.

**기존 코드와 연결 책임**: ARTEX에는 `traffic` 기록과 `evidence.Store`가 있지만, E²의 `Capture`/`ResponseView`·판정 레코드로 연결하는 어댑터는 미구현이다. E²는 요청·응답을 식별하는 증거 참조와 **스코핑된 비민감 본문**을 공급해야 한다. `evidence.Store.Record`는 finding 생성까지 수행하므로 판정 전 캡처 보관용으로 호출하지 않는다. finding 생성은 §6.3의 확정 저장 게이트를 따른다. 판정 레코드의 발행·감사·개정은 서버 어댑터 몫(verdict-record §2, e3-oracle §8-⑤)이며, 인증 원문·응답 원문을 판정 레코드에 저장하지 않는다.

## 4. 임시확정값과 미구현 항목
| 항목 | 임시확정 | 비고 |
|---|---|---|
| resource_ref 형식 | `type:id`(마지막 콜론 split) | §1. correlation·verdict-record 공통 |
| owner | ref와 분리, 별도 인자 | `ResolvePolicy(resourceRef, owner, evaluated)` |
| 판정 레코드 물리 형식 | **JSON 덤프 제안, 미구현** | DB 쿼리 vs JSON은 동찬 결정(verdict-record §5, #18 열린 질문) |
| 상태변경 허가 정책 | **비움** | E²(동찬) 소유. `Transmitter` stub의 TODO |
| 실 전송 | **비움(stub)** | E²(동찬) 소유. 플럼빙·인터페이스까지만 |
| `type` 명명 규칙 | **미정** | E²(동찬) 확정 후 correlation `types`·레코드가 채움. 짐작 금지 |

## 5. Open / 핸드오프 (동찬)
- **실 `Transmitter`** — `idor/verifier.go`에는 전송 없이 false를 반환하는 stub이 있다. E²(동찬)가 §2.2의 실제 전송 경계를 구현해야 하며, 실 `PolicyResolver`·증거 어댑터·§6 저장 연결도 있어야 end-to-end 검증이 가능하다.
- **상태변경 허가 정책** — setup(POST)·destructive-last의 승인+리셋 게이트 정책은 E²(동찬). 검증기의 `StateChanging`은 제안 정보이며 실행 허가가 아니다. 초기 조회 경로의 fixture 준비와 setup POST 차단은 §2.2를 따른다.
- **`type` 명명** — resource_ref의 type을 E²가 어떻게 뽑을지 확정되면 평가 쪽 GT 상관 데이터(`scripts/eval/`)의 `types`와 레코드가 채워진다(지금은 전부 비어 out-of-list).
- **신원 status↔ref 결합** — kg-policy Open #5, E² 배선에서 정렬.

## 6. 엔진 연결 계약 (제안 · 이동찬 구현용)

### 6.1 현재 호출과 새 실행 책임
`oracle.Evaluate`는 I/O 없는 순수 판정 함수다. `idor.Verify`는 setup POST·owner GET·cross GET을 `Transmitter`에 제안하는 **실행 흐름**이므로, DB 결과 저장 함수 안에서 호출하지 않는다.

현재 `agent/retester.go`의 재검증 에이전트는 검사를 수행한 **뒤** `record_finding_retest_result`를 호출한다. `server/finding_retests.go`의 해당 도구는 서버가 가진 conversation ID로 `db.RecordFindingRetestResult`에 문자열 verdict·summary·evidence를 전달한다. 이 저장 호출을 `Verify` 실행 지점으로 바꾸면 앞선 검사 뒤에 추가 요청이 발생한다.

새 인가 검증 경로는 서버/엔진이 다음 책임을 나누어 연결한다. 아래는 구현할 계약이며 현재 동작이 아니다.

| 단계 | 책임 |
|---|---|
| 후보 구성 | 서버가 재검증 conversation→finding→원래 작업을 확인하고, 등록된 계정·자원에서 `idor.Candidate`를 구성한다. 모델의 ID·계정 참조는 다시 검증하며 자격 원문이나 모델이 선언한 신원 상태를 신뢰하지 않는다. 작업·검사·정책 버전·reset 세대는 서버 문맥에 바인딩한다. |
| 검사 실행 | 엔진의 검증 실행 단계에서 `idor.Verify(candidate, transmitter, policyResolver)`를 호출한다. 해당 인가 검사는 이 경로로 수행하고, 기존 LLM 도구로 같은 검사를 실행한 뒤 저장 시점에 다시 실행하지 않는다. 추가 실행은 새 실행 식별자와 E² 판단이 필요하다. |
| 판정 | `Verify` 내부의 `Compare`·`Evaluate`가 `Result.Verdict`·`Result.Facts`를 만든다. 기존의 신뢰 가능한 캡처를 평가할 때는 순수 비교·판정만 호출하고, 평가/저장을 위해 새 요청을 보내지 않는다. |
| 구조화 저장 | 서버 어댑터가 Result와 해당 실행의 정책·신원·관찰 시각·증거 참조를 [판정 레코드 §2](./verdict-record.md)에 맞춰 별도 저장한다. `Result` 자체가 이 메타데이터를 모두 반환하는 것은 아니며, 실 어댑터가 서버 문맥·정책 reader·전송 영수증을 보존·대조해야 한다. |
| 재검증 결론 저장 | 기존 `finding_retests`의 `reproduced/fixed/inconclusive` 수명주기를 유지한다. 별도 판정 레코드와 동일 conversation·검사·증거를 연결하고, 저장 재시도는 기존 결과를 사용하며 네트워크 검사를 재실행하지 않는다. 연결 필드·물리 저장 형식·트랜잭션은 구현 시 확정한다. |

**실행 결과의 연결 계약(서버 관리 묶음, 구현 예정):** 현재 `Capture`·`Result`에 없는 메타데이터는 서버 어댑터가 아래 항목을 가진 실행별 묶음으로 보존해야 한다. 새 Go 타입명·물리 필드명은 미정이며, 묶음의 구성·검증 의무는 필수다.
- 서버가 발급한 실행 식별자와 작업·conversation·case 참조, reset 세대, 사용한 정책 버전.
- 등록된 owner/evaluated 계정 참조·검증 상태, 전체 `resource_ref`, 해당 실행에서 관찰한 시각·traffic 증거 참조, 그 실행이 반환한 `Result`.
- `Verify` 호출마다 전송 영수증·정책 조회 결과를 해당 실행 식별자에만 수집하고, 결과를 붙인 뒤 모델·에이전트가 수정할 수 없게 한다. 서버 관리 묶음을 재구성할 수 없는 과거 캡처는 확정 판정에 재사용하지 않는다.
- 저장 어댑터는 위 항목의 누락과 실행·계정·자원·정책 버전·reset 세대 불일치를 검사한다. 누락/불일치 시 해당 근거로 확정 저장을 거부하고, 검증불가 사유를 남기며 기존 `confirmed` 경로로 우회하지 않는다. `Result`만 받거나 모델이 준 실행 ID만으로 이 검사를 통과시키지 않는다.

### 6.2 전송 결과와 판정의 구분
- `ok`는 `Transmitter.Send`·`PolicyResolver`의 개별 결과다. `Verify` 전체의 성공 플래그로 취급하지 않는다. 전송 거부·응답 미확인 단계는 검증불가로 기록하며, 도구 승인·HTTP 200만으로 접근 성공이나 취약점을 확정하지 않는다.
- 판정은 `oracle.Evaluate`의 규칙을 따른다. 필요한 증거가 부족한 위반 검증은 `UNDECIDABLE`이며, 유효한 명시적 허용 정책으로 나온 `ALLOWED`는 정책상 허용 판단이다. `ALLOWED`도 실제 송신·조회 성공이나 취약점 없음의 증명이 아니다.
- 구조화 판정 4종과 기존 재검증 결론 3종의 전체 매핑은 미정이다. 매핑·증거가 확정되지 않은 결과는 `reproduced/fixed`로 저장하지 않고 `inconclusive`와 부족 사유를 남기는 방향으로 연결한다. 특히 `ALLOWED`·요청 실패·증거 부족을 자동으로 `fixed`로 바꾸지 않는다.

### 6.3 최초 발견 저장과 완료 조건
재검증 연결만으로 E③ 통합이 완료되는 것은 아니다. 통제 작업의 최초 finding도 **확정 저장 전** 동일한 증거 평가를 거쳐야 한다([엔진 개선 초안 §5.3](../engine-improvement-draft.md#53-발견-저장-전-강제)). 현재 `db.RecordFindingTx`에는 `confirmed` 직접 저장 경로가 남아 있다.

구현 시에는 `report_finding`의 recorder·fallback 및 직접 저장 경로에서 동일 작업·검사·증거 버전의 서버 발급 `CONFIRMED` 판정 참조를 확인해야 한다. 미발급·미지원·저장 실패 때 기존 확정 경로로 내려가지 않도록 하고, 구조화 판정과 finding 연결·중복 저장 방지는 저장 트랜잭션에서 검증한다. 재검증 저장과 최초 확정 저장은 각각 시험한다.

**이 PR의 완료 범위는 문서 정렬이다.** 실 `Transmitter`·`PolicyResolver`·증거/판정 저장 어댑터·최초 확정 게이트·통합 시험은 미구현이며, 여기서 엔진 연결 완료나 실제 전송 통제를 달성했다고 주장하지 않는다.
