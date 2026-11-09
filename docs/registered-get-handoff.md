# 등록 GET 준비 자료 인계와 통신 없는 검사

경준에게 요청할 자료와 이동찬의 연결·검증 범위를 정리한다. 기존
[carrier 기준](contracts/kg-canary-carrier-stage1.md)·[레지스트리 계약](contracts/kg-fixture-registry.md)·
[첫 진단 기동](first-diagnosis-launch.md)을 사용하며 새로운 KG 형식을 만들지 않는다.
시작은 기존 CLI로 진행한다. 이 준비 검사는 웹 UI·모델 서버·DB 연결을 요구하지 않는다.

## 경준에게 요청할 자료

공유 회신에는 필드명·참조 라벨·구조와 준비 상태만 적는다. 실제 표식값·계정 원문·세션 헤더·키·
비밀번호는 저장소·PR·메신저에 넣지 않고 실행 UID 소유의 보호 파일로 인계한다.

| 요청 항목 | 필요한 내용 | 담당·확인 경계 |
|---|---|---|
| 최종 carrier 결정 | type과 선정 근거, C1~C6 결과. 기존 사전 순서 `review → feedback → export → user`와 address 추가의 정렬 근거 | 이경준이 내용·순서를 확정. 교차 성공이나 GT로 후보를 고르지 않음 |
| 새 대상·세대 | 실제 origin·고정 접속 IP, instance/reset·generation 참조, KG 준비 `run_id`와 snapshot ID | 폐기된 실측 인스턴스의 ID·세션·승인을 새 회차로 복사하지 않음 |
| 같은 객체의 등록 GET | 전체 `resource_ref`, 하나의 고정 자원 URL, owner/cross 계정 라벨, 읽기 승인 참조, 조회 비변경 확인·허용 기간 | 이동찬이 설정으로 등록. GET이라는 이유만으로 `ReadOnly=true`를 추정하지 않음 |
| 정상·오류 응답 규칙 | Content-Type, 문자열 상태의 `StatusPath`·`NormalValue`·오류값, 객체의 `DataPath`, 그 객체의 직속 표식 필드명 | 구조만 공유. 비민감 표본도 실제 값은 보호 자료로 인계. live 응답 수용은 별도 |
| A/B 계정·세션 참조 | 서로 다른 owner/evaluated 라벨·handle, 검증 상태·만료, 보호 credential 파일의 이름·승인된 위치 | 실제 헤더값은 별도 0600 파일. 이름·참조만 snapshot에 기록 |
| KG 레지스트리 | 기존 `abc2lab.kg-fixture-registry/v1` 전체 자료, owner 연결·평가 주체별 정책·유효기간·canary 집합 | 같은 세대 value 전역 유일성과 원자 등록은 KG 책임. 목록 사전 스캔 0건이 이를 대체하지 않음 |
| 표식 준비 근거 | 필드별 비자명 표식, 앱의 준비 쓰기와 별도 owner GET, 반사 배제·정확한 전체 값 일치 | 준비 쓰기는 조회 실행과 분리. 여러 필드에 같은 값을 다른 등록 키로 재사용하지 않음 |
| 실행 전 metadata | 기존 `abc2lab.first-controlled-read-inputs/v1`, 실제 paused task/intent·exploration 참조, expected owner/evaluated·resource·정책 버전, KG digest·세대·인스턴스·CanaryFields | 이동찬이 E② snapshot과 대조. collector·판정·완료 사실을 미리 채우지 않음 |
| 설치·시작 승인 자료 | snapshot이 참조하는 감사 키·credential 파일, M01 host binding·앵커·제한된 runtime/authority DB 역할, 모델/JWT 설치와 승인 기간 | 준비 파일 인계와 설치 권위의 발급은 구분. 실제 실행은 기존 기동 검사를 모두 통과해야 함 |

아직 없는 항목은 `MISSING`, 자료는 있으나 실제 확인 전이면 `NOT_VERIFIED`로 회신한다.
교차 GET의 성공/실패나 취약점 확정은 carrier 선정 자료에 요구하지 않는다.
등록·pairing 확인 후 자료를 고정하고, 최민준이 판정과 평가 GT 상관을 별도로 수행한다.

## 응답 객체 경로와 표식 필드

`DataPath`는 표식 문자열 자체가 아니라 **비어 있지 않은 객체**를 가리킨다.
표식은 그 객체의 직속 문자열 필드에서 찾고 전체 값이 정확히 일치해야 한다.
`StatusPath`도 실제 응답의 문자열 값으로 확인한다. HTTP 200은 정상 구조나 setup-success의 증거가 아니다.

address 회신의 구조가 실제 새 회차에서도 확인되면 `DataPath=["data"]`, `Markers[].Field="fullName"`으로
설정할 수 있다. `data.fullName` 전체를 객체 경로로 넣거나 `data[]` 배열을 객체로 취급하지 않는다.
이 필드·URL·type은 엔진 기본값이 아니며 다른 자원은 다른 경로·필드로 설정한다.

E② snapshot의 `view`는 기존 `transmit.JSONViewRule` 형식의 `StatusPath`, `NormalValue`,
`ErrorValues`, `DataPath`, `Markers`를 사용한다. 레지스트리 `canaries[].field/value/trivial`과
E② `Markers[].Field/Value/Trivial`은 같은 준비의 정확히 같은 집합이어야 한다.
KG의 `run_id`는 준비 회차 식별자다. 이를 실행 전 metadata의 빈 `Expected.Scope.RunRef`에 복사하지 않는다.
실제 E② 실행 run 참조는 기존 기동·등록 경로가 만든다.

## 준비 파일 검사

기존 snapshot·registry·metadata와 snapshot이 참조하는 감사 키·credential 보호 파일이 있으면 다음을 실행한다.
경로는 설치 예시이며 프로그램이 파일·키·승인·실제 task를 만들어 채우지 않는다.

```sh
go run ./cmd/first-controlled-diagnosis check-registered-get \
  -metadata /protected/registered/metadata.json \
  -snapshot-dir /protected/registered/snapshots \
  -registry registry.json
```

`-registry`는 snapshot 디렉터리 안의 파일명이다. 경로 이동·symlink·다른 권한 파일·자료 누락·
중복 JSON·참조/세대/표식 불일치는 성공 보고서 없이 종료 1, 인자 오류는 종료 2다.
snapshot 파일명은 metadata의 검증된 `snapshot_ref`로 정한다.

성공 출력은 다음과 같다. 세션 인증·실제 응답·판정 수용은 완료로 표시하지 않는다.

```json
{
  "registration": "PASS",
  "response_rule": "PASS",
  "pairing": "PASS",
  "metadata": "PASS",
  "authentication": "NOT_VERIFIED",
  "authority": "NOT_ACQUIRED",
  "database": "NOT_CHECKED",
  "model": "NOT_CALLED",
  "target_requests": 0,
  "diagnosis": "NOT_RUN"
}
```

검사 범위는 다음과 같다.

- 기존 보호 파일 reader로 metadata·snapshot·registry·참조 파일을 읽는다. 파일 생성·상태 marker·승인 소비·DB/모델/대상 접속은 없다.
- 실제 송신 생성자와 같은 origin·고정 URL·계정 참조·응답 규칙 검사를 사용한다. URI를 계정별로 재구성하지 않는다.
- 기존 `CheckPairing`으로 principal 라벨·전체 자원 참조·reset 세대·표식의 정확한 집합을 대조한다. ownership은 `resources[].accounts`에서 추정하지 않고 KG/metadata의 owner와 대조한다.
- metadata의 KG canonical digest·정책 버전·generation·instance·reset·owner/evaluated·canary 필드·사전 판정 정책을 함께 검사한다.
- 기존 첫 등록 GET의 2요청·최소 500ms 간격·요청당 최대 10초·응답 최대 1MiB·최대 15분 유효기간 제한을 확인한다. 이 수치는 새 기본 승인이 아니다.

`response_rule=PASS`는 규칙의 구조 검사다. 해당 경로에 실제 JSON 객체가 있는지, 표식이
owner 응답에 정확히 돌아오는지, endpoint가 비변경인지, 세션이 실제로 인증되는지는 이 명령이 관찰하지 않는다.
지원하지 않는 shape나 증거 부족을 취약점 없음·접근 성공으로 바꾸지 않는다.

## 실제 기동과 인계 완료 조건

같은 준비 검사는 기존 `check -config ...`의 registered_get 모드와 실제 첫 기동에도 연결돼 있다.
불일치는 DB 실행 권위와 단회 시작 marker 취득 전에 거부한다. 검사 후 파일이 바뀔 수 있으므로
기존 설치자 앵커의 실제 바이트 승인·task/intent DB 검증·등록 시 동결·송신 직전 검사를 다시 수행한다.
준비 검사 결과를 권위로 전달하거나 검사를 통과했다는 이유로 실행 가드를 건너뛰지 않는다.

자료를 받은 뒤 이동찬이 등록·pairing과 owner/cross 동일 객체를 검증하고,
승인된 새 회차에서 owner 정상 대조와 cross GET의 실제 전송·응답·감사·저장 근거를 연결한다.
최민준의 증거 판정과 실제 모델 진단 능력 검증은 이 준비 검사와 구분한다.
