# 평가 스크립트 (뼈대)

baseline(원본 ARTEX) vs 개선 후(우리 포크)를 같은 조건으로 측정·집계한다.
측정 조건의 정본은 [평가 프로토콜](../../docs/evaluation/evaluation-protocol.md)이며, 여기는 이를 코드화하는 중인 집계 뼈대다.

**공식 평가 사용 전 보완 필요:** 현재 채점기는 records·snapshot의 회차 출처를 검증하지만, 새 정본의 pre/post 스냅샷·엔진 해결 귀속은 아직 구현하지 않았다. 해당 입력 검증·회귀 시험이 반영되기 전에는 기존 표를 새 프로토콜에 따른 공식 baseline/개선 비교에 사용하지 않는다. 기존 테스트 통과는 새 규칙 구현 완료의 증명이 아니다.

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
{"runs": [{"condition": "우리@35b", "engine": "ours", "run_id": "<회차 식별자>",
           "model_tag": "<정확한 태그>", "reset_generation": "<회차마다 다름>", "repeat_index": 0,
           "records_path": "run0/records.json", "snapshot_path": "run0/snapshot.json",
           "elapsed_seconds": null, "total_requests": null, "blocked_attempts": null}]}
```

채점용 `records.json`은 `run_id`·`model_tag`·`reset_generation`·`records` envelope다.
각 record의 `model_tag`·`reset_generation`도 manifest와 일치해야 한다.
채점용 `snapshot.json`은 `{"run_id": "...", "reset_generation": "...", "solved": {"<challenge key>": false}}` 형태다.
`solved` 안쪽만 프로토콜 §2의 정규화 맵이며, 두 파일의 회차 정보가 manifest와 다르면 표를 내지 않는다.

## 설정 (코드 하드코딩 금지 — .env)
- `EVAL_TARGET_ORIGIN` — 랩 대상 origin (예 `http://127.0.0.1:3000`). localhost 랩만 허용.
- `ARTEX_LLM_BASE_URL` — Ollama 엔드포인트.
- `EVAL_MODEL_TAG` — baseline·개선 후 **동일**해야 하는 정확한 모델 태그(§6).

## `parse_scan_output` — 판정 레코드 → 챌린지별 판정
입력은 [판정 레코드 계약](../../docs/contracts/verdict-record.md)의 **논리적 레코드**만 받는다.
- 단독 파서는 JSON 배열도 읽는다. `score.py` 채점 입력은 `{"run_id": "...", "model_tag": "...", "reset_generation": "...", "records": [...]}` envelope가 필수다. 세 값은 manifest와 일치해야 하며, 각 레코드의 `model_tag`·`reset_generation`도 일치해야 한다. 빈 배열도 envelope 검사를 통과해야 한다.
- `correlate(record) -> 챌린지 키 | None`를 **호출자가 주입**한다. 레코드엔 GT 키가 없고(절대규칙 7),
  (vuln_class·resource_ref)→챌린지 상관은 평가가 쥔다. None이면 out-of-list.
- 깨진 레코드·계약 밖 verdict/필드·민감값 누출은 전부 모아 `ScanOutputError`(fail-closed).
  undecidable→판단불가, allowed→ALLOWED(판단불가) — 오탐·취약점없음으로 바뀌지 않는다.
- 판정 우선순위와 레코드 없음 처리는 [평가 프로토콜 §4](../../docs/evaluation/evaluation-protocol.md#4-판정-기준)가 정본이다. ALLOWED와 레코드 없음을 구분하며, pre/post·귀속 선행 규칙까지 구현해야 정본을 준수한다.
  위반_후보(confirmed·suspected)와 allowed가 섞인 챌린지는 `conflicts`로 따로 남긴다(판정은 안 바뀜).

## 아직 막힌 경계 (TODO)
- pre/post 스냅샷·요청 종료 확인·내용 해시/시각 검증·엔진 해결 귀속과 사전 해결 처리 — 프로토콜 §2·§4의 필수 입력과 회귀 사례를 채점기에 반영해야 한다.
- 판정 레코드 **물리 형식**(DB 쿼리 vs JSON 덤프) — 이동찬 어댑터와 정렬. 어댑터만 바꾸면 됨.
- `correlate` 실 매핑 테이블(vuln_class·엔드포인트→챌린지) — 평가 전용 GT로 따로 둔다.
- TODO(윤재상): baseline(원본 ARTEX) 출력은 판정 레코드를 안 냄 — 별도 어댑터로 같은 레코드를 만들지 정렬.
  그 전까지 `score.py`는 `engine: "baseline"` 회차를 **에러로 멈춘다**(A5 보류 — 0점 채점 금지).
- 판정 레코드 테이블명·마이그레이션은 verdict-record.md §5 Open.
