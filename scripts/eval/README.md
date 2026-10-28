# 평가 스크립트 (뼈대)

baseline(원본 ARTEX) vs 개선 후(우리 포크)를 같은 조건으로 측정·집계한다.
측정 조건의 정본은 `docs/evaluation/evaluation-protocol.md` — 여기는 그 §3·§4·§5를
코드로 옮긴 집계 틀이다. 숫자가 아니라 틀만 있다.

## 구성
- `ground_truth.py` — 평가 정답(코어 GT 9 = 수평5·수직4, BAC category 10, 별도축 3).
  **평가에만** 쓴다(절대규칙 7). 엔진·탐색 경로에서 import 금지.
- `aggregate.py` — 순수 집계. §4 분류(확정/오탐/미탐/판단불가) + §5 평균 ± 분산 + 표 렌더.
- `collect.py` — I/O 어댑터. Juice Shop `/api/Challenges` 해결상태, env 설정, 스캔출력 파싱.
- `tests/` — 순수부 테이블 테스트.

## 실행
```bash
cd scripts/eval
python3 -I -m unittest discover -s tests   # 순수부 테스트
```

## 설정 (코드 하드코딩 금지 — .env)
- `EVAL_TARGET_ORIGIN` — 랩 대상 origin (예 `http://127.0.0.1:3000`). localhost 랩만 허용.
- `ARTEX_LLM_BASE_URL` — Ollama 엔드포인트.
- `EVAL_MODEL_TAG` — baseline·개선 후 **동일**해야 하는 정확한 모델 태그(§6).

## `parse_scan_output` — 판정 레코드 → 챌린지별 판정
입력은 [판정 레코드 계약](../../docs/contracts/verdict-record.md)의 **논리적 레코드**만 받는다.
- 물리적 출처는 얇은 어댑터 뒤 — 지금은 JSON 파일(`[...]` 또는 `{"records": [...]}`) 하나.
- `correlate(record) -> 챌린지 키 | None`를 **호출자가 주입**한다. 레코드엔 GT 키가 없고(절대규칙 7),
  (vuln_class·resource_ref)→챌린지 상관은 평가가 쥔다. None이면 out-of-list.
- 깨진 레코드·계약 밖 verdict/필드·민감값 누출은 전부 모아 `ScanOutputError`(fail-closed).
  undecidable→판단불가, allowed→NONE(미탐 측) — 오탐·취약점없음으로 바뀌지 않는다.
- 한 챌린지에 레코드가 여럿이면 tie-break `confirmed > suspected > undecidable > allowed/none`.
  위반_후보(confirmed·suspected)와 allowed가 섞인 챌린지는 `conflicts`로 따로 남긴다(판정은 안 바뀜).

## 아직 막힌 경계 (TODO)
- 판정 레코드 **물리 형식**(DB 쿼리 vs JSON 덤프) — 이동찬 어댑터와 정렬. 어댑터만 바꾸면 됨.
- `correlate` 실 매핑 테이블(vuln_class·엔드포인트→챌린지) — 평가 전용 GT로 따로 둔다.
- TODO(윤재상): baseline(원본 ARTEX) 출력은 판정 레코드를 안 냄 — 별도 어댑터로 같은 레코드를 만들지 정렬.
- 판정 레코드 테이블명·마이그레이션은 verdict-record.md §5 Open.
