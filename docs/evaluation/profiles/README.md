# 공식 비교 선언

[평가 프로토콜 §5.1](../evaluation-protocol.md)에 따라 결과를 보기 전에 고정하는 비민감 선언만 둔다. 커밋 예외는 `official-*.json`과 그 근거 파일에만 해당한다. 계정·자원 원문, 결과, 회차 기록은 넣지 않는다.

| 파일 | 내용 |
|---|---|
| `official-35b.json` | 공식 비교 프로파일 v5(fixture·environment·scope). 예산(`time_budget_seconds`·`request_budget`)은 시범 회차 뒤 한 번만 조정한다 |
| `evidence/openrouter-endpoints-*.json` | 고정 제공자 slug가 엔드포인트 하나·정밀도 하나에만 매칭됐다는 조회 근거(조회 시각·원문 응답 SHA-256 포함) |
| `evidence/qwen3.6-35b-a3b-model-card-*.json` | 샘플링 고정값의 근거: 모델 공식 카드 커밋·README SHA-256·권장 샘플링 원문·고른 모드와 이유·고정 제공자의 파라미터 지원 |
| `evidence/openrouter-endpoint-params-*.json` | 고정 엔드포인트의 지원 파라미터 목록(게이트웨이가 `require_parameters:true`로 넣는 일곱 값이 모두 있어야 함) |
| `evidence/ollama-openai-sampling-*.json` | 로컬 서버의 OpenAI 호환 경로가 어떤 샘플링 값을 실제로 적용하는지 잰 결과. local 프로파일의 `sampling` 묶음 근거 |
| `evidence/observer-readonly-*.json` | 관측 front의 스코어보드 GET이 대상 해결 상태를 바꾸지 않는다는 근거(러너 `observer_readonly_ref`) |

지문 확인:

```bash
python3 -I scripts/eval/profile_fingerprint.py docs/evaluation/profiles/official-35b.json
```

baseline·개선 후의 사전검사 선언은 이 파일과 같은 값이어야 하며, 지문 세 개가 같아야 사전검사를 통과한다. 공식 회차 전에 이 디렉터리와 프로토콜을 담은 커밋에 태그를 붙인다. 실행 순서는 [공식 회차 실행 절차](../official-run.md)를 따른다.
