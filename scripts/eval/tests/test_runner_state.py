"""상태 차이 사본 뜨기(프로토콜 §8.2): 대상 일시정지 → 데몬 복사 → 재개, 0600 저장. docker는 가짜다."""

from __future__ import annotations

import io
import os
import sqlite3
import stat
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_docker import CommandResult, Docker, DockerError  # noqa: E402
from runner_state import StateCopyError, copy_sqlite, discard_copy  # noqa: E402
from state_diff import RUN_KEY_BYTES, snapshot  # noqa: E402

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
    def __init__(self, files: dict[str, bytes], unpause_fails: bool = False):
        self.files, self.calls, self.unpause_fails = files, [], unpause_fails

    def pause(self, cid):
        self.calls.append(("pause", cid))

    def unpause(self, cid):
        self.calls.append(("unpause", cid))
        if self.unpause_fails:
            raise DockerError("docker_unpause_failed")

    def copy_out(self, cid, path, max_bytes):
        self.calls.append(("copy", path))
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


class TestDockerCopyOut(unittest.TestCase):
    def copy(self, stdout=b"", returncode=0, path=DB, max_bytes=MAX):
        executor = Exec(CommandResult(returncode, stdout))
        return Docker(executor, {}, 1).copy_out(CID, path, max_bytes), executor.calls

    def test_one_regular_file_is_returned(self):
        data, calls = self.copy(tar_of(("target.sqlite", b"f", b"abc")))
        self.assertEqual((data, calls), (b"abc", [["cp", f"{CID}:{DB}", "-"]]))

    def test_failed_copy_is_none(self):
        self.assertIsNone(self.copy(returncode=1)[0])

    def test_archive_must_hold_exactly_the_named_regular_file(self):
        for stdout in (tar_of(("target.sqlite", b"l", b"")), tar_of(("target.sqlite", b"d", b"")),
                       tar_of(("other.sqlite", b"f", b"abc")),
                       tar_of(("target.sqlite", b"f", b"abc"), ("extra", b"f", b"x")),
                       tar_of(("target.sqlite", b"f", b"x" * 11)), b"not a tar" * 100):
            with self.subTest(stdout=stdout[:20]), self.assertRaisesRegex(DockerError, "docker_copy_output_invalid"):
                self.copy(stdout, max_bytes=10)

    def test_output_beyond_the_limit_is_refused(self):
        with self.assertRaisesRegex(DockerError, "docker_copy_too_large"):
            self.copy(b"\x00" * (10 + 64 * 1024 + 1), max_bytes=10)

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


if __name__ == "__main__":
    unittest.main()
