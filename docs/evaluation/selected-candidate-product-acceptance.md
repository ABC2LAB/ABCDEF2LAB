# 선택 후보 검증: 소유 합성 제품 수용시험

이 시험은 `selected_candidate_product_acceptance.py`가 실제 제품 관리 API를 호출한다. 제품 설치, PostgreSQL 16, 보호 준비 서비스, E② Host, 격리 Runner, 로컬 합성 모델, 소유 HTTPS 대상 및 DNS 증명을 사용한다. OpenRouter와 외부 대상에 송신하지 않는다. 합성 모델의 판정은 실제 LLM 품질 측정이 아니다.

## 격리와 재실행

- 전용 worktree와 Compose 프로젝트를 사용한다. 다른 세션의 컨테이너, DB, 네트워크 및 볼륨은 중지하거나 삭제하지 않는다.
- 기본 Compose의 `172.31.252.0/24`와 `172.31.253.0/24`가 다른 프로젝트에서 사용 중이면 `scripts/eval/prepare_selected_owned_compose.py <S01|S02|S03|S04|S05|S07|S08>`로 같은 디렉터리에 `10.93.*` 전용 Compose 사본을 만든다. 이 사본은 커밋하지 않는다. 해당 대역도 점유됐다면 충돌하지 않는 새 전용 대역을 설계·검증한 후 실행한다.
- `scripts/eval/create_selected_owned_install.py <새 0700 디렉터리> 10.93`으로 비공개 설치 입력을 만든다. S03·S04는 마지막 인자 `allow-login-post`를 추가한다. 이 시험에서만 합성 대상의 로그인 POST를 승인한다. 생성된 자격값과 DNS 제어 토큰은 출력·커밋하지 않는다.
- 제품과 fixture 이미지는 같은 **전체 커밋 SHA**를 `VCS_REF`로 빌드한다. 수용 스크립트는 두 이미지의 revision label이 `--head`와 일치하지 않으면 시작하지 않는다. 각 시나리오는 새 설치·DB 볼륨에서 실행한다. fixture 모델이 최초 회차의 노드 번호를 고정해서 사용하므로 같은 설치에서 새 진단을 반복해 성공으로 세지 않는다.
- 생성된 Compose 사본 네 개를 순서대로 `docker compose -p pr950-selected93 -f <base> -f <acceptance> -f <selected> -f <scenario>`에 준다. `PRODUCT_SOURCE_SHA`, `PRODUCT_TAG`, `PRODUCT_INSTALL_FILE`, `PRODUCT_POSTGRES_PASSWORD_FILE`, loopback 제품·fixture 포트를 지정해 `up -d --no-build --wait`한다. `fixture-init`의 `/target-tls/control-token`을 전용 0600 파일로 복사한 뒤 `python3 -I scripts/eval/selected_candidate_product_acceptance.py <install.json> <dns-token> --head <SHA> --scenario <ID>`를 실행한다. 끝나면 **자신의 프로젝트만** `down -v`한다.

수용 스크립트는 원본 후보 1건, 정상 종료와 감사 완료, 승인 전 대상 송신 0건, 승인 후 송신 수, 범위 밖 송신 0건, 최신 보고서 후보 상태, 내부용·공유용 HTML/DOCX 200 및 합성 자격값 비노출을 검사한다. S03은 최초·후속 로그인과 인증 읽기를 별도로 센다. S04는 계정 변경 뒤 후속 송신과 결정적 판정이 없는지 확인한다. S05는 정확한 Query·헤더 정상 대조 요청을 센다. S07은 바뀐 계획 digest의 승인 거절과 사용자의 거부를 확인한다. S08은 모델의 존재하지 않는 출처 인용이 최종 판정으로 저장되지 않는지 확인한다.

## 범위와 완료 기준

| 시나리오 | 제품 수용 범위 | 결과 기록 |
| --- | --- | --- |
| S01 | 봉인된 독립 저장 관찰 재사용, 추가 대상 송신 0건 | 시나리오별 실행 출력과 PR 코멘트 |
| S02 | 새 E② 읽기, Host 감사·봉인, 최신 보고서 | 시나리오별 실행 출력과 PR 코멘트 |
| S03 | 동일 시험 계정의 재로그인·인증 읽기 | 시나리오별 실행 출력과 PR 코멘트 |
| S04 | 계정 교체 후 후속 송신 및 결정적 판정 차단 | 시나리오별 실행 출력과 PR 코멘트 |
| S05 | 재현 요청과 구조화된 정상 대조 요청 | 시나리오별 실행 출력과 PR 코멘트 |
| S06 | Body·상태 변경은 안전한 복구 계약 부재로 미지원 | 계획 경계의 POST 거부 단위시험; 실제 쓰기 송신 없음 |
| S07 | 계획 digest 불일치와 운영자 거부 | 시나리오별 실행 출력과 PR 코멘트; 만료·철회·예산은 별도 시험 |
| S08 | 모델의 잘못된 근거 인용 | 시나리오별 실행 출력과 PR 코멘트 |
| S09 | 중복 선택 요청, 승인 대기 중 API 재시작, 취소 멱등성 및 이력 보존 | 제품 재시작 수용시험. 실행 중단·결과 불명은 별도 시험 필요 |
| S10 | 기존 E③·보고서·#918 회귀 | Go/DB/race/CI와 별도 제품 회귀 결과 |

표의 수용 범위를 실행하지 않은 경우 PASS로 표시하지 않는다. 비 IDOR `model_supported`는 프로그램의 `confirmed`나 E③ IDOR 확정과 구별한다. 실제 로컬 LLM 품질 측정은 별도 평가 러너의 승인된 로컬 모델과 정답 사례가 있을 때 수행한다.
