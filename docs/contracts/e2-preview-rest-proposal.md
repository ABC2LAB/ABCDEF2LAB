# E② 실행 전 HTTP Preview REST·수명 계약 초안

2026-11-03 · 이동찬 · **팀 리뷰 요청용 제안, 미확정**

이 문서는 [HTTP Preview 코어 계약](./e2-http-preview.md)을 서버의 실행 전 확인에 연결하기 위한 초안이다. 현재 `dev` `9b06c05cc4d6e51834850473e660f566730e4587`에는 `HTTPTransmitter.Preview`와 그 단위 시험이 있다. 서버 REST·사전 준비된 전송기·UI는 아직 없다. 기존 코어 계약의 “Preview 미구현” 문구와 옛 SHA는 작성 당시 상태이며 현재 코드의 사실을 뜻하지 않는다. 이 초안은 구현·운영 활성화·팀 합의를 완료했다는 뜻이 아니다.

## 1. 현재 경계와 결정 대상

현재 `registerControlledReadHTTP`는 일시정지된 task/intent에 private snapshot을 등록하고 `registered`를 반환한다. `prepareControlledRead`는 **Worker 실행 중** 새 RunRef·journal·전송기를 만든다. 따라서 활성 lease에 REST를 붙여도 운영자가 **첫 송신 전에** 안정적으로 Preview할 기회가 없다. 현재 등록 응답도 RunRef를 제공하지 않는다.

| 선택지 | 의미 | 검토 의견 |
| --- | --- | --- |
| A. 사전 준비 lease | task가 paused인 등록 단계에서 전송기까지 준비해 보관하고, Preview 뒤 재개가 **같은 인스턴스**를 소비 | **권장 제안**. 기존 “실행 전 확인” 목적과 `Preview`의 기존 인스턴스 조건을 함께 충족하지만 등록·취소 수명 변경과 검증이 필요 |
| B. 활성 실행 진단 | 지금처럼 Worker가 만든 전송기를 실행 중 REST에서 조회 | 구현 범위는 작지만 첫 송신 전 운영자 확인이 아니므로 별도 “진단” 기능으로만 이름 붙일 수 있음 |
| C. 일회성 검증 전송기 | Preview 요청마다 snapshot으로 새 전송기를 만들고 버림 | 이후 실제 실행 인스턴스와 다르며 private 파일 I/O·수명/예산 결속을 다시 정의해야 하므로 이 계약에는 권장하지 않음 |

아래 §2–4는 **A를 채택할 경우의 상세 제안**이다. A 채택 자체와 REST 권한은 §5의 팀 리뷰 결정이다. 등록 전 설정 편집기의 검증은 다른 기능으로 남는다.

## 2. 제안 수명: 등록 → 준비 → 선택적 Preview → 재개

1. 현재처럼 서버가 task/intent의 일시정지·비종료·비실행 상태를 확인하고 통제 marker를 먼저 영속화한다. private snapshot·계정·자원·정책을 신뢰 경계에서 읽어 검증한다. 실패해도 marker를 지워 일반 Worker로 강등하지 않는다.
2. 등록 요청 안에서 서버가 실행별 새 RunRef를 발급하고 journal/header checkpoint와 **실제 재개에 쓸** `HTTPTransmitter`를 한 번 준비한다. 준비 전송기의 context는 등록 HTTP 요청 `r.Context()`나 이미 취소된 paused work context를 재사용하지 않고, 서버/작업 수명에 묶인 **별도 준비 context**를 소유한다. 등록 응답 종료로 준비 전송기가 취소되지 않아야 하며 취소/삭제/종료 때는 이를 명시적으로 중지한다. 준비 단계의 파일/DB 작업은 `Preview` 호출의 부수효과가 아니다. 누적예산 경로가 활성화되는 후속에서는 `BindRun`의 영속 결속과 현재 authority 검증이 먼저 성공해야 하며, 새 Ledger·자동 Grant·예산 예약을 만들지 않는다. 준비 실패는 pending lease를 게시하지 않는다.
3. 준비된 lease를 task/intent에 **하나만** 보관하고 task는 계속 paused로 둔다. 후속 REST는 이 lease를 찾고 `tx.Preview`만 호출한다. Preview는 선택적 정보 조회이며 호출 여부/결과를 재개 승인 조건으로 저장하지 않는다. 기존 resume은 Preview 없이도 가능하다. 여러 번 Preview해도 RunRef·전송기·시도 번호·로컬/공유 예산·journal/checkpoint·기한이 바뀌지 않는다. 결과를 저장하거나 실행 허가로 쓰지 않는다.
4. 운영자가 기존 재개 흐름을 호출하면, 서버는 등록 때 준비한 **같은** lease/RunRef/전송기를 Worker에 단 한 번 넘긴다. 재개한 work context가 취소되면 기존 준비 전송기에 `Stop`을 전달하고 종료 시 `StopAndWait`로 drain한다. 준비 context를 work context로 교체하거나 전송기를 새로 만들지 않는다. 누적예산 모드에서는 재개 **직전** 현재 authority epoch/잠금·Grant 결속을 다시 확인하고 실행 gate와 결속해야 한다. 재개 후 실제 GET은 현재 세션·승인·만료·예산·중지·감사·health를 기존 송신 경계에서 다시 검사한다. Preview가 match였어도 송신은 거부될 수 있다.
5. 재등록·취소·삭제·보관·서버 종료·재시작은 사용하지 않은 준비 lease를 닫고 폐기한다. 준비 context와 전송기를 중지·drain하고 이미 만든 journal/DB 실행 결속은 삭제·환불하거나 다른 RunRef로 재활용하지 않는다. 송신 0건의 준비 폐기와 실제 실행 실패를 구분할 감사 종료 방식은 구현 전 리뷰가 필요하다. crash 뒤 같은 전송기/시도 순번을 복원하지 않으며 marker가 남은 작업을 일반 Worker로 재개하지 않는다.

등록과 재개, 등록 교체, Preview 조회는 같은 task/intent gate에서 순서를 정해야 한다. 동시 재개가 먼저 이기면 Preview REST는 “준비된 lease 없음”으로 거부한다. Preview가 먼저 이기면 정적 결과를 낸 뒤 재개될 수 있어 그 결과는 미래 송신 허가가 아니다. 잠금을 잡은 채 `StopAndWait`·DB·파일 작업을 하지 않도록 lock order를 별도 검토한다. 이 수명 변경은 현재 코드에 없으며 [서버 실행 계약](./e2-server-runtime.md)의 일회 pending·감사 종료와 함께 리뷰해야 한다.

## 3. 제안 REST·권한·비저장

| 항목 | v0 제안 |
| --- | --- |
| 경로 | `POST /api/tasks/{id}/intents/{iid}/controlled-read/preview` |
| 시점 | 해당 task/intent가 paused이며 사전 준비 lease가 하나 있을 때만. 실행 시작 뒤에는 이 REST를 제공하지 않음 |
| 인증 | 기존 `/api` JWT 필수. 현재 단일 계정 범위의 제안이며 예산 발급·대상 송신 권한을 부여하지 않음 |
| 본문 | 정확한 JSON 객체 `{"account_ref":"owner","resource_ref":"document:example"}`; 4096바이트 이하 |
| 전송기 입력 | 서버가 등록 사본의 handle/status를 붙이고 method=`GET`, state-changing=`false`로 고정해 **준비된** `tx.Preview`를 한 번 호출 |
| 결과 | `schema_version: 1`, 코어의 닫힌 `status`/`reason`/`method`와 등록에서 확인한 비민감 참조만 |

`id`·`iid`는 caller의 주장이다. 서버는 실제 task/intent와 준비 lease의 내부 RunRef 결속을 대조하고, 이전 등록 인스턴스를 사용하지 않는다. 등록 API가 RunRef를 반환하지 않아도 이 경로는 현재 task/intent의 유일한 준비 lease를 선택한다. 필요하다면 등록/Preview 응답에 서버 발급 RunRef를 **진단 참조**로 안전하게 투영하는 방안을 팀이 별도 선택한다. RunRef는 bearer 권한이 아니다.

요청에 URL·헤더·본문·handle·status·method·예산·승인·snapshot_ref·`Inputs`를 받지 않는다. ref 길이/문법을 검사하고 크기 제한·중복/추가 키·trailing JSON을 거부한다. 미등록 account에는 caller 라벨만 넘겨 코어의 `unknown_account`를 얻으며 verified/handle을 조립하지 않는다. 성공 시 필요한 값은 준비 lease의 서버 등록 사본에서만 복원한다. 코어의 `unsupported_action`은 Go API 시험에 남는다.

| 상황 | HTTP 응답 제안 |
| --- | --- |
| JWT 없음/실패 | 기존 인증 거부. lease·등록 조회 전 종료 |
| path/JSON 오류 | `400`, 고정 오류. 원문 반사 0 |
| 다른 task/intent, 준비되지 않음, 이미 재개/닫힘 | 모두 같은 `404`, 해당 lease/등록 존재 신호 최소화 |
| 준비 lease의 정적 match/reject | `200`, Preview 결과. reject는 송신 영수증이 아님 |
| 준비 lease가 내부적으로 unavailable | `503`, `status=unavailable`, 고정 사유·빈 참조 |

REST는 match에서만 등록된 `resource_ref`·`account_ref`·`approval_ref`를 반환하고 reject/unavailable에서는 참조를 비우는 **코어보다 좁은 투영**을 권장한다. `method`는 `GET`/`UNSUPPORTED`만 쓴다. 모든 응답에 `Cache-Control: no-store`를 붙이고 URL·IP·session handle·자격증명·canary·본문·원시 오류를 출력하지 않는다. Preview 결과는 DB·파일·journal·checkpoint·finding·평가 레코드에 저장하지 않으며 접근 로그에도 입력 본문/등록 참조를 남기지 않는다. 이 비저장 검증은 동시 실제 송신이 없는 격리 구간에서 수행한다.

현재 JWT는 별도 task별 RBAC가 아니다. 모델 도구 목록에 이 REST를 추가하지 않는다는 것은 OS 격리의 증명이 아니다. 모델 제어 Python은 별도 Credential 없이 서버 UID를 상속하고 `keyDir/jwt.key`는 서버 UID의 0600 파일이다. 실제 서명키에는 저장된 비밀번호 해시도 쓰이므로 키 파일 읽기만으로 토큰 위조가 증명되는 것은 아니다. 그러나 **모델 프로세스와 JWT/DB 자격의 분리를 검증하기 전** JWT가 모델의 관리 REST 호출이나 등록 참조 조회를 막는다고 주장할 수 없다. 운영 활성화 전 격리 근거를 확인하고, 다중 사용자 역할을 도입하면 Preview 권한과 task 소유를 따로 합의한다.

## 4. 후속 구현 수용시험 제안

아래는 **미실행 계획**이다. 현재 Preview 코어 시험을 서버 통합 통과로 세지 않는다. 합성 계정·자원과 격리 대상만 사용한다.

| ID | 조건 | 확인 |
| --- | --- | --- |
| PRV01 | task paused 등록·준비 후 반복 Preview | 같은 RunRef/전송기, 정적 match, Preview 구간 target/DNS/audit/Ledger/DB/파일 쓰기 0 |
| PRV02 | Preview 뒤 재개·정상 GET | **동일** 전송기 사용, 첫 실제 시도 번호 1, 실제 송신 gate 재검사·수신은 허가된 경우만 |
| PRV03 | 재개 전에 승인/세션/시간/공유예산 상태 변동 | 이전 match로 송신 허가 0; 실제 Send의 현재 gate가 거부 가능 |
| PRV04 | JWT 없음/실패, 잘못된 task/intent, malformed/중복/추가 JSON·URL 주입 | 인증/400/404 고정 결과, 원문·등록 정보 노출 0, 새 전송기 0 |
| PRV05 | 미등록 계정/자원·미승인 조합 | 정적 reject 사유, 반환 참조 공백, 송신/예약/감사 0 |
| PRV06 | Preview ↔ 재개·취소·재등록 경합 | 결과/거부의 단일 순서, 다른 lease 선택·race·deadlock·사용 후 접근 0; work 취소가 같은 tx를 중지 |
| PRV07 | 준비 뒤 미실행 폐기·서버 재시작 | 기존 RunRef/전송기/예산 복원 0, marker 유지, 일반 Worker fallback 0, 종료 감사 일관 |
| PRV08 | journal/checkpoint/BindRun 준비 단계 장애 | pending 게시·송신 0, 부분 준비 안전 폐기, 불확실한 DB 결속 재사용 0 |
| PRV09 | Preview 반환/접근 로그·저장 확인 | 비밀/URL/body/canary 0, Preview 자체가 만든 영속 행·예약·영수증 0 |
| PRV10 | 운영 활성화 전 모델 실행 UID·관리 자격 접근 검사 | JWT/DB 자격의 실질 분리 근거 확인; 현재 같은 UID 배포를 통과로 보고하지 않음 |
| PRV11 | 등록 HTTP 응답 직후 요청 context 취소, task는 계속 paused | 준비 tx로 Preview 가능, 재개 후 동일 tx 사용; 요청 context에 잘못 결속된 구현 검출 |
| PRV12 | 준비 뒤 긴 pause 중 authority 상실, 재개 뒤 실행 중 상실 | 재개/새 admission gate 폐쇄, 기존 전송기 StopAndWait·감사 종료, 일반 Worker/새 Ledger fallback 0 |

## 5. 팀 리뷰가 필요한 결정

1. **시점/수명:** A(사전 준비 lease, 권장 제안)의 등록·취소·재개 변경을 받아들일지. B는 실행 중 진단으로만 별도 명명할지.
2. **준비 폐기:** 송신 0건 RunRef/journal/DB 결속의 종료 기록과 재등록 시 원래 예산을 유지하는 규칙을 어떻게 표현할지. 삭제·환불·자동 재발급은 금지하는 방향을 권장한다.
3. **권한/운영:** 현재 단일 JWT는 새 OS 보안 경계가 아니다. 모델 프로세스·관리 자격 분리를 어떤 배포/시험에서 검증한 뒤 REST를 열지, 다중 사용자 권한은 누가 결정할지.
4. **와이어/UI:** 위 task/intent 경로·`schema_version: 1`·거부 시 참조 공백이 충분한지. 경준 담당 UI는 match를 “등록 일치 · 실행 시 재검사”로만 표현하고 실행 승인으로 사용하지 않는지.
5. **예산 선행:** 누적예산 모드에서 준비 시 `BindRun`뿐 아니라 재개 직전 epoch/잠금 재검사, 실행 중 authority 상실 감지→새 admission 차단→`StopAndWait`/감사 종료를 구현·시험한 뒤에만 서버 연결을 활성화하는지. 현재 Ledger만으로 DB 권한 상실을 감지하지 못하므로 이 coordinator가 없으면 누적예산 경로는 비활성으로 유지하는 방향을 권장한다. 현재 UID 분리 미검증 상태의 로컬 발급 소켓은 이 문서로 활성화하지 않는다.

팀 선택 후 서버 수명 변경·REST handler·격리된 통합시험을 별도 PR로 구현한다. 이 문서는 E③ 판정, UI, 운영 권한 변경 또는 실제 전송의 완료 근거가 아니다.
