# 평가 E② 제품 연결 준비와 남은 신뢰 경계

이 문서는 구현한 준비 검사와 아직 연결하지 않은 평가 배치를 구분한다.
[평가 정책 생산·소비 형식 확정 제안](../contracts/e2-evaluation-policy-format.md)은
기본 정책, 회차별 정책, 내용 지문의 입력과 검증을 따로 정의한다.
[e2-runner-control 제안](../contracts/e2-runner-control-proposal.md)이나
[e2-runner-integration 제안](../contracts/e2-runner-integration-proposal.md)을
공식 계약으로 승격하지 않는다. 담당 리뷰는 E② 이동찬과 평가 최민준이 함께 한다.

## 현재 실행 가능한 준비 검사

`e2config.Load`는 기존 Python `prepare_e2_config`가 생성하는
`abc2lab.e2-runner-config/v1`의 12개 필드를 그대로 소비한다.
별도 runner 설정 형식이나 task/intent 필드를 추가하지 않는다.

- 필수·미지·중복 필드, 잘못된 형식·타입·UTF-8·크기·참조를 거부한다.
- 정책 원문 상한은 로더와 제품 factory가 공유하는 256KiB이며, 일반 M01의 기존 상한도 유지한다.
- 비평가 전달 모드의 `evaluation: null`은 기존 생산자 계약대로 허용한다.
- `listen`과 정확한 `http://eval-meter-upstream:<port>`를 대조한다.
- 준비·제어·출력 디렉터리는 현재 UID 소유 0700이며 서로 중첩하지 않아야 한다.
- 설정·정책·32바이트 감사 키는 현재 UID 소유 0600 일반 파일이어야 한다.
  symlink, hardlink, 불안전한 조상 디렉터리, 파일/디렉터리 교체를 거부한다.
- `Check`와 보호 읽기 메서드는 최초 파일 identity와 정확한 digest를 재검사한다.
  호출자가 로드한 설정 객체의 공개 필드를 바꾸어도 거부한다.
- `ConfigSHA256`은 설정 파일의 정확한 바이트 SHA-256이다.
  `PolicySHA256`은 정책 파일의 정확한 바이트 SHA-256이며
  `e2export.PolicyFingerprint`의 정책 내용 지문과 다르다.

사전검사 바이너리:

```sh
go build -o /tmp/e2-runner-config-check ./cmd/e2-runner-config-check
/tmp/e2-runner-config-check --config /run/e2-config/e2-config.json
```

명령을 실행하는 환경에는 설정에 적힌 보호 경로가 실제로 있어야 한다.
검사 도중 키나 경로를 출력하거나 소켓·실행·감사 파일을 만들지 않는다.
성공 출력은 `state=inputs_validated`, `service_ready=false`,
`product_policy_validated=false`이다. 이 명령은 제품 서비스 entrypoint가 아니며
Compose의 `eval-e2` 명령으로 쓰지 않는다.

**이 명령과 `e2config.Load`만** 정책 원문에 수행하는 검사는 JSON 구조와
`approval_mode=preauthorized_scope`이다. 독립 backend는 이제
`server.ValidateE2HTTPPolicyDocument(raw, true)`로 정확한 필드·null·정적 상한을
검사한 뒤 기존 factory에서 보호 파일·권위·실제 집행 의미를 검증한다.
기본 정책은 `ValidateE2HTTPPolicyDocument(raw, false)`로 검사하며
`audit_key` 키가 있으면 거부한다.
사전검사 성공은 정책 수용·실제 task/intent 승인·대상 전송 허가가 아니다.
`Check`는 시점 검사이므로 소비자는 실제 자원 사용 직전에도 보호 읽기와 권위 검사를 유지해야 한다.

## 구현한 독립 backend 코어

`server.NewE2StandaloneBackend`는 기존 embedded runtime을 감싸지 않는다.
실제 `e2proxy.Execution`, `e2runner.Runner`, `e2proxy.Journal`,
`e2export.Publisher`의 수명을 직접 소유한다.

- 동결된 정책의 실제 감사 키 경로·digest, 전달 모드, 평가 선언과 내용 지문을 준비 설정과 대조한다.
- 독립 배치 readiness 제공자와 기존 정책 authority·실행 supervisor가 모두 필요하다.
  이 Go 인터페이스 자체는 인증 구현이 아니다. 원격 입력을 Execute에 바로 넘기면 안 된다.
- BindReset은 소문자 64자리 container ID를 최초 한 번 고정한다.
  같은 값은 멱등이며, 실패/불명 후에도 다른 값으로 바꾸지 않는다.
- private 평가 factory 경로만 동적 reset을 승인 scope·budget·실행에 적용한다.
  원문 정책 파일·파일 digest는 바꾸지 않고 일반 M01의 고정 reset 경로는 유지한다.
- Execute는 매번 실제 task/intent를 supervisor에 대조한다.
  도구 호출 수를 원자적으로 예약하며, CloseAdmission은 실제 Execution.Stop 이후에만 ACK한다.
- Finalize는 모든 등록 호출의 writer 종료와 기존 Publisher 검증을 거친다.
  실패한 게시나 close도 제한된 독립 정리 경로를 시도하며 complete로 바꾸지 않는다.

현재 command 단위 API만 있으므로 실제 Planner round, Worker turn·세션별 intent-run
제한은 아직 연결되지 않았다. 정책 지문 일치가 이 lifecycle 제한의 집행 완료를 뜻하지 않는다.
`Config.Listen/Upstream`의 실제 공유 ingress 배치도 아직 없다.

## 서비스 진입점

`cmd/e2-eval-service`가 `eval-e2`의 진입점이다. 이미지는 `deploy/eval/e2.Dockerfile`로 엔진 이미지 위에 만든다.
러너 제어 채널을 열고, `-engine-token-file`을 주면 엔진이 부를 실행 통로를 `listen` 주소에 연다.
토큰은 러너가 회차마다 만들어 `eval-e2`와 엔진에만 읽기 전용으로 mount한다. 토큰이 없으면 통로를 열지 않고
`AuthorizeExecution`을 전부 거절한다. `service_ready`는 설정과 정책 지문이 맞는다는 뜻뿐이고 실행 권한을 뜻하지 않는다.

Docker 기본 seccomp는 샌드박스가 쓰는 user namespace를 막는다(`unshare` EPERM, 11/10 실측). 그래서 `eval-e2`에는
샌드박스가 쓰는 플래그 조합만 여는 `deploy/eval/seccomp-e2.json`을 준다. 샌드박스가 호출마다 임시 루트를 `/tmp`에 만들므로
read-only 컨테이너에 `/tmp` tmpfs가 필요하다.

엔진 쪽은 `ARTEX_E2_REMOTE_URL`을 받으면 정책과 한도를 `eval-e2`에서 읽어 런타임을 만든다. 실행 자원은 `eval-e2`가 소유한다.
러너가 `eval-e2`를 닫으면 엔진이 이를 보고 task를 마감해 프롬프트 투영을 봉인한다.

**실측(2026-11-10, 합성 모델):** 실제 엔진·`eval-e2`·계측기·Juice Shop을 러너로 돌려 회차가 `complete`로 끝났다. GET 6건이 대상에 도달하고 POST 1건이 차단됐으며 기록되지 않은 도달은 0건이다. 프롬프트 투영은 3건 모두 수집됐고 서명 검증을 통과했다. 샌드박스 안에서 제어 소켓·설정·근거·토큰 경로는 보이지 않고, 대상 직접 연결과 엔진 통로 연결(loopback·컨테이너 IP)은 실패했다(rc=7). 샌드박스 안에서 namespace를 다시 만드는 것도 샌드박스 자체 seccomp가 거절한다(`unshare` EPERM, 컨테이너 프로필이 허용하는 플래그 조합 포함). 합성 모델이라 탐지 성능은 재지 않았다.

**공식 정책 모양 실측(2026-11-10, 스크립트 모델):** 메서드 7개·전 경로·`evaluation_caller_passthrough`로 3회차 돌렸고 마지막 회차는 완료·서명됐다. 가입 POST 201, 로그인 토큰을 받아 `Authorization: Bearer`는 토큰 없이 401·있으면 200, `Cookie`는 본문의 사용자 정보까지 갈렸다. HEAD·OPTIONS·PUT·PATCH도 대상까지 갔다. 계측기 33건이 전부 E② 허용 기록과 맞았고 기록 없는 도달과 차단 뒤 도달은 0이다. `curl -X DELETE`는 원본에도 있는 기본 안전 규칙이 E② 앞에서 막아 DELETE는 대상까지 가지 않았다. `Accept-Encoding: gzip`은 502(unsupported)였고, 같은 명령을 다시 보내면 실행되지 않았다.

합성 수용시험은 실제 자식 프로세스의 backend와 UDS를 사용한다.
부모가 ready → bind → head → close → close_status를 보내며,
실제 Linux 격리 Runner와 e2proxy에서 허용 요청 1건, 범위 밖 요청 0건,
proxy를 지운 직접 대상 연결 0건을 확인한다.
대상이 받은 attempt 헤더를 trusted 관측·감사와 대조하고, 동적 reset이
감사·manifest에 유지되는지 확인한 뒤 실제 `e2-audit-verify` 명령을 실행한다.

이 시험의 authority·task/intent·UID 구성은 명시적인 합성 fixture다.
실제 엔진/Planner나 실제 eval-meter·Docker Compose 회차 수용을 대신하지 않는다.
생산 서비스 이미지·진입점은 연결하지 않으며 기존 Compose 후보 명령도 그대로 둔다.

## 기존 흐름과 변경하지 않는 경계

현재 러너의 product 분기는 이미 ready → bind → task 생성 → close → collect를 수행한다.
가짜 제어 서버 시험이나 stub 결과를 제품 수용으로 바꾸지 않는다.
`StartE2ControlService`의 embedded 엔진 거부와 일반 M01의 고정 ResetRef 검사는 유지한다.

러너는 Docker 관측으로 대상 ID를 얻으며 pre/post 경계와 구간 이벤트를 비교한다.
E②에는 Docker socket을 주지 않는다. E②의 문자열 bind만으로 컨테이너 재생성 감지까지
완료했다고 주장하지 않는다.

## 실제 배치 전에 남은 연결

1. **제품 정책 생산자.** Python 준비기는 회차마다 새 감사 키를 만들고 정책의 `audit_key` pin을 채운다.
   기존 `server.E2HTTPFilePolicy`는 CA 인증서/키, helper, state 경로, 승인 수명,
   IP pin, 감사 키 digest 등을 요구한다. 이 자원을 실제로 준비하고 새 키와 정책 pin을
   일치시키는 경계가 필요하다. 동결 정책을 bind 시점에 덮어쓰지 않는다.
2. **실제 task/intent 생산자.** 공개 양수 ID나 proxy 요청은 승인이 아니다.
   실제 Manager task와 Planner 생성 intent를 기존 DB 소유·상태·payload 검증에 묶고,
   reservation이 다음 실제 task 하나에만 소비되는 기동 전 경계를 구현해야 한다.
3. **신뢰 호출 채널.** mTLS OpenCall/CloseCall은 제안 상태다.
   launcher identity, 호출별 데이터 ingress, 재사용·종료·결과 불명 처리와
   도구/자식 프로세스의 자격 접근 차단을 함께 확정·구현해야 한다.
   단순 raw 명령 RPC나 HTTP_PROXY만으로 대체하지 않는다.
4. **제품 이미지·Compose 수용.** 실제 서비스 바이너리와 검증된 이미지 digest,
   UID/mount/network 분리, meter 양성 대조와 직접 우회 음성 시험을 갖춘 뒤 연결한다.
5. **마감 근거.** 기존 e2export의 실제 Runner/Execution 종료·trusted 결과 관찰·
   감사 검증을 보존한다. 엔진의 자체 보고만으로 complete manifest를 만들지 않는다.

이 연결이 없으면 `e2.ours_mode=product`의 실제 회차와 실제 Planner 수용은 NOT_RUN이다.
설정 검사나 backend 단위 시험을 이 회차의 성공으로 표시하지 않는다.

## 담당자에게 확인할 좁은 접점

- 정책 파일의 구체 타입과 CA/helper/감사 키 pin 준비 책임
- launcher↔E②의 인증 identity 및 호출별 데이터면/마감 증명
- 기존 task POST 본문을 유지하는 사전 reservation 소비 위치
- 제품 이미지·별도 프로세스 배치와 공식 수용시험의 담당

기존 러너의 공식 모델·GT·샘플링·반복·판정·signed evidence 규칙은 바꾸지 않는다.
