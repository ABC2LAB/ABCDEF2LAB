"""manifest loader의 구조·타입·경로 조합 회귀.

합성 회차만 임시 디렉터리에 쓰며, loader가 산출하는 객체를 기대값으로 재사용하지 않는다.
실제 러너·GT·provider와의 연결은 이 테스트 범위가 아니다.
"""

from __future__ import annotations

import copy
import itertools
import json
import os
import socket
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from manifest import Engine, ManifestError, RunManifest, load_manifests


_PATH_FIELDS = ("records_path", "snapshot_path", "pre_snapshot_path", "run_record_path")
_REQUIRED_FIELDS = ("condition", "run_id", "model_tag", "reset_generation", *_PATH_FIELDS)
_ABSOLUTE = "absolute"
_RELATIVE = "./nested/../relative"


def _run(index: int = 0, **overrides: object) -> dict[str, object]:
    value: dict[str, object] = {
        "condition": f"synthetic@{index}",
        "engine": "ours" if index % 2 == 0 else "baseline",
        "run_id": f"run-{index}",
        "model_tag": "synthetic-model",
        "reset_generation": f"generation-{index}",
        "repeat_index": index,
        "records_path": f"runs/{index}/records.json",
        "snapshot_path": f"runs/{index}/snapshot.json",
        "pre_snapshot_path": f"runs/{index}/pre.json",
        "run_record_path": f"runs/{index}/runner.json",
    }
    value.update(overrides)
    return value


def _object_json(pairs: list[tuple[str, object]]) -> str:
    return "{" + ",".join(json.dumps(key, ensure_ascii=False) + ":" + json.dumps(value, ensure_ascii=False)
                          for key, value in pairs) + "}"


class TestManifestProperties(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)
        self.path = self.root / "manifest.json"
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("manifest properties must remain offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def write(self, payload: object, *, raw: str | None = None) -> str:
        if raw is None:
            self.path.write_text(json.dumps(payload, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        else:
            self.path.write_text(raw, encoding="utf-8")
        return str(self.path)

    def load(self, payload: object, *, raw: str | None = None) -> list[RunManifest]:
        path = self.write(payload, raw=raw)
        before = self.path.read_bytes()
        loaded = load_manifests(path)
        self.assertEqual(self.path.read_bytes(), before)
        return loaded

    def expected_manifest(self, raw: dict[str, object]) -> RunManifest:
        base = self.root
        def path_value(name: str) -> str:
            value = str(raw[name])
            return os.path.normpath(os.path.join(str(base), value))
        return RunManifest(
            condition=str(raw["condition"]), engine=Engine.OURS if raw["engine"] == "ours" else Engine.BASELINE,
            run_id=str(raw["run_id"]), model_tag=str(raw["model_tag"]),
            reset_generation=str(raw["reset_generation"]), repeat_index=int(raw["repeat_index"]),
            records_path=path_value("records_path"), snapshot_path=path_value("snapshot_path"),
            pre_snapshot_path=path_value("pre_snapshot_path"), run_record_path=path_value("run_record_path"),
            elapsed_seconds=None if raw.get("elapsed_seconds") is None else float(raw["elapsed_seconds"]),
            total_requests=raw.get("total_requests"), blocked_attempts=raw.get("blocked_attempts"),
        )

    def assert_rejected(self, payload: object = None, pattern: str = "", *, raw: str | None = None) -> str:
        self.write(payload, raw=raw)
        before = self.path.read_bytes()
        with self.assertRaisesRegex(ManifestError, pattern) as caught:
            load_manifests(str(self.path))
        self.assertEqual(self.path.read_bytes(), before)
        return str(caught.exception)

    def test_valid_arrays_wrappers_orders_and_path_forms_preserve_declared_runs(self):
        for count, reverse_runs, wrapper, reverse_keys in itertools.product((1, 2, 4), (False, True), (False, True), (False, True)):
            raw_runs = [_run(i) for i in range(count)]
            supplied = list(reversed(raw_runs)) if reverse_runs else raw_runs
            if reverse_keys:
                run_json = [_object_json(list(reversed(tuple(run.items())))) for run in supplied]
            else:
                run_json = [json.dumps(run, ensure_ascii=False, separators=(",", ":")) for run in supplied]
            raw = ("{" + json.dumps("runs") + ":[" + ",".join(run_json) + "]}") if wrapper else "[" + ",".join(run_json) + "]"
            with self.subTest(count=count, reverse_runs=reverse_runs, wrapper=wrapper, reverse_keys=reverse_keys):
                before = copy.deepcopy(supplied)
                loaded = self.load(supplied, raw=raw)
                expected = [self.expected_manifest(run) for run in supplied]
                self.assertEqual(loaded, expected)
                self.assertEqual(supplied, before)
                self.assertEqual([item.run_id for item in loaded], [str(run["run_id"]) for run in supplied])

        for flags in itertools.product((False, True), repeat=4):
            raw = _run(7)
            expected_paths = {}
            for field, absolute in zip(_PATH_FIELDS, flags):
                leaf = field.replace("_path", "") + ".json"
                if absolute:
                    raw[field] = str(self.root / _ABSOLUTE / leaf)
                    expected_paths[field] = str(self.root / _ABSOLUTE / leaf)
                else:
                    raw[field] = f"{_RELATIVE}/{leaf}"
                    expected_paths[field] = str(self.root / "relative" / leaf)
            with self.subTest(path_flags=flags):
                loaded = self.load([raw])[0]
                for field, expected in expected_paths.items():
                    self.assertEqual(getattr(loaded, field), expected)

    def test_measurement_presence_zero_and_none_are_independent(self):
        absent = object()
        values = (absent, None, 0, 3)
        for elapsed, requests, blocked in itertools.product(values, values, values):
            raw = _run(0)
            for name, value in (("elapsed_seconds", elapsed), ("total_requests", requests), ("blocked_attempts", blocked)):
                if value is not absent:
                    raw[name] = value
            with self.subTest(elapsed=elapsed, requests=requests, blocked=blocked):
                loaded = self.load([raw])[0]
                self.assertEqual(loaded.elapsed_seconds, None if elapsed in (absent, None) else float(elapsed))
                self.assertEqual(loaded.total_requests, None if requests is absent else requests)
                self.assertEqual(loaded.blocked_attempts, None if blocked is absent else blocked)
                self.assertIsNot(loaded.total_requests, False)
        for name in ("elapsed_seconds", "total_requests", "blocked_attempts"):
            raw = _run(0)
            raw[name] = None
            loaded = self.load([raw])[0]
            self.assertIsNone(getattr(loaded, name))

    def test_invalid_measurement_types_and_repeat_values_fail_closed(self):
        invalid = {
            "repeat_index": (True, -1, 1.5, "0", None),
            "elapsed_seconds": (True, -0.1, "1", float("nan"), float("inf")),
            "total_requests": (True, -1, 1.5, "1", float("nan")),
            "blocked_attempts": (True, -1, 1.5, "1", float("inf")),
        }
        fragments = {
            "repeat_index": "repeat_index", "elapsed_seconds": "elapsed_seconds",
            "total_requests": "total_requests", "blocked_attempts": "blocked_attempts",
        }
        for field, values in invalid.items():
            for value in values:
                with self.subTest(field=field, value=repr(value)):
                    self.assert_rejected([_run(0, **{field: value})], fragments[field])

    def test_required_and_unknown_fields_are_reported_across_multiple_runs(self):
        for field in _REQUIRED_FIELDS:
            raw = _run(0)
            del raw[field]
            with self.subTest(missing=field):
                message = self.assert_rejected([raw], field)
                self.assertIn("1건", message)

        first = _run(0, engine="invalid-engine")
        second = _run(1, repeat_index=-1)
        message = self.assert_rejected([first, second], "manifest 오류 2건")
        self.assertIn("[0] engine 부정", message)
        self.assertIn("[1] repeat_index", message)

        first = _run(0)
        first.pop("model_tag")
        first["elapsed_seconds"] = -1
        second = _run(1)
        second.pop("run_id")
        second["blocked_attempts"] = True
        message = self.assert_rejected([first, second], "manifest 오류 4건")
        for fragment in ("[0] model_tag", "[0] elapsed_seconds", "[1] run_id", "[1] blocked_attempts"):
            self.assertIn(fragment, message)

        for field in ("name", "model", "ready", "proof"):
            raw = _run(0, **{field: "synthetic"})
            with self.subTest(unknown=field):
                self.assert_rejected([raw], "모르는 필드")

    def test_duplicate_json_keys_at_root_and_nested_run_are_rejected(self):
        valid = json.dumps([_run(0)], ensure_ascii=False, separators=(",", ":"))
        root_duplicate = "{" + json.dumps("runs") + ":" + valid + "," + json.dumps("runs") + ":" + valid + "}"
        self.assert_rejected(raw=root_duplicate, pattern="key 중복")

        run_pairs = list(_run(0).items())
        for key, same, replacement in (("model_tag", "synthetic-model", "synthetic-model"),
                                        ("repeat_index", 0, 1), ("records_path", "runs/0/records.json", "other.json")):
            pairs = [(name, value) for name, value in run_pairs]
            index = next(i for i, (name, _) in enumerate(pairs) if name == key)
            pairs.insert(index + 1, (key, replacement))
            raw = "[" + _object_json(pairs) + "]"
            with self.subTest(key=key, same=same == replacement):
                self.assert_rejected(raw=raw, pattern="key 중복")

        wrapper_pairs = [("runs", [_run(0)]), ("runs", [_run(1)])]
        self.assert_rejected(raw=_object_json(wrapper_pairs), pattern="key 중복")

    def test_malformed_roots_and_run_entries_do_not_become_empty_manifests(self):
        for payload in (None, True, 1, "runs", {}, {"runs": []}, {"runs": None}, {"runs": {}}, []):
            with self.subTest(payload=repr(payload)):
                self.assert_rejected(payload, "회차 배열|비어|runs")
        for value in (None, True, 1, "run", []):
            with self.subTest(entry=repr(value)):
                self.assert_rejected([value], "회차가 객체가 아님")

    def test_loader_is_read_only_and_does_not_require_artifact_paths_to_exist(self):
        payload = [_run(0, records_path="never-created/records.json", snapshot_path="never-created/post.json")]
        before = copy.deepcopy(payload)
        loaded = self.load(payload)
        self.assertEqual(payload, before)
        self.assertFalse((self.root / "never-created").exists())
        self.assertEqual(loaded[0].records_path, str(self.root / "never-created" / "records.json"))
        self.assertEqual(loaded[0].snapshot_path, str(self.root / "never-created" / "post.json"))

    def test_mutation_guards_detect_unknown_field_and_duplicate_key_bypasses(self):
        valid = [_run(0)]
        self.assertEqual(len(self.load(valid)), 1)
        self.assert_rejected([_run(0, unexpected="x")], "모르는 필드")

        import manifest
        original_unique = manifest._unique_manifest_object
        valid_pairs = list(_run(0).items())
        condition_index = next(i for i, (name, _) in enumerate(valid_pairs) if name == "condition")
        valid_pairs.insert(condition_index + 1, ("condition", "synthetic@0"))
        duplicate_raw = "[" + _object_json(valid_pairs) + "]"
        self.assert_rejected(raw=duplicate_raw, pattern="key 중복")
        with patch("manifest._unique_manifest_object", side_effect=lambda pairs: dict(pairs)):
            self.assertEqual(self.load([], raw=duplicate_raw)[0], self.expected_manifest(_run(0)))
            with self.assertRaises(AssertionError):
                self.assert_rejected(raw=duplicate_raw, pattern="key 중복")
        self.assertIs(manifest._unique_manifest_object, original_unique)

        original_parse = manifest._parse_run
        def ignore_unknown(index, raw, base):
            return original_parse(index, {key: value for key, value in raw.items() if key in manifest._FIELDS}, base)
        with patch("manifest._parse_run", side_effect=ignore_unknown):
            with self.assertRaises(AssertionError):
                self.assert_rejected([_run(0, unexpected="x")], "모르는 필드")


if __name__ == "__main__":
    unittest.main()
