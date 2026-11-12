# KG fixture 레지스트리 스켈레톤 — Juice Shop IDOR 1단계 (초안)

> 이경준 초안(§8 #2 레지스트리 **내용** 담당 = 이경준/윤재상). 형식 리뷰 = 최민준.
> 대상 파일: `kg-registry-stage1.skeleton.json`. 형식 계약 = `docs/contracts/kg-fixture-registry.md`(v1).
> **이건 구조 초안이다. 실제 값은 평가 준비 단계에서 채운다.** `address`·A 소유 1자원·합성 placeholder로 구조·정책·가드만 고정.
> **(11/9) carrier type 확정: `address`(canary `field`=`fullName`)** — 윤재상 C1~C6 실측(조건·결과 요약은 아래 §carrier)으로 basket(C1 탈락)·review/feedback(C4 부적격) 제치고 전부 통과. 아래 §carrier 참조. (`fullName`은 `Markers[].Field`이지 `DataPath`가 아님 — 구분은 item 2·§carrier.)

## 무엇 / 왜
- IDOR 1단계(수평·읽기·한 회차) 정본 정책을 담는 수동 등록 레지스트리의 **구조 뼈대**.
- **1단계 = 자원 1개(A 소유)**. 그 자원에 **owner 자기조회(allow, 정상 대조) + cross 조회(deny, 수평 IDOR 기대 차단)** 정책 쌍 + canary 1개.
- owner GET(A→A) + cross GET(B→A) = 접근통제 지도 **한 칸**(B행 × A자원열). §1 "한 쌍"·최민준 "칸 하나짜리"와 일치.
- 대칭 2번째(B 소유, A 평가)는 지도 2×2를 채우는 **확장(선택)**이지 1단계 필수 아님. 필요하면 같은 형식으로 resource 1개 더 추가.

## 절대규칙 가드 (이 뼈대가 지키는 것)
- **절대규칙 7**: 챌린지 목록을 보지 않고 작성. 정책(`deny` = B가 A의 사적 자원 못 읽음)은 **앱의 정상 인가모델**에서 나온 것이지 "어떤 챌린지가 있다"에서 나온 게 아니다. 값 채울 때도 챌린지/풀이 상태를 입력으로 쓰지 않는다.
- **절대규칙 2**: 비밀 없음. 계정 비밀번호·세션·쿠키·토큰, 하네스 세대 토큰 **원문**은 없다. `label`은 라벨(계정 원문 ID 아님). canary `value`는 **합성 sentinel**.
- **하드코딩 금지**: `account-a`·`address:a-owned-PLACEHOLDER` 등은 전부 **플레이스홀더**다. 실제 Juice Shop 계정·자원·엔드포인트를 뼈대에 박지 않았다. type(`address`)·field(`fullName`)는 앱 인가모델·심기가능 측정으로 고른 것이지 챌린지/GT로 고른 게 아니다(절대규칙 7).

## 플레이스홀더 → 평가 준비 때 채우는 값 (누가)
| 필드 | 지금(뼈대) | 채우는 값 / 주체 |
|---|---|---|
| `run_id` | `prep-run-PLACEHOLDER` | 준비 회차 ID — fixture 하네스(비평가=윤재상/운영자) 발급 |
| `reset_generation` | `reset-gen-PLACEHOLDER` | 대상 reset 세대 — 리셋·health 확인 때 발급 |
| `generation_ref`·`instance_ref` | `*-PLACEHOLDER` | 하네스 발급 세대 참조·대상 인스턴스 참조(원문 토큰 아님) |
| `resource_ref` | `address:a-owned-PLACEHOLDER` | type=`address`(확정, 아래 §carrier), id=한 세대 안 인스턴스. 실제 전체 `type:id` |
| `canaries[].field` | `fullName` | E² `view` `Markers[].Field`와 **같은** 값(=`DataPath`가 가리키는 객체 **안의** 직속 string 필드명). **DataPath 자체가 아님** — DataPath는 그 필드를 담은 부모 객체 경로(prep). prep 공동작성(아래 item 2) |
| `canaries[].value` | `kgfx-canary-PLACEHOLDER-1` | 실제로 **심는** 합성 sentinel(세대 안 전역 유일, 비자명). 레지스트리·심은 값·E² `view` marker 집합이 **정확히 같아야** 함(§9) |
| `valid_from`·`valid_until` | 11/05~11/06 **예시(지난 창 — 반드시 교체)** | 회차 유효창(세션/snapshot 만료 안, 첫 회차 한도 이내). 만료 상태면 fail-closed라 위험은 없지만 실제 회차 창으로 교체 |
| `snapshot_id` | `kgfx-juiceshop-s1` | 운영자가 붙이는 불투명 ID(≤49바이트). E² `snapshot_ref`와 host 설정이 짝지음 |

## 설계 선택
- `expected_access`: owner 자기조회=`allow`(정상 대조), cross=`deny`(수평 IDOR 기대 차단). 둘 다 정본 선언이지 관측 추론 아님.
- `sharing`·`delegation`=`resolved_none`: 공유·위임 **없음을 해소함**(§3). 모르면 `resolved_none`이 아니라 `unknown`. 실제 값 채울 때 공유/위임 상태를 확인 못 하면 `unknown`.
- `trivial: false`: canary가 **의도적으로 심은 비자명 sentinel**이라는 전제. 확신 없으면 `true`(§3.1). false면 반드시 비추측 값으로 심는다.

## 열린 항목 3개 — 정렬 결과
1. **`resource_ref` type.** type:id·마지막 콜론 규약 (a)마지막콜론 보존 (b)표시이름=UI전용 (c)`user`≠소유축 → **이경준 사후 승인(resource-ref-types-proposal §5-2 닫힘)**. 내 AccessMap은 셀 키를 **전체 ref**(`KgResource.id`)로, label은 표시용. **1단계 수평·읽기 type은 앱의 정상 인가모델 + canary 심기가능성으로만 고른다**(판정·GT는 선택 입력 아님). **최종 type = `address`(확정).** 사적 자원(비인증 401·계정 단위 격리)이라 수평 IDOR 인가모델에 맞고(이동찬 E² 등록 기준 확인 필요), 심을 직속 자유 string(`fullName`)이 있어 carrier로도 적격 — 아래 §carrier의 C1~C6 실측 결과. (basket은 인가모델엔 맞지만 심을 필드가 없어 탈락.) **GT 상관 가능 여부는 레지스트리를 고정한 뒤 평가 쪽(최민준)이 별도 판단하고, 그 결과로 등록할 자원을 거르지 않는다**(판정 담당은 내용 선택에 참여하지 않음 — `:14`·§8 #2). 스켈레톤·레지스트리엔 GT 키·상관 데이터 없음.
2. **canary `field`**: 내가 고르는 값이 아니라 **규칙** — `idor.Marker.Field`(=`fullName`)는 E² `JSONViewRule.DataPath`가 가리키는 **객체 안의** 직속 string 필드명(view당 유일)이다. `DataPath`는 그 객체까지의 경로이지 필드명이 아니다(`transmit/projection.go`가 DataPath를 `map[string]any`로 단언한 뒤 `data[Field]`를 읽음). 레지스트리↔E² view **정확히 같은 집합**, 같은 prep에서 공동작성(불일치=송신 전 `canary_mismatch`). 규칙은 기존 계약으로 닫힘. **스켈레톤은 합성 placeholder**(field·value), 실제 값은 prep.
3. **자원 개수**: 1단계 = **자원 1개(A 소유)** 확정(§1·"칸 하나짜리"). 대칭 확장은 선택.

## ✅ carrier 결정 (해소) — canary carrier = `address`
**(11/9) 윤재상 C1~C6 실측(Juice Shop v15.0.0)으로 확정.**
측정 조건: 격리된 1회용 로컬 인스턴스(시드 데이터만, 외부 주소·리다이렉트 없음), 같은 권한(customer)의 신규 계정 A·B, 모델 미사용(순수 HTTP 관찰), 후보마다 심기 전 사전 스캔 0건, 심기와 되읽기를 다른 요청으로 분리. 측정 결과물은 레포에 넣지 않으므로 원본 보고서는 레포 밖에 측정 담당(윤재상)·이경준이 보관하고 요청하면 공유한다. 리뷰 대조에 필요한 조건·결과는 아래에 요약한다. 교차 조회의 성공·실패와 GT 대조는 측정하지 않았다.
후보별 결과:
- **`address` — 적격(C1~C6 전부 pass). 채택.** 사적 자원(비인증 GET `401`·계정 B 목록에 A 자원 미노출=계정 격리), per-id GET `/api/Addresss/{id}` 응답 `{data:{fullName, streetAddress, …}}`의 `data` 객체 안 직속 string `fullName`(대안 `streetAddress`) → 심기·verbatim 복귀·반사없음. (E² view로는 DataPath=`data` 객체 경로, `Markers[].Field`=`fullName`.) 교차 GET 시도 경로(`GET /api/Addresss/{id}`, B가 A의 id로) 존재(※성공/실패는 미측정 = 판정[최민준] 몫). 심기(POST)/되읽기(GET per-id) 분리, sentinel 32hex·사전 스캔 0건.
- **`basket` — 탈락(C1).** 객체 필드(id/coupon/UserId/createdAt/updatedAt/Products)에 심을 비자명 직속 자유 string 없음. (사적+교차경로는 가장 좋으나 심을 데가 없음.)
- **`review`/`feedback` — 부적격(C4).** 심기·verbatim·반사없음(C1~C3,C5,C6)은 되지만 **비인증 GET으로 전량 공개 조회**라 사적 접근통제 경계가 없음 → 교차로 증명할 IDOR 경계 자체가 없음.

이전 내 추천(review/feedback)이 **측정으로 뒤집힌 지점**: 자유텍스트 심기가능성만 봤고 "사적 경계"를 놓쳤다. C4(사적+교차 시도 가능)가 그걸 잡아냄 → `address`가 심기가능 + 사적 경계를 **둘 다** 만족하는 유일 후보.

**폴백 규칙 유지**: C1~C6 전부 통과 type이 없었으면 CONFIRMED 불가(UNDECIDABLE 상한). 지금은 `address`가 통과해 1단계가 CONFIRMED에 **도달 가능**.

→ 반영: 레지스트리 `resource_ref` type=`address`·`canaries[].field`=`fullName`로 **교체 완료**(이 변경). **활성화 전제**(별도, 이동찬 prep): E² view `Markers[].Field`=`fullName` 확인 + `DataPath`=그 필드를 **담은 부모 객체 경로**(응답 `{data:{fullName}}`면 `["data"]`)로 확정. ⚠ `DataPath`를 `fullName`(string 필드)로 지정하면 `transmit/projection.go`의 객체 단언(`atPath(…).(map[string]any)`)이 실패해 정상 소유자 응답에서도 표식 추출 불가 → 판정 UNDECIDABLE에 멈춘다. 이어서 §9 `CheckPairing` 결속, 최민준 GT 상관(type 고정 뒤·별도이고 자원 선택 조건 아님). 스켈레톤 값(run_id·sentinel 등)은 여전히 prep에서 채운다.

## 검증
- **실제 `kgfixture.Parse`로 로드 확인 — PARSE OK.** `address:a-owned-PLACEHOLDER`(type:id 콜론 규약)·canary `field=fullName`(자원내 유일)·value 전역유일·`trivial=false` 통과. (11/9 address/fullName 교체본 재검증.) `owner A→A=allow`·`cross B→A=deny`, policy version `kg-fixture/v1:kgfx-juiceshop-s1:<digest>` 결정적 산출. (최종 게이트 = 그 로더.)
- 값 채운 뒤: §9 `(*Snapshot).CheckPairing`으로 레지스트리 ↔ E² snapshot 라벨·resource_ref·reset 세대·canary 집합 대조(불일치=`pairing_*`) = 이동찬 서버 조립 때.

## 다음
- 커밋 위치: 평가용 레지스트리는 §2가 "결과 전 파일째 커밋" → **평가 디렉토리(최민준/윤재상 영역)와 맞물림**, §8 #2가 위치를 "열린 채". 뼈대·type 합의 후 커밋 지점 협의.
- 실제 값: 평가환경(윤재상 35B/스캔·운영자 reset·canary 심기) 준비와 동시.
