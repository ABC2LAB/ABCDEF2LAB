"""분리 배치 O 회차의 E③ 흔적과 E② 저널 사건 수(근거 파일 e3-traces.json).

선언 없는 회차가 E③를 정말 건드리지 않았는지(개발 계획 Go 조건 2) 회차를 내리기 전에 제품 볼륨 세 곳을 읽어 센다.
  - E② Host v3 저널(`e2-host-state` 볼륨 `journals/<sha256("run-" + 초안 ID)>/events.jsonl`): E③ 목적 사건까지 다 있다.
  - 제품 쪽 E② 저널 사본(`product-state` 볼륨 `runs/<초안 ID>/state/events.jsonl`): E③ 프로그램 사건은 싣지 않는다.
  - 대상 선언 저널(`product-state` 볼륨 `state/targets/declarations/preparation.journal`): 선언·쓰기 기록(confirmation_writes)·
    리셋 확인 줄.
  - Host 실행 디렉터리(Host v3 저널 옆)의 봉인 포함 확인(`seal-checks.json`: 마감 단계·확인 수·포함 수)과 내준 근거 체크포인트
    기록(`evidence-checkpoints.json`: 체크포인트 수). 제품 #810 뒤에 생긴다. 선언 없는 회차는 근거를 묻지 않으므로 근거 기록이
    없고 봉인 확인이 0개여야 한다.
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
# 사건 시각(e2proxy Receipt.At, RFC 3339 UTC). 모양이 아니면 null로 둔다.
_AT = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})\Z")
# 시각 순서 줄 상한. 넘으면 앞부분만 두고 timeline_truncated를 참으로 둔다(수·종류는 그대로 다 센다).
MAX_TIMELINE = 4096
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
    kinds, headers, events, e3, timeline = Counter(), 0, 0, 0, []
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
            if len(timeline) < MAX_TIMELINE:
                # 시각 순서 근거: 단계·프로그램·결정·사유 어휘와 모델 쪽(의도 번호 > 0, Worker) 여부·시각만. 호출 참조·경로·값은 없다.
                at, intent = event.get("at"), event.get("intent_id")
                timeline.append({"stage": _word(event.get("stage")), "program": _word(event.get("program")),
                                 "decision": _word(event.get("decision")), "reason": _word(event.get("reason")),
                                 "model": type(intent) is int and intent > 0,
                                 "at": at if isinstance(at, str) and _AT.fullmatch(at) else None})
            if event.get("program") in E3_PROGRAMS or E3_AUDIT_FIELD in event:
                e3 += 1
    except (ValueError, RecursionError):
        return {"error": "journal_unparsable"}
    return {"headers": headers, "events": events, "e3_events": e3, "by_kind": dict(sorted(kinds.items())),
            "timeline": timeline, "timeline_truncated": events > len(timeline)}


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


_VERSION = re.compile(r"[a-z0-9][a-z0-9._/-]{0,63}\Z")


def _version(value: object) -> str:
    return value if isinstance(value, str) and _VERSION.fullmatch(value) else INVALID


def seal_checks_summary(raw: bytes | None) -> dict:
    """Host 봉인 포함 확인 기록({version, phase, head, checks: [{sequence, mac, included}]})의 판·마감 단계·확인 수·포함 수.
    체크포인트 값(MAC·head)은 옮기지 않는다."""
    if raw is None:
        return {"error": "not_found"}
    try:
        value = json.loads(raw)
    except (ValueError, RecursionError):
        return {"error": "unparsable"}
    checks = value.get("checks") if isinstance(value, dict) else None
    if not isinstance(checks, list) or not all(isinstance(item, dict) for item in checks):
        return {"error": "unparsable"}
    return {"version": _version(value.get("version")), "phase": _word(value.get("phase")), "checks": len(checks),
            "included": sum(1 for item in checks if item.get("included") is True)}


def evidence_checkpoints_summary(raw: bytes | None) -> dict:
    """Host가 내준 근거 체크포인트 기록({version, checkpoints: [...]})의 판·체크포인트 수. 값은 옮기지 않는다."""
    if raw is None:
        return {"error": "not_found"}
    try:
        value = json.loads(raw)
    except (ValueError, RecursionError):
        return {"error": "unparsable"}
    checkpoints = value.get("checkpoints") if isinstance(value, dict) else None
    if not isinstance(checkpoints, list):
        return {"error": "unparsable"}
    return {"version": _version(value.get("version")), "checkpoints": len(checkpoints)}


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
    """세 저널·결과 파일·Host 봉인 기록에서 근거 파일 내용을 만든다. 요약(`summary`)은 회차 결과에도 싣는다."""
    host = journal_counts(sources.get("e2_host_journal"))
    product = journal_counts(sources.get("e2_product_journal"))
    declarations = declaration_counts(sources.get("declarations"))
    writes = result_writes(sources.get("e3_result"))
    seal = seal_checks_summary(sources.get("host_seal_checks"))
    issued = evidence_checkpoints_summary(sources.get("host_evidence_checkpoints"))
    summary = {
        "e3_events": host.get("e3_events"),
        "product_journal_e3_events": product.get("e3_events"),
        "declaration_writes": None if "error" in declarations else declarations["by_action"].get(DECLARATION_WRITES, 0),
        "result_writes": writes,
        # 봉인 확인 수(파일이 없으면 null)와 근거 체크포인트 수(파일이 없으면 0: 근거를 내준 적 없음, 모양이 틀리면 null)
        "host_seal_phase": seal.get("phase"),
        "host_seal_checks": seal.get("checks"),
        "host_evidence_checkpoints": 0 if issued.get("error") == "not_found" else issued.get("checkpoints"),
    }
    return {"format": FORMAT, "e2_host_journal": host, "e2_product_journal": product, "declarations": declarations,
            "host_seal_checks": seal, "host_evidence_checkpoints": issued, "summary": summary}
