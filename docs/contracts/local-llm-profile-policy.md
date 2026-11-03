# 로컬 LLM profile 정책 입력과 resolver

상태: **입력 계약·명시적 순수 resolver 초안. 실제 profile 연결은 별도 후속이다.** 기존 DB profile·서버의 선택/캐시/fallback·provider 생성 경로는 바꾸지 않는다. 이 기능만으로 현재 앱의 LLM 요청이 자동으로 로컬 통제되는 것은 아니다.

## pin 설정의 출처

pin은 **운영자가 배포 시 관리하는 별도의 read-only 정책 manifest**에서 공급한다. DB의 사용자 편집 profile, task/conversation metadata, 모델 출력, 모델 URL, DNS 응답으로 pin 목록을 만들거나 보충하지 않는다. 운영자가 승인한 정책과 실제 선택된 profile의 입력을 서로 독립적으로 읽는다.

이 단계의 구현 경계는 신뢰된 host loader가 manifest를 검증해 전달한 `[]llmpolicy.ProfilePin`이다. 생성자는 원본 파일이나 호출자를 인증하지 않는다. manifest의 구체적인 파일 경로·JSON/env 파서·권한/링크/원자적 읽기·설정 재적용은 아직 구현하지 않았으며 실제 서버 연결과 함께 후속으로 둔다. 모델 요청에서 이 Go 타입을 조립할 수 있다는 사실이 운영자 출처를 증명하지 않는다.

manifest는 credential 저장소가 아니다. API key, key hint, 쿠키·헤더 원문을 아래 입력이나 오류/공유 로그로 복사하지 않는다. policy 등록과 registry 조회는 파일·DB·DNS·socket·환경변수·시계에 접근하지 않는다.

## profile 결속 범위

```go
type ProfileTarget struct {
    ID      int64
    Format  string
    Model   string
    BaseURL string
}

type ProfilePin struct {
    Target    ProfileTarget
    Addresses []netip.Addr
}

resolver, err := llmpolicy.NewProfileResolver(operatorPins)
policy, err := resolver.Resolve(effectiveProfileTarget)
```

이름·가까운 endpoint·등록 순서로 profile을 찾지 않는다. **양의 profile ID와 Format·Model·BaseURL 세 문자열의 정확한 일치**를 요구한다. `Addresses`는 운영자 등록 입력에만 있고 `Resolve`에는 없다. 조회 caller가 현재 DNS 결과를 pin으로 제안할 수 없다.

runtime target은 같은 host가 선택한 profile에서 **실제 provider에 전달할 유효한 `agent.Config`**의 Format·Model·BaseURL을 복사해 만든다. `ConfigFrom`의 기본값/정리 전 DB 원문과 실제 factory 설정을 서로 다른 시점에 섞지 않는다. resolver가 성공한 뒤 다른 config를 다시 읽어 생성자에 넘기지 않는다. 이 일관된 snapshot 읽기와 선택된 ID의 진위는 후속 host adapter의 책임이다.

현재 DB `LLMProfile`에는 profile별 monotonic revision이 없다. task의 `llm_chain_revision`은 failover chain용이고 `provCacheGen`은 프로세스 캐시 무효화 값이므로 이 결속의 profile revision으로 사용하지 않는다. 새 가짜 revision·DB 열을 추가하지 않고 **endpoint 정책을 선택하는 제한된 tuple**로 결속한다.

API key 교체·key hint·name·선택 우선순위·retry/rate/출력/추론 옵션 등은 이 tuple 밖이다. 이 resolver가 전체 profile revision, credential 변경, 같은 tuple로 되돌린 수정 이력 또는 불변 provider snapshot을 증명한다고 표현하지 않는다. credential은 기존의 비공개 경로로 전달하고 실제 factory가 검사한다.

## BaseURL과 origin 분리

BaseURL은 `/v1` 등 경로를 포함한 **원문 전체를 exact tuple 비교에 보존**한다. 경로를 잘라 profile을 비교하거나, host 대소문자·기본 port·trailing slash가 다른 문자열을 같은 tuple로 추정하지 않는다. 동일한 목적지로 보이더라도 tuple을 변경하면 운영자 binding을 다시 맞춰야 한다.

정책 생성에는 기존 `parseOrigin(baseURL, false)`가 반환한 **path 없는 canonical origin key**만 `llmpolicy.New`의 `Endpoint.Origin`으로 넘긴다. 예를 들어 BaseURL `http://model.internal:11434/v1`은 tuple에는 그대로 남고, origin 정책에는 `http://model.internal:11434`가 들어간다. 실제 provider는 후속 연결에서 원 BaseURL을 사용한다. 경로를 허용하지 않는 configuration-mode parser에 `/v1`을 직접 넘기지 않는다.

query 구분자 `?`는 빈 query를 포함해 거부한다. URL에 key를 싣는 구성을 이 입력에서 받지 않는다. fragment·userinfo·opaque URL·raw 공백/control/backslash·잘못된 escape/UTF-8 등은 기존 origin parser가 거부한다. URL의 `%3F` 같은 일반 escaped path 문자는 기존 candidate parser의 규칙을 따른다.

## 등록·조회 판정표

| 조건 | 결과 |
|---|---|
| nil/빈 등록 목록·64개 초과·중복 profile ID | nil registry + `ErrInvalidProfilePolicy` |
| 등록 target의 누락/잘못된 ID·문자열·URL | nil registry + `ErrInvalidProfilePolicy` |
| pin 누락·잘못된/공인/허용 범위 밖 주소·중복·literal origin과 불일치 | 기존 `llmpolicy.New` 검증으로 전체 등록 거부, `ErrInvalidProfilePolicy` |
| runtime target 누락·잘못된 ID/문자열/URL | nil policy + `ErrInvalidProfileTarget` |
| nil/zero resolver 또는 등록되지 않은 유효 profile ID | nil policy + `ErrProfilePolicyMissing` |
| 같은 ID지만 Format·Model·BaseURL 중 하나라도 다름 | nil policy + `ErrProfileTargetMismatch` |
| 정상 등록 ID와 tuple 전체 일치 | 해당 profile의 독립적인 `Policy` 값 반환 |

모든 오류는 입력 원문·underlying cause를 담지 않는 고정 sentinel이다. 실패 시 부분 registry/policy를 반환하지 않는다. 소문자 변환·trim·global active profile·다른 profile·legacy/external endpoint fallback을 이 resolver에서 하지 않는다. 서로 다른 ID가 같은 origin을 사용해도 되지만 각 profile의 pin 목록은 서로 독립적이다.

등록 수 상한은 64개다. Format은 1..64바이트, Model은 1..1024바이트이며 유효 UTF-8·앞뒤 공백 없음·Unicode control 없음을 요구한다. 모델 태그의 `:`, `/`, `.`, `-` 같은 punctuation은 허용한다. SDK가 지원하는 format 또는 실제 존재하는 모델인지 판정하는 기능은 아니다. BaseURL과 주소의 형식·허용 범위·개수 상한은 기존 [로컬 정책 코어](./local-llm-policy.md)를 재사용한다.

## 불변성과 후속 연결

등록은 target 값과 pin을 복사하고 주소 정규화·IP 검사는 기존 `llmpolicy.New`에 위임한다. 원본 slice·target·주소를 나중에 바꿔도 registry는 바뀌지 않는다. 조회마다 `Policy` 값을 복사해 반환하므로 caller가 반환 포인터를 다른 Policy로 교체해도 registry에는 영향이 없다. 비공개 policy map은 읽기 전용이며 동시 조회가 가능하다.

registry의 일반 문자열/Go 형식 출력은 결속 metadata를 가리고 JSON은 비공개 필드를 내보내지 않는다. 입력 DTO와 반환 policy 전체를 모델·공유 로그에 출력하는 API는 아니다. 안전한 오류 범주만 외부로 전달한다.

후속 host adapter는 다음을 함께 구현해야 한다.

- 신뢰 manifest의 일관된 읽기·출처 검증과 effective profile target/config snapshot을 확보한다.
- 성공한 policy와 **같은 config**를 [Config.NewLocalProvider](./local-llm-http.md)에 전달한다. Proxy·빈 BaseURL·SDK 지원·Host/TLS·매 요청/dial DNS/IP 검증은 기존 factory/HTTP 어댑터가 담당한다. 이 resolver에 그 검사를 중복 구현하지 않는다.
- resolver/factory 실패를 기존 `providerForProfile`의 bool 실패나 다른 profile/global/legacy 생성자 fallback으로 바꾸지 않는다. 각 명시적 profile 선택마다 그 profile의 policy를 새로 확인한다.
- 정책 변경은 새 registry를 만든다. 정책 제거·reload 실패 후 이전 provider/registry 캐시로 새 요청을 조용히 허용하지 않도록 무효화·적용 세대를 조율한다. 이 순수 객체가 이미 발급된 provider를 폐기하거나 진행 중인 요청을 되돌리지는 않는다.

DB schema·UI·profile 모드 선택·캐시/재로드·실제 provider 생성·서버 fallback은 이번 구현 범위에서 변경하지 않는다. DLP·평가용 외부 API 예외 역시 이 local-only resolver의 허용 근거로 사용하지 않는다.

## 검증 범위

합성 typed 입력으로 누락·중복·tuple 변조·URL/주소 오류·`/v1` 분리·profile별 pin 격리·원본 및 반환 값 교체·동시 조회·안전한 오류/출력을 시험한다. 긍정 대조는 반환 policy의 실제 `Decide`로 확인한다. 실제 manifest 파일 인증, DB profile 설정, server/provider 연결, 외부 모델 호출과 대상 진단을 실행한 결과는 아니다.

**확인한 결과:** 신규 resolver 최하위 사례 66개와 기존 HTTP/TLS를 포함한 `llmpolicy` 전체 238개가 race에서 통과했고 실패·skip은 0이다. 신규 resolver 함수의 statement coverage는 100%이며 관련 vet·전체 build·gofmt/공백·상대 링크 검사와 독립 코드 리뷰를 확인했다. 실제 파일 로더·profile/DB 연결·reload/fallback의 통합 시험은 포함하지 않는다.
