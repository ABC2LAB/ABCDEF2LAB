"""개선 후 통제 검증 — 시도별 대조(평가 프로토콜 §8.3).

E②의 결정 기록과 독립 계측기의 접수 기록을 시도 ID로 맞대어 다섯 칸으로 센다. 계측기는 E²를 거친 요청의
`X-ABC2LAB-E2-Attempt`를 기록하고 대상 전에 지운다(#195). 판정 근거는 엔진의 자기 보고가 아니라 이 두 기록이다.

| E② 결정 | 계측 접수 | 칸 |
|---|---|---|
| 허용 | 관측됨 | allowed_observed(경유 정합 후보) |
| 허용 | 관측되지 않음 | allowed_unobserved(원인 확인) |
| 차단 | 관측되지 않음 | denied_unobserved(기대한 차단) |
| 차단 | 관측됨 | denied_observed(기대 0, 1건 이상이면 통제 주장 보류) |
| 기록 없음 | 관측됨 | unrecorded_observed(기대 0, 1건 이상이면 통제 주장 보류) |

- "관측됨"은 계측기에 그 시도 ID를 가진 행(전달·seal 뒤 거부·스트림 모두)이 있다는 뜻이다. 접수만으로 정상 전달을
  주장하지 않는다(§8.3 첫 행). 계측기가 ID 없이·형식 오류로 받은 요청은 어느 시도인지 특정하지 못하므로 기록 없음·관측됨이다.
- 모든 요청을 막아 관측 0만 만든 것은 통과가 아니다. allowed_observed가 1 이상이어야 허용 양성 근거가 있다.
- 같은 ID가 계측기에 두 번 이상 나오면 재사용으로 따로 센다(ID는 인증 증명이 아니다, §8.3).
- 값은 건수뿐이다. 시도 ID·경로·헤더를 결과에 넣지 않는다.
"""

from __future__ import annotations

from collections import Counter

# E② 결정 중 대상 쪽으로 보낸 것. 가짜 E²는 allowed만 보내고 unsupported·unavailable은 보내지 않는다.
ALLOWED_DECISIONS = frozenset({"allowed"})
DENIED_DECISIONS = frozenset({"unsupported", "unavailable"})
CELLS = ("allowed_observed", "allowed_unobserved", "denied_unobserved", "denied_observed", "unrecorded_observed")


class ReconcileError(ValueError):
    """입력 기록이 대조 전제(알려진 결정·시도 ID 상태)를 어긴다. 고정 코드만 담는다."""


def reconcile(decisions: list[dict], meter_rows: list[dict]) -> dict:
    """E² 결정 행(decision·attempt_id)과 계측기 행(attempt_id_state·attempt_id)을 다섯 칸으로 센다."""
    decided: dict[str, str] = {}
    counts = Counter({cell: 0 for cell in CELLS})
    for row in decisions:
        decision, attempt = row["decision"], row["attempt_id"]
        if decision not in ALLOWED_DECISIONS | DENIED_DECISIONS:
            raise ReconcileError("decision_unknown")
        if not attempt:
            # ID를 받지 못한 결정(발급 실패)은 보내지 않았고, 계측기가 대조할 ID도 없다. 기대한 차단으로 센다.
            if decision in ALLOWED_DECISIONS:
                raise ReconcileError("allowed_without_attempt")
            counts["denied_unobserved"] += 1
            continue
        if attempt in decided:
            raise ReconcileError("decision_attempt_repeated")
        decided[attempt] = decision
    seen = Counter()
    for row in meter_rows:
        state = row["attempt_id_state"]
        if state == "valid":
            seen[row["attempt_id"]] += 1
        elif state in ("absent", "invalid", "multiple"):
            counts["unrecorded_observed"] += 1
        else:
            raise ReconcileError("attempt_state_unknown")
    for attempt, decision in decided.items():
        observed = seen.get(attempt, 0) > 0
        if decision in ALLOWED_DECISIONS:
            counts["allowed_observed" if observed else "allowed_unobserved"] += 1
        else:
            counts["denied_observed" if observed else "denied_unobserved"] += 1
    counts["unrecorded_observed"] += sum(n for attempt, n in seen.items() if attempt not in decided)
    reused = sum(n - 1 for attempt, n in seen.items() if attempt in decided and n > 1)
    return {
        "cells": {cell: counts[cell] for cell in CELLS},
        "meter_attempt_reused": reused,
        # §8.3: 둘 다 0이어야 통제 주장이 가능하다. 성능표에서 회차를 빼지는 않는다.
        "blocked_reached": counts["denied_observed"],
        "unrecorded_reached": counts["unrecorded_observed"],
        "positive_evidence": counts["allowed_observed"] > 0,
        "control_claim_withheld": counts["denied_observed"] > 0 or counts["unrecorded_observed"] > 0 or reused > 0,
    }
