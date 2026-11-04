# 첫 IDOR 서버 조립 경계

상태: 기존 C bootstrap을 보존하고 최신 선행·A/B·Config handle을 결합했다.
전체 finalization scope는 세션2의 충돌 보완 인계 대기다. 실제 진단은 미실행이다.

아래 기존 설계·수치는 최초 #166 제출 당시 기록이며, 최신 상태는 마지막 재개 기록을 따른다.

## 생성 순서와 호환성

`NewWithControlledReadAssembly`는 기존 `ControlledReadRevisionHostOptions`를
받으며 `Durable.Host.AuditSource`를 필수로 요구한다. 취소된 context,
필수 공급자/host handle 누락과 감사 읽기 상한 오류를 먼저 거부하고,
`NewWithRevisionedDurableControlledReadHost`를 그대로 호출한다.
revision Store의 DB는 해당 생성자가 받는 실제 Manager DB다.

기존 `ControlledReadHostOptions`에 선택적 `AuditSource` 한 필드만 추가한다.
기존 호출자가 이를 생략하면 공급한 Findings/Source 동작이 유지된다.
strict bootstrap 순서는 mode → Publisher → 실제 Server의 감사 reader 결속 →
Findings 설치다. 이 과정이 성공한 뒤에만 기존 복원·background admission으로 간다.
미초기화 callback이나 일반 생성자로의 fallback은 없다.

결속은 `Server.NewControlledReadAuditSource`와 `SourceReader`를 재사용한다.
resolver 실행 전에 context·서버 수명·root를 검사하고, resolver 반환 후 다시 검사한다.
metadata Source는 bootstrap/resolver 시점에 조기 조회하지 않는다.
최종화 시마다 기존 reader가 최신 checkpoint·키·root 신뢰와 전체 감사를 확인한다.
승인·KG·완료·증거 digest·revision·EvalRunID는 이 연결부가 생성하지 않는다.

## 소유 범위와 아직 연결하지 않은 것

C 고유 변경은 `server/controlled_read_assembly.go`, Linux 합성 시험,
이 문서와 승인된 공용 `server/controlled_read_host.go` 최소 연결점이다.
A의 `server/controlled_read_real_inputs*`, B의 `controlledhost/`,
기존 기본 명령 `cmd/artex/main.go`는 변경하지 않는다.

A PR #165의 확인 head는 `299f7520b7553279afff9037b9040f25997ab12c`다.
`BindControlledReadRealInputs(ctx, request, expected, approved, kg, source)`는
이미 인증한 Config·계정·조회 승인·KG·Source 사이의 결속 검사다.
현재 원본 구현과 신규 3파일 범위를 확인했으며, 실제 자료 producer가 있다는
근거로 사용하지 않는다. C의 Findings는 실제 승인 Config를 확보한 resolver가
그 함수를 호출해 반환한 plan을 받는 형태로 연결할 수 있다. 감사 Source는
C bootstrap이 그 plan의 기존 metadata Source에 결합한다.

B 산출물과 실제 운영 공급자는 정확한 commit·API·시험 근거 확인 후 연결한다.
현재 이 변경은 A/B 코드를 상속하지 않으며 실제 운영 공급자나 CLI를 추가하지 않는다.
감사 Source의 전후 재검사는 전체 Source → Prepare → 저장 구간의 권위 lease나
epoch ABA 방지를 증명하지 않는다. B가 지적한 해당 구간의 추가 수명 hook은
별도 연결이 필요하며, 여기서 임의의 새 권위 API로 확정하지 않는다.
누락 자료는 [기존 입력 대장](first-idor-host-inputs.md)을 단일 기준으로 사용한다.
준비되지 않은 callback, 고정 승인, 가짜 KG, 기본 revision/완료 값으로 채우지 않는다.

실제 실행 명령과 종료 orchestration은 실제 A/B handle의 수명·해제 계약 및
실행 자료 확보 후 연결한다. 서버 생성자는 기존 동작으로 JWT/인증·실행 저장소를
초기화할 수 있으므로 구성 검사의 성공이 실제 기동 승인이나 무변경 보장은 아니다.
schema·root·epoch provisioning은 이 연결부에 없다.
이 변경만으로 신규 실행 차단·모든 자식 작업 drain·감사 마감·저장 확인·자원 해제의
완전한 종료를 증명하지 않는다. listener/대상 HTTP/실제 모델은 실행하지 않았다.

첫 실제 회차는 비평가 진단으로 Source의 `EvalRunID`를 비워야 한다.
실제 provider/profile, 대상·DB·상태 위치와 승인 상한, 명시적 schema 설치,
소유 자원의 수명 계약을 확인한 후 기존 인증된 API로 진행한다.
내부 DB 행 조작이나 새 RunRef 자동 재송신으로 대체하지 않는다.

## 확인 기준과 선행

최초 기준 dev: `893d1d771a10764591ee209814800f46420e0dc9`.
게시 전 재확인 dev: `2f069b9cbc3420b8197a053dc207327c3364434a`.
최신 선행을 정상 merge한 C 고유 변경 기준:
`0e00435d071d08db78591daa36bef68df428b1cc`.
누적 선행 diff와 C 고유 diff는 이 SHA를 기준으로 분리한다.

| 선행 | 확인 head |
|---|---|
| #143 | `1dfc4a0fbf9f47db07eacf8df420fcbd9982e9ad` |
| #149 | `8cc0d5588fb6e8b6313434006224cccd8968bab2` |
| #151 | `3312a07ca9bc14b1e326250f86ae289326114f97` |
| #156 | `684451300875251325eb507db30c4d3b45ec4dbc` |
| #157 | `9c079d92e5728411774005cf4760124a55068bb4` |
| #158 | `e6af2335fbd0412fccf03df589f95d754c26c41b` |
| #159 | `32bd87f77460953f2a60a5de3cdc391cc0b75758` |

순서는 #143+#149 → #151, #156 → #157 → #158, 두 계통 → #159다.
C branch는 확인한 #159 head와 #151 후속 head, 최신 dev를 정상 merge했다.
#143/#149는 게시 전 상태 확인에서 dev 병합을 확인했다.
#151 후속 변경은 합성 시험의 인증 setting 복원 17줄이며 production 변경은 없다.
A/B 완료물을 확인한 뒤 실제 연결로 간다.
#160/#161/#163/#164를 필수 코드 선행에 추가하지 않는다.

합성 시험은 `_test.go`의 기존 소유 fixture를 재사용한다.
실제 감사 파일 검증과 Source 결합, 같은 plan 재조회 시의 신뢰 상실,
미초기화/다른 runtime 거부, resolver 오류·취소, 기존 호출 호환성을 확인한다.
이는 실제 KG·승인·AI·PostgreSQL·Juice Shop 진단의 증명이 아니다.
실측 결과는 PR 검증란에 기록하며 skip/미실행을 성공에 포함하지 않는다.

최종 합성 race 회귀는 leaf 기준 60건 통과, PostgreSQL 미설정 23건 skip,
실패 0건이다(부모 test를 포함한 pass 이벤트 67건).
`go vet ./server`, `go build ./...`, gofmt·diff 검사와 독립 리뷰를 수행했다.
실제 PostgreSQL·실제 LLM·Juice Shop 진단과 종료 orchestration 시험은 미실행이다.


## 2026-11-04 재개: 선행 결합과 정확한 대기 지점

재개 시 확인한 dev는 `b1ebc71d3fad5833308619f5dfd221496a2c5ac1`이다.
현재 제품 코드 통합 기준은 `931354e3a6a260abff040168a3015e382fdd7572`이며
기존 C `fc36aa41804188c06513c62b8d5d75af63bc776a`를 조상으로 보존한다.

| 받은 결과 | 통합한 SHA / 상태 |
|---|---|
| #151 | `3c9ee431c5846822d38e86d15207affa37130378` |
| #156 | `ef17137e76df4d80fd10017e29e2ef7496341e24` |
| #157 | `cbdf6a82dc3317eab3ebe6c0de4abb24f449a80a` |
| A #165 | `7d5516f132b91d57ea0ff7819688dee10e0d4dd9` |
| Config handle #171 | `dd7d962cfd8010de0d1aac4115998900de58fa7e` |
| 보존 B | `acd5333d8a23db9743a8c40b1c399f6deee784bb` |
| finalization scope | `ee508a4d6bd455a37259c2fae40d968ad9c96355` 확인, **미통합** |

선행·A/B·handle은 정상 merge했다. 닫힌 후행 PR을 다시 열지 않았으며,
보존 분기의 존재나 C 통합을 dev 반영으로 취급하지 않는다.
Config handle의 `View/Bind`는 있으나 runtime → verification → Resolver 전달은
세션1의 미완료다. C는 별도 Config로 handle을 만들어 실제 출처인 것처럼 대체하지 않는다.

scope merge는 `server/controlled_read_e3.go`의 reader 한 구간에서 #159와 충돌해
해당 merge만 abort했다. **사용자는 C의 수동 해소 대신 세션2의 보완 인계 대기를 선택했다.**
세션2 보완 시 기존 `revisionSource` 분기와 `revisionErr` 분류를 유지하면서
그 전후의 `checkScope()`를 결합해야 한다. scope 획득은 collector mutex 밖에서,
revision Source A/B·descriptor I/O·SQL Issue·Prepare·최종 save 반환까지 보유해야 한다.
기존 revision 서비스를 버리는 일반 Findings 재설정으로 충돌을 우회하면 안 된다.

현재 `NewWithControlledReadAssembly`는 감사 결속을 요구하지만 전체 finalization scope의
필수성을 강제하는 C 연결은 아직 없다. 이 hook의 보완·strict 주입 전에는 실제 기동 조건을
충족하지 않는다. B의 `Bind/Revoke/Close` 어댑터를 실제 authority·감독자의 대체물로 쓰지 않는다.
`cmd/first-controlled-diagnosis`는 미작성이며 빈 실행 도구나 가짜 producer를 추가하지 않았다.

통합 기준의 소유 폐기용 PostgreSQL16 회귀는 다음과 같다. 모두 실제 DB와 합성 공급자 시험이다.

- host 수명 + revision 복구: **130 leaf pass / 0 skip / 0 fail**.
- C·감사 Source·revision 연결·strict 생성자·A·Config handle: **122 leaf pass / 0 skip / 0 fail**.
- 관련 vet, 전체 build, gofmt·diff 검사 통과. 고유 라벨의 시험 컨테이너 제거 확인.

실제 운영 담당자·보호된 자료 경로는 사용자 확인상 미정이다.
[기존 입력 대장](first-idor-host-inputs.md)의 실제 session/승인/KG/fixture·RootTrust/key/epoch·
복원/자식 추적·모델/대상/DB/state 자료가 필요하다. 실제 LLM·Juice Shop 회차와
같은 execution의 전송·감사·판정·revision·저장·결과 조회 대조는 미실행이다.
