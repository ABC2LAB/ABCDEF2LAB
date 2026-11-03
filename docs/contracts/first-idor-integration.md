# 최초 IDOR 조회의 엔진 → 판정 → 저장 연결

## 범위와 기준

등록된 owner/evaluated 계정의 같은 비민감 자원을 조회하는 지원 경로에 한정한다.
기존 `transmit.VerifyRead`의 의미, KG/검증기/오라클, 평가 러너 계약은 변경하지 않는다.
`VerifyReadDetailed`는 같은 실제 호출에서 관찰·완결 영수증·동결 정책을 전달한다.
저장을 위해 다시 송신하거나 KG를 최신 버전으로 다시 판정하지 않는다.

이 구현의 통합 기준은 dev `b1dea7458b8ff80744991de5b5c46c54f91465ec`다.
착수 때 포함한 선행 head와 최종 상태는 다음과 같다.

| 선행 | 착수 시 확인 head | 최종 확인 |
|---|---|---|
| #130 저장 결정안 | `9b07265ca21ece5ab74fbf4521690cbfb37cd903` | 최종 head `646102123c85c11e7bd09b523c408a8e037d1012`, 위 dev에 병합됨 |
| #131 mode 활성화 계약 | `15f16ead41edc7ad141725f4efefb65013d671cd` | 동일 head가 dev에 병합됨 |

두 갈래는 분리 작업트리에서 검토·검증한 뒤 정상 merge로 결합했다.
E② 고유 commit은 `7641c837f6a56863ac58b6c37343c440dbb5ec14`,
E③ 고유 commit은 `48a92abb067882c4717531a6c2cda672775a6c9d`다.
`server/server.go`와 `db/db.go`는 결합 담당자 한 명이 작성했다.
게시 시 dev 대비 고유 diff에는 E② 실행 연결, E③ 최초 저장 및 이 결합부만 포함한다.
실제 통합 순서는 mode fence/marker → 검증된 조회 → 필수 감사 마감 → 판정/발급/최초 finding의 원자 저장이다.

## 신뢰 호스트가 공급할 경계

`server.NewWithControlledReadHost`는 복구 작업·백그라운드 admission 전에 다음을 설치한다.

- `ControlledTaskModeHost`: 실행 store와 같은 0700 root의 열린 anchor, 현재 root 검증,
  독립적인 작업·자식 프로세스 quiescence 증명. 콜백은 배출을 시작하거나 잠금 안에서 배출을 기다리지 않는다.
- `ControlledReadFindingResolver`: 현재 request와 정확히 결속된 candidate, 실제 KG resolver,
  `evidencebind.Context`, `e3record.SourceReader`.
- 선택적 로컬 LLM pin manifest. 미지정이면 기존 DB/env provider 선택이 적용되며,
  이 생성자가 완전 로컬 모드나 평가 provider 조건을 자동으로 증명하지 않는다.

이 설정은 Go 호스트 조립용이다. 모델 JSON·일반 HTTP 요청에서 source/writer/모드를 받지 않는다.
필수 host 입력이 없거나 mode가 unknown이면 실행을 허용하지 않는다.
controlled-only 초기 구성이며 전체 기존 작업의 eager legacy 이관은 제공하지 않는다.

SourceReader는 해당 실행 전에 동결한 KG snapshot ID/version/digest, 승인·계정 결속,
하네스 세대·인스턴스·reset 세대와 모델 식별자를 독립적으로 검증한다.
보호된 최신 checkpoint/journal을 읽고 신뢰 키로 검증한 뒤 동일 scope/sequence/digest를 돌려준다.
`ControlledReadCheckpointDigest`는 typed checkpoint의 내용 digest일 뿐 MAC 진위나 최신성을 증명하지 않는다.
하네스 세대 참조를 run/reset 문자열에서 추론하지 않는다.

엔진 전용 collector는 그 조회에서 받은 실제 관찰·영수증만 동결하고,
수집 digest·별도 생성 시각·완료 claim·감사 마감 성공을 공급한다.
Source에 이 엔진 필드가 이미 있으면 동일값이어야 한다.
Source는 KG·승인·신원·하네스의 진위를 맡으며 collector가 이 정보를 만들어 넣지 않는다.
응답 원문과 canary 값은 E③ manifest/payload에 저장하지 않는다.

## 완료와 재시도

완결 관찰과 감사 기록을 확인한 뒤 Engine의 work 잠금 아래 완료 claim을 획득한다.
claim 이전 stop/pause/cancel은 저장하지 않는다.
claim 이후 task pause는 다음 실행을 막고 해당 intent의 저장을 계속한다.
저장 성공이 확인된 경우에만 완료 intent의 done 처리를 유지한다.
shutdown은 제한 시간 내 마감도 취소할 수 있으며 실패를 성공으로 바꾸지 않는다.
Source/DB I/O 중에는 work/runtime/recorder 잠금을 잡지 않는다.

최초 저장은 mode·소유 관계를 transaction 안에서 확인하고 verdict/manifest/issuance와
CONFIRMED finding/node/link를 원자 커밋한다. 그 밖의 판정은 구조화 기록만 만든다.
첫 prepared operation key/input digest를 고정한다. 같은 입력 재시도는 DB 멱등 경계를 사용하며,
응답 유실 뒤 Source revision 등 불변 입력이 바뀌면 두 번째 writer 호출 전에 conflict다.
입력/모드 거절, 충돌, 커밋 결과 불명을 구분한다. 결과 불명을 미저장으로 단정하지 않는다.
실제 실행의 finish는 finalizer를 한 번 호출하며 자동 재시도·DB 대조를 수행하지 않는다.
실패 시 모델/intent에는 일반 실패만 돌려주고, 서버 로그에는 고정 사유
`storage_rejected`·`storage_conflict`·`storage_outcome_unknown`(기타 callback 오류는
`finalization_rejected`)와 사용 가능한 비민감 operation key를 남긴다. Prepare 이전 등 key를
발급하지 못한 경우 `unavailable`이다. 원문 Source/SQL 오류·자격값은 로그에 넣지 않는다.
ACK 유실로 intent가 blocked여도 해당 key로 저장 행을 운영자가 대조해야 하며,
대조·복구 API나 자동 재송신 기능을 구현했다고 주장하지 않는다.

## 확인한 시험과 한계

`TestFirstIDORSyntheticEngineThroughStoredFinding`은 fake LLM과 명시적인 합성 신원/KG/하네스,
owned HTTP 대상, 격리 PostgreSQL을 사용한다. 실제 Juice Shop 진단이나 평가 결과가 아니다.

| 시험 | 확인 |
|---|---|
| confirmed | owner/cross 실제 HTTP 2회, 감사 HMAC 검증, finding/node/issuance 각 1개, intent done |
| late-pause | 완료 claim 뒤 pause, 같은 저장 1개와 done 유지, DB·메모리 pause 유지 |
| missing-source | HTTP 0회, 저장 0개 |
| source-error / checkpoint-mismatch | HTTP 각 2회, 저장 0개 |
| unknown-commit-source-change | 실제 커밋 1개 뒤 ACK 유실, 결과 불명, 변경된 Source 재시도는 conflict·writer 총 1회 |

위 6개와 상위 테스트는 race 실행에서 7 pass / 0 fail / 0 skip이다.
격리 DB의 canonical 판정 6건은 Python `json.dumps(sort_keys=True, separators=(",", ":"),
ensure_ascii=False, allow_nan=False)`와 정확히 일치했다. HTML 문자·비ASCII·U+2028/U+2029를 포함한다.
원문 payload·비밀·canary 값을 출력하거나 commit하지 않고 건수만 확인했다.
재현 시 두 DB 환경 변수 `ARTEX_PG_DSN`·`ARTEX_E3_TEST_DSN`은 전용 시험 DB를 가리켜야 한다.

```sh
go test -race ./server -run '^TestFirstIDORSyntheticEngineThroughStoredFinding$' -count=1
```

전체 수용시험·운영 활성화 완료를 주장하지 않는다. generic 저장 API는 known-controlled의
생성/승격/근거 변경/재검증을 거부하지만 모든 unknown 작업의 전역 DB gate는 후속이다.
정본 1~65536바이트·signed int64·판정 UTC6 Z는 #142 평가 담당의 v1 결정이다. 다른 운영 권한·보존 정책의 확정으로 확대하지 않는다.
공유 예산의 실제 서버 연결, 재검증 저장·수동 확정·평가 export·Preview REST도 이 범위 밖이다.

## 실제 Juice Shop 첫 회차 전 남은 연결

1. 실제 신뢰 host가 위 SourceReader·KG resolver·root/자식 quiescence 증명을 공급해야 한다.
   등록 신원·승인·KG 출처를 임의 생성해 활성화하지 않는다.
2. 설정으로 고정한 Juice Shop v15.0.0과 reset/instance, A의 비민감 자원·B 계정,
   조회 승인·만료·예산, 동결 KG/canary/harness 세대를 검증한다.
3. 실제 LLM/provider/model/sampling과 평가 여부를 확정한다. 평가 회차라면 신뢰 러너의
   `eval_run_id`도 별도로 공급하며 E② execution ID에서 추론하지 않는다.
4. 위 조건을 확인한 뒤 실제 회차를 순차 실행하고 최종 감사·판정/발급/link·finding과
   기존 결과 조회를 확인한다. 합성 통과 건수와 실제 진단 결과는 따로 보고한다.

현재 이 변경에서는 실제 Juice Shop 회차를 실행하지 않았다.

## #142 리뷰 피드백 검증

- controlled 작업 삭제 시험은 일반 finding 생성 API를 우회하는 **합성 DB fixture**로 준비하고 삭제 거부와 출처 보존을 다시 확인했다. 실제 저장 권한을 raw INSERT로 제공한 것이 아니다.
- verdict/issuance/link/manifest의 UPDATE·DELETE·TRUNCATE, mode version·intent kind·manifest 내용·링크 재생·읽기 digest·running retest 전환의 거부와 legacy 대조를 보강했다. completed 전용 가드는 후단 finding 상태 가드에 가려지지 않도록 `reproduced` 대조를 포함한다.
- worker가 반환한 뒤 최종 감사 저장과 실제 완료 claim 사이에서 work/task 취소·pause·delete·action을 각각 시험했다. 허용 대조는 저장 1회·HTTP 2회, 거부 대조는 저장 0회·HTTP 2회다.
- 서명 정수·시각·65536/65537 경계, checkpoint sequence 0·생성 시각, digest를 다시 맞춘 위조 판정의 재계산 거부를 확인했다. [공용 벡터](../../e3record/testdata/canonical-v1.json) 27개는 Go와 별도 Python 검사가 같은 파일을 읽었다. 현재 `collect.py`는 새 Prepare 기대 payload를 받고, 과거 `record_version` 추가 대조는 거부한다.
- 최종 **분할 race 검증 집계는 3794 pass / 0 fail / 2 skip**(상위·하위 사례 포함)이다. 첫 전체 실행은 UID/조상 소유자·DNS 격리 조건과 server 누적 10분 제한으로 종료코드 1이었다. 제품 Go 코드를 바꾸지 않고 해당 환경 패키지와 미완료 server 시험만 재검증해 집계했다. server 상위 시험 428개의 종료 결과를 모두 확인했다.
- skip은 외부 모델 live 시험 `TestLiveContextReview`와 실제 root 권한이 필요한 `TestRunRejectsParentOwnedByAnotherUser`다. 전체 vet·Linux 빌드·Windows server 빌드·gofmt·문서 상대 링크 34개·diff 검사가 통과했다.

이 수치는 합성 회귀이며 실제 Juice Shop 실행·운영 권한 공급·처리량 벤치마크 결과가 아니다. 평가 Python의 공용 벡터 회귀 연결은 최민준 담당 후속으로 남긴다.
