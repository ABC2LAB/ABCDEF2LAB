# KG 1단계 canary carrier 선정 — 측정 기준·체크리스트 (이경준, 설계 lane)

> canary 설계 소유 = 이경준(kg-policy §3.2). 이 문서는 1단계 최종 `resource_ref` type을
> **canary 심기 가능성 + 앱 인가모델**로 고르기 위한 측정 스펙이다. **GT는 선택 입력이 아니다**
> (§8 #2·#234 리뷰: 판정 담당은 내용 선택 불참, GT 상관은 고정 뒤 평가 쪽 별도).
> 라이브 측정(1회용 Juice Shop v15)은 평가환경 prep에서 수행 — 이 문서는 그 전 선작업.

## 왜
- `basket`은 심을 비자명 문자열 필드가 없어 탈락(최민준 실측: id/UserId=정수, coupon=null·PUT 404, createdAt/updatedAt=안 심은 값, Products=배열).
- 표식이 없으면 setup-success=Unknown → 판정 UNDECIDABLE에서 멈춤 → 접근통제 지도 "취약 확정" 칸 안 뜸. 1단계 walking skeleton의 CONFIRMED 도달이 여기 달림.
- 그래서 1단계 최종 type은 **측정으로** 심기 가능한 자원에서 고른다.

## carrier 적격 기준 (전부 충족해야 함)
| # | 기준 | 근거 |
|---|---|---|
| C1 | 등록 조회 **정상 응답**이 모양을 만족: `StatusPath`가 **문자열**이고 그 값 = `NormalValue` + `DataPath`가 **비어있지 않은 JSON 객체** + 표식 넣을 **직속 문자열 필드**가 그 객체에 있음(중첩·배열 아님) | transmit `projection.go`(`status==NormalValue`·`DataPath`=map·직속 string) |
| C2 | 심은 **무작위 비자명 sentinel**(`Trivial=false`)이 조회 응답의 그 필드에 **전체 값이 정확히 그대로** 돌아온다 — 감싸기·이스케이프·절단·부분 포함은 **불일치**(owner 자기조회로 verbatim 확인, 교차 아님) | kg-policy §3.1 · projection.go `field == marker.Value` |
| C3 | sentinel이 **요청에서 반사되지 않는다**(저장된 A 데이터라서만 응답에 뜸) | idor-verifier-seam anti-reflection |
| C4 | 자원이 **앱 인가모델상 계정별 사적**(owner A 소유, 교차 조회 **기대=deny**)이고, **id로 지정하는 조회 경로**가 있어 엔진이 교차 GET을 **시도**할 수 있다. 교차 GET이 실제 성공하는지(=취약 여부)는 **측정 항목이 아니다**(그게 1단계 판정 결과) — 그 결과로 후보를 거르지 않는다 | §1 수평·읽기 · 절대규칙 7 |
| C5 | sentinel을 **조회경로와 분리된 준비 쓰기**로 심을 수 있다(가능하면 정상 앱 쓰기, DB 해킹 아님) | 절대규칙 6·§9 step3 |
| C6 | 세대 안 **value 전역 유일** 가능 + owner 역참조가 `owner_ref` 라벨만 | kg-policy §3.2(3)(6)·절대규칙 2 |

## 후보 (resource-ref-types-proposal §3) — 전부 **가설, 측정으로만 확정**
스키마를 기억으로 단정하지 않는다. 라이브에서 C1~C6을 재서 채운다.
| type | 상태 | 측정할 것 |
|---|---|---|
| `basket` | **측정됨 · 탈락**(C1/C2 불충족) | — |
| `review` | 미측정 | 사용자 작성 텍스트 필드가 조회 응답 DataPath 직속인지, **계정별 사적인지**(준사공개=사적 아님이면 C4 탈락)·**id 지정 조회경로** 유무, 반사 여부 |
| `feedback` | 미측정 | 위와 동일 — comment류 텍스트 필드·**사적 여부·조회경로**·반사 |
| `export`·`user` | 미측정 | 자유텍스트 유무·**사적 경계**·반사. `user`는 `owner_ref`/인증 주체와 섞지 않음(proposal §2) |

## 측정 프로토콜 (1회용 Juice Shop v15, 평가환경)
- **담당**: C1~C6 심기가능·구조 측정 = **내용 담당/운영자(이경준·윤재상·운영자)**. 등록 GET(owner/cross 같은 객체·비변경→`ReadOnly=true`)·§9 `CheckPairing` = **이동찬**. GT 단일대응 = **최민준(고정 뒤·선택 입력 아님)**.
- 후보 순서는 **측정 전에 고정**한다: `review` → `feedback` → `export` → `user`. 각 후보에 C1~C6(전부 **구조·심기가능** 기준) 통과/실패 기록, **첫 전부통과 후보 채택**(측정 결과로 순서·채택을 바꾸지 않음).
- **교차 GET 실제 성공 여부(= IDOR 취약 여부)는 측정 항목이 아니다.** prep에서 확인차 재더라도 그 결과로 후보를 거르지 않는다(절대규칙 7·§8 #2 "결과 보기 전 고정").
- 가드: 리셋 후 측정, 대상 origin만(절대규칙 1), 챌린지 목록 입력 금지(절대규칙 7), 준비 쓰기는 조회경로와 분리(절대규칙 6). 응답 원문·sentinel 값은 공유 로그·근거에 원문으로 남기지 않음(절대규칙 2).

## 결정·반영
1. 측정 → 첫 통과 type 채택.
2. 이동찬 등록/pairing 확인.
3. 고정 → 스켈레톤 `resource_ref` type·`canaries[].field`만 교체(구조·정책 동일).
4. (고정 뒤) 최민준 GT 상관.

**폴백**(C1~C6 통과 후보가 없을 때): `createdAt` 같은 관측값은 **심은 값이 아니라 canary가 아니다** — idor-canary-fixture §2–3의 심기+KG 등록·owner `setup-success`를 증명하지 못하므로 **CONFIRMED 근거가 될 수 없다.** 쓰더라도 **관측 보조로만, 판정은 UNDECIDABLE 상한**. CONFIRMED를 허용하는 폴백은 **별도 검증 계약·시험을 최민준·이동찬과 합의한 뒤에만** 정의한다. C1~C6 carrier가 없으면 그 type의 1단계는 정직하게 **UNDECIDABLE에서 멈춘다**(희석된 CONFIRMED를 만들지 않음). DB 직접 쓰기는 앱별 미검증이라 최후.
