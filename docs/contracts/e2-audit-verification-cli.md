# E② 감사 파일 오프라인 검증 명령

상태: 등록 GET [감사 저장기](./e2-audit-journal.md)의 `auditlog.Verify`(요약)·`auditlog.VerifyTimeline`(타임라인), 또는 일반 HTTP `e2proxy.VerifyJournal`(요약)을 호출하는 운영자용 Linux 명령. 서버·LLM·DB·대상 서비스에 연결하지 않고 입력 파일을 수정하지 않는다. 서버 실행 어댑터·KG·UI 없이 사용할 수 있다.

## 실행

레포 루트에서 소스 빌드한다. 아래 경로는 운영자의 실제 비공개 경로로 교체하며 키 원문은 명령행·환경 변수·공유 문서에 넣지 않는다.

```sh
go build -o /tmp/e2-audit-verify ./cmd/e2-audit-verify
/tmp/e2-audit-verify \
  --journal /srv/e2-export/RUN.jsonl \
  --checkpoint /srv/trusted-checkpoints/JOURNAL.checkpoint \
  --key-file /srv/e2-secrets/audit.key
```

- `--journal`: 마감된 journal 또는 검증 중 변경되지 않는 사본. 실시간으로 쓰는 파일은 사용하지 않는다.
- `--checkpoint`: **운영자가 별도로 신뢰하는 최신 checkpoint**. 파일을 journal에서 추출·추정·생성하지 않으며 기본 탐색 경로도 없다. 같은 쓰기 권한 안의 두 파일을 같이 제공한다고 신뢰가 분리되는 것은 아니다.
- `--key-file`: 해당 `KeyRef`의 32바이트 바이너리 HMAC 키를 담은 일반 파일, 권한 `0600`. hex/base64 문자열·개행·공백을 자동 변환하거나 제거하지 않는다.

각 입력은 일반 파일이어야 한다. 부모 디렉터리를 고정한 뒤 basename을 `openat`의 `O_NOFOLLOW|O_NONBLOCK`으로 열어 최종 심볼릭 링크·FIFO·디바이스·디렉터리를 거부한다. 부모 경로의 신뢰·접근 권한은 운영자가 관리한다. 키·checkpoint·journal을 서로 구분해 보관하고 저장기와 동일한 키를 선택해야 한다.

checkpoint는 기존 `auditlog.Checkpoint`의 `json.Marshal` 형식으로 보존한다. 필드명·순서·누락·중복·추가 필드·대소문자 별칭·후행 JSON을 엄격히 검사하며 크기는 4KiB 이하다. 들여쓰기/공백은 허용하지만 수동으로 재구성한 다른 직렬화 형식은 받지 않는다. 키와 checkpoint를 먼저 제한 크기로 읽고, journal은 스트리밍 검증한다.

## 출력과 종료 코드

기본 실행은 기존대로 전체 검증이 성공해야 stdout에 `auditlog.Summary` JSON 한 줄을 쓴다. `Checkpoint`와 `Intents`·`Results` 건수를 포함하며 요청/응답 본문·개별 이벤트·인증 헤더·키·입력 경로는 출력하지 않는다. checkpoint의 참조는 저장기가 요구하는 비민감 식별자라는 전제가 유지된다.

`--timeline`은 같은 키·checkpoint·journal 전체를 한 번 검증한 뒤, 시간순 JSON 배열 한 줄을 출력한다. 각 항목은 `at`(이벤트 시각), `account_ref`, `resource_ref`, `stage`, `reason`, `observed`(`admitted`, `request_written`, `response_received`, `status_code`)만 포함한다. 시각이 같은 항목은 journal 순번을 유지한다. 정렬 기준은 `at`이므로 서버 시계가 뒤로 가면 서로 다른 시각의 항목은 journal 기록 순서와 다르게 보일 수 있다. 출력에는 sequence·attempt가 없어 이 투영만으로 전체 기록 순서나 intent/result 짝을 복원할 수 없다. 참조는 저장기와 서버가 보증하는 비민감 ID라는 전제가 필요하며 이 명령은 참조의 실제 의미를 재검증하지 않는다. 전송 관측값은 애플리케이션 접근 성공이나 취약점 판정이 아니다.

`--max-events N`은 타임라인 모드에서만 사용한다. 기본값과 허용 상한은 1000이며 1~1000만 받는다. 인증된 이벤트가 이 수를 넘으면 끝까지 검증한 뒤 `audit_timeline_limit_exceeded`로 실패하고 stdout은 비운다. 전체 검증 실패나 마지막 레코드 오류가 있으면 앞선 항목도 출력하지 않는다. 정상적인 빈 journal은 `[]`를 출력한다. 타임라인도 입력 사본이 검증 중 바뀌지 않아야 한다.

| 경우 | 종료 코드 | 출력 |
|---|---:|---|
| 전체 검증 성공 | 0 | stdout에 기본 요약 JSON 또는 `--timeline` 배열, stderr 비움 |
| `--help`/`-h` | 0 | 고정 사용법 |
| 인자 누락·알 수 없는 옵션·위치 인자·`--max-events` 범위 밖 또는 `--timeline` 없이 지정 | 2 | stderr에 고정 사용법 |
| 입력/키/형식 오류 | 1 | `audit_input_unavailable` 또는 `audit_input_invalid` |
| MAC·범위·순서·최종 checkpoint 불일치 | 1 | `audit_integrity_failed` |
| 취소 | 1 | `audit_verification_canceled` |
| 타임라인 출력 상한 초과 | 1 | `audit_timeline_limit_exceeded` |
| 출력 실패 | 1 | `audit_output_unavailable` |
| Linux 이외 환경 | 1 | `audit_platform_unsupported` |

입력·검증 실패에는 stdout 요약이나 타임라인을 남기지 않고, stderr에도 원문·경로·원시 OS 오류를 노출하지 않는다. 출력 장치의 부분 쓰기 실패는 stdout에 불완전한 prefix를 남길 수 있으므로 **소비자는 종료 코드 0과 완전한 JSON을 모두 확인**한다. 일반 파일의 커널 IO 자체를 강제로 중단하는 명령은 아니며, 취소는 검증 함수의 협력적 context 확인을 따른다.

## 검증 의미

이 명령은 **제공한 신뢰 checkpoint와 journal 전체가 인증·범위·체인·끝부분까지 일치하는지** 검사한다. 최신 checkpoint를 제공했을 때 변경·삭제·과거 prefix 복원을 거부한다. 운영자가 오래된 checkpoint와 당시 파일을 함께 제공하면 둘의 일치는 검증할 수 있어도 그 checkpoint가 최신인지는 증명하지 못한다.

이벤트 수는 실제 요청 전송 건수나 취약점 수와 다르다. intent 기록이 있어도 전송이 실패할 수 있고 result에는 거부도 포함된다. 이 명령은 승인 적정성·로그인 성공·접근 성공·E③ 취약점 판정·OS 권한 분리를 검사하지 않는다. HMAC 키가 유출된 공격자의 재서명은 별도의 키 보호가 필요하다.

합성 journal·키·checkpoint로 정상 검증과 내용/끝부분 변조, 다른 범위/키/checkpoint, 입력 형식·권한·파일 종류, 취소·출력 실패를 시험한다. 타임라인은 정확한 JSON·전송 관측값 투영, 상한에 도달한 뒤의 후반 변조 우선순위, 같은 시각을 섞은 40건의 안정 정렬, 기본/명시 상한 1000건 성공·1001건 무출력 실패를 시험한다. 실제 스캔 로그·계정·모델 호출·baseline 자료는 사용하지 않는다.

## 일반 HTTP 감사 형식 선택

일반 프록시의 `e2-http-v1` journal은 등록 GET의 journal v1과 별도 형식이다.
`--journal-format e2-http-v1`을 명시하면 같은 안전 파일 리더를 거쳐
`e2proxy.VerifyJournal`에 **항상 제공된 checkpoint를 전달**한다. 형식을 자동 탐지하거나
검증 실패 뒤 다른 형식으로 재시도하지 않는다. 옵션 생략 또는
`--journal-format registered-get-v1`은 기존 등록 GET 동작을 선택한다.

```sh
/tmp/e2-audit-verify \
  --journal-format e2-http-v1 \
  --journal /srv/e2-http-export/RUN.journal \
  --checkpoint /srv/trusted-checkpoints/HTTP.checkpoint \
  --key-file /srv/e2-secrets/http-audit.key
```

- checkpoint는 `e2proxy.JournalCheckpoint`의 `json.Marshal` 형식이며 4KiB 이하다.
  들여쓰기·공백을 제외한 전체 필드명·순서·누락·중복·추가 필드·별칭·후행 값 검사는
  등록 GET과 같다. 다른 형식의 checkpoint는 거부한다.
- 키는 일반 HTTP 저장기가 받는 **32~4096바이트 raw HMAC 키**이고 일반 파일·`0600`이어야 한다.
  기존 keygen의 32바이트 키를 사용할 수 있다. 등록 GET 모드의 키 길이는 계속 32바이트다.
- 성공 출력은 `e2proxy.JournalSummary` JSON 한 줄(`Checkpoint`, `Events`)뿐이다.
  이벤트·시도별 상세·입력 경로·키는 출력하지 않는다. `Events`는 요청 전송 건수나
  완료된 호출 수가 아니다. 정상적인 header-only journal도 0건 요약으로 검증된다.
- 일반 HTTP 모드에 `--timeline` 또는 `--max-events`를 지정하면 종료 코드 2로 거부한다.
  기존 GET 타임라인으로 변환하지 않는다. 라이브러리의 바이트·행·이벤트 검증 상한은
  그대로 적용되며, 초과 시 `audit_verification_limit_exceeded`·종료 코드 1·stdout 비움이다.
- 나머지 취소·입력·무결성·출력 실패의 종료 코드와 고정 진단은 위 표를 따른다.

`JournalCheckpoint`는 Sync가 확인된 prefix다. Record가 취소되면 이 prefix가 전진했어도
호출자가 ACK를 받지 못했을 수 있다. 따라서 파일 일치는 Execution ACK·`AuditComplete`·
호출 마감/결과의 완전성·현재 host head/epoch·키의 운영 권한·승인·E③ 판정을 증명하지 않는다.
오래된 journal과 그때의 checkpoint를 함께 제공한 경우의 일치를 최신성으로 해석하지 않는다.
외부 최신 정본 조회·일반 E②와 IDOR의 같은 실행 연결·실제 배치 수용은 별도 작업이다.
