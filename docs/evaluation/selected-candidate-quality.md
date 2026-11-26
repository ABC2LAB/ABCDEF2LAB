# 선택 후보 판정 품질 비교

`scripts/eval/fixtures/selected_candidate_gold.json`은 결과를 보기 전에 고정한 합성 정답 16건이다. 앞의 7건은 #925에서 사용한 HTML 헤더 관련 독립 근거·정상 대조·근거 부족 분류를 회귀 입력으로 재구성했다. 뒤의 9건은 인증, 잘림, 업무 규칙, 복구, 위조 출처를 포함한다. 대상 원문이나 실제 자격정보는 없다.

`python3 -I scripts/eval/test_selected_candidate_quality.py`는 입력 투영, 정답 누출 방지, 응답 파싱, 범위 제한을 시험한다. 모의 권고 비교는 다음 명령으로 재현한다.

```sh
python3 -I scripts/eval/selected_candidate_quality.py \
  --mock-responses scripts/eval/fixtures/selected_candidate_mock.json
```

모의 권고는 모델 품질 측정이 아니다. 실제 모델 평가는 사용자가 승인한 무료 로컬 OpenAI 호환 모델을 loopback 주소에 띄운 뒤 `--local-url http://127.0.0.1:<port>/v1/chat/completions --model <model>`을 사용한다. 러너는 외부 호스트·프록시·리다이렉트를 거부한다. `--backend-results`와 `--human-results`는 별도 측정에서 만든, 사례 ID 전체를 포함하는 JSON 맵만 받는다. 사람 검토 시간과 제품 대상 송신 수는 별도 기록이 없으면 `null`로 남긴다. 모델 입력에는 정답·규칙 판정·`comparable` 플래그가 들어가지 않는다.

집계는 올바른 긍정·부정, 잘못된 긍정·부정, 근거 부족 사례의 잘못된 결정적 권고, 사람 판단이 남은 건수를 분리한다. `rule_only`는 그 규칙이 지원하지 않는 유형을 `insufficient`로 센다. `model_advice`는 **권고**이며 제품의 감사·봉인·출처·인증 비교 검사 뒤 저장된 판정과 다르다. 제품 경로의 판정은 실제 API 결과를 `--backend-results`로 제공해야 비교된다. 모의 모델과 제품 API를 시험한 사실을 유료 또는 실제 LLM 품질의 증거로 기록하지 않는다.

#925의 별도 과거 실측은 고정 HTML 헤더 합성 사례 7건에서 규칙 7건 일치, 당시 실제 Qwen 권고 2건 일치, 근거 부족 5건 모두 잘못된 확정 권고였다. 해당 기록은 [기존 계약](../contracts/passive-candidate-verification.md)에 보존한다. 현재 16건의 새 입력에 그 수치를 전이하지 않는다. 이번 작업에서는 유료 OpenRouter 호출을 하지 않는다.
