# ABC2LAB

**실행 범위를 통제하고 실제 근거로 결과를 확인하는 웹 보안 진단 프로젝트**입니다.
인가·비즈니스 로직 진단을 목표로 하며, 현재는 서로 다른 두 계정으로 같은 자료를 조회하는 수평 IDOR 검사부터 개발하고 있습니다.

> **개발 중입니다.** 전송 통제와 근거 판정의 핵심 모듈이 구현되어 있으며, 서버 승인·최초 결과 저장·화면까지 이어지는 전체 연결과 실제 반복 평가는 후속 작업입니다.

[문서 목차](docs/README.md) · [개발 환경](docs/local-dev-ko.md) · [기여 안내](CONTRIBUTING.md) · [개발 계획](docs/development-plan.md)

## 목표

- AI가 검사 후보를 제안하고, 프로그램이 요청의 실행 가능 여부와 판정 근거를 확인합니다.
- 사용자는 탐색 결과를 검토하고 다음 경로를 선택하며, 현재 작업과 판단 근거를 확인합니다.
- 민감한 업무 환경의 로컬 운영을 지향합니다. 로컬 모델 연결과 전체 외부 송신 차단은 각각 검증합니다.
- 처음 보는 웹 서비스에도 설정과 시험 자료를 등록해 적용하는 것이 최종 방향입니다. 현재 검증 범위를 모든 웹 서비스나 취약점 유형으로 확대해 해석하지 않습니다.

## 현재 상태

아래 표는 `dev`의 모듈 경계입니다. 최신 세부 상태는 각 계약 문서와 PR의 기준 커밋·검증 결과를 확인합니다.

| 영역 | 구현된 범위 | 남은 연결 |
|---|---|---|
| 요청 통제 | 등록된 HTTP 조회 요청의 대상·계정·승인·예산·중단 검사, 서버 Worker 실행 경로 | 승인 단위 누적 예산과 운영 승인 발급·전달 |
| 근거 판정 | 정상·교차 조회 비교, 필수 근거 검사, 확정·의심·판단불가·권한상 허용 구분 | 서버 증거 수집, 판정 발급과 최초 확정 저장 강제 |
| 공통 작업 모드 | 순수 판별 코어와 PostgreSQL 등록·조회 | private marker 이관, 실행·저장 경계의 공통 적용 |
| 로컬 모델 | HTTP 정책·provider 생성 경계, profile 정책 resolver | 운영 설정 읽기, 실제 profile 선택·캐시·fallback 연결 |
| 사용자 화면·평가 | 기존 화면과 평가 기록·수집·검증 도구 | 승인·판정 실데이터 연결, 같은 조건의 실제 반복 측정 |

등록된 HTTP 경로의 통제를 셸·MCP·브라우저·임의 프로세스의 전수 통제로 표현하지 않습니다.
개별 테스트 통과나 기록 도구 준비를 완제품의 탐지 성능으로 표현하지 않습니다.

## 목표 사용 흐름

1. 운영자가 대상·계정·권한·시험 자료와 요청 예산을 등록합니다.
2. AI가 검사 후보를 제안하고 사용자가 검사 방향을 확인합니다.
3. 전송 직전에 범위·승인·예산·중단 상태를 검사합니다.
4. 정상 조회와 교차 조회의 실제 자료·권한 근거를 비교합니다.
5. 판정과 근거를 기록하고 사용자에게 상태를 보여줍니다.

이 흐름의 전체 서버·화면 연결은 개발 중입니다. 근거가 부족한 결과는 판단불가로 남기며, 단순한 응답 성공을 취약점 확정으로 취급하지 않습니다.

## 시작하기

개발 기준 브랜치는 `dev`이며 프로젝트 변경이 포함된 소스를 빌드합니다.

```bash
git clone --branch dev https://github.com/ABC2LAB/ABCDEF2LAB.git
cd ABCDEF2LAB
```

Go 1.26.3, Node.js 22.22.2 이상인 22.x 계열, PostgreSQL 16을 준비하고 [로컬 개발 안내](docs/local-dev-ko.md)를 따릅니다.
현재 Compose의 `artex` 서비스는 기반 프로젝트의 이미지를 사용하므로 ABC2LAB 변경 확인에는 소스 실행을 사용합니다.

실행 파일 `artex`, Go 모듈 경로, `ARTEX_*` 환경변수와 기존 API 이름은 현재 코드의 호환 식별자입니다. 문서의 프로젝트 이름과 구분합니다.

## 구조

| 경로 | 역할 |
|---|---|
| `server/`, `agent/` | 작업 API, 실행 문맥, 계획·실행 역할 |
| `transmit/`, `guard/` | 실제 HTTP 요청 통제, 승인·감사 경계 |
| `idor/`, `oracle/`, `evidencebind/`, `retest/` | 조회 비교, 근거 판정·결속 검사, 재검증 매핑 |
| `taskmode/`, `db/` | 작업 모드, PostgreSQL 저장과 그래프 |
| `llmpolicy/`, `dlp/`, `llmrec/` | 모델 통신 정책, 민감정보 처리와 기록 |
| `web/` | 진단·승인·결과 화면 |
| `scripts/eval/` | 시험 환경·기록 검증과 평가 도구 |

모듈 사이의 연결 상태와 제한은 [계약 문서 목차](docs/README.md#구현-경계와-계약)를 기준으로 확인합니다.

## 사용 범위

소유하거나 명시적으로 승인받은 시험 대상에서만 실행합니다. 초기 통합은 등록된 비민감 자료의 조회로 한정하며, 상태 변경 검사는 별도 승인된 시험 환경에서 다룹니다.
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
