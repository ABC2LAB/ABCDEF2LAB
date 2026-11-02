# 원본 ARTEX 분류 매핑 사전진단기

`scripts/eval/baseline_mapping_check.py`는 원본 FindingDTO를 기존 추출기로 검증한 후
미등록 분류 필드별 source 건수와 검토할 source ID만 집계한다.
[기존 추출기](baseline-extractor.md)와 [페이지 병합기](baseline-pages.md)를 재사용하고 해당 파일은 수정하지 않는다.
페이지 수집기 PR #70·기존 회차 작성기·네트워크·러너·GT·채점에는 의존하지 않는 오프라인 도구다.

## 집계 경계

- 고정 원본 SHA는 `fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`다. 실제 출처 인증은 아니며 기존 출처·AGPL-3.0 표시는 유지한다.
- 필드는 `vulnclass`, `assets.type`, `severity` 3개만 반환한다. 원본 분류명·매핑 key/value·URL·본문·헤더·snapshot·note·원본 상태는 출력하지 않는다.
- 각 필드의 건수는 해당 필드가 미등록인 **DTO source 행 수**다. 한 행에 미등록 자원 type이 여러 개여도 한 번 센다.
- `unmapped_source_count`는 하나 이상 미등록 필드가 있는 source 행의 수다. 필드별 건수를 더한 값과 다를 수 있다.
- source ID는 원본 DTO의 `id`다. 조회에 따라 탐색 노드 또는 DB finding ID일 수 있으며 namespace나 standalone `finding_id`를 추정하지 않는다.
- 서로 다른 source 행이 같은 standalone finding을 가리켜도 별도로 집계한다. 자원/근거 공유·owner 검증은 기존 추출기 규칙을 따른다.
- 자원 참조의 누락/null은 미수집이며 `asset_refs_not_collected_count`와 ID 목록으로 따로 남긴다. 명시적 빈 배열은 미수집이 아니다.
- ID 목록은 양의 정규 int64 문자열을 숫자순으로 정렬한다. 입력을 수정하거나 미등록 원문을 매핑으로 자동 등록하지 않는다.
- 잘못된 필수 필드·타입·중복·원본 선언·분류 매핑·페이지 집합은 오류다. 한 행이라도 잘못되면 부분 집계를 내지 않는다.

이 결과는 입력 범위의 매핑 검토 목록이다. 건수0이나 빈 입력으로 공식 평가 준비 완료·실검사 커버리지·취약점 없음·GT 상관·판정을 만들지 않는다.
자원 참조가 미수집이거나 명시적으로 비어 있으면 그 자원 type의 매핑을 확인한 것도 아니다.

## 입력과 실행

기본 입력은 기존 추출기가 지원하는 FindingDTO 배열·단건 또는 **단일 페이지**다.
단일 페이지가 전체 조회 결과라는 뜻은 아니며 출력의 `input_source_count`는 전달된 행만 센다.
여러 페이지의 JSON 배열은 `--pages`를 명시해야 하고 페이지 병합기의 연속성·건수·교차 중복 검사도 통과해야 한다.
한 모드의 입력을 다른 모드로 추측해 바꾸지 않는다. 추출 완료 JSON이나 임의로 만든 `unmapped_fields`를 신뢰하지 않는다.

```bash
python3 -I -B scripts/eval/baseline_mapping_check.py \
  /private/evaluation/findings.json /private/evaluation/vocabulary.json \
  --source-sha fdfb7a17b7da5e2de716c0e98475ec77a41c64c6
# 여러 페이지 객체 배열을 진단할 때 위 명령에 --pages 추가
```

합성 입력 예시:

```json
[
  {"id":"1","vulnclass":"SQL Injection","severity":"high","status":"confirmed","assets":[]},
  {"id":"2","vulnclass":"Unregistered class","severity":"unrated","status":"pending","assets":null},
  {"id":"3","vulnclass":"SQL Injection","severity":"high","status":"resolved",
   "assets":[{"id":"77","type":"Unregistered type","label":"SYNTHETIC-PRIVATE"}]}
]
```

같은 예시의 검토된 분류 매핑:

```json
{"vulnclasses":{"SQL Injection":"sqli"},"asset_types":{"endpoint":"endpoint"}}
```

이 예시의 입력 source는3건, 미등록 source는2건이다. 필드별 미등록은 vulnclass=1건(ID2),
assets.type=1건(ID3), severity=1건(ID2)이며 자원 참조 미수집은1건(ID2)이다.
원본 미등록 라벨·자원 label·상태는 집계 결과에 포함되지 않는다. 매핑을 검토하는 책임은 평가자에게 있다.

## 출력과 API

`diagnose_baseline_mapping(payload, vocabulary, source_sha=source_sha, pages=False)`를 호출한다.
`pages`는 bool만 허용하며 명시적 True일 때만 다중 페이지 입력이다.
결과 `BaselineMappingDiagnosis`의 필드:

| 필드 | 의미 |
|---|---|
| `declared_source_sha` | 기존 추출기가 지원 원본과 대조한 호출자 선언 |
| `source_shape` | array / detail / page / pages |
| `input_source_count` | 실제로 검증·집계한 DTO source 행 수 |
| `unmapped_source_count` | 하나 이상의 미등록 필드가 있는 source 행 수 |
| `asset_refs_not_collected_count` | assets 누락/null인 source 행 수 |
| `asset_refs_not_collected_source_ids` | 위 미수집 source ID 목록 |
| `fields` | 고정 순서(vulnclass, assets.type, severity)의 `field`, `source_count`, `source_ids` 세 항목 |

CLI는 성공 시 종료0과 JSON 한 개, 실패 시 종료2·빈 stdout·고정 필드명/오류 코드를 반환한다.
알 수 없는 CLI 인자·입력 key/value·경로·원래 예외의 원문은 오류에 싣지 않는다.
파일 읽기 외 I/O·환경변수·네트워크·프로세스 실행·파일 쓰기는 없다.
입력/매핑/실제 집계 결과는 레포 밖에 보관한다. 파일 입력 상한은 전체8 MiB, 매핑64 KiB이며
기존 strict JSON reader로 UTF-8·중복 key·비유한수·잘못된 JSON을 거부한다.

## 검증

합성 DTO만 이용해 필드 중복 집계·동일 standalone 참조·누락/null/빈 assets·여러 미등록 자원·수치순 정렬,
모드별 입력·페이지 오류·미등록 원문 비출력·오류 시 부분 결과 미반환·CLI 크기/인자 경계를 검증한다.
실제 스캔 출력·로그·DB 덤프·서버·모델은 사용하지 않는다.

```bash
cd scripts/eval
python3 -I -B -m unittest discover -s tests -p test_baseline_mapping_check.py -q
python3 -I -B -m unittest discover -s tests -q
```
