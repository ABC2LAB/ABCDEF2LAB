"""score — 저장 raw 채점기 end-to-end 테스트(T1·T2·T3①·T5~T9).

회차 디렉터리(판정 레코드·정규화 스냅샷)·manifest·GT 상관 데이터를 전부 합성으로 임시
디렉터리에 만든다. 실제 스캔 출력·스냅샷은 쓰지 않는다(절대규칙 2). GT key는 평가 안에서만
쓴다(절대규칙 7).

실행: scripts/eval 에서  python3 -I -m unittest discover -s tests
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from aggregate import count_basis  # noqa: E402
from collect import ScanOutputError, ScanParseResult  # noqa: E402
from correlation import load_correlation  # noqa: E402
from ground_truth import Axis, bac_category, core_gt  # noqa: E402
from manifest import Engine, load_manifests  # noqa: E402
from score import (  # noqa: E402
    BaselineNotImplementedError,
    ComparabilityError,
    assemble_repeat,
    check_comparable,
    main,
    score_repeat,
)
from scoreboard import SnapshotError  # noqa: E402

# 채점 분모 전체(코어 ∪ BAC). 합성 스냅샷은 기본으로 이 key를 전부 미해결로 싣는다.
_SCORED_KEYS = sorted({c.key for c in core_gt() + bac_category()})

# 합성 GT 상관: type 접두부 → 챌린지. 나머지 코어 챌린지는 type 없음(빈 배열).
_TYPES = {"basketAccessChallenge": ["rest:basket"], "forgedFeedbackChallenge": ["rest:feedback"]}


def _record(oracle_value: str, resource_ref: str, **overrides: object) -> dict:
    """계약 §2 모양의 최소 합성 레코드."""
    rec: dict = {
        "oracle_value": oracle_value,
        "vuln_verdict": None if oracle_value == "allowed" else oracle_value,
        "vuln_class": "idor",
        "resource_ref": resource_ref,
        "model_tag": "test-model",
    }
    rec.update(overrides)
    return rec


class _RunDir(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.correlation_path = self._write_correlation()
        self.correlate = load_correlation(self.correlation_path).correlate

    def _path(self, *parts: str) -> str:
        return os.path.join(self._tmp.name, *parts)

    def _write_correlation(self) -> str:
        axis = {Axis.HORIZONTAL: "horizontal", Axis.VERTICAL: "vertical"}
        rules = [
            {"challenge": c.key, "axis": axis[c.axis], "vuln_class": None, "types": _TYPES.get(c.key, [])}
            for c in core_gt()
        ]
        path = self._path("gt_correlation_test.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump({"version": 1, "rules": rules}, f)
        return path

    def make_run(
        self,
        name: str,
        records: list[dict],
        solved: dict[str, bool] | None = None,
        drop_keys: tuple[str, ...] = (),
        **overrides: object,
    ) -> dict:
        """회차 디렉터리를 만들고 manifest 회차 항목(상대경로)을 돌려준다."""
        os.makedirs(self._path(name))
        with open(self._path(name, "records.json"), "w", encoding="utf-8") as f:
            json.dump(records, f)
        snapshot = {k: False for k in _SCORED_KEYS} | (solved or {})
        for key in drop_keys:
            del snapshot[key]
        with open(self._path(name, "snapshot.json"), "w", encoding="utf-8") as f:
            json.dump(snapshot, f)
        run: dict = {
            "condition": "우리@test",
            "engine": "ours",
            "model_tag": "test-model",
            "reset_generation": f"gen-{name}",
            "repeat_index": 0,
            "records_path": f"{name}/records.json",
            "snapshot_path": f"{name}/snapshot.json",
        }
        run.update(overrides)
        return run

    def write_manifest(self, runs: list[dict]) -> str:
        path = self._path("manifest.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump({"runs": runs}, f)
        return path

    def load(self, runs: list[dict]) -> list:
        return load_manifests(self.write_manifest(runs))

    def run_main(self, runs: list[dict]) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = main([self.write_manifest(runs), "--correlation", self.correlation_path])
        return code, out.getvalue(), err.getvalue()


class TestTitleMapping(_RunDir):
    """T1 — 제목(이름) 매핑만으로 확정 금지(§4-1)."""

    def test_title_field_in_record_rejected(self) -> None:
        # 레코드가 제목을 실어 오면 계약 밖 필드로 막힌다 — 제목으로 상관할 길이 없다.
        run = self.make_run("r0", [_record("confirmed", "rest:basket:1", name="View Basket")])
        with self.assertRaises(ScanOutputError):
            score_repeat(self.load([run])[0], self.correlate)
        code, out, _ = self.run_main([run])
        self.assertEqual((code, out), (2, ""))

    def test_confirmed_without_solve_is_false_positive(self) -> None:
        run = self.make_run("r0", [_record("confirmed", "rest:basket:1")])
        counts = count_basis(score_repeat(self.load([run])[0], self.correlate), core_gt())
        self.assertEqual((counts.confirmed, counts.false_positive), (0, 1))


class TestOutOfList(_RunDir):
    """T2 — GT에 매핑 안 된 finding은 오탐이 아니라 out-of-list(§4-3)."""

    def test_unmapped_finding_counted_separately(self) -> None:
        run = self.make_run("r0", [_record("confirmed", "rest:unknownThing:7")])
        for basis in (core_gt(), bac_category()):
            counts = count_basis(score_repeat(self.load([run])[0], self.correlate), basis)
            self.assertEqual(counts.out_of_list, 1)
            self.assertEqual(counts.false_positive, 0)


class TestDenominator(_RunDir):
    """T3① — GT 밖 미해결 챌린지는 어느 칸에도 안 들어간다(분모 = GT, §4-4)."""

    def test_non_gt_unsolved_not_counted(self) -> None:
        extra = {f"notInGt{i}Challenge": False for i in range(93)}
        run = self.make_run("r0", [], solved=extra)
        repeat = score_repeat(self.load([run])[0], self.correlate)
        for basis, size in ((core_gt(), 9), (bac_category(), 10)):
            c = count_basis(repeat, basis)
            self.assertEqual(c.confirmed + c.false_positive + c.missed + c.undecided, size)


class TestEvidenceAndCoverage(_RunDir):
    """T3②·T4 end-to-end — 증거·커버리지가 없으면 확정·미탐을 주지 않는다."""

    _SOLID = [_record("confirmed", "rest:basket:1", traffic_evidence_refs=["t-cross"])]
    _TRAFFIC = {"t-cross": "rest:basket:1"}

    def _core(self, run: dict, **kwargs: object):
        return count_basis(score_repeat(self.load([run])[0], self.correlate, **kwargs), core_gt())

    def test_t4_cli_path_confirmed_solved_is_undecided(self) -> None:
        # traffic 인덱스 없음(지금 CLI 경로) → 증거 (b)(c) 확인 불가 → 확정 아님.
        run = self.make_run("r0", self._SOLID, solved={"basketAccessChallenge": True})
        c = self._core(run)
        self.assertEqual((c.confirmed, c.false_positive), (0, 0))
        code, out, _ = self.run_main([run])
        self.assertEqual(code, 0)
        self.assertIn("| 우리@test | — | — | 0 (n=1) | 0 (n=1) | 0 (n=1) | 9 (n=1) |", out)

    def test_confirmed_only_with_traffic_index_fixture(self) -> None:
        # 확정 칸은 traffic 인덱스가 붙어야(지금은 fixture에서만) 나온다.
        run = self.make_run("r0", self._SOLID, solved={"basketAccessChallenge": True})
        self.assertEqual(self._core(run, traffic=self._TRAFFIC).confirmed, 1)

    def test_t4_records_without_solid_ref_stay_undecided_even_with_index(self) -> None:
        noisy = [
            _record("confirmed", "rest:basket:1"),
            _record("confirmed", "rest:basket:2", traffic_evidence_refs=["t-gone", "t-login"]),
        ]
        run = self.make_run("r0", noisy, solved={"basketAccessChallenge": True})
        c = self._core(run, traffic={"t-login": "rest:user:login"})
        self.assertEqual((c.confirmed, c.undecided), (0, 9))

    def test_t3_no_finding_unsolved_is_undecided_not_missed(self) -> None:
        c = self._core(self.make_run("r0", []))
        self.assertEqual((c.missed, c.undecided), (0, 9))

    def test_solve_without_finding_is_missed(self) -> None:
        c = self._core(self.make_run("r0", [], solved={"forgedReviewChallenge": True}))
        self.assertEqual((c.missed, c.undecided), (1, 8))

    def test_coverage_fixture_turns_unsolved_no_finding_into_missed(self) -> None:
        c = self._core(self.make_run("r0", []), covered=frozenset({"adminSectionChallenge", "feedbackChallenge"}))
        self.assertEqual((c.missed, c.undecided), (2, 7))


class TestSameScript(_RunDir):
    """T5 — 정규화 뒤 채점은 엔진을 안 본다. baseline은 0점 대신 에러."""

    def test_assembly_ignores_engine_label(self) -> None:
        run = self.make_run("r0", [])
        ours = self.load([run])[0]
        base = self.load([dict(run, engine="baseline")])[0]
        self.assertIs(base.engine, Engine.BASELINE)
        parsed = ScanParseResult(verdicts={}, out_of_list=("rest:x:1",), conflicts=())
        solved = {k: False for k in _SCORED_KEYS}
        args = (parsed, solved, {"basketAccessChallenge": True}, frozenset({"feedbackChallenge"}))
        self.assertEqual(assemble_repeat(ours, *args), assemble_repeat(base, *args))

    def test_baseline_is_error_not_zero(self) -> None:
        run = self.make_run("r0", [], engine="baseline", condition="원본@test")
        with self.assertRaisesRegex(BaselineNotImplementedError, "A5"):
            score_repeat(self.load([run])[0], self.correlate)

    def test_cli_stops_without_table_on_baseline(self) -> None:
        runs = [
            self.make_run("r0", []),
            self.make_run("r1", [], engine="baseline", condition="원본@test"),
        ]
        code, out, err = self.run_main(runs)
        self.assertEqual(code, 2)
        self.assertEqual(out, "")  # 우리 행만 있는 표나 baseline 0점 표를 내지 않는다
        self.assertIn("미구현(A5 보류)", err)


class TestComparable(_RunDir):
    """T6·T7 — 통제변수 단일 태그(§6), 회차마다 리셋(§4-5)."""

    def test_two_model_tags_rejected(self) -> None:
        runs = [self.make_run("r0", []), self.make_run("r1", [], repeat_index=1, model_tag="other-model")]
        with self.assertRaisesRegex(ComparabilityError, "모델 태그"):
            check_comparable(self.load(runs))
        self.assertEqual(self.run_main(runs)[:2], (2, ""))

    def test_shared_reset_generation_rejected(self) -> None:
        runs = [self.make_run("r0", []), self.make_run("r1", [], repeat_index=1, reset_generation="gen-r0")]
        with self.assertRaisesRegex(ComparabilityError, "reset 세대"):
            check_comparable(self.load(runs))

    def test_duplicate_repeat_and_mixed_engine_rejected(self) -> None:
        dup = [self.make_run("r0", []), self.make_run("r1", [])]  # 둘 다 우리@test#0
        with self.assertRaisesRegex(ComparabilityError, "회차 중복"):
            check_comparable(self.load(dup))
        mixed = [self.make_run("r2", []), self.make_run("r3", [], repeat_index=1, engine="baseline")]
        with self.assertRaisesRegex(ComparabilityError, "엔진이 섞임"):
            check_comparable(self.load(mixed))

    def test_valid_repeats_pass(self) -> None:
        check_comparable(self.load([self.make_run("r0", []), self.make_run("r1", [], repeat_index=1)]))


class TestSnapshotGuard(_RunDir):
    """T8 — 스냅샷에 GT key가 빠지면 미해결로 넘기지 않고 멈춘다(§2)."""

    def test_missing_gt_key_stops_scoring(self) -> None:
        run = self.make_run("r0", [], drop_keys=("basketAccessChallenge",))
        with self.assertRaisesRegex(SnapshotError, "basketAccessChallenge"):
            score_repeat(self.load([run])[0], self.correlate)
        self.assertEqual(self.run_main([run])[:2], (2, ""))


class TestReport(_RunDir):
    """T9 — 같은 입력이면 같은 출력. 미측정은 "—"."""

    def _runs(self) -> list[dict]:
        return [
            self.make_run("r0", [_record("suspected", "rest:feedback:3")]),
            self.make_run("r1", [_record("confirmed", "rest:basket:1")], repeat_index=1),
            self.make_run("r2", [], condition="우리@measured", elapsed_seconds=60, total_requests=10),
        ]

    def test_byte_identical_output(self) -> None:
        runs = self._runs()
        first, second = self.run_main(runs), self.run_main(runs)
        self.assertEqual(first[0], 0)
        self.assertEqual(first, second)

    def test_unmeasured_renders_dash_and_rows_keep_manifest_order(self) -> None:
        _, out, _ = self.run_main(self._runs())
        self.assertIn("| 우리@test | — | — |", out)
        self.assertIn("| 우리@measured | 60 (n=1) | 10 (n=1) |", out)
        self.assertLess(out.index("우리@test"), out.index("우리@measured"))
        self.assertIn("코어 GT(인가 9) 기준", out)
        self.assertIn("BAC category(10) 기준", out)


if __name__ == "__main__":
    unittest.main()
