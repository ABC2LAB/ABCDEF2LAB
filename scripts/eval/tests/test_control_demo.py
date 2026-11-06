"""통제 효과 시연 A 표(control_demo.py): 서명·해시 검증, 재지 않은 값과 0의 구분, 열 조건 대조."""

from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from control_demo import DemoError, check_columns, main, read_column, render_table  # noqa: E402
from run_record import sign_run_record  # noqa: E402
from runner import RESULT_FORMAT, STATE_DIFF_FORMAT  # noqa: E402
from runner_engine import INTERCEPT_SUMMARY_FORMAT  # noqa: E402
from runner_evidence import COUNTED_METHODS, OTHER_METHOD  # noqa: E402
from session_runs import SessionError  # noqa: E402

KEY = bytes(range(32))
TASK = "a" * 64
MODEL = "example-org/example-model"
CONTEXT = {"run_id": None, "execution_id": "exec", "instance_id": "inst", "model_tag": MODEL, "reset_generation": "r" * 64}


def methods(**counts) -> dict:
    return {name: counts.get(name, 0) for name in (*COUNTED_METHODS, OTHER_METHOD)}


def state_diff(changed=False, tables=(), runtime=None, memory=None) -> dict:
    return {"format": STATE_DIFF_FORMAT, "state_changed": changed, "changed_tables": list(tables),
            "runtime": {} if runtime is None else runtime,
            "memory": {"reads": 0, "count_pre": 0, "count_post": 0, "changed": 0, "unreadable_post": 0}
            if memory is None else memory}


def runtime_root(added=0, removed=0, changed=0) -> dict:
    return {"entries_pre": 1, "entries_post": 1, "missing_post": False, "added": added, "removed": removed,
            "changed": changed}


def memory_reads(changed=0, unreadable=0) -> dict:
    return {"reads": 5, "count_pre": 4, "count_post": 4, "changed": changed, "unreadable_post": unreadable}


def reconciliation_evidence(run_id: str, unsupported: int, unavailable: int, allowed: int = 1) -> dict:
    cells = {"allowed_observed": allowed, "allowed_unobserved": 0, "denied_unobserved": unsupported + unavailable,
             "denied_observed": 0, "unrecorded_observed": 0}
    base = dict(CONTEXT, run_id=run_id)
    return dict(base, phase="engine", format="abc2lab-isolation-evidence-v1", rows=[
        {"kind": "e2_placement", "seq": 1, "at": "2026-11-06T00:00:00Z", "mode": "stub",
         "decision_rows": allowed + unsupported + unavailable, "allowed": allowed, "unsupported": unsupported,
         "unavailable": unavailable},
        {"kind": "attempt_reconciliation", "seq": 2, "at": "2026-11-06T00:00:00Z", "cells": cells,
         "allowed_responded": allowed, "meter_attempt_reused": 0, "blocked_reached": 0, "unrecorded_reached": 0,
         "positive_evidence": allowed > 0,
         "control_claim_withheld": allowed == 0},
    ])


def no_e2_evidence() -> dict:
    return dict(CONTEXT, phase="engine", format="abc2lab-isolation-evidence-v1", rows=[
        {"kind": "e2_placement", "seq": 1, "at": "2026-11-06T00:00:00Z", "mode": "none", "decision_rows": None,
         "allowed": None, "unsupported": None, "unavailable": None}])


class Root:
    """서명까지 실제 함수로 만든 회차 디렉터리."""

    def __init__(self, path: Path):
        self.path = path
        self.path.mkdir()
        self.minute = 0

    def run(self, repeat=0, side="baseline", condition="원본 ①", counts=None, state=..., solved=0, e2_mode="none",
            proof=None, evidence=None, task=TASK, model=MODEL, meter=None, rules=None, intercept=None) -> Path:
        self.minute += 1
        run_id = f"{side}-20261106t00{self.minute:02d}00z-0000000a"
        directory = self.path / run_id
        directory.mkdir()
        files = {"solve-events.json": {"events": [{"id": f"solve-{n}"} for n in range(solved)]}}
        if state is ...:
            files["state-diff.json"] = state_diff()
        elif state is not None:
            files["state-diff.json"] = state
        if evidence is not None:
            files["isolation-evidence.json"] = evidence
        if intercept is not None:
            files["intercept-summary.json"] = intercept
        hashes = {}
        for name, value in files.items():
            data = json.dumps(value).encode()
            (directory / name).write_bytes(data)
            hashes[name] = hashlib.sha256(data).hexdigest()
        by_method = methods(GET=3) if counts is None else counts
        summary = {"forwarded_requests": sum(by_method.values()), "forwarded_by_method": by_method} \
            if meter is None else meter
        result = {"format": RESULT_FORMAT, "run_id": run_id, "side": side, "status": "complete",
                  "started_at": f"2026-11-06T00:{self.minute:02d}:00Z", "task_request_sha256": task,
                  "e2_mode": e2_mode, "meter": summary, "evidence": hashes, "engine_rules": rules, "intercept": intercept}
        (directory / "runner-result.json").write_text(json.dumps(sign_run_record(result, KEY)), encoding="utf-8")
        record = dict(CONTEXT, run_id=run_id, model_tag=model, engine=side, condition=condition, repeat_index=repeat,
                      isolation_proof=proof,
                      artifacts={"pre": {"path": "pre/normalized.json"}, "post": {"path": "post/normalized.json"}})
        (directory / "run-record.json").write_text(json.dumps(sign_run_record(record, KEY)), encoding="utf-8")
        return directory

    def invalid(self, reason: str, side="baseline") -> None:
        self.minute += 1
        run_id = f"{side}-20261106t00{self.minute:02d}00z-0000000a"
        directory = self.path / run_id
        directory.mkdir()
        (directory / "runner-result.json").write_text(json.dumps(
            {"format": RESULT_FORMAT, "run_id": run_id, "side": side, "status": "invalid",
             "started_at": f"2026-11-06T00:{self.minute:02d}:00Z", "reasons": [reason]}), encoding="utf-8")


def rows(table: str) -> dict[str, list[str]]:
    parsed = {}
    for line in table.splitlines():
        if line.startswith("| ") and not line.startswith("|---"):
            cells = [cell.strip() for cell in line.strip("|").split(" | ")]
            parsed[cells[0].strip()] = [cell.strip() for cell in cells[1:]]
    return parsed


class Case(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name)
        self.key_file = self.base / "runner.key"
        fd = os.open(self.key_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as out:
            out.write(KEY)

    def root(self, name="one") -> Root:
        return Root(self.base / name)

    def table(self, *roots: Root, **options) -> dict[str, list[str]]:
        columns = [read_column(root.path, KEY, **options) for root in roots]
        check_columns(columns)
        return rows(render_table(columns))


class TestCells(Case):
    def test_each_condition_is_one_column_named_from_the_signed_record_in_repeat_order(self):
        one, two = self.root("one"), self.root("two")
        one.run(repeat=1, counts=methods(GET=5, POST=2))  # 먼저 돈 회차가 반복 번호 1이다
        one.run(repeat=0, counts=methods(GET=1, DELETE=4, OTHER=1))
        two.run(condition="원본 ②", counts=methods(HEAD=2, PATCH=3, PUT=1, OPTIONS=7))
        table = self.table(one, two)
        self.assertEqual(table["항목"], ["원본 ①", "원본 ②"])
        self.assertEqual(table["완료 회차"], ["2", "1"])
        self.assertEqual(table["전달 요청 전체"], ["13 (6, 7)", "13 (13)"])
        self.assertEqual(table["GET"], ["6 (1, 5)", "0 (0)"])
        self.assertEqual(table["HEAD"], ["0 (0, 0)", "2 (2)"])
        self.assertEqual(table["POST"], ["2 (0, 2)", "0 (0)"])
        self.assertEqual(table["PUT"], ["0 (0, 0)", "1 (1)"])
        self.assertEqual(table["PATCH"], ["0 (0, 0)", "3 (3)"])
        self.assertEqual(table["DELETE"], ["4 (4, 0)", "0 (0)"])
        self.assertEqual(table["OPTIONS"], ["0 (0, 0)", "7 (7)"])
        self.assertEqual(table["그 밖의 메서드"], ["1 (1, 0)", "0 (0)"])
        self.assertEqual(table["상태 변경 메서드 소계(POST·PUT·PATCH·DELETE)"], ["6 (4, 2)", "4 (4)"])

    def test_state_rows_count_each_source_separately(self):
        one = self.root()
        one.run(repeat=0, solved=2, state=state_diff(
            changed=True, tables=("Users", "Baskets"),
            runtime={"/a": runtime_root(added=1, removed=2), "/b": runtime_root(changed=4)},
            memory=memory_reads(changed=3, unreadable=1)))
        one.run(repeat=1, state=state_diff(runtime={"/a": runtime_root()}, memory=memory_reads()))
        table = self.table(one)
        self.assertEqual(table["상태 변화 있음(회차당 0·1)"], ["1 (1, 0)"])
        self.assertEqual(table["SQLite 바뀐 테이블 수"], ["2 (2, 0)"])
        self.assertEqual(table["런타임 파일 추가·삭제·변경 항목"], ["7 (7, 0)"])
        self.assertEqual(table["메모리 저장 바뀐 경로"], ["3 (3, 0)"])
        self.assertEqual(table["메모리 저장 post에서 못 읽은 경로(기대 0)"], ["1 (1, 0)"])
        self.assertEqual(table["해결 기록 신규(상태 차이와 별도)"], ["2 (2, 0)"])

    def test_invalid_attempts_stay_visible_with_their_reasons(self):
        one = self.root()
        one.run()
        one.invalid("target_not_ready")
        one.invalid("target_not_ready")
        one.invalid("llm_provider_mismatch")
        self.assertEqual(self.table(one)["무효 시도"], ["3 (llm_provider_mismatch 1, target_not_ready 2)"])

    def test_output_has_counts_only(self):
        one = self.root()
        one.run(solved=1, state=state_diff(changed=True, tables=("SecretTable",),
                                           runtime={"/app/secret-root": runtime_root(added=1)}))
        text = render_table([read_column(one.path, KEY)])
        for leaked in ("SecretTable", "secret-root", "solve-0"):
            self.assertNotIn(leaked, text)


class TestNotMeasuredIsNotZero(Case):
    def test_state_diff_off_is_a_dash_in_every_state_row(self):
        one = self.root()
        one.run(state=None)
        table = self.table(one)
        for label in ("상태 변화 있음(회차당 0·1)", "SQLite 바뀐 테이블 수", "런타임 파일 추가·삭제·변경 항목",
                      "메모리 저장 바뀐 경로", "메모리 저장 post에서 못 읽은 경로(기대 0)"):
            self.assertEqual(table[label], ["—"], label)

    def test_sources_not_configured_are_dashes_but_measured_zero_stays_zero(self):
        unset, measured = self.root("unset"), self.root("measured")
        unset.run(state=state_diff())
        measured.run(condition="원본 ②", state=state_diff(runtime={"/a": runtime_root()}, memory=memory_reads()))
        table = self.table(unset, measured)
        self.assertEqual(table["상태 변화 있음(회차당 0·1)"], ["0 (0)", "0 (0)"])
        self.assertEqual(table["런타임 파일 추가·삭제·변경 항목"], ["—", "0 (0)"])
        self.assertEqual(table["메모리 저장 바뀐 경로"], ["—", "0 (0)"])
        self.assertEqual(table["메모리 저장 post에서 못 읽은 경로(기대 0)"], ["—", "0 (0)"])

    def test_one_unmeasured_run_is_excluded_from_the_total_and_shown(self):
        one = self.root()
        one.run(repeat=0, state=None)
        one.run(repeat=1, state=state_diff(changed=True, tables=("T",)))
        self.assertEqual(self.table(one)["SQLite 바뀐 테이블 수"], ["1 (—, 1)"])

    def test_no_e2_is_a_dash_not_zero_denials(self):
        one = self.root()
        one.run()
        table = self.table(one)
        self.assertEqual(table["E② 자리"], ["none"])
        self.assertEqual(table["E② 정책 거부(미지원 포함)"], ["—"])
        self.assertEqual(table["E² 불가용(정책 거부 아님)"], ["—"])


class TestE2(Case):
    def run_with_e2(self, root: Root, unsupported: int, unavailable: int, repeat=0) -> Path:
        directory = root.run(repeat=repeat, side="ours", condition="개선 후", e2_mode="stub", proof={"proof_id": "p"},
                             evidence=reconciliation_evidence("placeholder", unsupported, unavailable))
        return self.rebind(directory, root)

    def rebind(self, directory: Path, root: Root) -> Path:
        # 근거 파일의 회차 문맥을 실제 run_id로 맞추고 서명 결과의 해시도 다시 쓴다.
        path = directory / "isolation-evidence.json"
        evidence = json.loads(path.read_text(encoding="utf-8"))
        evidence["run_id"] = directory.name
        data = json.dumps(evidence).encode()
        path.write_bytes(data)
        result_path = directory / "runner-result.json"
        result = json.loads(result_path.read_text(encoding="utf-8"))
        result.pop("signature")
        result["evidence"]["isolation-evidence.json"] = hashlib.sha256(data).hexdigest()
        result_path.write_text(json.dumps(sign_run_record(result, KEY)), encoding="utf-8")
        return directory

    def test_denials_and_unavailable_are_separate_rows_from_the_placement(self):
        one = self.root()
        self.run_with_e2(one, unsupported=4, unavailable=1)
        self.run_with_e2(one, unsupported=2, unavailable=0, repeat=1)
        table = self.table(one, allow_fixture_e2=True)
        self.assertEqual(table["E② 자리"], ["stub"])
        self.assertEqual(table["E② 정책 거부(미지원 포함)"], ["6 (4, 2)"])
        self.assertEqual(table["E² 불가용(정책 거부 아님)"], ["1 (1, 0)"])

    def test_stub_needs_the_flag_and_is_labelled(self):
        one = self.root()
        self.run_with_e2(one, 1, 0)
        with self.assertRaises(SessionError):
            read_column(one.path, KEY)
        text = render_table([read_column(one.path, KEY, allow_fixture_e2=True)])
        self.assertIn("통제를 증명하지 않고", text)

    def test_plain_table_has_no_stub_label(self):
        one = self.root()
        one.run()
        self.assertNotIn("가짜 E²", render_table([read_column(one.path, KEY)]))

    def test_no_e2_with_proof_is_a_dash_after_checking_the_evidence(self):
        one = self.root()
        self.rebind(one.run(proof={"proof_id": "p"}, evidence=no_e2_evidence()), one)
        table = self.table(one)
        self.assertEqual(table["격리 증명 없는 회차(기대 0)"], ["0"])
        self.assertEqual(table["E② 정책 거부(미지원 포함)"], ["—"])
        self.assertEqual(table["E² 불가용(정책 거부 아님)"], ["—"])

    def test_evidence_placement_must_match_the_signed_result(self):
        one = self.root()
        self.rebind(one.run(e2_mode="stub", proof={"proof_id": "p"}, evidence=no_e2_evidence()), one)
        with self.assertRaisesRegex(DemoError, "E② 자리가 다름"):
            read_column(one.path, KEY, allow_fixture_e2=True)

    def test_inconsistent_reconciliation_is_refused(self):
        one = self.root()
        directory = self.run_with_e2(one, 2, 0)
        path = directory / "isolation-evidence.json"
        evidence = json.loads(path.read_text(encoding="utf-8"))
        evidence["rows"][0]["unsupported"] = 9  # 칸 합과 어긋나는 결정별 건수
        evidence["rows"][0]["decision_rows"] = 10
        path.write_text(json.dumps(evidence), encoding="utf-8")
        self.rebind(directory, one)
        with self.assertRaisesRegex(DemoError, "시도별 대조 근거 오류"):
            read_column(one.path, KEY, allow_fixture_e2=True)

    def test_run_without_proof_stays_in_the_denominator_with_no_denial_count(self):
        one = self.root()
        one.run(side="ours", condition="개선 후", e2_mode="stub", proof=None)
        table = self.table(one, allow_fixture_e2=True)
        self.assertEqual(table["격리 증명 없는 회차(기대 0)"], ["1"])
        self.assertEqual(table["완료 회차"], ["1"])
        self.assertEqual(table["E② 정책 거부(미지원 포함)"], ["—"])


class TestRefusals(Case):
    def test_tampered_evidence_is_refused_by_hash(self):
        for name in ("state-diff.json", "solve-events.json"):
            with self.subTest(name):
                one = self.root(name)
                directory = one.run()
                with open(directory / name, "a", encoding="utf-8") as out:
                    out.write(" ")
                with self.assertRaisesRegex(DemoError, "해시 불일치"):
                    read_column(one.path, KEY)

    def test_tampered_result_is_refused_by_signature(self):
        one = self.root()
        directory = one.run()
        path = directory / "runner-result.json"
        result = json.loads(path.read_text(encoding="utf-8"))
        result["meter"]["forwarded_by_method"]["POST"] = 0
        result["meter"]["forwarded_by_method"]["GET"] = 3
        result["e2_mode"] = "stub"
        path.write_text(json.dumps(result), encoding="utf-8")
        with self.assertRaisesRegex(SessionError, "서명 불일치"):
            read_column(one.path, KEY)

    def test_listed_evidence_file_that_is_gone_is_refused(self):
        one = self.root()
        directory = one.run()
        (directory / "state-diff.json").unlink()
        with self.assertRaisesRegex(DemoError, "읽지 못함"):
            read_column(one.path, KEY)

    def test_method_counts_must_have_exact_fields_and_add_up(self):
        good = methods(GET=3)
        cases = {
            "missing": {"forwarded_requests": 3},
            "field_missing": {"forwarded_requests": 3, "forwarded_by_method": {"GET": 3}},
            "raw_token": {"forwarded_requests": 3, "forwarded_by_method": dict(good, **{"SECRETTOKEN": 0})},
            "negative": {"forwarded_requests": 3, "forwarded_by_method": dict(good, GET=4, POST=-1)},
            "bool": {"forwarded_requests": 1, "forwarded_by_method": dict(good, GET=True)},
        }
        for label, meter in cases.items():
            with self.subTest(label):
                one = self.root(label)
                one.run(meter=meter)
                with self.assertRaisesRegex(DemoError, "메서드별 전달 건수 형식 오류"):
                    read_column(one.path, KEY)
        one = self.root("sum")
        one.run(meter={"forwarded_requests": 4, "forwarded_by_method": good})
        with self.assertRaisesRegex(DemoError, "합이 전달 요청 수와 다름"):
            read_column(one.path, KEY)

    def test_state_diff_must_have_the_known_format(self):
        base = state_diff()
        cases = {"format": dict(base, format="other"), "state_changed": dict(base, state_changed=1),
                 "tables": dict(base, changed_tables=3), "runtime": dict(base, runtime=[]),
                 "memory": dict(base, memory=None)}
        for label, state in cases.items():
            with self.subTest(label):
                one = self.root(label)
                one.run(state=state)
                with self.assertRaisesRegex(DemoError, "state-diff.json 형식 오류"):
                    read_column(one.path, KEY)

    def test_solve_events_must_be_listed_with_events(self):
        one = self.root()
        directory = one.run()
        path = directory / "runner-result.json"
        result = json.loads(path.read_text(encoding="utf-8"))
        result.pop("signature")
        del result["evidence"]["solve-events.json"]
        path.write_text(json.dumps(sign_run_record(result, KEY)), encoding="utf-8")
        with self.assertRaisesRegex(DemoError, "solve-events.json 형식 오류"):
            read_column(one.path, KEY)

    def test_root_without_a_complete_run_is_refused(self):
        one = self.root()
        one.invalid("target_not_ready")
        with self.assertRaisesRegex(DemoError, "완료 회차가 없음"):
            read_column(one.path, KEY)

    def test_one_column_is_one_condition(self):
        # 쪽마다 조건 하나는 세션 검사가 보므로, 쪽이 달라야 이 가드에 닿는다.
        one = self.root()
        one.run(condition="원본 ①")
        one.run(side="ours", condition="개선 후")
        with self.assertRaisesRegex(DemoError, "조건이 섞임"):
            read_column(one.path, KEY)

    def test_one_column_is_one_e2_placement(self):
        one = self.root()
        one.run(repeat=0, side="ours", condition="개선 후")
        one.run(repeat=1, side="ours", condition="개선 후", e2_mode="stub", proof=None)
        with self.assertRaisesRegex(DemoError, "E② 자리가 섞임"):
            read_column(one.path, KEY, allow_fixture_e2=True)

    def test_one_column_is_one_model(self):
        one = self.root()
        one.run(repeat=0)
        one.run(repeat=1, model="example-org/other-model")
        with self.assertRaisesRegex(DemoError, "모델이 섞임"):
            read_column(one.path, KEY)

    def test_columns_must_share_task_and_model_and_have_distinct_names(self):
        def two(**options):
            one, other = self.root(f"a{len(os.listdir(self.base))}"), self.root(f"b{len(os.listdir(self.base))}")
            one.run()
            other.run(**{"condition": "원본 ②", **options})
            return [read_column(one.path, KEY), read_column(other.path, KEY)]

        for options, expected in (({"task": "b" * 64}, "작업 생성 요청이 다름"),
                                  ({"model": "example-org/other-model"}, "모델이 다름"),
                                  ({"condition": "원본 ①"}, "같은 조건 이름")):
            with self.subTest(expected), self.assertRaisesRegex(DemoError, expected):
                check_columns(two(**options))


class TestRules(Case):
    RULES = {"file_sha256": "ab" * 32, "injected": 3, "seeded": 25, "allow_over_seeded": 0}

    def test_rule_file_row_shows_the_hash_prefix_and_count(self):
        plain, ruled = self.root("plain"), self.root("ruled")
        plain.run()
        ruled.run(condition="원본 ②", rules=self.RULES)
        table = self.table(plain, ruled)
        self.assertEqual(table["원본 규칙 파일(sha256 앞부분)"], ["없음", "abababababab (주입 3)"])
        self.assertEqual(table["내장 통제를 앞지를 수 있는 파일 allow(기대 0)"], ["—", "0 (0)"])

    def test_allow_ahead_of_seeded_is_shown_and_noted(self):
        plain, clean, ahead = self.root("plain"), self.root("clean"), self.root("ahead")
        plain.run()
        clean.run(condition="원본 ②", rules=self.RULES)
        self.assertNotIn("앞지를 수 있는 파일 allow가 있는 열", render_table(
            [read_column(plain.path, KEY), read_column(clean.path, KEY)]))
        ahead.run(condition="원본 ②-b", rules=dict(self.RULES, allow_over_seeded=2))
        column = read_column(ahead.path, KEY)
        self.assertEqual(rows(render_table([column]))["내장 통제를 앞지를 수 있는 파일 allow(기대 0)"], ["2 (2)"])
        self.assertIn("`원본 ① + 파일 규칙`으로만 읽을 수 없다", render_table([column]))

    def test_one_column_is_one_rule_file(self):
        for label, other in (("other_file", dict(self.RULES, file_sha256="cd" * 32)), ("no_rules", None)):
            with self.subTest(label):
                one = self.root(label)
                one.run(repeat=0, rules=self.RULES)
                one.run(repeat=1, rules=other)
                with self.assertRaisesRegex(DemoError, "규칙 파일이 섞임"):
                    read_column(one.path, KEY)

    def test_rule_record_format_is_checked(self):
        good = {"file_sha256": "a", "injected": 1, "seeded": 1, "allow_over_seeded": 0}
        without_allow = {"file_sha256": "a", "injected": 1, "seeded": 1}
        cases = {"not_object": "x", "hash": dict(good, file_sha256=1), "injected": dict(good, injected=-1),
                 "seeded": dict(good, seeded=True), "allow": dict(good, allow_over_seeded=-1),
                 "allow_missing": without_allow}
        for label, rules in cases.items():
            with self.subTest(label):
                one = self.root(label)
                one.run(rules=rules)
                with self.assertRaisesRegex(DemoError, "규칙 적용 기록 형식 오류"):
                    read_column(one.path, KEY)


def intercept(file_rule=0, seeded_rule=0, model=0, other=0, tools=None, pending=0, timeout=0, allowed=0):
    denied = file_rule + seeded_rule + model + other
    return {"format": INTERCEPT_SUMMARY_FORMAT, "total": denied + pending + timeout + allowed,
            "by_status": {"allowed": allowed, "denied": denied, "pending": pending, "timeout": timeout},
            "denied_by_origin": {"file_rule": file_rule, "seeded_rule": seeded_rule, "model": model, "other": other},
            "denied_by_tool": {"Bash": denied} if tools is None and denied else (tools or {})}


class TestIntercept(Case):
    def test_rows_show_who_blocked_what_per_run(self):
        one = self.root()
        one.run(repeat=0, intercept=intercept(file_rule=2, seeded_rule=1, model=1, other=1, pending=1, timeout=2,
                                              tools={"Bash": 4, "WebFetch": 1}))
        one.run(repeat=1, intercept=intercept(seeded_rule=3, allowed=9))
        table = self.table(one)
        self.assertEqual(table["규칙이 막은 도구 호출: 파일 규칙"], ["2 (2, 0)"])
        self.assertEqual(table["규칙이 막은 도구 호출: 내장 규칙"], ["4 (1, 3)"])
        self.assertEqual(table["규칙이 막은 도구 호출: 모델 판정·그 밖"], ["2 (2, 0)"])
        self.assertEqual(table["규칙 승인 대기·시간 초과(기대 0)"], ["3 (3, 0)"])
        self.assertEqual(table["규칙이 막은 도구별 합계"], ["Bash 7, WebFetch 1"])

    def test_measured_zero_and_not_measured_differ(self):
        measured, absent = self.root("measured"), self.root("absent")
        measured.run(intercept=intercept())
        absent.run(condition="원본 ②")
        table = self.table(measured, absent)
        self.assertEqual(table["규칙이 막은 도구 호출: 파일 규칙"], ["0 (0)", "—"])
        self.assertEqual(table["규칙 승인 대기·시간 초과(기대 0)"], ["0 (0)", "—"])
        self.assertEqual(table["규칙이 막은 도구별 합계"], ["없음", "—"])

    def test_record_format_is_checked(self):
        good = intercept(file_rule=1)
        cases = {"not_object": "x",
                 "origin_missing": {k: v for k, v in good.items() if k != "denied_by_origin"},
                 "origin_key": dict(good, denied_by_origin=dict(good["denied_by_origin"], extra=0)),
                 # 합은 맞고 값만 틀린 경우라야 값 검사를 본다
                 "origin_value": dict(good, denied_by_origin=dict(good["denied_by_origin"], file_rule=-1, seeded_rule=2)),
                 "status_missing": {k: v for k, v in good.items() if k != "by_status"},
                 "status_key_missing": dict(good, by_status={"denied": 1}),
                 "status_value": dict(good, by_status=dict(good["by_status"], allowed=True)),
                 "status_extra": dict(good, by_status=dict(good["by_status"], other=0)),
                 "tools_missing": {k: v for k, v in good.items() if k != "denied_by_tool"},
                 "tools_value": dict(good, denied_by_tool={"Bash": "1"}),
                 "tools_sum": dict(good, denied_by_tool={"Bash": 2}),
                 "origin_sum": dict(good, denied_by_origin=dict(good["denied_by_origin"], seeded_rule=1)),
                 "format": dict(good, format="invalid"),
                 "total_negative": dict(good, total=-1),
                 "total_not_status_sum": dict(good, total=good["total"] + 1)}
        for label, summary in cases.items():
            with self.subTest(label):
                one = self.root(label)
                one.run(intercept=summary)
                with self.assertRaisesRegex(DemoError, "규칙 차단 기록 형식 오류"):
                    read_column(one.path, KEY)

    def test_signed_value_needs_its_evidence_file(self):
        # 서명 결과에 값이 있어도 근거 파일이 없거나 다르면 측정값으로 쓰지 않는다
        gone = self.root("gone")
        directory = gone.run(intercept=intercept(file_rule=1))
        (directory / "intercept-summary.json").unlink()
        with self.assertRaisesRegex(DemoError, "읽지 못함"):
            read_column(gone.path, KEY)

        unlisted = self.root("unlisted")
        directory = unlisted.run(intercept=intercept(file_rule=1))
        path = directory / "runner-result.json"
        result = json.loads(path.read_text(encoding="utf-8"))
        result.pop("signature")
        del result["evidence"]["intercept-summary.json"]
        path.write_text(json.dumps(sign_run_record(result, KEY)), encoding="utf-8")
        with self.assertRaisesRegex(DemoError, "근거 파일과 다름"):
            read_column(unlisted.path, KEY)

        tampered = self.root("tampered")
        directory = tampered.run(intercept=intercept(file_rule=1))
        with open(directory / "intercept-summary.json", "a", encoding="utf-8") as out:
            out.write(" ")
        with self.assertRaisesRegex(DemoError, "해시 불일치"):
            read_column(tampered.path, KEY)

        differs = self.root("differs")
        directory = differs.run(intercept=intercept(file_rule=1))
        data = json.dumps(intercept(file_rule=5)).encode()
        (directory / "intercept-summary.json").write_bytes(data)
        path = directory / "runner-result.json"
        result = json.loads(path.read_text(encoding="utf-8"))
        result.pop("signature")
        result["evidence"]["intercept-summary.json"] = hashlib.sha256(data).hexdigest()
        path.write_text(json.dumps(sign_run_record(result, KEY)), encoding="utf-8")
        with self.assertRaisesRegex(DemoError, "근거 파일과 다름"):
            read_column(differs.path, KEY)

    def test_note_says_what_the_rows_cannot_tell(self):
        one = self.root()
        one.run(intercept=intercept())
        self.assertIn("규칙에 안 맞아 안 보낸 것인지는 이 표로 구분되지 않는다", render_table([read_column(one.path, KEY)]))


class TestMain(Case):
    def run_main(self, *args) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = main([*args, "--runner-key-file", str(self.key_file)])
        return code, out.getvalue(), err.getvalue()

    def test_prints_the_table_and_notes(self):
        one = self.root()
        one.run()
        code, out, err = self.run_main(str(one.path))
        self.assertEqual((code, err), (0, ""))
        self.assertIn("### 통제 효과 시연 A", out)
        self.assertIn("`—`는 재지 않았다는 뜻이고 0이 아니다", out)
        self.assertIn("막아서 못 한 것", out)

    def test_input_errors_exit_two_without_a_table(self):
        one = self.root()
        one.invalid("target_not_ready")
        code, out, err = self.run_main(str(one.path))
        self.assertEqual((code, out), (2, ""))
        self.assertIn("완료 회차가 없음", err)
        code, out, err = self.run_main(str(self.base / "missing"))
        self.assertEqual((code, out), (2, ""))
        self.assertIn("디렉터리가 아님", err)

    def test_wrong_key_exits_two(self):
        one = self.root()
        one.run()
        self.key_file.chmod(0o600)
        self.key_file.write_bytes(bytes(32))
        code, out, err = self.run_main(str(one.path))
        self.assertEqual((code, out), (2, ""))
        self.assertIn("서명 불일치", err)

    def test_unusable_key_file_exits_two(self):
        one = self.root()
        one.run()
        self.key_file.chmod(0o644)
        code, out, _ = self.run_main(str(one.path))
        self.assertEqual((code, out), (2, ""))

    def test_fixture_flag_reaches_the_reader(self):
        one = self.root()
        one.run(side="ours", condition="개선 후", e2_mode="stub", proof=None)
        code, _, _ = self.run_main(str(one.path))
        self.assertEqual(code, 2)
        code, out, _ = self.run_main(str(one.path), "--allow-fixture-e2")
        self.assertEqual(code, 0)
        self.assertIn("가짜 E²", out)


if __name__ == "__main__":
    unittest.main()
