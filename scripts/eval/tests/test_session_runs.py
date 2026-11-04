"""공식 회차 세션 집계(session_runs.py): 시도·무효 집계, §5.1 교대 순서·상한 경고, 완료 회차 manifest, 입력 오류 fail-closed."""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from manifest import load_manifests  # noqa: E402
from run_record import sign_run_record  # noqa: E402
from runner import RESULT_FORMAT  # noqa: E402
from session_runs import (  # noqa: E402
    RESULT_MISSING, SessionError, build_manifest, expected_order, main, read_session, tally,
)

CONDITION = {"baseline": "원본@35b", "ours": "우리@35b"}
KEY = bytes(range(32))


def write(path: Path, value: dict, signed: bool) -> None:
    """러너처럼 완료 결과·회차 기록은 서명해서, 무효 결과는 서명 없이 쓴다."""
    path.write_text(json.dumps(sign_run_record(value, KEY) if signed else value), encoding="utf-8")


class Session:
    """합성 runs_root. 회차 디렉터리 이름은 러너 run_id 형식(<쪽>-<UTC 시각>-<16진 8자리>)."""

    def __init__(self, root: Path):
        self.root = root
        self.count = 0

    def _dir(self, side: str, minute: int) -> Path:
        self.count += 1
        directory = self.root / f"{side}-20261110t10{minute:02d}00z-{self.count:08x}"
        directory.mkdir()
        return directory

    def _result(self, directory: Path, side: str, minute: int, **fields) -> None:
        result = {"format": RESULT_FORMAT, "run_id": directory.name, "side": side,
                  "started_at": f"2026-11-10T10:{minute:02d}:00+00:00", **fields}
        write(directory / "runner-result.json", result, signed=fields.get("status") == "complete")

    def complete(self, side: str, minute: int, repeat: int, condition: str | None = None,
                 task: str = "t" * 64) -> Path:
        directory = self._dir(side, minute)
        self._result(directory, side, minute, status="complete", task_request_sha256=task.replace("t", "a"))
        record = {"run_id": directory.name, "engine": side, "condition": condition or CONDITION[side],
                  "repeat_index": repeat, "model_tag": "qwen/qwen3.6-35b-a3b", "reset_generation": "r" * 64,
                  "total_requests": 400, "blocked_attempts": None, "elapsed_seconds": 1200.5,
                  "artifacts": {"pre": {"path": "pre/normalized.json"}, "post": {"path": "post/normalized.json"},
                                "records": None}}
        write(directory / "run-record.json", record, signed=True)
        return directory

    def invalid(self, side: str, minute: int, *reasons: str) -> Path:
        directory = self._dir(side, minute)
        self._result(directory, side, minute, status="invalid", reasons=list(reasons), failed_step=reasons[0])
        return directory

    def missing(self, side: str, minute: int) -> Path:
        return self._dir(side, minute)


class SessionCase(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name) / "runs"
        self.root.mkdir()
        self.s = Session(self.root)
        self.key_file = Path(temp.name) / "runner.key"
        fd = os.open(self.key_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as out:
            out.write(KEY)

    def official(self):
        """N=3, B O O B B O 전부 유효."""
        for minute, (side, repeat) in enumerate([("baseline", 0), ("ours", 0), ("ours", 1), ("baseline", 1),
                                                 ("baseline", 2), ("ours", 2)]):
            self.s.complete(side, minute * 5, repeat)


class TestOrder(unittest.TestCase):
    def test_pairs_alternate_which_side_goes_first(self):
        self.assertEqual(expected_order(3), ["B", "O", "O", "B", "B", "O"])
        self.assertEqual(expected_order(4), ["B", "O", "O", "B", "B", "O", "O", "B"])


class TestTally(SessionCase):
    def test_clean_session_has_no_warnings(self):
        self.official()
        lines, warnings = tally(read_session(self.root, KEY), 3)
        self.assertEqual(warnings, [])
        self.assertIn("| baseline | 원본@35b | 3 | 3 | 0 | 0% | — |", lines)
        self.assertIn("| ours | 우리@35b | 3 | 3 | 0 | 0% | — |", lines)
        self.assertIn("유효 순서: B O O B B O (기대 B O O B B O: 일치)", lines)

    def test_invalid_attempts_are_counted_per_side_with_their_first_reason(self):
        self.s.complete("baseline", 0, 0)
        self.s.invalid("ours", 5, "drain_timeout", "llm_unverified")
        self.s.complete("ours", 10, 0)
        self.s.missing("ours", 15)
        lines, warnings = tally(read_session(self.root, KEY), 3)
        self.assertIn(f"| ours | 우리@35b | 3 | 1 | 2 | 67% | drain_timeout 1, {RESULT_MISSING} 1 |", lines)
        self.assertIn("시도 순서(시작 시각): B✓ O✗ O✓ O✗", lines)
        # 재실행이 교대 순서를 지켰으니 유효 순서는 기대의 앞부분과 같다.
        self.assertIn("유효 순서: B O (기대 B O O B B O: 일치)", lines)
        self.assertTrue(any(w.startswith("경고[유효 부족] ours") for w in warnings))

    def test_valid_order_that_breaks_the_alternation_is_flagged(self):
        for minute, (side, repeat) in enumerate([("baseline", 0), ("ours", 0), ("baseline", 1), ("ours", 1)]):
            self.s.complete(side, minute * 5, repeat)
        lines, warnings = tally(read_session(self.root, KEY), 3)
        self.assertIn("유효 순서: B O B O (기대 B O O B B O: 불일치)", lines)
        self.assertTrue(any(w.startswith("경고[교대 순서]") for w in warnings))

    def test_more_than_two_n_attempts_on_one_side_is_flagged(self):
        self.official()
        for minute in range(4):
            self.s.invalid("baseline", 40 + minute, "seal_failed")
        _, warnings = tally(read_session(self.root, KEY), 3)
        self.assertEqual([w for w in warnings if w.startswith("경고[시도 상한]")],
                         ["경고[시도 상한] baseline: 시도 7회 > 2N(6) — 더 돌리지 말고 원인부터 고친다"])
        self.assertFalse(any("ours" in w for w in warnings))

    def test_exactly_two_n_attempts_is_not_flagged(self):
        self.official()
        for minute in range(3):
            self.s.invalid("ours", 40 + minute, "seal_failed")
        _, warnings = tally(read_session(self.root, KEY), 3)
        self.assertEqual(warnings, [])


class TestManifest(SessionCase):
    def test_only_complete_runs_in_start_order_and_the_real_loader_accepts_it(self):
        self.s.invalid("baseline", 0, "compose_up_failed")
        self.official()
        payload = build_manifest(self.root, read_session(self.root, KEY))
        self.assertEqual([r["engine"] for r in payload["runs"]], ["baseline", "ours", "ours", "baseline", "baseline", "ours"])
        first = payload["runs"][0]
        directory = (self.root / first["run_id"]).resolve()
        self.assertEqual(first["run_record_path"], str(directory / "run-record.json"))
        self.assertEqual(first["snapshot_path"], str(directory / "post/normalized.json"))
        self.assertEqual(first["pre_snapshot_path"], str(directory / "pre/normalized.json"))
        out = self.root.parent / "manifest.json"
        out.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
        loaded = load_manifests(str(out))
        self.assertEqual([(m.condition, m.repeat_index) for m in loaded][:2], [("원본@35b", 0), ("우리@35b", 0)])

    def test_main_refuses_to_overwrite_an_existing_manifest(self):
        self.official()
        out = self.root.parent / "manifest.json"
        out.write_text("keep", encoding="utf-8")
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as err:
            code = main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file), "--manifest", str(out)])
        self.assertEqual(code, 2)
        self.assertIn("이미 있음", err.getvalue())
        self.assertEqual(out.read_text(encoding="utf-8"), "keep")

    def test_main_writes_a_new_manifest(self):
        self.official()
        out = self.root.parent / "manifest.json"
        with contextlib.redirect_stdout(io.StringIO()) as printed:
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file", str(self.key_file), "--manifest", str(out)]), 0)
        self.assertEqual(len(json.loads(out.read_text(encoding="utf-8"))["runs"]), 6)
        self.assertIn("완료 회차 6개", printed.getvalue())

    def test_main_requires_at_least_three_repeats(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(main([str(self.root), "--repeats", "2", "--runner-key-file", str(self.key_file)]), 2)


class TestInputErrors(SessionCase):
    def reject(self, expected: str) -> None:
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        self.assertTrue(any(expected in e for e in caught.exception.errors), caught.exception.errors)

    def edit(self, path: Path, resign: bool = True, **changes) -> None:
        """기본은 바꾼 뒤 다시 서명한다 — 그래야 서명 검사가 아니라 노리는 가드에 걸린다."""
        data = json.loads(path.read_text(encoding="utf-8"))
        signed = "signature" in data
        data.pop("signature", None)
        data.update(changes)
        write(path, data, signed=signed and resign)

    def test_malformed_result_stops_instead_of_being_skipped(self):
        directory = self.s.complete("ours", 0, 0)
        (directory / "runner-result.json").write_text("{", encoding="utf-8")
        self.reject("runner-result.json 읽기·형식 오류")

    def test_result_must_match_its_directory_side_format_and_status(self):
        for field, value in (("run_id", "ours-20261110t100000z-ffffffff"), ("side", "baseline"),
                             ("format", "other"), ("status", "done"), ("started_at", "2026-11-10T10:00:00")):
            with self.subTest(field=field):
                for child in self.root.iterdir():
                    for f in child.iterdir():
                        f.unlink()
                    child.rmdir()
                directory = self.s.complete("ours", 0, 0)
                self.edit(directory / "runner-result.json", **{field: value})
                self.reject("runner-result.json의 형식·run_id·side·started_at·status가 맞지 않음")

    def test_invalid_result_needs_a_reason(self):
        directory = self.s.invalid("ours", 0, "seal_failed")
        self.edit(directory / "runner-result.json", reasons=[])
        self.reject("무효 결과에 사유가 없음")

    def test_complete_result_needs_a_matching_signed_record(self):
        directory = self.s.complete("ours", 0, 0)
        (directory / "run-record.json").unlink()
        self.reject("run-record.json 읽기·형식 오류")
        directory = self.s.complete("baseline", 5, 0)
        self.edit(directory / "run-record.json", engine="ours")
        self.reject("run-record.json의 run_id·engine·condition·repeat_index가 맞지 않음")

    def test_complete_record_needs_snapshot_paths(self):
        directory = self.s.complete("ours", 0, 0)
        self.edit(directory / "run-record.json", artifacts={"pre": {"path": "pre/normalized.json"}})
        self.reject("pre·post 스냅샷 경로가 없음")

    def test_one_side_cannot_mix_condition_names_or_repeat_indexes(self):
        self.s.complete("ours", 0, 0)
        self.s.complete("ours", 5, 1, condition="우리@35b-다른")
        self.s.complete("ours", 10, 1)
        with self.assertRaises(SessionError) as caught:
            read_session(self.root, KEY)
        errors = " ".join(caught.exception.errors)
        self.assertIn("조건 이름이 섞임", errors)
        self.assertIn("같은 repeat_index의 완료 회차가 둘 이상 [1]", errors)

    def test_every_complete_run_used_the_same_task_request(self):
        self.s.complete("baseline", 0, 0)
        self.s.complete("ours", 5, 0, task="b" * 64)
        self.reject("작업 생성 요청이 회차마다 다름(해시 2종)")

    def test_complete_result_needs_its_task_request_hash(self):
        directory = self.s.complete("ours", 0, 0)
        self.edit(directory / "runner-result.json", task_request_sha256=None)
        self.reject("완료 결과에 작업 요청 해시가 없음")

    def test_result_hash_changed_without_resigning_is_rejected(self):
        # 리뷰 재현: B 결과의 작업 해시만 A로 바꾸고 재서명하지 않으면, 같은 작업처럼 보여도 거부해야 한다.
        self.s.complete("baseline", 0, 0)
        tampered = self.s.complete("ours", 5, 0, task="b" * 64)
        self.edit(tampered / "runner-result.json", resign=False, task_request_sha256="a" * 64)
        self.reject("runner-result.json 서명 불일치")

    def test_record_changed_without_resigning_is_rejected(self):
        directory = self.s.complete("ours", 0, 0)
        self.edit(directory / "run-record.json", resign=False, repeat_index=1)
        self.reject("run-record.json 서명 불일치")

    def test_complete_files_need_a_signature_from_this_key(self):
        for name in ("runner-result.json", "run-record.json"):
            for label, change in (("missing", lambda d: d.pop("signature")),
                                  ("other key", lambda d: d.update(sign_run_record(
                                      {k: v for k, v in d.items() if k != "signature"}, bytes(32))))):
                with self.subTest(file=name, label=label):
                    for child in self.root.iterdir():
                        for f in child.iterdir():
                            f.unlink()
                        child.rmdir()
                    path = self.s.complete("ours", 0, 0) / name
                    data = json.loads(path.read_text(encoding="utf-8"))
                    change(data)
                    path.write_text(json.dumps(data), encoding="utf-8")
                    self.reject(f"{name} 서명 불일치")

    def test_main_needs_a_readable_runner_key(self):
        self.official()
        with contextlib.redirect_stderr(io.StringIO()) as err:
            self.assertEqual(main([str(self.root), "--repeats", "3", "--runner-key-file",
                                   str(self.root.parent / "nope.key")]), 2)
        self.assertIn("러너 키를 읽지 못함", err.getvalue())

    def test_other_directories_in_runs_root_are_errors(self):
        (self.root / "notes").mkdir()
        self.reject("notes: 러너 회차 디렉터리 이름이 아님")


if __name__ == "__main__":
    unittest.main()
