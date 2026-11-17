"""탐색 분리 배치의 도구 면 측정(결정안 §4, 분기점 3의 (c) 정적 측정). 모델이 쓸 수 있는 도구를 쪽별로 잰다.

실행: python3 -I -B scripts/eval/tool_surface.py --baseline <원본 엔진 이미지> --ours <제품 이미지>
      --ours-browser <제품 탐색 브라우저 이미지> --out <새 파일>

- CLI: B는 엔진 이미지의 PATH 전체(원본 Worker Bash는 샌드박스 없이 엔진 컨테이너에서 돈다), O는 제품 이미지 중 E² 샌드박스가
  읽기 전용으로 붙이는 경로만(`e2sandbox/helper_linux.go` `sandboxRuntimePaths`·샌드박스 PATH). 이름마다 셸 찾기 순서로 처음
  실행되는 파일의 내용 SHA를 남긴다. 샌드박스에서는 대상 파일이나 스크립트 해석기가 붙지 않은 경로에 있으면 쓸 수 없다.
- MCP: 같은 이미지를 망 없이 1회용으로 띄운 stdio `tools/list`. **원 목록**(B·O 공통 지문)과 **모델에 노출되는 목록**을 둘 다
  남긴다. O는 제품 허용 목록(`server/mcp_tool_policy.go`) 적용 뒤, B는 원본이 브라우저 MCP를 꺼진 채 시드하고(`db/db.go`)
  러너도 켜지 않으므로 빈 목록이다.

정적 측정의 한계: 실행 파일·스크립트 해석기의 경로만 본다. 공유 라이브러리·모듈 import가 샌드박스에서 풀리는지는 실제
회차에서 한 번 대조한다(결정 (c)). 원 출력은 쓰지 않고 지문·이름·사유만 남긴다. 종료 코드: 0 측정함, 2 입력·측정 오류.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import posixpath
import re
import selectors
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable, Sequence

sys.path.insert(0, str(Path(__file__).resolve().parent))

from runner_docker import BoundedResult, DockerError, client_environment, run_docker_bounded  # noqa: E402
from tool_tree import ToolTreeError, resolve_image_id  # noqa: E402

FORMAT = "abc2lab.tool-surface/v1"
# E² 샌드박스 Bash 환경과 붙이는 경로(e2sandbox/helper_linux.go). Go와 같은지는 시험이 원문으로 대조한다.
SANDBOX_PATH = "/usr/local/bin:/usr/sbin:/usr/bin:/bin"
SANDBOX_FIXED = ("/usr/bin", "/usr/sbin", "/bin", "/usr/local/bin", "/usr/lib/{triplet}", "/lib/{triplet}", "/usr/lib64", "/lib64",
                 "/usr/lib/cargo/bin/coreutils", "/etc/alternatives", "/etc/ld.so.cache")
SANDBOX_DIR_GLOBS = ("/usr/lib/python3*", "/usr/local/lib/python3*")
SANDBOX_FILE_GLOBS = ("/usr/local/lib/libpython3*",)
TRIPLETS = {"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}
# 탐색 계약 §5.1·server/mcp_tool_policy.go playwrightMCPAllowedTools(제품 핀 @playwright/mcp@0.0.84). 시험이 Go와 대조한다.
MCP_ALLOWED = frozenset({
    "browser_navigate", "browser_navigate_back", "browser_tabs", "browser_click", "browser_hover", "browser_drag", "browser_type",
    "browser_fill_form", "browser_select_option", "browser_press_key", "browser_handle_dialog", "browser_wait_for",
    "browser_snapshot", "browser_find", "browser_take_screenshot", "browser_resize", "browser_emulate_media",
    "browser_console_messages", "browser_network_requests", "browser_evaluate", "browser_close"})
# 원본 시드(db/db.go, 원본 e53a79f와 같음): stdio `npx @playwright/mcp --headless`, enabled=false.
BASELINE_MCP_ARGS = ("--headless",)
BASELINE_MCP_EXPOSED_BASIS = "original_seed_disabled"
# 제품 explore-mcp 명령에서 전송·연결 칸(--port 등)을 뺀 기능 칸. 렌더 시험(scripts/ci)이 제품 compose와 대조한다.
OURS_MCP_ARGS = ("--headless", "--isolated", "--no-webmcp", "--browser", "chromium")
# 전송·연결 칸(값 하나를 받는다). 이 칸을 뺀 나머지가 도구 목록에 영향을 줄 수 있는 기능 칸이다. 모르는 칸은 남겨 렌더
# 시험이 OURS_MCP_ARGS와 어긋나게 한다(제품이 새 기능 칸을 더하면 측정 인자도 같이 바뀌어야 한다).
MCP_TRANSPORT_FLAGS = ("--proxy-server", "--cdp-endpoint", "--output-dir", "--port", "--host", "--allowed-hosts")
MCP_BIN = "playwright-mcp"
MCP_PROTOCOL = "2025-06-18"
MCP_TIMEOUT_SECONDS = 120.0
MAX_MCP_BYTES = 4 << 20
MAX_MCP_PAGES = 16
COLLECT_TIMEOUT_SECONDS = 300.0
MAX_COLLECT_BYTES = 16 << 20
INSPECT_TIMEOUT_SECONDS = 30.0
MAX_INSPECT_BYTES = 4 << 10
SCRATCH_TMPFS = "/tmp"

_IMAGE_ID = re.compile(r"^sha256:[0-9a-f]{64}$")
_HEX = re.compile(r"^[0-9a-f]{64}$")
_VERSION = re.compile(r"^Version ([0-9][0-9A-Za-z.+-]{0,63})$")

DockerRun = Callable[[Sequence[str], float, int], BoundedResult]


class ToolSurfaceError(RuntimeError):
    """측정을 끝낼 수 없음. 고정 사유만 담는다(이미지 출력·도구 원문은 싣지 않는다)."""


# 컨테이너 안에서 python3 -I로 돈다. PATH 디렉터리 순서대로 실행 가능한 파일을 모두 적고(이름이 겹쳐도 다 적는다:
# 샌드박스에서는 앞 파일이 안 보이면 셸이 다음 디렉터리로 넘어간다), 샌드박스 모드면 붙는 경로의 실제 위치를 함께 낸다.
COLLECTOR = r'''
import glob, hashlib, json, os, sys
cfg = json.loads(sys.argv[1])
def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()
def shebang(path):
    with open(path, "rb") as f:
        head = f.read(256)
    if not head.startswith(b"#!"):
        return None
    return head[2:].split(b"\n", 1)[0].decode("utf-8", "replace").split()
mounts = None
if cfg["sandbox"] is not None:
    s = cfg["sandbox"]
    found = list(s["fixed"])
    for pattern in s["dir_globs"]:
        found += [p for p in sorted(glob.glob(pattern)) if os.path.isdir(p)]
    for pattern in s["file_globs"]:
        found += sorted(glob.glob(pattern))
    mounts = sorted({os.path.realpath(p) for p in found if os.path.exists(p)})
entries = []
for directory in cfg["path"].split(":"):
    if not directory or not os.path.isdir(directory):
        continue
    for name in sorted(os.listdir(directory)):
        path = os.path.join(directory, name)
        if not (os.path.isfile(path) and os.access(path, os.X_OK)):
            continue
        target = os.path.realpath(path)
        bang = shebang(target)
        interpreter = None
        if bang and bang[0].startswith("/") and os.path.exists(bang[0]):
            interpreter = os.path.realpath(bang[0])
        entries.append({"name": name, "dir": directory, "target": target, "sha256": digest(target), "shebang": bang,
                        "interpreter_target": interpreter})
json.dump({"mounts": mounts, "entries": entries}, sys.stdout, sort_keys=True)
'''


def canonical_sha256(value) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")).hexdigest()


def sandbox_spec(architecture: str) -> dict:
    triplet = TRIPLETS.get(architecture)
    if triplet is None:
        raise ToolSurfaceError("architecture_unsupported")
    return {"fixed": [path.format(triplet=triplet) for path in SANDBOX_FIXED], "dir_globs": list(SANDBOX_DIR_GLOBS),
            "file_globs": list(SANDBOX_FILE_GLOBS)}


def _visible(path: str | None, mounts: Sequence[str]) -> bool:
    return path is not None and any(path == mount or path.startswith(mount.rstrip("/") + "/") for mount in mounts)


def classify(collected: dict, path: str) -> dict:
    """수집 결과를 셸 찾기 순서로 이름마다 하나로 줄인다. 샌드박스(mounts 있음)면 보이는 파일만 이름이 되고, 안 보이는
    이름은 사유와 함께 따로 둔다. 스크립트는 해석기도 같은 조건으로 풀려야 한다(`env X`는 X를 같은 PATH에서 찾는다)."""
    if not isinstance(collected, dict) or not isinstance(collected.get("entries"), list):
        raise ToolSurfaceError("collect_output_invalid")
    mounts = collected.get("mounts")
    order = [d for d in path.split(":") if d]
    by_name: dict[str, list[dict]] = {}
    for entry in collected["entries"]:
        if (not isinstance(entry, dict) or not isinstance(entry.get("name"), str) or entry.get("dir") not in order
                or not isinstance(entry.get("target"), str) or not _HEX.fullmatch(str(entry.get("sha256")))):
            raise ToolSurfaceError("collect_output_invalid")
        by_name.setdefault(entry["name"], []).append(entry)
    for candidates in by_name.values():
        candidates.sort(key=lambda e: order.index(e["dir"]))
    resolved: dict[str, dict | None] = {}
    reasons: dict[str, str] = {}

    def usable(entry: dict, depth: int) -> str | None:
        """None이면 쓸 수 있다. 아니면 사유."""
        if mounts is not None and not _visible(entry["target"], mounts):
            return "target_outside_sandbox"
        bang = entry.get("shebang")
        if not bang:
            return None
        if posixpath.basename(bang[0]) == "env":
            command = next((token for token in bang[1:] if not token.startswith("-")), None)
            if command is None or depth > 4 or lookup(command, depth + 1) is None:
                return "interpreter_unavailable"
            return None
        if entry.get("interpreter_target") is None:
            return "interpreter_missing"
        if mounts is not None and not _visible(entry["interpreter_target"], mounts):
            return "interpreter_unavailable"
        return None

    def lookup(name: str, depth: int = 0) -> dict | None:
        if name in resolved:
            return resolved[name]
        resolved[name] = None  # 해석기 고리 막기
        first_reason = None
        for entry in by_name.get(name, ()):
            reason = usable(entry, depth)
            if reason is None:
                resolved[name] = entry
                return entry
            first_reason = first_reason or reason
        if first_reason is not None:
            reasons[name] = first_reason
        return None

    commands = {}
    for name in sorted(by_name):
        entry = lookup(name)
        if entry is not None:
            commands[name] = {"dir": entry["dir"], "sha256": entry["sha256"]}
    hidden = {name: reasons[name] for name in sorted(reasons) if name not in commands}
    out = {"path": path, "count": len(commands), "sha256": canonical_sha256({k: v["sha256"] for k, v in commands.items()}),
           "commands": commands}
    if mounts is not None:
        out["hidden"] = hidden
    return out


def image_architecture(image_id: str, run: DockerRun) -> str:
    result = run(["image", "inspect", "--format", "{{.Architecture}}", image_id], INSPECT_TIMEOUT_SECONDS, MAX_INSPECT_BYTES)
    value = result.stdout.decode("ascii", "replace").strip()
    if result.returncode != 0 or not value:
        raise ToolSurfaceError("image_inspect_failed")
    return value


def image_path(image_id: str, run: DockerRun) -> str:
    """원본 Worker Bash는 엔진 컨테이너 환경 그대로다. 그 PATH는 이미지 설정의 PATH다."""
    result = run(["image", "inspect", "--format", "{{json .Config.Env}}", image_id], INSPECT_TIMEOUT_SECONDS, MAX_INSPECT_BYTES)
    try:
        env = json.loads(result.stdout) if result.returncode == 0 else None
    except ValueError:
        env = None
    values = [item[len("PATH="):] for item in env or () if isinstance(item, str) and item.startswith("PATH=")]
    if len(values) != 1 or not values[0]:
        raise ToolSurfaceError("image_path_missing")
    return values[0]


def collect_cli(image_id: str, path: str, sandbox: dict | None, run: DockerRun) -> dict:
    if not _IMAGE_ID.fullmatch(image_id):
        raise ToolSurfaceError("image_id_invalid")
    config = json.dumps({"path": path, "sandbox": sandbox}, sort_keys=True)
    args = ["run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--tmpfs", SCRATCH_TMPFS, "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true", "--entrypoint", "python3", image_id, "-I", "-c", COLLECTOR, config]
    result = run(args, COLLECT_TIMEOUT_SECONDS, MAX_COLLECT_BYTES)
    if result.returncode != 0:
        raise ToolSurfaceError("cli_collect_failed")
    try:
        collected = json.loads(result.stdout)
    except ValueError:
        raise ToolSurfaceError("collect_output_invalid") from None
    if (sandbox is None) != (collected.get("mounts") is None if isinstance(collected, dict) else True):
        raise ToolSurfaceError("collect_output_invalid")
    return classify(collected, path)


def mcp_version(image_id: str, run: DockerRun) -> str:
    args = ["run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--tmpfs", SCRATCH_TMPFS, "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true", "--entrypoint", MCP_BIN, image_id, "--version"]
    result = run(args, INSPECT_TIMEOUT_SECONDS * 4, MAX_INSPECT_BYTES)
    match = _VERSION.fullmatch(result.stdout.decode("utf-8", "replace").strip())
    if result.returncode != 0 or match is None:
        raise ToolSurfaceError("mcp_version_failed")
    return match.group(1)


class _Session:
    """MCP stdio 세션 하나. 줄 단위 JSON-RPC를 시한·바이트 상한 안에서 주고받는다."""

    def __init__(self, process, timeout: float):
        self.process, self.deadline, self.buffer, self.read = process, time.monotonic() + timeout, b"", 0
        self.selector = selectors.DefaultSelector()
        self.selector.register(process.stdout, selectors.EVENT_READ)

    def send(self, message: dict) -> None:
        try:
            self.process.stdin.write(json.dumps(message, separators=(",", ":")).encode("utf-8") + b"\n")
            self.process.stdin.flush()
        except OSError:
            raise ToolSurfaceError("mcp_session_closed") from None

    def receive(self, request_id: int) -> dict:
        while True:
            while b"\n" in self.buffer:
                line, self.buffer = self.buffer.split(b"\n", 1)
                if not line.strip():
                    continue
                try:
                    message = json.loads(line)
                except ValueError:
                    raise ToolSurfaceError("mcp_output_invalid") from None
                if isinstance(message, dict) and message.get("id") == request_id:
                    if "error" in message or not isinstance(message.get("result"), dict):
                        raise ToolSurfaceError("mcp_request_failed")
                    return message["result"]
            remaining = self.deadline - time.monotonic()
            if remaining <= 0 or not self.selector.select(remaining):
                raise ToolSurfaceError("mcp_timeout")
            chunk = os.read(self.process.stdout.fileno(), 65536)
            if not chunk:
                raise ToolSurfaceError("mcp_session_closed")
            self.read += len(chunk)
            if self.read > MAX_MCP_BYTES:
                raise ToolSurfaceError("mcp_output_too_large")
            self.buffer += chunk

    def close(self) -> None:
        self.selector.close()
        try:
            self.process.stdin.close()
        except OSError:
            pass
        try:
            self.process.wait(timeout=30)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()


def mcp_tools(image_id: str, args: Sequence[str], popen: Callable = subprocess.Popen, env: dict | None = None,
              timeout: float = MCP_TIMEOUT_SECONDS) -> dict:
    """망 없는 1회용 컨테이너에서 stdio MCP를 띄워 initialize → tools/list(쪽 넘김까지)만 한다. 도구는 실행하지 않는다."""
    if not _IMAGE_ID.fullmatch(image_id):
        raise ToolSurfaceError("image_id_invalid")
    command = ["docker", "run", "--rm", "-i", "--pull", "never", "--network", "none", "--read-only", "--tmpfs", SCRATCH_TMPFS,
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--entrypoint", MCP_BIN, image_id, *args]
    try:
        process = popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                        env=None if env is None else dict(env))
    except OSError:
        raise ToolSurfaceError("docker_unavailable") from None
    session = _Session(process, timeout)
    try:
        session.send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": MCP_PROTOCOL, "capabilities": {}, "clientInfo": {"name": "abc2lab-tool-surface", "version": "1"}}})
        info = session.receive(1)
        session.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        tools, cursor = [], None
        for page in range(MAX_MCP_PAGES):
            request_id = 2 + page
            params = {} if cursor is None else {"cursor": cursor}
            session.send({"jsonrpc": "2.0", "id": request_id, "method": "tools/list", "params": params})
            result = session.receive(request_id)
            if not isinstance(result.get("tools"), list):
                raise ToolSurfaceError("mcp_output_invalid")
            tools += result["tools"]
            cursor = result.get("nextCursor")
            if not cursor:
                break
        else:
            raise ToolSurfaceError("mcp_too_many_pages")
    finally:
        session.close()
    server = info.get("serverInfo") if isinstance(info.get("serverInfo"), dict) else {}
    return {"server_version": server.get("version") if isinstance(server.get("version"), str) else None, "tools": tools}


def mcp_capability_args(command: Sequence[str]) -> list[str]:
    """제품 explore-mcp 명령에서 전송·연결 칸과 그 값을 뺀다."""
    out, skip = [], False
    for item in command:
        if skip:
            skip = False
        elif item in MCP_TRANSPORT_FLAGS:
            skip = True
        else:
            out.append(item)
    if skip:
        raise ToolSurfaceError("mcp_command_invalid")
    return out


def tool_digest(tools: Sequence[dict]) -> dict:
    """도구 정의(이름·설명·입력 스키마·주석)를 이름 순으로 묶은 지문. 이름이 겹치거나 빠지면 거부한다."""
    names = [tool.get("name") if isinstance(tool, dict) else None for tool in tools]
    if any(not isinstance(name, str) or not name for name in names) or len(set(names)) != len(names):
        raise ToolSurfaceError("mcp_output_invalid")
    ordered = sorted(tools, key=lambda tool: tool["name"])
    return {"names": [tool["name"] for tool in ordered], "sha256": canonical_sha256(ordered)}


def mcp_side(image_id: str, args: Sequence[str], version: str, listed: dict, exposed_names: frozenset | None,
             exposed_basis: str) -> dict:
    raw = tool_digest(listed["tools"])
    if exposed_names is None:
        exposed_tools = []
    else:
        exposed_tools = [tool for tool in listed["tools"] if tool["name"] in exposed_names]
    exposed = tool_digest(exposed_tools)
    return {"image_id": image_id, "args": list(args), "package_version": version, "server_version": listed["server_version"],
            "raw": raw, "exposed": exposed, "exposed_basis": exposed_basis}


def compare_cli(baseline: dict, ours: dict) -> dict:
    b, o = baseline["commands"], ours["commands"]
    return {"baseline_only": sorted(set(b) - set(o)), "ours_only": sorted(set(o) - set(b)),
            "content_differs": sorted(name for name in set(b) & set(o) if b[name]["sha256"] != o[name]["sha256"])}


def build_report(baseline_ref: str, ours_ref: str, browser_ref: str, run: DockerRun, popen: Callable, env: dict) -> dict:
    try:
        baseline_id, ours_id, browser_id = (resolve_image_id(ref, run) for ref in (baseline_ref, ours_ref, browser_ref))
    except ToolTreeError:
        raise ToolSurfaceError("image_inspect_failed") from None
    baseline_path = image_path(baseline_id, run)
    baseline_cli = collect_cli(baseline_id, baseline_path, None, run)
    ours_cli = collect_cli(ours_id, SANDBOX_PATH, sandbox_spec(image_architecture(ours_id, run)), run)
    baseline_mcp = mcp_side(baseline_id, BASELINE_MCP_ARGS, mcp_version(baseline_id, run),
                            mcp_tools(baseline_id, BASELINE_MCP_ARGS, popen, env), None, BASELINE_MCP_EXPOSED_BASIS)
    ours_mcp = mcp_side(browser_id, OURS_MCP_ARGS, mcp_version(browser_id, run), mcp_tools(browser_id, OURS_MCP_ARGS, popen, env),
                        MCP_ALLOWED, "product_allowlist")
    return {
        "format": FORMAT,
        "baseline": {"image_id": baseline_id, "cli": baseline_cli, "mcp": baseline_mcp},
        "ours": {"image_id": ours_id, "cli": ours_cli, "mcp": ours_mcp},
        "common": {"mcp_raw_same": baseline_mcp["raw"]["sha256"] == ours_mcp["raw"]["sha256"],
                   "mcp_version_same": baseline_mcp["package_version"] == ours_mcp["package_version"]},
        "cli_differences": compare_cli(baseline_cli, ours_cli),
    }


def _docker_runner(env: dict) -> DockerRun:
    def run(args: Sequence[str], timeout: float, max_stdout: int) -> BoundedResult:
        return run_docker_bounded(args, env, timeout, max_stdout)
    return run


def _write_report(path: str, report: dict) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as out:
        json.dump(report, out, ensure_ascii=False, indent=1, sort_keys=True)
        out.write("\n")


def _print_summary(report: dict) -> None:
    b, o = report["baseline"], report["ours"]
    print(f"CLI: 원본 {b['cli']['count']}개, 개선 후 샌드박스 {o['cli']['count']}개(보이지 않음 {len(o['cli']['hidden'])}개)")
    diff = report["cli_differences"]
    print(f"  원본에만: {len(diff['baseline_only'])}개 {' '.join(diff['baseline_only'][:40])}")
    print(f"  개선 후에만: {len(diff['ours_only'])}개, 내용 다름: {len(diff['content_differs'])}개")
    print(f"MCP: 원 목록 같음={report['common']['mcp_raw_same']} 판 같음={report['common']['mcp_version_same']} "
          f"원본 {len(b['mcp']['raw']['names'])}·노출 {len(b['mcp']['exposed']['names'])}, "
          f"개선 후 {len(o['mcp']['raw']['names'])}·노출 {len(o['mcp']['exposed']['names'])}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="탐색 분리 배치 도구 면 측정(정적)")
    parser.add_argument("--baseline", required=True, help="원본 엔진 이미지")
    parser.add_argument("--ours", required=True, help="제품 이미지(E² 샌드박스가 붙는 이미지)")
    parser.add_argument("--ours-browser", required=True, help="제품 탐색 브라우저 이미지(explore-mcp)")
    parser.add_argument("--out", required=True, help="새로 만들 보고서 파일(있으면 거부)")
    args = parser.parse_args(argv)
    if os.path.lexists(args.out):
        print("out: EXISTS", file=sys.stderr)
        return 2
    env = client_environment(os.environ, {})
    try:
        report = build_report(args.baseline, args.ours, args.ours_browser, _docker_runner(env), subprocess.Popen, env)
    except (ToolSurfaceError, DockerError) as exc:
        print(str(exc), file=sys.stderr)
        return 2
    _write_report(args.out, report)
    _print_summary(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
