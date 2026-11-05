# E② 네트워크 경계 집행 공통 계약

상태: **구현 전 제안·팀 검토용. 운영 비활성.** 기준 `dev` `f21d73fe89315ae69bd70051fb2d043d363ea1d0` (2026-11-04). 이 문서는 12월 18일 필수 범위의 집행점, A/B 병렬 구현 경계, C 통합 조건을 정한다. 실제 주체·권한자·키·epoch·배포 환경은 발급하거나 확정하지 않는다. 승인·감사·네트워크 분리의 실제 증거가 없으면 기능을 활성화하지 않는다.

## 1. 현재 근거와 범위

- [등록 HTTP 전송기](./e2-http-transmitter.md)와 `transmit.NewHTTP`는 서버가 등록한 정확한 `GET`, `ReadOnly` 자원·계정, 고정 연결 IP, 대상 호스트의 TLS 인증서 검증, 환경 프록시 비사용(`Proxy:nil`), 무리다이렉트·무재시도를 집행한다. `StopAndWait`는 **그 전송기**의 admission과 진행 요청만 다룬다. `Preview`는 등록 정합성만 보고 현재 승인·만료·예산·감사를 확인하지 않는다.
- [서버 실행 연결](./e2-server-runtime.md)은 통제 Worker에 `controlled_http_get`만 제공한다. 이 한 경로의 제한을 일반 ARTEX 도구, 셸, MCP, 브라우저, 자식 프로세스의 출구 통제로 확대 해석하지 않는다.
- `traffic/traffic.go`는 `SslInsecure:true`, MITM 실패 후 호스트를 터널 목록에 넣는 `maybePassthrough`, 캡처 토글을 가진 관찰용 프록시다. `agent/worker.go`의 `proxyEnv`와 WebFetch 프록시 설정은 환경 힌트다. 둘 다 필수 집행점이 아니다.
- `transmit/auditlog`의 현재 v1 이벤트는 `GET`/`UNSUPPORTED`의 intent/result와 제한된 사유만 받는다. `Receipt.Admitted`는 예약이며 도달 증명이 아니고, `RequestWritten=false`도 부분 송신이 없었다는 증명이 아니다. 범용 HTTP/소켓 차단 사건을 v1 형식에 억지로 기록하지 않는다.
- [공유 예산](./approval-budget-server-wiring.md)의 `budget.Ledger`는 실제 host가 공급한 같은 인스턴스·정확한 `TaskRef/BudgetRef/PolicyRef/ResetRef/RunRef/Attempt`에 한해 예약한다. `Resource.ApprovalRef`는 `BudgetRef`가 아니다. [동기 CPD 게시](./e2-durable-audit-server-wiring.md)와 [Publisher](./audit-checkpoint-publisher.md)는 실제 Host/Lease/RootScope/키/현재 head가 있어야 한다. 로컬 journal·합성 Host·증언만으로 운영 권한이나 전역 종료를 만들지 않는다.

**제품 허용 범위는 현재 strict controlled-read의 등록된 조회 GET뿐이다.** 정상 요청은 이 범위에서 HTTP와 HTTPS 모두 실제 대상에 전달되어야 한다. 임의 URL·메서드·쓰기·상태 변경 GET, 범용 도구의 인터넷 접근, 임의 CONNECT와 불투명 터널은 허용하지 않는다. 더 넓은 기능은 승인·감사 의미와 수용시험을 별도 개정한 뒤에만 열린다.

## 2. 신뢰 경계와 실행 결속

신뢰 집행점은 모델·도구·자식이 수정할 수 없는 host 소유 네트워크 경계와 그 경계의 단일 송신 서비스다. 도구 영역에는 대상에 대한 직접 IPv4/IPv6 경로, DNS/UDP 출구, 재사용 가능한 외부 소켓 또는 임의 로컬 프록시를 주지 않는다. 승인된 요청은 인증된 내부 통로로 집행점에 전달하고, 집행점만 검증된 대상에 연결한다. 도구가 환경 프록시를 지우거나 직접 소켓을 열어도 같은 차단이 적용되어야 한다. Linux network namespace는 인터페이스·라우팅·방화벽·소켓을 분리할 수 있고, 배포의 차단 규칙은 명시적 drop이어야 한다. 이는 구현 후보일 뿐이며, 실제 namespace·경로·자식 상속·권한 제거의 설치 증거와 우회 시험이 없으면 분리 완료로 기록하지 않는다. 현재 dev의 `agent.Worker.Execute`는 서버 프로세스에서 직접 호출된다. 같은 프로세스의 도구 호출에 namespace 설정만 덧붙여 신뢰 host와 도구를 분리했다고 주장할 수 없다. 별도 도구 프로세스를 사전에 격리하고 host 측 송신 서비스를 밖에 두거나, 서버 전체를 격리하고 외부 신뢰 송신 서비스를 두는 프로세스 경계가 필요하다. B의 배포 구성만으로 기존 Worker 수명이 바뀌지 않으므로 C의 실제 기동·종료 연결 전에는 일반 도구 대상 경계를 활성화하지 않는다. [Linux network_namespaces(7)](https://man7.org/linux/man-pages/man7/network_namespaces.7.html), [nftables base-chain policy](https://wiki.nftables.org/wiki-nftables/index.php/Configuring_chains).

host는 작업·의도에 대해 **한 실행**을 열 때 다음의 보호된 결속을 공급한다. 누락·출처 불명·불일치·재사용은 차단한다.

| 결속 | 필수 의미 |
| --- | --- |
| 실행 | 실제 `TaskRef`, `RunRef`, 의도/검사 참조, `ResetRef`, `PolicyRef`; 새 reset·정책은 새 권한과 새 실행 |
| 주체 | 실제 host/issuer와 도구 영역의 식별·수명, 모델이 재생할 수 없는 내부 통로; 모델 입력의 주체 주장 불신 |
| 자원 | 등록 계정/세션, `ResourceRef` → 정확한 scheme·authority·경로·query·고정 연결 IP·읽기 승인·`ReadOnly`; 대상 IP와 TLS 이름 모두 결속 |
| 예산 | 실제 승인에서 온 `BudgetRef`, 공유 `Ledger` 인스턴스, `Attempt{RunRef, Sequence}`, 유효 시간·총량·간격 |
| 감사 | 실제 Host/Lease/RootScope/키 참조와 실행 scope, 동기 intent/result·현재 checkpoint ACK; 불명 상태면 차단 |

도구 메시지·JSON·환경 변수·Preview·과거 영수증은 권한을 만들지 않는다. 집행점은 매 요청의 **송신 직전** 현재 host 승인, 철회, 만료, 계정/자원/경로, reset·정책, 중지 상태, 예산과 감사 건강을 재검사한다. 승인 상태가 바뀌면 기존 연결도 다음 요청에 재검사를 거치며, 재검사를 피할 수 있는 keep-alive·터널 재사용은 이 단계에서 허용하지 않는다. `Ledger.Reserve` 성공은 환불하지 않고, 중복 Attempt는 재송신 허가가 아니다. 예약과 송신 경합을 철회 barrier와 직렬화한다. 이미 admission된 바이트는 철회로 되돌릴 수 없으므로 결과에 불확실성을 남긴다.

## 3. HTTP/HTTPS 데이터 경로

1. 내부 요청은 서버가 고정한 `account_ref`·`resource_ref`만 받는다. 집행점은 현재 실행 결속에서 등록된 `GET`과 정확한 URL을 재구성한다. 모델·도구의 요청 입력으로 임의 절대 URL, Host 헤더, method, body, proxy 설정, path/query 치환을 받지 않는다. host가 보호된 등록을 통해 공급한 URL은 §2의 자원 결속으로만 사용한다. URL의 userinfo·모호한 정규화·등록 외 authority·리다이렉트 대상은 거부한다. GET의 실제 비변경성은 승인된 시험 환경에서 별도로 확인한다.
2. 대상 연결은 등록한 IP/포트에만 열고 HTTP Host와 HTTPS SNI/인증서 이름은 등록 authority를 유지한다. 인증서 체인·이름·유효 기간 검증 오류, TLS handshake 실패, 신뢰 루트 불명, scheme 혼동은 송신 실패/차단이다. `InsecureSkipVerify`나 대체 직결 경로로 성공 처리하지 않는다. HTTP는 TLS가 없음을 정책에서 명시적으로 승인한 정확한 등록 origin에만 허용한다.
3. 일반 도구의 CONNECT, TLS 안의 불투명 HTTP, TLS 가로채기 실패 후 터널 전환은 기본 거부한다. 허용된 HTTPS GET은 신뢰 집행점이 등록 요청을 직접 생성해 검증된 TLS로 송신한다. 포워드 프록시를 추가한다면 CONNECT 전후의 대상·메서드·경로를 검증할 수 없는 경우 차단하고, 이 계약의 승인 범위를 넓히지 않는다.
4. 요청은 허용된 경우 실제 대상에 한 번 전달한다. 동일 호출의 자동 retry·리다이렉트·압축·무한 스트림은 거부한다. 응답은 기존 크기·시간·투영 제한을 지킨다. HTTP 오류 응답 수신, 네트워크 오류, 조회 결과의 취약점 판정은 서로 다른 상태다.
5. 기존 `transmit.NewHTTP`를 등록 GET 송신의 구현으로 재사용할 수 있다. 그 자체를 일반 도구의 네트워크 경계라고 표시하지 않는다. 신규 경계가 보호 통로와 OS 차단을 증명하기 전에는 기존 제한된 통제 조회 이외의 경로를 열지 않는다.

## 4. 중지·회수·마감

C 통합은 작업/실행/철회/서버 종료 시 아래 순서를 **같은 실행 결속**으로 조정한다. A의 전송기 종료만으로 C의 전체 종료 증명을 채우지 않는다.

1. host가 실행을 `quiescing`으로 표시하고 새 권한·새 내부 요청 admission을 닫는다. B 경계가 새 직접/프록시/터널 흐름도 차단한다. 이후에 요청이 새로 통과할 창이 없어야 한다.
2. A의 요청·연결을 취소하고 B가 해당 도구 영역의 터널·자식/손자 프로세스·상속된 소켓과 재접속 경로를 종료한다. 이미 admission된 요청/부분 송신은 취소로 소급 제거하지 않는다.
3. 요청 결과 기록과 각 소유자의 wait를 마친 뒤, 경계에서 잔존 통신 경로가 없는지 확인한다. 기다림 실패·시간 초과·프로세스 식별 불명은 `drain_unknown`으로 남기고 격리를 유지한다. 성공으로 Seal하거나 새 실행으로 강등하지 않는다.
4. 실제 drain 뒤 Publisher가 Journal.Close를 수행하고, 실제 감사 Host가 CloseWitness·종료 증거·현재 권한을 독립 확인한 뒤에만 Seal ACK와 보호된 checkpoint/finding 마감으로 진행한다. 성공/실패가 불명인 저장은 같은 연산·정본을 재확인한다. 임의 새 키·epoch·RunRef·batch·가짜 ACK로 성공을 꾸미지 않는다.
5. 경계 차단과 종료 확인이 유지되는 상태에서 내부 통로, 자격증명·키 사본, OS 권한을 해제한다. 해제 중 재접속 가능해지면 완료가 아니다. 실제 권한자는 팀 결정과 배포 증거 전까지 미정으로 둔다.

## 5. 감사 표현과 실패 의미

기존 v1 `transmit.Event`는 등록 GET의 intent/result에만 그대로 사용한다. 범용 경계 사건은 **새 버전·저장 위치·검증자 합의 전까지 구현 제안**이며, 기존 `Reason` enum이나 `ApprovalRef` 의미를 조용히 바꾸지 않는다. 아래 상태는 집행점의 결정·관측을 표현하는 논리 분류이지, 내구 감사 행의 존재를 주장하지 않는다. 실제 저장 여부는 별도 ACK·현재 head로만 확인한다. 새 표현의 최소 논리 필드는 실행 scope, 비민감 주체/등록 자원 참조, 시도/흐름 식별자, 단계, 안전한 사유, 관측 시각, 승인·예약 여부, 요청 쓰기 관측, 응답 관측, 감사 저장 상태다. URL·query·헤더·본문·세션/키 원문·원시 오류·임의 모델 문자열은 넣지 않는다. 자원 참조도 비민감 등록값만 사용한다.

| 상태 | 기록·판정 의미 |
| --- | --- |
| `blocked_pre_send` | 송신 전 gate에서 거부됨. 해당 통로의 새 대상 수신 0을 시험으로 확인할 수 있지만, 타 통로의 부재를 이 한 이벤트로 증명하지 않음 |
| `admitted` | 승인·감사 intent·예산 예약 성공. 대상 도달/완전한 요청 쓰기 증명 아님 |
| `send_partial_or_unknown` | 전송 오류·취소·TLS/연결 경합 등으로 일부 바이트/원격 처리 여부 불명. `RequestWritten=false`도 여기에 포함 가능 |
| `response_observed` | 제한된 응답 수신 관측. 조회 성공·취약점·진단 완료를 뜻하지 않음 |
| `audit_unknown` | 결과 쓰기/CPD ACK/현재 head가 불명 또는 실패. 이후 admission 중지, 원본 보존, 성공 Seal 금지 |

거부 사유는 고정 enum으로 제한하고 단계별로 `unsupported_path`, `approval_unavailable`, `revoked`, `expired`, `binding_mismatch`, `budget_unavailable`, `budget_exhausted`, `audit_unavailable`, `tls_rejected`, `drain_unknown` 등을 **제안**한다. 코드·journal 스키마 변경은 감사 담당 리뷰와 버전 합의 후 진행한다. 필수 intent 감사 실패는 송신 0, 결과 감사 실패는 이미 송신했을 가능성을 유지하고 이후 admission 0이다. intent ACK 실패·불명 시 내구 사건 자체가 없을 수 있다. 이때 차단·격리 상태는 유지하되 감사에 기록됐다고 표시하지 않는다. 저장 결과 불명은 `blocked_pre_send`로 거짓 축소하지 않는다.

## 6. 구현 소유권과 최소 접점

| 단위 | 소유 파일·책임 | 공유 접점과 보류 |
| --- | --- | --- |
| 공통 계약 | 이 문서 `docs/contracts/e2-network-enforcement.md` | 요구/시험/실패 의미의 기준. 실제 운영 승인 아님 |
| A | 새 제품 집행 패키지 `netboundary/`와 그 패키지 시험. 등록 HTTP/HTTPS 송신, 신뢰 gate, 실행 결속, 승인·공유 예산·감사 adapter | 기존 `transmit`, `budget`, `auditpublisher`의 의미를 유지. 공유 API·v1 journal 변경 필요 시 별도 리뷰·합의 전 보류 |
| B | 새 `deploy/e2-network-boundary/`의 격리 구성과 같은 디렉터리의 우회/경계 시험 파일. 기존 `deploy/engine-isolated/` 수정은 C 조정 후 별도 PR. 직접 소켓·DNS·IPv6·셸·MCP·자식·TLS·CONNECT·캡처 토글 경로의 차단/정상 전달 증명 | A와는 보호된 내부 통로·실행 식별·경계 차단/종료 증거만 합의. 실제 프로세스 배치와 Worker 연결은 C 의존. `traffic` 캡처 설정을 보안 권한으로 사용하지 않음 |
| C | 기존 작업1 소유의 `controlledhost`·기동·종료 연결 | A/B 증거를 실제 host 수명에 묶는 통합은 작업1과 조정 후 별도 진행. 이 계약 PR은 해당 코드와 기존 평가·격리 파일을 수정하지 않음 |

A→B의 최소 계약은 `Open(run binding)`에서 host가 검증한 scope와 양쪽 영역 식별을 결속하고, `SendRegisteredRead(account_ref, resource_ref)`가 요청별 현재 gate를 통과할 때만 송신하며, `CloseAdmission/StopAndWait`가 **A 소유 요청**의 종료 결과를 반환하는 것이다. B→C는 `BlockNewFlows(run)`, `TerminateAndWait(run)` 및 잔존 소켓/자식 여부의 실제 증거를 제공해야 한다. 이 이름은 **개념적 접점**이며 공개 Go API/새 authority를 선언하지 않는다. C는 두 증거와 실제 host 권한을 결합해 종료·Seal을 결정한다. 양쪽 구현은 서로의 파일을 수정하지 않고 별도 PR로 검토한다.

## 7. 수용시험과 활성화 조건

아래는 **계획**, 이 문서 작성 시 실행 결과가 아니다. 합성 계정/승인/키, 소유 loopback HTTP·HTTPS target, 격리된 프로세스/namespace만 사용한다. 실제 모델·진단 대상·운영 권한 발급은 수행하지 않는다. 각 시험은 예약, 대상 수신, 감사 ACK, 자식/소켓 잔존을 가능한 범위에서 별도로 계수하고 PASS/FAIL/SKIP/미실행으로 보고한다.

| ID | 담당 | 조건 | 통과 조건 |
| --- | --- | --- | --- |
| NE01 | A+B | 정확히 등록한 HTTP GET | 현재 승인·예산·intent ACK 뒤 대상 수신 1, result 기록; 프록시 환경 변경에도 동일 |
| NE02 | A+B | 정확히 등록한 HTTPS GET, 고정 IP/정상 인증서 | 대상 수신 1, 검증한 TLS 이름/인증서·경로와 등록 결속 |
| NE03 | A | 다른 task/run/reset/정책·계정·경로/query, POST·상태 변경 GET | 예약/송신 0, 비민감 거부 사유; Preview 일치 결과로 우회 0 |
| NE04 | A | 철회·만료·예산 소진·intent/현재 head 실패와 송신 직전 경합 | barrier 뒤 새 admission 0, 기존 admission의 수신·부분 송신 가능성 보존, 중복 예약·환불·새 Ledger 재충전 0 |
| NE05 | A | 예약 뒤 부분 쓰기·취소·결과/CPD 저장 불명 | 예약 유지, 송신 불명과 감사 불명 보존, 추가 admission 0, 성공 Seal 0 |
| NE06 | B | 도구 영역의 직접 IPv4/IPv6 TCP/UDP/DNS, 환경 프록시 제거, 임의 localhost/Unix socket, 상속 fd | 허용 내부 통로 외 대상 수신 0; 실패 경로의 일반 네트워크 fallback 0 |
| NE07 | B | Bash/WebFetch/Search/MCP/브라우저/자식·손자의 자체 소켓 시도 | 각 별도 출구 차단 또는 해당 도구 비활성 증거; 일반 도구 권한 확장 0 |
| NE08 | A+B | HTTPS 잘못된 인증서·이름·만료·handshake, 불투명 CONNECT·MITM 실패 | 대상 애플리케이션 요청 0, 직결/무검사 터널 0, `SslInsecure`·캡처 토글 영향 0 |
| NE09 | A+B+C | 중지와 새 요청/터널/자식 생성 경합 | 새 admission 차단 → 요청·터널·자식 drain → 감사 마감 → 권한 해제 순서; 잔존 시 격리·미완료 |
| NE10 | A+B | 비밀값 포함 오류·응답·감사 실패 | 모델/영수증/감사/공유 로그에 비밀 원문 0, 상태는 성공으로 승격하지 않음 |

### 7.1 공식 비교 범위 정책 모드 (제안·운영 비활성)

기존 **strict controlled-read 등록 GET 모드**와 공식 비교의 익명 도구 실행을 같은 모드로 취급하지 않는다. 전자는 등록 자원만 허용하는 제품 기본 범위이고, 후자는 개선본의 E²를 켠 상태에서 원본과 비교하기 위한 별도 **공식 비교 범위 정책 모드**다. 이 모드는 이 문서와 수용시험·구현이 합의되기 전에는 활성화하지 않는다.

공식 프로파일은 `target_origin=http://eval-meter:8080`, `paths=["/"]`, `methods=["GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"]`로 고정한다. 이는 현재 공식 프로파일의 선언을 옮겨 적은 것이며, 메서드 선언만으로 실행 허가를 만들지 않는다. 이 모드에서 `/`는 **동일한 선언 origin의 루트와 모든 하위 경로**를 허용하는 prefix 정책 표기다. trailing slash·dot segment·percent encoding을 포함한 prefix 정규화·범위 매칭 규칙은 정책 내용·지문과 NE 시험에 명시해 사전 고정한다. 현재 프로파일 검증기의 정적 path 템플릿 의미를 이 모드의 prefix 허용 사실로 소급하지 않는다. 이 origin·정책 범위·7개 메서드 안의 HTTP 요청만 E²가 검사한 뒤 공통 독립 계측기로 전달·감사하고, 다른 origin·프로토콜·경로·검사 불가 연결·직접 소켓·우회 경로는 차단한다. 상태 변경 메서드는 회차마다 새로 띄우고 원복할 수 있는 사전 승인 시험환경에서만 범위 안에 허용한다.

공식 경로는 **엔진 → 실제 E² 집행점 → 공통 독립 평가 계측기 → 대상**이다. 계측기는 baseline·개선본에 동일하게 적용하며 제품 E²를 계측기로 대체하지 않는다. 범위 정책의 내용과 지문은 시범 회차 전에 고정하고, 시범도 공식과 동일한 개선본 E² 구성으로 수행한다. 이 선행 조건과 NE 시험이 끝나기 전에는 시범을 근거로 예산을 조정하지 않으며, 조정은 그 뒤 한 번만 한다.

E²가 전송 전에 차단한 시도 수는 대상에 전달된 요청 수와 별도 집계한다. 정책 지문과 허용·차단·실패 집행 요약은 회차 근거로 수집하는 러너·평가 후속이며, 이 문서가 새 저장 형식이나 구현 완료를 주장하지 않는다. E² 내부 집행점 장애 또는 감사 불명은 유효 회차에서 **제품 실패**로 기록한다. 사전에 정의한 외부 환경 장애만 기술적 무효로 분류한다. 제품 실패 시 추가 송신을 중단하고 원자료와 근거 부족을 보존하며, 그 장애로 근거가 부족해진 항목만 `UNDECIDABLE`로 남긴다. 이미 완결된 불변 판정 원값·근거는 보존하고 기존 scorer 규칙을 적용한다. 제품 실패를 성공으로 승격하지 않고, 수치 없는 칸을 0으로 채우거나 분모에서 제외하지 않는다.

공식 비교의 이 범위 모드는 HTTP 계측기 경로의 수용을 HTTPS 가로채기 없이 검증할 수 있다. HTTPS 검사·신뢰·우회 차단은 이 HTTP 비교와 별개의 제품 완성 요건으로 유지하며, HTTP 비교가 HTTPS 완성을 뜻하거나 반대로 HTTPS 미완성이 HTTP 비교를 무효화한다고 섞어 해석하지 않는다.

공식 비교 모드에 대한 추가 수용시험은 NE11–NE15로 계획한다.

| ID | 담당 | 조건 | 통과 조건 |
| --- | --- | --- | --- |
| NE11 | A+B | 공식 프로파일의 동일 origin 루트·모든 하위 경로·7개 메서드 안 요청 | 개선본 E²가 검사·감사 후 공통 계측기로 전달; baseline과 동일 계측기에서 관측 |
| NE12 | A+B | 다른 origin·프로토콜·경로·검사 불가 연결 | 대상 전달 0, 차단 시도 수에 별도 계수; 등록 GET 모드로 조용히 대체하지 않음 |
| NE13 | B | 직접 소켓·프록시 제거·CONNECT·자식 출구 우회 | 공통 계측기나 대상에 우회 전달 0; 실패 근거 보존 |
| NE14 | A+B | 회차별 새 대상·reset 가능한 상태 변경 메서드 | 사전 승인 환경에서만 허용하고 회차 종료 후 원복·초기화 확인 |
| NE15 | A+B+C | E² 집행 장애·감사 불명·정책 지문 불일치 | 신규 송신 중지, 원자료·`UNDECIDABLE` 보존, 성공 승격·0 대체·분모 제외 0 |

단위 시험 통과는 B의 실제 배포 격리나 C의 종료 증명이 아니다. B는 실제 적용한 네트워크·프로세스 권한 구성과 우회 경로마다 수신 0을 보여야 한다. A/B 계약 일치, 각 단위의 독립 리뷰 1회 이상, 합성 통합 양성·음성 시험, C의 실제 수명 연결, 감사 버전/Host 결정과 팀 승인이 끝나기 전에는 “전수 집행”이나 운영 준비 완료로 표시하지 않는다.
