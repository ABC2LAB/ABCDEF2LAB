# 원본 ARTEX finding 추출기

`scripts/eval/baseline_extract.py`는 원본 ARTEX의 FindingDTO JSON에서 평가용 비민감 분류·자원·근거 연결 참조를 추출한다.
표준 라이브러리만 사용하는 독립 모듈이며, 기존 baseline 채점 어댑터(A5)·GT 상관·해결 귀속에는 연결하지 않는다.
검증은 합성 JSON으로 수행한다. 실제 스캔 출력·DB 덤프·대상 앱·모델·baseline 실행은 사용하지 않는다.

## 원본 근거

지원 원본은 `jiwoochris/artex-global`의 `fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`다. 원본 출처와 기존 AGPL-3.0 표시는 유지한다.

- [FindingDTO와 자원 DTO](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/server/dto.go#L305): 필드·triage 상태·severity·optional 배열.
- [전역 DB finding 변환](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/server/dto.go#L439): 전역 조회의 `id`와 `finding_id`는 같은 DB finding ID다.
- [finding 목록 응답](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/server/server.go#L2023): 배열·페이지 envelope와 task 조회 분기.
- [traffic binding 구조와 역할](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/db/finding_traffic.go#L42): 요청의 `traffic_refs`와 응답의 `traffic_bindings`를 구분한다.

## 입력과 실행

원본 `GET /api/exploration/findings`의 배열, `{items, total, page, page_size}` 페이지 객체,
`GET /api/exploration/findings/{id}`의 단건 FindingDTO 객체를 지원한다. 서로 다른 조회·task의 행을 합쳐 넣지 않는다.
DB row 덤프, `{data: ...}` 같은 다른 API envelope, 독립 `/traffic` 응답, 요청 입력용 `traffic_refs`는 지원하지 않는다.

```bash
python3 -I -B scripts/eval/baseline_extract.py \
  /private/evaluation/findings.json /private/evaluation/vocabulary.json \
  --source-sha fdfb7a17b7da5e2de716c0e98475ec77a41c64c6
```

`--source-sha`는 고정 지원 형식과 비교하는 호출자 선언이다. 원자료의 실제 출처·checkout SHA를 인증하지 않는다.
입력은 최대 8 MiB, 분류 매핑은 최대 64 KiB의 UTF-8 JSON이다. 중복 key·비유한수·잘못된 JSON은 거부한다.
오류는 입력값·알 수 없는 필드명을 출력하지 않고 stderr의 고정 코드와 종료2만 반환한다. 한 행이라도 잘못되면 stdout은 비어 있다.
성공은 종료0과 JSON 한 개다. 파일 쓰기·환경변수·네트워크·프로세스 실행은 없고 입력 파일도 바꾸지 않는다.
실제 원자료·설정·추출 결과는 레포 밖에 보관하며 커밋하지 않는다.

분류 매핑의 형식 설명용 합성 예시:

```json
{
  "vulnclasses": {"SQL Injection": "sqli"},
  "asset_types": {"endpoint": "endpoint"}
}
```

원본의 `vulnclass`·asset `type`은 자유 문자열이므로 그대로 출력하지 않는다. 평가자가 미리 등록한 정확한 문자열만
비민감 정규 라벨로 변환한다. 출력 라벨은 `[a-z][a-z0-9_]{0,63}`이며, 제목·이름·대소문자 변환으로 분류를 추정하지 않는다.
등록되지 않은 값은 null과 `unmapped_fields`의 고정 필드명으로 표시한다. 명시적 빈 매핑도 허용하며 모든 분류는 미등록으로 남는다.
이 매핑은 평가자가 검토한 분류명만 담아야 한다. 대상 앱·LLM이 생성한 설정이나 계정·URL·GT key를 매핑으로 등록하지 않는다.

## 출력 경계

| 출력 필드 | 의미 |
|---|---|
| `declared_source_sha` | 지원 원본 SHA에 일치한 호출자 선언 |
| `source_shape`, `pagination` | 입력 배열/페이지/단건 형식과 원래 페이지 수치. 완전한 수집·검사 커버리지는 증명하지 않음 |
| `findings[].source_id` | DTO의 원래 `id`. task 조회에서는 탐색 노드, 전역 조회에서는 DB finding ID이므로 namespace를 추정하지 않음 |
| `finding_id` | 선택적 standalone finding ID. `source_id`에서 만들어 채우지 않음 |
| `vuln_class`, `asset_refs[].type` | 등록된 정규 분류 라벨. 미등록 값은 null |
| `severity`, `source_status` | 등록된 severity 4종과 triage 상태 9종만 보존. 원본의 빈 값·화이트리스트 밖 severity는 null로 내리고 `unmapped_fields`에 `severity`를 표시 |
| `asset_refs[].id` | 원본에 선언된 자원 ID |
| `traffic_binding_refs[]` | 원본 binding의 `binding_id`, `finding_id`, `role`만 보존. role은 baseline/proof/verification/supporting |
| `unmapped_fields` | 미등록 원문을 담지 않는 `vulnclass` / `assets.type` / `severity` 표시 |

원본 DTO는 signed int64를 문자열로 직렬화한다. 이 평가 추출기는 정상 DB 참조로 사용할 양의 정규 10진 문자열만 지원하며,
JSON 숫자·UUID·0·음수·앞자리0·범위 초과를 거부한다. 양수 제한은 이 추출기의 입력 정책이다.
근거 binding은 명시적 `finding_id`가 있어야 하며 소유 finding ID와 일치해야 한다.
source ID와 binding ID는 응답 전체에서 중복을 거부한다. 자원 ID는 한 finding 안에서 중복을 거부하며 서로 다른 finding의 공유 자원은 허용한다. 원본 severity는 화이트리스트가 보장되지 않으므로 지원하지 않는 문자열을 baseline 전체 실패로 만들지 않고 null과 `unmapped_fields`로 표시한다.
원문의 `snapshot_id`·`source_traffic_id`는 자유 문자열이므로 출력하지 않는다. 후속 수집기는 binding ID로 원본 근거를 조회·검증해야 한다.

분류와 참조를 포함하는 합성 FindingDTO 예시:

```json
{
  "id": "456", "finding_id": "123", "vulnclass": "SQL Injection",
  "severity": "high", "status": "confirmed",
  "assets": [{"id": "77", "type": "endpoint", "label": "SYNTHETIC-PRIVATE"}],
  "traffic_bindings": [{"id": "2", "finding_id": "123", "role": "proof", "note": "SYNTHETIC-PRIVATE"}]
}
```

이 입력의 source_status는 `confirmed`지만 오라클 판정·확정/오탐/미탐·엔진 해결 귀속을 생성하지 않는다.
summary·evidence·report·name·task 설명·label·note·snapshot·URL·헤더·본문은 출력에서 제외하며, 무시하는 본문 필드의 내용은 검증하지 않는다.
단건의 분류나 자원 ID만으로 GT 상관을 만들거나 원문의 triage 상태를 E③ 판정으로 바꾸지 않는다.

assets/bindings가 없거나 null이면 결과도 null이다. 명시적 빈 배열은 빈 배열로 보존하며, traffic_count를 이용해 근거를 만들어 채우지 않는다.
빈 finding 배열은 해당 응답에 항목이 없었다는 관찰이다. 존재하지 않는 task도 빈 배열을 반환하므로 취약점 없음이나 전체 수집 완료로 기록하지 않는다.
조회 범위·회차/인스턴스·실제 ID 존재·근거 무결성·수집 완료는 별도 신뢰 러너가 기록·검증해야 한다.

## 검증

합성 원본 DTO의 배열/페이지/단건, 노드/DB ID 구분, legacy·미수집·미등록 분류, 근거 소유 충돌,
원문 미출력, 잘못된 타입/중복/페이지 수치, strict JSON·크기 제한·CLI 종료·실패 시 빈 stdout을 검증한다.
기존 평가 테스트도 함께 실행해 A5 미구현 중단과 기존 채점 동작을 유지한다.

추출기는 빈 페이지도 수집된 관측으로 보존하며, 선언된 total 안의 빈 페이지를 수집 완료나 발견 없음으로 해석하지 않는다. 전체 페이지 수·행 수·누락 검증은 [페이지 병합기](baseline-pages.md)와 수집기가 수행한다. JSON 문법 오류의 고정 `json: …` 코드는 두 입력 파일에 공통이며 파일명이나 원문을 포함하지 않는다. 입력 역할은 호출자가 지정한 결과 파일/분류 매핑 인자로 구분한다. 원본 DTO의 `omitempty`로 생략된 assets/bindings는 이 단계에서 부재·미수집을 구분할 수 없으므로 null로 보존하고, 명시적 빈 배열과는 구분한다.
