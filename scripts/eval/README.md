# 평가 스크립트 (뼈대)

baseline(원본 ARTEX) vs 개선 후(우리 포크)를 같은 조건으로 측정·집계한다.
측정 조건의 정본은 `docs/evaluation/evaluation-protocol.md` — 여기는 그 §3·§4·§5를
코드로 옮긴 집계 틀이다. 숫자가 아니라 틀만 있다.

## 구성
- `ground_truth.py` — 평가 정답(코어 GT 9 = 수평5·수직4, BAC category 10, 별도축 3).
  **평가에만** 쓴다(절대규칙 7). 엔진·탐색 경로에서 import 금지.
- `aggregate.py` — 순수 집계. §4 분류(확정/오탐/미탐/판단불가) + §5 평균 ± 표본 표준편차(SD) + 표 렌더.
- `collect.py` — I/O 어댑터. Juice Shop `/api/Challenges` 해결상태, env 설정, 스캔출력 파싱.
- `scoreboard.py` — 스코어보드 스냅샷(프로토콜 §2). 원본→정규화(`{key: solved}`) 규칙, 정규화본 파서,
  GT key 누락 검사(누락 = 에러, 미해결로 간주 X).
- `manifest.py` — 회차 메타데이터(조건·엔진·model_tag·reset 세대·레코드/스냅샷 경로·시간·요청수·차단 시도 수).
  측정값은 러너가 못 주면 null → 표 "—". 총 요청 수 = 실제 전송 HTTP만(E² 차단 시도·LLM 호출 제외),
  차단 시도 수는 별도 열(프로토콜 §5).
- `score.py` — 저장 raw 채점기 CLI. 회차 manifest → §4 분류 → §5 표(코어 GT 9 / BAC 10).
  엔진별로 다른 건 '레코드 → 판정' 어댑터 하나뿐. 통제변수 검사(모델 태그 하나·회차마다 다른 reset 세대).
- `tests/` — 순수부 테이블 테스트 + 합성 fixture end-to-end.

## 실행
```bash
cd scripts/eval
python3 -I -m unittest discover -s tests   # 테스트
python3 -I score.py <manifest.json>        # 채점 (레포 밖 결과물 디렉터리의 manifest)
```

manifest 모양(경로는 manifest 파일 기준 상대경로 가능, 결과물이라 커밋 금지):
```json
{"runs": [{"condition": "우리@35b", "engine": "ours", "model_tag": "<정확한 태그>",
           "reset_generation": "<회차마다 다름>", "repeat_index": 0,
           "records_path": "run0/records.json", "snapshot_path": "run0/snapshot.json",
           "elapsed_seconds": null, "total_requests": null, "blocked_attempts": null}]}
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
  그 전까지 `score.py`는 `engine: "baseline"` 회차를 **에러로 멈춘다**(A5 보류 — 0점 채점 금지).
- 판정 레코드 테이블명·마이그레이션은 verdict-record.md §5 Open.
