# KG fixture 레지스트리 스켈레톤 — Juice Shop IDOR 1단계 (초안)

> 이경준 초안(§8 #2 레지스트리 **내용** 담당 = 이경준/윤재상). 형식 리뷰 = 최민준.
> 대상 파일: `kg-registry-stage1.skeleton.json`. 형식 계약 = `docs/contracts/kg-fixture-registry.md`(v1).
> **이건 구조 초안이다. 실제 값은 평가 준비 단계에서 채운다.** `basket`·A 소유 1자원·합성 placeholder로 구조·정책·가드만 고정.

## 무엇 / 왜
- IDOR 1단계(수평·읽기·한 회차) 정본 정책을 담는 수동 등록 레지스트리의 **구조 뼈대**.
- **1단계 = 자원 1개(A 소유)**. 그 자원에 **owner 자기조회(allow, 정상 대조) + cross 조회(deny, 수평 IDOR 기대 차단)** 정책 쌍 + canary 1개.
- owner GET(A→A) + cross GET(B→A) = 접근통제 지도 **한 칸**(B행 × A자원열). §1 "한 쌍"·최민준 "칸 하나짜리"와 일치.
- 대칭 2번째(B 소유, A 평가)는 지도 2×2를 채우는 **확장(선택)**이지 1단계 필수 아님. 필요하면 같은 형식으로 resource 1개 더 추가.

## 절대규칙 가드 (이 뼈대가 지키는 것)
- **절대규칙 7**: 챌린지 목록을 보지 않고 작성. 정책(`deny` = B가 A의 사적 자원 못 읽음)은 **앱의 정상 인가모델**에서 나온 것이지 "어떤 챌린지가 있다"에서 나온 게 아니다. 값 채울 때도 챌린지/풀이 상태를 입력으로 쓰지 않는다.
- **절대규칙 2**: 비밀 없음. 계정 비밀번호·세션·쿠키·토큰, 하네스 세대 토큰 **원문**은 없다. `label`은 라벨(계정 원문 ID 아님). canary `value`는 **합성 sentinel**.
- **하드코딩 금지**: `account-a`·`basket:a-owned-PLACEHOLDER` 등은 전부 **플레이스홀더**다. 실제 Juice Shop 계정·자원·엔드포인트를 뼈대에 박지 않았다.

## 플레이스홀더 → 평가 준비 때 채우는 값 (누가)
| 필드 | 지금(뼈대) | 채우는 값 / 주체 |
|---|---|---|
| `run_id` | `prep-run-PLACEHOLDER` | 준비 회차 ID — fixture 하네스(비평가=윤재상/운영자) 발급 |
| `reset_generation` | `reset-gen-PLACEHOLDER` | 대상 reset 세대 — 리셋·health 확인 때 발급 |
| `generation_ref`·`instance_ref` | `*-PLACEHOLDER` | 하네스 발급 세대 참조·대상 인스턴스 참조(원문 토큰 아님) |
| `resource_ref` | `basket:a-owned-PLACEHOLDER` | type=`basket`(후보, 아래), id=한 세대 안 인스턴스. 실제 전체 `type:id` |
| `canaries[].field` | `PLACEHOLDER-field` | E² `view` marker와 **같은** 직속 필드명 — prep 공동작성(아래 item 2) |
| `canaries[].value` | `kgfx-canary-PLACEHOLDER-1` | 실제로 **심는** 합성 sentinel(세대 안 전역 유일, 비자명). 레지스트리·심은 값·E² `view` marker 집합이 **정확히 같아야** 함(§9) |
| `valid_from`·`valid_until` | 11/05~11/06 **예시(지난 창 — 반드시 교체)** | 회차 유효창(세션/snapshot 만료 안, 첫 회차 한도 이내). 만료 상태면 fail-closed라 위험은 없지만 실제 회차 창으로 교체 |
| `snapshot_id` | `kgfx-juiceshop-s1` | 운영자가 붙이는 불투명 ID(≤49바이트). E² `snapshot_ref`와 host 설정이 짝지음 |

## 설계 선택
- `expected_access`: owner 자기조회=`allow`(정상 대조), cross=`deny`(수평 IDOR 기대 차단). 둘 다 정본 선언이지 관측 추론 아님.
- `sharing`·`delegation`=`resolved_none`: 공유·위임 **없음을 해소함**(§3). 모르면 `resolved_none`이 아니라 `unknown`. 실제 값 채울 때 공유/위임 상태를 확인 못 하면 `unknown`.
- `trivial: false`: canary가 **의도적으로 심은 비자명 sentinel**이라는 전제. 확신 없으면 `true`(§3.1). false면 반드시 비추측 값으로 심는다.

## 열린 항목 3개 — 정렬 결과
1. **`resource_ref` type.** type:id·마지막 콜론 규약 (a)마지막콜론 보존 (b)표시이름=UI전용 (c)`user`≠소유축 → **이경준 사후 승인(resource-ref-types-proposal §5-2 닫힘)**. 내 AccessMap은 셀 키를 **전체 ref**(`KgResource.id`)로, label은 표시용. **1단계 수평·읽기 type은 앱의 정상 인가모델 + canary 심기가능성으로만 고른다**(판정·GT는 선택 입력 아님). `basket`은 사용자 소유·수평 조회 자원이라 인가모델엔 맞고(이동찬 E² 등록 기준 동의) 스켈레톤 예시로 씀 — 심기가능성은 아래 canary carrier에서 미결이라 최종 type은 거기서 정해진다. **GT 상관 가능 여부는 레지스트리를 고정한 뒤 평가 쪽(최민준)이 별도 판단하고, 그 결과로 등록할 자원을 거르지 않는다**(판정 담당은 내용 선택에 참여하지 않음 — `:14`·§8 #2). 스켈레톤·레지스트리엔 GT 키·상관 데이터 없음.
2. **canary `field`**: 내가 고르는 값이 아니라 **규칙** — `idor.Marker.Field` = E² `JSONViewRule.DataPath` **직속 필드명**(view당 유일), 레지스트리↔E² view **정확히 같은 집합**, 같은 prep에서 공동작성(불일치=송신 전 `canary_mismatch`). 규칙은 기존 계약으로 닫힘. **스켈레톤은 합성 placeholder**(field·value), 실제 값은 prep.
3. **자원 개수**: 1단계 = **자원 1개(A 소유)** 확정(§1·"칸 하나짜리"). 대칭 확장은 선택.

## ⚠ prep 전 결정 (실제 등록 전까지) — canary carrier
**basket엔 심을 수 있는 비자명 표식 필드가 없다**(최민준 실측: id/UserId=정수, coupon=null·PUT 404, createdAt/updatedAt=안 심은 문자열, Products=배열). 표식이 없으면 setup-success=Unknown → **판정 UNDECIDABLE에서 멈춰 CONFIRMED 못 감** → **내 지도 "취약 확정" 칸이 안 뜬다.** 스켈레톤(합성 placeholder)은 안 막히지만, **실제 등록(prep) 전에** 아래 중 하나로 정해야 1단계가 CONFIRMED에 도달:
- **(이경준 추천) 심을 수 있는 자원 type으로 이관** — 사용자 작성 자유텍스트 필드(제안서 후보 `review`/`feedback` 계열)면 prep에서 본문에 무작위 sentinel을 심고(조회경로와 분리된 준비 쓰기, 절대규칙 6) B가 GET으로 읽어 CONFIRMED가 깨끗이 섬. §3.1·§3.2(3)(심은 값·전역 유일·비자명) 그대로 만족. → 후보 선정 기준은 **심기 가능한 필드 존재 + 앱 인가모델**(심기가능 필드 확인=내용 담당/운영자, 등록 GET·§9 결속 확인=이동찬). GT 상관은 고정 뒤 평가 쪽(최민준) 별도이고 **자원 선택 조건이 아니다**.
- (대안) `createdAt`을 표식으로 — 제일 가볍지만 **심은 값이 아니라 관측값**이라 전역 유일·비자명을 못 보장(같은 세대 A·B 충돌 가능·저엔트로피). §3.1 희석이라 폴백으로만, 쓰면 세대 내 유일성 검증 + "약한 근거" 명시.
- (대안) prep이 DB에 직접 표식 쓰기 — basket엔 넣을 문자열 컬럼이 없고 앱별 미검증 경로라 약함.

→ 이 carrier 결정이 **1단계 최종 type을 정한다**(basket이 심기가능 미충족이면 심을 수 있는 type으로). 스켈레톤 PR은 구조·정책·가드 고정이라 안 막힌다. 결정되면 레지스트리 `resource_ref` type·`canaries[].field`만 교체.

## 검증
- **머지된 실제 `kgfixture.Parse`(dev)로 로드 확인 — PARSE OK.** `owner A→A=allow`·`cross B→A=deny`·canary 1(`trivial=false`), policy version `kg-fixture/v1:kgfx-juiceshop-s1:<digest>` 결정적 산출. (최종 게이트 = 그 로더.)
- 값 채운 뒤: §9 `(*Snapshot).CheckPairing`으로 레지스트리 ↔ E² snapshot 라벨·resource_ref·reset 세대·canary 집합 대조(불일치=`pairing_*`) = 이동찬 서버 조립 때.

## 다음
- 커밋 위치: 평가용 레지스트리는 §2가 "결과 전 파일째 커밋" → **평가 디렉토리(최민준/윤재상 영역)와 맞물림**, §8 #2가 위치를 "열린 채". 뼈대·type 합의 후 커밋 지점 협의.
- 실제 값: 평가환경(윤재상 35B/스캔·운영자 reset·canary 심기) 준비와 동시.
