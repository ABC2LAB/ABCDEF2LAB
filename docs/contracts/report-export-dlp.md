# 공유용 리포트 export DLP

상태: 첫 구현안. 사용자가 선택한 기준은 **원문 전체 마스킹, 안전 메타데이터·증거 참조 유지**다. 자유문장에서 비밀값만 골라낸다고 가정하지 않는다. 재현용 PoC 구조와 선택적 공개는 별도 설계다.

## 적용 경계

- 그래프 보고서 `report.Markdown`과 `GET /api/report`.
- `GET /api/exploration/findings/export`의 `md-single`, `md-zip`, `csv`, `json`.
- 독립 호출 가능한 `FindingsMarkdown`, `SingleFindingMarkdown`, `FindingsCSV`, `FindingFilename`.
- ZIP의 디렉터리·Markdown·manifest·요청/응답 안내 파일까지 포함한다.

조회 화면의 일반 DTO, 원본 트래픽 열람/다운로드 API, archive/restore, DB 저장·과거 행, 모델 입력, 일반 로그 전체에 적용하는 정책은 아니다. 기존 권한 검사와 E② 실행 통제도 별개다. 원본 판정 근거를 마스킹된 출력으로 덮어쓰지 않는다.

## 필드 정책

| 입력 | 공유 사본 |
|---|---|
| 작업 제목·목표·설명, 취약점 이름·유형·요약·증거·상세 보고서·PoC | 비어 있지 않으면 `[REDACTED]`, 빈 값은 빈 값 |
| URL 전체, 트래픽 note·content type·요청/응답 헤더 | 같은 전체 마스킹. host·path·query·fragment·계정 값도 남기지 않음 |
| 원문 요청/응답 본문 | 읽거나 복사하지 않음. ZIP `.http`는 `[REDACTED]` 안내만, `.bin`은 제외 |
| Snapshot ID·source traffic ID·본문 hash | 전체 마스킹. 증거 결속 검증용 원본 hash를 공유하지 않음 |
| 심각도 | `critical`, `high`, `medium`, `low`, `info`만 유지. 그 외/누락은 `[REDACTED]` |
| 상태 | DB의 고정 triage 상태만 유지. 그 외/누락을 `pending`·`confirmed`로 추정하지 않음 |
| 트래픽 역할 | `baseline`, `proof`, `verification`, `supporting`만 유지 |
| HTTP 메서드 | `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS`, `CONNECT`, `TRACE`만 유지 |
| HTTP 상태 코드 | 미관측 `0` 또는 `100..599`만 허용. 나머지는 export 차단 |
| 내부 finding/task/node/asset/binding ID, 증거 버전·순서·길이·시각 | 검증한 숫자/시각 유지. 입력 소유권·접근권한을 새로 증명한다는 뜻은 아님 |
| JSON 자산 | 내부 숫자 ID와 `[REDACTED]` type/label만. 원본 자산 조회를 하지 않음 |
| 파일명 | 고정 심각도 라벨 + `finding` + 내부 숫자 ID. 원문 제목을 정규식 치환해 사용하지 않음 |

메타데이터 허용 목록은 대상이나 모델이 확장할 수 없다. 허용된 메서드·상태 자체에 데이터를 인코딩하는 은닉 채널까지 막는 정책은 아니다. 숫자 참조와 시각도 조직의 공개 범위에 맞는지 배포 시 검토해야 한다.

Markdown은 공유용 사본임을 알리고 원문 PoC가 재현 가능한 요청이라고 표시하지 않는다. 증거 버전 불일치 안내, 트래픽 binding ID와 요청/응답 안내 파일 링크는 유지한다. CSV의 기존 열과 UTF-8 BOM, JSON의 기존 배열 형태를 유지한다. 미분류 심각도는 집계에서 별도로 표시한다.

## 구현과 오류 처리

`report.ProjectFindings`는 새 `DBFinding`·binding·snapshot을 허용 필드만으로 구성한다. 포인터·slice도 소유한 사본으로 만들고 원본을 수정하지 않는다. 미래에 DB 타입에 필드가 추가되어도 얕은 복사로 원문이 따라 나가지 않게 한다. 자유문장을 직렬화하거나 정규식으로 검사한 뒤 허용하지 않는다.

그래프 finding payload에는 공통 `dlp.Policy`의 닫힌 JSON schema를 적용한다. `vulnclass`, `name`, `summary`, `evidence`는 전체 마스킹하고 `severity`는 고정 enum이다. 잘못된 JSON·중복/미등록 키·잘못된 enum·한도 초과는 고정 차단 안내로 끝난다. 새 payload 필드는 별도 정책 분류 없이 출력되지 않는다. 자산 집계는 고정 type 이름만 유지하고 그 밖의 이름은 `other`로 합산한다.

DB 메타데이터의 nil finding, 음수 참조/버전/길이/순서/`captured_at`, 다른 finding 소유 binding, 잘못된 HTTP 상태·표현 불가능한 시각은 빈 투영과 고정 `report.ErrExportBlocked`로 차단한다. 일부 항목만 성공한 사본이나 원문 fallback은 반환하지 않는다. 빈 목록은 정상이다. 0인 참조는 미지정 값이며 존재하는 참조로 추정하지 않는다.

내부 구조 상한은 finding 10,000건, 전체 binding 10,000건, 전체 자산 참조 10,000건이다. 그래프 payload는 건당 128 KiB·전체 8 MiB, 깊이 16·객체당 32필드·배열당 256원소다. 그래프 출력 투영은 건당 4 KiB, 자산 집계는 10,000개 type 입력·합계별 PostgreSQL INTEGER 비음수 범위로 제한한다. 시각은 UTC의 year `0..9999`를 허용한다. 상한 초과 시 자르거나 일부 결과만 내보내지 않는다.

서버는 지원하지 않는 형식/잘못된 scope·ID를 DB 접근 전에 고정 400으로 거부한다. DB·파일·ZIP 오류에도 원래 오류 문자열·쿼리값·경로를 응답하지 않는다. 증거 동결은 기존 transaction에서 유지하되 원본 body 복사 옵션은 끈다. JSON 변환과 ZIP 생성·CRC 검증을 끝내고 취소를 재확인한 다음 다운로드 헤더를 설정한다. 응답 전 취소는 고정 408, 투영 실패는 422이며 증거 동결의 기존 404/409 분류는 유지한다.

렌더러의 기존 반환 시그니처는 유지한다. 차단된 독립 Markdown/CSV 렌더링은 고정 안내 문자열이며 성공 보고서나 빈 취약점 결과로 표시하지 않는다. 그래프 HTTP API도 이 고정 안내를 반환한다. 원본 DB 조회 오류는 고정 500이며 '취약점 없음'으로 대체하지 않는다. 전송 시작 뒤 I/O 오류·취소는 이미 전달한 안전한 바이트를 회수할 수 없다.

HTTP reader는 SQL의 `LIMIT 10001`로 초과 확인용 한 건까지만 읽고, 10,000건을 넘으면 evidence staging 전에 고정 422로 거부한다. selected 요청은 ID를 순차 파싱하며 10,000개를 넘으면 DB 접근 전에 같은 고정 오류로 거부한다. 기존 `ListFindingsForExport` 시그니처는 유지하고 이 출력 경로에서만 bounded reader를 사용한다. 이 행 수 제한이 단일 DB 필드의 크기나 DB 내부 정렬 비용까지 제한한다는 뜻은 아니다.

그래프 Markdown 보고서(`/api/report`)는 기존 경계를 유지해 finding 최대 1,000건, 자산 type별 최대 100,000건을 허용한다. finding은 상한보다 한 건 많은 sentinel까지 SQL에서 읽는다. 자산은 기존 `CountByType`으로 개수만 조회하여 본문·extra 등 전체 행을 메모리에 만들지 않는다. 어느 경계든 초과하면 Markdown을 만들거나 일부 결과를 내보내지 않고 `report export limit exceeded` 고정 422로 거부한다. COUNT의 DB 내부 스캔 비용이나 개별 finding payload의 바이트 크기까지 제한하는 것은 아니다.

## 내부 재현 보고서와의 관계

[실무형 보고서 요구](../llm-adoption-and-improvement-plan.md#6-실무형-재현-보고서와-실제-스크린샷)는
실제 수행 단계·전제·관찰·수정 권고와 증거 참조·실제 스크린샷을 연결하는 후속 설계다.
현재 공유 사본은 원문과 PoC를 마스킹하므로 완전한 내부 재현 보고서와 목적이 다르다.
이 요구를 이유로 위 보호 정책을 제거하거나 원문·스크린샷을 공유 export에 자동 첨부하지 않는다.

내부 재현용과 공유용 출력의 접근권한·비밀 처리·캡처 보관·마스킹·참조 검증은 별도 합의·구현이 필요하다.
출력 프로그램이 실제 캡처를 검증해 번호·캡션과 함께 삽입하고, 요청·응답 렌더링 이미지는 웹페이지 캡처와 구분한다.
생성 이미지·없는 화면·미실행 단계로 증거를 채우지 않는다. 새 로그인·요청·상태 변경이 필요한 캡처는
새 실행으로 승인·E② 통제를 적용한다. 이미지 이해 모델·OCR·비전 파인튜닝은 필수 조건이 아니다.

## 검증 기준

합성 canary를 자유 필드·URL·헤더·hash·임의 enum에 넣고 Markdown·CSV·디코딩한 JSON·압축 해제한 ZIP·파일명 어디에도 남지 않는지 확인한다. 알려진 상태·심각도·HTTP 코드·참조·버전은 대조 사례로 유지한다. 원본 구조·참조 배열·DB 값·증거 파일은 export 전후 동일해야 한다.

기존 raw binary export 시험은 공유 사본의 마스킹·원본 reader의 보존을 확인하도록 변경한다. 마스킹 export는 원문 body를 읽지 않으므로 body 부재/손상만으로 실패하지 않으며, 원본 reader의 무결성 검사는 그대로 유지된다. 실제 DB 통합 실행 여부와 건수는 구현 검증 결과에서 별도로 보고한다.

## 유형 독립적 종합 상세 경로

기존 공유 export 정책은 유지한다. 별도 `/api/report/diagnostic`의 범위·상태 집계·내부/공유 선택·보호 한계는 [보고서 구현 경계](../llm-adoption-and-improvement-plan.md#유형-독립적-결과-수집과-종합-출력)를 따른다. 단일 관리자 인증은 고객별 다중 테넌트 권한 모델을 의미하지 않는다. 상세 출력은 비밀이 없다는 보증이나 외부 공유 승인으로 해석하지 않는다.

## 조치·복사용 프롬프트 경계

기술별 조치 artifact는 자유 증거 원문이 아니라 닫힌 기술/유형 메타데이터에서 생성한다. 조회·복사·종합 HTML은 같은 저장 bundle의 원본 fingerprint와 생성기 버전을 검증한다. stale bundle의 본문은 내보내거나 복사하지 않는다. 기본 공유 사본은 조치·프롬프트·원본 digest를 제외한다. 내부 모드는 명시적 선택과 기존 관리자 인증을 필요로 하며 외부 개발 AI에 대한 실행 권한을 부여하지 않는다. 세부 API·잔여 범위는 [저장된 기술별 조치 계약](../llm-adoption-and-improvement-plan.md#저장된-기술별-조치와-ai-수정-프롬프트)을 따른다.

### 독립 수정 작업 맥락

내부 생성 API는 선택적 `context` 객체를 받는다. 필드는 `component`, `component_role`, `endpoint`, `prerequisites`, `observation`, `expected_behavior`, `prohibited_behavior`, `source`다. 서버는 사용자 입력의 source를 `user_provided`로 강제하고 생성기가 비민감·길이·지시문 방어 정규화한 값만 저장한다. 사용자는 고객 원문이나 비밀 대신 검토한 비민감 요약만 입력해야 한다. 원본 실행의 digest와 사용자 맥락의 입력 digest는 별도로 결속되며 사용자 요약을 자동 검증 관찰로 승격하지 않는다.

내부 목록의 `context`는 현재 원본에서 구조화 가능한 상태 요약이다. 저장된 작업 맥락은 `record.bundle.context`에 있다. 공유 응답은 두 본문 모두 제외한다. 종합 HTML과 복사 프롬프트는 저장된 같은 맥락을 사용한다. 기존 Bash/E3 자료에 구조화된 영향 위치가 없으면 이를 추측하거나 원문 전체를 옮기지 않고 미확인으로 표시한다. 개발용 AI는 실제 저장소에서 해당 처리 경로와 컴포넌트 기술을 확인해 코드·설정을 수정하고 정상/취약 동작 회귀시험 및 남은 확인 사항을 보고해야 한다. 생성 자체는 코드 수정·배포 승인이나 해결 완료가 아니다.
