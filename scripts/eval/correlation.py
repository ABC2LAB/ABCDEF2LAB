"""평가 전용 GT 상관 — 판정 레코드(계약 #16 모양) → GT 챌린지 키.

parse_scan_output에 주입하는 correlate를 만든다(`load_correlation().correlate`). 매핑
규칙은 코드가 아니라 gt_correlation.json(평가 전용 GT 데이터)에서 읽는다 — 엔진
seam·자원 명명이 바뀌어도 데이터만 고친다. 대상 앱 엔드포인트를 코드에 박지 않는다.

이 모듈과 데이터는 **평가에만** 쓴다(CLAUDE.md 절대규칙 7). 엔진·탐색·판정 경로에서
import하거나 읽지 않는다 — tests가 레포 전체에서 참조를 검사한다.

매칭 키(민준·경준 임시확정, 동찬 리뷰 대기): resource_ref = `type:id`(콜론)에서 **type
접두부(마지막 콜론으로 split)** + vuln_class. 구체 id엔 안 건다 — id는 한 reset 세대에서만
유효한 관측 인스턴스(sameResource 비교 전용)이고, type은 재실행해도 안정인 정규 자원종류다.
제목·이름 매핑으로 성공 처리하지 않는다(프로토콜 §4.1) — 해결 여부는 aggregate.classify가 solved로.
"""

from __future__ import annotations

import json
import os
from collections.abc import Mapping
from dataclasses import dataclass

from ground_truth import Axis, core_gt

DEFAULT_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "gt_correlation.json")

# 계약 §2(a) axis 값 ↔ ground_truth.Axis. 규칙 axis가 GT 축과 어긋나면 데이터 오류.
# axis는 로드 시 데이터 정합 검사용 — 런타임 매칭 키는 type+vuln_class(위 확정)뿐이다.
_CONTRACT_AXIS = {"horizontal": Axis.HORIZONTAL, "vertical": Axis.VERTICAL}

_TOP_LEVEL_KEYS = frozenset({"version", "target", "source", "todo", "rules"})
_RULE_KEYS = frozenset({"challenge", "axis", "vuln_class", "types"})


class CorrelationError(ValueError):
    """GT 상관 데이터 오류 또는 모호한 매칭. 조용히 넘기지 않는다(절대규칙 5)."""


@dataclass(frozen=True)
class Rule:
    challenge: str
    axis: str
    vuln_class: str | None
    types: tuple[str, ...]


@dataclass(frozen=True)
class CorrelationTable:
    rules: tuple[Rule, ...]

    @property
    def unmapped(self) -> tuple[str, ...]:
        """type이 하나도 없는 챌린지 — 상관이 안 돼 항상 미탐 측으로 간다. 러너가 경고로 띄운다."""
        return tuple(r.challenge for r in self.rules if not r.types)

    def correlate(self, record: object) -> str | None:
        """레코드 → 챌린지 키. 매칭 없음 = None(out-of-list). 둘 이상 매칭 = 에러(GT 모호).

        record는 계약 필드를 가진 dict 또는 속성 객체(collect.VerdictRecord 등) — 특정
        타입에 묶지 않아 파서와 독립으로 쓴다. resource_ref의 type 접두부 + vuln_class로만 건다.
        """
        vuln_class = _field(record, "vuln_class")
        resource_ref = _field(record, "resource_ref")
        if not isinstance(resource_ref, str):
            return None
        rtype = _type_of(resource_ref)
        if rtype is None:
            return None
        hits = sorted(r.challenge for r in self.rules if _rule_matches(r, rtype, vuln_class))
        if len(hits) > 1:
            raise CorrelationError(f"type {rtype!r}가 여러 챌린지에 매칭: {hits} — GT 데이터 모호")
        return hits[0] if hits else None


def load_correlation(path: str = DEFAULT_PATH) -> CorrelationTable:
    """GT 상관 데이터를 읽고 검증한다. 코어 GT 9종을 정확히 한 번씩 담아야 한다."""
    with open(path, encoding="utf-8") as f:
        try:
            payload = json.load(f)
        except json.JSONDecodeError as e:
            raise CorrelationError(f"GT 상관 JSON 파싱 실패: {e}") from e
    if not isinstance(payload, dict) or not isinstance(payload.get("rules"), list):
        raise CorrelationError("최상위가 객체가 아니거나 rules 배열이 없음")

    errors = [f"최상위에 모르는 키: '{k}'" for k in payload if k not in _TOP_LEVEL_KEYS]
    core = {c.key: c for c in core_gt()}
    rules: list[Rule] = []
    seen: set[str] = set()
    all_types: dict[str, str] = {}  # type → 그 type을 쓴 챌린지(로드 시 중복 type 검출)
    for i, raw in enumerate(payload["rules"]):
        rule, rule_errors = _parse_rule(i, raw, core)
        errors.extend(rule_errors)
        if rule is None:
            continue
        if rule.challenge in seen:
            errors.append(f"[{i}] 챌린지 중복: '{rule.challenge}'")
            continue
        seen.add(rule.challenge)
        for tp in rule.types:
            if tp in all_types:
                errors.append(f"[{i}] type '{tp}'를 '{all_types[tp]}'와 '{rule.challenge}'가 공유 — 모호")
            else:
                all_types[tp] = rule.challenge
        rules.append(rule)

    missing = sorted(set(core) - seen)
    if missing:
        errors.append(f"코어 GT 챌린지 누락: {missing} — type이 없어도 빈 배열로 명시")
    if errors:
        raise CorrelationError(f"GT 상관 데이터 오류 {len(errors)}건:\n  " + "\n  ".join(errors))
    return CorrelationTable(tuple(rules))


def _parse_rule(index: int, raw: object, core: Mapping) -> tuple[Rule | None, list[str]]:
    if not isinstance(raw, dict):
        return None, [f"[{index}] 규칙이 객체가 아님: {type(raw).__name__}"]
    errors = [f"[{index}] 규칙에 모르는 필드: '{k}'" for k in raw if k not in _RULE_KEYS]

    challenge = raw.get("challenge")
    if challenge not in core:
        errors.append(f"[{index}] 코어 GT에 없는 챌린지: {challenge!r}")

    axis = raw.get("axis")
    if axis not in _CONTRACT_AXIS:
        errors.append(f"[{index}] axis 부정: {axis!r} (허용: {sorted(_CONTRACT_AXIS)})")
    elif challenge in core and _CONTRACT_AXIS[axis] is not core[challenge].axis:
        errors.append(f"[{index}] '{challenge}' axis {axis!r}가 GT 축 {core[challenge].axis.name}과 다름")

    vuln_class = raw.get("vuln_class")
    if vuln_class is not None and (not isinstance(vuln_class, str) or not vuln_class):
        errors.append(f"[{index}] vuln_class는 null 또는 비지 않은 문자열: {vuln_class!r}")

    raw_types = raw.get("types")
    types: list[str] = []
    if not isinstance(raw_types, list):
        errors.append(f"[{index}] types 배열 아님: {raw_types!r}")
    else:
        for tp in raw_types:
            if not isinstance(tp, str) or not tp:
                errors.append(f"[{index}] 잘못된 type: {tp!r} (비지 않은 문자열이어야)")
            else:
                types.append(tp)

    if errors:
        return None, errors
    return Rule(challenge=challenge, axis=axis, vuln_class=vuln_class, types=tuple(types)), []


def _field(record: object, name: str) -> object:
    if isinstance(record, Mapping):
        return record.get(name)
    return getattr(record, name, None)


def _type_of(resource_ref: str) -> str | None:
    """resource_ref 'type:id'에서 type 접두부 — 마지막 콜론 앞 전부.

    id(마지막 콜론 뒤)엔 안 건다. 콜론이 없거나 type이 비면 None(= 상관 불가, out-of-list).
    type 자체는 콜론을 품을 수 있다(예 'rest:basket:42' → type 'rest:basket').
    """
    head, sep, _ = resource_ref.rpartition(":")
    if not sep or not head:
        return None
    return head


def _rule_matches(rule: Rule, rtype: str, vuln_class: object) -> bool:
    # 매칭 키는 type + vuln_class뿐(민준·경준 확정). axis는 로드 검사용이라 여기선 안 본다.
    if rtype not in rule.types:
        return False
    if rule.vuln_class is not None and vuln_class != rule.vuln_class:
        return False
    return True
