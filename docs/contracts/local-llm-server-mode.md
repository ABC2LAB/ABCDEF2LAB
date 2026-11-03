# 서버 로컬 LLM provider 모드

상태: 운영자 pin manifest를 사용해 서버가 만드는 LLM provider를 로컬 전용으로 결속하는 첫 연결. 평가 회차의 적용 상태 필드·profile 지문 스키마, 정책 hot reload, 웹 검색·업데이트·custom tool 등 다른 네트워크 경로는 이 계약에 포함하지 않는다. **제품 전체의 외부 송신 차단 완료를 뜻하지 않는다.**

## 시작 설정

제품 바이너리 `cmd/artex`는 `ARTEX_LLM_POLICY_MODE`가 비어 있거나 `local-only`이면 로컬 LLM provider 모드로 시작한다. 이때 `ARTEX_LLM_PIN_MANIFEST`가 [운영자 파일 로더](./local-llm-manifest-file.md)의 신뢰·형식 검사를 통과해야 한다. 파일·권한·파싱 실패는 고정 오류로 시작을 중단하며 외부 provider로 전환하지 않는다. 평가 등에서 외부 API를 사용할 때는 운영자가 `ARTEX_LLM_POLICY_MODE=external`을 명시해야 한다. 다른 mode 값은 시작 오류다. 실제 운영 파일의 권한을 서버가 변경하지 않는다.

local-only에서 `ARTEX_LLM_*` 환경 설정만으로 provider를 활성화하지 않는다. pin된 DB active profile이 없으면 엔진은 idle이다. DB profile ID와 `agent.ConfigFrom`이 정규화한 tuple(끝 `/`·`/chat/completions` 제거 등)이 manifest와 일치해야 한다. 컨테이너의 서버가 root라면 manifest도 root 소유여야 한다. 시작 오류는 exit 1이며 `start.sh` 감독 루프는 계속 재시작하므로 운영자가 정책 파일을 고쳐야 한다.

manifest는 시작 시 한 번 읽어 프로세스 수명 동안 고정한다. 운영자가 파일을 교체하거나 제거한 뒤 적용하려면 서버를 재시작한다. 운영 중 재로드와 이전 provider 즉시 폐기는 아직 제공하지 않으므로 정책 변경 시 운영자가 이전 프로세스의 종료를 확인해야 한다. 기존 `server.New`는 내장 호출자의 legacy 생성자로 남고, 제품 바이너리는 로컬 모드에서 `NewWithLocalLLM`을 사용한다.

## provider 선택과 실패

DB에서 선택한 양의 profile ID와 실제 factory에 넘기는 `agent.Config`의 Format·Model·BaseURL을 **같은 snapshot**으로 [profile resolver](./local-llm-profile-policy.md)에 조회한다. 성공한 `Policy`와 그 Config로 `NewLocalProvider`를 만든다. 입력 원문과 기저 오류는 외부로 내보내지 않고 고정 로컬 profile 오류로 바꾼다.

로컬 모드에서는 명시적 task/profile/agent binding의 결속 실패를 다른 profile이나 전역 provider로 넘기지 않는다. 명시적 binding은 실패 provider로 닫고, 명시적 chat profile 실패는 chat agent를 반환하지 않는다. 전역 active profile은 같은 effective config가 검증될 때만 적용한다. 설정 변경에 실패하면 이전 전역 provider와 profile 캐시를 비운다. 자동 pool fallback은 로컬 모드에서 끈다. 기존 task의 명시적 할당량 체인 전환은 다음 profile도 각각 pin 검증을 통과해야 한다.

legacy `/api/llm` 설정 변경과 `/api/llm/test` 연결시험, `/api/llm/models` 모델 목록 조회는 로컬 모드에서 요청을 송신하지 않고 고정 실패를 반환한다. 연결시험은 아직 guarded provider를 받아 실행하도록 연결하지 않았으므로 성공 시험이라고 주장하지 않는다. `external` 모드의 기존 동작은 유지한다.

## 검증과 한계

합성 localhost policy·profile tuple의 성공/불일치, proxy 거부, fallback pool 제거, 실패 provider, legacy 연결시험/설정 API의 수신 0, 불신 manifest 시작 실패를 시험한다. 관련 server·agent 시험과 전체 Go build/vet를 확인한다. 실제 외부 모델 API·운영 manifest·대상 진단을 호출하지 않는다.

평가 회차의 local-only 적용 여부를 profile 지문·회차 기록에 결속하는 필드는 [기존 계약](./local-llm-profile-policy.md)의 담당자 합의 후 연결한다. 이 PR의 mode만으로 공식 평가 비교의 설정 근거가 완성되는 것은 아니다.

피드백 검증: 모델 목록 조회는 form key·저장 profile key, OpenAI·Anthropic 각각 legacy loopback 수신 사전조건을 확인한 뒤 local-only 수신 0을 검증한다. 실제 생성자에서 pin된 active의 성공, unpinned active·env-only의 idle, legacy 설정 403과 CLI mode 선택·정책 실패 시 fallback 0을 고정한다. 비차단 후속인 안전한 오류 범주/사유 코드·운영 모드 관측 API는 이번 수정에 포함하지 않는다. UI 담당자는 위 세 legacy API의 고정 실패와 pin 없는 profile의 invalid 상태를 통합 시 반영해야 한다.
