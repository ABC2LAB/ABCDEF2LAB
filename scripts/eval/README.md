# 평가 스크립트 (뼈대)

baseline(원본 ARTEX) vs 개선 후(우리 포크)를 같은 조건으로 측정·집계한다.
측정 조건의 정본은 [평가 프로토콜](../../docs/evaluation/evaluation-protocol.md)이며, 여기는 이를 코드화하는 중인 집계 뼈대다.

**공식 평가 사용 전 보완 필요:** 현재 코드와 미병합 PR #28은 pre/post 스냅샷·엔진 해결 귀속·회차/모델/reset 연결 검증을 모두 구현하지 않았다. 기존 표는 새 프로토콜에 따른 공식 baseline/개선 비교에 사용하지 않는다. 단위 테스트 통과는 기존 구현의 회귀 확인이며 새 규칙 준수의 증명이 아니다.

## 구성
- `ground_truth.py` — 평가 정답(코어 GT 9 = 수평5·수직4, BAC category 10, 별도축 3).
  **평가에만** 쓴다(절대규칙 7). 엔진·탐색 경로에서 import 금지.
- `aggregate.py` — 기존 순수 집계·표 렌더 뼈대. 증거/커버리지/해결 귀속 게이트와 표본 SD 규칙은 정본에 맞춰 보완해야 한다.
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
  현재의 `allowed→NONE` 변환은 **수정할 기존 동작**이다. 새 정본은 ALLOWED를 보존하고 판단불가 + 경고로 분류한다.
- 판정 우선순위와 레코드 없음 처리는 [평가 프로토콜 §4](../../docs/evaluation/evaluation-protocol.md#4-판정-기준)가 정본이다. 기존 구현의 `allowed/none` 병합을 새 규칙으로 사용하지 않는다.
  위반_후보(confirmed·suspected)와 allowed가 섞인 챌린지는 `conflicts`로 따로 남긴다(판정은 안 바뀜).

## 아직 막힌 경계 (TODO)
- 판정 레코드 **물리 형식**(DB 쿼리 vs JSON 덤프) — 이동찬 어댑터와 정렬. 어댑터만 바꾸면 됨.
- `correlate` 실 매핑 테이블(vuln_class·엔드포인트→챌린지) — 평가 전용 GT로 따로 둔다.
- TODO(윤재상): baseline(원본 ARTEX) 출력은 판정 레코드를 안 냄 — 별도 어댑터로 같은 레코드를 만들지 정렬.
- 판정 레코드 테이블명·마이그레이션은 verdict-record.md §5 Open.
