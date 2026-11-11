# 실무형 진단 보고서 구현 계획과 경계

기준: 2026-11-11, dev `596b217caac892121d0fb1316ca1651f2fc5dddd`.
참고 양식은 사용자가 제공한 ‘웹_취약점_진단_결과보고서_v1.0_형식만.docx’다. 표지·목차·개요·수행 현황·요약·항목별 상세·부록의 서식만 참고한다. 양식의 기관·인원·취약점·일정·수치·예시 공격은 제품 자료가 아니다.

## 재사용과 누락

- `readDiagnosticReport`의 repeatable-read 조회, E③ 발급/증거 버전/digest 검증, 후보·미실행·불명·비취약 구분을 재사용한다.
- `LoadDiagnosticRemediationSource`의 호스트 소유 HTTP observation, `remediation.Context.Observations`, 저장 Bundle·동일 프롬프트·stale 검사·출력 DLP를 재사용한다. 두 번째 조치/프롬프트 생성기는 만들지 않는다.
- 기존 E③ 보고서는 원문 manifest를 검증하지만 owner/cross/판정의 구조화된 재현 절차가 없다. 저장 manifest의 결속을 재검사해 실제 관측만 단계로 투영한다. 누락된 요청·경로·사전 조건·종속성은 생성하지 않는다.
- HTML 출력의 검증/마스킹을 공통 자료 준비 함수로 분리해 DOCX도 동일한 결과·조치·프롬프트를 사용한다. Word는 Go 표준 ZIP/XML로 온프레미스에서 생성한다.
- `WorkflowReportPanels`의 레일·중앙 목록·상세·탭은 유지하며 네 섹션과 기존 조치 생성/복사 기능을 연결한다. 그래프·지도 화면을 삭제하지 않는다.

## 구현 순서

1. 실제 저장 관측·E③ manifest → 버전 결속 재현 Step 및 누락 표시.
2. 동일 보고서 자료의 5장 HTML·DOCX·내보내기 API.
3. 단계 결속 PNG 캡처 인터페이스·보호 저장·인증 조회, 픽셀 마스킹·digest 검증. 임의 파일 경로/업로드 URL은 받지 않는다.
4. Workbench 네 섹션·동일 저장 프롬프트 보기/생성/복사·Word 다운로드.
5. 판정·stale·교차 회차·DLP·출력 구조·실제 PostgreSQL·브라우저·Word 표시 검증 및 PR.

## 브라우저 캡처와 권한

현재 dev의 Browser 실행기 수용 문서는 초안이며 [#370](https://github.com/ABC2LAB/ABCDEF2LAB/pull/370)은 우회 시험 fixture다. 이를 진단 실행기로 간주하지 않는다. 캡처는 인가된 실행기의 현재 화면을 받는 별도 interface로 연결한다. 보고서 생성·조회·다운로드가 캡처·로그인·HTTP 요청·모델 호출을 실행하면 안 된다. 운영 실행기에 adapter가 연결되지 않은 실제 캡처는 NOT_RUN으로 보고한다. UI 시험 화면을 취약점 증거로 쓰지 않는다.

이미지는 task/entry/evidence version/run/call/step/evidence digest에 정확히 결속한다. capture 실패·미설정·stale와 이미지 부재를 구분한다. 허용한 비민감 픽셀 영역 밖은 저장 전에 제거하고 PNG 재인코딩으로 부가 메타데이터를 제거한다. 이미지는 내부용 인증 조회에서만 제공하고 공유 사본에서는 생략한다. 픽셀의 의미를 자동 비밀 판별했다고 주장하지 않는다. 비민감 영역 승인 없는 캡처는 보관하지 않는다.

스크린샷 미확보 문구: ‘스크린샷 미확보 — 실제 요청·응답 및 실행 기록으로 증거 제공’. 부재는 유효한 E③ 판정을 낮추거나 새 판정을 만들 근거가 아니다.

## 검증·리뷰

새 자료는 버전이 있는 저장 구조로 관리하고 기존 계약을 바꾸는 부분은 관련 담당자에게 리뷰를 요청한다. E③·모델 예산·송신 권위를 새로 발급하지 않는다. 실제 Qwen 유료 호출은 별도 키·USD 승인 전 NOT_RUN이다. 작업 중 [#368](https://github.com/ABC2LAB/ABCDEF2LAB/pull/368)의 bundle parser와 #370의 fixture가 dev에 병합되어 최신 기준으로 반영했다. 해당 구현을 복제하지 않는다.

## 구현 계약

- `reproduction.FromStored`는 호스트 소유 `observed` 관측 중 실제 시각·실행·원본 Step·증거 참조가 모두 있는 행만 사용하며 저장 순서를 유지한다. E③는 저장 manifest의 owner read / cross read / 비교 snapshot을 독립 결속 검사한 뒤 투영한다. E③ manifest에 없는 HTTP 경로·일반 call ID는 공란으로 보존한다.
- Step에는 task/entry/run/call/원본 Step/evidence version/evidence digest와 전체 정규화 관측의 SHA256 결속을 둔다. Step ID는 그 결속에서 결정한다. 내용·회차·증거가 바뀌면 이전 이미지를 재사용하지 않는다.
- `db.Open`의 기존 advisory lock·DDL 재시도 안에서 버전 있는 `diagnostic_captures_v1.sql`을 적용한다. PNG는 기존 PostgreSQL의 보호된 BYTEA 행에 저장한다. 파일 경로·URL·SVG·base64 그래프 fact를 받는 공개 업로드 API는 없다.
- 제품 runtime에는 해당 표의 SELECT/INSERT와 캡처 결과 열에 한정한 UPDATE를 추가한다. 결속 열·DELETE·TRUNCATE 권한은 주지 않는다. 공개 API·모델 키 writer에는 캡처 쓰기 권한을 주지 않는다. 기존 E③ 발급·판정·감사 표는 수정하지 않는다. 구 역할이 새 표를 읽을 수 없으면 `storage_unavailable`을 명시한다.
- `reportcapture.Collect` → sealed `Asset` → `DB.SaveDiagnosticCapture`는 현재 DB 근거에서 Step 결속을 다시 유도한 뒤 추가한다. 실패·미지원 상태는 현재 근거를 다시 확인한 뒤 성공으로 한 번 승격할 수 있다. 반복 실패는 멱등이며, 성공 캡처는 재수집으로 덮어쓰지 않는다. DB trigger는 성공 행 UPDATE·직접 DELETE·TRUNCATE와 결속 변경을 거부한다(기존 부모 task 삭제 cascade는 보존). 이미지 최대 1 MiB·4096변·8백만 픽셀, 승인 영역 최대 16개다. PNG는 재인코딩하며 조회·출력 때 digest/크기/정적 PNG chunk를 검증한다. 텍스트 입력 16 MiB와 이미지는 별도 예산으로 검사한다. 내부 내보내기의 이미지 base64 예산은 4 MiB이며 넘는 이미지만 `capacity_omitted`(용량 초과로 생략)로 바꾸고 텍스트를 유지한다. 최종 파일 상한은 두 예산을 합친 20 MiB다.
- 캡처 상태: 행 없음 `not_captured`, adapter/비민감 영역 승인 없음 `not_supported`, Source 실패 `failed`, 읽기 권한/저장소 없음 `storage_unavailable`, 공유 사본 `shared_omitted`, 이미지 예산 초과 `capacity_omitted`, 현재 결속·PNG 검증 실패 `binding_invalid`. 오래된 증거는 재현 Step·이미지·프롬프트를 반환하지 않는다.
- 조회는 `GET /api/report/diagnostic?task=<id>&audience=internal|shared&format=html|docx`. 기본은 기존 공유 HTML이다. PNG 조회는 `/api/report/diagnostic/image?task=<id>&audience=internal&entry=<id>&step=<id>&digest=<image-sha256>`이며 동일 권한·현재 버전을 검사한다. 재시작한 보고서 전용 handler에도 같은 GET을 등록한다. 조치 목록과 공유 내보내기는 PNG blob을 읽지 않고, 이미지 GET은 선택 entry의 현재 근거와 해당 이미지 한 행만 읽는다. 응답은 no-store, nosniff와 비활성 CSP를 유지한다.
- 개요의 승인 URL·모델 정책은 보호된 preparation service의 `AuthorizeTask`가 돌려준 해당 Job Scope만 사용한다. 모델 사용량은 실제 `llm_usage` 행을 별도 표시하고 기록 부재를 NOT_RUN으로 추정하지 않는다.
- 조치는 기존 명시적 POST와 CAS·영속 Bundle을 쓴다. 생성기 `remediation-v6`는 추가된 실제 E③ 단계와 Method를 반영한다. 이전 저장본은 기존 stale 검증으로 차단한다. Workbench 복사는 서버가 재검증한 entry/record/input digest/전체 프롬프트가 표시 저장본과 같을 때만 수행한다.

## 실제 캡처 adapter 연결 조건

제품 진단용 Browser 실행기 구현은 이 PR에 없다. 후속 adapter는 기존 E② 실행 권한 안의 현재 화면만 읽고, 해당 Step의 실행 당시 시각·호출·증거 참조를 보존해야 한다. 이미지를 얻기 위한 추가 탐색·로그인·재현을 하지 않으며, 호스트가 승인한 비민감 영역을 제공해야 한다. 현재 DB 증거 결속으로 저장할 수 없는 frame은 폐기한다. 임의 Source나 UI 수용 캡처를 운영 실행기에 자동 설치하지 않는다.

DOCX는 Go 표준 ZIP/XML 생성기로 A4·맑은 고딕·청회색 표·5장 구성·항목 페이지 구분·내부 PNG 관계만 생성한다. 매크로·OLE·외부 이미지·외부 문서 변환·원격 폰트 로딩이 없다. 목차는 고정 장 목록이며 페이지 번호를 추정하지 않는다.

## 로컬 검증 결과 — 2026-11-11

| 검증 | 결과와 범위 |
|---|---|
| 전체 Go race | PASS 7,612 / FAIL 0 / SKIP 18. 시작 기준 dev에서 전체 실행. 기존 소유권·별도 수용 flag·외부 모델 조건의 SKIP은 통과로 계산하지 않음 |
| 최신 dev 병합분 | `cmd/browser-fixture`, `diagnosisflow` race PASS 213 / FAIL 0 / SKIP 2(기존 DB 미제공) |
| 최종 DB·서버 보고서 회귀 | PostgreSQL에서 DB PASS 11, 서버 PASS 121 / FAIL 0 / SKIP 0. 실제 E② HTTP→저장 E③→재현 Step→저장 Bundle→동일 프롬프트·HTML·DOCX 포함 |
| 출력·PNG·Step·조치 단위 | PASS 87 / FAIL 0 / SKIP 0. 한국어·긴 프롬프트·항목 페이지 구분·XML·공유 마스킹·외부 관계 부재 포함 |
| Workbench | Vitest 39개 파일·525건 PASS, TypeScript·변경 파일 Biome·정적 제품 빌드 PASS |
| 실제 Chrome | `TestFirstDiagnosisURLBrowserIndependentProcessesOwnedPostgres` PASS. 실제 API/준비 프로세스 재시작·개량 Planner/Worker·소유 HTTPS 대상 허용1/차단1·후보1/확정0·네 섹션·명시적 조치 저장·검증된 클립보드 복사·HTML/DOCX 다운로드·모바일 폭 검사. 모델만 로컬 합성 fixture이며 Qwen 성능 수용이 아님 |
| 기존 평가·운영 | 평가 Python 1,733건, 제품 스크립트 3건, 웹 변경 판별 16건, E② 배치 55건 PASS |
| 빌드 | Go build/vet/gofmt·darwin arm64/windows amd64 크로스빌드 PASS |
| Word 앱 레이아웃 | NOT_RUN. DOCX ZIP/XML·한국어·페이지 구분·내부 PNG 관계는 시험했지만 Windows 도구가 WSL 작업 경로 오류로 시작되지 않아 Word 실제 표시 검사는 수행하지 못함 |
| 진단용 Browser 캡처·실제 Qwen | NOT_RUN. 운영 Source adapter·캡처 비민감 영역 승인 연결이 없고, 유료 모델 별도 승인·예산도 없음 |

초기 웹/평가 시험의 샌드박스 listen EPERM은 로컬 시험 권한으로 재실행해 해소했다. Chrome에서 발견한 공용 다운로드 클라이언트의 HTML 전용 형식 검사를 지정 MIME(HTML/DOCX/PNG) 검사로 확장하고, 형식 불일치 차단과 이미지 해시 검사를 회귀 시험에 추가했다. 생성 문서·PNG·인증 상태·로그·DB 자료·원본 Word 양식은 커밋하지 않는다. UI 수용 화면은 진단 캡처 증거로 사용하지 않는다.

DB/E③ 저장·권한은 최민준, Workbench 상세·복사는 이경준·윤재상의 담당 리뷰가 필요하다. Word 앱 표시 확인과 실제 E② Browser adapter 연결은 후속 수용 항목으로 남긴다. 이 PR을 자동 병합하지 않는다.

## PR #373 리뷰 반영 — 2026-11-11

- E③ manifest를 자기 자신과 비교하지 않는다. 불변 발급 행의 `binding_canonical`을 기대 Source로 읽고 task/run/case/intent/자원/정책/reset 세대 등 발급 열과 대조한 뒤, `e3record.DecodeBoundManifest`/`CheckManifest`를 보고서·저장 상세·접근통제 지도에서 공유한다. 원문 해시 검사도 유지한다.
- 근거 하나의 로드/형식/결속 오류는 `source_load_failed`와 실패 건수로 보존한다. 잘못된 후보 payload는 안전한 항목 ID와 판단불가 자리표시자로 남기며 원문·재현·프롬프트를 제공하지 않는다. 일반 실행용 후보 조회의 엄격한 거부는 유지한다. SQL 오류는 savepoint로 격리하고 snapshot commit 실패·전체 한도·기본 자료의 해시 훼손은 정상 보고서로 수용하지 않는다.
- 모든 항목의 첫 제목은 ‘① 판정과 영향’이다. 저장 상세 보고를 Workbench에서 다시 표시하고 비밀 마스킹을 유지한다. 도구 출력/Step 잘림, 기술 출처·상충, 사용자 보완과 관측, 모델 제안의 한계를 HTML·DOCX에서 함께 보존한다.
- DOCX가 지원하지 않는 XML 제어 문자는 U+FFFD로 바꾸고 대체 건수를 기록한다. 원본 증거는 수정하지 않고 XML escaping·UTF-8·기본 DLP 검사는 유지한다.
- 회귀 시험은 재시작 report-only handler의 실제 PNG, 실패/미지원→성공 재수집, 성공 행 불변성, 잘못된 발급 run/계정·manifest 버전의 항목별 격리, CRC가 유효한 PNG tEXt 거부, 이미지 예산 초과·공유 생략, Step 순번과 HTML/DOCX 경고 일치, UI 상세 복구·중립 제목을 포함한다. 실제 Browser Source·Word 앱 표시·유료 모델 수용과 구분한다.

리뷰 반영 검증: 웹 533건·정적 빌드·TypeScript·변경 파일 Biome PASS. PostgreSQL 서버 전체 race 2,008 PASS / 13 SKIP, DB 패키지 race 484 PASS / 0 SKIP이며, 추가한 후보 오류 격리·불변 trigger·재시작 이미지·독립 결속·접근통제 지도 최종 시험은 94 PASS / 0 SKIP, 출력·Step·E③·PNG·설치자 패키지 race는 362 PASS / 0 SKIP입니다. 전체 서버의 기존 수용 flag·외부 모델 SKIP을 제품 수용 완료로 계산하지 않습니다. 최신 dev 병합 뒤의 최종 전체 결과는 PR의 해당 SHA CI 기록을 따릅니다.

추가 UI 리뷰: 복사는 서버의 현재 저장 프롬프트·digest 일치를 확인하고, 화면 마스킹과 원문이 다르면 복사를 차단해 숨겨진 민감값이 클립보드로 나가지 않게 한다. Word/HTML·공유/내부별 진행·완료 상태를 구분하고, 이미지 404/409는 무결성 오류와 나누며 캡처 상태를 한국어로 표시한다. 상한은 웹의 공통 상수와 위 서버 계약을 대조한다.
