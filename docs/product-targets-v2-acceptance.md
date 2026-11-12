# Docker 제품 v2: 회차별 대상 승인 수용

2026-11-10 KST. #353의 설치 대상 고정을 제거했다. 설치 v2는 엔진·DB·모델 정책·보안 환경만 준비하고, 새 대상은 Workbench에서 등록·소유 확인·승인한다. Workbench CSS·레이아웃과 `diagnosis-api.v2`, 보호 preparation, 개량 Planner/Worker를 유지한다. [설치 절차](../deploy/product/README.md)와 [기존 단일 대상 합성 수용](workbench-product-acceptance.md)은 별도로 보존한다.

## 출처와 검증 구분

| 항목 | 실제 검증 출처 |
|---|---|
| 엔진·UI 실행 소스 | `b676e38` (최신 dev와 #352 반영) |
| 제품 image ID | `sha256:0361d2e1d67dd72690a16e0d5a49259739a2ad0a0d477dc7526b0235bb5e99d7` |
| first-controlled-diagnosis SHA256 | `365f84cff49931a27ae7d7d43acb2b8da162abfebf7e6d7aac8380333461d29c` |
| E② helper SHA256 | `25cea9de2914f5c4d90b26441dd77a300b9f6aed13c07b9ba8934ab5e28d71e3` |
| 실제 대상 | 소유 Juice Shop v15.0.0 + 서로 다른 HTTPS front/IP |
| 실제 실행 모델 | 로컬 합성 `owned-synthetic-products` |
| 실제 OpenRouter/Qwen 추론 | **NOT_RUN — 사용자 요청으로 유료 검증 보류** |

이미지의 Go 엔진·UI는 같은 소스에서 빌드했다. 일반 ARTEX 실행 엔진이나 목업 실행 엔진으로 대체하지 않았다. 모델 응답만 로컬 합성이다. 마지막 후속 변경은 브라우저 시험 드라이버·수용 문서와 “설치된 범위”를 “현재 URL의 승인 기록”으로 바로잡는 화면 문구이며, 위 실행 소스와 구분한다.

기준 dev `d5829379dda0a8cb32ee54ca5b151eb897aa3cc7`와 선행 #352 `85c52fbb9e0e3cc6e7bb456e5c5c61cb26ca6112`를 통합했다. #351은 `718754b949183a71d7ba14de327d4e553f52a8cd`로 병합됐다. #353의 base는 열린 #352이며, 담당자 리뷰 없이 자동 병합하지 않는다.

## 실제 Docker·Chrome 결과

하나의 새 설치에서 아래 두 URL을 각각 승인·진단했다. URL을 바꿀 때 설치 JSON·볼륨·컨테이너 이미지를 바꾸지 않았다.

- `https://juice-a.test:9443/rest/products` → `172.31.253.20`
- `https://juice-b.test:9443/rest/products` → `172.31.253.22`

설치 입력에는 `target_origin`, `target_ip`, `path_prefixes`가 없다. 시험망 예외 CIDR/포트, 시험 DNS resolver, 소유 CA는 보안 환경으로만 준비했다. 제품은 일반 공개 IP·HTTPS 443이 기본이며 UI로 설치 상한을 넓힐 수 없다.

| 검증 | 실측 결과 |
|---|---|
| 최초 설치 | 두 URL 없이 DB 역할·보호 파일·정책·IPC·격리 preflight 준비, 모델 completion/대상 HTTP 0 |
| Workbench 대상 등록 | A/B 각각 28 assertions PASS. URL·운영자 설명만으로 승인이 되지 않음 |
| 소유 확인 전 | UI 승인 시 `ownership_not_verified`. DNS TXT 게시 후 서버 DNS/IP·TLS 확인으로 승인 |
| 외부 모델 키 등록 | 실제 Workbench→인증 API→최소 권한 writer→DB PASS, 25 assertions. 합성 키 1회 등록·password 입력 즉시 비움·브라우저 저장소/화면 노출 없음 |
| 키 재사용 | 새 화면에서 private preparation의 `configured`·고정 Qwen ID만 조회 PASS, 22 assertions. 키 재입력·추가 저장·Start 0 |
| Qwen 범위·비용 연결 | 실제 Workbench 범위 확인 32 assertions PASS. 고정 모델·provider·USD 상한 표시, 미동의 Start 비활성, 실제 Start 0 |
| 재기동 복원 | DB·preparation·API·Workbench 재기동 후 API 31 checks·Workbench 보고서 21 assertions PASS. 같은 두 회차·키 상태·HTML 복원, 추가 모델/대상 HTTP 0 |
| URL A 진단 | 초안 26·응답 유실 시작 10·보고서 21 assertions PASS |
| URL B 진단 | 초안 28·응답 유실 시작 10·보고서 21 assertions PASS |
| 각 회차 엔진 | goals 2 + Planner 3 + Worker 4 = 모델 9회. 실제 task·intent·Worker·E② 경로 실행 |
| 각 회차 E② | attempts 2, allowed 1, denied 1, forward_attempted 1, responses 1, unknown 0, Bash calls 2 |
| 실제 수신 | A/B 각각 상품 GET 1·HTTP 200. 다른 승인 대상에 대한 교차 송신은 각 회차에서 차단, 추가 수신 0 |
| 관찰·저장·판정 | 각 회차 후보 1·확정 0, 총 findings 0. HTTP 관찰을 E③ 확정으로 승격하지 않음 |
| 감사·마감 | 각 run closed/completed, drain_complete=true, audit_complete=true |
| Workbench 결과 | 두 회차의 실제 저장 run·후보 집계·공유 HTML 다운로드 PASS |
| 미승인 URL/경로·메타데이터 | scope/task/authority 없음, Start 409, 추가 모델/대상 HTTP 0 |
| DNS 재결속 | 승인 A를 메타데이터 IP로 바꾼 뒤 Start: `target_dns_mismatch`, failed·task 없음·authority not_issued. 추가 모델/대상 HTTP 0 |
| 승인 철회·격리 | A 철회 후 새 A 차단. B의 동일 입력 source_digest·ready 범위 유지. A의 저장 보고서 조회 가능 |
| API 부정 검사 | 실제 인증·private IPC·E② 감사·모델 정책/key projection 포함 58 checks PASS |

전체 사용량은 DB `llm_usage` **18행, input 180/output 180 토큰**, 모델 이름은 로컬 합성 모델 한 종류다. Qwen 사용량 행은 없다. 실제 유료 USD 사용량·성능으로 해석하지 않는다.

키 등록 시험의 `fixture/endpoint`, `Fixture`, `product-key-registration-fixture` manifest와 키는 계약 검사 전용 합성 자료다. 실제 제공사·계정의 privacy/ZDR·spend cap 확인이나 유료 실행 승인이 아니다. 키 등록·상태 조회·범위 확인은 OpenRouter로 송신하지 않는다. 실제 키·제공사 승인·USD 총액 상한이 준비되기 전 실제 Qwen 실행은 NOT_RUN으로 남긴다.

## 재현과 회귀

`scripts/product-diagnosis-browser.cjs`는 실제 Chrome CDP와 보호 설치 입력을 사용한다. `target-approve`, `external-key-save`, `external-key-reuse`, `external-scope`, `url-draft`, `url-lost-start`, `url-report`를 순차 실행한다. DNS 증명은 운영자 동작에 해당하는 소유 시험 DNS 제어로만 게시한다. 브라우저의 진단/모델 응답이나 React 내부 상태를 위조하지 않는다. 시작 응답 유실은 서버가 수락한 응답만 버린다.

`scripts/product-targets-acceptance.py negative <protected-install.json> <protected-dns-token> <draft-A> <draft-B>`는 고정 loopback 시험 API만 사용한다. `recover`는 같은 두 회차의 저장 결과·감사·키 상태를 조회하고 모델 18·대상 1/1 유지 여부를 확인한다. 비밀번호·키·JWT·DNS 제어 토큰·원문 보고서는 출력하지 않는다. HTML/로그/DB 덤프를 커밋하지 않는다.

- 전체 PostgreSQL 16 Go race: **62패키지, 7,392 PASS / 0 FAIL / 18 SKIP**. 새 제품 DB 검사는 SKIP 없이 실행했다. SKIP은 다른 UID 소유권 5개와 선택 브라우저/독립 프로세스·실모델 수용 13개이며 PASS로 계산하지 않는다.
- Web 전체: **35파일, 476 PASS / 0 FAIL / 0 SKIP**. 후속 화면 문구 관련 39개 회귀·TypeScript도 PASS.
- 최신 dev 평가 Python: **1,716 PASS**, E② 배치 경계 **55 PASS**. 첫 샌드박스 실행의 loopback EPERM은 환경 실패로 기록하고, 소유 loopback 서버를 허용해 전체 재실행했다.
- 전체 Go build/vet·darwin/windows 교차 빌드, 변경 Go gofmt·Biome·정적 Next Docker 빌드·diff 검사 PASS.
- 새 경계: 무송신 Inspect·캐시된 파생 정책 변조, target별 digest·DNS/TLS 변경·proof 혼입·철회 격리, 사설 예외의 메타데이터/보호 서비스 우회, strict IPC의 null 배열, 실제 TLS 공개키 변경 전 HTTP 0과 지문 의미를 검사했다. 기존 E③ 판정·감사·영속성·E② 평가 golden 계약은 전체 회귀로 확인했다.

초기 범위는 DNS 소유 증명이 가능한 HTTPS 호스트, 명시적으로 승인된 GET/HEAD 경로, 고정 OpenRouter/Qwen 정책이다. IP literal 소유 승인·추가 제공사/모델·상태 변경 검사·지원되지 않는 E③ 자동 재현·v1 설치 자동 이관은 완료 범위가 아니다. UI 삭제에 대한 기존 담당자 동의와 최종 병합 검토도 별도로 대기한다.

## 리뷰 후속의 접촉·만료 해석

[권한 모델 제안](contracts/product-target-approval-proposal.md)에 별도 PR 분리와 팀 합의 조건을 기록했다. 이 문서의 대상 HTTP 0은 대상과 무통신을 뜻하지 않는다. 소유 승인에서 정상 TLS 1회(주소 후보 실패 시 최대 8회), 시작 직전 선택 IP에 TLS 1회를 수행한다. 새 구현은 이 시도·결과를 별도 보호 감사에 기록한다. 기존 회차의 TLS 접촉 수를 소급 실측한 결과는 아니다.

승인 만료는 요청 시간(최대 24시간)과 인증서 NotAfter 중 빠른 값이며, 만료 시각부터 새 시작과 현재 회차의 신규 전송을 거부한다. 실행 중 current 감시는 기존 250ms 주기로 취소를 요청한다. 새 가드·감사·UI 단위 검증은 과거 Docker 회차와 구분하며, 실제 유료 모델 NOT_RUN은 유지한다.
