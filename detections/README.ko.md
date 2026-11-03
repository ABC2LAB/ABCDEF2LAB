# 기존 탐지 자료 색인

이 디렉터리는 기반 프로젝트에서 이어받은 탐지 규칙·지표·시험 자료입니다.
현재 제품의 목적과 진행 상태는 [ABC2LAB 문서](../docs/README.md)를 확인합니다.
이 자료의 규칙 적중이나 시험 통과를 ABC2LAB의 요청 통제·근거 판정·전체 탐지 성능으로 해석하지 않습니다.

한국어 · [English](README.md)

## 자료

| 경로 | 내용 |
|---|---|
| [Sigma](sigma/README.ko.md) | 호스트·로그·SIEM 규칙과 근거·한계 |
| [Suricata](suricata/README.ko.md) | 네트워크 규칙과 재현 입력 |
| [ATT&CK 레이어](attack/README.ko.md) | 규칙 태그와 커버리지 자료 |
| [침해지표](indicators/README.ko.md) | 소스·규칙과 연결한 CSV·MISP 자료 |
| [호스트 분류](triage/README.ko.md) | 소유 호스트에서 사용하는 읽기 전용 점검 |
| [시험 안내](tests/README.ko.md) | 스위트·입력 생성·하네스·의존성 |
| [방어 가이드](../docs/defense-ko.md) | 기존 규칙과 연결된 배경 자료 |

## 현재 실행 조건

기존 시험은 소스 문자열·규칙·지표·레이어·내보내기가 맞는지 확인하는 별도 자료입니다.
`tests/run-all.sh`와 일부 지표·MISP 시험은 `.github/workflows/detections.yml`을 읽지만 현재 저장소에는 그 파일이 없습니다.
따라서 전체 러너가 이 체크아웃에서 통과하거나 CI로 자동 실행된다고 안내하지 않습니다. 실제 실행 전 참조 파일과 시험 환경을 확인하고, 하네스 조정·CI 등록은 별도 변경으로 검토합니다.

수정 시에는 소스 지표와 규칙·CSV·MISP·ATT&CK 레이어·재현 시험을 함께 맞춥니다.
시험 대상은 소유하거나 승인받은 격리 환경으로 한정하고, 실행 명령·SHA·결과와 실패·제한을 기록합니다.
기여 절차는 [ABC2LAB 기여 안내](../CONTRIBUTING.md)를 따릅니다.

## Sigma 백엔드 이식성

백엔드별 변환 명령·지원 범위·한계는 [Sigma 안내](sigma/README.ko.md)와 [시험 자료](tests/README.ko.md)를 확인합니다. 해당 자료의 예시 출력을 현재 체크아웃의 검증 결과로 간주하지 않습니다.
