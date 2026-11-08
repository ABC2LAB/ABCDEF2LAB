"""증거 3조건 — confirmed 주장이 요청·응답 증거로 뒷받침되는지(평가 프로토콜 §4-2).

  (a) 판정 레코드의 traffic_evidence_refs가 비어 있지 않다.
  (b) 그 ref가 같은 회차 traffic 기록에 실재한다.
  (c) 그 ref 요청의 대상이 해당 챌린지 자원이다(GT 상관과 같은 기준: type 접두부 + vuln_class).

판정 단위(§4-2): 레코드는 (a)이고 (b)·(c)를 **함께** 만족하는 ref가 하나 이상이면 충족,
챌린지는 confirmed 레코드 중 충족 레코드가 하나 이상이면 충족. 레코드·ref를 많이 내서
확정을 얻는 길은 (c)가 막는다. traffic에 없는 ref(끊긴 ref)는 세지 않고 경고로 올린다.

traffic 인덱스는 논리 모양만 정한다 — `{traffic id: 요청 대상 resource_ref(type:id)}`.
공식 채점은 run_record가 서명된 러너 기록에 묶인 traffic 아티팩트에서만 읽는다.
실제 E² traffic export 연결은 후속이다. 인덱스가 없으면(None) (b)·(c)는 미충족이다.
"""

from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping
from dataclasses import replace

from collect import VerdictRecord

TrafficIndex = Mapping[str, str]  # traffic id → 그 요청 대상의 resource_ref(type:id)
Correlate = Callable[[VerdictRecord], str | None]


def has_evidence_refs(record: VerdictRecord) -> bool:
    """(a) traffic_evidence_refs가 비어 있지 않다."""
    return bool(record.traffic_evidence_refs)


def ref_resolves(ref: str, traffic: TrafficIndex | None) -> bool:
    """(b) ref가 같은 회차 traffic 기록에 실재한다. 인덱스가 없으면 확인 불가 = 아님."""
    return traffic is not None and ref in traffic


def ref_targets_challenge(
    ref: str,
    record: VerdictRecord,
    challenge_key: str,
    traffic: TrafficIndex | None,
    correlate: Correlate,
) -> bool:
    """(c) ref 요청의 대상이 해당 챌린지 자원이다.

    레코드와 같은 상관(correlate)을 그 요청 대상에 적용한다 — 레코드의 vuln_class는 그대로,
    resource_ref만 traffic 요청 대상으로 바꿔서. 상관 기준을 따로 만들지 않는다.
    """
    if not ref_resolves(ref, traffic):
        return False
    return correlate(replace(record, resource_ref=traffic[ref])) == challenge_key


def record_has_evidence(
    record: VerdictRecord, challenge_key: str, traffic: TrafficIndex | None, correlate: Correlate
) -> bool:
    """레코드 증거 충족 = (a) 그리고 (b)·(c)를 함께 만족하는 ref가 하나 이상."""
    # (a)는 'ref ≥ 1'에 이미 함의되지만, 프로토콜 조건을 코드에서 그대로 읽히게 둔다.
    return has_evidence_refs(record) and any(
        ref_targets_challenge(ref, record, challenge_key, traffic, correlate)
        for ref in record.traffic_evidence_refs
    )


def challenge_has_evidence(
    records: Iterable[VerdictRecord], challenge_key: str, traffic: TrafficIndex | None, correlate: Correlate
) -> bool:
    """챌린지 증거 충족 = confirmed 레코드 중 증거 충족 레코드가 하나 이상."""
    return any(record_has_evidence(r, challenge_key, traffic, correlate) for r in records)


def dangling_refs(records: Iterable[VerdictRecord], traffic: TrafficIndex | None) -> tuple[str, ...]:
    """traffic 인덱스에 없는 ref(끊긴 증거 포인터) — 경고용, 정렬.

    인덱스 자체가 없으면 끊겼는지 알 수 없어 빈 값이다(인덱스 부재는 따로 알린다).
    """
    if traffic is None:
        return ()
    return tuple(sorted({ref for r in records for ref in r.traffic_evidence_refs if ref not in traffic}))
