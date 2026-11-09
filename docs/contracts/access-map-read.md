# 접근통제 지도 조회 API — Open#6 1단계

이동찬 서버 구현 경계. 의미 기준은 [kg-policy §4.2](kg-policy.md#42-접근통제-지도-실데이터-읽기-어댑터-open-6-구체화--verdict-record-open-4-공동-응답)와 [판정 레코드](verdict-record.md)다.
합의한 현재 세대·수평·읽기 projection만 제공한다. UI 연결과 실대상 진단 성능은 이 구현의 완료 범위가 아니다.

## 요청·인증

`GET /api/tasks/{id}/access-map`

- 정규 양의 정수 task ID만 받는다. run/case/이력/query 파라미터와 HEAD·쓰기 메서드는 지원하지 않는다.
- 기존 ARTEX 관리자 JWT의 `Authorization: Bearer …`만 사용한다. 쿠키나 query token으로 이 핸들러를 직접 인증할 수 없다.
- 첫 진단 서버와 저장 보고서 서버에서는 기동 과정에 결속된 task만 허용한다. 준비 전용 서버에는 열지 않는다.
- `Cache-Control: no-store`. GET은 읽기 전용 repeatable-read 트랜잭션으로만 조회하며 모델·대상·검증기·판정 발급을 호출하지 않는다.

## 현재 범위의 출처·수명

서버가 KG fixture를 성공적으로 등록할 때 비밀 없는 축·소유·정책 등록 목록을 기존 `exploration_nodes`의 전용 origin `abc2lab.access-map.current/v1`에 동결한다. task 행 잠금 아래 현재 포인터 하나만 갱신하며 여러 행이면 실패한다. 일반 fact나 finding 제목에서 범위를 추측하지 않는다.

등록 소비 때 서버가 새로 발급한 E② `Scope.RunRef`를 같은 등록에 결속한다. fixture 준비 `run_id`와 평가 `eval_run_id`로 대체하지 않는다. 실행 전에는 run이 없으므로 409다. 실행·task mode 버전·case·전체 자원 참조·KG ref/digest·reset·하네스 세대·대상 인스턴스를 저장 발급 정보 및 canonical 판정/증거/Source와 대조한다.

새 등록은 먼저 기존 현재 포인터를 무효화한다. 로드·pairing이 실패해도 이전 지도를 복구하지 않는다. 새 등록/세대의 실행이 시작되면 그 실행만 읽으며 과거 confirmed는 저장 이력에 남아도 현재 셀에 재사용하지 않는다. 실행 종료로 폐기된 전송 핸들을 다시 열지 않는다. 등록·세대 갱신 없이 외부에서 수행한 대상 리셋을 이 GET이 자동 탐지하는 것은 아니다.

판정 없는 실제 시도도 같은 범위의 계정·자원 참조로 저장한다. owner/cross 검증에서는 판정 대상인 교차 계정만 지도 시도로 기록하고, 정상 대조용 소유자 GET은 별도 판정 대상 셀로 기록하지 않는다. 소유자 셀은 등록 정책이 있으면 `observation_unavailable`, 없으면 `blank`다. 소유자 GET의 실제 전송·감사·증거 기록은 그대로 유지한다. 시도 저장 실패는 해당 통제 조회를 진행하지 않는다. 원래 E② 승인·전송 판정과 E③ 발급 권위는 대체하지 않는다.

UI는 `state=unknown`만으로 관측 누락과 판단불가 판정을 합치지 않는다. `projection_error=observation_unavailable`는 관측 근거가 없다는 뜻이고, `oracle_value=undecidable`·`projection_error=null`은 저장된 판단불가 판정이다.

E③ v1 payload의 정책·관찰·생성 시각은 마이크로초 정밀도로 저장된다. 지도는 같은 저장 정밀도로 원본 manifest/Source와 대조하며, 원본 시각이나 증거 digest 자체는 수정하지 않는다.

저장 보고서 역할에는 기존 권한에 더해 `task_control_modes`의 **SELECT**가 필요하다. 설치자가 부여하며 runtime/GET은 DDL·GRANT를 실행하지 않는다. 보고서 생성자가 이 권한을 기동 전에 확인한다.

## 응답

최상위: `task_id`(정수), `run_ref`, `reset_generation`, `axis="horizontal"`, `policy_version`, `cells`.
`policy_version`은 **최상위 하나만** 제공하며 `kg-fixture/v1:<snapshot_id>:<64hex digest>` 원값이다.

등록 principal×resource 격자의 각 셀을 하나씩 제공한다. 축 표시는 등록된 참조로 구성하고, 1단계 `resource_label`은 전체 논리 `resource_ref`와 같다. 실제 URL·자격 값·계정 원문·표식 값·응답 본문은 제공하지 않는다.

| 셀 필드 | 의미 |
|---|---|
| `principal`, `owner_ref`, `resource_ref`, `resource_label` | 라벨/전체 논리 참조 |
| `state` | 아래 서버 파생 상태. API `confirmed`는 기존 UI에서 `vuln`으로 매핑 |
| `oracle_value` | 정본 소문자 `confirmed/suspected/undecidable/allowed`, 레코드 없음·선택 불가 시 null |
| `applicable` | 정책에 평가 주체가 등록되면 true, 등록 근거 없으면 null. false는 1단계 미산출 |
| `record_id` | 정본 판정 PK, nullable. 별도 evidence_ref를 발급하지 않음 |
| `case_ref`, `observed_at` | 단일 유효 판정의 case와 관찰 시각, 없으면 null |
| `policy_check`, `identity_check`, `data_check` | 저장 체크 신호 `yes/no/unknown` |
| `same_resource`, `owner_normal_data`, `cross_normal_data`, `nontrivial_data_match`, `reflection_excluded`, `template_excluded` | 저장 비교 신호 `yes/no/unknown` |
| `reason_codes` | 저장 오라클 사유 배열. 없는 경우 [] |
| `canary_fields` | 필드명만. 없는 경우 [] |
| `projection_error` | 출처·선택·기록 누락 오류 코드 또는 null. 오라클 사유와 분리 |

`applicable`은 접근 허용이 아니다. 평가 정답 목록·레코드 부재·소유자와 평가 주체의 동일성으로 적용불가를 추론하지 않는다.

## 셀 선택·상태

현재 scope의 셀 키는 `(principal, owner_ref, resource_ref, axis)`다.

- 0건: 레코드 없음 분기. 명시적 시도 후 누락이면 unknown(`record_missing_after_attempt`), 적용 근거만 있고 관측을 확인하지 못하면 unknown(`observation_unavailable`). 성공한 조회에서 관련 근거가 전무할 때만 blank.
- 1건: 출처·digest·회차/세대·모델 메타·manifest 비교 신호가 모두 맞는 레코드에서 파생한다. 불일치는 unknown(`record_invalid`).
- 2건 이상: 판정이 같아도 unknown(`selection_ambiguous`), 단일 판정 필드는 null. 최신·confirmed 우선 선택은 없다. 재검증의 evidence_revision 선택은 후속 계약이다.

유효 단일 레코드의 confirmed·위반후보는 `confirmed`, suspected는 `suspected`, undecidable은 `unknown`이다. 원래 oracle_value·reason_codes는 보존한다. allowed도 그대로 보존하며 동일 자원·정상 교차 데이터·신원 검증·정책 허용/공유/위임 해소·유효 관찰 시각·근거가 모두 있어야 `allow-ok`다. allowed 하나만으로 정상 접근을 산출하지 않는다.

`untested`·`na`는 Phase 2, 별도 앱 차단 관측이 필요한 `deny-ok`는 Phase 3까지 미산출이다. E② 거부·통신 실패·표식 미복귀·HTTP 상태 하나를 앱의 정상 차단으로 바꾸지 않는다.

## 실패

| HTTP | 의미 |
|---|---|
| 400 | 직접 핸들러의 잘못된 task/조회 형식 |
| 401 | Bearer 인증 부재·실패 |
| 403 | 첫 진단/보고서 task 결속 위반·전용 서버의 미허용 요청 |
| 404 | 존재하지 않거나 삭제된 task |
| 409 | 현재 등록/실행 scope 없음·무효·복수 현재 포인터·mode 버전 변경 |
| 503 | 저장소 조회/권한·구조·크기 검증 실패 |

scope를 확인하지 못한 응답을 빈 지도·blank·미검사로 합성하지 않는다. 전체 현재 포인터/발급 레코드 조회는 5초·16MiB 상한을 적용하고, 입력 레지스트리의 계정·자원 상한을 넘는 축과 과도한 레코드 수는 거부한다.
