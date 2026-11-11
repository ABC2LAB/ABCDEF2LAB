# JinDo

저장소·코드 식별자는 `ABC2LAB`을 그대로 씁니다. 제품 표시 이름 JinDo는 2026-11-11 팀 결정입니다.

**고객 내부망에 설치해 사용하는 온프레미스 AI DAST 소프트웨어**를 개발합니다.
AI가 웹서비스의 여러 취약 가능성을 탐색하고, 프로그램이 승인된 요청의 실행 범위를 통제하며 실제 관찰 근거와 검증 상태를 기록합니다.
인가·비즈니스 로직과 수평 IDOR는 초기 집중 개발 대상이며, 제품 전체 목표를 IDOR 하나로 한정하지 않습니다. 호스팅 SaaS나 외부 모델 의존형 서비스가 최종 목표는 아닙니다.

> **개발 중입니다.** 첫 통합 진단의 CLI·Planner/Worker·격리 실행·E② 승인/중지·후보 저장·마감·보고서 연결과 개발용 OpenRouter 연결은 구현되어 있습니다. 합성·통합 시험과 실제 모델·대상 수용, UI 전체 연결 완료는 구분합니다. 현재 모든 취약점 유형의 탐지·자동 확정을 보장하지 않습니다.

[Workbench Docker 설치](deploy/product/README.md) · [문서 목차](docs/README.md) · [첫 진단 기동](docs/first-diagnosis-launch.md) · [개발 환경](docs/local-dev-ko.md) · [기여 안내](CONTRIBUTING.md) · [개발 계획](docs/development-plan.md)

## 목표

- AI는 탐색과 검사 후보를 제안하고, 실행 허용은 모델이 아니라 E② 전송 통제가 결정합니다.
- 최종 고객의 폐쇄망·민감 환경은 로컬 모델이 기본입니다. 모델 연결 성공과 전체 외부 송신 차단은 각각 검증합니다.
- 초기 로컬 기준 모델(검증 전)은 `Qwen/Qwen3.5-35B-A3B`입니다. 최종 모델·성능·고객 장비 사양은 미확정입니다.
- 첫 개발용 실모델 검증을 위한 OpenRouter `qwen/qwen3.5-35b-a3b` 연결도 구현됐습니다. 명시적으로 승인된 비민감 개발 환경에서만 사용하며, 외부 API 측정치를 로컬 제품 성능으로 옮기지 않습니다.
- 처음 보는 웹서비스에도 대상·권한·시험 자료를 설정해 적용하는 것이 최종 방향입니다. 현재 지원 범위 밖의 자동 재현·확정은 후속 과제입니다.

실제 기준 성능·커버리지·시간·비용을 측정한 뒤 프롬프트·도구·문맥·재계획을 개선하고, 필요한 경우 RAG, 모델·추론·서빙 비교, 최후 수단으로 파인튜닝을 검토합니다. [모델 도입·개선 계획](docs/llm-adoption-and-improvement-plan.md)을 따릅니다.

인가·비즈니스 로직은 초기 집중 영역입니다. 제품 전체 목표를 설명하는 이번 정비는 팀의 E②·E③ 우선순위와 [개발 계획의 축소 순서](docs/development-plan.md)를 변경하지 않습니다.

## 현재 상태

2026-11-08 확인 시점의 `dev` 코드 스냅샷은 `d2bfec57f34dae1c17b6026366e747f3a588750b`입니다. [#317](https://github.com/ABC2LAB/ABCDEF2LAB/pull/317) 첫 통합 진단, [#323](https://github.com/ABC2LAB/ABCDEF2LAB/pull/323) OpenRouter 개발 모드, [#325](https://github.com/ABC2LAB/ABCDEF2LAB/pull/325) 평가 승인 모드 확인이 병합되어 있습니다. 새 실행은 최신 `origin/dev`와 [기동 문서](docs/first-diagnosis-launch.md)의 조건을 다시 확인합니다.

| 영역 | 구현 완료·합성/통합 시험 범위 | 미구현·미검증 및 후속 과제 |
|---|---|---|
| 첫 통합 진단 | CLI 준비·점검·실행, 단일 task/profile 결속, Planner/Worker, 격리 Bash·E②, 승인/거절·중지, 종료 상태 저장 | 실제 승인된 모델·대상으로 끝까지 실행하는 수용 검증 |
| 관찰·후보 | 일반 HTTP 도구 실행의 실제 `call_ref`에 결속한 후보 저장, `manual_review_required` 유지 | 유형별 탐지 성능·자동 재현·확정 범위 확대 |
| 등록 GET/IDOR·E③ | A/B 계정·등록 자원·KG·구조화 증거 판정과 저장을 위한 별도 경계 | 실제 공급 자료·운영 수용. 일반 HTTP 경로와 하나로 결합됐다고 보지 않음 |
| 모델 | 로컬 manifest/profile 정책·provider 선택과 명시적 OpenRouter 보호 모드 | 실제 키·제공사/개인정보 정책·비용 한도·승인된 대상 준비 및 실모델 수용 |
| 보고서·수정 프롬프트 | 저장 후보/판정 집계, 증거·상태 구분, 조치 artifact 생성·조회·복사 | 실제 진단 품질·고객 저장소 수정 결과 검증, 실제 스크린샷 수집 |
| UI·평가 | task 상태·요청 승인·중지·후보/결과·보고서 API 및 일부 UI 연결, 평가 기록·수집 도구 | 현재 API를 이용한 UI 통합·실브라우저 수용, 같은 조건의 실제 반복 측정 |

개별 테스트·CI 통과는 실제 Qwen3.5 진단 성공이나 제품 성능의 근거가 아닙니다. 지원된 격리 HTTP 경로의 통제를 셸·MCP·브라우저·임의 프로세스의 전수 통제로 확대하지 않습니다.

## 첫 통합 진단 흐름

현재 공식 시작 경로는 CLI의 `prepare-task → prepare-intents → check → run`입니다. 종료된 작업의 결과는 별도 `report` 명령으로 제공합니다. 준비 단계의 모델 호출 여부와 필요한 권한은 [준비 안내](docs/first-diagnosis-preparation.md)와 [기동 절차](docs/first-diagnosis-launch.md)를 확인합니다.

승인된 읽기 전용 일반 HTTP 범위에서 다음 흐름을 우선 검증합니다.

1. 운영자가 대상·profile·정책·예산·실행 환경을 준비하고 Planner가 intent를 만듭니다.
2. Worker가 격리 도구를 사용하며 E②가 요청 승인·범위·예산·중지 상태를 검사합니다.
3. 실제 실행 관찰에 연결된 취약점 후보를 검토 필요 상태로 저장합니다.
4. 실행을 마감하고 저장된 결과·보고서·AI 수정 프롬프트를 조회합니다.

등록 GET/IDOR의 A/B 계정·KG·E③ 확정 경로는 [별도 통합 경계](docs/contracts/first-idor-integration.md)를 따릅니다. 일반 HTTP 후보를 이 경로로 자동 확정하지 않습니다. **AI 추론, HTTP 200, 도구 실행 성공만으로 취약점 확정을 선언하지 않습니다.**

UI는 [현재 API](docs/contracts/ui-api-map.md)로 상태·승인/거절·중지·결과를 연결합니다. 기존 task API가 있다는 사실을 웹에서 임의 URL의 첫 통합 진단을 시작하는 API가 완성됐다는 뜻으로 해석하지 않습니다.

## 유형 독립적 보고서와 AI 수정 프롬프트

보고서는 IDOR 전용이 아니라 실제 진단에서 확보한 여러 유형의 후보와 판정을 모읍니다. 확정·검토 필요·미실행·결과 불명을 구분하고, 확정은 지원되는 E③ 증거 검증·저장 조건이 충족된 경우에만 표시합니다. 자동 검증 미지원 유형은 후보로 보존합니다. [구현 범위와 남은 재현·캡처 과제](docs/llm-adoption-and-improvement-plan.md#유형-독립적-결과-수집과-종합-출력)를 확인하세요.

취약점별 AI 수정 프롬프트는 **고객의 개발용 AI가 고객 애플리케이션의 실제 저장소에서 코드 경로와 기술을 확인하고 수정·테스트·리뷰하도록 전달하는 작업 지시**입니다. 언어별 예시 코드 목록만을 제공하는 기능이 아닙니다. 저장 artifact의 증거·입력 버전에 결속하며 오래된 조치의 복사는 차단합니다. 보고서 생성 자체가 고객 코드를 자동 수정하거나 운영에 배포하지는 않습니다. [조치·프롬프트 구현 범위](docs/llm-adoption-and-improvement-plan.md#저장된-기술별-조치와-ai-수정-프롬프트)를 확인하세요.

## 시작하기

개발 기준 브랜치는 `dev`이며 프로젝트 변경이 포함된 소스를 빌드합니다.

```bash
git clone --branch dev https://github.com/ABC2LAB/ABCDEF2LAB.git
cd ABCDEF2LAB
```

Go 1.26.3, Node.js 22.22.2 이상인 22.x 계열, PostgreSQL 16을 준비하고 [로컬 개발 안내](docs/local-dev-ko.md)를 따릅니다.
현재 Compose의 `artex` 서비스는 기반 프로젝트의 이미지를 사용하므로 ABC2LAB 변경 확인에는 소스 실행을 사용합니다.
첫 진단의 격리 helper·DB 권한·정책·인증·모델 준비는 [기동 문서](docs/first-diagnosis-launch.md), 개발용 외부 모델은 [OpenRouter 안내](docs/openrouter-first-diagnosis.md)를 따릅니다.

실행 파일 `artex`, Go 모듈 경로, `ARTEX_*` 환경변수와 기존 API 이름은 현재 코드의 호환 식별자입니다. 문서의 프로젝트 이름과 구분합니다.

## 구조

| 경로 | 역할 |
|---|---|
| `cmd/first-controlled-diagnosis/`, `server/`, `agent/` | 첫 진단 CLI, 작업 API, 실행 문맥, 계획·실행 역할 |
| `transmit/`, `e2proxy/`, `e2runner/`, `e2sandbox/`, `guard/` | 지원 경로의 실제 HTTP 요청 통제, 격리 실행·승인·감사 경계 |
| `idor/`, `oracle/`, `evidencebind/`, `retest/` | 조회 비교, 근거 판정·결속 검사, 재검증 매핑 |
| `taskmode/`, `db/` | 작업 모드, PostgreSQL 저장과 그래프 |
| `llmpolicy/`, `dlp/`, `llmrec/` | 모델 통신 정책, 민감정보 처리와 기록 |
| `report/`, `remediation/` | 저장 결과 보고서와 고객 개발용 AI 수정 프롬프트 |
| `web/` | 진단·승인·결과 화면 |
| `scripts/eval/` | 시험 환경·기록 검증과 평가 도구 |

모듈 사이의 연결 상태와 제한은 [계약 문서 목차](docs/README.md#구현-경계와-계약)를 기준으로 확인합니다.

## 사용 범위

소유하거나 명시적으로 승인받은 시험 대상에서만 실행합니다. 첫 일반 HTTP 통합은 승인된 읽기 전용 범위, 별도 IDOR 경로는 등록된 비민감 자료의 조회로 한정합니다. GET이라고 무조건 비변경 요청으로 보지 않으며 상태 변경 검사는 별도 승인된 시험 환경에서 다룹니다.
계정 비밀번호·인증 헤더·쿠키·토큰·대상 원문과 실행 결과를 저장소에 커밋하지 않습니다.

민감한 업무 환경에서는 로컬 모델을 사용합니다. 외부 API 모델을 사용하면 프롬프트와 대상 자료가 가려지지 않은 채 제공자에게 전달됩니다. 그래서 실제 대상은 시작 전에 [사전 공지·승인](docs/external-llm-notice-ko.md)을 받습니다.
공개 Juice Shop 평가의 외부 API 사용 조건과 모델·제공자·샘플링 고정 기준은 [평가 프로토콜](docs/evaluation/evaluation-protocol.md)을 따릅니다.
실제 baseline 대비 반복 측정을 마치기 전에는 개선 수치를 주장하지 않습니다.

## 출처와 라이선스

ABC2LAB은 [ARTEX](https://github.com/Autumn-27/ARTEX)의 [global edition](https://github.com/jiwoochris/artex-global)을 기반으로 개발합니다.
에이전트 SDK는 [Autumn-27/norma](https://github.com/Autumn-27/norma)를 사용합니다([go.mod](go.mod)).
포크 기준 커밋은 [`fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`](https://github.com/ABC2LAB/ABCDEF2LAB/commit/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6)입니다.
원본 출처와 AGPL-3.0 [LICENSE](LICENSE)를 유지합니다. [기존 기반 자료](docs/README.md#기존-참고-자료)는 현재 제품 안내와 구분합니다.

<details>
<summary>기반 프로젝트에서 이어받은 라이선스·이용 고지 원문</summary>

## License and disclaimer

### Open-source license

This project is distributed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**. The full terms are in the [LICENSE](LICENSE) file at the repository root.

Anyone is free to use, modify, and distribute it, but **derivative works must also be released under AGPL-3.0.** In particular, if you modify this project and **provide it to users over a network (e.g., as an online service), you must make the corresponding complete source code available to those users.** This global edition likewise keeps AGPL-3.0.

> ⚠️ **Important:** an open-source license itself does not restrict how the software may be used. The "Usage restrictions" and "Disclaimer" below are an additional covenant and a serious notice that the original author requires of users — please observe them.

### Usage restrictions

- Use this tool to **read and study the source code**, and to **verify its technical principles in a locally isolated environment.**
- Unless the target is one you own or for which you have **explicit written authorization**, do not scan, probe, exploit, or attack any website, online service, or connected system.
- Using it for illegal intrusion, data theft, denial of service (DoS), or any other destructive or criminal activity is strictly prohibited.
- You must comply with all laws on network security, data protection, and computer crime in your country and region (in Korea, the 정보통신망법, 개인정보보호법, and others).

### Disclaimer

This project is provided "AS IS" without any warranty, express or implied. The original author and contributors are not liable for any direct or indirect damage, data loss, system damage, or legal dispute arising from the use of this tool (regardless of whether it was used appropriately). **Downloading, installing, or using this project is deemed to constitute your having read, understood, and agreed to all of the above conditions.**

**All legal responsibility and consequences rest with the user.**

</details>
