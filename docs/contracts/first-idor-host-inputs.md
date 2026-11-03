# 첫 실제 IDOR 회차의 입력·공급 주체 대장

2026-11-03 · 이동찬 · 구현/실제 공급 확인을 구분하는 통합 준비 대장

## 기준

현재 dev `d68d78e3d69be5a4d029a1ae7b274d1e6bf599ab`와 열린 구현을 대조했다.
[#142](https://github.com/ABC2LAB/ABCDEF2LAB/pull/142) `28101e583ac8ed088f3aa8c412ac07363c07164e`는
엔진→E②→E③ 최초 저장 연결, [#143](https://github.com/ABC2LAB/ABCDEF2LAB/pull/143)
`49f9684375c9a8c4ba1f6eb6f57dcd20d012e9a4`는 최종 감사 Source 어댑터다.
둘은 아직 열린 선행이며, 아래 **코드가 있음**을 실제 자료·운영 배치 검증 완료로 해석하지 않는다.

이 문서는 발급 권한·주체를 새로 지정하거나 실제 운영자의 권한을 부여하지 않는다.
공급 주체 열은 기존 개발 계획·계약의 소유 경계와 확인 창구다. 실제 producer의
프로세스/서비스·인증 identity·연락 책임자는 해당 담당자와 설치 환경에서 확인해야 한다.
시험 fake·임의 JSON·참조 문자열을 실제 producer로 승격하지 않는다.

## 입력과 제공 주체

| 필요한 실제 입력/증명 | 코드의 소비 경계 · 현재 준비 | 기존 공급/확인 창구 · 아직 확인할 것 |
| --- | --- | --- |
| 실제 task/intent 소유 관계, paused/비실행 상태, DB mode revision·private marker | #142 `ControlledTaskModeHost`와 등록 coordinator, `db.TaskModeReader`. unknown은 차단. | 이동찬(서버/DB): 사용할 실제 task/intent와 DB identity·저장소 배치. 코드 시험 통과와 별개로 실제 대상 행·전환을 확인. |
| 장기 `RootAnchor`와 `CheckRoot(ctx)`가 검증하는 배타 writer·DB/state 세대·복원 연속성 | #142 host가 borrowed fd와 live callback을 요구. inode/0700만으로 신뢰를 생성하지 않음. | 이동찬(호스트 구현·운영 책임 겸임 **권장안**, MD02 팀 확인 필요) 및 실제 운영자/기동기: 검증된 배치 참조·허용 writer·복원 이력과 fd 수명. 재시작 시 새 경로를 자동 승인하지 않음. |
| `CheckTaskQuiescent(ctx, taskID)`의 독립적인 planner/예약 Worker/chat/자식 실행 종료 증명 | #142는 빠른 검증 callback만 소비. 기존 `waitTaskQuiescent`·inflight=0이 모든 자식 종료의 증명은 아님. | 이동찬(실행 supervisor/기동기): 실제 추적·격리 경계와 종료 handle. callback 안에서 잠금에 걸린 drain을 시작하지 않고 전환 전에 종료를 확립. 실제 OS 배치/권한 변경은 별도 범위. |
| A/B의 검증된 비민감 계정/세션 참조와 만료, 실제 인증 자료의 보호된 전달 | `transmit.Session{Account, ExpiresAt, Headers}`를 서버 snapshot이 공급. #142의 후보/관찰/Source 문맥도 같은 참조. | 실제 인증/fixture 공급자·운영자, 이경준/윤재상(KG·UI 등록), 최민준(검증기 문맥): 계정별 인증 확인 근거·정확한 handle·유효기간. `Status=verified`를 문자열로 채우는 것은 신원 확인이 아님. |
| A의 같은 비민감 객체에 대한 B 조회를 허용한 자원별 실행 승인 | `transmit.Resource`의 전체 `Ref`, exact URL/등록 origin·IP, `ApprovalRef`, ReadOnly, Accounts, 응답 투영. 승인된 GET도 부수효과 없는 조회인지 별도 확인. | 운영자/대상 소유자(시험 승인), 이경준/윤재상(등록/UI·KG), 이동찬(E② 검증): 정확한 자원·계정·범위·비변경성·만료. URL/id/type 이름을 보고 권한을 추정하지 않음. |
| 실행별 요청 상한·시각/만료·간격·timeout·응답 크기·중지 조건 | 기존 E② snapshot/HTTP `Config`가 강제. 첫 회차의 값은 실제 승인자가 공급하며 코드 기본값에서 승인으로 바꾸지 않음. | 운영자와 이동찬: 회차에 쓸 구체 값 및 신뢰 host 시계. 최초 owner/cross GET에 필요한 예산과 차단/취소 처리를 함께 확인. |
| 동결한 KG 정책·소유/공유/위임·유효기간, canary 등록과 snapshot ID/version/digest | `idor.PolicyResolver`·`transmit.VerifyRead`와 #142 `ControlledReadFindingPlan`/`e3record.Source`. `ResolvePolicy/Canaries` 검색에서 dev에는 검증기 인터페이스·전송 wrapper·시험 대역이 있으며 실제 KG 공급부는 별도 연결. | 이경준(KG), 윤재상(보조), 최민준(판정 의미/대리 계약 검토): **실제** reader와 내용 digest·generation 결속. #130 대리 문서 검토는 실 KG API/자료 확인 완료가 아님. |
| 실제 `Source.ExplorationID/ModeVersion`, 불변 `EvidenceRevision`과 표시 심각도 `Severity`, 선택적 실제 traffic evidence ID | #142 Source/Prepare가 양의 DB ID·mode/revision과 허용 severity를 검사하고 저장 transaction에서 소유·mode를 대조. KG version과 EvidenceRevision은 별개. traffic ID가 없으면 비워 두며 임의 row를 만들지 않음. | 이동찬(신뢰 서버 metadata 발행·DB 대조), 최민준(판정 의미): 실제 task/exploration·fence revision, 같은 관찰 묶음의 근거 revision과 승인된 표시 metadata. 모델/클라이언트가 준 revision·심각도를 그대로 정본으로 쓰지 않음. |
| `EvidenceDigest/RecordCreatedAt/Completed/Finalized`와 실제 동결 Policy/CanaryFields의 결합 | #142 private collector가 실제 owner/cross 관찰 digest·별도 수집 시각을 만들고, engine 완료 claim과 필수 감사 마감 결과를 결합. Source의 Policy/CanaryFields는 같은 KG 동결값이어야 함. | **운영자 입력이 아니라 실제 collector/engine의 생산값**. 이동찬(배선), 최민준(증거 의미): callback은 이 필드를 비워 두거나 같은 실제 생산값만 전달. 관찰 시각을 생성 시각으로 쓰거나 `completed=true`를 채워 대신하지 않음. |
| reset/health 성공 뒤 하네스가 발급한 불변 준비 묶음·세대 참조, 대상 instance, 별도 reset generation | #142 Source의 GenerationRef/InstanceRef/ResetGeneration과 KG snapshot 문맥. RunRef/ResetRef 문자열에서 하네스 토큰을 만들지 않음. | 윤재상의 fixture/평가 하네스(**기존 canary 계약의 발급 제안·확인 필요**), 최민준(평가·검증 계약), 이경준(KG 결속 저장): 실제 reset/health 결과와 같은 묶음의 전달. KG는 세대 토큰을 발급·재결속하지 않음. |
| 현재 인증된 store identity/epoch·publisher/실행 결속·권한·**sealed phase**의 최신 checkpoint 및 비민감 참조 | #143 `LatestCheckpoint(ctx, scope)`가 필수. 이번 `auditlog.VerifyExtension`은 같은 snapshot의 prefix 포함만 검사. | 이동찬(CPD publisher/store/adapter)과 실제 운영자, 최민준(무결성 리뷰): 실제 정본 서비스·transport/인증·복원 anchor·동기 ACK/마감 상태. sidecar나 MAC/Close 호출만으로 현재 sealed 권위를 만들지 않음. |
| 실행 snapshot의 KeyRef/KeyFile에 결속된 journal 생성 키, 이후 JournalID/전체 scope/KeyRef에 결속된 검증 키와 현재 신뢰 상태 | #142 실행 snapshot은 보호 파일에서 키를 받아 journal을 만들지만 시작 시 KLD 활성 상태 확인 producer는 별도 필요. #143 `ResolveKey`는 **소비 시점** 신뢰를 I/O 전후 확인하며 reader 소유 사본만 사용. | 이동찬(KLD 공급/회수·실행 admission), 운영자/secret provider, 최민준(해석 리뷰): 실제 생성 키의 출처·불변 secret version mapping·시작 시 active 검증과 과거 소비 허용을 구분. 보존 키의 MAC 검증이 신규 admission 권한은 아님. 실제 registry 인증/API·보존/파기 권한은 팀·운영 확인 항목. |
| 실제 provider/model/profile·sampling과 E③ ModelTag의 일치 | 기존 로컬 LLM manifest/file loader·서버 profile 경로 및 #142 Source. manifest가 있어도 실제 모델 응답/선택을 확인해야 함. | 운영자/모델 구동자와 이동찬(서버), 최민준(평가 조건): 실제 provider/model 식별자·temperature/seed 등 설정. 합성 fake provider 이름을 ModelTag 정본으로 사용하지 않음. |
| 설정으로 고정한 Juice Shop v15.0.0의 실제 instance/reset, 접속 origin/IP 및 조회 준비 완료 | target 설정·fixture·E② 등록이 같은 대상을 가리켜야 함. 코드에 앱 endpoint·계정·포트를 고정하지 않음. | 윤재상(fixture/대상), 최민준(평가 조건), 실제 실행 운영자: 이미지/버전·reset/health·비민감 객체와 A/B 준비. 이번 작업에서는 실제 환경 준비를 확인하지 않았음. |
| 평가 회차인 경우 별도 `eval_run_id`, 신뢰 러너 기록의 run_id/model/reset 결속 | #142 Source·발급 저장이 비평가 실행에서는 EvalRunID 없음 허용. 평가 시 E② execution ID에서 추론하지 않음. | 최민준(러너·평가 계약): 실제 서명 회차/설정. 비평가 첫 통합 진단인지 평가 회차인지 명시하며 비평가 결과를 공식 평가로 계산하지 않음. |

## 공유예산을 활성화하는 경우의 추가 연결

`Resource.ApprovalRef`와 누적 `BudgetRef`는 다르다. 공유예산 도입을 선택한 경로에서는
승인 연결 실패를 `CumulativeBudget=nil`로 바꾸지 않는다.

| 추가 입력/연결 | 현재 있는 코드 | 공급 주체·남은 경계 |
| --- | --- | --- |
| 인증된 운영자의 명시 결정·task/policy/reset/한도/기간, 고정된 승인 요청 참조 | `db.BudgetAuthority.Issue`의 영속 준비→Ledger 등록→ready 게시, 운영자 로컬 IPC/CLI 코어 | 사용자가 선택한 운영자 전용 로컬 발급. 운영자/대상 승인 주체, 이동찬(검증·조립). JWT 단일 계정이 별도 예산 발급 권한을 뜻하지 않음. |
| 실제 server/operator/model UID 분리와 신뢰 기동기의 모델 실행 신원 | `budgetlocal.Listen`은 서로 다른 UID를 요구. 현재 제품 기동에는 소켓 서버 미등록. | 운영자/신뢰 기동기와 이동찬: 실제 OS 신원·0755 소켓 디렉터리·보호된 CLI config를 확인. 시험의 비공개 동일 UID helper는 운영 근거가 아님. |
| 이미 ready인 정확한 Key/Binding을 새 execution에 고정하고 영속 성공 뒤 같은 Ledger 사용 | `BudgetAuthority.BindRun`이 fresh RunRef·출처·만료를 저장. 현재 #142 runtime은 여전히 별도 RunRef를 만들며 공유예산 서버 배선은 범위 밖. | 이동찬: 실제 서버 실행 gate·철회/authority 상실 시 추적·StopAndWait·동일 Scope/journal/HTTP 연결 및 SB 수용시험. `Resolve`의 Ledger를 실행 입력으로 우회 사용하지 않음. |

이 대장이 공유예산 운영 활성화·OS 권한 변경을 승인하지 않는다. 첫 회차에서 사용할
예산 방식과 실제 발급/집행 근거를 확인하고, 없는 승인을 새 참조로 만들어 채우지 않는다.

## 실제 회차 착수와 결과 확인

1. 담당자별 실제 producer와 입력의 출처·시각/만료·전체 문맥을 확보한다. 위 표의 미확인
   항목을 시험 fake나 `true` callback으로 대체하지 않는다. transport/인증·권한·복원/격리의
   미확정 보안 결정은 해당 팀/운영 범위에서 확인한다.
2. 사용할 최신 선행 SHA와 고유 변경을 기록하고, 검증된 Source·mode·감사·예산 경계를
   호스트 bootstrap에서 연결한다. 복구·admission 이전의 구성 순서를 유지한다.
3. 한 회차만 순차 실행한다. A 대조 조회와 B 조회의 전송/영수증·관찰, 필수 감사 마감,
   판정/issuance/link·finding 저장과 기존 결과 조회를 같은 execution 문맥으로 대조한다.
4. 미실행/실패·결과 불명·UNDECIDABLE을 정상 완료나 취약점 없음으로 바꾸지 않는다.
   합성 시험과 실제 진단, 비평가와 공식 평가의 결과를 각각 기록한다.

이 변경에서 실제 Juice Shop·운영 권한·실제 키/승인 발급은 실행하지 않았다.
