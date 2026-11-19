"""E③ 실제 검증 시나리오 집계(Go 조건 1). 시나리오 목록(scenarios.json)의 기대 상태·사유와 회차 결과
(e3-confirmation.json)를 맞대고, 거짓 CONFIRMED 수를 센다.

결과 배치: <results>/<시나리오 ID>/e3-confirmation.json (제품 회차의 runs/<job>/e3-confirmation.json을 시나리오 ID 폴더로
복사한 것, 또는 가짜 v3 사전 실행 E3_REHEARSAL_OUT 출력).

거짓 CONFIRMED: 기대가 confirmed가 아닌 종류의 confirmed 판정, 또는 같은 회차의 confirmed 판정이 가리키지 않는 confirmed
후보(E③ 계약 §9). 결과 파일이 없거나 읽지 못하면 그 시나리오는 미수집이고 통과로 세지 않는다.

모드:
  fake  목록에서 fake=true인 시나리오를 모두 비교한다.
  real  real=runnable은 비교하고, real=fake_only는 건너뛰며, real=blocked_*는 결과가 있으면 거짓 CONFIRMED만 센다.

종료 코드: 0 모두 통과, 1 거짓 CONFIRMED 1건 이상, 2 기대 불일치·미수집·목록 오류.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

MANIFEST_VERSION = "e3-acceptance-scenarios/v1"
RESULT_VERSION = "e3-confirmation-run/v1"
TALLY_FORMAT = "e3-acceptance-tally/v1"
RESULT_FILE = "e3-confirmation.json"
# 결과 파일 상한(바이트). 판정 몇 개·참조·digest뿐이다.
MAX_RESULT_BYTES = 1 << 20
REAL_MODES = {"runnable", "fake_only"}
CONFIRMED = "confirmed"

EXIT_OK, EXIT_FALSE_CONFIRMED, EXIT_MISMATCH = 0, 1, 2


class TallyError(Exception):
    """목록이 계약 모양이 아님."""


def _no_duplicates(pairs):
    out = {}
    for k, v in pairs:
        if k in out:
            raise ValueError(f"중복 키 {k}")
        out[k] = v
    return out


def _load_json(path: Path, limit: int):
    raw = path.read_bytes()
    if len(raw) > limit:
        raise ValueError("크기 상한 초과")
    return json.loads(raw, object_pairs_hook=_no_duplicates)


def load_manifest(path: Path) -> dict:
    try:
        m = _load_json(path, MAX_RESULT_BYTES)
    except (OSError, ValueError) as exc:
        raise TallyError(f"시나리오 목록을 읽지 못함: {exc}") from exc
    if not isinstance(m, dict) or m.get("version") != MANIFEST_VERSION or not isinstance(m.get("scenarios"), list):
        raise TallyError("시나리오 목록 형식")
    ids = set()
    for sc in m["scenarios"]:
        if not isinstance(sc, dict) or not isinstance(sc.get("id"), str) or not sc["id"]:
            raise TallyError("시나리오 ID")
        if sc["id"] in ids:
            raise TallyError(f"시나리오 ID 중복: {sc['id']}")
        ids.add(sc["id"])
        real = sc.get("real")
        if not isinstance(real, str) or not (real in REAL_MODES or real.startswith("blocked_")):
            raise TallyError(f"{sc['id']}: real 값")
        exp = sc.get("expect")
        if not isinstance(exp, dict) or not isinstance(exp.get("status"), str) or not isinstance(exp.get("reason"), str) \
                or not isinstance(exp.get("verdicts"), dict):
            raise TallyError(f"{sc['id']}: expect 모양")
    return m


def load_result(path: Path):
    """결과 하나를 읽는다. 못 읽으면 (None, 사유)."""
    try:
        r = _load_json(path, MAX_RESULT_BYTES)
    except FileNotFoundError:
        return None, "missing"
    except (OSError, ValueError):
        return None, "unreadable"
    if not isinstance(r, dict) or r.get("version") != RESULT_VERSION or not isinstance(r.get("status"), str) \
            or not isinstance(r.get("verdicts"), list) or not isinstance(r.get("candidates", []), list) \
            or not isinstance(r.get("requests", 0), int):
        return None, "invalid"
    return r, ""


def false_confirmed(result: dict, expected: dict) -> list[str]:
    """거짓 CONFIRMED 목록. 기대가 confirmed가 아닌 종류의 확정 판정과, 확정 판정이 가리키지 않는 확정 후보."""
    out = []
    confirmed_refs = set()
    for v in result.get("verdicts", []):
        if not isinstance(v, dict) or v.get("oracle_value") != CONFIRMED:
            continue
        typ = v.get("resource_type")
        want = expected.get(typ) if isinstance(typ, str) else None
        if not isinstance(want, dict) or want.get("value") != CONFIRMED:
            out.append(f"verdict:{typ}")
        for ref in v.get("candidate_refs") or []:
            confirmed_refs.add(ref)
    for c in result.get("candidates", []):
        if isinstance(c, dict) and c.get("level") == CONFIRMED and c.get("candidate_ref") not in confirmed_refs:
            out.append(f"candidate:{c.get('candidate_ref')}")
    return out


def compare(result: dict, expect: dict) -> list[str]:
    """기대와 다른 점. 상태·사유는 정확히, 판정은 종류별 값과 사유 포함으로 본다."""
    problems = []
    if result.get("status") != expect["status"]:
        problems.append(f"status {result.get('status')} != {expect['status']}")
    if result.get("reason", "") != expect["reason"]:
        problems.append(f"reason {result.get('reason', '')} != {expect['reason']}")
    if expect.get("requests_zero") and result.get("requests", 0) != 0:
        problems.append(f"requests {result.get('requests')} != 0")
    got = {v.get("resource_type"): v for v in result.get("verdicts", []) if isinstance(v, dict)}
    for typ, want in expect["verdicts"].items():
        v = got.get(typ)
        if v is None:
            problems.append(f"verdict {typ} 없음")
            continue
        if v.get("oracle_value") != want.get("value"):
            problems.append(f"verdict {typ} {v.get('oracle_value')} != {want.get('value')}")
        reasons = set(v.get("product_reason_codes") or []) | set(v.get("oracle_reason_codes") or [])
        for r in want.get("reasons") or []:
            if r not in reasons:
                problems.append(f"verdict {typ} 사유 {r} 없음")
    if not expect["verdicts"] and got:
        problems.append("기대하지 않은 판정 행")
    return problems


def tally(manifest: dict, results: Path, mode: str) -> dict:
    rows, totals = [], {"pass": 0, "fail": 0, "missing": 0, "blocked": 0, "skipped": 0, "false_confirmed": 0}
    for sc in manifest["scenarios"]:
        row = {"id": sc["id"], "name": sc.get("name", "")}
        real = sc["real"]
        if mode == "fake" and not sc.get("fake"):
            row["outcome"] = "skipped"
        elif mode == "real" and real == "fake_only":
            row["outcome"] = "skipped"
        else:
            result, why = load_result(results / sc["id"] / RESULT_FILE)
            blocked = mode == "real" and real.startswith("blocked_")
            if result is None:
                row["outcome"] = "blocked" if blocked else "missing"
                row["problems"] = [why]
            else:
                row["actual"] = {"status": result.get("status"), "reason": result.get("reason", ""),
                                 "requests": result.get("requests", 0),
                                 "confirmed": sorted(v.get("resource_type") for v in result["verdicts"]
                                                     if isinstance(v, dict) and v.get("oracle_value") == CONFIRMED)}
                fc = false_confirmed(result, sc["expect"]["verdicts"])
                row["false_confirmed"] = fc
                totals["false_confirmed"] += len(fc)
                if blocked:
                    row["outcome"] = "blocked"
                else:
                    problems = compare(result, sc["expect"])
                    row["problems"] = problems
                    row["outcome"] = "fail" if problems or fc else "pass"
        totals[row["outcome"]] += 1
        rows.append(row)
    return {"format": TALLY_FORMAT, "mode": mode, "scenarios": rows, "totals": totals}


def exit_code(summary: dict) -> int:
    t = summary["totals"]
    if t["false_confirmed"]:
        return EXIT_FALSE_CONFIRMED
    if t["fail"] or t["missing"]:
        return EXIT_MISMATCH
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--manifest", required=True, type=Path)
    ap.add_argument("--results", required=True, type=Path)
    ap.add_argument("--mode", required=True, choices=["fake", "real"])
    args = ap.parse_args(argv)
    try:
        manifest = load_manifest(args.manifest)
    except TallyError as exc:
        print(f"목록 오류: {exc}", file=sys.stderr)
        return EXIT_MISMATCH
    summary = tally(manifest, args.results, args.mode)
    json.dump(summary, sys.stdout, ensure_ascii=False, indent=2)
    print()
    return exit_code(summary)


if __name__ == "__main__":
    sys.exit(main())
