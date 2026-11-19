"""분리 배치 O 회차의 E③ 흔적과 E② 저널 사건 수(근거 파일 e3-traces.json).

선언 없는 회차가 E③를 정말 건드리지 않았는지(개발 계획 Go 조건 2) 회차를 내리기 전에 제품 볼륨 세 곳을 읽어 센다.
  - E② Host v3 저널(`e2-host-state` 볼륨 `journals/<sha256("run-" + 초안 ID)>/events.jsonl`): E③ 목적 사건까지 다 있다.
  - 제품 쪽 E② 저널 사본(`product-state` 볼륨 `runs/<초안 ID>/state/events.jsonl`): E③ 프로그램 사건은 싣지 않는다.
  - 대상 선언 저널(`product-state` 볼륨 `state/targets/declarations/preparation.journal`): 선언·쓰기 기록(confirmation_writes)·
    리셋 확인 줄.
E② 저널 한 줄은 e2proxy 저널 줄(`{"Kind": "header"|"event", "Header"|"Record": …, "MAC": …}`)이고 사건은 `Record.Event`다.
원문은 메모리에서만 보고, 사건마다 (단계, 프로그램, 결정, 사유)의 어휘 값별 개수와 E③ 사건 수만 남긴다. 어휘 모양이 아닌
값은 `invalid`로 센다(값을 옮기지 않는다). 저널 검증(HMAC·체크포인트)은 하지 않는다: 이 파일은 사건 종류·수의 근거이고, 통제
대조는 v3 수집(#785)의 몫이다. 표식 등록은 저널에 따로 남지 않는다(E② 실행의 메모리 값). 비활성 회차는 e3contract.Run이
표식을 만들기 전에 끝나므로 E③ 사건 0건과 결과 파일 상태로 본다.
"""

from __future__ import annotations

import json
import re
from collections import Counter

FORMAT = "abc2lab.e3-traces/v1"
EVIDENCE_NAME = "e3-traces.json"
# E③ 프로그램 호출 종류(e2proxy.ProgramE3Identity·ProgramE3Create·ProgramE3Read = 목적 e3_identity·e3_create·e3_read).
E3_PROGRAMS = frozenset({"e3_identity", "e3_create", "e3_read"})
# E③ 감사 칸(e2proxy.E3Audit, 사건에 펼쳐 들어간다). 이 칸이 있으면 프로그램 종류와 상관없이 E③ 사건이다.
E3_AUDIT_FIELD = "e3_binding"
KIND_FIELDS = ("stage", "program", "decision", "reason")
DECLARATION_WRITES = "confirmation_writes"
_WORD = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")
ABSENT, INVALID = "-", "invalid"


def _word(value: object) -> str:
    if value in (None, ""):
        return ABSENT
    return value if isinstance(value, str) and _WORD.fullmatch(value) else INVALID


def _lines(raw: bytes):
    for line in raw.splitlines():
        if line.strip():
            value = json.loads(line)
            if not isinstance(value, dict):
                raise ValueError("not an object")
            yield value


JOURNAL_HEADER, JOURNAL_EVENT = "header", "event"


def journal_counts(raw: bytes | None) -> dict:
    """E② 저널(e2proxy 저널 줄) 머리 줄 수와 사건 수. 파일이 없으면 not_found, 줄이 저널 줄 모양이 아니면 journal_unparsable."""
    if raw is None:
        return {"error": "not_found"}
    kinds, headers, events, e3 = Counter(), 0, 0, 0
    try:
        for line in _lines(raw):
            if line.get("Kind") == JOURNAL_HEADER and isinstance(line.get("Header"), dict):
                headers += 1
                continue
            record = line.get("Record")
            event = record.get("Event") if line.get("Kind") == JOURNAL_EVENT and isinstance(record, dict) else None
            if not isinstance(event, dict):
                raise ValueError("journal line")
            events += 1
            kinds["|".join(_word(event.get(name)) for name in KIND_FIELDS)] += 1
            if event.get("program") in E3_PROGRAMS or E3_AUDIT_FIELD in event:
                e3 += 1
    except (ValueError, RecursionError):
        return {"error": "journal_unparsable"}
    return {"headers": headers, "events": events, "e3_events": e3, "by_kind": dict(sorted(kinds.items()))}


def declaration_counts(raw: bytes | None) -> dict:
    """대상 선언 보호 저널(줄마다 {sequence, previous, payload, digest}) 줄 수와 동작별 수. 저널을 만들 때 쓰는 형식 줄
    (payload에 `format`만 있고 동작이 없음, diagnosisprep)은 따로 센다. confirmation_writes 줄의 객체 수를 따로 센다."""
    if raw is None:
        return {"error": "not_found"}
    actions, records, formats, objects = Counter(), 0, 0, 0
    try:
        for record in _lines(raw):
            payload = record.get("payload")
            if not isinstance(payload, dict):
                raise ValueError("payload")
            if "action" not in payload and "format" in payload:
                formats += 1
                continue
            records += 1
            action = _word(payload.get("action"))
            actions[action] += 1
            if action == DECLARATION_WRITES:
                items = payload.get("objects")
                objects += len(items) if isinstance(items, list) else 0
    except (ValueError, RecursionError):
        return {"error": "journal_unparsable"}
    return {"format_records": formats, "records": records, "by_action": dict(sorted(actions.items())), "write_objects": objects}


def result_writes(raw: bytes | None) -> int | None:
    """E③ 결과 파일(e3contract.RunResult)의 쓰기 기록 수. 파일이 없거나 모양이 다르면 None."""
    if raw is None:
        return None
    try:
        value = json.loads(raw)
    except ValueError:
        return None
    writes = value.get("writes") if isinstance(value, dict) else None
    return len(writes) if isinstance(writes, list) else None


def traces(sources: dict) -> dict:
    """세 저널과 결과 파일에서 근거 파일 내용을 만든다. 요약(`summary`)은 회차 결과에도 싣는다."""
    host = journal_counts(sources.get("e2_host_journal"))
    product = journal_counts(sources.get("e2_product_journal"))
    declarations = declaration_counts(sources.get("declarations"))
    writes = result_writes(sources.get("e3_result"))
    summary = {
        "e3_events": host.get("e3_events"),
        "product_journal_e3_events": product.get("e3_events"),
        "declaration_writes": None if "error" in declarations else declarations["by_action"].get(DECLARATION_WRITES, 0),
        "result_writes": writes,
    }
    return {"format": FORMAT, "e2_host_journal": host, "e2_product_journal": product, "declarations": declarations,
            "summary": summary}
