"""manifest — 회차 메타데이터 로드·검증 테스트.

합성 manifest를 임시 디렉터리에만 쓴다. 실제 회차 결과물은 쓰지 않는다(절대규칙 2).

실행: scripts/eval 에서  python3 -I -m unittest discover -s tests
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from manifest import Engine, ManifestError, load_manifests  # noqa: E402


def _run(**overrides: object) -> dict:
    run: dict = {
        "condition": "우리@test",
        "engine": "ours",
        "run_id": "run-1",
        "model_tag": "test-model",
        "reset_generation": "gen-1",
        "repeat_index": 0,
        "records_path": "run0/records.json",
        "snapshot_path": "run0/snapshot.json",
        "pre_snapshot_path": "run0/pre.json",
        "run_record_path": "run0/runner.json",
    }
    run.update(overrides)
    return run


class _ManifestDir(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)

    def write(self, payload: object) -> str:
        path = os.path.join(self._tmp.name, "manifest.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps(payload))
        return path


class TestLoad(_ManifestDir):
    def test_array_and_runs_envelope(self) -> None:
        for payload in ([_run()], {"runs": [_run()]}):
            runs = load_manifests(self.write(payload))
            self.assertEqual(len(runs), 1)
            self.assertIs(runs[0].engine, Engine.OURS)

    def test_relative_paths_resolve_against_manifest_dir(self) -> None:
        run = load_manifests(self.write([_run()]))[0]
        self.assertEqual(run.records_path, os.path.join(self._tmp.name, "run0", "records.json"))

    def test_absolute_path_kept(self) -> None:
        absolute = os.path.join(self._tmp.name, "elsewhere", "snapshot.json")
        run = load_manifests(self.write([_run(snapshot_path=absolute)]))[0]
        self.assertEqual(run.snapshot_path, absolute)

    def test_unmeasured_values_are_none(self) -> None:
        # 러너가 못 준 측정값은 None — 표에 "—"로 나가고 0으로 지어내지 않는다.
        for run in (_run(), _run(elapsed_seconds=None, total_requests=None, blocked_attempts=None)):
            loaded = load_manifests(self.write([run]))[0]
            self.assertIsNone(loaded.elapsed_seconds)
            self.assertIsNone(loaded.total_requests)
            self.assertIsNone(loaded.blocked_attempts)

    def test_measured_values_kept_separately(self) -> None:
        loaded = load_manifests(self.write([_run(elapsed_seconds=120, total_requests=345, blocked_attempts=12)]))[0]
        self.assertEqual(loaded.elapsed_seconds, 120.0)
        self.assertEqual((loaded.total_requests, loaded.blocked_attempts), (345, 12))


class TestReject(_ManifestDir):
    def assertRejected(self, payload: object, pattern: str) -> None:
        with self.assertRaisesRegex(ManifestError, pattern):
            load_manifests(self.write(payload))

    def test_empty_or_wrong_shape(self) -> None:
        self.assertRejected([], "비어")
        self.assertRejected({"runs": [_run()], "extra": 1}, "runs")
        self.assertRejected("문자열", "회차 배열")

    def test_unknown_field(self) -> None:
        self.assertRejected([_run(name="View Basket")], "모르는 필드")

    def test_missing_required(self) -> None:
        run = _run()
        del run["model_tag"]
        self.assertRejected([run], "model_tag")

    def test_missing_run_id_rejected(self) -> None:
        run = _run()
        del run["run_id"]
        self.assertRejected([run], "run_id")

    def test_unknown_engine_label(self) -> None:
        self.assertRejected([_run(engine="원본")], "engine")

    def test_repeat_index_bool_or_negative(self) -> None:
        self.assertRejected([_run(repeat_index=True)], "repeat_index")
        self.assertRejected([_run(repeat_index=-1)], "repeat_index")

    def test_bad_measurements(self) -> None:
        self.assertRejected([_run(elapsed_seconds=-1)], "elapsed_seconds")
        self.assertRejected([_run(elapsed_seconds=True)], "elapsed_seconds")
        self.assertRejected([_run(total_requests=1.5)], "total_requests")
        self.assertRejected([_run(blocked_attempts=-1)], "blocked_attempts")
        self.assertRejected([_run(blocked_attempts=True)], "blocked_attempts")

    def test_duplicate_model_tag_json_key_rejected(self) -> None:
        path = os.path.join(self._tmp.name, "manifest.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps([_run()]).replace(
                '"model_tag": "test-model"',
                '"model_tag": "old-model", "model_tag": "test-model"',
            ))
        with self.assertRaisesRegex(ManifestError, "key 중복"):
            load_manifests(path)

    def test_nan_elapsed_rejected(self) -> None:
        path = os.path.join(self._tmp.name, "manifest.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps([_run()]).replace('"repeat_index": 0', '"repeat_index": 0, "elapsed_seconds": NaN'))
        with self.assertRaisesRegex(ManifestError, "elapsed_seconds"):
            load_manifests(path)

    def test_errors_aggregated(self) -> None:
        with self.assertRaises(ManifestError) as ctx:
            load_manifests(self.write([_run(engine="x"), _run(repeat_index=-1)]))
        self.assertIn("2건", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()
