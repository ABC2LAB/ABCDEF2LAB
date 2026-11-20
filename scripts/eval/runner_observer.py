"""O 회차를 제품 화면으로 지켜볼 때 쓰는 관리자 로그인 파일(2026-11-20 사용자).

공식·파일럿 O 회차를 제품 화면으로 지켜볼 수 있다(읽기만, 공식 회차 절차 6절 관찰자 규칙). 로그인에 쓸 회차 관리자 비밀번호는
회차마다 새로 만들어지고 보호 입력(private/install.json)에만 있다. 러너 설정 `split.observer_password_file`(선택)에 경로를
적으면, 그 비밀번호를 이 PC의 0600 파일 하나에만 쓰고 회차가 어떻게 끝나도 보호 입력과 같이 지운다.
- 로그·결과·서명 근거에는 경로도 값도 남기지 않는다. 설정 지문(러너 설정·구동기 설정 SHA)에도 넣지 않는다. 화면 관찰
  여부는 회차 조건이 아니기 때문이다.
- 위치: runs_root 밖(근거와 같이 복사되지 않게), 부모는 러너 uid 소유 0700 디렉터리이고 그 안에 다른 설정 파일(키·비밀·
  manifest 등)이 없어야 한다(설정 검사). 파일은 새로 만든다(O_EXCL·O_NOFOLLOW, 0600). 강제 종료로 전 회차 파일이 남아
  있으면, 러너 uid 소유 일반 파일이고 관찰 로그인 파일 모양(첫 줄 '아이디: ')일 때만 지우고 새로 쓴다. 그 밖의 파일은 지우지
  않고 멈춘다(설정을 잘못 적어 키 파일을 가리켜도 지우지 않게, #824 리뷰).
"""

from __future__ import annotations

import os
import stat
from pathlib import Path

from runner_engine import AdminSecret


class ObserverFileError(ValueError):
    """관찰 로그인 파일을 쓰거나 지우지 못함. 고정 code만 담는다(값·경로 없음)."""


def _private_parent(path: Path) -> bool:
    try:
        info = os.lstat(path.parent)
    except OSError:
        return False
    return stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o700


HEADER = "아이디: ".encode("utf-8")


def _remove_stale(path: Path) -> None:
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        return
    except OSError:
        raise ObserverFileError("observer_file_unreadable") from None
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
        raise ObserverFileError("observer_file_untrusted")
    try:
        with open(path, "rb") as stream:
            head = stream.read(len(HEADER))
    except OSError:
        raise ObserverFileError("observer_file_unreadable") from None
    if head != HEADER:
        raise ObserverFileError("observer_file_not_ours")  # 관찰 로그인 파일이 아닌 것(키 파일 등)은 지우지 않는다
    try:
        os.unlink(path)
    except OSError:
        raise ObserverFileError("observer_file_remove_failed") from None


def write_observer_password(path: Path, secret: AdminSecret) -> None:
    """회차 관리자 아이디·비밀번호를 0600 새 파일에 쓴다."""
    path = Path(path)
    if not _private_parent(path):
        raise ObserverFileError("observer_directory_not_private")
    _remove_stale(path)
    data = f"아이디: {secret.username}\n비밀번호: {secret.reveal()}\n".encode("utf-8")
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    except OSError:
        raise ObserverFileError("observer_file_create_failed") from None
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
    except OSError:
        try:
            os.unlink(path)
        except OSError:
            pass  # 아래 오류로 멈추고, 회차 정리(remove_observer_password)가 다시 지운다
        raise ObserverFileError("observer_file_write_failed") from None


def remove_observer_password(path: Path | None) -> None:
    """관찰 로그인 파일을 지운다. 칸이 없거나 이미 없으면 그대로 둔다."""
    if path is None:
        return
    _remove_stale(Path(path))
