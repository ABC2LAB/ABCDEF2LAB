# 평가 스냅샷 저장기

`write_snapshot`은 신뢰 수집기가 전달한 비민감 챌린지 응답 bytes를 pre/post 원자료로 저장한다.
[러너 기록 v1](run-record.md)의 producer 보조 함수이며, 수집 시각·실행 수명·해결 주체를 실제로 관측하는 러너와 함께 사용한다.
이 후속 구현은 PR #34의 입력 계약에 의존한다. PR #34 변경과는 별도 브랜치에서 검토한다.

## 사용법

```python
from snapshot_writer import SnapshotContext, write_snapshot

context = SnapshotContext(
    run_id=run_id,
    model_tag=model_tag,
    reset_generation=reset_generation,
    instance_id=instance_id,
    execution_id=execution_id,
)
artifacts = write_snapshot(
    run_directory,
    phase,                  # "pre" 또는 "post"
    challenge_response_bytes,
    context,
    max_bytes=snapshot_max_bytes,
)
runner_record["artifacts"][phase + "_raw"] = artifacts.raw
runner_record["artifacts"][phase] = artifacts.normalized
```

`run_directory`는 이미 만들어진 실행 결과 디렉터리다. 신원 필드와 크기 상한은 실제 수집기 설정에서 전달한다.
수집기는 비민감 챌린지 응답 body만 넘긴다. 인증 헤더·쿠키·계정 원문·일반 스캔 로그를 이 API에 전달하지 않는다.
저장기는 JSON 중복 key·비유한수·잘못된 solved/GT key와 알려진 민감 필드값을 거부한다.
민감 필드명은 앞뒤 공백(Unicode 공백 포함)을 제거하고 소문자로 비교한다. ` cookie `·탭/개행이 붙은 `TOKEN`도 해당 알려진 필드로 검사하며, 값이 null 또는 `[REDACTED]`일 때만 통과한다. 이 비교는 알려진 이름 목록을 확대하거나 원본 JSON bytes를 변경하지 않는다.
이 검사는 수집기의 비민감 데이터 선별을 대체하지 않는다. 원본 JSON은 수정/마스킹하지 않고 bytes 그대로 보존한다.

## 출력과 실패 처리

- `pre/raw.json`, `pre/normalized.json` 또는 `post/raw.json`, `post/normalized.json`을 만든다.
- 단계 디렉터리는 0700, 파일은 0600이며 기존 단계나 파일은 덮어쓰지 않는다.
- 열린 디렉터리 FD와 `O_NOFOLLOW`로 경로를 열며 POSIX 기능 미지원 환경은 실패한다.
- 정상 반환 전에 두 파일을 flush/fsync한다. 반환값의 descriptor는 상대경로·SHA-256·크기·단계·5개 회차 신원을 담는다.
- 정상 반환값의 `challenge_count`·`solved_count`는 평가용 요약이다. GT 정보와 파일을 엔진/LLM에 전달하지 않는다.
- 저장 오류가 나면 이번 호출이 만든 파일만 정리한다. 다른 프로세스가 만든 충돌 파일은 유지하고 정리 미완료 오류를 반환한다.
- 강제 종료나 디스크 장애로 남은 미완성 단계는 재사용하지 않는다. 러너 기록을 발행하기 전에 기존 검증기로 파일·해시·회차를 확인해야 한다.

## 실제 수집 경계

이 함수는 네트워크 요청이나 엔진 실행을 시작하지 않는다. 실제 독립 수집기가 raw를 받은 뒤 호출하는 저장 경계다.
러너는 실제 관측 시각과 증가 sequence를 timeline에 기록하고, post 전에 신규 송신 중지와 진행 요청 종료를 확인한다.
이벤트 관측·귀속과 HMAC 서명/키 격리는 러너 기록의 별도 책임이다. 저장 성공만으로 그 조건이 충족되지 않는다.

검증은 합성 bytes·임시 디렉터리로 수행한다. 실제 대상·모델·baseline 평가 완료를 의미하지 않는다.
