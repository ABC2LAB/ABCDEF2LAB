# 평가 러너 기록 v1.1 제안 — 격리 보증 구간 해결 귀속

상태: **최민준과 필드·검증 조건을 합의할 계약 제안. 현재 채점기는 v1의 직접 관측 이벤트만 수용하며, 이 문서가 `isolated_window` 수용이나 실환경 격리 구현 완료를 뜻하지 않는다.**

선행 관계: **[PR #51](https://github.com/ABC2LAB/ABCDEF2LAB/pull/51) 리뷰·병합 → #52를 그 위로 정렬 → v1.1 필드 합의.** 대상 구동·격리 모드·같은 호스트의 운영 조건은 #51이 반영하는 [평가 프로토콜 §2](evaluation-protocol.md#2-평가-대상-고정)와 §4가 정본이다. 이 문서는 그 조건을 기록·검증할 입력 계약 제안이며 Docker 예시나 별도 격리 정책을 정의하지 않는다.

연결: [현재 v1 계약](run-record.md) · [평가 규칙 §4](evaluation-protocol.md#4-판정-기준) · [측정 매트릭스 §5](evaluation-protocol.md#5-측정-매트릭스-값은-비움-틀만)

## 1. 공통 귀속 방식과 한계

baseline에는 E²가 없으므로 E² traffic ID를 해결 주체 확인의 필수 조건으로 삼지 않는다. baseline·개선 후 모두 **러너가 실제로 집행한 격리 구간**과 pre/post scoreboard 델타를 같은 규칙으로 사용한다. 전송 통제(E²)의 지원 범위나 요청별 원인 증명을 이 계약으로 확대하지 않는다. v1.1 격리 귀속 비교 표에서는 양쪽 모두 같은 v1.1 계약·격리 조건을 적용한다. 기존 v1 직접 이벤트는 하위 호환용이며 v1.1 격리 회차와 한 비교 표에 섞지 않는다.

측정 단위는 챌린지의 신규 해결 여부다. 증명이 유효한 격리 구간에서 `pre=false → post=true`인 항목만 그 회차 엔진의 해결로 귀속할 수 있다. 이 방식은 해결을 일으킨 개별 요청이나 정확한 해결 시각을 알려 주지 않는다. post 관측 시각을 실제 해결 시각으로 쓰지 않으며, 기존 `elapsed_seconds`도 독립적으로 측정한 확정 시각이 없으면 null로 둔다.

격리 귀속은 **해결 귀속의 선행 조건**만 충족한다. 평가 §4의 판정 레코드·traffic 증거·상관·커버리지 규칙은 그대로 적용한다. 유효한 격리 증명만으로 finding이나 자동 매트릭스 CONFIRMED를 만들지 않는다. baseline의 traffic/출력 어댑터도 별도 필요하다. 사람 수동 확정은 이 이벤트의 발행 근거가 아니며 자동 집계에서 제외한다.

## 2. 러너의 실제 집행 의무

1. **회차 전용 접근 경계:** 네트워크 모드와 전용 평가 호스트 운영 조건은 평가 프로토콜 §2를 따른다. 러너는 선택한 모드·경계 식별자·적용 시각·정책 지문·검증 근거와 같은 호스트의 운영 조건 확인 근거를 보존한다. 네트워크가 다른 로컬 프로세스·사용자까지 차단한다고 주장하지 않는다. 네트워크와 운영 조건을 함께 확인해 해당 엔진과 등록 관측기 외의 접근 가능성을 해소해야 한다. 공개 listener·우회 경로·정책 유실 또는 호스트 운영 조건 불충족을 발견하면 그 구간의 격리 증명을 발행하지 않는다.
2. **pre 이후 준비 금지:** 회차별 인스턴스 생성/reset·계정·fixture 준비는 pre 전에 완료한다. pre부터 post까지 provisioning·fixture 수정·다른 사용자의 조작·추가 준비 요청을 허용하지 않는다. 이는 엔진 자신의 승인된 검사 요청을 준비 요청으로 분류한다는 뜻이 아니다. reset·인스턴스/실행 교체도 같은 구간에 섞지 않는다.
3. **관측기 제한:** 러너가 등록한 GET 목록만 사용한다. 각 경로는 해당 대상 버전에서 상태 변경·solve 유발이 없는 읽기 전용 계측임을 별도로 검증하고 그 근거를 연결한다. GET이라는 메서드만으로 읽기 전용을 추정하지 않는다. 등록 밖 요청·재시도·redirect를 통한 우회도 허용하지 않는다.
4. **실제 요청 기록과 종료:** 관측기의 실제 송신 요청을 누락 없이 세고, 각 요청의 메서드·등록 경로 참조·시각·결과를 비민감 로그로 보존한다. 요청 수는 로그 행 수와 일치해야 한다. post는 엔진 신규 송신 종료와 진행 요청 drain을 확인한 뒤 수집한다. 미완료/송신 불명·관측 로그 누락·경계 공백이 있으면 증명을 발행하지 않는다.

이 의무의 집행 주체는 엔진과 키를 공유하지 않는 신뢰 러너다. HMAC은 러너 기록의 출처와 무결성을 증명하며 네트워크 격리 자체를 생성하지 않는다. 실제 경계를 확인하지 않고 키 보유자가 필드를 채운 기록은 유효한 측정 근거가 아니다. 실환경 도입 전 우회 접근·pre 이후 준비·관측기 제한 위반을 실제로 차단하는 시험이 필요하다.

## 3. v1.1 러너 기록 확장안

필드명·타입은 아래 안으로 리뷰한다. v1의 서명·회차 문맥·아티팩트 해시·요청 종료·pre/post 정규화 검증을 유지한다. manifest에는 귀속 선언이나 격리 증명을 추가하지 않는다.

- `version`: 문자열 `"1.1"`을 제안한다. 기존 정수 `1` 기록은 기존 직접 관측 규칙으로 읽는다. 버전을 느슨한 숫자/문자열 변환으로 비교하지 않는다.
- `isolation_proof`: 아래 구조의 객체 또는 null. 구간 증명이 없으면 null이며 `isolated_window` 이벤트를 인정하지 않는다.
- `artifacts.isolation_evidence`: 격리 집행·관측 로그의 descriptor 또는 null. 기존 descriptor와 같은 문맥·phase=engine·안전한 경로·size·SHA-256 검증을 적용한다. 증명 객체가 있으면 이 자료도 필수다.

`isolation_proof`와 `artifacts.isolation_evidence`는 둘 다 null이거나 둘 다 유효한 객체여야 한다. 한쪽만 제출한 부분 증명은 입력 오류다.

`isolation_proof` 전체와 `isolation_evidence` descriptor는 러너 기록의 HMAC 서명 대상이다. 이벤트는 증명 전체를 복제하지 않고 canonical SHA-256을 참조한다. canonical JSON 규칙은 v1의 서명 직렬화와 같고, 증명 digest 자체가 별도의 발급 권한을 만들지는 않는다.

| 증명 필드 제안 | 검증 의무 |
|---|---|
| `proof_id` | 회차별 고유한 비민감 증명 참조. 다른 회차에 재사용 금지 |
| `run_id`, `model_tag`, `reset_generation`, `instance_id`, `execution_id` | 서명된 러너 기록의 5개 문맥과 정확히 일치 |
| `window.pre`, `window.post` | 러너 timeline의 pre/post `{at, sequence}`와 정확히 일치. 격리 경계는 pre 직후부터 post 관측 완료까지 연속 유지 |
| `snapshots.pre_sha256`, `snapshots.post_sha256` | 각각 `artifacts.pre.sha256`, `artifacts.post.sha256`와 문자열이 정확히 같아야 함. 정규화 파일 바이트의 해시이며 descriptor JSON 자체의 해시가 아님. raw/정규화 대응은 기존 검증 유지 |
| `network.mode` | `loopback_only` 또는 `dedicated_network`. 모드 이름 자체를 집행 증거로 보지 않음 |
| `network.target_origin`, `network.boundary_ref`, `network.policy_sha256` | 설정의 대상 origin·회차 전용 경계와 결속. URL에 자격정보·query·fragment 금지. loopback 모드는 127.0.0.1, 전용 네트워크 모드는 등록된 대상·경계만 허용 |
| `network.allowed_actors` | 해당 실행의 엔진과 등록 관측기만 포함. 다른 actor/경계 외 접근의 가능성이 해소되지 않으면 증명 불가 |
| `enforcement_evidence_sha256` | `artifacts.isolation_evidence.sha256`와 일치. 경계 적용/유지·준비 봉인·관측 경로 읽기 전용 검증의 러너 소유 근거가 실재해야 함. 해시 문자열만 있고 자료가 없으면 오류 |
| `preparation.sealed` | 준비 완료의 `{at, sequence}`. fixture_ready와 일치하며 pre보다 앞섬 |
| `preparation.actions_after_pre` | pre 이후 준비 요청/변경 건수인 정수 0. 엔진 자신의 검사와 구분하고 읽기 전용 준비 요청도 포함. bool·음수·누락 금지. 준비 통제 로그와 대조하며 단독 자기 선언으로 인정하지 않음 |
| `observer.ref`, `observer.registered_gets` | 러너가 등록한 관측기 참조와 GET 경로 참조 목록. 각 경로의 읽기 전용 검증 근거를 isolation_evidence에 연결 |
| `observer.request_count` | pre 관측 완료 직후부터 post 관측 완료까지 실제 송신 건수. 순서 범위는 `(pre.sequence, post.sequence]`다. pre 수집 GET 제외·post 수집 GET 포함. 정상 post 수집이 있으므로 양의 정수이며 bool 금지. 미수집을 0으로 대체하지 않음 |

`isolation_evidence`는 위 5개 문맥·`phase: "engine"`과 경계 수명주기/준비 통제/관측 요청 로그를 가진 평가 전용 자료다. 상세 행 스키마와 실제 경계 adapter는 후속 구현 전에 합의한다. 최소 검증 조건은 다음과 같다.

- 정책 적용이 pre 이전에 끝나고 post까지 유효하며 경계 교체·해제·허용 actor 변경이 없어야 한다. 같은 회차 대상과 정책 지문이어야 한다.
- 준비 봉인 이후 pre..post에 준비 요청/변경이 0건이며 로그가 구간 전체를 관측했음을 확인해야 한다. 로그 없음과 준비 없음은 다르다.
- 구간의 관측 요청마다 ID·GET·등록 경로 참조·송신/완료 시각·순서·결과를 보존한다. 요청 ID 중복, 등록 밖 경로, 비-GET, 구간 밖 송신, 미완료·송신 불명, 누락된 결과는 거부한다. pre 수집 GET은 pre 단계에서 완료되어 진행 요청이 없어야 하며 같은 GET 등록/읽기 전용 검증을 받는다. post 수집 GET을 포함한 구간 송신 로그 수와 `observer.request_count`가 같아야 한다.
- 본문·인증값·계정 원문·표식 값·셸 명령 원문을 기록하지 않는다. 서버/네트워크 정책의 비민감 참조·digest와 관측 경로 참조만 사용한다.

관측기 요청 수는 별도 기록이며 평가 §5의 엔진 `total_requests`에 섞지 않는다. baseline·개선 후 모두 같은 정의를 사용한다. 엔진 실제 요청 수를 측정하지 못했다면 기존 규칙대로 null이며, 임의로 0이나 추정 차감값을 넣지 않는다.

## 4. 해결 이벤트의 basis

v1.1 이벤트는 기존 필드에 `basis`와 `isolation_proof_sha256`을 추가하는 안이다. 다음은 구조 예시이며 실제 서명/증명으로 사용할 자료가 아니다.

```json
{"id":"solve-1","challenge":"evaluation-only-key","actor":"engine",
 "execution_id":"execution-1","observed_at":"2026-10-30T09:00:10+00:00","sequence":6,
 "basis":"isolated_window",
 "isolation_proof_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
```

| 버전/basis/actor | 수용 조건 |
|---|---|
| v1의 engine 이벤트 | 기존 직접 관측 출처·실행 ID·엔진 시작..drained 시각/sequence 검증. 기존 형식을 보존하고 v1.1 격리 이벤트로 승격하지 않음 |
| v1.1의 `isolated_window` + engine | 유효한 서명·격리 증명·집행/관측 근거와 그 증명의 canonical digest가 모두 일치. 실행 ID·회차·스냅샷도 일치 |
| v1.1의 `direct_event` 또는 proof 없는 engine | 거부. v1.1 엔진 귀속은 반드시 isolated_window와 유효 proof를 요구 |
| v1.1의 other/unknown | execution_id/basis/digest는 null, 성과에 귀속하지 않음. 유효 격리 proof를 동시에 제출할 수 없음 |

`isolated_window`는 각 이벤트마다 **actor=engine·basis=isolated_window·증명 digest**를 모두 요구한다. 엔진의 solved=true, actor만 있는 행, `exclusive_window=true`, 서명되지 않은 manifest 선언은 대체물이 아니다.

- 이벤트 challenge마다 해당 구간의 정확한 `pre=false → post=true`를 확인한다. 단순 `post=true`, pre부터 해결, true→false, key 누락·인스턴스/reset 혼입은 수용하지 않는다. ID/challenge 중복도 기존 규칙대로 거부한다.
- 격리 이벤트는 델타를 확인한 **post 관측 시각·sequence**를 사용한다. 현재 직접 이벤트의 engine_start..drained 검사를 그대로 적용해 가상의 요청 시각을 만들지 않는다. 격리 경계의 끝은 post 관측 완료까지다.
- 유효한 proof를 제출하면 러너는 전체 `false→true` 델타에서 이벤트를 발행한다. 채점기는 **신규 해결 key 집합과 engine/isolated_window 이벤트 key 집합의 완전 일치**를 확인한다. 신규 해결 누락/추가·other/unknown 혼입은 입력 오류로 중단한다. 델타가 비었을 때만 빈 이벤트 배열을 허용한다. proof 없이 이벤트를 조작해 빈칸을 채우지도 않는다.
- proof=null로 명시한 미수집이고 engine 귀속 주장도 없으면 새 해결의 귀속은 미확인이다. 유효 proof를 주장하면서 이벤트 자료가 빠졌거나, 제출된 증명의 서명·문맥·필수 필드·참조·시각·해시·실제 로그 정합성이 잘못되면 입력 오류로 채점을 중단한다. 위반을 발견한 러너는 성립하지 않은 proof를 발행하지 않고 귀속 불가 사유를 보존한다.

## 5. 구현 전 필수 회귀·실측 검증

아래는 **예정 사례**이며 실행한 테스트가 아니다.

| ID | 사례 | 기대 결과 |
|---|---|---|
| IW01 | baseline·개선 후 동일 v1.1 격리 계약·유효 증명/델타 이벤트, E² traffic ID 없음; v1/v1.1 회차 혼합 | 동일 v1.1이면 같은 귀속 규칙 적용, E² 보유 여부로 갈리지 않음. 버전/귀속 방식 혼합 비교는 거부 |
| IW02 | 유효 격리·false→true·engine/basis/digest·post 관측 문맥 모두 일치 | engine_solved로 전달 가능. 최종 확정은 평가 §4의 다른 게이트 유지 |
| IW03 | solved=true 또는 actor=engine만, exclusive_window boolean, manifest 자체 귀속 선언 | 격리 귀속 거부 |
| IW04 | proof/필수 evidence/서명 누락·잘못된 digest·서명 후 proof 변조 | 오류·집계 표 미출력. null로 명시한 미수집과 구분 |
| IW05 | 다른 run/model/reset/instance/execution/proof 재사용·pre/post digest 불일치 | 오류·집계 표 미출력 |
| IW06 | pre=true인 항목의 신규 이벤트, true→false, key 불완전, 델타와 무관한 이벤트·중복 이벤트 | 입력 오류·집계 표 미출력. 이벤트 없이 pre=true인 항목은 기존 사전 해결 규칙 유지 |
| IW07 | 공개 listener·전용 네트워크 우회·정책 공백·같은 호스트의 운영 조건 불충족 | 다른 호스트 접근 차단과 전용 평가 호스트 운영 조건을 각각 확인. 네트워크만으로 same-host 차단을 주장하지 않으며 격리 조건 불충족이면 proof/귀속 발행 0 |
| IW08 | pre 이후 준비·fixture 변경·reset/인스턴스 교체 | 실제 변경 경로 차단, 구간 성립 불가 시 proof/귀속 발행 0 |
| IW09 | 관측기의 비-GET·등록 밖 GET·redirect/재시도 우회·solve 유발 GET | 실제 관측기 경계에서 차단, 미검증 GET을 등록하지 않음 |
| IW10 | 관측 요청 수 bool/음수/null·로그 수 불일치·중복 ID·누락/미완료 결과 | 채점 입력 오류. 미수집을 0으로 표시하지 않음 |
| IW11 | 직접 이벤트 시각을 격리 이벤트에 복사하거나 post 시각·sequence 불일치 | 오류. 정상 격리 이벤트는 post 관측으로 기록 |
| IW12 | pre GET·구간 관측 GET 여러 건·post GET과 엔진 송신을 함께 계측, baseline/개선 후 비교 | 관측 구간 건수에서 pre GET 제외·post GET 포함, total_requests는 엔진 실제 송신만. 미측정은 null |
| IW13 | 유효 proof의 신규 해결 이벤트 누락/추가·other/unknown 혼입, 빈 델타/빈 이벤트; 또는 proof=null인 미수집 | 불완전/모순 입력은 오류, 완전한 빈 집합은 정상, proof=null은 엔진 귀속 미확인. 정상 engine 이벤트라도 최종 확정은 기존 평가 §4 적용 |
| IW14 | v1 직접 관측 기록, v1.1 proof 없는 direct_event/engine 기록, 알 수 없는 버전 | v1은 기존 계약 검증, v1.1은 격리 증명 없는 엔진 귀속 거부, 알 수 없는 버전/느슨한 타입 변환 거부 |
| IW15 | post 델타만 있고 실제 엔진 확정 시각 없음·사람 수동 확정만 추가 | elapsed_seconds 추정 생성 0, 자동 CONFIRMED 증가 0 |

합의 순서는 proof/증거 로그 필드와 버전 호환성 확정 → 채점기·합성 회귀 구현 → 실제 러너의 격리/준비/관측 경계 구현과 우회 시험 → baseline·개선 후 같은 조건의 실측이다. 합성 서명·해시 시험은 실제 격리 집행 검증을 대신하지 않는다. 그 전까지 현재 v1의 fail-closed 동작을 유지한다.
