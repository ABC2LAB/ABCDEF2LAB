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

## 아직 막힌 경계 (TODO)
- `parse_scan_output` — finding/E③ 판정 레코드 포맷 확정 후 구현.
  - TODO(이동찬): 개선 후(우리 포크) finding·판정 레코드 포맷.
  - TODO(윤재상): baseline(원본 ARTEX) 스캔 출력 포맷.
- E③ 판정 레코드 테이블명·스키마는 e3-oracle 스펙 §8-⑤ 정렬 후.
