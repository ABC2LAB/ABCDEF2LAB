# 구조화 판정 레코드 계약 v0.1 (설계 초안)

작성: 2026-10-28 · 최민준 · 팀 검토용 초안
상태: **설계 초안. 저장소·마이그레이션 미구현.** [e3-oracle](./e3-oracle.md) Open #1·#2·#5 에 대한 응답.
연결: [E③ 오라클](./e3-oracle.md) · [소유·권한 KG](./kg-policy.md) · [평가 프로토콜](../evaluation/evaluation-protocol.md)

## 0. 범위 — 왜 별도 레코드인가
E③ 오라클(`oracle.Evaluate`)이 내는 **구조화 판정 1건**을 저장하는 레코드의 논리 스키마다. ARTEX 기존 `findings`(취약점 triage)·`finding_retests`(재검증 이력, 자유텍스트 `verdict`)와 **분리**한다(e3-oracle §8-⑤):
- `finding_retests.verdict` 는 에이전트가 쓰는 자유텍스트 결론이다. 구조화 판정(CONFIRMED/SUSPECTED/UNDECIDABLE/ALLOWED + 세 체크 + 증거 신호)을 거기 욱여넣으면 "증거 없는 확정"을 막는 보수성이 사라진다.
- 이 레코드는 **판정의 근거·통제변수**(정책버전·reset 세대·관찰시각·증거 ref)를 같이 실어, 평가 집계와 감사·재판정이 재현 가능하게 한다.

**경계**: 논리 스키마·필드 의미·마스킹·평가 읽기 계약 = 최민준(E③ 설계). 테이블명·마이그레이션·ARTEX db 배선 = 이동찬(§5). 시각화 읽기 필드 = 이경준.

## 1. 레코드 = oracle 판정 1건
한 case(동일 작업·의도·엔드포인트·자료상태, e3-oracle §5) 의 `oracle.OracleVerdict` + 어댑터가 distill 한 통제변수. **순수 오라클 밖**(어댑터가 생성·저장).

## 2. 필드

### (a) 연결·식별
| 필드 | 타입 | 의미 |
|---|---|---|
| `id` | id | 레코드 PK |
| `run_id` | string | 서버/평가 러너가 부여한 회차 식별자. GT 정보를 담지 않으며 pre/post·traffic·커버리지의 회차 메타데이터와 같아야 한다. `case_ref`로 대체하지 않는다. |
| `finding_id` | id? | 연관 ARTEX finding(있으면). 판정이 finding 생성보다 앞서면 NULL |
| `case_ref` | string | case 식별(작업·의도·엔드포인트·자료상태 해시). 같은 근거만 한 case |
| `axis` | enum | `horizontal`(v0.1). 수직은 후속 v0.2가 같은 레코드에 `vertical` 로 |

### (b) 판정 (OracleVerdict → 저장, ALLOWED 분해 = e3-oracle §8-①)
| 필드 | 타입 | 의미 |
|---|---|---|
| `oracle_value` | enum | 원값 그대로 {confirmed, suspected, undecidable, allowed} — 감사·충실성 |
| `vuln_verdict` | enum? | triage 대면 판정 {confirmed, suspected, undecidable} 또는 **NULL**. `oracle_value=allowed` 일 때만 NULL |
| `access_policy_result` | enum | 정책 관찰 축(§8-① 분해): `위반_미관찰`(allowed) / `위반_후보`(confirmed·suspected) / `판정_불가`(undecidable) |
| `policy_check` `identity_check` `data_check` | enum | 세 체크 {yes, no, unknown} |
| `reason_codes` | []string | `JudgeReason` 순서대로(정책→신원→데이터). ALLOWED 는 허용 사유 1개 |

### (c) 비교 신호 (ComparisonFacts → 저장, **이름·불리언만, 원문 금지**)
| 필드 | 타입 | 의미 |
|---|---|---|
| `same_resource` `owner_normal_data` `cross_normal_data` `nontrivial_data_match` `reflection_excluded` `template_excluded` | enum | 6 신호 {yes, no, unknown} |
| `canary_fields` | []string | 표식이 산 **응답 필드명만**. 값·본문은 저장 안 함(절대규칙 2·8 / DLP) |

### (d) 통제변수·근거 ref (§5 — 재현·감사)
| 필드 | 타입 | 의미 |
|---|---|---|
| `expected_access` `sharing` `delegation` | enum | 정책 사유 입력(해소 상태) |
| `policy_version` | string | KG 정책 snapshot 버전(§8-②). 정본 reader 가 준 버전 |
| `policy_valid_from` `policy_valid_until` | ts | 정책 유효창. 유효성은 `observed_at` 기준 판정 |
| `hint_matches_canonical` | bool | 힌트=정본 여부. 불일치면 정책 무효 |
| `observed_at` | ts | 근거 관찰시각(정책 유효성 판정 기준, §3(a)) |
| `reset_generation` | string | Juice Shop reset 세대. 세대 섞이면 어댑터가 Evaluate 전 UNDECIDABLE 로 떨굼 |
| `owner_ref` `evaluated_ref` | string | 주체 **이름/라벨만**(토큰·비밀 금지, 절대규칙 2) |
| `owner_status` `evaluated_status` | enum | 주체 검증 상태 |
| `traffic_evidence_refs` | []id | ARTEX traffic 증거 id(본문 아님) |
| `vuln_class` | string | ARTEX VulnClass. 평가가 챌린지 상관에 씀 |
| `resource_ref` | string | 논리 자원 참조(엔드포인트 서명) |

### (e) 운영
| 필드 | 타입 | 의미 |
|---|---|---|
| `model_tag` | string | 판정 run 의 LLM 태그(평가 통제변수, baseline·개선 동일 문자열) |
| `created_at` | ts | 생성시각 |

## 3. 마스킹 (절대규칙 2·8 — 하드 규칙)
- 저장 금지: 응답 본문 원문, canary **값**, 인증 헤더·쿠키·토큰·비밀번호·계정 원문, HMAC 키.
- 저장 허용: 필드 **이름**, 비교 신호 **불리언/enum**, 증거 **ref/id**, 주체 **라벨**.
- 이 레코드는 LLM 입력·공유 로그·근거 export 어디에도 원문을 싣지 않는다. 비교기(`idor.Compare`)가 이미 값이 아닌 신호만 내므로, 어댑터는 그 신호·이름만 기록한다.

## 4. 평가 읽기 계약 (eval count_basis ↔ 레코드)
평가 분류의 정본은 [평가 프로토콜 §4](../evaluation/evaluation-protocol.md#4-판정-기준)이며 여기에 별도 판정표를 두지 않는다. 평가기는 레코드를 같은 회차의 pre/post 스냅샷·해결 귀속 근거·traffic·커버리지와 조합한다. **레코드만으로 확정/미탐을 정하지 않는다.**

- 사전 해결 항목·해결 귀속 미확인은 판단불가다. confirmed도 엔진 귀속 해결과 증거 3조건을 충족해야 확정이 된다.
- 레코드 없음은 자동 미탐이 아니다. 상관 가능 여부와 실제 검사 커버리지 또는 엔진 귀속 해결을 확인한다.
- `oracle_value=allowed`는 정책상 허용 판단이며, GT에 allowed뿐이면 판단불가 + 경고로 남긴다. NONE/미탐으로 접지 않는다.
- 회차·모델·reset 세대 및 필수 입력의 오류는 채점 중단이다. 물리 저장/어댑터는 이 연결 근거를 보존해야 한다.
- 레코드의 필수 `run_id`는 러너의 회차 기록 및 각 아티팩트 메타데이터와 대조한다. 없거나 다르면 채점 중단이다. `model_tag`·`reset_generation`만 같다는 이유로 회차 일치를 추정하지 않는다. 이 필드의 발행·파싱·검증도 후속 어댑터 구현 범위다.

- **절대규칙 7**: 레코드는 GT 챌린지 키를 **담지 않는다**. 레코드는 자연 증거(`vuln_class`·`resource_ref`)만 싣고, (vuln_class·엔드포인트)→챌린지 매핑은 **평가 쪽(평가 전용 GT)** 이 소유한다. GT 를 탐색·판정·레코드에 넣지 않는다.
- 현재 `parse_scan_output`에는 JSON 레코드 파싱 뼈대가 있으나 위 평가 규칙 전체를 구현한 것은 아니다. 공식 비교는 프로토콜 §1의 구현 조건을 충족한 채점기로 수행한다.

## 5. Open — 리뷰 정렬
1. **테이블명·마이그레이션** — `oracle_verdicts`(가칭). `findings`/`finding_retests` 와 FK 관계·인덱스. (이동찬)
2. **`access_policy_result` enum 값 3종**이 §8-① 의도와 맞나 — ALLOWED 분해를 `위반_미관찰` 1개로 둘지, 비위반 축을 더 둘지. (최민준 ↔ 이동찬)
3. **`policy_version` ↔ 유효창 ↔ 정본 reader** 매핑(e3-oracle §8-②). (이동찬 ↔ 이경준)
4. **시각화가 읽는 필드 집합** — 접근통제 지도가 레코드에서 뭘 읽나(판정·주체·자원·축). (이경준)
5. **`case_ref` 산출 규칙** — 무엇을 한 case 로 묶는 해시 입력(작업·의도·엔드포인트·자료상태). (최민준, 어댑터 정렬 이동찬)
