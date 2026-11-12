# API Recon 발견 저장과 탐색 입력 경계

이 문서는 P1의 API Recon 복원 부분을 다룬다. 송신 허가는 [탐색 경계 계약](exploration-boundary.md)과 기존 대상 승인 범위가 정한다. 발견 저장은 승인 origin·경로·메서드·쓰기 범위·요청 예산을 넓히지 않는다. 진단 요청의 E②·E③ 계약도 바꾸지 않는다.

## 원본과 비교한 범위

로컬에서 확인할 수 있는 원본 import는 `e53a79f`다. 프로젝트가 지정한 포크 SHA `fdfb7a17b7da5e2de716c0e98475ec77a41c64c6` 객체는 이 작업의 로컬 Git에 없어 그 SHA와의 완전한 동등성을 주장하지 않는다. 기존 스크립트의 원본 출처와 AGPL 표시는 유지한다. 원본 script를 실제 대상으로 실행한 결과는 없다.

| 원본 기능 | 재사용한 부분 | 이 변경의 실행 범위 |
|---|---|---|
| `harvest_static.py` | API dialect(`api/rest/service/gateway/graphql/vN` 등), route literal, webpack/Vite 청크 발견 방식 | 기존 Go bundle parser와 순수 정적 metadata parser. 직접 urllib, TLS 검증 해제, 고정 동시성은 실행하지 않음 |
| `spider_mpa.py` | 링크·폼 이름 발견 | 기존 Go HTML parser에 form field 이름·query 이름 수집 추가. 쿠키나 입력값 저장 없음 |
| `preload.js`, `runtime_harvest.js` | 페이지 내부 fetch/XHR hook, 동적 URL/폼/WS/SSE 구분 | 별도 `metadata_only.js`. 이미 열린 페이지의 native 호출을 그대로 전달하고 메타데이터만 기록 |
| route/permission helper | 정적 route 후보 방식 | route literal을 추정 후보로 보존. 대상별 메뉴 위조·권한 트리·로그인 성공은 구현 완료로 계산하지 않음 |

원본 runtime의 Node 직접 fetch, TLS 검증 해제, 응답·헤더·WS frame 저장을 제품 경로로 켜지 않았다. metadata-only 어댑터는 브라우저 프로세스나 네트워크를 새로 만들지 않는다. 브라우저 MCP와 탐색 Direct Bash의 실행 허용 목록·별도 실행 환경은 별도 P1 런타임 변경이 제공해야 한다.

## 추정과 실제 관측

`ReportStatic(ctx, []Discovery)`는 기존 호출자와의 호환 이름이다. `source` 생략 또는 `api_recon_static`은 정적 분석, `api_recon_runtime`은 페이지 hook·동적 보고를 뜻한다. 둘 다 **inferred**다. 선택적인 `transport`는 `http`, `sse`, `websocket`만 받는다. 모델이 runtime이라고 쓰거나 seq를 자유텍스트에 붙여도 observed로 바뀌지 않는다. 도구 입력의 seq·confidence·응답 본문·모르는 필드는 거부한다.

실제 관측의 입력은 모델·MCP 응답과 분리한 호스트 소유 `ExplorationReader`다.

```go
Session(context.Context) (ExplorationBinding, error)
ReadObservation(context.Context, ExplorationBinding, uint64) (ExplorationObservation, error)
```

세션 factory를 열기 전 `CheckExplorationTarget(ctx, binding, targetURL)`로 보호 회차의 URL과 정확히 같은 문자열인지 확인한다. origin·경로를 정규화해 다른 URL을 승인하지 않고 값을 돌려주거나 저장 상태를 바꾸지 않는다. 같은 Job/Task/Run/SourceDigest·대상 revision과 Current도 확인한다.

`BindExploration`은 reader의 현재 세션을 한 번 고정한다. 호출자가 reader를 교체하거나 임의 결속을 보내는 API는 없다. `ReportObserved(ctx, binding, seq)`마다 현재 세션, 회차의 Job/Task/Run/SourceDigest, 승인 대상 참조·revision과 `Binding.Current()`를 다시 검사한다. reader는 닫힘·철회·장애 시 오류를 반환해야 한다. 기록은 같은 결속·seq, 실제 forwarded, 유효 시각·메서드·경로·전송 종류여야 한다. 바뀐 seq replay와 과거 seq는 거절하며 마지막 동일 기록 재전달은 멱등 처리한다. 여러 병렬 요청의 기록은 **seq 순서대로** 전달해야 한다. 정상 종료의 최종 수집은 browser close 뒤·boundary close 전에 수행한다. 취소·권한 철회로 Current가 실패하면 미수집 기록의 새 관측 승격은 거부한다. 이미 저장한 부분 발견은 유지하며, cleanup context의 `FinishExploration(cancelled/target_ended)`로 닫을 수 있다. 이 PR은 철회 뒤의 sealed 역사 기록 승격 API를 제공하지 않는다.

관측 목록은 HTTP 요청 메타데이터·SSE 연결·WS upgrade 메타데이터까지만 담는다. 헤더, query/body 값, 응답 본문, WS frame은 저장하지 않는다. 정적 보고의 키와 실제 관측 키는 별도 근거로 보존하고, 정적 키를 관측 endpoint의 키에 섞지 않는다. 앞서 발견한 GET form의 부작용 가능성도 실제 GET을 보았다는 이유로 자동 읽기 선택으로 바꾸지 않는다.

### 마스킹된 운영 기록과 private metadata

탐색 경계 계약 §4의 `path_pattern`은 가린 운영 기록이다. 그 문자열에 모델이 주장한 원문 path·필드 이름을 붙여 실제 관측으로 승격해서는 안 된다. reader는 실제 요청에서 만든 **별도의 private metadata**(원문 정규 경로, 이름만 있는 요청 모양)를 제공해야 한다. 실제 응답을 정적 분석하는 경우에만 private reader가 계산한 `ResponseSHA256`, `ResponseContentType`과 전달 바이트가 일치해야 한다. 이 두 응답 필드는 JSON·영속 발견 목록으로 보내지 않는다.

**실제 경계의 private metadata 프로토콜·production adapter는 이 PR에 없다(NOT_RUN).** 단위 시험의 reader는 프로그램 소유 합성 fixture이며, 이것을 실제 경계 연동 성공으로 계산하지 않는다. 계약 담당자가 private 채널의 저장·접근 권한과 어댑터를 연결하기 전에는 masked 운영 기록으로 대체하지 않는다.

## E②와 분리한 정적 pass

기존 `Crawl(ctx, Fetch)`와 `e2runner.Result` 검증은 유지한다. 새 `CrawlExploration(ctx, binding, ExplorationFetch)`만 탐색 호스트가 주입한 fetch와 reader를 사용한다. fetch가 돌려준 URL의 GET/path·seq와 private 응답 hash를 검증한 후 기존 HTML/bundle parser를 사용한다. 이 API 자체에 HTTP client, TLS 설정 변경, 일반 Bash, MCP transport는 없다.

`RecordExplorationUnavailable(ctx, reason)`은 bound/claimed 이전의 호스트 preflight 전용 API다. Current와 crawling 상태를 검사하고 boundary/model/runtime/browser 미가용의 고정 사유만 `ExplorationStatus=NOT_RUN`으로 저장한다. Stage·InitialCompleted·claimed·선택은 바꾸지 않아 기존 E② 정적 Crawl fallback을 유지한다. 세션을 실제 결속하면 RUNNING, 종료 시 COMPLETED/PARTIAL/STOPPED로 표시한다.

정적 pass 뒤에도 stage는 `crawling`으로 남겨 동적 탐색의 추정 보고와 관측 수집을 받는다. 호출자가 `FinishExploration`으로 완료 또는 부분 종료 사유를 전달하면 수동 모드의 선택 단계 또는 기존 자동 선택 단계로 간다. `cap_exhausted`, `boundary_unavailable`, `model_error`, `runtime_unavailable`, 시간·페이지 상한 등은 부분 발견을 남긴다. 모델 오류나 런타임 미가용을 completed로 바꾸지 않는다. 취소·대상 종료는 stopped다. 보호 저장은 호출 취소와 분리하지만 caller deadline과 기존 5초 저장 상한 중 빠른 시간을 지켜 마감 시간 상한을 늘리지 않는다. 일반 fetch 오류는 요청 하나의 실패와 부분 완료로 남기고 다음 페이지로 진행한다. 호스트가 `ExplorationFetchResult.Reason`으로 경계 장애·상한을 명시한 경우에만 그 사유로 종료한다. 이 흐름은 E② `Sent/Attempted/Blocked`를 만들지 않고 별도 `ExplorationRequests`만 센다. 부분 탐색을 취약점 없음 또는 E③ 성공으로 바꾸지 않는다.

## 발견 보존과 남은 한도

기존 32칸 endpoint 목록은 실행 선택에 그대로 쓰고 승인 한도를 바꾸지 않는다. `Record.DiscoveryInventory`는 별도 보호 자료로, 출처·inferred/observed·실제 세션/seq·전송 종류·이름을 최대 256개·64KiB까지 보존한다. 공개 `Snapshot`에는 포함하지 않는다. 추정은 같은 메서드/path/종류/source/transport별로 병합하고 관측은 seq별 근거를 보존한다. 상한에 걸린 수는 `InventoryOmitted`에 남긴다. 공개 48KiB 압축이 요청 모양을 지워도 보호 자료는 지우지 않는다. 전체 저장 record의 기존 128KiB 한도는 유지한다.

결정적 로컬 fixture의 실측:

| 입력 | 공개 선택 목록 | 보호 목록 | 측정 결과 |
|---|---|---|---|
| 승인된 API 후보 90개 + 시작 페이지 | API 31개 / 전체 32개 | 90개 | 공개 omitted 59, 보호 omitted 0 |
| 후보 13개 × query/body 각각 32개 이름(총 832개) | 압축 후 이름 0개, 4,156 bytes | 이름 832개 | 전체 record 58,943 bytes, 공개 `detail_truncated=true` |
| 후보 300개 | 전체 32개 | 256개 | 보호 omitted 44, 추가 전송·선택 권한 없음 |

남은 제한은 명시한다. 경로는 기존 정규 경로 128 bytes, 보고는 한 번에 32개, query/body 이름은 각각 32개·각 64 bytes다. 단순 정적 객체·route literal만 이름 후보로 읽고 spread·변수·런타임 식을 추측해 펼치지 않는다. 이름 병합 상한 이후의 추가 이름은 보존되지 않는다. 공개 선택 목록 밖의 보호 발견은 자동 실행·선택 대상이 아니며, 전체 inventory를 보여주거나 별도 승인하는 UI는 이 변경 범위가 아니다. 공식 모델 기반 발견 보존율·인증 후 탐색률은 별도 회차가 필요하다(NOT_RUN).

## 페이지 어댑터 사용

`scripts/metadata_only.js` 전체를 승인된 탐색 페이지의 init/evaluate context에 먼저 로드한다. 이 코드는 독립적으로 로드되며 Node `require`나 새 네트워크 라이브러리가 필요 없다.

```js
const hooks = globalThis.__apiReconMetadata.install();
// Run only approved UI exploration through the exploration runtime.
const result = hooks.drain(); // { confidence: "inferred", discoveries, omitted, invalid }
hooks.stop();
```

`result.discoveries`만 32개씩 보고한다. `confidence`, `omitted`, `invalid`는 보고 항목이 아니며 측정·부분 성공 표시에 쓰는 값이다. 브라우저가 시도한 요청이 서버에 닿았는지, 권한이 있는지, mock이 아닌 실제 backend 응답인지 이 어댑터만으로 판단하지 않는다. hook 설치 전 요청, Request stream의 body 필드, ServiceWorker/worker 내부 호출, programmatic form.submit의 전용 경로, WS frame은 이 어댑터가 수집하지 않는다. native 요청을 차단하거나 mock 응답을 만들지 않는다. 시작된 네트워크 요청의 허용·차단은 별도 탐색 경계가 집행한다.

어댑터의 path도 비신뢰 입력이다. 보호된 발견 저장 외에 모델·공유 출력으로 보낼 때는 현행 S09와 계정 규칙을 적용해야 한다. 경로에 자격값이 있을 가능성을 metadata-only 형식만으로 제거했다고 주장하지 않는다. 모델·로그·보고서에는 금지된 자격값·제어 비밀을 보내지 않는다.

검증은 `go test -json ./diagnosisflow ./agent`, 해당 패키지 `go vet`, `node --test skills/api-recon/scripts/metadata_only.test.js`로 한다. 실제 브라우저·private boundary·DB·모델 수용 결과는 각각 실행 여부를 별도로 기록한다.
