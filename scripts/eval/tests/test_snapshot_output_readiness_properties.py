"""스냅샷 수집 전 저장 경계의 조합 property 회귀.

실제 네트워크는 사용하지 않고 메모리 HTTP 응답만 주입한다. 이 파일은
``_output_ready``의 입력 행렬과 공개 ``collect_snapshot``의 사전검증 순서,
그리고 검증 실패 때의 파일 보존을 독립적으로 확인한다.
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import snapshot_collector
from ground_truth import bac_category, core_gt
from snapshot_collector import SnapshotCollectionError, SnapshotEndpoint, collect_snapshot
from snapshot_writer import SnapshotContext


_LIMIT = 1024 * 1024
_KEYS = tuple(sorted({challenge.key for challenge in core_gt() + bac_category()}))
_RAW = json.dumps({"data": [{"key": key, "solved": False} for key in _KEYS]},
                  separators=(",", ":")).encode("utf-8")
_CONFIG = SnapshotEndpoint("http://127.0.0.1:18080", "/instrumentation/state", 1.0, _LIMIT)
_CONTEXT = SnapshotContext("run-합성", "model-正常", "reset-1", "instance-1", "execution-1")
_MARKER = "SYNTHETIC-PRIVATE-MARKER"
_DEFAULT_ROOT = object()


class _MemoryBody:
    def __init__(self, body: bytes):
        self.body = body

    def read(self, _maximum: int = -1) -> bytes:
        return self.body if _maximum < 0 else self.body[:_maximum]


class _MemoryResponse:
    status = 200

    def __init__(self, body: bytes = _RAW):
        self.fp = _MemoryBody(body)

    def getheader(self, name: str, default=None):
        return {"Content-Type": "application/json", "Content-Length": str(len(_RAW))}.get(
            name, default
        )

    def close(self) -> None:
        pass

    def __enter__(self):
        return self

    def __exit__(self, *_exc):
        self.close()


class _MemoryConnection:
    constructed = 0
    requests: list[tuple[str, str]] = []

    def __init__(self, *_args, **_kwargs):
        type(self).constructed += 1

    def request(self, method: str, path: str, **_kwargs) -> None:
        type(self).requests.append((method, path))

    def getresponse(self) -> _MemoryResponse:
        return _MemoryResponse()

    def close(self) -> None:
        pass


class _PrivatePath:
    def __init__(self, value: str):
        self.value = value

    def __fspath__(self) -> str:
        return self.value


def _reset_memory_http() -> None:
    _MemoryConnection.constructed = 0
    _MemoryConnection.requests = []


def _filesystem_state(root: Path) -> dict[str, tuple[str, object]]:
    """Capture entries without following symlinks, including bytes of files."""
    if not root.exists() and not root.is_symlink():
        return {}
    state: dict[str, tuple[str, object]] = {}

    def visit(directory: Path, prefix: str = "") -> None:
        for entry in os.scandir(directory):
            relative = f"{prefix}{entry.name}"
            if entry.is_symlink():
                state[relative] = ("symlink", os.readlink(entry.path))
            elif entry.is_dir(follow_symlinks=False):
                state[relative] = ("dir", None)
                visit(Path(entry.path), relative + "/")
            else:
                state[relative] = ("file", Path(entry.path).read_bytes())

    if root.is_dir() and not root.is_symlink():
        visit(root)
    else:
        state["."] = ("symlink" if root.is_symlink() else "file",
                       os.readlink(root) if root.is_symlink() else root.read_bytes())
    return state


class TestSnapshotOutputReadinessProperties(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        _reset_memory_http()
        self.socket_guard = patch("socket.socket", side_effect=AssertionError("raw socket forbidden"))
        self.dns_guard = patch("socket.getaddrinfo", side_effect=AssertionError("DNS forbidden"))
        self.process_guard = patch("subprocess.Popen", side_effect=AssertionError("process forbidden"))
        self.connection_guard = patch("socket.create_connection",
                                      side_effect=AssertionError("socket connection forbidden"))
        self.system_guard = patch("os.system", side_effect=AssertionError("os.system forbidden"))
        for guard in (self.socket_guard, self.dns_guard, self.process_guard,
                      self.connection_guard, self.system_guard):
            guard.start()
            self.addCleanup(guard.stop)

    def _collect(self, *, root=_DEFAULT_ROOT, phase="pre", context=_CONTEXT):
        selected_root = self.root if root is _DEFAULT_ROOT else root
        return collect_snapshot(_CONFIG, selected_root, phase, context)

    def _assert_zero_io_failure(self, *, root, phase="pre", context=_CONTEXT,
                                message: str):
        _reset_memory_http()
        before = _filesystem_state(self.root)
        with patch.object(snapshot_collector.http.client, "HTTPConnection",
                          side_effect=_MemoryConnection), \
                patch.object(snapshot_collector, "write_snapshot") as writer:
            caught = None
            try:
                self._collect(root=root, phase=phase, context=context)
            except SnapshotCollectionError as error:
                caught = error
            self.assertEqual(_MemoryConnection.constructed, 0)
            self.assertEqual(_MemoryConnection.requests, [])
            writer.assert_not_called()
            self.assertIsNotNone(caught)
            self.assertEqual(str(caught), message)
            self.assertNotIn(_MARKER, str(caught))
        self.assertEqual(_filesystem_state(self.root), before)

    def test_valid_control_uses_one_get_and_real_writer_after_direct_readiness_success(self):
        context = SnapshotContext("  run-합성  ", "model-正常", "reset-1", "instance-1", "execution-1")
        snapshot_collector._output_ready(self.root, "pre", context)
        with patch.object(snapshot_collector.http.client, "HTTPConnection", side_effect=_MemoryConnection):
            result = self._collect(context=context)
        self.assertEqual(_MemoryConnection.constructed, 1)
        self.assertEqual(_MemoryConnection.requests, [("GET", _CONFIG.path)])
        self.assertEqual((self.root / result.artifacts.raw["path"]).read_bytes(), _RAW)
        self.assertEqual(result.artifacts.raw["run_id"], context.run_id)
        self.assertEqual(result.artifacts.normalized["model_tag"], context.model_tag)
        self.assertEqual(result.artifacts.challenge_count, len(_KEYS))
        self.assertLessEqual(result.requested_at, result.received_at)

    def test_context_identity_matrix_has_zero_io_and_preserves_accepted_unicode(self):
        invalid = (None, True, 7, "", " ", "\u3000", "\ud800")
        fields = tuple(SnapshotContext.__dataclass_fields__)
        for field in fields:
            for value in invalid:
                with self.subTest(field=field, value=repr(value)):
                    context = _CONTEXT.__class__(**{name: (value if name == field else _MARKER)
                                                   for name in fields})
                    expected = "UTF-8 회차 신원 필요" if value == "\ud800" else (
                        "회차 신원 문자열 부정" if not isinstance(value, str) or not value.strip()
                        else "UTF-8 회차 신원 필요")
                    self._assert_zero_io_failure(root=self.root, context=context, message=expected)
        accepted = ("  주변 공백  ", "유니코드 정상 문자열", "x\u2003y")
        for field in fields:
            for value in accepted:
                with self.subTest(accepted_field=field, value=repr(value)):
                    context = _CONTEXT.__class__(**{name: (value if name == field else "정상 값")
                                                   for name in fields})
                    snapshot_collector._output_ready(self.root, "pre", context)

    def test_phase_and_root_type_matrix_has_zero_io(self):
        cases = (
            (None, "pre/post와 SnapshotContext 필요"),
            (True, "pre/post와 SnapshotContext 필요"),
            ("", "pre/post와 SnapshotContext 필요"),
            ("other", "pre/post와 SnapshotContext 필요"),
            ("pre ", "pre/post와 SnapshotContext 필요"),
        )
        for phase, message in cases:
            with self.subTest(phase=repr(phase)):
                self._assert_zero_io_failure(root=self.root, phase=phase, message=message)
        for root in (None, 7, object()):
            with self.subTest(root=repr(root)):
                self._assert_zero_io_failure(root=root, message="저장 디렉터리 경로 필요")
        for context in (None, object()):
            with self.subTest(context=repr(context)):
                self._assert_zero_io_failure(root=self.root, context=context,
                                             message="pre/post와 SnapshotContext 필요")
        missing = self.root / "missing"
        self._assert_zero_io_failure(root=missing, message="저장 경로 사전 검증 실패")
        regular = self.root / "regular"
        regular.write_bytes(b"keep")
        self._assert_zero_io_failure(root=regular, message="저장 경로 사전 검증 실패")
        pathlike = _PrivatePath(str(self.root))
        snapshot_collector._output_ready(pathlike, "post", _CONTEXT)

    def test_root_symlink_matrix_rejects_normal_and_dangling_without_mutation(self):
        target = self.root / "target"
        target.mkdir()
        normal = self.root / "normal-link"
        normal.symlink_to(target, target_is_directory=True)
        dangling = self.root / "dangling-link"
        dangling.symlink_to(self.root / "gone", target_is_directory=True)
        for link in (normal, dangling):
            with self.subTest(link=link.name):
                self._assert_zero_io_failure(root=link, message="저장 경로 사전 검증 실패")

    def test_existing_phase_file_directory_and_symlink_reject_without_mutation(self):
        for kind in ("file", "dir", "symlink-file", "symlink-dir", "symlink-dangling"):
            for phase_name in ("pre", "post"):
                root = self.root / f"{kind}-{phase_name}"
                root.mkdir()
                phase = root / phase_name
                if kind == "file":
                    phase.write_bytes(b"previous bytes")
                elif kind == "dir":
                    phase.mkdir()
                    (phase / "keep").write_bytes(b"previous bytes")
                elif kind == "symlink-file":
                    target = root / "phase-target"
                    target.write_bytes(b"previous bytes")
                    phase.symlink_to(target)
                elif kind == "symlink-dir":
                    target = root / "phase-target"
                    target.mkdir()
                    (target / "keep").write_bytes(b"previous bytes")
                    phase.symlink_to(target, target_is_directory=True)
                else:
                    phase.symlink_to(root / "missing")
                with self.subTest(kind=kind, phase=phase_name):
                    self._assert_zero_io_failure(root=root, phase=phase_name,
                                                 message="이미 존재하는 단계: 송신하지 않음")

    def test_accepted_identity_strings_use_real_collect_and_writer_for_all_path_forms(self):
        fields = tuple(SnapshotContext.__dataclass_fields__)
        accepted = ("  주변 공백  ", "유니코드 정상 문자열", "x\u2003y")
        for field in fields:
            for value_index, value in enumerate(accepted):
                context = replace(_CONTEXT, **{field: value})
                for phase in ("pre", "post"):
                    for root_kind in ("str", "Path", "PathLike"):
                        actual = self.root / f"accepted-{field}-{value_index}-{phase}-{root_kind}"
                        actual.mkdir()
                        root = {"str": str(actual), "Path": actual,
                                "PathLike": _PrivatePath(str(actual))}[root_kind]
                        with self.subTest(field=field, value=repr(value), phase=phase,
                                          root_kind=root_kind):
                            _reset_memory_http()
                            with patch.object(snapshot_collector.http.client, "HTTPConnection",
                                              side_effect=_MemoryConnection):
                                result = self._collect(root=root, phase=phase, context=context)
                            self.assertEqual(_MemoryConnection.constructed, 1)
                            self.assertEqual(_MemoryConnection.requests, [("GET", _CONFIG.path)])
                            base = actual / phase
                            self.assertEqual((base / "raw.json").read_bytes(), _RAW)
                            normalized = json.loads((base / "normalized.json").read_bytes())
                            self.assertEqual(normalized, dict(vars(context), phase=phase,
                                                              solved={key: False for key in _KEYS}))
                            for name in fields:
                                self.assertEqual(normalized[name], getattr(context, name))
                                self.assertEqual(result.artifacts.raw[name], getattr(context, name))
                                self.assertEqual(result.artifacts.normalized[name], getattr(context, name))
                            self.assertEqual(result.artifacts.raw["phase"], phase)
                            self.assertEqual(result.artifacts.normalized["phase"], phase)

    def test_mutation_bypassing_output_ready_is_caught_by_the_same_zero_io_oracle(self):
        missing = self.root / "missing"
        self._assert_zero_io_failure(root=missing, message="저장 경로 사전 검증 실패")
        _reset_memory_http()
        with patch.object(snapshot_collector, "_output_ready", return_value=None):
            with patch.object(snapshot_collector.http.client, "HTTPConnection",
                              side_effect=_MemoryConnection), \
                    patch.object(snapshot_collector, "write_snapshot"):
                self._collect(root=missing)
        self.assertEqual(_MemoryConnection.constructed, 1)
        self.assertEqual(_MemoryConnection.requests, [("GET", _CONFIG.path)])
        with patch.object(snapshot_collector, "_output_ready", return_value=None):
            with self.assertRaises(AssertionError):
                self._assert_zero_io_failure(root=missing, message="저장 경로 사전 검증 실패")

    def test_mutation_missing_identity_validation_is_caught_by_zero_io_oracle(self):
        invalid_context = SnapshotContext("", "model", "reset", "instance", "execution")
        self._assert_zero_io_failure(root=self.root, context=invalid_context,
                                     message="회차 신원 문자열 부정")

        original_ready = snapshot_collector._output_ready

        def weak_ready(root, phase, context):
            original_ready(root, phase, replace(context, run_id="repaired"))

        _reset_memory_http()
        with patch.object(snapshot_collector, "_output_ready", side_effect=weak_ready):
            with patch.object(snapshot_collector.http.client, "HTTPConnection",
                              side_effect=_MemoryConnection), \
                    patch.object(snapshot_collector, "write_snapshot"):
                self._collect(root=self.root, context=invalid_context)
        self.assertEqual(_MemoryConnection.constructed, 1)
        self.assertEqual(_MemoryConnection.requests, [("GET", _CONFIG.path)])
        with patch.object(snapshot_collector, "_output_ready", side_effect=weak_ready):
            with self.assertRaises(AssertionError):
                self._assert_zero_io_failure(root=self.root, context=invalid_context,
                                             message="회차 신원 문자열 부정")
        self.assertEqual(_MemoryConnection.constructed, 1)
        self.assertEqual(_MemoryConnection.requests, [("GET", _CONFIG.path)])


if __name__ == "__main__":
    unittest.main()
