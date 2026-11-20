"""평가 머신 환경 점검(읽기만 한다). 공식 측정 세션 전에 평가 머신(이 PC, 2026-11-20 결정)에서 돌린다.

무엇도 만들거나 바꾸지 않는다: 이미지 빌드·pull, 컨테이너·망·볼륨 생성, 파일 쓰기를 하지 않는다. 기본 이미지는 로컬에
있는지 보고, 없으면 레지스트리 manifest만 읽는다(`docker buildx imagetools inspect`). 비밀 값은 읽지 않는다.

기준값은 개발 머신(WSL2 Ubuntu 24.04, 네이티브 dockerd)에서 분리 배치 회차를 실제로 돌린 값과 레포 설정값이다(공식 회차 절차
3절). 출력은 항목마다 한 줄 `PASS|FAIL|SKIP 항목: 사유`다. 하나라도 FAIL이면 종료 코드 1, FAIL 없이 SKIP이 있으면 2(점검을
끝내지 못함: 안내대로 다시 돌린다), 모두 PASS면 0이다.

실행(동결 작업 트리 "$T"에서): python3 -I -B scripts/eval/env_check.py --config <공식 runner.json>
공식 runner.json은 docs/evaluation/runner.official-split.example.json을 복사해 채운 파일이다(분리 배치 포트가 들어 있다).
"""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from runner_checks import Listener, check_host_listeners, read_host_listeners, subnet_overlaps  # noqa: E402

# 개발 머신에서 분리 배치 회차(B·O)를 실제로 돌린 값(2026-11-19). 이보다 낮으면 그 판에서 확인한 동작을 보장하지 못한다.
# Docker 29: 구간 감시가 docker stop 이벤트 순서(kill→disconnect→stop→die)와 healthcheck 동작 문자열을 29.1.3에서 확인했다(#669).
MIN_DOCKER = (29, 0)
MIN_COMPOSE = (2, 40)
MIN_PYTHON = (3, 12)
MIN_OPENSSL = (3, 0)
# 개발 머신(WSL2) 메모리. 분리 배치 O·B 회차를 이 메모리에서 돌렸다.
MIN_MEM_KIB = 7_904_928
# 분리 배치 공식 회차에 쓰는 이미지 합계(개발 머신 실측: 제품 2.51GB·탐색 브라우저 1.93GB·fixture 131MB·계측기 16MB·원본 엔진
# 2.4GB·VAmPI 187MB·postgres 16 bookworm 648MB·alpine 420MB). 빌드 캐시·회차 기록은 더 든다(바닥값).
MIN_DOCKER_FREE_BYTES = 8_250_000_000
SPLIT_OVERLAY = "deploy/eval/compose.split.yaml"
DOCKERFILES = ("Dockerfile", "deploy/product/Dockerfile", "deploy/eval/meter.Dockerfile", "deploy/eval/practice/vampi.Dockerfile")
COMPOSE_FILES = ("deploy/product/compose.yaml",)
COMMAND_TIMEOUT = 30
REGISTRY_TIMEOUT = 60
# 러너 설정에서 호스트에 여는 포트(공식 회차 절차 4절 runner.json): B 엔진 API·관측 포트, 분리 배치 제품·DNS 제어 포트.
CONFIG_PORTS = (("ports", "engine_api_host"), ("ports", "obs_host"), ("split", "product_port"), ("split", "dns_control_port"))

Run = Callable[[list[str], float], "tuple[int, str]"]


SPLIT_EXAMPLE = "docs/evaluation/runner.official-split.example.json"


@dataclass(frozen=True)
class Result:
    ok: bool
    name: str
    reason: str
    skipped: bool = False  # 설정이 모자라 점검하지 못함(FAIL이 아니라 다시 돌릴 안내)

    def line(self) -> str:
        return f"{'SKIP' if self.skipped else 'PASS' if self.ok else 'FAIL'} {self.name}: {self.reason}"


def run_command(args: list[str], timeout: float) -> tuple[int, str]:
    """환경 변수를 그대로 넘겨 읽기 명령을 돌린다(docker 소켓 설정을 따라야 한다). 실행 못 하면 (127, '')."""
    try:
        done = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout,
                              check=False, text=True)
    except (OSError, subprocess.SubprocessError):
        return 127, ""
    return done.returncode, done.stdout


def version_tuple(text: str) -> tuple[int, ...] | None:
    match = re.search(r"(\d+)\.(\d+)(?:\.(\d+))?", text or "")
    return None if match is None else tuple(int(part) for part in match.groups() if part is not None)


def at_least(found: tuple[int, ...] | None, minimum: tuple[int, ...]) -> bool:
    return found is not None and found[:len(minimum)] >= minimum


def check_version(name: str, run: Run, args: list[str], minimum: tuple[int, ...], measured: str) -> Result:
    code, out = run(args, COMMAND_TIMEOUT)
    found = version_tuple(out) if code == 0 else None
    if found is None:
        return Result(False, name, "명령을 실행하지 못했거나 판을 읽지 못함")
    shown = ".".join(map(str, found))
    if not at_least(found, minimum):
        return Result(False, name, f"{shown} < 최소 {'.'.join(map(str, minimum))} (개발 머신 실측 {measured})")
    return Result(True, name, f"{shown} (최소 {'.'.join(map(str, minimum))}, 개발 머신 실측 {measured})")


def check_docker(run: Run) -> list[Result]:
    code, out = run(["docker", "version", "--format", "{{.Server.Version}} {{.Client.Version}}"], COMMAND_TIMEOUT)
    parts = out.split() if code == 0 else []
    if len(parts) != 2:
        return [Result(False, "docker", "Docker 데몬에 닿지 못함(서버·클라이언트 판을 읽지 못함)")]
    results = []
    for role, value in zip(("docker_server", "docker_client"), parts):
        found = version_tuple(value)
        results.append(Result(at_least(found, MIN_DOCKER), role,
                              f"{value} (최소 {MIN_DOCKER[0]}.{MIN_DOCKER[1]}, 개발 머신 실측 29.1.3)"))
    code, out = run(["docker", "info", "--format", "{{.OperatingSystem}}"], COMMAND_TIMEOUT)
    desktop = code == 0 and "docker desktop" in out.lower()
    results.append(Result(code == 0 and not desktop, "docker_native",
                          "Docker Desktop 데몬임: 종료하고 WSL 통합을 끈 뒤 네이티브 dockerd를 쓴다(절차 3절 2번)" if desktop
                          else ("네이티브 dockerd" if code == 0 else "docker info를 읽지 못함")))
    return results


def check_go(run: Run, repo: Path) -> Result:
    try:
        wanted = re.search(r"^go (\S+)$", (repo / "go.mod").read_text(encoding="utf-8"), re.M)
    except OSError:
        wanted = None
    code, out = run(["go", "version"], COMMAND_TIMEOUT)
    found = re.search(r"go(\d+\.\d+(?:\.\d+)?)", out) if code == 0 else None
    if wanted is None or found is None:
        return Result(False, "go", "go.mod 판이나 go version을 읽지 못함")
    same = found.group(1) == wanted.group(1)
    return Result(same, "go", f"{found.group(1)} (go.mod {wanted.group(1)}와 같아야 계측기 빌드 판이 고정된다)")


def check_memory(read_text: Callable[[str], str | None]) -> Result:
    text = read_text("/proc/meminfo") or ""
    match = re.search(r"^MemTotal:\s+(\d+) kB$", text, re.M)
    if match is None:
        return Result(False, "memory", "/proc/meminfo를 읽지 못함")
    total = int(match.group(1))
    return Result(total >= MIN_MEM_KIB, "memory",
                  f"MemTotal {total / 1048576:.1f} GiB (개발 머신 실측 {MIN_MEM_KIB / 1048576:.1f} GiB에서 회차를 돌림)")


def check_disk(run: Run, free_bytes: Callable[[str], int | None]) -> Result:
    code, out = run(["docker", "info", "--format", "{{.DockerRootDir}}"], COMMAND_TIMEOUT)
    root = out.strip() if code == 0 else ""
    free = free_bytes(root) if root else None
    if free is None:
        return Result(False, "disk", "Docker 루트 디렉터리의 남은 공간을 읽지 못함")
    return Result(free >= MIN_DOCKER_FREE_BYTES, "disk",
                  f"{root} 남은 {free / 1e9:.1f} GB (바닥: 공식 회차 이미지 합계 {MIN_DOCKER_FREE_BYTES / 1e9:.1f} GB, 빌드 캐시·회차 기록 제외)")


POWERCFG = "/mnt/c/Windows/System32/powercfg.exe"
# (하위 그룹, 설정, 이름). 전원 연결(AC) 때 둘 다 '안 함'(0)이어야 측정 중 멈추지 않는다.
POWER_SETTINGS = (("SUB_SLEEP", "STANDBYIDLE", "절전"), ("SUB_SLEEP", "HIBERNATEIDLE", "최대 절전"))
_POWER_INDEX = {side: re.compile(side + r"[^\n]*?0x([0-9a-fA-F]{8})") for side in ("AC", "DC")}
PowerQuery = Callable[[str, str], "str | None"]


def powercfg_query(subgroup: str, setting: str) -> str | None:
    """WSL에서 Windows 전원 설정 하나를 읽는다(powercfg /query, 읽기만). Windows가 아니거나 못 읽으면 None. 출력은 로캘
    인코딩이라 바이트로 받아 깨진 글자는 바꾼다(AC·DC·0x 값은 ASCII)."""
    if not os.path.exists(POWERCFG):
        return None
    try:
        done = subprocess.run([POWERCFG, "/query", "SCHEME_CURRENT", subgroup, setting], stdin=subprocess.DEVNULL,
                              stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=COMMAND_TIMEOUT, check=False)
    except (OSError, subprocess.SubprocessError):
        return None
    return done.stdout.decode("utf-8", errors="replace") if done.returncode == 0 else None


def check_power(query: PowerQuery) -> Result:
    """측정 PC가 절전·최대 절전으로 멈추지 않는지(2026-11-20 사용자). 전원 연결(AC) 때 두 설정이 '안 함'이어야 한다. 배터리(DC)
    값은 사유에만 적는다(측정은 전원 연결로 한다). Windows 전원 설정을 읽을 수 없으면(WSL이 아님) 직접 확인하라고 SKIP이다."""
    found = {}
    for subgroup, setting, label in POWER_SETTINGS:
        text = query(subgroup, setting)
        if text is None:
            return Result(True, "power", "Windows 전원 설정을 읽을 수 없음 → 측정 PC의 절전·최대 절전을 직접 '안 함'으로 확인", skipped=True)
        values = {side: pattern.search(text) for side, pattern in _POWER_INDEX.items()}
        if values["AC"] is None:
            return Result(False, "power", f"{label} 설정 값을 읽지 못함")
        found[label] = {side: (int(m.group(1), 16) if m else None) for side, m in values.items()}
    bad = [label for label, v in found.items() if v["AC"] != 0]
    detail = ", ".join(f"{label} AC {v['AC']}초·DC {v['DC']}초" for label, v in found.items())
    if bad:
        return Result(False, "power", f"전원 연결 때 {'·'.join(bad)}이 켜져 있음({detail}) → 측정 동안 '안 함'(0)으로")
    return Result(True, "power", f"전원 연결 때 절전·최대 절전 안 함({detail}, 0 = 안 함). 측정은 전원 연결로")


def check_running_containers(run: Run) -> Result:
    code, out = run(["docker", "ps", "-q"], COMMAND_TIMEOUT)
    if code != 0:
        return Result(False, "containers", "docker ps를 읽지 못함")
    count = len([line for line in out.splitlines() if line.strip()])
    return Result(count == 0, "containers", f"실행 중 컨테이너 {count}개(0이어야 함, 절차 3절 3번)")


def config_ports(config: dict) -> dict[str, int]:
    ports = {}
    for block, key in CONFIG_PORTS:
        value = (config.get(block) or {}).get(key)
        if type(value) is int and 0 < value < 65536:
            ports[f"{block}.{key}"] = value
    return ports


def allowed_listeners(config: dict) -> frozenset[Listener]:
    allowed = set()
    for item in config.get("host_listen_allowlist") or []:
        if isinstance(item, dict) and isinstance(item.get("address"), str) and type(item.get("port")) is int:
            allowed.add(Listener(item["address"], item["port"]))
    return frozenset(allowed)


def check_listeners(config: dict, read_text: Callable[[str], str | None]) -> list[Result]:
    try:
        listeners = read_host_listeners(read_text)
    except Exception:
        return [Result(False, "host_listen", "/proc/net/tcp를 읽지 못함")]
    violations = check_host_listeners(listeners, allowed_listeners(config))
    outside = [v.removeprefix("host.listen.").removesuffix(": NOT_ALLOWED") for v in violations]
    results = [Result(not outside, "host_listen", "loopback 밖 리슨이 허용 목록 안에 있음" if not outside
                      else f"허용 목록 밖 리슨 {len(outside)}개(절차 3절 3번): " + ", ".join(outside))]
    if not isinstance(config.get("split"), dict):
        # 엔진 직접 배치 예시(runner.example.json)처럼 분리 배치 블록이 없으면 제품·DNS 포트를 알 수 없다. 환경 문제가 아니라
        # 설정 문제라 FAIL로 세지 않고 다시 돌릴 방법을 안내한다(2026-11-19 재상님 PC 점검).
        results.append(Result(True, "ports", f"분리 배치 설정 없음 → 공식 runner.json({SPLIT_EXAMPLE}를 채운 파일)으로 다시 실행",
                              skipped=True))
        return results
    ports = config_ports(config)
    if len(ports) != len(CONFIG_PORTS):
        missing = [f"{b}.{k}" for b, k in CONFIG_PORTS if f"{b}.{k}" not in ports]
        results.append(Result(False, "ports", "러너 설정에 없는 포트: " + ", ".join(missing)))
        return results
    busy = sorted(f"{name}={port}" for name, port in ports.items() if any(item.port == port for item in listeners))
    results.append(Result(not busy, "ports", "회차가 쓸 호스트 포트가 비어 있음(" + ", ".join(f"{n}={p}" for n, p in sorted(ports.items())) + ")"
                          if not busy else "이미 쓰는 포트: " + ", ".join(busy)))
    return results


def overlay_subnets(repo: Path) -> list[str]:
    text = (repo / SPLIT_OVERLAY).read_text(encoding="utf-8")
    return sorted(set(re.findall(r"subnet:\s*([0-9.]+/\d+)", text)))


def route_subnets(route_table: str) -> list[str]:
    """/proc/net/route의 IPv4 경로 대역(기본 경로 0.0.0.0/0 제외). 값은 리틀엔디언 16진수다."""
    found = set()
    for line in route_table.splitlines()[1:]:
        fields = line.split()
        if len(fields) < 8:
            continue
        try:
            dest = ipaddress.IPv4Address(int(fields[1], 16).to_bytes(4, "little"))
            mask = ipaddress.IPv4Address(int(fields[7], 16).to_bytes(4, "little"))
        except ValueError:
            continue
        network = ipaddress.ip_network(f"{dest}/{mask}", strict=False)
        if network.prefixlen > 0:
            found.add(str(network))
    return sorted(found)


def docker_subnets(run: Run) -> list[str] | None:
    code, out = run(["docker", "network", "ls", "-q"], COMMAND_TIMEOUT)
    if code != 0:
        return None
    ids = [line.strip() for line in out.splitlines() if line.strip()]
    if not ids:
        return []
    code, out = run(["docker", "network", "inspect", "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", *ids], COMMAND_TIMEOUT)
    if code != 0:
        return None
    return sorted({value for value in out.split() if "/" in value})


def check_subnets(run: Run, repo: Path, read_text: Callable[[str], str | None]) -> Result:
    try:
        planned = overlay_subnets(repo)
    except OSError:
        return Result(False, "subnets", f"{SPLIT_OVERLAY}를 읽지 못함")
    existing = docker_subnets(run)
    routes = read_text("/proc/net/route")
    if existing is None or routes is None:
        return Result(False, "subnets", "Docker 망 목록이나 /proc/net/route를 읽지 못함")
    overlaps = subnet_overlaps(planned, existing + route_subnets(routes))
    if overlaps:
        return Result(False, "subnets", "분리 배치 대역과 겹침: " + ", ".join(f"{o['planned']}↔{o['existing']}" for o in overlaps))
    return Result(True, "subnets", f"분리 배치 대역 {len(planned)}개가 Docker 망·호스트 경로와 겹치지 않음")


def base_images(repo: Path) -> list[str]:
    """Dockerfile의 FROM과 compose의 고정 image(변수 없는 것) 가운데 레지스트리에서 받을 이미지."""
    refs = set()
    stages = set()
    for name in DOCKERFILES:
        for line in (repo / name).read_text(encoding="utf-8").splitlines():
            match = re.match(r"\s*FROM\s+(?:--platform=\S+\s+)?(\S+)(?:\s+AS\s+(\S+))?", line, re.I)
            if match:
                refs.add(match.group(1))
                if match.group(2):
                    stages.add(match.group(2))
    for name in COMPOSE_FILES:
        refs.update(re.findall(r"^\s+image:\s*(\S+)\s*$", (repo / name).read_text(encoding="utf-8"), re.M))
    return sorted(ref for ref in refs if ref != "scratch" and ref not in stages and "$" not in ref)


def check_images(run: Run, repo: Path) -> list[Result]:
    code, _ = run(["docker", "buildx", "version"], COMMAND_TIMEOUT)
    results = [Result(code == 0, "buildx", "docker buildx 있음" if code == 0 else "docker buildx 없음(이미지 빌드 불가)")]
    try:
        refs = base_images(repo)
    except OSError:
        return results + [Result(False, "base_images", "Dockerfile·compose를 읽지 못함")]
    missing = []
    for ref in refs:
        code, _ = run(["docker", "image", "inspect", "--format", "{{.Id}}", ref], COMMAND_TIMEOUT)
        if code == 0:
            continue
        code, _ = run(["docker", "buildx", "imagetools", "inspect", ref], REGISTRY_TIMEOUT)
        if code != 0:
            missing.append(ref)
    results.append(Result(not missing, "base_images",
                          f"기본 이미지 {len(refs)}개가 로컬에 있거나 레지스트리에서 받을 수 있음" if not missing
                          else "받을 수 없는 기본 이미지: " + ", ".join(missing)))
    return results


def _read_text(path: str) -> str | None:
    try:
        with open(path, encoding="ascii", errors="replace") as source:
            return source.read()
    except OSError:
        return None


def _free_bytes(path: str) -> int | None:
    try:
        return shutil.disk_usage(path).free
    except OSError:
        return None


def run_checks(config: dict, repo: Path, run: Run = run_command, read_text: Callable[[str], str | None] = _read_text,
               free_bytes: Callable[[str], int | None] = _free_bytes, power_query: PowerQuery = powercfg_query) -> list[Result]:
    results = check_docker(run)
    results.append(check_version("compose", run, ["docker", "compose", "version", "--short"], MIN_COMPOSE, "2.40.3"))
    results.append(check_go(run, repo))
    results.append(check_version("python", lambda args, timeout: (0, sys.version), ["python3"], MIN_PYTHON, "3.12.3"))
    results.append(check_version("openssl", run, ["openssl", "version"], MIN_OPENSSL, "3.0.13"))
    code, _ = run(["git", "--version"], COMMAND_TIMEOUT)
    results.append(Result(code == 0, "git", "git 있음" if code == 0 else "git 없음"))
    results.append(check_memory(read_text))
    results.append(check_disk(run, free_bytes))
    results.append(check_power(power_query))
    results.append(check_running_containers(run))
    results.extend(check_listeners(config, read_text))
    results.append(check_subnets(run, repo, read_text))
    results.extend(check_images(run, repo))
    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="평가 머신 환경 점검(읽기만 함)")
    parser.add_argument("--config", required=True, help="러너 설정 runner.json(포트·리슨 허용 목록을 읽음)")
    parser.add_argument("--repo", default=".", help="동결 작업 트리(기본: 현재 디렉터리)")
    args = parser.parse_args(argv)
    try:
        config = json.loads(Path(args.config).read_text(encoding="utf-8"))
    except (OSError, ValueError):
        print("FAIL config: 러너 설정을 읽지 못함")
        return 1
    if not isinstance(config, dict):
        print("FAIL config: 러너 설정이 JSON 객체가 아님")
        return 1
    results = run_checks(config, Path(args.repo))
    for result in results:
        print(result.line())
    failed = [result for result in results if not result.ok]
    skipped = [result for result in results if result.skipped]
    passed = len(results) - len(failed) - len(skipped)
    if failed:
        print(f"FAIL 전체: {passed}/{len(results)} 통과" + (f", 건너뜀 {len(skipped)}" if skipped else ""))
        return 1
    if skipped:
        print(f"SKIP 전체: {passed}/{len(results)} 통과, 건너뜀 {len(skipped)} → 공식 runner.json으로 다시 실행")
        return 2
    print(f"PASS 전체: {passed}/{len(results)} 통과")
    return 0


if __name__ == "__main__":
    sys.exit(main())
