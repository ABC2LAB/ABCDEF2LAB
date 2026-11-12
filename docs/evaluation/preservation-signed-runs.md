# 탐색 입구와 서명된 보존 원자료

이 문서는 개발·브라우저 수용시험의 재계산 경로를 설명한다. 기준은 [브라우저 수용시험 §6·§7](browser-acceptance.md#6-3단계-보존-시험)이며 공식 비교의 분모·귀속·v1.1 기록 계약은 바꾸지 않는다. 고객 회차나 제품 보고서에 원본 ARTEX 비교를 넣지 않는다.

## 현재 구현과 결과

- 계측기는 실제 리슨에 따라 요청 원자료와 경로 행에 `ingress=main|explore`를 남긴다. `main`은 원본 직접 입구 또는 E② 진단 입구, `explore`는 탐색 경계 뒤 입구다. 호출자의 헤더로 입구를 선택하지 않는다. 봉인 뒤 거부 행도 입구를 남긴다.
- 전체 요청 예산·도달 경로는 기존대로 두 입구를 합산한다. `meter-summary.json.forwarded_by_ingress`는 전체 전달 행의 입구별 수, 보존 요약 `ingress`는 요청 예산 안 전달 행의 입구별 수다. 봉인 뒤 거부는 전달 수에서 빠진다.
- 실제 탐색 분리 배치에서 E② 시도별 대조는 `main`만 받는다. 현행 공식·원본 직접·`e2-legacy/v1` 배치에 `forwarded_by_ingress.explore > 0`이 있으면 배치 위반으로 회차를 무효화하고 대조에서 조용히 빼지 않는다. `explore`의 AttemptID 부재는 정상이다. 탐색 입구에서 E② ID가 보이거나 입구가 미등록·혼합되면 추정으로 고치지 않고 거부한다. 입구 칸이 없던 과거 기록은 기존 main 의미로 읽되 새 서명 계약에는 쓰지 않는다.
- 원자료 서명·변조·조건 혼입·기존 판정 경계 시험은 합성 fixture로 실행한다. 합성 자료의 `execution_source=synthetic_fixture`는 실측으로 승격하지 않는다.
- 현행 Docker 평가 러너는 개선 후 배치를 `e2-legacy/v1`로 발행한다. 설정에 새 배치 이름을 적어 우회할 수 없다. 새 실행기는 실제 배치·망·허용 도구·마감 검증 뒤 공통 발행 함수를 사용할 수 있다.
- 검증된 제품 탐색 경계 exporter는 아직 없다. `signed_evidence.exploration_boundary`는 `NOT_RUN / verified_boundary_export_unavailable`이다. 탐색 차단 건수를 0으로 채우거나 E② 진단 차단·`repeat_denials`와 합치지 않는다. 실제 MCP fixture 연결도 제품 경계 검증이나 원본 모델 실측을 대신하지 않는다.

실제 모델·원본 엔진·승인 대상·제품 탐색 경계 근거를 갖춘 비교는 **NOT_RUN**이다. 서명은 발행자의 출처와 바이트 무결성 근거이며, 실제 모델 실행이나 네트워크 격리를 증명하는 대체물이 아니다.

## 별도 sidecar

`purpose=browser_acceptance` 설정에 선택 필드 `preservation`을 넣으면 `preservation-round.json`을 발행한다. 공식 설정에서는 거부하고, 필드를 생략하면 기존 동작이다. 형식은 `abc2lab.preservation-round/v1`이다. `run-record.json`의 v1.1·기존 HMAC 방식과 공식 채점기는 그대로다.

```json
"preservation": {
  "format": "abc2lab.preservation-options/v1",
  "series_ref": "fixture-session-01",
  "comm_mode": "full_local",
  "write_mode": "approved_writes",
  "mcp_version": "0.0.84",
  "mcp_tools_sha256": "<허용 도구 목록의 64 hex 지문>",
  "browser_image_id": "sha256:<실제 브라우저 이미지 64 hex>",
  "exploration_policy_sha256": "<적용할 탐색 정책 64 hex>",
  "declared_model_budget_sha256": "<호출·토큰·USD 예산 선언의 64 hex>",
  "execution_source": "model_run"
}
```

위 예의 지문 표시는 실제 값으로 교체해야 한다. API 키·세션·비밀번호·원문 URL은 받지 않는다. `comm_mode`는 `full_local|external_allowed`, `write_mode`는 `read_only|approved_writes`, `execution_source`는 `synthetic_fixture|model_run`만 받는다. 모델 비용 예산의 지문은 선언의 결속이며 이 옵션 자체가 예산이나 망을 집행하지 않는다. 실제 한도 집행·전송 동의·제공자 검증은 기존 실행 경로의 책임이다.

발행 자료에는 회차·짝 순번·세션 참조, 엔진 커밋/이미지, 모델·제공자·샘플링 핀 지문, 대상 이미지/초기 상태 지문, 머신 지문, 러너의 폴링/마감/작업/worker/컨텍스트 조건 지문, 요청/시간 예산, 통신/쓰기 모드, MCP 버전/도구 지문/브라우저 이미지, 탐색/E② 정책 지문, 모델 예산 선언 지문, 종료 사유를 남긴다. 원본의 두 정책 지문은 `null`이다. 원본·개선 후의 엔진 커밋/이미지는 달라도 되지만 각 조건 안에서는 고정해야 한다. R-R은 계약대로 R-F 원본과 짝을 지으므로 원본의 쓰기 모드와 개선 후 읽기 모드는 다를 수 있다.

원자료는 고정 파일 네 개(`engine-requests.jsonl`, `engine-paths.jsonl`, `engine-sealed.json`, `engine-drained.json`)의 정확한 바이트 SHA-256과 크기로 결속한다. 발행·읽기 모두 봉인/drain·순번·전달 수·경로/입구 대조를 확인한다. 러너는 post 단계에 공개 발행 조건만 보관하고, 기존 격리·모델·마감 검증과 `down` 성공 뒤에 원자료를 다시 읽어 최종 바이트의 hash를 계산한 다음 sidecar를 서명하며, 결과의 서명된 근거 목록에도 넣는다. 봉인·drain 뒤부터 `down`까지 생긴 `rejected_after_seal` 행도 최종 hash에 포함된다. 이 거부는 전달 수·도달 패턴을 늘리지 않으며, 기존 meter 요약은 측정 마감 시점의 관측값이다. 개인 근거 디렉터리(러너 소유·0700), 경로 가림 키와 별도 러너 키(근거 밖 보호 파일)를 유지한다.

새 배치 실행기 연동점은 `preservation_record.build_payload(...)`이다. `conditions.layout=exploration-split/v1`은 실제 검증한 호출자만 전달한다. 이 함수는 writer가 종료된 뒤 호출해야 하며 배치를 만들거나 브라우저를 켜지 않는다. `layout` 이름과 입구 대조만으로 실제 네트워크 격리를 증명하지 않는다. 제품 경계 exporter 계약이 확정되면 호스트가 검증한 경계 원자료의 hash·카운터를 별도 공급하는 확장이 필요하다. 현재 API에는 임의 경계 정본 형식을 넣지 않는다.

## 재계산

회차 순서와 정책·제외 규칙을 먼저 고정한다. 기존 `--baseline`, `--improved`에 같은 순서의 sidecar 디렉터리, 보호 러너 키, 프로파일을 함께 지정한다. 실제 경로는 저장소 밖 운영자 근거 위치다.

```bash
python3 -I -B scripts/eval/preservation.py \
  --baseline /private/eval/b1/meter --improved /private/eval/o1/meter \
  --baseline-record-dir /private/eval/b1 --improved-record-dir /private/eval/o1 \
  --budget 100 --profile R-P \
  --exclusions /private/eval/exclusions.json \
  --path-key-file /private/eval-keys/path.key \
  --runner-key-file /private/eval-keys/runner.key
```

반복 회차는 각 `--baseline`/`--improved`와 `--*-record-dir`를 같은 짝 순서로 반복한다. 공개 대상·유료 모델 호출 없이 검증하려면 다음 합성 시험을 실행한다. 원자료나 runtime 로그를 터미널에 출력할 필요가 없다.

```bash
python3 -I -B -m unittest discover -s scripts/eval/tests -p test_preservation_record.py
python3 -I -B -m unittest discover -s scripts/eval/tests -p test_runner_evidence.py
go test -race ./cmd/eval-meter ./pathmask
```

새 인자 중 하나를 쓰면 모두 필수다. 검증기는 서명, 정확한 JSON 구조, 원자료 바이트, 마감 상태, 경로 키, 짝 순서/회차 중복, 세션, 각 조건의 엔진 핀, 모델·머신·초기 상태·예산·통신 모드·MCP 조건, 조건 안의 배치·정책 고정을 확인한다. 다른 조건이 섞이면 종료 코드 2이며 요약은 출력하지 않는다. 서명은 소문자 64 hex만 받고 비 ASCII·다른 타입도 traceback 없이 고정 validation 오류로 거부한다. 원본/개선 후 사이에서 달라야 하는 배치·정책과 R-R의 쓰기 모드는 각 조건 안에서 따로 고정한다.

서명 검증을 켠 출력만 `abc2lab.preservation-summary/v7`이고 기존 지표에 `signed_evidence`, `assessment`가 추가된다. 기존 원자료 전용 CLI는 `v6` 그대로다. v6는 새 서명/조건 검증 완료로 읽지 않는다. 검증된 경계 export가 없는 현재 v7의 완료 판정도 `NOT_RUN`이다.

## 기존 기준과 남은 검증

분자는 실제 `sent=yes` 도달 패턴, 분모는 같은 키로 변환한 검토된 제외 규칙을 적용한 원본 패턴이다. `sent=no|uncertain`, 정적 자원 비용, 합집합/짝별 평균/SD·원본끼리 참고값·빈 분모 처리 규칙은 기존 집계기를 그대로 쓴다. 새 지표나 통과선은 만들지 않는다.

- R-P는 100회 관찰·보고이며 완료 기준으로 쓰지 않는다. 기존 0.90과의 참고 비교를 유지한다.
- R-F는 짝별 평균 0.90 이상, R-R은 GET/HEAD 분모의 짝별 평균 0.95 이상이다. 각 비교의 빈 분모를 뺀 유효 짝이 3개 미만이면 `UNDECIDABLE`이다. 합집합 비율로 대신 판정하지 않는다.
- R-F·R-R은 같은 세션의 **첫 complete·time_budget 원본 예비 회차** N0의 2배를 요청 예산으로 고정한다. `full_budget_from_pilots`는 이미 검증한 실제 `abc2lab-eval-runner-result-v1` 결과를 시간순으로 받는다. `meter.stop_reason`·`meter.forwarded_requests`에서 처음 유효한 값을 고르고, 무효 회차는 `discarded_pilots`에 `reasons`·`failed_step`·당시 stop_reason을 보존한다. 무효를 `time_budget` 폐기만으로 축약하지 않는다. 이 helper의 현재 호출처는 합성 회귀 시험뿐이며 러너·CLI에 자동 예비 회차 실행/수집 연결은 없다. 원자료/서명 검증이나 예비 회차 이력의 완결성을 대신하지 않는다. 예비 회차는 본 짝에 넣지 않는다. 이후 좋은 N0를 고르지 않는 규칙을 회귀 시험으로 고정한다.
- `series_ref`는 운영자의 세션 선언이며 자체로 실제 시간순 실행·세션 완결성을 증명하지 않는다. 실제 세션의 `B O O′ | O′ O B | B O O′` 순서, 예비 회차 이력, 정책 상한과 모델 비용 집행, 최초 모델 핀 재조회, 실제 격리·도구 동등성·원본 실행은 운영자/수용 실행기의 근거가 필요하다. sidecar의 조건 일치만으로 이를 완료 처리하지 않는다.

현재 시험은 합성 계산·변조 차단·입구 대조의 구현 회귀다. 실제 Qwen·원본 보존율 R-P/R-F/R-R 및 제품 경계 종단은 해당 환경과 별도 승인이 준비된 뒤 실행하며, 그전에는 `NOT_RUN`을 유지한다.
