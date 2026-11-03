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
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import IO, Callable, Mapping, Protocol, Sequence

# docker CLI가 데몬에 접속하는 데 쓰는 이름만 셸에서 가져온다.
DOCKER_CLIENT_ENV = ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
                     "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "XDG_RUNTIME_DIR")
# 평가 프로토콜 §2: 28.0.0 이전에는 같은 L2 구간에서 publish 안 된 포트에 닿는 예외가 있다.
MIN_SERVER_VERSION = (28, 0, 0)
MAX_OUTPUT_BYTES = 4 * 1024 * 1024
_VERSION = re.compile(r"(\d+)\.(\d+)\.(\d+)(.*)\Z", re.S)
_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")
_OBJECT_ID_LENGTH = 64
_OBJECT_ID = re.compile(r"[0-9a-f]{%d}\Z" % _OBJECT_ID_LENGTH)
_PROJECT = re.compile(r"[a-z0-9][a-z0-9_-]{0,62}\Z")
# docker events --since/--until에 넘기는 UTC 시각(초 소수 9자리까지). 러너가 만든 값만 받는다.
_EVENT_TIME = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z\Z")
# 컨테이너 inspect는 이 필드만 꺼낸다. .Config.Env 등 비밀이 들 수 있는 필드는 러너 메모리에도 올리지 않는다.
CONTAINER_TEMPLATE = (
    '{"id":{{json .Id}},'
    '"project":{{json (index .Config.Labels "com.docker.compose.project")}},'
    '"service":{{json (index .Config.Labels "com.docker.compose.service")}},'
    '"image":{{json .Image}},"running":{{json .State.Running}},'
    '"started_at":{{json .State.StartedAt}},"restart_count":{{json .RestartCount}},'
    '"ports":{{json .NetworkSettings.Ports}},"networks":{{json .NetworkSettings.Networks}}}'
)


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


# 구간 이벤트 스트림. 데몬은 지난 이벤트를 256개만 보관하므로 구간 끝에 한 번에 조회하면 긴 구간(healthcheck만으로
# 초당 1.5건)을 덮지 못한다. 그래서 구간 시작에 스트림을 열어 두고 실시간으로 받는다.
MAX_STREAM_EVENTS = 100_000  # 메모리 상한. 넘으면 불완전
MAX_EVENT_LINE_BYTES = 64 * 1024
# 구간 끝 시각을 정한 뒤 그때까지의 이벤트가 스트림에 도착하도록 기다리는 시간(초).
EVENTS_DRAIN_GRACE_SECONDS = 1.0


class StreamProcess(Protocol):
    stdout: IO[bytes]

    def poll(self) -> int | None: ...
    def terminate(self) -> None: ...
    def wait(self, timeout: float | None = None) -> int: ...


def open_docker_stream(args: Sequence[str], env: Mapping[str, str]) -> StreamProcess:
    try:
        return subprocess.Popen(["docker", *args], env=dict(env), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=None)
    except OSError:
        raise DockerError("docker_unavailable") from None


def _read_cidfile(path: Path) -> str:
    """docker가 쓴 컨테이너 ID(64 hex) 하나. 없거나 모양이 다르면 그 probe를 러너 것으로 인정할 수 없다."""
    try:
        with open(path, "rb") as handle:
            data = handle.read(_OBJECT_ID_LENGTH + 1)  # 한 바이트 더 읽어 뒤에 붙은 내용도 거부한다
        value = data.decode("ascii")
    except (OSError, UnicodeError):
        raise DockerError("docker_probe_cid_invalid") from None
    if not _OBJECT_ID.fullmatch(value):
        raise DockerError("docker_probe_cid_invalid")
    return value


class EventStream:
    """`docker events --since`를 구간 동안 읽는다. 끊김·해석 불가·상한 초과는 불완전(complete=False)이다."""

    def __init__(self, process: StreamProcess, sleep: Callable[[float], None] = time.sleep):
        self._process, self._sleep = process, sleep
        self._events: list[dict] = []
        self._broken = False
        # 러너가 종료를 시작한 뒤의 EOF만 정상이다. 그 전의 EOF는 프로세스가 살아 있어도 구간 중 관측 공백이다(#126 리뷰).
        self._stopping = threading.Event()
        self._reader = threading.Thread(target=self._read, name="docker-events", daemon=True)
        self._reader.start()

    def _read(self) -> None:
        try:
            while True:
                line = self._process.stdout.readline(MAX_EVENT_LINE_BYTES + 1)
                if not line:
                    if not self._stopping.is_set():
                        self._broken = True
                    return
                if len(line) > MAX_EVENT_LINE_BYTES or len(self._events) >= MAX_STREAM_EVENTS:
                    self._broken = True
                    return
                event = json.loads(line.decode("utf-8"))
                if not isinstance(event, dict):
                    self._broken = True
                    return
                self._events.append(event)
        except (OSError, ValueError, UnicodeError, RecursionError):
            self._broken = True

    def stop(self, until_ns: int) -> tuple[list[dict], bool]:
        """until_ns(나노초)까지의 이벤트와 완결 여부. 유예 뒤에도 프로세스가 살아 있어야 구간 내내 붙어 있던 것이다."""
        self._sleep(EVENTS_DRAIN_GRACE_SECONDS)
        alive = self._process.poll() is None
        self._stopping.set()
        self._process.terminate()
        try:
            self._process.wait(timeout=EVENTS_DRAIN_GRACE_SECONDS * 10)
        except subprocess.TimeoutExpired:
            alive = False
        self._reader.join(timeout=EVENTS_DRAIN_GRACE_SECONDS * 10)
        complete = alive and not self._broken and not self._reader.is_alive()
        events = [e for e in self._events if not isinstance(e.get("timeNano"), int) or e["timeNano"] <= until_ns]
        return events, complete


def _require_project(project: str) -> None:
    if not isinstance(project, str) or not _PROJECT.fullmatch(project):
        raise DockerError("docker_project_invalid")


def _version_ok(version: str) -> bool:
    match = _VERSION.match(version)
    if not match:
        return False
    numbers = tuple(int(part) for part in match.groups()[:3])
    # 정확히 최소 버전의 prerelease(28.0.0-rc.1 등)는 그 버전 이전으로 본다.
    return numbers > MIN_SERVER_VERSION or (numbers == MIN_SERVER_VERSION and not match.group(4).startswith("-"))


class Docker:
    def __init__(self, executor: Executor, env: Mapping[str, str], timeout: float,
                 stream_opener: Callable[[Sequence[str], Mapping[str, str]], StreamProcess] = open_docker_stream,
                 sleep: Callable[[float], None] = time.sleep):
        self._executor = executor
        self._env = dict(env)
        self._timeout = timeout
        self._stream_opener = stream_opener
        self._sleep = sleep

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

    def json_lines(self, args: Sequence[str], *, code: str) -> list:
        out = self.run(args, code=code)
        try:
            return [json.loads(line) for line in out.decode("utf-8").splitlines() if line.strip()]
        except (UnicodeError, ValueError, RecursionError):
            raise DockerError(code + "_output_invalid") from None

    def _ids(self, args: Sequence[str], *, code: str) -> list[str]:
        try:
            ids = [line.strip() for line in self.run(args, code=code).decode("ascii").splitlines() if line.strip()]
        except UnicodeError:
            raise DockerError(code + "_output_invalid") from None
        if not all(_OBJECT_ID.fullmatch(value) for value in ids) or len(set(ids)) != len(ids):
            raise DockerError(code + "_output_invalid")
        return sorted(ids)

    def project_network_ids(self, project: str) -> list[str]:
        _require_project(project)
        return self._ids(["network", "ls", "--no-trunc", "--quiet", "--filter",
                          f"label=com.docker.compose.project={project}"], code="docker_network_list_failed")

    def project_container_ids(self, project: str) -> list[str]:
        """멈춘 것까지 전부(-a). 회차 구성원이 죽어 있으면 그것도 점검 대상이다."""
        _require_project(project)
        return self._ids(["ps", "--all", "--no-trunc", "--quiet", "--filter",
                          f"label=com.docker.compose.project={project}"], code="docker_container_list_failed")

    def running_container_ids(self) -> list[str]:
        return self._ids(["ps", "--no-trunc", "--quiet"], code="docker_container_list_failed")

    def inspect_networks(self, ids: Sequence[str]) -> list:
        if not ids:
            return []
        return self.json_lines(["network", "inspect", "--format", "{{json .}}", "--", *ids],
                               code="docker_network_inspect_failed")

    def inspect_containers(self, ids: Sequence[str]) -> list:
        if not ids:
            return []
        return self.json_lines(["container", "inspect", "--format", CONTAINER_TEMPLATE, "--", *ids],
                               code="docker_container_inspect_failed")

    def compose_up(self, base_args: Sequence[str], wait_seconds: int) -> None:
        """pull·빌드 없이 띄우고 healthcheck·실행 상태를 기다린다. 이미지는 미리 내용 ID로 확인해 둔다."""
        self.run([*base_args, "up", "--detach", "--pull", "never", "--no-build", "--wait",
                  "--wait-timeout", str(wait_seconds)], code="docker_compose_up_failed")

    def compose_down(self, base_args: Sequence[str], timeout_seconds: int) -> None:
        """회차 망·볼륨·컨테이너를 지운다. 성공·실패와 무관하게 러너가 항상 부른다."""
        self.run([*base_args, "down", "--volumes", "--remove-orphans", "--timeout", str(timeout_seconds)],
                 code="docker_compose_down_failed")

    def event_stream(self, since: str) -> EventStream:
        """since부터 데몬 이벤트를 실시간으로 받기 시작한다. 필터 없이 전부 받아 러너가 판정한다."""
        if not _EVENT_TIME.fullmatch(since or ""):
            raise DockerError("docker_events_window_invalid")
        return EventStream(self._stream_opener(["events", "--since", since, "--format", "{{json .}}"], self._env),
                           self._sleep)

    def signal(self, container_id: str, signal_name: str) -> None:
        if not _OBJECT_ID.fullmatch(container_id or "") or signal_name not in ("USR1",):
            raise DockerError("docker_signal_invalid")
        self.run(["kill", "--signal", signal_name, container_id], code="docker_signal_failed")

    def probe(self, image: str, engine_container_id: str, plan: bytes, uid: int, gid: int,
              cidfile: Path) -> tuple[bytes, str]:
        """엔진 컨테이너의 네트워크 namespace에서 계측기 이미지(내용 ID)의 probe 역할을 1회 실행한다.

        엔진 이미지 내용에 기대지 않는다. 마운트·소켓·권한 없이 읽기 전용으로 돌고, pull하지 않는다.
        종료 코드가 0이 아니면(계획 오류·시작 실패) 호출 실패이며, 점검 무효로 처리하는 것은 호출자다.
        probe 컨테이너 ID를 `--cidfile`로 받아 출력과 함께 돌려준다. 구간 감시는 러너 자신의 probe만 허용하고
        그 밖의 컨테이너 생성·시작은 경계 변경으로 본다(runner_window). cidfile은 회차 디렉터리 안 새 경로다.
        """
        if not _OBJECT_ID.fullmatch(engine_container_id or ""):
            raise DockerError("docker_probe_target_invalid")
        if not isinstance(image, str) or not _IMAGE_ID.fullmatch(image):
            raise DockerError("docker_probe_image_not_id")
        if type(uid) is not int or type(gid) is not int or uid < 0 or gid < 0:
            raise DockerError("docker_probe_user_invalid")
        output = self.run(["run", "--rm", "-i", "--pull", "never", "--cidfile", str(cidfile),
                           "--network", f"container:{engine_container_id}",
                           "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                           "--user", f"{uid}:{gid}", "--", image, "-role=probe"],
                          stdin=plan, code="docker_probe_failed")
        return output, _read_cidfile(cidfile)

    def daemon(self) -> DaemonIdentity:
        server = self.json(["version", "--format", "{{json .Server}}"], code="docker_version_failed")
        if not isinstance(server, dict):
            raise DockerError("docker_version_output_invalid")
        platform = server.get("Platform")
        fields = (server.get("Version"), server.get("Os"), server.get("Arch"),
                  platform.get("Name") if isinstance(platform, dict) else None)
        # 배포판 패키지(Ubuntu docker.io 등)는 Platform.Name을 비워 둔다. 이름은 있으면 기록·대조하고, 빈 값도
        # 하나의 값으로 대조한다(Desktop↔배포판 전환은 그대로 다름으로 잡힌다). 버전·OS·아키텍처는 비면 거부.
        if not all(isinstance(value, str) for value in fields) or not all(fields[:3]):
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
