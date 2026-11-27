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

## 구조화 응답 근거 평가 (2026-11-27)

`scripts/eval/fixtures/selected_candidate_structured_gold.json`은 결과를 보기 전에 고정한 독립 합성 사례 20건이다(기존 16건과 별개, 기존 16건은 그대로 둔다). 근거가 주장을 지지·반박하는 사례, 근거 부족, 정상 동작 오인, 동적 정상 응답, 로그인 화면 복귀, 구조를 못 담은 응답, 입력·계정 불일치, 같은 구조 다른 의미, 오류 응답만 있는 사례, 위조 출처, 응답 속 프롬프트 인젝션, HEAD, 재현 404를 포함한다. 각 사례는 원본·재현·정상 대조의 합성 HTTP 응답(상태·Content-Type·헤더·본문)을 갖고, 하네스가 **실제 제품 코드**로 근거를 추출하고(`responseevidence.Extract`), 모델 입력을 만들고(`BuildCandidateFinalRequest`), 제품 판정을 낸다(`ParseCandidateFinalVerdict`). 정답·유형·규칙 표시는 모델 입력에 들어가지 않으며 하네스가 입력마다 확인한다.

비교 코호트(같은 사례·같은 모델·temperature 0):

| 코호트 | 뜻 |
|---|---|
| `legacy_model_advice` | #950 입력(구조 근거 없음)에 대한 모델 원판정 |
| `legacy_product` | #950 제품 판정(기존 문맥 검사만) |
| `rule_only` | 프로그램 규칙만: 헤더 누락 주장만 원본·재현의 헤더 사실로 판정, 나머지는 판단 불가 |
| `structured_model_advice` | 구조 근거·프로그램 비교를 넣은 입력에 대한 모델 원판정 |
| `structured_product` | 위 권고가 응답 근거 게이트를 거친 최종 제품 판정 |

지표: 올바른/잘못된 긍정·부정, 근거 부족 사례의 잘못된 결정적 권고, 판단 불가 수·비율, 판단한 사례의 정확도, 지지 사례 재현율·정밀도, 모델 호출 수·입력/출력 토큰·소요 시간(로컬 모드), 대상 송신 0(오프라인 평가), 비용 0(로컬).

```sh
# 기능 시험(모의 과신 모델, 무료·오프라인): 게이트가 막는 오판과 남기는 정당한 판정
ARTEX_SELECTED_EVAL_OUT=/path/out.json go test ./server -run TestSelectedCandidateStructuredEvalMockGate -v
# 실제 품질(사용자가 승인한 무료 로컬 모델을 loopback에 띄운 경우만)
ARTEX_SELECTED_EVAL_MODEL_URL=http://127.0.0.1:<port>/v1/chat/completions ARTEX_SELECTED_EVAL_MODEL=<model> \
  ARTEX_SELECTED_EVAL_OUT=/path/out.json go test ./server -run TestSelectedCandidateStructuredEvalLocalModel -v -timeout 2h
```

로컬 모드는 loopback `http`의 `/chat/completions`만 받고 프록시·리다이렉트를 쓰지 않는다. 위조 출처 사례는 모델 출력의 출처 하나를 하네스가 위조한 뒤 제품 판정에 넣는다(모델이 위조하게 만들 수 없으므로 제품 거부 경로를 재는 것).

**2026-11-27 기록.** 로컬 모델이 없어 실제 모델 품질은 **미측정**이다. 모의 과신 모델(모든 출처를 정확히 인용하고 늘 같은 결정적 판정) 기능 측정 결과(모델 품질 아님): 근거 부족 정답 10건 중 잘못된 결정적 제품 판정은 `legacy_product` 7건 → `structured_product` 2건(R14 오류 응답만·R16 응답 속 지시문 — 의미 판단이 필요해 프로그램이 막지 않는 사례). 지지 정답 5건과 반박 정답 5건은 각각 `structured_product`에서 모두 결정 판정이 유지됐다. 이 수치는 게이트의 차단·보존 범위이며 실제 LLM의 판정 정확도나 탐지 성능을 뜻하지 않는다.

## 판정 게이트 v2 평가 (2026-11-27)

v2 게이트(`selected/evidence-gate-v2`, [계약](../contracts/selected-candidate-response-evidence.md#판정-게이트-v2-selectedevidence-gate-v2-2026-11-27))를 같은 하네스에 코호트로 더했다. 기존 20건 정답은 바꾸지 않았다.

| 코호트 | 뜻 |
|---|---|
| `gate_v2_model_advice` | v2 입력(구조 근거 + 주장별 사실 검토 + v2 지시)에 대한 모델 원판정 |
| `gate_v2_product` | 위 권고가 v2 게이트(주장별 사실 충분성·재현 인용)를 거친 최종 제품 판정 |

새 회귀 사례 12건 `scripts/eval/fixtures/selected_candidate_gate_v2_gold.json`(G01~G12)은 v2 실행 전에 고정했다. 입력 대비로 지지되는 입력 처리(G01), SQL 오류 신호(G02), HTML·JSON 일반 오류만(G03·G06), HTML 속 지시문(G04), 지시문처럼 보이는 업무 데이터 + 운영자 진술(G05), 원본·정상 대조만 인용(G07, 하네스가 모델 출력에서 재현 인용을 뺌), 정상 대조가 있는 동적 정상 응답(G08), 쿠키 미관찰(G09), 무인증 설정 읽기(G10), 반박되는 입력 처리(G11), 노출 헤더(G12)를 담는다.

```sh
ARTEX_SELECTED_EVAL_OUT=/path/structured.json go test ./server -run TestSelectedCandidateStructuredEvalMockGate -v
ARTEX_SELECTED_EVAL_V2_OUT=/path/regression.json go test ./server -run TestSelectedCandidateGateV2RegressionEvalMock -v
```

**2026-11-27 기록(모의 과신 모델, 모델 품질 아님).** 근거 부족 사례의 잘못된 결정적 제품 판정:

| 사례 묶음 | 모의 모델 | `legacy_product` | `structured_product`(v1) | `gate_v2_product` |
|---|---|---|---|---|
| 기존 20건(근거 부족 10건) | 늘 확정 | 7 | 2 (R14·R16) | **0** |
| 기존 20건 | 늘 반박 | 7 | 2 (R14·R16) | 1 (R16) |
| 회귀 12건(근거 부족 5건) | 늘 확정 | 5 | 5 | **0** |
| 회귀 12건 | 늘 반박 | 5 | 5 | 1 (G04) |

정당한 판정 보존: 기존 지지 5건·반박 5건, 회귀 지지 5건(G01·G02·G05·G10·G12)·반박 2건(G08·G11)이 `gate_v2_product`에서 모두 결정 판정을 유지했다. 늘-확정 모의에서 정답 반박인 R05·R08의 잘못된 확정도 v2가 막았다(구체 신호·업무 진술 없음). 남은 2건(R16·G04)은 대상 응답 속 지시문에 끌린 **반박**이다. 데이터 노출의 반박은 업무 진술 없이도 가능해야 해서(예: R05 필드 부재) 프로그램이 막지 않는 의미 판단 몫이다. 실제 LLM 품질은 로컬 모델이 없어 **미측정**이다.
