# E② 감사 파일 오프라인 검증 명령

상태: 병합된 [감사 저장기](./e2-audit-journal.md)의 `auditlog.Verify`를 호출하는 운영자용 Linux 명령. 서버·LLM·DB·대상 서비스에 연결하지 않고 입력 파일을 수정하지 않는다. 서버 실행 어댑터·KG·UI 없이 사용할 수 있다.

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

전체 검증이 성공해야 stdout에 기존 `auditlog.Summary` JSON 한 줄을 쓴다. `Checkpoint`와 `Intents`·`Results` 건수를 포함하며 요청/응답 본문·개별 이벤트·인증 헤더·키·입력 경로는 출력하지 않는다. checkpoint의 참조는 저장기가 요구하는 비민감 식별자라는 전제가 유지된다.

| 경우 | 종료 코드 | 출력 |
|---|---:|---|
| 전체 검증 성공 | 0 | stdout에 요약 JSON, stderr 비움 |
| `--help`/`-h` | 0 | 고정 사용법 |
| 인자 누락·알 수 없는 옵션·위치 인자 | 2 | stderr에 고정 사용법 |
| 입력/키/형식 오류 | 1 | `audit_input_unavailable` 또는 `audit_input_invalid` |
| MAC·범위·순서·최종 checkpoint 불일치 | 1 | `audit_integrity_failed` |
| 취소 | 1 | `audit_verification_canceled` |
| 출력 실패 | 1 | `audit_output_unavailable` |
| Linux 이외 환경 | 1 | `audit_platform_unsupported` |

입력·검증 실패에는 stdout 요약을 남기지 않고, stderr에도 원문·경로·원시 OS 오류를 노출하지 않는다. 출력 장치의 부분 쓰기 실패는 stdout에 불완전한 prefix를 남길 수 있으므로 **소비자는 종료 코드 0과 완전한 JSON을 모두 확인**한다. 일반 파일의 커널 IO 자체를 강제로 중단하는 명령은 아니며, 취소는 검증 함수의 협력적 context 확인을 따른다.

## 검증 의미

이 명령은 **제공한 신뢰 checkpoint와 journal 전체가 인증·범위·체인·끝부분까지 일치하는지** 검사한다. 최신 checkpoint를 제공했을 때 변경·삭제·과거 prefix 복원을 거부한다. 운영자가 오래된 checkpoint와 당시 파일을 함께 제공하면 둘의 일치는 검증할 수 있어도 그 checkpoint가 최신인지는 증명하지 못한다.

이벤트 수는 실제 요청 전송 건수나 취약점 수와 다르다. intent 기록이 있어도 전송이 실패할 수 있고 result에는 거부도 포함된다. 이 명령은 승인 적정성·로그인 성공·접근 성공·E③ 취약점 판정·OS 권한 분리를 검사하지 않는다. HMAC 키가 유출된 공격자의 재서명은 별도의 키 보호가 필요하다.

합성 journal·키·checkpoint로 정상 검증과 내용/끝부분 변조, 다른 범위/키/checkpoint, 입력 형식·권한·파일 종류, 취소·출력 실패를 시험한다. 실제 스캔 로그·계정·모델 호출·baseline 자료는 사용하지 않는다.
