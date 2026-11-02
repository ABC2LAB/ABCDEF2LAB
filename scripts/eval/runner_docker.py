"""평가 러너의 docker 호출 경계.

셸 없이 인자 목록으로만 docker를 실행한다. 환경은 docker 접속에 필요한 이름과 러너가 만든
compose 값만 넘긴다. 셸에 남은 EVAL_*·ARTEX_*·API 키가 회차 설정을 덮어쓰지 못하게 하려는 것이다.
docker 출력은 JSON으로만 해석하고 원문을 오류에 넣지 않는다(설정값·경로가 섞일 수 있음).
docker 자신의 진단(stderr)은 운영자 터미널로만 가고 러너 기록에는 남기지 않는다.
"""

from __future__ import annotations

import json
import re
import subprocess
from dataclasses import dataclass
from typing import Callable, Mapping, Sequence

# docker CLI가 데몬에 접속하는 데 쓰는 이름만 셸에서 가져온다.
DOCKER_CLIENT_ENV = ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
                     "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "XDG_RUNTIME_DIR")
# 평가 프로토콜 §2: 28.0.0 이전에는 같은 L2 구간에서 publish 안 된 포트에 닿는 예외가 있다.
MIN_SERVER_VERSION = (28, 0, 0)
MAX_OUTPUT_BYTES = 4 * 1024 * 1024
_VERSION = re.compile(r"(\d+)\.(\d+)\.(\d+)(.*)\Z", re.S)
_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")


class DockerError(RuntimeError):
    """docker 호출 실패. 메시지는 고정 코드이며 docker 출력 원문을 담지 않는다."""


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: bytes


Executor = Callable[[Sequence[str], Mapping[str, str], "bytes | None", float], CommandResult]


@dataclass(frozen=True)
class DaemonIdentity:
    version: str
    os: str
    arch: str
    platform: str


def client_environment(source: Mapping[str, str], extra: Mapping[str, str]) -> dict[str, str]:
    """docker 접속 이름만 source에서 고르고 러너 값을 더한다. 겹치면 오류(설정 혼선 방지)."""
    env = {name: source[name] for name in DOCKER_CLIENT_ENV if name in source}
    overlap = set(env) & set(extra)
    if overlap:
        raise DockerError("docker_env_overlap")
    env.update(extra)
    return env


def run_docker(args: Sequence[str], env: Mapping[str, str], stdin: bytes | None, timeout: float) -> CommandResult:
    try:
        completed = subprocess.run(["docker", *args], input=stdin, env=dict(env), stdout=subprocess.PIPE,
                                   stderr=None, timeout=timeout, check=False)
    except subprocess.TimeoutExpired:
        raise DockerError("docker_timeout") from None
    except OSError:
        raise DockerError("docker_unavailable") from None
    return CommandResult(completed.returncode, completed.stdout)


def _version_ok(version: str) -> bool:
    match = _VERSION.match(version)
    if not match:
        return False
    numbers = tuple(int(part) for part in match.groups()[:3])
    # 정확히 최소 버전의 prerelease(28.0.0-rc.1 등)는 그 버전 이전으로 본다.
    return numbers > MIN_SERVER_VERSION or (numbers == MIN_SERVER_VERSION and not match.group(4).startswith("-"))


class Docker:
    def __init__(self, executor: Executor, env: Mapping[str, str], timeout: float):
        self._executor = executor
        self._env = dict(env)
        self._timeout = timeout

    def run(self, args: Sequence[str], *, stdin: bytes | None = None, code: str) -> bytes:
        """성공(종료 0)한 호출의 stdout만 돌려준다. 실패는 호출별 고정 code."""
        result = self._executor(list(args), self._env, stdin, self._timeout)
        if result.returncode != 0:
            raise DockerError(code)
        if len(result.stdout) > MAX_OUTPUT_BYTES:
            raise DockerError(code + "_output_too_large")
        return result.stdout

    def json(self, args: Sequence[str], *, stdin: bytes | None = None, code: str) -> object:
        out = self.run(args, stdin=stdin, code=code)
        try:
            return json.loads(out.decode("utf-8"))
        except (UnicodeError, ValueError, RecursionError):
            raise DockerError(code + "_output_invalid") from None

    def daemon(self) -> DaemonIdentity:
        server = self.json(["version", "--format", "{{json .Server}}"], code="docker_version_failed")
        if not isinstance(server, dict):
            raise DockerError("docker_version_output_invalid")
        platform = server.get("Platform")
        fields = (server.get("Version"), server.get("Os"), server.get("Arch"),
                  platform.get("Name") if isinstance(platform, dict) else None)
        if not all(isinstance(value, str) and value for value in fields):
            raise DockerError("docker_version_output_invalid")
        identity = DaemonIdentity(*fields)
        if not _version_ok(identity.version):
            raise DockerError("docker_server_too_old")
        return identity

    def image_id(self, reference: str) -> str:
        """로컬 이미지의 내용 ID. 러너는 pull하지 않으므로 없으면 실패다."""
        value = self.json(["image", "inspect", "--format", "{{json .Id}}", "--", reference],
                          code="docker_image_missing")
        if not isinstance(value, str) or not _IMAGE_ID.fullmatch(value):
            raise DockerError("docker_image_missing_output_invalid")
        return value
