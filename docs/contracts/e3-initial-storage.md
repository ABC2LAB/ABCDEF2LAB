# E③ 최초 판정 저장 구현 경계

이 문서는 최초 horizontal read 저장의 개발 구현과 남은 연결을 기록한다.
팀의 미확정 KG 발급·운영 권한·보존 정책을 확정하지 않는다.

## 기준과 의존

- 최종 통합 검토 기준: `dev b1dea7458b8ff80744991de5b5c46c54f91465ec`.
- 저장 결정은 위 dev에 포함된 [저장 게이트](e3-storage-gate.md)를 기준으로 읽는다.
  #130 최종 head `646102123c85c11e7bd09b523c408a8e037d1012`는 이 기준에 이미 통합됐다.
- E③ 개발 갈래 `codex/first-idor-e3`를 `codex/first-idor-integration`에 일반 merge로 결합했다.
- [verdict-record](verdict-record.md),
  [저장 게이트](e3-storage-gate.md),
  [증거 결속](e3-evidence-binding.md),
  [모드 DB](task-mode-db.md)를 사용한다.
- signed int64, 판정 시각 UTC 소수점 6자리 Z, canonical 1~65536바이트는
  #142 평가 담당 리뷰의 v1 결정이다([저장 게이트 §9.1](e3-storage-gate.md#91-팀-결정을-위한-저장-경계-제안)).
  `record_version`은 DB 열에만 두며 정형 payload에는 넣지 않는다.
  공용 벡터는 [canonical-v1.json](../../e3record/testdata/canonical-v1.json)이며 평가 reader의 회귀 연결은 별도 후속이다.

## 호출과 신뢰

`e3record.Prepare(ctx, Input, SourceReader)`가 실행 메타데이터를 검증하고
외부에서 채울 수 없는 `Prepared`를 만든다.
`db.RecordControlledFinding(ctx, Prepared, TaskModeMarkerReader)`만
최초 판정·finding을 저장한다. 저장 함수는 HTTP 전송이나 VerifyRead를 다시 하지 않는다.

SourceReader는 신뢰 호스트가 설치하는 권한 경계다. 같은 Context를 문자열로
복사하는 DTO는 정본이 아니다. 다음 항목을 독립적으로 확인할 수 없으면 거부한다.

- KG snapshot/version/digest와 발급자, freeze 및 유효기간
- harness generation/reset/instance와 owner/evaluated 계정 결속
- 실행 완료와 Journal.Close 이후 최종 checkpoint의 보호된 영속 성공
- Task/Run/Case/Attempt/Intent/Resource/Policy/identity scope의 정확한 일치

`EvidenceDigest`는 실제 E② VerifyReadDetailed 호출에서 수집한 관찰·receipt·
result 메타데이터의 domain-separated SHA-256이다. 호스트의 비공개 Collect가
발행한 digest와 Prepare 입력이 같아야 한다. 최종화 DTO에서 정본을 재구성해
digest를 만들어서는 안 된다. SourceReader가 인증한 최종 checkpoint도 호스트가
실제로 저장한 checkpoint와 같아야 한다.

`RecordCreatedAt`은 호스트가 발행하고 보존하는 별도 생성 시각이며 관찰 시각을
복사하지 않는다. 최종 checkpoint의 sequence는 양수이고 Scope와 일치해야 한다.
취소·중지·실패 또는 cleanup 실행만으로 Completed/Finalized를 세우면 안 된다.

패키지는 실제 KG·harness·identity provider를 제공하지 않는다. 미설정 provider는
차단해야 하며 합성 시험의 SourceReader를 실제 진단용으로 사용하면 안 된다.

## 원자 저장

하나의 READ COMMITTED evidence 트랜잭션에서 다음을 수행한다.

1. task를 잠그고 DB fence와 신뢰 marker를 `taskmode.Resolve`로 재검증한다.
2. 실제 DB mode version이 Prepare 때의 version과 같은지 확인한다.
3. task/exploration/intent 소속을 확인한다.
4. 불변 manifest, canonical verdict, issuance를 저장한다.
5. CONFIRMED일 때만 finding node, finding, link를 같은 트랜잭션에서 저장한다.

finding node의 상태는 confirmed, finding의 사람 검토 상태는 pending이다.
다른 oracle 값은 구조화된 판정만 남기고 finding을 만들지 않는다.
kind/resource_ref/발급 scope가 맞지 않으면 저장하지 않는다.

operation key는 task/run/case/evidence revision/initial 목적을 묶는다.
같은 key와 같은 input digest의 재시도는 기존 결과를 돌려준다.
같은 key에 다른 입력은 conflict이며 부분 판정이나 finding을 남기지 않는다.
manifest/verdict/issuance/link의 UPDATE, DELETE, TRUNCATE는 trigger로 거부한다.
DB 역할과 보존·정리 절차의 운영 정책은 별도 팀 검토가 필요하다.

## Canonical과 조회

verdict canonical은 정렬 key, compact UTF-8 JSON, 정수 전용 수치, UTC 6자리
시각을 사용한다. HTML 문자와 U+2028/U+2029를 다른 문자열로 escape하지 않으며
invalid UTF-8을 거부한다. 마이크로초 절삭이 기존 oracle 결과를 바꾸면 거부한다.
manifest에는 관찰 메타데이터의 원래 정밀도를 보존한다.

raw body, cookie, token, 실제 canary 값은 저장하지 않는다.
canary에는 검증된 필드명만 남긴다.
`ControlledFindingRecord`는 저장된 canonical의 digest를 확인하여 읽고,
과거 KG를 재실행하거나 현재 정본 존재를 주장하지 않는다.

## 기존 API 경계와 미지원 범위

기존 finding 생성·traffic·finding 노드 상태 변경·삭제·confirmed/fixed triage 및 retest
진입은 **알려진 controlled fence**에 대해 전용 경로 사용을 요구한다.
negative ID의 기존 NULL sentinel과 존재하지 않는 대상의 no-op은 보존한다.
intent/fact 변경은 finding evidence 전역 잠금을 추가로 잡지 않는다.

현재 `cmd/artex/main.go`는 여전히 `server.New`로 시작하므로 신뢰 host가 자동으로 설치되지 않는다.
반면 generic AddFinding·SetNodeState·SetFindingStatus·retest 등의 호환 경로는 legacy를 포함해
`WithEvidenceTx`의 전역 evidence advisory lock과 mode guard를 거친다. legacy에 controlled 판정을
요구하지는 않지만, 잠금 획득·transaction 비용과 작업 간 직렬화 영향이 추가된다.
기능 회귀 결과와 처리량/지연 성능 검증은 구분하며, 성능 영향은 아직 벤치마크하지 않았다.

이 호환 guard는 전역 unknown 모드 저장 게이트의 완성본이 아니다.
기존 legacy API의 missing fence 동작은 유지하므로, 서버 admission이 unknown을
거부하고 controlled fence를 실행 및 파일 marker 공개 전에 commit해야 한다.
전용 최초 저장 경로는 missing/unknown marker와 불일치 mode version을 거부한다.
marker 자체의 run/reset/epoch freshness를 검증하는 기능은 이 저장소에 추가하지 않는다.

이번 단위는 initial issuance만 지원한다. controlled retest 생성·시작·결론·
completed 전환은 거부하며 실패/중지 정리는 허용한다. 사람 판정 저장과 retest
issuance, Preview REST, 다른 취약점·축, 운영 배포를 추가하지 않는다.

## 검증과 실제 회차

독립 코드 리뷰 후 source evidence 변조, stale isolation, retest admission,
legacy sentinel/no-op, non-finding 잠금 영향을 수정하고 회귀 검증한다.
PG16 전용 개발 DB의 합성 fixture만 사용한다.

이 단위의 단위·DB 시험 성공은 실제 Juice Shop 진단 성공이 아니다.
실제 회차 전에는 호스트의 E② Collect/finalizer 연결, 신뢰 KG/harness/identity
provider, 승인·공유 예산, 실제 계정 A/B 및 비민감 fixture, 최종 감사 checkpoint
영속성과 결과 조회를 함께 확인해야 한다.

