"""상태 차이용 대상 SQLite 사본 뜨기(평가 프로토콜 §8.2). 비교는 state_diff.py가 한다.

대상을 잠깐 멈추고(docker pause) 데몬으로 DB 파일을 꺼낸 뒤 다시 움직인다. 대상 안의 셸·sqlite 도구는 쓰지 않는다
(엔진이 대상을 바꿨을 수 있다). 사본에는 비밀번호 해시·카드 번호가 들어 있으므로 러너 전용 0700 디렉터리에 0600으로만
쓰고, 비교가 끝나면 러너가 지운다. 결과·기록에는 경로를 넣지 않는다.

- pause·unpause는 Docker 이벤트를 만든다. 그래서 격리 구간 밖(pre 수집 전, 구간 감시 종료 뒤)에서만 부른다.
- WAL DB(헤더 18·19번째 바이트가 2)는 `-wal`까지 있어야 마지막 커밋이 보인다. 없으면 사본을 쓰지 않는다.
- 롤백 저널(`-journal`)이 있고 헤더가 0이 아니면 멈춘 순간에 쓰기가 진행 중이었다는 뜻이라 사본을 쓰지 않는다
  (읽기 전용으로는 되돌릴 수 없다). 평가 회차에서는 계측기 봉인·drain 뒤라 대상이 조용해야 정상이다.
- '파일 없음'은 데몬이 그 경로에 파일이 없다고 답한 경우만이다(runner_docker.copy_out). 본 DB와 WAL의 `-wal`은
  필수다. 저널은 확실히 없을 때만 없는 것으로 보고, 저널 복사가 그 밖의 이유로 실패하면 사본을 쓰지 않는다
  (쓰기 도중 저널을 못 보고 미커밋 행을 읽을 수 있다, #226 리뷰).
"""

from __future__ import annotations

import json
import os
import posixpath
import re
import stat
from pathlib import Path

from runner_docker import DockerError
from state_diff import digest_fingerprint, require_directory_root, tree_fingerprint

SQLITE_HEADER = b"SQLite format 3\x00"
_HEADER_BYTES = 100
_WAL_VERSION = 2
# 롤백 저널 헤더(매직 8바이트 + 레코드 수·nonce·초기 크기 등)가 전부 0이면 커밋이 끝난 저널이다(PERSIST 모드).
_JOURNAL_HEADER_BYTES = 28


_READ_OK = "ok"
_READ_NOT_COUNTED = "not_counted"
_READ_NO_BODY = frozenset({"status", "transport", "too_large"})
_READ_FIELDS = frozenset({"code", "status", "count", "sha256"})
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")


class StateCopyError(RuntimeError):
    """사본을 믿고 쓸 수 없다. 고정 코드만 담는다(경로·값 없음)."""


def _private_dir(path: Path) -> None:
    try:
        info = os.lstat(path)
    except OSError:
        raise StateCopyError("state_copy_dir_missing") from None
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise StateCopyError("state_copy_dir_unsafe")


def _write_private(path: Path, data: bytes) -> None:
    """O_EXCL로 새로 만든 파일에만 쓴다. 쓰기·fsync·close가 실패하면 이 호출이 만든 그 파일을 지운다(#227 리뷰).

    O_EXCL이라 열기에 성공했다면 이 호출이 만든 파일이다. 열기 자체가 실패하면(이미 있음 등) 아무것도 지우지 않는다.
    """
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    try:
        try:
            view = memoryview(data)
            while view:
                written = os.write(fd, view)
                view = view[written:]
            os.fsync(fd)
        finally:
            os.close(fd)
    except BaseException:
        os.unlink(path)
        raise


def _is_wal(header: bytes) -> bool:
    return header[18] == _WAL_VERSION or header[19] == _WAL_VERSION


def _copy_files(docker, container_id: str, path: str, max_bytes: int) -> dict[str, bytes]:
    main = docker.copy_out(container_id, path, max_bytes)
    if main is None:
        raise StateCopyError("state_copy_missing")
    if len(main) < _HEADER_BYTES or not main.startswith(SQLITE_HEADER):
        raise StateCopyError("state_copy_not_sqlite")
    try:
        journal = docker.copy_out(container_id, path + "-journal", max_bytes)
    except DockerError:
        raise StateCopyError("state_copy_journal_unknown") from None
    if journal and any(journal[:_JOURNAL_HEADER_BYTES]):
        raise StateCopyError("state_copy_hot_journal")
    files = {"": main}
    if _is_wal(main[:_HEADER_BYTES]):
        wal = docker.copy_out(container_id, path + "-wal", max_bytes)
        if wal is None:
            raise StateCopyError("state_copy_wal_missing")
        files["-wal"] = wal
    return files


def runtime_fingerprints(docker, container_id: str, roots: tuple[str, ...], max_bytes: int, key: bytes,
                         *, required: bool) -> dict[str, dict[str, bytes] | None]:
    """대상을 멈춘 채 설정한 런타임 디렉터리의 항목 지문을 뜬다. 파일은 호스트에 쓰지 않는다.

    required면(pre) 없는 디렉터리는 설정 오류로 멈춘다. post에서 없어진 디렉터리는 None으로 돌려준다(상태 변화로 센다).
    required면 루트가 실제 디렉터리인지도 본다(링크·정규 파일이면 설정 오류). post에서 루트가 다른 종류로 바뀌었으면
    막지 않는다. 항목 종류가 지문에 들어가서 변화로 집계된다.
    unpause는 성공·실패와 무관하게 부르고, 실패하면 그 오류가 우선이다.
    """
    docker.pause(container_id)
    try:
        trees: dict[str, dict[str, bytes] | None] = {}
        for root in roots:
            archive = docker.copy_archive(container_id, root, max_bytes)
            if archive is None and required:
                raise StateCopyError("state_tree_missing")
            if required:
                require_directory_root(archive, root)
            trees[root] = None if archive is None else tree_fingerprint(archive, key)
    finally:
        docker.unpause(container_id)
    return trees


def _read_result(result: object) -> dict:
    """state-read 역할의 결과 한 줄을 계약대로 확인한다. 어긋나면 그 도구를 믿지 않는다."""
    if not isinstance(result, dict) or set(result) != _READ_FIELDS or type(result["status"]) is not int:
        raise StateCopyError("state_memory_output_invalid")
    code, count, digest = result["code"], result["count"], result["sha256"]
    if code == _READ_OK:
        valid = type(count) is int and count >= 0 and isinstance(digest, str) and bool(_SHA256.fullmatch(digest))
    elif code == _READ_NOT_COUNTED:
        valid = count is None and isinstance(digest, str) and bool(_SHA256.fullmatch(digest))
    else:
        valid = code in _READ_NO_BODY and count is None and digest == ""
    if not valid:
        raise StateCopyError("state_memory_output_invalid")
    return result


def memory_fingerprints(docker, image: str, container_id: str, memory: dict, port: int, uid: int, gid: int, key: bytes,
                        *, required: bool) -> list[dict]:
    """등록 읽기 경로(같은 순서)의 건수와 회차 키 지문. 응답 본문·다이제스트는 남기지 않는다.

    required면(pre) 못 읽은 경로는 설정 오류로 멈춘다. post에서 못 읽으면 그 경로를 readable=False로 돌려준다(변화로 센다).
    """
    plan = json.dumps({"port": port, "timeout_ms": memory["timeout_ms"], "max_body_bytes": memory["max_body_bytes"],
                       "count_field": memory["count_field"], "paths": list(memory["paths"])}).encode()
    try:
        report = json.loads(docker.state_read(image, container_id, plan, uid, gid).decode("utf-8"))
    except (UnicodeError, ValueError, RecursionError):
        raise StateCopyError("state_memory_output_invalid") from None
    if not isinstance(report, dict) or set(report) != {"results"} or not isinstance(report["results"], list) \
            or len(report["results"]) != len(memory["paths"]):
        raise StateCopyError("state_memory_output_invalid")
    entries = []
    for result in report["results"]:
        checked = _read_result(result)
        if checked["code"] != _READ_OK:
            if required:
                raise StateCopyError("state_memory_unreadable")
            entries.append({"readable": False, "count": None, "fingerprint": None})
        else:
            entries.append({"readable": True, "count": checked["count"],
                            "fingerprint": digest_fingerprint(checked["sha256"], key)})
    return entries


def discard_copy(target: Path) -> None:
    """사본과 그 곁 파일(-wal·-shm·-journal, SQLite가 열 때 만든 것 포함)을 지운다. 지우지 못하면 오류다(값이 남는다)."""
    for suffix in ("", "-wal", "-shm", "-journal"):
        try:
            os.unlink(str(target) + suffix)
        except FileNotFoundError:
            continue
        except OSError:
            raise StateCopyError("state_copy_discard_failed") from None


def copy_sqlite(docker, container_id: str, path: str, dest: Path, max_bytes: int) -> Path:
    """대상을 멈춘 채 SQLite 파일(WAL이면 `-wal`까지)을 꺼내 dest에 0600으로 쓰고 본 사본 경로를 돌려준다.

    unpause는 성공·실패와 무관하게 부른다. unpause가 실패하면 대상이 멈춘 채 남으므로 그 오류가 우선이다.
    """
    _private_dir(dest)
    target = dest / posixpath.basename(path)
    docker.pause(container_id)
    try:
        files = _copy_files(docker, container_id, path, max_bytes)
    finally:
        docker.unpause(container_id)
    written: list[str] = []
    for suffix, data in files.items():
        try:
            _write_private(Path(str(target) + suffix), data)
        except OSError:
            # 일부만 쓴 사본은 남기지 않는다(먼저 있던 파일은 건드리지 않는다).
            for done in written:
                os.unlink(str(target) + done)
            raise StateCopyError("state_copy_write_failed") from None
        written.append(suffix)
    return target
