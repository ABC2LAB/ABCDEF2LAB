"""합성 v1 기록의 산출물 문맥·경로·역할·바이트 결속 생성형 회귀.

시험용 재서명은 내부 거부 지점에 도달하기 위한 준비다. 운영 키의 신뢰성이나
실제 러너 격리, 임의의 파일 교체 경쟁에 대한 안전성을 증명하지 않는다.
"""

from __future__ import annotations

import contextlib
import copy
import hashlib
import io
import itertools
import json
import os
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import run_record
from manifest import load_manifests
from run_fixture import KEYS, RunFixture
from run_record import RunInputs, RunRecordError, load_run_inputs

KEY = b"artifact-properties-fixed-key-0123456789"
CHALLENGE = "basketAccessChallenge"
ROLES = ("pre_raw", "pre", "post_raw", "post", "records", "traffic", "coverage", "solve_events")
CONTEXT = ("run_id", "model_tag", "reset_generation", "instance_id", "execution_id")
MANIFEST_PATH = {"pre": "pre_snapshot_path", "post": "snapshot_path", "records": "records_path"}
PHASES = {"pre_raw": "pre", "pre": "pre", "post_raw": "post", "post": "post",
          "records": "engine", "traffic": "engine", "coverage": "engine", "solve_events": "engine"}


class ArtifactProperties(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        guards = contextlib.ExitStack()
        self.addCleanup(guards.close)
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system"):
            guards.enter_context(patch(target, side_effect=AssertionError(target)))
        self.fixture = RunFixture(self.tmp.name, KEY)
        self.make("case")

    def make(self, name, *, solved=True):
        self.name, self.solved = name, solved
        self.run = self.fixture.make(name, [], solved={CHALLENGE: solved})
        self.fixture.evidence(self.run, traffic={"request": "rest:basket:1"}, covered=set())
        self.folder = self.root / name
        self.original = self.fixture.receipt(self.run)
        self.write_manifest()

    def write_manifest(self):
        path = self.root / "manifest.json"
        path.write_text(json.dumps({"runs": [self.run]}), encoding="utf-8")
        self.manifest = load_manifests(str(path))[0]

    def descriptor(self, role, **changes):
        receipt = copy.deepcopy(self.original)
        receipt["artifacts"][role].update(changes)
        self.fixture._write_receipt(self.run, receipt)

    def artifact(self, role):
        return self.folder / self.original["artifacts"][role]["path"]

    def tree(self):
        result = {}
        for current, dirs, files in os.walk(self.root, followlinks=False):
            for name in dirs + files:
                path = Path(current) / name
                relative = str(path.relative_to(self.root))
                mode = path.lstat().st_mode
                if path.is_symlink():
                    result[relative] = ("link", mode, os.readlink(path))
                elif path.is_dir():
                    result[relative] = ("dir", mode)
                else:
                    result[relative] = ("file", mode, path.read_bytes())
        return result

    def read(self):
        before = self.tree()
        run_before, manifest_before = copy.deepcopy(self.run), copy.deepcopy(self.manifest)
        out, err = io.StringIO(), io.StringIO()
        try:
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                return load_run_inputs(self.manifest, KEY, lambda _: None)
        finally:
            self.assertEqual(self.tree(), before)
            self.assertEqual(self.run, run_before)
            self.assertEqual(self.manifest, manifest_before)
            self.assertEqual((out.getvalue(), err.getvalue()), ("", ""))

    def success(self):
        pre = dict.fromkeys(KEYS, False)
        post = dict(pre, **{CHALLENGE: self.solved})
        expected = RunInputs(
            pre, post, {"run_id": "run-" + self.name, "model_tag": "test-model",
                        "reset_generation": "gen-" + self.name, "records": []},
            {"request": "rest:basket:1"}, frozenset(),
            frozenset({CHALLENGE}) if self.solved else frozenset(), "execution-" + self.name,
            hashlib.sha256(b"fixture-v1").hexdigest(), hashlib.sha256(b"environment-v1").hexdigest())
        actual = self.read()
        self.assertEqual(actual, expected)
        for states, expected_states in ((actual.pre, pre), (actual.post, post)):
            for key in KEYS:
                self.assertIs(states[key], expected_states[key])
        return actual

    def reject(self, message):
        with self.assertRaisesRegex(RunRecordError, re.escape(message)):
            self.read()

    def test_all_role_context_and_phase_bindings(self):
        self.success()
        for role, field, value in itertools.product(ROLES, CONTEXT, ("other-context", None)):
            with self.subTest(role=role, field=field, value=value):
                self.descriptor(role, **{field: value})
                self.reject(f"{role}: {field} 불일치/부재")
        for role in ROLES:
            for phase in ("pre", "post", "engine"):
                if phase == PHASES[role]:
                    continue
                with self.subTest(role=role, phase=phase):
                    self.descriptor(role, phase=phase)
                    self.reject(f"{role}: 단계 불일치")

    def test_unresigned_same_size_and_growing_bytes_hit_distinct_guards(self):
        self.success()
        for role, grow in itertools.product(ROLES, (False, True)):
            with self.subTest(role=role, grow=grow):
                path = self.artifact(role)
                original = path.read_bytes()
                changed = original + b" " if grow else original[:-1] + b"!"
                path.write_bytes(changed)
                self.reject(f"{role}: 파일 {'크기' if grow else '해시'} 불일치")
                path.write_bytes(original)

    def test_resigned_size_and_hash_descriptor_boundaries(self):
        self.success()
        for role in ROLES:
            descriptor = self.original["artifacts"][role]
            for size in (-1, True, float(descriptor["size"]), descriptor["size"] + 1):
                with self.subTest(role=role, size=size):
                    self.descriptor(role, size=size)
                    self.reject(f"{role}: 파일 크기 불일치")
            for digest in ("", "0" * 64, descriptor["sha256"].upper(), None):
                with self.subTest(role=role, digest=digest):
                    self.descriptor(role, sha256=digest)
                    self.reject(f"{role}: 파일 해시 불일치")

    def test_role_path_escape_duplicate_and_manifest_binding(self):
        self.success()
        for role, path in itertools.product(ROLES, ("../outside.json", "/absolute.json", "nested/../x.json")):
            with self.subTest(role=role, path=path):
                self.descriptor(role, path=path)
                self.reject(f"{role}: 경로 이탈/중복")
        self.descriptor("post_raw", path=self.original["artifacts"]["pre_raw"]["path"])
        self.reject("post_raw: 경로 이탈/중복")
        for role in MANIFEST_PATH:
            with self.subTest(role=role, binding="manifest"):
                copied = self.folder / ("copy-" + role + ".json")
                copied.write_bytes(self.artifact(role).read_bytes())
                self.descriptor(role, path=copied.name)
                self.reject(f"{role}: manifest 경로 불일치")

    def test_literal_names_and_nested_paths_preserve_complete_inputs(self):
        for role, name in itertools.product(ROLES, ("literal [x] ü.json", "literal%2Fname.json", "nested/./value.json")):
            with self.subTest(role=role, name=name):
                original = self.artifact(role)
                moved = self.folder / name
                moved.parent.mkdir(exist_ok=True)
                original.rename(moved)
                self.descriptor(role, path=name)
                old_run = dict(self.run)
                if role in MANIFEST_PATH:
                    self.run[MANIFEST_PATH[role]] = str(moved.relative_to(self.root))
                    self.write_manifest()
                self.success()
                moved.rename(original)
                self.run = old_run
                self.write_manifest()
                self.fixture._write_receipt(self.run, self.original)

    def test_final_and_middle_symlink_reads_are_rejected(self):
        self.success()
        raw_roles = ("pre_raw", "post_raw", "traffic", "coverage", "solve_events")
        for role, middle in itertools.product(raw_roles, (False, True)):
            with self.subTest(role=role, middle=middle):
                original = self.artifact(role)
                real_dir = self.folder / ("real-" + role)
                real_dir.mkdir(exist_ok=True)
                moved = real_dir / original.name
                original.rename(moved)
                alias = self.folder / ("alias-" + role)
                if middle:
                    alias.symlink_to(real_dir, target_is_directory=True)
                    self.descriptor(role, path=alias.name + "/" + original.name)
                else:
                    original.symlink_to(moved)
                    self.fixture._write_receipt(self.run, self.original)
                self.reject("회차 아티팩트 경로/파일 읽기 실패")
                (alias if middle else original).unlink()
                moved.rename(original)
                self.fixture._write_receipt(self.run, self.original)

    def test_symlinked_run_directory_root_is_accepted(self):
        self.success()
        alias = self.root / "root-alias"
        alias.symlink_to(self.folder, target_is_directory=True)
        for field in ("run_record_path", "records_path", "pre_snapshot_path", "snapshot_path"):
            self.run[field] = str(Path("root-alias") / Path(self.run[field]).name)
        self.write_manifest()
        self.success()

    def test_inode_weakening_mutant_is_rejected_by_same_oracle(self):
        self.make("same", solved=False)
        self.success()
        source, target = self.artifact("pre_raw"), self.artifact("post_raw")
        self.assertEqual(source.read_bytes(), target.read_bytes())
        target.unlink()
        os.link(source, target)
        self.reject("아티팩트 파일 경로/역할 중복")
        original_read = run_record._RunDirectory.read
        ids = {}

        def unique_ids(directory, relative):
            data, _ = original_read(directory, relative)
            ids.setdefault(relative, (0, len(ids) + 1))
            return data, ids[relative]

        with patch("run_record._RunDirectory.read", unique_ids):
            self.success()
            with self.assertRaises(AssertionError):
                self.reject("아티팩트 파일 경로/역할 중복")

    def test_context_weakening_mutant_is_rejected_by_same_oracle(self):
        self.success()
        self.descriptor("post", run_id="other-context")
        self.reject("post: run_id 불일치/부재")
        original_context = run_record._context

        def skip_post_descriptor(value, expected, label):
            if label == "post" and value.get("path") == "snapshot.json":
                return
            return original_context(value, expected, label)

        with patch("run_record._context", skip_post_descriptor):
            self.success()
            with self.assertRaises(AssertionError):
                self.reject("post: run_id 불일치/부재")


if __name__ == "__main__":
    unittest.main()
