# E③ 제품 판정 저장 연결(E1)

상태: 사용자 결정(2026-11-20) (가)를 구현하는 계약. 동찬님 조건 넷(#810 본문)과 같이 읽는다.
관련: E② Host 근거·마감 확인 #810, 판정 목록 #806(`product-verdict-list-proposal.md`), 판정 상세 OD06, 화면 #808.

## 1. 결정 (가) 요약

- 확정 단계에서 Host가 검증한 현재 head 체크포인트를 근거로 판정을 **바로** 저장한다(봉인을 기다리지 않는다).
- 봉인 뒤 그 체크포인트가 봉인 저널의 앞부분인지 따로 확인해 기록한다.
- 판정 행은 바꾸지 않는다. 봉인 확인은 별도 기록이고, 표시는 세 단계다.

| 봉인 확인 | 표시 | 판정 값 |
|---|---|---|
| 확인됨 | 그대로 | 보임 |
| 봉인 미확인(봉인 실패·ACK 불명·interrupted·진행 중·근거 체크포인트를 못 받음) | "봉인 미확인" 표시 | 보임 |
| 포함 실패(되감기·다른 분기·다른 저널) | 진위 실패 | 숨김 |

## 2. 근거 체크포인트 (회차당 하나)

- 확정 단계(`e3contract.Run`)가 끝나면 제품 연결이 그 회차의 마지막 근거 호출을 Host에 한 번 더 검증받는다(`VerifiedCheckpoint`, Host `VerifiedAt`). E② 마감 전, 확정 단계 안에서 한다.
- Host는 검증에 쓴 현재 head 체크포인트(`journal_id`·`sequence`·`mac`)를 돌려준다.
- 그 head는 확정 단계의 모든 근거 사건을 앞부분으로 포함한다. 같은 회차의 저장소 head는 CAS(`ExpectedRevision`·`Previous`)로만 앞으로 가고, Host가 매번 `VerifyCurrent`로 같은 사슬임을 확인하기 때문이다. 그래서 판정마다 체크포인트를 따로 두지 않는다.
- 받지 못하면(ACK 불명·Host 오류·연결 없음) 판정은 저장하되 체크포인트는 `null`이고, 봉인 확인은 "봉인 미확인"(`evidence_checkpoint_missing`)이다. 재전송·재시도는 하지 않는다.
- 근거 호출이 하나도 없는 회차(몫에 든 종류 없음 → 예산 UNDECIDABLE만)는 체크포인트를 묻지 않는다. 봉인 확인은 해당 없음(`evidence_seal: null`)이다.

## 3. manifest 형식 (제품 판정, record_version 2)

```json
{"format":"e3-product-manifest/v2","run_ref":"<run>","evidence_checkpoint":{"journal_id":"<id>","sequence":12,"mac":"<64hex>"}}
```

- `evidence_checkpoint`는 없으면 `null`이다.
- 옛 칸 `journal_ref`·`journal_digest_sha256`(봉인 뒤 저장 전제)를 대체한다. `RecordProductVerdict`는 지금까지 호출처가 없어 저장된 제품 행이 없으므로 형식 이름만 `e3-product-manifest/v2`로 올리고 옛 형식은 읽지 않는다(읽으면 진위 실패).
- manifest는 회차의 근거 체크포인트를 담는다. 같은 회차의 판정들은 같은 manifest를 쓴다.

## 4. 저장

- 확정 단계 결과가 `completed`·`interrupted`이면 판정마다 `RecordProductVerdict`로 INSERT한다. `confirmation_integrity_failed`는 판정 행이 없다(계약 §7 그대로).
- 저장이 실패해도 `e3-confirmation.json`과 모델 단계 결과는 그대로다. 실패는 고정 문구로 서버 로그에 남긴다(값 없음).
- 같은 회차·같은 종류를 두 번 저장하지 않는다(작업 키 멱등, 기존 `RecordProductVerdict` 규칙).

## 5. 실행 역할 권한

- `product_runtime`에 `e3_evidence_manifests`·`e3_oracle_verdicts`·`e3_verdict_issuances`·`e3_seal_checks` **INSERT만** 더한다(일련번호 USAGE 포함).
- UPDATE·DELETE·TRUNCATE는 주지 않는다. 기존 행 잠금용 `UPDATE(id)`는 그대로이고, 불변 트리거(`guard_e3_immutable`)가 실제 갱신을 막는다.
- 권한 시험: INSERT 성공, 판정 칸 UPDATE·DELETE·TRUNCATE 거부.

## 6. 봉인 확인 기록 `e3_seal_checks`

| 칸 | 뜻 |
|---|---|
| `task_id`·`run_ref` | 회차(UNIQUE) |
| `journal_id`·`head_sequence`·`head_mac` | 신뢰 저장소가 인증한 sealed head(Host `SealResult.Head`) |
| `checks` | Host가 내준 근거 체크포인트별 `{sequence, mac, included}` |
| `recorded_at` | 기록 시각 |

- E② 봉인 응답(`SealChecked`)이 결과를 주면 서버가 INSERT한다. 한 번만 쓰고 바꾸지 않는다.
- 봉인 실패·ACK 불명·결과 없음·interrupted는 행을 만들지 않는다. interrupted 결과는 Host 볼륨 기록(`seal-checks.json`, `phase: interrupted`)에만 있다.

## 7. 표시 (`evidence_seal`, 목록·상세 행마다)

목록 행과 판정 상세 응답(최상위 `evidence_seal`, 평가 판정은 늘 `null`)이 같은 칸·같은 규칙이다. 모양은 늘 `{"status": …, "reason": … | null}`이고 `reason`은 `unconfirmed`일 때만 있다.

| 값 | 조건 |
|---|---|
| `null` | 근거 호출이 없는 판정(봉인 확인 해당 없음) |
| `{"status":"confirmed","reason":null}` | 봉인 확인 행이 있고, 같은 `journal_id`이고, 판정의 근거 체크포인트(`sequence`·`mac`)가 `included: true` |
| `{"status":"unconfirmed","reason":"seal_not_recorded"}` | 근거 체크포인트는 있는데 봉인 확인 행이 없음(봉인 실패·ACK 불명·interrupted·진행 중) |
| `{"status":"unconfirmed","reason":"evidence_checkpoint_missing"}` | 근거 호출은 있는데 근거 체크포인트를 못 받음 |
| `{"status":"inclusion_failed","reason":null}` | 봉인 확인 행이 그 체크포인트를 `included: false`로 적었거나, 목록에 없거나, 다른 저널 → 행은 `record_integrity_failed`(값 숨김), `evidence_seal`은 남김 |

- `unconfirmed`는 판정 값을 바꾸지 않는다. 화면은 "봉인 미확인"으로 적는다.
- 저장된 봉인 확인 기록이 계약 모양이 아니면(위조로 봄) 그 회차의 근거가 있는 행은 모두 `inclusion_failed`다.
- 봉인 확인 행 자체를 읽지 못하면(DB 오류) 응답 전체 503이다. 다른 행의 값을 추측해 채우지 않는다.
