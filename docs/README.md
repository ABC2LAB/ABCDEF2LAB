# ABC2LAB 문서 목차

프로젝트 소개는 [README](../README.md), 작업 순서와 역할은 [개발 계획](development-plan.md)을 기준으로 합니다.
문서는 세 종류로 구분해 읽습니다.

- **현재 안내**: README·개발 계획·첫 진단 기동/준비·OpenRouter·UI 연결 안내는 최신 `dev`의 사용 범위를 설명합니다. 구현 완료, 합성·통합 시험, 실제 모델·대상 수용은 서로 다릅니다.
- **기술 계약·공식 평가 기준**: 아래 계약과 평가 프로토콜의 요구·수용 기준은 그대로 적용합니다. 오래된 기준 SHA만으로 계약을 폐기하거나 완화하지 않습니다. 개별 문서의 명시된 구현 단계와 후속 연결도 함께 확인합니다.
- **과거 결정·설계 기록**: 날짜·PR·SHA를 붙인 조사와 초안은 당시 판단을 보존합니다. 역사적 미구현 설명을 오늘의 구현 현황으로 읽지 말고 현재 안내와 코드를 대조합니다.

2026-11-08 확인 `dev`는 `d2bfec57f34dae1c17b6026366e747f3a588750b`이며 #317·#323·#325가 병합됐습니다. 새로운 실행에서는 최신 원격 기준을 재확인합니다. 문서가 있다는 사실만으로 기능 연결이나 검증이 완료된 것은 아닙니다.

## 시작하기

- [프로젝트 목표와 현재 상태](../README.md)
- [로컬 개발·소스 빌드](local-dev-ko.md)
- [첫 통합 진단 기동: CLI 준비·점검·실행·저장 보고서](first-diagnosis-launch.md)
- [첫 진단 환경·권한·입력 준비](first-diagnosis-preparation.md)
- [개발용 OpenRouter 연결과 실모델 수용 조건](openrouter-first-diagnosis.md)
- [UI API 연결 지도](contracts/ui-api-map.md) · [UI 작업 지침](../web/src/abc2lab-ui/README.md)
- [외부 LLM 사용 진단 사전 공지·승인 양식](external-llm-notice-ko.md)
- [기여와 검증 절차](../CONTRIBUTING.md)
- [유지보수와 통합 확인](../MAINTAINING.md)
- [보안 문제 신고](../SECURITY.md)
- [변경 이력](../CHANGELOG.md)

## 프로젝트 방향

- [개발 계획·우선순위·역할 분담](development-plan.md)
- [LLM 도입·개선·증거 기반 보고서 계획](llm-adoption-and-improvement-plan.md)
- [엔진 개선 설계·과거 코드 조사](engine-improvement-draft.md)
- [엔진 언어 전환 계획](engine-language-migration.md)
- [불편과 개선 대장](pain-points-to-improvements.md)
- [기능 후보와 우선순위](feature-shortlist.md)

개선 설계의 과거 코드 조사와 현재 구현 상태를 구분합니다. [첫 통합 진단 최소 경로 초안](first-integrated-diagnosis-minimal-draft.md)은 설계·수용 범위의 추적 자료이며 현재 실행 절차는 기동 문서가 안내합니다. 일정·범위 조정은 개발 계획을 먼저 갱신합니다.

## 구현 경계와 계약

### 요청 통제와 감사

- [HTTP 전송기](contracts/e2-http-transmitter.md)
- [서버 실행 문맥과 Worker 연결](contracts/e2-server-runtime.md)
- [승인별 요청 예산](contracts/approval-request-budget.md)
- [감사 저널](contracts/e2-audit-journal.md)
- [감사 키 생성 CLI](contracts/e2-audit-key-generation-cli.md) · [감사 검증 CLI](contracts/e2-audit-verification-cli.md)
- [대상 상태 코어](contracts/target-health-core.md) · [HTTP 상태 관측과 중단](contracts/e2-http-health-stop.md)
- [가로채기 패턴 검증](contracts/intercept-pattern-validation.md)
- [실행 컨텍스트 훅](contracts/runtime-hooks.md)

### 근거 판정과 결과 저장

- [오라클 입력·판정](contracts/e3-oracle.md)
- [IDOR 검증기와 엔진 연결](contracts/idor-verifier-seam.md)
- [저장 전 증거 결속](contracts/e3-evidence-binding.md)
- [재검증 매핑](contracts/e3-retest-mapping.md)
- [최초 확정 저장 설계](contracts/e3-storage-gate.md) · [시험 명세](contracts/e3-storage-gate-tests.md) · [현재 최초 저장 경계](contracts/e3-initial-storage.md)
- [등록 GET/IDOR 별도 통합 경계](contracts/first-idor-integration.md)
- [일반 HTTP 후보·통합 보고서 현황](llm-adoption-and-improvement-plan.md#유형-독립적-결과-수집과-종합-출력) · [고객 개발용 AI 수정 프롬프트](../remediation/README.md)
- [판정 레코드](contracts/verdict-record.md)
- [기존 취약점·트래픽 증거 기능](finding-traffic-evidence-ko.md)

### 작업 모드·시험 자료·화면

- [공통 모드 DB](contracts/task-mode-db.md) · [private marker](contracts/task-mode-marker.md)
- [시험 자료와 표식](contracts/idor-canary-fixture.md)
- [소유·권한 KG](contracts/kg-policy.md)
- [화면과 API 지도](contracts/ui-api-map.md)

### 로컬 모델과 민감정보

- [로컬 통신 정책](contracts/local-llm-policy.md)
- [로컬 HTTP·provider 생성](contracts/local-llm-http.md)
- [profile 정책 resolver](contracts/local-llm-profile-policy.md) · [서버 모드](contracts/local-llm-server-mode.md)
- [첫 진단 개발용 OpenRouter 보호 모드](openrouter-first-diagnosis.md)
- [민감정보 투영](contracts/dlp-projection.md)
- [LLM 연결 시험 출력](contracts/llm-connection-output.md)
- [보고서 내보내기](contracts/report-export-dlp.md)

## 평가

- [평가 프로토콜: 공식 기준](evaluation/evaluation-protocol.md)
- [공식 실행 절차](evaluation/official-run.md) · [평가 러너](evaluation/runner.md)
- [실행 전 점검](evaluation/preflight.md) · [프로필 지문](evaluation/profile-fingerprint.md)
- [회차 기록](evaluation/run-record.md) · [v1.1 추가 계약](evaluation/run-record-v1.1.md)
- [스냅샷 수집](evaluation/snapshot-collector.md) · [스냅샷 저장](evaluation/snapshot-writer.md)
- [기준 출력 추출](evaluation/baseline-extractor.md) · [매핑 검사](evaluation/baseline-mapping-check.md)
- [기준 출력 페이지](evaluation/baseline-pages.md) · [페이지 수집](evaluation/baseline-pages-collector.md)

도구 자체의 합성 시험과 실제 엔진의 baseline/개선 후 반복 측정을 구분합니다. 평가 정답은 평가기에만 두고 검사 후보 생성·실행·판정 입력에 넣지 않습니다.

## 기존 참고 자료

[탐지 규칙 자료](../detections/README.ko.md)와 [방어·탐지 가이드](defense-ko.md)는 기존 규칙의 근거와 시험을 위해 유지합니다.
이 자료는 ABC2LAB의 현재 기능 목록·통합 완료·탐지 성능을 설명하는 문서가 아닙니다.
기반 프로젝트의 출처와 라이선스는 [README](../README.md#출처와-라이선스)에 모았습니다.
