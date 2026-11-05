# KG 1단계 정본: fixture 레지스트리와 최소 resolver (계약 초안)

상태: **1단계 계약 · 로더·resolver 구현됨([`kgfixture`](../../kgfixture/registry.go)) · 서버 연결 전.** 2026-11-05 결정(kg-policy §6 Open #1의 이경준 제안 채택)에 따른 IDOR 1단계용 최소 KG 경계다.
분담: 최소 resolver·레지스트리 형식 = 최민준, KG 계약 정합 리뷰 = 이경준, 서버 조립(`ControlledReadFindingResolver`) = 이동찬.
이 문서는 [kg-policy](./kg-policy.md)·[canary fixture](./idor-canary-fixture.md)·[첫 IDOR 통합](./first-idor-integration.md)의 의미를 바꾸지 않고, 1단계에서 그 계약을 만족하는 **가장 작은 정본 reader**를 정한다.

## 1. 범위

- **대상**: IDOR 1단계 — 수평·읽기·한 회차·등록 자원 소수. owner/cross 조회 한 쌍을 같은 동결 snapshot으로 판정한다.
- **정본**: 운영자가 fixture 준비 때 작성한 **수동 등록 레지스트리** 한 파일. 관측 추론은 정본이 아니다(그 결과는 `ExpectedAccess=unknown` 상한, kg-policy §3).
- **범위 밖**: 수직(역할) 정책, 여러 회차·세대를 합친 조회, 지도 조회 API(§4.2 전체 규칙), KG 저장소 테이블, canary 등록 API(§3.2 물리 형식). 이것들은 1단계 뒤 정한다.
- 이 resolver는 대상에 요청을 보내지 않고, DB·네트워크를 읽지 않으며, 세션·계정 비밀을 다루지 않는다. A/B 세션은 E② private snapshot으로 서버가 받는다(§9).

## 2. 레지스트리 파일

- 운영자가 회차 준비 단계(심기+등록, kg-policy §3.2(1))에 작성한다. 서버가 읽는 사본은 저장소 밖 보호 경로, 소유자 실행 계정·`0600`이다.
  - **실제 진단 대상(비평가)** 레지스트리는 커밋하지 않는다(대상 조직의 계정·자원 구조를 담는 준비 자료). 결과 전 고정이 필요하면 policy version(내용 digest 포함)만 커밋한다.
  - **평가용**(공개 연습 대상) 레지스트리는 비밀이 없으므로 결과를 보기 전에 파일째 커밋해 고정한다(§8 #2). 회차 기록의 policy version이 커밋한 파일의 digest와 같아야 그 회차를 쓴다.
- 담는 것: 참조·라벨·정책 선언·비민감 canary 값. **담지 않는 것**: 계정 비밀번호·세션·쿠키·토큰, 하네스 세대 토큰 원문(절대규칙 2).
- 형식(키 이름은 아래와 정확히 같다. 최상위·`principals[]`·`resources[]`·`policies[]`·`canaries[]` 객체마다 아래 키가 전부 있어야 하고 다른 키는 없어야 한다):

| 필드 | 뜻 | 규칙 |
|---|---|---|
| `format` | 형식 버전 | 정확히 `abc2lab.kg-fixture-registry/v1` |
| `snapshot_id` | 운영자가 붙인 불투명 ID. 1단계에서는 kg-policy §3.2의 `bundle_ref`를 대신한다(작업당 단일 묶음) | 공통 ref 문법: 첫 글자 영숫자, 나머지 영숫자와 `_.:/-`, 1~49바이트, `://` 금지(§3 policy version이 128바이트 안에 들어가도록) |
| `run_id` · `reset_generation` | 별도 축(§3.2(2)). `run_id`는 **준비 회차 ID**로, fixture 하네스(비평가 통합은 윤재상 하네스·운영자, 평가는 러너)가 준비 시작 때 발급한다. E② `Scope.RunRef`(실행 소비 때 서버가 새로 발급)나 `EvalRunID`(첫 회차는 비어 있음)가 아니다 | 둘 다 필수, 공통 ref 문법. 서로 추론하지 않음 |
| `generation_ref` · `instance_ref` | 하네스가 발급한 세대 **참조**와 대상 인스턴스 참조 | 원문 토큰 아님. resolver는 발급·재결속하지 않음 |
| `principals[]` | 계정 라벨 목록 | `idor.AccountRef.Label`과 1:1. 중복 금지 |
| `resources[]` | `resource_ref`(전체 `type:id`, 마지막 콜론 split), `owner_ref`(principal 라벨) | `resource_ref` 중복 금지 |
| `resources[].policies[]` | 평가 주체별 정책: `evaluated_ref`, `expected_access`(allow·deny·unknown), `sharing`·`delegation`(oracle `ResolutionState` 값), `valid_from`·`valid_until` | 모든 필드 명시 필수(누락을 기본값으로 메우지 않음). `evaluated_ref` 중복 금지 |
| `resources[].canaries[]` | `field`, `value`, `trivial` | `trivial`은 명시 필수(확신 없으면 true, §3.1). `field`는 한 자원 안에서 유일(등록 키 = 자원+필드). `value`는 한 세대 안에서 전역 유일(§3.2(3)) |

- 모르는 키·중복 키·빈 문자열·형식이 틀린 ref는 전부 **로드 실패**(fail-closed). 부분만 받아 쓰지 않는다.
- 구현이 고정한 디코딩 규칙(Go·Python 정규 바이트가 갈리지 않게):
  - 키와 문자열 값은 **출력 가능한 ASCII(0x20~0x7E)만** 쓴다. 제어 문자·DEL·비ASCII(`\u00e9` 같은 이스케이프로 쓴 것 포함)는 거부한다.
  - 중복 키는 **이스케이프를 푼 키**로 비교한다(`"\u0066ormat"`과 `"format"`은 같은 키). encoding/json은 마지막 값을 조용히 받으므로 토큰 단위로 검사한다.
  - 숫자는 정수 표기만 디코딩 단계에서 받고(소수·지수 거부), v1 스키마에는 숫자 필드가 없으므로 결국 거부된다. `null`도 거부된다.
  - `principals`·`resources`는 1개 이상. `policies`·`canaries`는 빈 배열을 허용하며, 그 자원의 조회는 §5에서 `ok=false`가 된다.
  - `valid_from`·`valid_until`은 `Z`로 끝나는 RFC 3339 UTC(`2026-11-05T00:00:00Z`, 소수 초 9자리까지)이고 `valid_from < valid_until`.
  - 입력 상한은 1 MiB(`kgfixture.MaxRegistryBytes`). 비었거나 넘으면 `registry_unreadable`.
- 예(값은 설명용):

```json
{"format": "abc2lab.kg-fixture-registry/v1", "snapshot_id": "kgfx-run-1", "run_id": "prep-run-1",
 "reset_generation": "reset-1", "generation_ref": "gen-1", "instance_ref": "target-1",
 "principals": [{"label": "account-a"}, {"label": "account-b"}],
 "resources": [{"resource_ref": "note:1001", "owner_ref": "account-a",
   "policies": [{"evaluated_ref": "account-b", "expected_access": "deny", "sharing": "resolved_none",
                 "delegation": "resolved_none", "valid_from": "2026-11-05T00:00:00Z", "valid_until": "2026-11-06T00:00:00Z"}],
   "canaries": [{"field": "body", "value": "kgfx-canary-7f3a", "trivial": false}]}]}
```

## 3. snapshot 동결과 식별

- 서버는 E② snapshot을 **등록할 때** 레지스트리를 한 번 읽어 불변 snapshot handle을 만들고, 그 등록을 소비하는 실행이 같은 handle을 쓴다. 이후 파일이 바뀌어도 그 등록·실행은 바뀐 내용을 보지 않는다(핫 리로드 없음).
- 식별 세 값(`e3record.Source`·`evidencebind.Context`에 그대로 들어감):

| 값 | 만드는 법 | 들어가는 곳 |
|---|---|---|
| snapshot ref | 레지스트리의 `snapshot_id` | `Source.KGSnapshotRef` |
| snapshot digest | 레지스트리 내용의 정규 JSON(키 바이트 순 정렬·공백 없음·`"`와 `\`만 이스케이프) SHA-256. 문자열이 출력 가능한 ASCII뿐이라 Python `json.dumps(sort_keys=True, separators=(",", ":"), ensure_ascii=True)`와 바이트가 같다([공용 벡터](../../kgfixture/testdata/vectors-v1.json), `scripts/eval/kg_fixture_canonical.py`) | `Source.KGSnapshotDigest` |
| policy version | `kg-fixture/v1:<snapshot_id>:<digest 64 hex>` (`snapshot_id`가 49바이트 이하라 전체 128바이트 이내, `evidencebind`·`e3record` 공통 ref 문법 안) | `Context.KGPolicyVersion` |

- **policy version의 뜻**: 발급 주체는 이 resolver(내용에서 결정적으로 계산)이고, 같은 내용이면 같은 값, 내용이 한 바이트라도 다르면 다른 값이다. 구분자는 기존 ref 검사기(`evidencebind` 참조 문법)가 받는 `:`만 쓴다(`@`는 `MissingContext`로 거부됨, #193 리뷰). 지도 헤더 표기는 이경준 확인(kg-policy §6 Open #1 (a)).
- snapshot은 `run_id`·`reset_generation`·`generation_ref`·`instance_ref`를 함께 싣는다. 서버는 이 값을 신뢰 host·Source 경계가 공급한 준비 회차·하네스 세대·대상 인스턴스와 대조하고 **모두 같을 때만** 조회·확정 저장으로 넘어간다. 하나라도 없거나 다르면 실행하지 않는다(이동찬, 서버 조립). 실행 때 발급되는 `Scope.RunRef`는 이 snapshot handle에 결속해 기록하며, 레지스트리 `run_id`와 같다고 보지 않는다.

## 4. 수명과 무효화

- owner·cross 두 조회는 **같은 snapshot 객체**를 쓴다. 두 조회 사이에 snapshot을 다시 읽지 않는다.
- snapshot은 그 E② 등록 하나와 그 등록을 소비하는 실행에만 유효하다. 다른 등록·작업·회차에 재사용하지 않는다.
- reset 세대가 바뀌면(대상 리셋) 이전 snapshot은 무효다. 새 세대의 레지스트리를 새로 작성·로드한다. 무효 snapshot으로 들어온 조회는 아래 §5에서 `ok=false`(→ UNDECIDABLE)이다(kg-policy §6 Open #1 (b)의 1단계 답).
- 과거 판정은 이력으로 남고, 지도에서는 새 범위의 현재 칸이 재검증 전까지 판단불가다(11/05 Open #6 1단계 규칙).

## 5. `idor.PolicyResolver` 동작

| 호출 | 반환 | 실패(`ok=false` → 검증기가 UNDECIDABLE) |
|---|---|---|
| `ResolvePolicy(resourceRef, owner, evaluated)` | 레지스트리 정책을 `oracle.PolicyAssertion`으로: `ExpectedAccess`·`Sharing`·`Delegation`·`ValidFrom`·`ValidUntil` 그대로, `HasRegisteredSource=true`, `HintMatchesCanonical=true`(이 레지스트리가 정본이므로) | 전체 `resource_ref` 정확 일치 없음 · `owner.Label`이 등록 `owner_ref`와 다름 · `evaluated.Label`의 정책 없음 · snapshot 무효 |
| `Canaries(resourceRef, owner)` | 등록 canary를 `idor.Marker{Field, Value, Trivial}`로(재분류 없음, §3.1) | 자원·소유자 불일치 · canary 0개 · snapshot 무효 |

- 정책이 없을 때 allow·deny를 추측하지 않는다. 공유·위임을 해소하지 못했으면 `unknown`이다(`resolved_none`으로 메우지 않음, kg-policy §3).
- 1단계에서는 레지스트리가 정본 자체라 `HintMatchesCanonical=true`이고, hint↔정본 교차검사는 하는 일이 없다. 레지스트리 오류에 대한 방어는 로더의 fail-closed 검사(§2)와 변이 검사(§7)뿐이라는 점을 알고 쓴다.
- `AccountRef.Status`(신원 확인)는 채우지 않는다. 신원은 E②·서버 몫이다(kg-policy §3).
- 부분 id·대소문자 보정·접두 일치로 자원을 찾지 않는다.

## 6. 비밀·노출

- resolver는 canary 값을 로그·오류·근거 파일·LLM 입력에 내보내지 않는다. 외부로 나가는 역참조 산출물은 `owner_ref` 라벨만이다(§3.2(6), 절대규칙 2).
- 오류는 고정 코드로만 낸다(오류 문자열 = `kgfixture: <코드>`). 원인 문자열에 파일 내용을 넣지 않는다.

| 코드 | 조건 |
|---|---|
| `registry_unreadable` | 입력이 비었거나 1 MiB 초과 |
| `registry_format` | JSON 문법·뒤따르는 데이터·중첩 한도, 비ASCII·제어 문자, 정수 아닌 숫자, 타입·빈 문자열·`null`, 필수 키 누락, `format` 값, 빈 `principals`·`resources`, 등록 안 된 `owner_ref`·`evaluated_ref`, 열거값(`expected_access`·`sharing`·`delegation`), 시각 형식·`valid_from ≥ valid_until`, `trivial`이 bool 아님 |
| `registry_unknown_field` | 정해진 키 밖의 키 |
| `registry_duplicate` | 중복 키(이스케이프 별칭 포함), 같은 라벨·`resource_ref`·`evaluated_ref`, 한 자원 안의 같은 canary `field`(등록 키는 자원+필드이고, E② view가 같은 필드를 거부함) |
| `ref_format` | 공통 ref 문법 위반, `snapshot_id` 49바이트 초과, `resource_ref`가 `type:id`가 아님 |
| `canary_value_not_unique` | canary `value`가 레지스트리 안에서 겹침 |
| `canary_trivial_missing` | canary에 `trivial` 없음 |
| `snapshot_invalid` | `Invalidate` 뒤의 `Err()` |

## 7. 시험 (구현됨)

- 로더: 위 §2 규칙마다 거부 사례 1개 이상(모르는 키·중복 키·빈 값·ref 형식·`trivial` 누락·값 중복), 정상 사례의 digest·policy version이 내용에서 결정적인지, 한 바이트 변경이 digest를 바꾸는지.
- resolver: §5 표의 성공 1 + 실패 조건 각각 단독. 무효화 뒤 두 호출 모두 `ok=false`. 같은 snapshot을 owner·cross가 공유하는지.
- 노출: canary 값·sentinel 문자열이 오류·로그에 나오지 않는지.
- 변이 검사로 각 가드가 단독으로 잡히는지 확인한다.
- 구현: `kgfixture/registry_test.go`·`vectors_test.go`, 공용 벡터 `kgfixture/testdata/vectors-v1.json`(따옴표·역슬래시·이스케이프 별칭은 받고, 비ASCII·제어 문자·DEL·lone surrogate·중복 키·소수는 Go·Python 둘 다 거부), Python 대조 `scripts/eval/tests/test_kg_fixture_vectors.py`.

## 8. 열린 것

1. ~~policy version 포맷 확정~~ — §3 형식으로 구현했다. 지도 헤더 표기는 이경준 확인(#197, kg-policy §4.1).
2. 레지스트리를 누가 작성하는지. **(제안)** 레지스트리 **내용**(대상 앱의 자원·소유·정책·canary)은 이경준 또는 윤재상이 **챌린지 목록을 보지 않고** 작성하고(절대규칙 7), 결과를 보기 전에 고정한다. 최민준은 형식 리뷰만 한다(검증기·판정 담당이 정책을 쓰지 않게). 비평가 통합 회차의 준비 절차는 fixture 하네스(윤재상, [실제 입력 대장](./first-idor-host-inputs.md))와 운영자. 작성 도구를 하나로 둘지는 열린 채로 둔다.
   - 고정 방식(2026-11-05 결정): 평가용은 파일째 결과 전 커밋, 실제 진단 대상용은 저장소 밖에 두고 policy version만 커밋(§2).
3. ~~실제 입력 대장의 "동결한 KG 정책 … 실제 reader" 행을 1단계분은 이 resolver(최민준)로 바꾸는 수정~~ — #197로 반영했다.
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
| `resources[].canaries[]` (`field`·`value`·`trivial`) | `resources[].view`의 `Markers[]` | **정확히 같은 집합**이어야 한다. 다르면 E②가 송신 전에 `canary_mismatch`로 거부한다(owner·cross 요청 0건, `transmit/verifier.go`) |
| `snapshot_id` | (없음) | 신뢰 host 설정이 E② `snapshot_ref`와 이 레지스트리를 명시적으로 짝짓는다(이동찬). E② JSON에 새 필드를 넣지 않는다 |
| (없음) | `approval_ref`·`url`·`read_only`·한도·키·health | E② 몫이라 레지스트리에 넣지 않는다 |

1단계 준비 순서(운영자, 비평가 통합 회차):

1. 대상을 reset하고 health를 확인한다. 하네스가 준비 회차 ID(`run_id`)와 세대 참조(`generation_ref`)를 발급한다.
2. A·B 테스트 계정으로 대상에 로그인해 세션을 확인한다. 세션 헤더 값은 snapshot의 `0600` 헤더 파일에만 저장하고 JSON·로그·LLM 입력·근거 파일에 넣지 않는다. `status="verified"`는 이 운영자 확인값이다.
3. A 소유 비민감 자원에 canary를 심는다(승인된 fixture, [canary fixture](./idor-canary-fixture.md)). 심기와 레지스트리 등록은 같은 단계다(kg-policy §3.2(1)).
4. snapshot JSON(자원·계정·승인·예산, 만료는 첫 회차 한도 이내)과 레지스트리를 같은 세대로 작성한다. 심은 canary의 같은 불변 묶음을 레지스트리 `canaries[]`와 snapshot `view`의 marker·응답 projection에 **똑같이** 넣는다. 세션이 snapshot 만료보다 먼저 만료되면 그 시각을 적용한다.
5. 운영자가 snapshot을 등록하면(`POST /api/tasks/{id}/intents/{iid}/controlled-read`, `{"snapshot_ref": …}`) 서버가 host 설정으로 짝지은 레지스트리를 그때 동결하고 위 표로 대조한 뒤, 그 등록을 소비하는 실행에서 같은 handle을 쓴다.

첫 회차는 EvalRunID 없이 돈다(비평가). 평가 회차에서 러너가 이 준비를 자동으로 하는 것은 후속이다.
