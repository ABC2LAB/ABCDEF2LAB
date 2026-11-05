# E② 네트워크 경계 집행 공통 계약

상태: **구현 전 제안·팀 검토용. 운영 비활성.** 기준 `dev` `a8f0ed0fdc98fe0a365f1409caba96f746c178fc` (2026-11-05). 이 문서는 12월 18일 필수 범위의 집행점, A/B 병렬 구현 경계, C 통합 조건을 정한다. 실제 주체·권한자·키·epoch·배포 환경은 발급하거나 확정하지 않는다. 승인·감사·네트워크 분리의 실제 증거가 없으면 기능을 활성화하지 않는다. 평가와 제품의 공통 의미는 [평가 프로토콜 §2](https://github.com/ABC2LAB/ABCDEF2LAB/blob/a8f0ed0fdc98fe0a365f1409caba96f746c178fc/docs/evaluation/evaluation-protocol.md#2-평가-대상-고정)·[§8.3](https://github.com/ABC2LAB/ABCDEF2LAB/blob/a8f0ed0fdc98fe0a365f1409caba96f746c178fc/docs/evaluation/evaluation-protocol.md#83-개선-후-통제-검증--시도별-대조) 및 채택된 #189의 [§6.1](https://github.com/ABC2LAB/ABCDEF2LAB/blob/b598c9a3e4b76e4e624e10d63b8d58c08729c82c/docs/e2-proxy-control-decision-draft.md#61-관측-단계와-상관-id)·[§10](https://github.com/ABC2LAB/ABCDEF2LAB/blob/b598c9a3e4b76e4e624e10d63b8d58c08729c82c/docs/e2-proxy-control-decision-draft.md#10-추가-회신-반영-구현-선택우선순위목표-일정)을 따른다.

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

거부 사유는 고정 enum으로 제한하고 단계별로 `unsupported_path`, `approval_unavailable`, `revoked`, `expired`, `binding_mismatch`, `budget_unavailable`, `budget_exhausted`, `audit_unavailable`, `tls_rejected`, `transport_unknown`, `timed_out`, `drain_unknown` 등을 **제안**한다. 코드·journal 스키마 변경은 감사 담당 리뷰와 버전 합의 후 진행한다. 필수 intent 감사 실패는 송신 0, 결과 감사 실패는 이미 송신했을 가능성을 유지하고 이후 admission 0이다. intent ACK 실패·불명 시 내구 사건 자체가 없을 수 있다. 이때 차단·격리 상태는 유지하되 감사에 기록됐다고 표시하지 않는다. 저장 결과 불명은 `blocked_pre_send`로 거짓 축소하지 않는다.

## 6. 구현 소유권과 최소 접점

| 단위 | 소유 파일·책임 | 공유 접점과 보류 |
| --- | --- | --- |
| 공통 계약 | 이 문서 `docs/contracts/e2-network-enforcement.md` | 요구/시험/실패 의미의 기준. 실제 운영 승인 아님 |
| A | 새 제품 집행 패키지 `netboundary/`와 그 패키지 시험. 등록 HTTP/HTTPS 송신, 신뢰 gate, 실행 결속, 승인·공유 예산·감사 adapter | 기존 `transmit`, `budget`, `auditpublisher`의 의미를 유지. 공유 API·v1 journal 변경 필요 시 별도 리뷰·합의 전 보류 |
| B | 새 `deploy/e2-network-boundary/`의 격리 구성과 같은 디렉터리의 우회/경계 시험 파일. 기존 `deploy/engine-isolated/` 수정은 C 조정 후 별도 PR. 직접 소켓·DNS·IPv6·셸·MCP·자식·TLS·CONNECT·캡처 토글 경로의 차단/정상 전달 증명 | A와는 보호된 내부 통로·실행 식별·경계 차단/종료 증거만 합의. 실제 프로세스 배치와 Worker 연결은 C 의존. `traffic` 캡처 설정을 보안 권한으로 사용하지 않음 |
| C | 기존 작업1 소유의 `controlledhost`·기동·종료 연결 | A/B 증거를 실제 host 수명에 묶는 통합은 작업1과 조정 후 별도 진행. 이 계약 PR은 해당 코드와 기존 평가·격리 파일을 수정하지 않음 |
| 평가 | 최민준: E² 배치 망 표·부정 연결 점검, 엔진이 쓰지 못하는 경로에서 정책 지문·집행 요약 수집, §8.3 대조, 외부 환경 장애 목록 사전 고정 | 독립 계측기(#195)와 E² 로그는 서로 대체하지 않으며, 공식 프로파일·지문 소비자 표는 평가 프로토콜과 함께 합의 |

A→B의 최소 계약은 `Open(run binding)`에서 host가 검증한 scope와 양쪽 영역 식별을 결속하고, `SendRegisteredRead(account_ref, resource_ref)`가 요청별 현재 gate를 통과할 때만 송신하며, `CloseAdmission/StopAndWait`가 **A 소유 요청**의 종료 결과를 반환하는 것이다. B→C는 `BlockNewFlows(run)`, `TerminateAndWait(run)` 및 잔존 소켓/자식 여부의 실제 증거를 제공해야 한다. 이 이름은 **개념적 접점**이며 공개 Go API/새 authority를 선언하지 않는다. C는 두 증거와 실제 host 권한을 결합해 종료·Seal을 결정한다. 양쪽 구현은 서로의 파일을 수정하지 않고 별도 PR로 검토한다.

## 7. 수용시험과 활성화 조건

아래는 **계획**, 이 문서 작성 시 실행 결과가 아니다. 합성 계정/승인/키, 소유 loopback HTTP·HTTPS target, 격리된 프로세스/namespace만 사용한다. 실제 모델·진단 대상·운영 권한 발급은 수행하지 않는다. 각 시험은 예약, 대상 수신, 감사 ACK, 자식/소켓 잔존을 가능한 범위에서 별도로 계수하고 PASS/FAIL/SKIP/미실행으로 보고한다.

러너 통과 전용 가짜 E², #184 등록 GET 코어, #185 합성 배포 회차는 모두 `synthetic` 표식이다. 이들은 시범·공식·제품 결과 또는 NE 통과 증거로 집계하지 않으며, 실제 제품 일반 HTTP 경로와 독립 계측기·러너 수명 연결이 확인된 뒤에만 공식 단계로 승격한다.

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

### 7.1 제품 일반 HTTP 정책 모드와 공식 정책 인스턴스 (제안·운영 비활성)

기존 **strict controlled-read 등록 GET 모드**와 제품의 **일반 HTTP 정책 모드**를 구분한다. 공식 비교는 제품 코드 경로의 일반 HTTP 모드에 동결한 공식 정책 인스턴스를 넣어 수행하며, 등록 GET 모드는 별도 호환 경로로 유지한다. 이 모드는 이 문서와 수용시험·구현이 합의되기 전에는 활성화하지 않는다.

공식 인스턴스의 scope 정본은 선택한 공식 프로파일이다. 현재 값 예시는 `target_origin=http://eval-meter:8080`, `paths=["/"]`, `GET/HEAD/OPTIONS/POST/PUT/PATCH/DELETE`이며 GPU 공식 프로파일은 별도 고정한다. 이 인스턴스에서 `/`는 동일 origin의 루트와 모든 하위 경로를 뜻하는 prefix다. trailing slash·dot segment·percent encoding의 매칭·정규화와 프로파일의 정적 템플릿→prefix 매핑을 E² 정책 지문에 포함한다. 현재 프로파일 검증기가 prefix를 집행한다고 소급하지 않는다. 프로토콜과 profile-fingerprint 소비자 표의 정렬은 최민준과 합의한 뒤 반영한다. Upgrade/WebSocket, 선언 authority로 가는 CONNECT, HTTP/2는 HTTP 인스턴스에서 미지원으로 거부·계수하며 등록 GET 모드로 대체하지 않는다.

일반 모드에 이 계약 §2–§6을 적용하는 차이는 다음과 같다. strict 입력·wire·감사 v1은 그대로 유지한다.

| 기존 절 | 일반 HTTP 모드의 적용 |
| --- | --- |
| §2 결속 | 실행·실제 host/호출·reset·정책·공유 예산·현재 권한은 필수. 등록 GET의 `ResourceRef` 필수 대신, 파싱한 실제 method·authority·raw target·header/body binding을 승인된 scope와 실제 인증 문맥에 결속한다. 익명 조건에는 보호된 익명 문맥을 쓰며 가짜 계정/자원 참조를 만들지 않는다. 도구가 주장한 계정은 인증 근거가 아니다. |
| §3 입력·송신 | 일반 모드만 §3.1의 참조 전용 입력을 대체한다. 도구가 만든 실제 요청을 파싱해 검사하고 의미를 보존하며, 임의 등록 GET으로 재구성하지 않는다. §3.2의 연결 대상/TLS 검증과 우회 거부는 유지한다. 일반 모드의 TLS 추가 조건은 아래 NE16을 따른다. |
| §4 중지 | 신규 admission 차단 → 요청·연결·자식 drain → 감사 마감 → 권한 해제 순서를 실제 실행/호출에 결속한다. |
| §5 감사·결과 | 범용 허용·거부와 단계/사유를 담는 감사 버전·저장 위치·검증자·동기 ACK·현재 head 계약을 먼저 합의한다. v1 GET 이벤트에 임의 method/body나 거부를 끼워 넣지 않는다. 이 버전과 신뢰 결과 채널 수용 전에는 일반 모드를 활성화하지 않는다. |
| §6 소유권 | A는 정책·송신·감사/결과, B는 격리/우회, C는 실제 호출·host 수명, 평가는 독립 관측·회차 대조를 맡는다. |

상태 변경 사전 승인 입력에는 실제 승인자/권한 참조, 실행·정책·reset, origin·경로·메서드, 만료/철회와 예산을 결속하고 시범 전에 동결한다. 승인 없는 `OPTIONS/POST/PUT/PATCH/DELETE`는 거부하고, 승인된 범위만 허용한다. 새 대상 생성·회차 폐기/초기화 증거는 러너 소유이며 A+B의 보상·원복 기능으로 계산하지 않는다. GET/HEAD도 부작용이 없다고 보장하지 않는다.

E² 승인 예산의 한도·예약 단위·차단 계수 규칙과 평가 프로토콜 §5.1의 시간/대상 전달 요청 예산 사이 관계를 시범 전에 고정한다. 대상 전달 상한은 양쪽 공통 계측기가 세고, E² 내부 승인 예산은 개선본의 제품 제약으로 별도 집행한다. 승인·토큰·호출·새 ID로 예산을 초기화하지 않는다. E² 예산 소진도 차단 시도로 보고하며 대상 전달 건수에는 넣지 않는다.

공식 경로는 **엔진 → 실제 E² 집행점 → 공통 독립 평가 계측기 → 대상**이다. 계측기는 baseline·개선본에 동일하게 적용한다. 제품 E²를 계측기로 대체하지 않고 E² 기록으로 독립 계측기를 대신하지 않는다. 정책 내용·승인 입력·예산·매핑 규칙·지문·미지원 목록을 시범 전에 고정한다. 시범도 공식과 같은 제품 E² 구성으로 수행하며 이 선행 조건과 NE 수용 뒤에만 공통 예산을 한 번 조정한다. 최소 E① 반복 거부 제한(#189 §10.4)의 개선본 빌드 포함 여부도 시범 전에 기록한다.

E²가 전송 전에 차단한 시도 수는 대상에 전달된 요청 수와 별도 집계한다. E²는 정책 판정 전에 CSPRNG 16바이트를 소문자 hex 32자로 인코딩한 시도 ID를 발급한다. 호출자가 넣은 `X-ABC2LAB-E2-Attempt` 값은 모두 제거·교체하고, 허용 요청에는 정확히 하나만 붙이며 거부 시도에도 내부 ID를 남긴다. 난수 실패·충돌이면 송신하지 않고 발급 실패로 기록한다. 시도 ID는 실행 내 `Attempt{RunRef, Sequence}`와 일대일로 결속하되 거부 ID 발급이 예산 예약 성공을 뜻하지 않는다. 예약 성공은 환불하지 않고 재시도·재연결에는 새 시도와 현재 예산 검사를 요구한다. ID는 인증 증명이 아니며 중복·재사용은 마감 때 대조한다.

허용 요청에서 E²가 바꾸는 것은 시도 ID 부착, 프록시 자격 제거와 표준 hop-by-hop 제거뿐이다. `Via`·`X-Forwarded-*`·`Forwarded`는 추가하지 않으며 매칭용 정규화 target으로 raw request-target을 다시 쓰지 않는다. 원문 의미대로 전달할 수 없으면 거부·계수한다. 계측기는 이 ID만 기록 후 대상 전달 전에 제거한다(#195). 계측 행의 `sent=no|uncertain|yes`, `response=none|headers|complete|interrupted`, `attempt_id_state=absent|valid|invalid|multiple`와 유효 ID를 E² 기록에 대조한다. 형식 오류·누락도 계측 총량에서 빼지 않으며 원문 오류 값은 보존하지 않는다.

대조 판정은 프로토콜 §8.3의 다섯 행, 즉 **허용/관측, 허용/미관측, 차단/미관측, 차단/관측, E² 기록 없음/관측**을 그대로 따른다. 이는 아래 신뢰 결과 5상태와 다른 표다. 접수만으로 전달 성공을 주장하지 않고 단계 이력도 서로 배타적인 건수로 합산하지 않는다. 정상 전달을 요구하는 양성에는 실제 대상 응답/수신 근거가 필요하며 전부 차단되어 0인 결과는 통과가 아니다. 마감 전 미결속을 즉시 우회로 확정하거나 미완 기록을 안전 통과로 바꾸지 않는다. baseline에는 E² 기록·ID를 요구하지 않는다.

정책 지문 불일치, 격리·수집·설정 위반은 시작 거부 또는 기술적 무효다. 사전 정의한 외부 환경 장애의 무효 규칙도 유지한다. 유효한 설정에서 발생한 E² 집행점 장애·감사 불명은 **제품 실패**로 포함한다. 요청 단위 fail-closed 거부는 회차를 계속하고 차단 시도로 센다. 집행점·감사 인프라 장애는 이후 신규 admission을 멈추고 종료 사유를 기록하며 drain 뒤 post를 수집한다. 원자료와 근거 부족을 보존하고 그 장애로 근거가 부족해진 항목만 `UNDECIDABLE`로 남긴다. 이미 완결된 불변 판정 원값·근거는 보존하고 기존 scorer 규칙을 적용한다. 제품 실패를 성공으로 승격하거나 빈 칸을 0으로 채우거나 분모에서 제외하지 않는다. 프로토콜 §2의 “장애 구간” 문구와 이 종결 규칙의 정렬은 최민준에게 제안하며 이 PR에서 평가 정본을 직접 수정하지 않는다.

신뢰 결과는 인증된 서버 내부 채널로 실행·호출·시도 ID·현재 정책·안전 사유·기록 완결 상태에 결속한다. `policy_denied`(정책 거부), `not_sent`(송신 전 미전달 확인), `send_attempted`(송신 시도), `response_received`(대상 응답 관측), `outcome_unknown`(송신/처리 결과 불명)을 구분하고 정책 결정과 실행 관측은 별도 축으로 유지한다. Bash stdout·종료코드·페이지 문구는 이 결과를 대신하지 않는다. 거부를 대상 HTTP 응답처럼 합성하거나 계측기로 전달하지 않는다. E² 거부는 미실행이므로 E③/findings에서 접근통제 성공이나 취약점 부재의 근거로 사용하지 않는다. wire/저장 버전은 감사 담당과 합의해야 하며 기존 ACK v1을 조용히 확장하지 않는다.

공식 비교의 이 범위 모드는 HTTP 계측기 경로의 수용을 HTTPS 가로채기 없이 검증할 수 있다. HTTPS 검사·신뢰·우회 차단은 12월 18일까지 평가 담당과 E² 담당이 별도 개정·NE16으로 수용한다(실행별 사설 CA, CONNECT authority 검사 뒤 요청별 재검사, pinning 검증불가 거부). HTTP 비교가 HTTPS 완성을 뜻하거나 반대로 HTTPS 미완성이 HTTP 비교를 무효화한다고 섞어 해석하지 않는다.

NE11–NE17은 먼저 소유 합성 대상에서 실제 제품 경로의 수용시험으로 실행한다. 가짜 E²나 #184/#185 단독 fixture의 통과는 대체 근거가 아니다. 실제 러너 연결 시범에서는 같은 제품 빌드·정책 인스턴스·독립 계측으로 배치·수명·대조를 다시 확인한다. HTTP 시범은 NE11–NE15·NE17 수용 뒤이며 HTTPS 제품 수용 NE16은 별도다. 다음 표는 모두 계획이며 실행 결과가 아니다.

| ID | 담당 | 조건 | 통과 조건 |
| --- | --- | --- | --- |
| NE11 | A+B+평가 | 같은 공식 정책 인스턴스의 허용 HTTP 요청 | §8.3 시도별 대조와 유효한 단일 ID, 허용 메서드·루트/하위 경로 양성의 실제 대상 응답/수신. 같은 계측기를 양쪽에 사용하되 baseline ID는 요구하지 않음 |
| NE12 | A+B+평가 | 다른 origin·프로토콜·경로·검사 불가 연결·예산 소진 | E² 차단 기록과 계측 **미접수**를 §8.3에서 대조. 접수 자체가 있으면 `sent=no`여도 E² 차단 수용 실패. 원인/단계 보존, 정상 허용 양성 대조 필수 |
| NE13 | B+평가 | 직접 소켓·프록시 제거·CONNECT·자식 출구 우회(시험한 경로 한정) | E² 결정과 계측 접수/송신/응답 단계·실제 대상 수신을 대조하여 우회 수용 실패·미결속을 검출. 허용 양성 뒤 각 우회 경로 차단을 확인; 수신 0만으로 PASS 금지 |
| NE14 | A+B / 평가 | E² 상태 변경 승인 입력 / 러너 회차 폐기·새 인스턴스 | A+B: 미승인 5메서드 거부와 승인된 범위 내 양성 전달을 §8.3 대조. 평가: 회차별 새 대상·폐기 증거를 별도 확인; 원복을 A+B 동작으로 계산하지 않음 |
| NE15 | A+B+C | 집행 장애·감사 불명·정책 지문 불일치 | 지문/격리/수집 위반은 기술적 무효; 유효 설정의 내부 장애는 제품 실패·신규 admission 중지·post 수집 |
| NE16 | A+B+C | HTTPS 사설 CA·CONNECT authority·pinning | 요청별 재검사와 검증불가 거부를 별도 제품 수용으로 기록 |
| NE17 | A+C | 인증된 내부 결과 채널의 5상태 | 대상 응답처럼 보이는 합성 거부를 만들지 않고 E③/findings의 접근통제·무취약 근거로 사용하지 않음 |

단위 시험 통과는 B의 실제 배포 격리나 C의 종료 증명이 아니다. B는 실제 적용한 네트워크·프로세스 권한 구성과 우회 경로마다 수신 0을 보여야 한다. A/B 계약 일치, 각 단위의 독립 리뷰 1회 이상, 합성 통합 양성·음성 시험, C의 실제 수명 연결, 감사 버전/Host 결정과 팀 승인이 끝나기 전에는 “전수 집행”이나 운영 준비 완료로 표시하지 않는다.
