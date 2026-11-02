# 원본 ARTEX 다중 페이지 결과 병합기

`scripts/eval/baseline_pages.py`는 FindingDTO 페이지 객체들의 배열을 받아
[기존 추출기](baseline-extractor.md)의 비민감 결과를 병합한다.
각 페이지를 공개 `extract_baseline()`으로 먼저 검증하고 페이지 집합의 수치·참조 일관성을 검사한다.
기존 추출기·채점기·GT 상관·회차 기록 계약은 수정하지 않는다. 표준 라이브러리만 사용한다.

## 입력과 실행

입력은 같은 조회에서 얻은 `{items, total, page, page_size}` 페이지 객체들의 비어 있지 않은 배열이다.
원본 FindingDTO 배열·단건·`{data: ...}` 응답이나 추출 완료된 JSON은 페이지 대신 섞지 않는다.
조회 필터·task·회차가 같은지, 원자료의 출처가 올바른지는 수집하는 신뢰 러너가 확인해야 한다.
source ID는 원본의 `id`이며, task 조회의 탐색 노드 ID와 전역 조회의 DB finding ID를 서로 바꿔 해석하지 않는다.

```bash
python3 -I -B scripts/eval/baseline_pages.py \
  /private/evaluation/pages.json /private/evaluation/vocabulary.json \
  --source-sha fdfb7a17b7da5e2de716c0e98475ec77a41c64c6
```

`--source-sha`는 지원하는 고정 원본 SHA에 대한 호출자의 선언이며 실제 checkout이나 API 출처 인증은 아니다.
원본 출처(`jiwoochris/artex-global`)와 AGPL-3.0 표시는 그대로 유지한다.
CLI는 입력 파일을 읽고 stdout에 JSON 한 개만 출력한다. 파일 쓰기·환경변수 설정·네트워크·프로세스 실행은 없다.
서로 다른 현재 디렉터리에서도 `-I`로 실행할 수 있도록 스크립트의 실제 디렉터리에서 동반 추출기를 찾는다.
입력/매핑/출력 원자료는 레포 밖에 보관하며 커밋하지 않는다.

검증용 합성 입력 예시:

```json
[
  {"items": [
    {"id": "1", "finding_id": "101", "vulnclass": "SQL Injection", "severity": "high", "status": "confirmed"},
    {"id": "2", "finding_id": "102", "vulnclass": "SQL Injection", "severity": "high", "status": "pending"}
  ], "total": 3, "page": 1, "page_size": 2},
  {"items": [
    {"id": "3", "finding_id": "103", "vulnclass": "SQL Injection", "severity": "medium", "status": "resolved"}
  ], "total": 3, "page": 2, "page_size": 2}
]
```

같은 예시의 평가자 검토용 분류 매핑:

```json
{"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}
```

매핑은 기존 추출기의 정규 라벨 규칙을 그대로 따른다. 미등록 분류/알 수 없는 severity는 null과
`unmapped_fields`로 남기며 원본 자유 문자열·제목·URL·헤더·본문·snapshot·note를 출력하지 않는다.
매핑 자체가 비민감 분류명인지 검토하는 책임은 평가자에게 있다.

## 페이지 집합 규칙

- 모든 페이지의 `total`·`page_size`가 같아야 한다. bool·소수·문자열·누락된 페이지 수치는 거부한다.
- `total > 0`이면 1부터 `ceil(total/page_size)`까지 페이지가 정확히 한 번씩 있어야 한다.
- 각 페이지의 행 수는 `min(page_size, total - (page-1)*page_size)`와 같아야 한다.
  중간/마지막 페이지의 부족한 행을 채우지 않고 실패한다. 마지막 페이지가 꽉 차면 빈 다음 페이지를 요구하지 않는다.
- `total=0`이면 `page=1`인 빈 페이지 하나만 허용한다. 비어 있는 입력 배열은 관측된 빈 결과와 구분하여 거부한다.
- 입력 페이지의 도착 순서는 자유다. 출력은 페이지 번호순이며 각 페이지의 원래 행 순서는 보존한다.
- source ID와 binding ID는 페이지 전체에서 중복을 거부한다. 중복 페이지나 행을 조용히 제거하지 않는다.
- 서로 다른 source 행에서 standalone `finding_id`나 자원 ID가 공유되는 것은 기존 정책처럼 허용한다.
  binding 소유 관계와 한 finding 안의 자원/binding 중복 검사는 기존 추출기를 따른다.
- null/미수집 참조와 명시적 빈 참조 배열을 그대로 구분한다. 부족한 근거나 ID를 만들어 넣지 않는다.

## 출력과 실패

성공 결과 `BaselinePageMerge`의 필드:

| 필드 | 의미 |
|---|---|
| `declared_source_sha` | 고정 지원 원본과 일치한 호출자 선언 |
| `declared_total` | 모든 입력 페이지에 공통으로 선언된 total. 실제 병합 행 수와 같음을 검사 |
| `page_size` | 공통 페이지 크기 선언 |
| `page_count` | 실제 입력·검증·병합한 페이지 수 |
| `findings` | 기존 추출기의 비민감 `ExtractedFinding`들. source/standalone ID·분류·원본 상태·참조를 그대로 구분 |

API 사용은 `merge_baseline_pages(pages, vocabulary, source_sha=source_sha)`다. 입력 객체를 수정하지 않는다.
CLI 입력 전체의 상한은 8 MiB, 분류 매핑 상한은 64 KiB다. 기존 `_read_json` helper를 재사용해
UTF-8·중복 JSON key·비유한수·잘못된 JSON·크기 초과를 같은 규칙으로 거부한다.
엄청나게 큰 total 선언을 검증할 때 그 크기만 한 페이지 번호 목록을 할당하지 않는다.

잘못된 인자도 원문을 재출력하지 않는다. 실패 시 stdout은 비어 있고 stderr에는 고정 필드명/오류 코드만 출력하며 종료2다.
API 오류는 `BaselinePagesError`이며 기존 페이지/분류/참조 오류 코드도 보존한다.
새 집합 오류는 `EXPECTED_NONEMPTY_PAGE_ARRAY`, `EXPECTED_PAGE_OBJECT`, `PAGINATION_MISMATCH`,
`DUPLICATE_PAGE`, `INCOMPLETE_PAGES`, `PAGE_SEQUENCE_GAP`, `PAGE_LENGTH_MISMATCH`,
`DUPLICATE_SOURCE_ID`, `DUPLICATE_BINDING_ID`다. 한 페이지라도 잘못되면 일부 병합 결과를 내지 않는다.

## 증명하는 범위

성공은 **전달된 페이지 집합이 자신이 선언한 페이지 수치·참조 규칙과 일치함**만 뜻한다.
필터가 잘못되었거나 다른 task/회차 자료가 섞였는지, 조회 중 서버 자료가 변경됐는지,
실제 서버 전체 자료를 수집했는지·근거가 실재하는지는 이 입력만으로 확인할 수 없다.
따라서 `complete=true` 같은 실제 수집 완료 표시나 검사 커버리지는 생성하지 않는다.
빈 결과도 해당 선언 아래 항목이 없었다는 뜻이며 취약점 없음·전체 검사 완료를 의미하지 않는다.

원본 triage 상태를 E③ 판정으로 변환하지 않는다. GT 상관·해결 귀속·traffic 조회·A5 baseline 채점 어댑터는 후속이며
기존 baseline 채점 미구현 중단은 그대로다. 평가 회차 작성기와 PR #54의 지문 생성기에 의존하지 않는다.
실제 대상·모델·스캔 출력·DB 덤프를 사용한 검증은 수행하지 않는다.

## 테스트

```bash
cd scripts/eval
python3 -I -B -m unittest discover -s tests -p test_baseline_pages.py -v
python3 -I -B -m unittest discover -s tests -q
```

합성 단일/다중/빈 페이지·마지막 부분 페이지·페이지/행 누락·수치 불일치·교차 중복·공유 참조 보존,
원문 미출력·strict JSON·크기 제한·CLI 오류·결정적 순서·입력 불변성을 검증한다.
전체 평가 suite의 소유 loopback 합성 HTTP 테스트는 소켓 제한 sandbox에서 별도 실행 권한이 필요하다.
