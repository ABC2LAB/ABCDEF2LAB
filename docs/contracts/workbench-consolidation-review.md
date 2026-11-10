# Workbench 단일화 검토안

상태: **구현 PR 검토 중, 담당자 동의·팀 시연 화면 확정 전**(2026-11-10).
이 문서는 [#353](https://github.com/ABC2LAB/ABCDEF2LAB/pull/353)의 삭제·이관·미지원 범위를 검토하기 위한 자료다. 이동찬의 Workbench 단일화 제품 요청을 구현했으나, 이를 이경준·윤재상의 담당 영역 삭제 동의나 12/18 시연안 채택으로 해석하지 않는다. 병합 승인도 대신하지 않는다.

## 제안과 채택 조건

`/`와 `/workbench`를 기존 Workbench 레이아웃으로 통일하고 실제 URL v2 진단을 연결한다. 중복 화면·목업·가짜 실행을 제거하며 필요한 공용 계약은 옮긴다. 서버 미지원·조회 실패를 가짜 데이터나 일반 task 생성으로 대체하지 않는다.

| 확인할 결정 | 담당자 | 현재 상태 |
|---|---|---|
| ABC2LabUI 제거, 이관된 공용 계약·시험, 기존 기능의 이관/제외 범위 | 이경준 | 명시적 동의 대기 |
| Workbench 목업·v1 제거와 여섯 화면·서랍·반응형 유지 | 윤재상 | 명시적 동의 대기. 기술 지적 해소 코멘트만으로 삭제 동의를 간주하지 않음 |
| Docker·키 등록 서버·권한 경계와 최종 병합 | 최민준 | 승인 대기 |
| 12/18에 사용할 화면과 아래 미지원 항목의 이관 우선순위 | 위 담당자와 PM | 확정 전 |

동의는 해당 PR의 리뷰에서 대상 범위와 함께 확인한다. 수정 요청이 오면 구현을 보완하고 재검토한다. 합의 없이 병합하거나 문서의 대기 표시를 완료로 바꾸지 않는다.

## 삭제와 이관 실측

리뷰 기준은 바로 아래 #352의 `d055ee0f40344296ea9896106d8823b1a95abffe`와 제품 구현 `54e0e31bad5fe00e68fcbce899eaa50790d78dde`다. dev와 비교한 숫자와 혼용하지 않는다. 이후 요청별 승인 시작 가드는 별도 보완이다.

| 경로 | 기준 파일 수 → 제품 구현 | 변경 해석 |
|---|---|---|
| `web/src/abc2lab-ui/` | 123 → 0 | 45파일 이관, 78파일 삭제. 전체 기능 보존을 의미하지 않음 |
| `web/src/lib/diagnosis/` | 0 → 44 | 공용 라이브러리와 시험 이관 |
| `web/src/lib/diagnosis-types.ts` | 없음 → 1 | 기존 UI 공용 타입의 이관·정리 |
| `web/src/workbench/` | 93 → 42 | 54파일 삭제, 3파일 추가, 25파일 수정 |
| `web/mockups/` | 2 → 0 | 원본 HTML 시안과 목업 서버 제거 |

아래 공용 모듈은 위 두 기준에서 바이트 단위로 같다. 경로가 바뀌었다는 사실을 화면 이관 완료로 보지 않는다.

| 공용 기능 | 새 경로 | 제품 화면 상태 |
|---|---|---|
| URL v2 결속·명시적 외부 전송 동의 | `web/src/lib/diagnosis/diagnosis-url.ts` | 범위 확인·시작에 연결 |
| 요청별 승인 API·검증 | `web/src/lib/diagnosis/request-approvals.ts` | 조회는 연결, 허용·거절 조작 UI는 미연결 |
| 실행·진단 조회 | `web/src/lib/diagnosis/diagnosis-management.ts`, `first-diagnosis.ts` | URL 진단·저장 결과에 필요한 경로 사용 |
| 보고서·수정 프롬프트 데이터 | `web/src/lib/diagnosis/remediation.ts`, `diagnostic-results.ts` | 저장 보고서·내보내기 연결, 옛 상세 화면 전체와의 동등성은 미확인 |
| KG·커버리지·E② 투영 | `web/src/lib/diagnosis/kg.ts`, `coverage.ts`, `e2flow.ts` | 공용 계약·시험 보존. 전용 화면 이관은 미완료 |

## 기능별 영향

| 기존 기능 | 제품 Workbench에서 확인할 부분 | 남은 차이 |
|---|---|---|
| 진단 생성·범위·시작 | `UrlNewScanLive.tsx`, `urlParts.tsx`, `live/urlDiagnosis.tsx` | URL v2만 지원. 일반 task/v1 생성·목업 재생은 제외 |
| 요청별 허용·거절 | `UrlLiveLive.tsx`, `live/urlDiagnosis.tsx` | 조작 UI 없음. `confirm_every_request`의 새 시작은 차단하고 운영자 확인을 안내 |
| 기존 실행·중지·결과 복구 | `UrlLiveLive.tsx`, `live/urlDiagnosis.tsx` | 이미 진행 중인 회차는 조회·중지 가능. 허용·거절이나 불명 실행을 자동 재전송하지 않음 |
| 결과·보고서·HTML | `UrlLiveLive.tsx`, `live/liveReport.ts` | 저장 결과와 후보·확정 구분을 사용. 옛 모든 보고서·finding 상세 화면의 기능 동등성은 미확인 |
| 자산·지도·KG·세부 커버리지 | `UrlAssetsLive.tsx` | 현재 진단의 승인 scope만 표시. 기존 전용 지도·그래프·커버리지 화면은 이관되지 않음 |
| 전체 이력·감사·설정·토큰 관리 | 로그인·현재 URL 회차·서버 예산·별도 모델 키 등록 | 종전 관리 화면 전체를 대체한 것으로 표시하지 않음 |

`confirm_every_request` 제한은 Workbench의 시작 조건이다. 서버·CLI의 승인 모드 계약을 변경하거나 `preauthorized_scope`로 자동 전환하지 않는다. 기존 팀 화면이 삭제된 상태에서 그 화면으로 이동하라는 안내도 제거한다.

## 분리해서 검토하는 방법

2026-11-10 최신 확인에서 #351은 dev에 병합됐고 열린 스택은 #352 → #353의 2개다. 이 PR은 #352 최신 head와 최신 dev를 함께 반영했다. 이 검토 보완으로 네 번째 PR을 추가하지 않고, 아래 범위를 나눠 검토 요청한다. 파일별 검토 구분은 두 개의 독립 PR이 되었다는 뜻이 아니다.

| 검토 단위 | 주요 경로 | 함께 볼 계약 |
|---|---|---|
| 서버·설치·Docker | `server/product_http*`, `server/product_frontend*`, `cmd/first-controlled-diagnosis/product_*`, `cmd/first-controlled-diagnosis/url_preparation_linux.go`, `deploy/product/`, `docker-compose.yml` | 키 열만 쓰는 DB 역할, 고정 모델·pin·비용·승인, 보호 준비 서비스·격리 |
| UI 단일화·삭제·공용 이관 | `web/` | 이 문서, [UI API 지도](ui-api-map.md), 기존 기능의 이관/제외 결정 |

초기 [281c851](https://github.com/ABC2LAB/ABCDEF2LAB/commit/281c851)은 UI 외에 키 등록 서버 코드도 포함한다. [1b097af](https://github.com/ABC2LAB/ABCDEF2LAB/commit/1b097af)와 커밋 두 개로 나뉘었다는 사실만으로 서버/UI가 완전히 분리됐다고 주장하지 않는다. 최신 전체 범위는 다음처럼 조회한다.

```bash
git diff d055ee0 HEAD -- server cmd deploy docker-compose.yml
git diff --find-renames=50% d055ee0 HEAD -- web
git diff d055ee0 HEAD -- docs
```

독립 PR 분리가 합의되면 현재 스택의 선행 PR 병합과 의존성부터 정리한 뒤 분리한다. 기존 리뷰 이력을 지우기 위한 force push는 하지 않는다.

## 검증 해석

삭제 전 #352의 Web 934 PASS와 제품 구현의 469 PASS는 서로 다른 시험 집합이다. 시험 수 감소를 회귀가 없다는 근거나 모든 화면의 기능 동등성으로 해석하지 않는다. 공용 계약 시험과 제품 경로 시험은 남아 있지만 삭제된 화면의 수용을 대신하지 않는다.

요청별 승인 시작 차단은 정상 사전 승인 시작을 대조하고, 서버가 확인한 ready 상태·확인 상자가 있어도 미지원 모드에서는 Start HTTP와 시작 키 생성이 0건인지 확인한다. 기존 회차의 조회·중지 안내도 별도로 검증한다.

[제품 수용 기록](../workbench-product-acceptance.md)의 합성 Docker 회차와 실제 유료 Qwen NOT_RUN을 구분한다. 이 문서 보완은 새 모델 호출·대상 진단·시연 화면 승인 결과가 아니다.
