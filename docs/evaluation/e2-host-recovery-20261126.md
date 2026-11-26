# E② Host 운영자 복구 무료 합성 제품 시험 (2026-11-26)

## 시험 경계

`dev` `8dbec877`의 이 PR 코드와 아직 병합되지 않은 #950 `d521a5bf`를 별도 임시 worktree에서 합친 제품 이미지 `869f5e0a`로 시험했다. 독립 Compose 프로젝트 `e2recovery-r01`, `e2recovery-r06`, PostgreSQL 16, 제품 준비·API·Workbench·E② Host, 격리 Runner, 소유 HTTPS 대상과 로컬 합성 모델을 사용했다. 유료 모델 및 외부 대상 호출은 0건이다. 운영 설치 또는 다른 세션의 컨테이너·DB·볼륨을 사용하지 않았다.

#950의 합성 모델은 첫 빈 DB에서만 유효한 `prove_goal(goal_id=2, evidence_id=4)`를 고정 사용한다. R03 첫 시도는 뒤 회차의 그래프 ID가 달라 정상 완료하지 못해 제품 API로 취소했다. 재시험에서는 **임시 통합 worktree의 합성 모델 fixture만** 현재 Planner `graph_overview`의 open goal 및 후보 fact ID를 읽도록 바꿔 다시 빌드했다. 이 fixture 조정은 제품 코드 및 이 PR에 포함하지 않는다. R06의 중간 장애도 같은 임시 worktree의 복구 실행기에 기록 link 직전 20초 대기와 고정 진단 로그를 넣은 **시험 전용 이미지**로 주입했다. 실제 PR 코드는 그 지연·로그를 포함하지 않는다.

## 관찰 결과

| 항목 | 결과 | 근거 |
| --- | --- | --- |
| R01 기존 장애 | PASS | #950 S09R: 승인된 후속 GET 1건이 대상에 도달한 상태에서 준비 서비스를 강제 종료했다. 새 선택 검증 송신 0건, 원본 후보 판정 없음, Host head `open` revision 2. |
| R02 운영자 복구와 선택 검증 | PASS | 이전 실행 컨테이너 종료와 Host/IPC 볼륨 결속 확인 후 운영자 기록 생성. 원본 head는 `open`, pending attempt는 그대로다. 같은 설치의 새 선택 검증에서 승인된 GET 2건, 합성 모델 2호출, `model_supported`, 최신 보고서 조회 성공, 범위 밖 송신 0건. |
| R03 일반 진단 | PASS (fixture 조정 후) | 동일 DB·Host 볼륨에서 새 일반 진단 task 7이 탐색·계획·진단·E②·마감을 거쳐 `completed`, `drain_complete=true`, `audit_complete=true`, 보고서 HTTP 200 및 항목 1건으로 끝났다. 기존 fixture 그대로인 첫 시도는 고정 그래프 ID 때문에 실패했다. |
| R04 재시작 | PASS | 복구 뒤 Host·준비 서비스를 재시작해 Host healthy 및 기존 후보의 최신 보고서 결과 유지 확인. |
| R05 복구 불가 | PASS | 송신 가능 준비 서비스가 살아 있을 때 복구 명령은 `host_active`로 거부됐다. 별도 소유 스택에서 복구 기록을 변조한 뒤 Host 재시작이 차단됐고 기존 원본 journal/head SHA-256은 변하지 않았다. 손상 상태에서 새 선택 검증 대상 송신 0건이었다. |
| R06 복구 도중 재장애 | PASS (시험 전용 장애 주입) | 다른 소유 스택의 S09R에서 신규 pending GET을 만들고 복구 파일 fsync 후 배타적 link 직전에 복구 컨테이너를 SIGKILL 했다(exit 137). 임시 파일만 남고 최종 기록은 없으며 DB head는 `open` revision 2였다. 정상 이미지로 재시도해 기록 1개만 생성했고 재시도 응답도 멱등이었다. Host 재시작 뒤 새 선택 검증이 승인된 GET 2건, 합성 모델 2호출, 최신 보고서 결과 `model_supported`로 끝났다. 범위 밖 송신 0건. |

R01/R06의 기존 결과 불명 요청은 자동 재송신하지 않았다. 기존 `open` 저널을 정상 `sealed`로 바꾸지 않았다. R05에서 손상시킨 기록은 소유 시험 스택에서만 복구 시험을 계속하기 위해 제거하고 운영자 복구를 다시 수행했다. 원본 감사 사건을 삭제하거나 고치지 않았다.

## 한계와 팀 확인

R03의 기존 결과는 임시 fixture 수정으로 얻었다. 아래 최신 조합 재시험은 이와 별개이며, #950에 커밋된 fixture 수정을 사용했다. R06의 장애 주입 결과는 기록 연결 직전의 결정적 프로세스 종료이며 실제 운영의 임의 시점 전원 장애를 재현한 것은 아니다. 대상 GET/HEAD도 부작용이 없다는 보증은 없으므로 운영자는 대상 상태와 다음 회차 승인을 확인해야 한다. Docker daemon 소켓은 사실상 호스트 root 권한이다. 컨테이너 종료 증거와 E③ 소비 경계는 팀 보안 계약 리뷰 후 활성화한다. `합성 통과` 라벨과 병합은 담당 러너 및 리뷰어 절차를 따른다.

## 최신 #950 결합 재시험 (2026-11-26)

최신 `dev` `8dbec877`와 #950 `0eeacbdb`, 이 PR `ca4d255c`를 임시 worktree에서 결합한 `c8c0b756` 제품·fixture 이미지를 사용했다. 실제 PR 브랜치를 합치거나 제품에 장애 주입 코드를 넣지 않았다. 두 독립 설치 모두 PostgreSQL 16, 소유 HTTPS 대상, 로컬 합성 모델을 사용했으며 외부 대상·유료 모델 호출은 0건이었다.

| 시험 | 결과 | 관찰 |
| --- | --- | --- |
| R03 새 일반 진단 | PASS | 전용 설치 `pr959-integrated1126`에서 S09R로 미완료 GET head를 만든 뒤 운영자 복구하고 같은 DB·Host 볼륨을 재사용했다. 정상 이미지로 Host가 healthy가 된 뒤 S02 일반 진단·선택 검증은 감사 완료, 후보 1건, 승인 전 송신 0건, 승인 후 GET 1건, 범위 밖 송신 0건, 내부·공유 HTML/DOCX 4종, `model_supported`로 완료됐다. fixture 재시작 직후 첫 S02는 DNS TXT 시험 증명이 휘발되어 `ownership_not_verified`로 실패했다. 저장된 동일 challenge를 **소유 DNS fixture에만** 다시 게시한 후 재시험이 통과했다. |
| R06 복구 연결 직전 중단 | PASS | 별도 설치 `pr959-fault1126`에서 S09R 승인 GET 1건 후 모든 송신 서비스를 정지했다. 시험 전용 Go overlay 이미지의 `TEST_FAULT_BEFORE_LINK` 표식을 확인하고 그 복구 컨테이너만 SIGKILL 했다(exit 137). 최종 `operator-recovery.json`은 없고 원본 head는 revision 2 `open`, pending 1건이었다. 정상 이미지로 동일 revision/checkpoint 복구를 두 차례 호출해 모두 멱등 응답을 받았고 최종 기록은 1개였다. 복구 전후 원본 `events.jsonl`·`head.json` SHA-256이 같았다. 기존 결과 불명 GET은 자동 재전송되지 않았다. |
| R06 이후 신규 실행 | PASS | 같은 `pr959-fault1126` DB·Host 볼륨에서 정상 이미지로 Host 재시작 후 새 S02 일반 진단·선택 검증이 감사 완료, 후보 1건, 승인 전 송신 0건, 승인 후 GET 1건, 범위 밖 송신 0건, HTML/DOCX 4종 및 `model_supported`로 끝났다. 유료 호출 0건. 첫 시도는 S09R용 합성 모델 fixture를 그대로 둬 `unreviewable_plan`으로 실패했고 대상 후속 송신은 없었다. fixture를 S02 모드로 재설정한 재시험이 통과했다. |

이 결과는 #950·#959의 **결합 이미지** 제품 시험이며 #959 단독 브랜치의 최종 HEAD 제품 시험으로 기록하지 않는다. 실패한 첫 시도 두 건도 시험 환경 설정 문제로 남긴다. R06 overlay는 정상 복구 실행기에만 적용한 시험 이미지로, 운영용 이미지에 포함되지 않는다.

## R06 시험 전용 결정적 장애 주입

`scripts/eval/prepare_e2_recovery_fault.py <새 임시 디렉터리>`를 #950과 이 PR을 결합한 **임시 통합 worktree**에서 실행한다. 스크립트는 `e2host/recovery.go`의 `operator-recovery.json` 최종 hard link 직전에 `TEST_FAULT_BEFORE_LINK`를 stderr에 쓰고 무기한 대기하는 **Go build overlay만** 임시 디렉터리에 생성한다. 제품 소스나 영구 런타임 플래그는 바꾸지 않는다. 소스의 link 지점이 달라지면 스크립트가 실패한다.

```sh
python3 -I scripts/eval/prepare_e2_recovery_fault.py /tmp/<새-전용-디렉터리>
go build -overlay=/tmp/<새-전용-디렉터리>/overlay.json -o /tmp/<새-전용-디렉터리>/first-controlled-diagnosis ./cmd/first-controlled-diagnosis
docker build --build-arg BASE_IMAGE=<동일-통합-HEAD-제품-이미지> -t <전용-fault-이미지> /tmp/<새-전용-디렉터리>
```

정상 이미지로 원본 미완료 GET head를 만든 뒤 모든 송신 서비스를 정지한다. 전용 fault 이미지를 **복구 컨테이너에만** 사용해 `recover`를 시작하고, 해당 컨테이너의 stderr 로그에서 `TEST_FAULT_BEFORE_LINK`가 나타난 뒤 그 컨테이너만 SIGKILL 한다. 최종 기록 부재, 원본 head `open`, 원본 journal/head 해시 불변을 확인한다. 이후 정상 이미지를 쓰는 일회성 복구 컨테이너로 같은 head를 재시도하고 기록 1개·멱등 응답·신규 실행 가능 여부를 검사한다. 시험 후 fault 컨테이너와 임시 overlay 디렉터리를 자신의 시험 환경에서만 제거한다. 위 표에 이 절차를 수행한 새 제품 R06 결과를 기록했다.
