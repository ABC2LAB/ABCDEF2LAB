"""상태 차이 사본 뜨기(프로토콜 §8.2): 대상 일시정지 → 데몬 복사 → 재개, 0600 저장. docker는 가짜다."""

from __future__ import annotations

import errno
import io
import json
import os
import sqlite3
import stat
import sys
import tarfile
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_docker import BoundedResult, CommandResult, Docker, DockerError, run_docker_bounded  # noqa: E402
from runner_state import (  # noqa: E402
    StateCopyError, copy_sqlite, discard_copy, memory_fingerprints, runtime_fingerprints,
)
from state_diff import RUN_KEY_BYTES, StateDiffError, runtime_diff, snapshot  # noqa: E402

CID = "c" * 64
DB = "/app/data/target.sqlite"
MAX = 1 << 20


def sqlite_bytes(root: Path, wal: bool = False) -> dict[str, bytes]:
    path = root / "src.sqlite"
    conn = sqlite3.connect(path)
    if wal:
        conn.execute("PRAGMA journal_mode=wal")
        conn.execute("PRAGMA wal_autocheckpoint=0")
    conn.execute("CREATE TABLE T (id INTEGER PRIMARY KEY, v TEXT)")
    conn.execute("INSERT INTO T (v) VALUES ('a')")
    conn.commit()
    files = {suffix: (root / ("src.sqlite" + suffix)).read_bytes()
             for suffix in ("", "-wal") if (root / ("src.sqlite" + suffix)).exists()}
    conn.close()
    return files


class FakeDocker:
    def __init__(self, files: dict[str, bytes], unpause_fails: bool = False, failing: frozenset = frozenset()):
        self.files, self.calls, self.unpause_fails, self.failing = files, [], unpause_fails, failing

    def pause(self, cid):
        self.calls.append(("pause", cid))

    def unpause(self, cid):
        self.calls.append(("unpause", cid))
        if self.unpause_fails:
            raise DockerError("docker_unpause_failed")

    def copy_out(self, cid, path, max_bytes):
        self.calls.append(("copy", path))
        if path in self.failing:
            raise DockerError("docker_copy_failed")
        return self.files.get(path)

    def copy_archive(self, cid, path, max_bytes):
        self.calls.append(("archive", path, max_bytes))
        if path in self.failing:
            raise DockerError("docker_copy_failed")
        return self.files.get(path)


class TestCopy(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.dest = self.root / "state"
        self.dest.mkdir(mode=0o700)
        os.chmod(self.dest, 0o700)

    def tearDown(self):
        self._tmp.cleanup()

    def test_copy_is_paused_private_and_readable(self):
        files = sqlite_bytes(self.root)
        docker = FakeDocker({DB: files[""]})
        copy = copy_sqlite(docker, CID, DB, self.dest, MAX)
        self.assertEqual(docker.calls, [("pause", CID), ("copy", DB), ("copy", DB + "-journal"), ("unpause", CID)])
        self.assertEqual((copy.name, copy.read_bytes()), ("target.sqlite", files[""]))
        self.assertEqual(stat.S_IMODE(os.stat(copy).st_mode), 0o600)
        self.assertEqual(len(snapshot(copy, bytes(RUN_KEY_BYTES))["tables"]["T"]["rows"]), 1)

    def test_wal_database_needs_its_wal_file(self):
        files = sqlite_bytes(self.root, wal=True)
        docker = FakeDocker({DB: files[""], DB + "-wal": files["-wal"]})
        copy = copy_sqlite(docker, CID, DB, self.dest, MAX)
        self.assertIn(("copy", DB + "-wal"), docker.calls)
        self.assertEqual(stat.S_IMODE(os.stat(str(copy) + "-wal").st_mode), 0o600)
        self.assertEqual(len(snapshot(copy, bytes(RUN_KEY_BYTES))["tables"]["T"]["rows"]), 1)
        missing = FakeDocker({DB: files[""]})
        with self.assertRaisesRegex(StateCopyError, "state_copy_wal_missing"):
            copy_sqlite(missing, CID, DB, self._fresh(), MAX)
        self.assertEqual(missing.calls[-1], ("unpause", CID))

    def _fresh(self) -> Path:
        path = Path(tempfile.mkdtemp(dir=self.root))
        os.chmod(path, 0o700)
        return path

    def test_journal_states(self):
        main = sqlite_bytes(self.root)[""]
        # 비었거나(TRUNCATE) 헤더가 0인(PERSIST) 저널은 끝난 저널이다. 헤더가 살아 있으면 쓰기 도중이다.
        for journal in (b"", b"\x00" * 512):
            with self.subTest(journal=len(journal)):
                copy_sqlite(FakeDocker({DB: main, DB + "-journal": journal}), CID, DB, self._fresh(), MAX)
        hot = FakeDocker({DB: main, DB + "-journal": b"\xd9\xd5\x05\xf9\x20\xa1\x63\xd7" + b"\x00" * 504})
        dest = self._fresh()
        with self.assertRaisesRegex(StateCopyError, "state_copy_hot_journal"):
            copy_sqlite(hot, CID, DB, dest, MAX)
        self.assertEqual((hot.calls[-1], os.listdir(dest)), (("unpause", CID), []))

    def test_journal_copy_failure_is_not_absence(self):
        # 저널 복사가 '없음' 말고 다른 이유로 실패하면, 쓰기 도중 저널을 못 본 채 미커밋 행을 읽을 수 있다(#226 리뷰).
        docker = FakeDocker({DB: sqlite_bytes(self.root)[""]}, failing=frozenset({DB + "-journal"}))
        dest = self._fresh()
        with self.assertRaisesRegex(StateCopyError, "state_copy_journal_unknown"):
            copy_sqlite(docker, CID, DB, dest, MAX)
        self.assertEqual((docker.calls[-1], os.listdir(dest)), (("unpause", CID), []))

    def test_missing_or_foreign_main_file(self):
        for files, code in (({}, "state_copy_missing"), ({DB: b"not sqlite" * 20}, "state_copy_not_sqlite"),
                            ({DB: b"SQLite format 3\x00"}, "state_copy_not_sqlite")):
            docker = FakeDocker(files)
            with self.subTest(code=code), self.assertRaisesRegex(StateCopyError, code):
                copy_sqlite(docker, CID, DB, self._fresh(), MAX)
            self.assertEqual(docker.calls[-1], ("unpause", CID))

    def test_unsafe_destination_is_refused_before_pausing(self):
        open_dir = self._fresh()
        os.chmod(open_dir, 0o755)
        link = self.root / "link"
        link.symlink_to(self._fresh())
        plain = self.root / "plain"
        plain.write_bytes(b"")
        os.chmod(plain, 0o700)
        for dest, code in ((open_dir, "state_copy_dir_unsafe"), (link, "state_copy_dir_unsafe"),
                           (plain, "state_copy_dir_unsafe"),
                           (self.root / "nope", "state_copy_dir_missing")):
            docker = FakeDocker({DB: sqlite_bytes(self._fresh())[""]})
            with self.subTest(code=code), self.assertRaisesRegex(StateCopyError, code):
                copy_sqlite(docker, CID, DB, dest, MAX)
            self.assertEqual(docker.calls, [])

    def test_unpause_failure_wins_and_nothing_is_written(self):
        docker = FakeDocker({DB: sqlite_bytes(self.root)[""]}, unpause_fails=True)
        dest = self._fresh()
        with self.assertRaisesRegex(DockerError, "docker_unpause_failed"):
            copy_sqlite(docker, CID, DB, dest, MAX)
        self.assertEqual(os.listdir(dest), [])

    def test_discard_removes_the_copy_and_its_side_files(self):
        dest = self._fresh()
        target = dest / "target.sqlite"
        for suffix in ("", "-wal", "-shm", "-journal"):
            Path(str(target) + suffix).write_bytes(b"x")
        (dest / "keep").write_bytes(b"other")
        discard_copy(target)
        self.assertEqual(os.listdir(dest), ["keep"])
        discard_copy(target)  # 이미 없으면 그대로 끝난다

    def test_discard_failure_is_an_error(self):
        dest = self._fresh()
        target = dest / "target.sqlite"
        target.mkdir()  # 파일이 아니라 지우지 못한다
        with self.assertRaisesRegex(StateCopyError, "state_copy_discard_failed"):
            discard_copy(target)

    def test_partial_write_is_cleaned_up(self):
        files = sqlite_bytes(self.root, wal=True)
        dest = self._fresh()
        Path(str(dest / "target.sqlite") + "-wal").write_bytes(b"old")  # 두 번째 파일 쓰기가 O_EXCL로 실패
        with self.assertRaisesRegex(StateCopyError, "state_copy_write_failed"):
            copy_sqlite(FakeDocker({DB: files[""], DB + "-wal": files["-wal"]}), CID, DB, dest, MAX)
        self.assertEqual(sorted(os.listdir(dest)), ["target.sqlite-wal"])
        self.assertEqual((dest / "target.sqlite-wal").read_bytes(), b"old")

    def test_failed_write_removes_the_file_it_created(self):
        # 새로 만든 파일에 쓰는 도중(ENOSPC) 실패하면 그 파일도 지운다. 앞서 쓴 사본도 지우고, 원래 있던 파일은 둔다.
        files = sqlite_bytes(self.root, wal=True)
        dest = self._fresh()
        (dest / "keep").write_bytes(b"other")
        real_write, calls = os.write, []

        def failing_write(fd, data):
            calls.append(len(data))
            if len(calls) == 2:  # 본 사본은 다 쓰고, -wal을 쓰기 시작할 때 실패
                raise OSError(errno.ENOSPC, "no space")
            return real_write(fd, data)
        with patch("runner_state.os.write", failing_write), \
                self.assertRaisesRegex(StateCopyError, "state_copy_write_failed"):
            copy_sqlite(FakeDocker({DB: files[""], DB + "-wal": files["-wal"]}), CID, DB, dest, MAX)
        self.assertEqual(os.listdir(dest), ["keep"])
        single = self._fresh()
        with patch("runner_state.os.write", lambda fd, data: (_ for _ in ()).throw(OSError(errno.ENOSPC, "x"))), \
                self.assertRaisesRegex(StateCopyError, "state_copy_write_failed"):
            copy_sqlite(FakeDocker({DB: sqlite_bytes(self._fresh())[""]}), CID, DB, single, MAX)
        self.assertEqual(os.listdir(single), [])

    def test_existing_copy_is_not_overwritten(self):
        main = sqlite_bytes(self.root)[""]
        dest = self._fresh()
        (dest / "target.sqlite").write_bytes(b"old")
        with self.assertRaisesRegex(StateCopyError, "state_copy_write_failed"):
            copy_sqlite(FakeDocker({DB: main}), CID, DB, dest, MAX)
        self.assertEqual((dest / "target.sqlite").read_bytes(), b"old")


def tar_of(*members: tuple[str, bytes, bytes]) -> bytes:
    """(이름, 종류, 내용). 종류 b'f'=정규 파일, b'l'=심볼릭 링크, b'd'=디렉터리."""
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w") as archive:
        for name, kind, data in members:
            info = tarfile.TarInfo(name)
            if kind == b"l":
                info.type, info.linkname = tarfile.SYMTYPE, "/etc/passwd"
                archive.addfile(info)
            elif kind == b"d":
                info.type = tarfile.DIRTYPE
                archive.addfile(info)
            else:
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
    return out.getvalue()


class Exec:
    def __init__(self, result: CommandResult):
        self.result, self.calls = result, []

    def __call__(self, args, env, stdin, timeout):
        self.calls.append(list(args))
        return self.result


class Bounded:
    def __init__(self, result: BoundedResult):
        self.result, self.calls = result, []

    def __call__(self, args, env, timeout, max_stdout):
        self.calls.append((list(args), max_stdout))
        return self.result


MISSING = b"Error response from daemon: Could not find the file /app/data/target.sqlite-journal in container c\n"


class TestRuntimeFingerprints(unittest.TestCase):
    KEY = bytes(RUN_KEY_BYTES)
    ROOTS = ("/app/ftp", "/app/uploads")

    def run_it(self, files, required=True, **fake):
        docker = FakeDocker(files, **fake)
        return docker, runtime_fingerprints(docker, CID, self.ROOTS, MAX, self.KEY, required=required)

    def test_paused_around_every_root_and_returns_fingerprints_only(self):
        ftp, uploads = tar_of(("ftp", b"d", b""), ("ftp/a", b"f", b"1")), tar_of(("uploads", b"d", b""))
        docker, trees = self.run_it({"/app/ftp": ftp, "/app/uploads": uploads})
        self.assertEqual(docker.calls, [("pause", CID), ("archive", "/app/ftp", MAX), ("archive", "/app/uploads", MAX),
                                        ("unpause", CID)])
        self.assertEqual((sorted(trees), sorted(trees["/app/ftp"])), (["/app/ftp", "/app/uploads"], ["ftp", "ftp/a"]))
        self.assertEqual(len(trees["/app/ftp"]["ftp/a"]), 32)

    def test_missing_root_stops_pre_and_is_recorded_after_the_run(self):
        ftp = tar_of(("ftp", b"d", b""))
        docker = FakeDocker({"/app/ftp": ftp})
        with self.assertRaisesRegex(StateCopyError, "state_tree_missing"):
            runtime_fingerprints(docker, CID, self.ROOTS, MAX, self.KEY, required=True)
        self.assertEqual(docker.calls[-1], ("unpause", CID))
        docker, trees = self.run_it({"/app/ftp": ftp}, required=False)
        self.assertIsNone(trees["/app/uploads"])
        self.assertEqual(docker.calls[-1], ("unpause", CID))

    def test_copy_failure_is_not_a_missing_root_and_still_unpauses(self):
        docker = FakeDocker({"/app/ftp": tar_of(("ftp", b"d", b""))}, failing=frozenset({"/app/uploads"}))
        with self.assertRaisesRegex(DockerError, "docker_copy_failed"):
            runtime_fingerprints(docker, CID, self.ROOTS, MAX, self.KEY, required=False)
        self.assertEqual(docker.calls[-1], ("unpause", CID))

    def test_unpause_failure_wins(self):
        docker = FakeDocker({"/app/ftp": tar_of(("ftp", b"d", b"")), "/app/uploads": tar_of(("uploads", b"d", b""))},
                            unpause_fails=True)
        with self.assertRaisesRegex(DockerError, "docker_unpause_failed"):
            runtime_fingerprints(docker, CID, self.ROOTS, MAX, self.KEY, required=True)

    def test_pre_rejects_a_root_that_is_not_a_directory_and_still_unpauses(self):
        # 링크 하나뿐인 tar를 그대로 지문으로 삼으면 링크 대상 안이 바뀌어도 "변화 없음"으로 읽힌다.
        ftp = tar_of(("ftp", b"d", b""))
        for label, uploads in (("link", tar_of(("uploads", b"l", b""))), ("file", tar_of(("uploads", b"f", b"x"))),
                               ("other_directory", tar_of(("uploads", b"f", b"x"), ("other", b"d", b"")))):
            with self.subTest(label):
                docker = FakeDocker({"/app/ftp": ftp, "/app/uploads": uploads})
                with self.assertRaisesRegex(StateDiffError, "state_tree_root_not_directory"):
                    runtime_fingerprints(docker, CID, self.ROOTS, MAX, self.KEY, required=True)
                self.assertEqual(docker.calls[-1], ("unpause", CID))

    def test_root_replaced_during_the_run_is_counted_as_a_change_not_a_failure(self):
        # 구간 중에 디렉터리가 링크로 바뀐 건 대상의 변화다. 회차를 버리지 않고 변화로 보여 준다.
        before = {"/app/ftp": tar_of(("ftp", b"d", b""), ("ftp/a", b"f", b"1")),
                  "/app/uploads": tar_of(("uploads", b"d", b""))}
        after = dict(before, **{"/app/ftp": tar_of(("ftp", b"l", b""))})
        _, pre = self.run_it(before, required=True)
        _, post = self.run_it(after, required=False)
        result = runtime_diff(pre, post)
        self.assertTrue(result["runtime_changed"])
        self.assertEqual({k: result["runtime"]["/app/ftp"][k] for k in ("removed", "changed", "missing_post")},
                         {"removed": 1, "changed": 1, "missing_post": False})


IMAGE = "sha256:" + "a" * 64
MEMORY = {"paths": ("/api/a/1", "/api/a/2"), "count_field": "data", "max_body_bytes": 4096, "timeout_ms": 1000}
A64, B64 = "a" * 64, "b" * 64


def ok(count=1, digest=A64):
    return {"code": "ok", "status": 200, "count": count, "sha256": digest}


class FakeReader:
    def __init__(self, stdout):
        self.stdout, self.calls = stdout, []

    def state_read(self, image, container_id, plan, uid, gid):
        self.calls.append((image, container_id, json.loads(plan), uid, gid))
        return self.stdout


class TestMemoryFingerprints(unittest.TestCase):
    KEY = bytes(RUN_KEY_BYTES)

    def read(self, results, required=True, raw=None):
        reader = FakeReader(raw if raw is not None else json.dumps({"results": results}).encode())
        return reader, memory_fingerprints(reader, IMAGE, CID, MEMORY, 3000, 1000, 1000, self.KEY, required=required)

    def test_plan_and_entries(self):
        reader, entries = self.read([ok(2), ok(0, B64)])
        self.assertEqual(reader.calls, [(IMAGE, CID, {"port": 3000, "timeout_ms": 1000, "max_body_bytes": 4096,
                                                      "count_field": "data", "paths": ["/api/a/1", "/api/a/2"]},
                                         1000, 1000)])
        self.assertEqual([(e["readable"], e["count"]) for e in entries], [(True, 2), (True, 0)])
        self.assertNotEqual(entries[0]["fingerprint"], entries[1]["fingerprint"])
        self.assertNotIn(A64, repr(entries))

    def test_unread_path_stops_pre_and_is_marked_after_the_run(self):
        failed = [ok(), {"code": "status", "status": 404, "count": None, "sha256": ""}]
        for result in (failed[1], {"code": "transport", "status": 0, "count": None, "sha256": ""},
                       {"code": "too_large", "status": 200, "count": None, "sha256": ""},
                       {"code": "not_counted", "status": 200, "count": None, "sha256": A64}):
            with self.subTest(code=result["code"]):
                with self.assertRaisesRegex(StateCopyError, "state_memory_unreadable"):
                    self.read([ok(), result])
                _, entries = self.read([ok(), result], required=False)
                self.assertEqual(entries[1], {"readable": False, "count": None, "fingerprint": None})

    def test_output_outside_the_contract_is_refused(self):
        for raw in (b"not json", b"\xff", b"[]", b'{"results":{}}', json.dumps({"results": [ok(), ok()], "x": 1}).encode(), b'{"results":[]}',
                    json.dumps({"results": [ok()]}).encode()):
            with self.subTest(raw=raw[:20]), self.assertRaisesRegex(StateCopyError, "state_memory_output_invalid"):
                self.read(None, raw=raw)
        bad_results = [
            "x", dict(ok(), extra=1), {k: v for k, v in ok().items() if k != "status"}, dict(ok(), status="200"),
            dict(ok(), code="weird"), {"code": "weird", "status": 200, "count": None, "sha256": ""},
            dict(ok(), count=None), dict(ok(), count=-1), dict(ok(), count=True),
            dict(ok(), sha256=""), dict(ok(), sha256="A" * 64), dict(ok(), sha256=1),
            {"code": "not_counted", "status": 200, "count": 1, "sha256": A64},
            {"code": "not_counted", "status": 200, "count": None, "sha256": ""},
            {"code": "status", "status": 404, "count": 1, "sha256": ""},
            {"code": "status", "status": 404, "count": None, "sha256": A64},
        ]
        for result in bad_results:
            with self.subTest(result=result), self.assertRaisesRegex(StateCopyError, "state_memory_output_invalid"):
                self.read([ok(), result], required=False)


class TestDockerStateRead(unittest.TestCase):
    def test_runs_hardened_in_the_target_namespace(self):
        executor = Exec(CommandResult(0, b"{}"))
        output = Docker(executor, {}, 1).state_read(IMAGE, CID, b"plan", 1000, 1001)
        self.assertEqual(output, b"{}")
        self.assertEqual(executor.calls, [["run", "--rm", "-i", "--pull", "never", "--network", f"container:{CID}",
                                           "--read-only", "--cap-drop", "ALL", "--security-opt",
                                           "no-new-privileges:true", "--user", "1000:1001", "--", IMAGE,
                                           "-role=state-read"]])

    def test_arguments_and_failure(self):
        docker = Docker(Exec(CommandResult(0, b"")), {}, 1)
        for args, code in (((IMAGE, "short", b"", 1, 1), "docker_state_read_target_invalid"),
                           (("name:tag", CID, b"", 1, 1), "docker_state_read_image_not_id"),
                           ((IMAGE, CID, b"", -1, 1), "docker_state_read_user_invalid"),
                           ((IMAGE, CID, b"", 1, True), "docker_state_read_user_invalid")):
            with self.subTest(code=code), self.assertRaisesRegex(DockerError, code):
                docker.state_read(*args)
        with self.assertRaisesRegex(DockerError, "docker_state_read_failed"):
            Docker(Exec(CommandResult(1, b"")), {}, 1).state_read(IMAGE, CID, b"", 1, 1)


class TestDockerCopyArchive(unittest.TestCase):
    def archive(self, stdout=b"", returncode=0, path="/app/ftp", max_bytes=MAX, diagnostic=b""):
        bounded = Bounded(BoundedResult(returncode, stdout, diagnostic))
        return Docker(Exec(CommandResult(0, b"")), {}, 1, bounded=bounded).copy_archive(CID, path, max_bytes), bounded.calls

    def test_directory_archive_is_returned_as_is_within_the_exact_limit(self):
        tar = tar_of(("ftp", b"d", b""), ("ftp/a", b"f", b"x"))
        data, calls = self.archive(tar)
        self.assertEqual((data, calls), (tar, [(["cp", f"{CID}:/app/ftp", "-"], MAX)]))

    def test_only_a_daemon_missing_answer_is_absence(self):
        self.assertIsNone(self.archive(returncode=1, diagnostic=MISSING)[0])
        with self.assertRaisesRegex(DockerError, "docker_copy_failed"):
            self.archive(returncode=1, diagnostic=b"permission denied")

    def test_arguments_are_checked_before_calling_docker(self):
        for path, max_bytes in (("app/ftp", MAX), ("/app/../ftp", MAX), ("/app/ftp", 0), ("/app/ftp", True)):
            with self.subTest(path=path, max_bytes=max_bytes), self.assertRaisesRegex(DockerError, "docker_copy_invalid"):
                self.archive(path=path, max_bytes=max_bytes)
        with self.assertRaisesRegex(DockerError, "docker_copy_invalid"):
            Docker(Exec(CommandResult(0, b"")), {}, 1).copy_archive("short", "/app/ftp", MAX)


class TestDockerCopyOut(unittest.TestCase):
    def copy(self, stdout=b"", returncode=0, path=DB, max_bytes=MAX, diagnostic=b""):
        bounded = Bounded(BoundedResult(returncode, stdout, diagnostic))
        docker = Docker(Exec(CommandResult(0, b"")), {}, 1, bounded=bounded)
        return docker.copy_out(CID, path, max_bytes), bounded.calls

    def test_one_regular_file_is_returned(self):
        data, calls = self.copy(tar_of(("target.sqlite", b"f", b"abc")))
        # 읽기 상한은 내용 상한 + tar 여유다(실행기가 그 자리에서 끊는다).
        self.assertEqual((data, calls), (b"abc", [(["cp", f"{CID}:{DB}", "-"], MAX + 64 * 1024)]))

    def test_only_a_daemon_missing_answer_is_absence(self):
        self.assertIsNone(self.copy(returncode=1, diagnostic=MISSING)[0])
        self.assertIsNone(self.copy(returncode=1, diagnostic=b"Error: No such container:path: c:/x\n")[0])
        for diagnostic in (b"", b"permission denied", b"Error response from daemon: context deadline exceeded"):
            with self.subTest(diagnostic=diagnostic), self.assertRaisesRegex(DockerError, "docker_copy_failed"):
                self.copy(returncode=1, diagnostic=diagnostic)

    def test_archive_must_hold_exactly_the_named_regular_file(self):
        for stdout in (tar_of(("target.sqlite", b"l", b"")), tar_of(("target.sqlite", b"d", b"")),
                       tar_of(("other.sqlite", b"f", b"abc")),
                       tar_of(("target.sqlite", b"f", b"abc"), ("extra", b"f", b"x")),
                       tar_of(("target.sqlite", b"f", b"x" * 11)), b"not a tar" * 100):
            with self.subTest(stdout=stdout[:20]), self.assertRaisesRegex(DockerError, "docker_copy_output_invalid"):
                self.copy(stdout, max_bytes=10)

    def test_arguments_are_checked_before_calling_docker(self):
        for path in ("relative.sqlite", "/a/../b", "/a/./b", "/a//b", "/a/b c", "/", "", "/a/b/"):
            with self.subTest(path=path), self.assertRaisesRegex(DockerError, "docker_copy_invalid"):
                self.copy(path=path)
        for max_bytes in (0, True, 1.5):
            with self.subTest(max_bytes=max_bytes), self.assertRaisesRegex(DockerError, "docker_copy_invalid"):
                self.copy(max_bytes=max_bytes)
        with self.assertRaisesRegex(DockerError, "docker_copy_invalid"):
            Docker(Exec(CommandResult(0, b"")), {}, 1).copy_out("short", DB, MAX)

    def test_pause_and_unpause_arguments(self):
        executor = Exec(CommandResult(0, b""))
        docker = Docker(executor, {}, 1)
        docker.pause(CID)
        docker.unpause(CID)
        self.assertEqual(executor.calls, [["pause", CID], ["unpause", CID]])
        for call in (docker.pause, docker.unpause):
            with self.assertRaisesRegex(DockerError, "docker_pause_invalid"):
                call("short")
        failing = Docker(Exec(CommandResult(1, b"")), {}, 1)
        with self.assertRaisesRegex(DockerError, "docker_pause_failed"):
            failing.pause(CID)
        with self.assertRaisesRegex(DockerError, "docker_unpause_failed"):
            failing.unpause(CID)


class TestBoundedRun(unittest.TestCase):
    """실제 하위 프로세스로 상한·시간 초과에서 생산자가 끝나기 전에 끊는지 본다(docker 대신 python 생산자)."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.marker = Path(self._tmp.name) / "finished"

    def tearDown(self):
        self._tmp.cleanup()

    def producer(self, body: str) -> list[str]:
        return [sys.executable, "-I", "-c", body]

    def test_output_beyond_the_limit_stops_the_producer_before_eof(self):
        body = ("import os, sys, time\n"
                "sys.stdout.buffer.write(b'x' * 65536); sys.stdout.flush()\n"
                "time.sleep(30)\n"
                f"open({str(self.marker)!r}, 'w').close()\n")
        started = time.monotonic()
        with self.assertRaisesRegex(DockerError, "docker_copy_too_large"):
            run_docker_bounded([], {}, 20, 1024, program=self.producer(body))
        self.assertLess(time.monotonic() - started, 10)
        time.sleep(0.2)
        self.assertFalse(self.marker.exists())  # 생산자는 끝까지 가지 못하고 종료됐다

    def test_timeout_kills_a_silent_producer(self):
        started = time.monotonic()
        with self.assertRaisesRegex(DockerError, "docker_timeout"):
            run_docker_bounded([], {}, 0.5, 1024, program=self.producer("import time; time.sleep(30)"))
        self.assertLess(time.monotonic() - started, 10)

    def test_result_carries_code_output_and_a_capped_diagnostic(self):
        body = "import sys; sys.stdout.write('abc'); sys.stderr.write('d' * 10000); sys.exit(3)"
        result = run_docker_bounded([], {}, 20, 1024, program=self.producer(body))
        self.assertEqual((result.returncode, result.stdout, len(result.diagnostic)), (3, b"abc", 4096))


if __name__ == "__main__":
    unittest.main()
