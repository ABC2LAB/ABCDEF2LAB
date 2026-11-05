# KG 1단계 정본: fixture 레지스트리와 최소 resolver (계약 초안)

상태: **제안 · 팀 리뷰용 · 구현 전.** 2026-11-05 결정(kg-policy §6 Open #1의 이경준 제안 채택)에 따른 IDOR 1단계용 최소 KG 경계다.
분담: 최소 resolver·레지스트리 형식 = 최민준, KG 계약 정합 리뷰 = 이경준, 서버 조립(`ControlledReadFindingResolver`) = 이동찬.
이 문서는 [kg-policy](./kg-policy.md)·[canary fixture](./idor-canary-fixture.md)·[첫 IDOR 통합](./first-idor-integration.md)의 의미를 바꾸지 않고, 1단계에서 그 계약을 만족하는 **가장 작은 정본 reader**를 정한다.

## 1. 범위

- **대상**: IDOR 1단계 — 수평·읽기·한 회차·등록 자원 소수. owner/cross 조회 한 쌍을 같은 동결 snapshot으로 판정한다.
- **정본**: 운영자가 fixture 준비 때 작성한 **수동 등록 레지스트리** 한 파일. 관측 추론은 정본이 아니다(그 결과는 `ExpectedAccess=unknown` 상한, kg-policy §3).
- **범위 밖**: 수직(역할) 정책, 여러 회차·세대를 합친 조회, 지도 조회 API(§4.2 전체 규칙), KG 저장소 테이블, canary 등록 API(§3.2 물리 형식). 이것들은 1단계 뒤 정한다.
- 이 resolver는 대상에 요청을 보내지 않고, DB·네트워크를 읽지 않으며, 세션·계정 비밀을 다루지 않는다. A/B 세션은 E② private snapshot으로 서버가 받는다(§9).

## 2. 레지스트리 파일

- 운영자가 회차 준비 단계(심기+등록, kg-policy §3.2(1))에 작성한다. 저장소 밖 보호 경로, 소유자 실행 계정·`0600`, **커밋 금지**(결과물·준비 자료).
- 담는 것: 참조·라벨·정책 선언·비민감 canary 값. **담지 않는 것**: 계정 비밀번호·세션·쿠키·토큰, 하네스 세대 토큰 원문(절대규칙 2).
- 형식(논리 필드, 정확한 키 이름은 구현 PR에서 고정):

| 필드 | 뜻 | 규칙 |
|---|---|---|
| `format` | 형식 버전 | 정확히 `abc2lab.kg-fixture-registry/v1` |
| `snapshot_id` | 운영자가 붙인 불투명 ID | 비어 있지 않음, 제어문자 없음 |
| `run_id` · `reset_generation` | 별도 축(§3.2(2)) | 둘 다 필수. 서로 추론하지 않음 |
| `generation_ref` · `instance_ref` | 하네스가 발급한 세대 **참조**와 대상 인스턴스 참조 | 원문 토큰 아님. resolver는 발급·재결속하지 않음 |
| `principals[]` | 계정 라벨 목록 | `idor.AccountRef.Label`과 1:1. 중복 금지 |
| `resources[]` | `resource_ref`(전체 `type:id`, 마지막 콜론 split), `owner_ref`(principal 라벨) | `resource_ref` 중복 금지 |
| `resources[].policies[]` | 평가 주체별 정책: `evaluated_ref`, `expected_access`(allow·deny·unknown), `sharing`·`delegation`(oracle `ResolutionState` 값), `valid_from`·`valid_until` | 모든 필드 명시 필수(누락을 기본값으로 메우지 않음). `evaluated_ref` 중복 금지 |
| `resources[].canaries[]` | `field`, `value`, `trivial` | `trivial`은 명시 필수(확신 없으면 true, §3.1). `value`는 한 세대 안에서 전역 유일(§3.2(3)) |

- 모르는 키·중복 키·빈 문자열·형식이 틀린 ref는 전부 **로드 실패**(fail-closed). 부분만 받아 쓰지 않는다.

## 3. snapshot 동결과 식별

- 서버는 E② 작업(run) 하나를 시작할 때 레지스트리를 **한 번** 읽어 불변 snapshot을 만든다. 이후 파일이 바뀌어도 그 작업은 바뀐 내용을 보지 않는다(핫 리로드 없음).
- 식별 세 값(`e3record.Source`·`evidencebind.Context`에 그대로 들어감):

| 값 | 만드는 법 | 들어가는 곳 |
|---|---|---|
| snapshot ref | 레지스트리의 `snapshot_id` | `Source.KGSnapshotRef` |
| snapshot digest | 레지스트리 내용의 정규 JSON(키 정렬·공백 없음·UTF-8) SHA-256 | `Source.KGSnapshotDigest` |
| policy version | `kg-fixture/v1:<snapshot_id>@<digest 64 hex>` (제안) | `Context.KGPolicyVersion` |

- **policy version 제안의 뜻**: 발급 주체는 이 resolver(내용에서 결정적으로 계산)이고, 같은 내용이면 같은 값, 내용이 한 바이트라도 다르면 다른 값이다. 포맷 확정은 이동찬(서버 결속·감사 표기)과 이경준(지도 헤더) 확인이 필요하다(kg-policy §6 Open #1 (a)).
- snapshot은 `run_id`·`reset_generation`·`generation_ref`·`instance_ref`를 함께 싣는다. 서버는 이 값이 실행 문맥(E② 작업·하네스 세대·대상 인스턴스)과 **모두 같을 때만** 조회·확정 저장으로 넘어간다. 하나라도 없거나 다르면 실행하지 않는다(이동찬, 서버 조립).

## 4. 수명과 무효화

- owner·cross 두 조회는 **같은 snapshot 객체**를 쓴다. 두 조회 사이에 snapshot을 다시 읽지 않는다.
- snapshot은 그 E② 작업 하나에만 유효하다. 다른 작업·회차에 재사용하지 않는다.
- reset 세대가 바뀌면(대상 리셋) 이전 snapshot은 무효다. 새 세대의 레지스트리를 새로 작성·로드한다. 무효 snapshot으로 들어온 조회는 아래 §5에서 `ok=false`(→ UNDECIDABLE)이다(kg-policy §6 Open #1 (b)의 1단계 답).
- 과거 판정은 이력으로 남고, 지도에서는 새 범위의 현재 칸이 재검증 전까지 판단불가다(11/05 Open #6 1단계 규칙).

## 5. `idor.PolicyResolver` 동작

| 호출 | 반환 | 실패(`ok=false` → 검증기가 UNDECIDABLE) |
|---|---|---|
| `ResolvePolicy(resourceRef, owner, evaluated)` | 레지스트리 정책을 `oracle.PolicyAssertion`으로: `ExpectedAccess`·`Sharing`·`Delegation`·`ValidFrom`·`ValidUntil` 그대로, `HasRegisteredSource=true`, `HintMatchesCanonical=true`(이 레지스트리가 정본이므로) | 전체 `resource_ref` 정확 일치 없음 · `owner.Label`이 등록 `owner_ref`와 다름 · `evaluated.Label`의 정책 없음 · snapshot 무효 |
| `Canaries(resourceRef, owner)` | 등록 canary를 `idor.Marker{Field, Value, Trivial}`로(재분류 없음, §3.1) | 자원·소유자 불일치 · canary 0개 · snapshot 무효 |

- 정책이 없을 때 allow·deny를 추측하지 않는다. 공유·위임을 해소하지 못했으면 `unknown`이다(`resolved_none`으로 메우지 않음, kg-policy §3).
- `AccountRef.Status`(신원 확인)는 채우지 않는다. 신원은 E②·서버 몫이다(kg-policy §3).
- 부분 id·대소문자 보정·접두 일치로 자원을 찾지 않는다.

## 6. 비밀·노출

- resolver는 canary 값을 로그·오류·근거 파일·LLM 입력에 내보내지 않는다. 외부로 나가는 역참조 산출물은 `owner_ref` 라벨만이다(§3.2(6), 절대규칙 2).
- 오류는 고정 코드로만 낸다: `registry_unreadable` · `registry_format` · `registry_unknown_field` · `registry_duplicate` · `ref_format` · `canary_value_not_unique` · `canary_trivial_missing` · `snapshot_invalid`. 원인 문자열에 파일 내용을 넣지 않는다.

## 7. 시험 계획 (구현 PR에서)

- 로더: 위 §2 규칙마다 거부 사례 1개 이상(모르는 키·중복 키·빈 값·ref 형식·`trivial` 누락·값 중복), 정상 사례의 digest·policy version이 내용에서 결정적인지, 한 바이트 변경이 digest를 바꾸는지.
- resolver: §5 표의 성공 1 + 실패 조건 각각 단독. 무효화 뒤 두 호출 모두 `ok=false`. 같은 snapshot을 owner·cross가 공유하는지.
- 노출: canary 값·sentinel 문자열이 오류·로그에 나오지 않는지.
- 변이 검사로 각 가드가 단독으로 잡히는지 확인한다.

## 8. 열린 것

1. policy version 포맷 확정(이동찬·이경준) — §3 제안.
2. 레지스트리를 누가 작성하는지: 평가 회차는 러너 fixture(최민준), 비평가 통합 회차는 fixture 하네스(윤재상, [실제 입력 대장](./first-idor-host-inputs.md))와 운영자. 작성 도구를 하나로 둘지.
3. 실제 입력 대장의 "동결한 KG 정책 … 실제 reader" 행 담당(이경준)을 1단계분은 이 resolver(최민준)로 바꾸는 대장 수정 — 대장 담당자 리뷰로.
4. `ReadApprovalRef`(자원별 실행 승인)는 E² 등록 몫이라 이 레지스트리에 넣지 않는다. 서버가 같은 `resource_ref`로 대조한다.

## 9. E② private snapshot과의 대응·1단계 준비 순서

A/B 세션의 비밀 전달 형식은 새로 만들지 않는다. [E② 서버 런타임](./e2-server-runtime.md)의 private snapshot(전용 `0700` 디렉터리의 `<snapshot_ref>.json` + `0600` 헤더 파일, 세션 원문은 JSON에 넣지 않음)을 그대로 쓴다.
레지스트리와 snapshot은 **같은 준비 단계에서 함께** 만들고, 아래 값이 서로 맞아야 한다. 서버는 snapshot 등록 때 레지스트리를 로드·동결해 대조하고, 하나라도 다르면 실행하지 않는다(이동찬, 서버 조립).

| 레지스트리 | E② snapshot | 비고 |
|---|---|---|
| `principals[].label` | `sessions[].label` | A/B 라벨. handle·헤더 비밀은 snapshot에만 있다 |
| `resources[].resource_ref` | `resources[].ref` | 전체 `type:id` 정확 일치 |
| `resources[].owner_ref` | (없음) | 소유 관계는 KG만 안다. snapshot `resources[].accounts`는 조회를 허용한 계정 목록일 뿐 소유를 뜻하지 않는다 |
| `reset_generation` | `reset_ref` | 같은 reset 세대 |
| (없음) | `approval_ref`·`url`·`read_only`·한도·키·health | E② 몫이라 레지스트리에 넣지 않는다 |

1단계 준비 순서(운영자, 비평가 통합 회차):

1. 대상을 reset하고 health를 확인한다. 하네스가 세대 참조를 발급한다(레지스트리 `generation_ref`).
2. A·B 테스트 계정으로 대상에 로그인해 세션을 확인한다. 세션 헤더 값은 snapshot의 `0600` 헤더 파일에만 저장하고 JSON·로그·LLM 입력·근거 파일에 넣지 않는다. `status="verified"`는 이 운영자 확인값이다.
3. A 소유 비민감 자원에 canary를 심는다(승인된 fixture, [canary fixture](./idor-canary-fixture.md)). 심기와 레지스트리 등록은 같은 단계다(kg-policy §3.2(1)).
4. snapshot JSON(자원·계정·승인·예산, 만료는 첫 회차 한도 이내)과 레지스트리를 같은 세대로 작성한다. 세션이 snapshot 만료보다 먼저 만료되면 그 시각을 적용한다.
5. 운영자가 snapshot을 등록하면(`POST /api/tasks/{id}/intents/{iid}/controlled-read`, `{"snapshot_ref": …}`) 서버가 레지스트리를 동결하고 위 표로 대조한 뒤에만 실행한다.

첫 회차는 EvalRunID 없이 돈다(비평가). 평가 회차에서 러너가 이 준비를 자동으로 하는 것은 후속이다.
