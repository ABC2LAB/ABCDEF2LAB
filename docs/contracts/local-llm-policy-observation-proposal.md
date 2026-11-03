# 로컬 LLM 정책 관측·평가 기록 연결 — 팀 리뷰안

상태: **계약 확정용 초안, 구현·팀 합의 전.** 이 문서의 신규 필드·API·지문 형식은 모두 제안이다. 기존 평가 입력에 지금 추가하면 안 된다. 이동찬이 서버 측 의미와 구현 가능성을, 최민준이 평가 입력·서명·비교 규칙을 확인한 뒤 소비자와 생산자를 함께 연결한다.

## 1. 근거와 담당 경계

- 검토 기준은 dev `f2c062bd596940027b184a683e69b414cf0cbb0c`의 [서버 모드](./local-llm-server-mode.md), [profile 결속](./local-llm-profile-policy.md), [manifest 로더](./local-llm-manifest-file.md)다. 로컬 모드는 검증한 manifest를 프로세스 수명 동안 고정하고, 실제 effective config를 pin에 결속해 provider를 만든다.
- 열린 [PR #126](https://github.com/ABC2LAB/ABCDEF2LAB/pull/126)의 검토 head는 `40d84dfdaaabfcf299ec6b64c4ffbc93168f8fd2`다. `engine.llm_policy_mode`는 `external`만 수용하고, 러너 결과의 `engine_llm_policy`는 `{"declared":"external","observed":null}`이다. baseline에는 이 모드 스위치가 없으며, LLM backend가 local이어도 게이트웨이를 쓰는 엔진 모드는 external이다.
- 현재 dev의 [평가 프로파일](../evaluation/profile-fingerprint.md)과 [러너 v1](../evaluation/run-record.md)·[v1.1](../evaluation/run-record-v1.1.md)은 닫힌 필드 집합을 사용한다. #126의 프로파일 v2도 별도 검토 중이다. 이 초안은 어느 버전의 파서·해시·공식 비교 규칙도 변경하지 않으며, #126 병합 여부에 의존하지 않는다.
- 변경 파일은 이 문서 하나다. Preview(#129), E③ 저장(#130), 예산 CLI·UID/소켓 활성화, 감사 CLI, task-mode 활성화, #126의 러너·망 격리·게이트웨이는 각각 기존 담당 범위다. 그 문서나 코드를 함께 수정하지 않는다.

## 2. 확정할 결정과 권장안

| 항목 | 권장안 | 확인 담당·확정 조건 |
|---|---|---|
| 의미 | backend 종류, 엔진 LLM 정책 모드, provider 준비 여부를 구별한다. `local-only`는 guarded LLM provider 경로의 의미다 | 이동찬·최민준: 제품 전체 망 격리·실제 모델 위치·응답 제공자를 뜻하지 않음을 확인 |
| 관측 출처 | 시작 시 선택을 보존한 서버 관측 API에서 수집한다. env 선언·nil resolver만으로 external 관측을 만들지 않는다 | 이동찬: 명시적 external과 legacy 생성자를 구별하는 시작 metadata 전달 |
| 정책 식별 | 같은 신뢰 로더가 읽어 검증한 **바이트**의 SHA-256을 반환 metadata로 보존한다 | 이동찬: 검증 후 파일 재열기 금지, 실패 시 부분 digest/관측 발행 금지 |
| 지문 위치 | 공통 `environment_fingerprint`를 유지하고 엔진별 정책 지문을 새 sidecar에 둔다 | 최민준: 서로 다른 엔진 기능을 공통 환경 동일성 조건과 섞지 않는 배치·버전 확인 |
| 회차 결속 | 러너가 pre/post 관측을 5개 회차 신원·서명된 descriptor와 결속한다 | 최민준: 새 기록 버전 및 기존 strict 파서 이행 계획 확인 |
| 관측 불가 | null/unknown을 보존한다. baseline 예외·공식 비교 적격성을 자동 승인하지 않는다 | 최민준: baseline 고정 SHA별 capability와 기존 프로토콜에 따른 처리 확인 |
| 적용 구간 | 두 snapshot과 프로세스 수명주기 근거를 구별한다 | 두 담당: 구간 근거 없이 회차 전체 적용을 주장하지 않는 검증 기준 확인 |

위 권장안 전체가 리뷰 대상이다. 담당자 이름은 합의 완료 표시가 아니다. HTTP 경로·version 식별자도 승인 전 예약하지 않는다.

## 3. 서버 관측 DTO v1 제안

후속 구현의 권장 경로는 `GET /api/llm/policy-observation?challenge=<nonce>`다. 기존 API 인증을 적용하고, 평가 러너가 소유한 인증값과 실행별로 고정한 서버 endpoint를 사용한다. 인증값·결과를 모델 입력이나 도구에 전달하지 않는다. URL/query의 입력은 nonce 하나뿐이며, policy·profile·경로·pin을 요청으로 주입하지 않는다. nonce는 러너가 관측마다 새로 생성하는 128-bit 난수의 소문자 hex 32자리다.

성공 객체는 아래 키만 갖는다. 모든 키가 필수이며 null은 표에서 허용한 곳만 받는다. 오류는 기존 인증 HTTP 상태 또는 고정 관측 실패이며, 실패를 유효한 unknown 성공 객체로 위장하지 않는다. nonce 형식 오류는 고정 400 응답이다. 응답 상한 4 KiB를 권장한다. 미지원 서버의 404는 러너가 관측 불가로 기록한다.

| 키 | 형식·의미 |
|---|---|
| `format` | 고정 `abc2lab-engine-llm-policy-observation-v1` |
| `challenge` | 요청 nonce 정확히 반영. 러너가 발급하지 않은 값·재사용은 거부 |
| `process_instance` | 서버 프로세스 시작마다 새로 만드는 128-bit 난수의 소문자 hex 32자리. 재시작·다른 서버와 재사용 금지 |
| `snapshot_sequence` | 프로세스 안 관측 순번. 1 이상 int64, 엄격 증가하며 소진 시 고정 오류로 닫음. profile revision이나 정책 세대가 아니며 기존 `provCacheGen`·task chain revision을 재사용하지 않음 |
| `mode` | `local-only`, `external`, `unknown` 중 하나 |
| `source` | local-only는 `default` 또는 `explicit`; external은 `explicit`; unknown은 `legacy`만 허용 |
| `scope` | 고정 `llm-provider` |
| `manifest_sha256` | local-only는 로더가 검증한 동일 바이트의 소문자 SHA-256 64자리. external·unknown은 null |

`unknown/legacy`는 시작 mode의 신뢰 metadata를 받지 못한 기존 내장 생성자만 나타낸다. `server.New`의 nil resolver를 보고 명시적 external로 추정하지 않는다. 로더 실패는 서버 시작 실패이므로 성공 DTO의 mode가 될 수 없다. local-only + 유효 manifest라도 pin된 active profile이 없으면 엔진은 idle일 수 있다. 이 DTO는 provider ready·요청 발생·모델 응답 성공을 나타내지 않는다.

관측은 메모리에 보존한 시작 metadata만 읽고 LLM·DNS·DB·파일 재읽기·reload를 수행하지 않는다. `manifest_sha256`은 내부 주소·profile tuple을 포함할 수 있는 **비공개 정책 자료의 commitment**다. 출력 대상은 인증된 host/러너와 비공개 회차 자료뿐이다. manifest 경로·원문·pin·API key·cookie·계정 원문·기저 오류와 credential의 해시는 반환하지 않는다. digest 자체는 파일 출처나 정책 승인·적정성을 증명하지 않으며 공개 보고서·Git·LLM 입력으로 복사하지 않는다.

## 4. 엔진별 정책 지문 제안

지문은 관측 DTO에서 아래 네 의미 필드만 추출한 **별도 profile**을 해시한다. challenge·process ID·순번·시각·run ID는 지문에 넣지 않는다. 이 profile은 API가 내려주는 임의 digest를 신뢰하지 않고 러너가 검증한 DTO로 재구성한다.

```json
{"format":"abc2lab-engine-llm-policy-profile-v1","mode":"external","source":"explicit","scope":"llm-provider","manifest_sha256":null}
```

정규화는 `sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False`에 해당하는 UTF-8 JSON, BOM·끝 줄바꿈 없음이다. 그 바이트에 SHA-256을 적용한 소문자 hex 64자리가 `engine_llm_policy_fingerprint`다. 허용 mode/source 조합·digest 형식·정확한 키 집합을 먼저 검증하고, unknown이면 지문도 null이다. 중복 JSON key·알 수 없는 필드·bool 순번·범위 초과·과대 응답은 거부한다.

위 external profile 예시의 고정 SHA-256 벡터는 `0b0b48146aa6ec40f86a41de8ff78a34cc21463ccd13ffb764eb52808eb48204`다. 이 벡터는 문서의 직렬화 규칙을 확인한 값이며 서버·러너 adapter가 구현됐다는 뜻은 아니다.

파일의 공백만 바뀌어도 manifest digest는 달라진다. 의미상 같아 보인다는 이유로 파일 바이트를 별도로 정리하지 않는다. 같은 manifest라도 effective Config의 ID/Format/Model/BaseURL 결속은 [기존 resolver 계약](./local-llm-profile-policy.md)이 별도로 집행한다. 이 지문은 전체 profile revision, credential·rate/retry·출력 옵션, 실제 선택된 모델·응답 제공자의 동일성을 보증하지 않는다.

공통 fixture/environment/scope 지문은 baseline·ours의 동일 모델·샘플링·대상·예산 등 기존 비교 조건에 사용한다. 엔진별 정책 지문은 `baseline`과 `ours` 각각 보존하고 서로 같은 값이어야 한다고 요구하지 않는다. backend/provider/fallback 선언·실제 제공자 검증 및 엔진에서 보는 게이트웨이 URL과 upstream URL의 구별은 평가 담당의 기존 경로를 유지한다. 정책 지문을 같게 맞추기 위해 local-only를 끄거나 조건을 바꾸지 않는다.

## 5. 러너 sidecar와 기록 버전 제안

새 비공개 artifact `engine_llm_policy`를 제안한다. [기존 descriptor](../evaluation/run-record.md)의 경로 제한·동일 바이트 해시/크기·중복 역할 방지·5개 신원 검증·서명을 그대로 적용하되, **그 키를 허용하는 새 기록 버전을 평가 담당이 확정한 뒤에만** 생산·소비한다. v1/v1.1의 모르는 필드를 무시하도록 완화하지 않고, 기존 기록에 키를 슬쩍 추가하지 않는다. 현재 #126의 결과 JSON에 기록하는 선언값은 이 서명 근거를 대신하지 않는다.

sidecar의 제안 필드 집합은 다음과 같다. 실제 자료는 레포 밖에 보관한다.

| 필드 | 제안 규칙 |
|---|---|
| `format` | 고정 `abc2lab-engine-llm-policy-evidence-v1` |
| `run_id`, `model_tag`, `reset_generation`, `instance_id`, `execution_id` | 서명된 러너 기록의 5개 신원과 정확히 일치. 서버에 이 신원이나 러너 서명키를 보내지 않음 |
| `engine` | 기존 기록의 엔진 식별자와 일치 |
| `declared_mode` | 실행 설정에서 읽은 `local-only`·`external`. 알 수 없으면 null. 빈 값의 default 선택은 host가 해석한 뒤 기록 |
| `coverage` | 고정 `snapshots`. 구간 적용 증명으로 읽지 않음 |
| `observations` | `phase: pre`와 `phase: post`인 정확히 두 행. 각각 러너의 `received: {at, sequence}`, 실행별 고정 endpoint에 연결한 `engine_process_ref`, `snapshot`(위 DTO 또는 null), `reason`(응답 수집·검증 성공이면 null, 실패면 아래 네 수집 코드) |
| `engine_llm_policy_fingerprint` | 두 유효 DTO의 profile이 같으면 계산한 digest. unknown·누락·불일치면 null |

pre는 대상 pre 수집 후 엔진 작업 시작 전에, post는 엔진의 신규 송신 중지·진행 요청 drain 후 대상 post 수집 전에 수행한다. `received`는 기존 timeline과 같은 러너 시계·순번 원장을 쓰는 `{at, sequence}`다. 시각은 timezone 포함·비감소, 순번은 `timeline.pre < observation.pre < timeline.engine_start`와 `timeline.drained < observation.post < timeline.post`를 만족한다. 순번은 0 이상 int64이며 bool을 받지 않는다. 같은 벽시각이어도 순번으로 구별하며 서버 자체 시각을 순서 근거로 쓰지 않는다. 각 nonce는 해당 수집 요청과 일치해야 하고, 두 관측의 `process_instance`·mode/source/digest가 같으며 서버 관측 순번도 증가해야 한다. `engine_process_ref`는 DTO가 자가 선언하는 ID가 아니라 러너가 고정한 실제 실행·컨테이너의 참조다.

행의 수집 실패 코드는 `unsupported`, `unavailable`, `invalid_response`, `challenge_mismatch`이며 이때 `snapshot`은 null이다. 형식이 유효한 unknown/legacy 응답은 snapshot을 보존하고 reason=null로 둔다. 두 행의 교차 검사에서 생기는 `process_changed`, `declaration_mismatch`, `policy_changed`는 검증기가 계산하는 실패 코드이며 행의 원래 응답을 덮어쓰지 않는다. 입력 원문·경로·URL·비밀은 코드에 붙이지 않는다. malformed/신원 불일치/서명·해시 실패를 미지원과 혼동하지 않고 해당 근거를 무효로 처리한다. 필수 근거 실패를 정상 관측이나 취약점 없음으로 축약하지 않는다.

최종 투영은 기존 결과의 `engine_llm_policy.declared` 의미를 유지하고, 새 근거를 검증한 경우에만 `observed`를 mode 문자열로 채우는 안이다. unknown·누락·실패에는 null을 유지한다. API 응답 객체나 fingerprint를 이 scalar 필드에 집어넣지 않는다. 새 sidecar의 위치·기록 버전·이 투영의 소비자는 최민준의 확인 후 연결한다.

## 6. 판정과 관측 한계

| 조건 | 권장 처리 |
|---|---|
| 선언과 유효한 두 관측이 일치 | mode snapshot 확인. 실행별 정책 지문·근거를 보존 |
| 선언 local-only / 관측 external 또는 반대 | mode 일치 실패. 관측값을 선언값으로 덮어쓰지 않음 |
| 파일 로드 실패 | 서버 시작 실패. external 재시작·fallback 근거로 쓰지 않음 |
| local backend + 관측 external | 모순 아님. gateway 뒤 로컬 모델일 수 있음. 전체 로컬 배포 완료로 분류하지 않음 |
| baseline API 미지원·legacy unknown·관측 누락 | `observed: null`. 고정 baseline SHA의 capability를 기록하고, 별도 합의 없이 external/local-only로 대체하거나 예외 적격을 발급하지 않음 |
| process/정책 변경 또는 연속 수명주기 근거 없음 | 두 snapshot을 회차 전체 정책 유지의 증명으로 사용하지 않음 |

서버 mode·manifest는 프로세스 내 불변이므로, 전체 실행에 적용됐다고 말하려면 러너가 pre부터 drain까지 동일 프로세스에 연결했고 교체·재시작·다른 listener가 없음을 별도로 확인해야 한다. pre/post 값 두 개가 같다는 사실만으로 중간 구간을 증명하지 않는다. 기존 망 격리 근거를 사용할 수 있는지는 평가 담당이 해당 자료의 적용 구간·관측 공백·프로세스 식별 범위를 검토한다.

이 계약의 mode 일치 판정은 [평가 프로토콜](../evaluation/evaluation-protocol.md)의 공식 비교 적격 판정을 대체하지 않는다. 격리·종료·귀속·제공자 고정·동일 조건·실측 요건은 그대로 별도 검사한다. baseline 예외나 기존 회차의 재분류는 이 문서 승인만으로 허용되지 않는다. 유효한 local-only 관측도 웹 검색·업데이트·custom tool의 외부 송신 차단이나 실제 로컬 모델 성능을 보증하지 않는다.

## 7. 후속 구현의 수용 기준 — 아직 미실행

| ID | 합성 시험·리뷰 기준 |
|---|---|
| LPO01 | 기본 local-only·명시 local-only·명시 external·legacy unknown의 허용 조합과 역조합 거부 |
| LPO02 | manifest 로드 성공의 동일 바이트 digest. 검증 후 원본 파일 교체에도 현재 프로세스 digest는 불변, 실패 시 관측 발행 없음 |
| LPO03 | pin된 active 성공과 idle을 같은 mode로 관측하되 provider 준비·모델 요청 성공으로 오표시하지 않음 |
| LPO04 | 관측 경로의 LLM/DNS/DB/파일 재읽기/reload 0회. 인증 실패·오류 원문 비노출 |
| LPO05 | 고정 canonical bytes와 Go/Python SHA-256 상호 일치. 순번/nonce/process 변화는 지문 불변, mode/source/manifest 변화는 분리 |
| LPO06 | 재사용 nonce·다른 프로세스·순번 역행·과대/중복/알 수 없는 필드·잘못된 타입을 해시·투영 전에 거부 |
| LPO07 | 5개 신원 각각 변조, descriptor 해시/크기·서명·경로·중복 역할 실패 거부 |
| LPO08 | 관측 없음/unknown은 null 유지. 선언값을 관측값으로 복사하는 변이를 검출 |
| LPO09 | local backend/external mode와 서로 다른 엔진별 정책 지문을 공통 환경 불일치로 오판하지 않음 |
| LPO10 | 기존 v1/v1.1 파서의 닫힌 schema 유지. 새 version의 생산자·소비자 동시 연결 전 기존 출력 변경 0 |
| LPO11 | pre/post는 같지만 중간 프로세스 교체·관측 공백이 있는 합성 자료로 전체 구간 적용 주장 거부 |

팀 리뷰에서는 §2의 결정표와 §3~6의 필드·신뢰 경계·이행 규칙을 확인한다. 승인 후 구현은 서버 관측, 평가 artifact/서명 adapter, 소비자/기존 회차 호환 순서로 담당을 나누고 각 단위의 검증을 새로 수행한다. 이 문서에 적힌 LPO 시험은 계획이며 실행 결과가 아니다.
