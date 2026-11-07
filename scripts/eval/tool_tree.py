"""엔진 이미지 두 개의 도구 트리를 대조한다(평가 프로토콜 §2 도구 동등성).

실행: python3 -I -B scripts/eval/tool_tree.py --baseline <이미지> --ours <이미지> --out <새 파일>

루트 Dockerfile은 베이스 이미지·apt 패키지·npm `@latest`를 고정하지 않는다. 그래서 Dockerfile이 같아도
빌드 시점이 다르면 도구가 달라지고, 이미지 ID 기록만으로는 이 차이가 드러나지 않는다.

대조 대상은 패키지 버전(dpkg·npm 전역·pip)과 도구 경로(TREE_ROOTS) 아래 파일 내용·링크다. 버전과 PATH 링크가 같아도
실제 코드가 다르면 드러나야 해서 패키지 파일까지 묶는다.
/app(엔진 바이너리·기동 스크립트·skills)은 양쪽이 원래 다른 코드라 넣지 않는다.
E② 격리 실행 안에서 보이는 트리는 제품 E²가 서버에 연결된 뒤 따로 잰다. 이 도구의 범위가 아니다.

종료 코드: 0 같음, 1 다름, 2 수집·입력 오류 또는 파일이 이미 있음.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shlex
import sys
from pathlib import Path
from typing import Callable, Mapping, Sequence

sys.path.insert(0, str(Path(__file__).resolve().parent))

from runner_docker import BoundedResult, DockerError, client_environment, run_docker_bounded  # noqa: E402

REPORT_FORMAT = "abc2lab.tool-tree-compare/v1"
SECTIONS = ("dpkg", "npm_global", "pip", "files", "links")
SECTION_MARK = "#section "
INSPECT_TIMEOUT_SECONDS = 30.0
COLLECT_TIMEOUT_SECONDS = 300.0
MAX_INSPECT_BYTES = 4 << 10
MAX_TREE_BYTES = 32 << 20
# 읽기만 하는 컨테이너다. npm이 캐시·로그를 쓸 곳만 내준다.
SCRATCH_TMPFS = "/tmp"

_IMAGE_ID = re.compile(r"^sha256:[0-9a-f]{64}$")
_FILE_LINE = re.compile(r"^([0-9a-f]{64})  (/\S.*)$")
_PAIR_LINE = re.compile(r"^([^=\s]+)=+([^=\s].*)$")

# 도구 코드가 실제로 있는 곳까지 내용으로 묶는다. PATH 링크가 가리키는 npm 패키지(/usr/lib/node_modules),
# 공유 라이브러리(/usr/lib), 파이썬(/usr/local/lib), Playwright 브라우저(/root/.cache)가 여기 든다.
# /var·/tmp는 실행 중 바뀌는 곳이고 /app은 양쪽이 원래 다르다.
TREE_ROOTS = ("/usr", "/etc", "/opt", "/root/.cache", "/root/.bashrc", "/root/.profile")
# Docker가 컨테이너마다 넣는 파일은 이미지 내용이 아니다. 계정 파일·개인 키는 도구 설정이 아니라서 읽지 않는다.
EXCLUDED_PATHS = ("/etc/hostname", "/etc/hosts", "/etc/resolv.conf",
                  "/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-", "/etc/ssl/private/*")


def collect_script(roots: Sequence[str] = TREE_ROOTS, excluded: Sequence[str] = EXCLUDED_PATHS) -> str:
    """수집 셸 스크립트. 없는 경로는 건너뛴다. 한쪽에만 있으면 항목 차이로 드러난다."""
    root_words = " ".join(shlex.quote(root) for root in roots)
    not_excluded = " ".join(f"! -path {shlex.quote(path)}" for path in excluded)
    return f"""set -eu
export LC_ALL=C
echo '{SECTION_MARK}dpkg'
dpkg-query -W -f '${{binary:Package}}=${{Version}}\\n'
echo '{SECTION_MARK}npm_global'
npm ls -g --depth=0 --json
echo '{SECTION_MARK}pip'
python3 -m pip list --format=freeze --disable-pip-version-check
set --
for root in {root_words}; do
  if [ -e "$root" ]; then set -- "$@" "$root"; fi
done
[ "$#" -gt 0 ]
echo '{SECTION_MARK}files'
find "$@" -type f {not_excluded} -exec sha256sum {{}} +
echo '{SECTION_MARK}links'
find "$@" -type l {not_excluded} -printf '%p\\t%l\\n'
"""


Tree = Mapping[str, Mapping[str, str]]
DockerRun = Callable[[Sequence[str], float, int], BoundedResult]


class ToolTreeError(ValueError):
    """수집 결과를 믿을 수 없다. '같음'으로 읽히지 않게 멈춘다."""


def _put(section: dict[str, str], key: str, value: str) -> None:
    if key in section:
        raise ToolTreeError(f"중복 항목: {key}")
    section[key] = value


def _parse_pairs(lines: Sequence[str]) -> dict[str, str]:
    section: dict[str, str] = {}
    for line in lines:
        match = _PAIR_LINE.match(line)
        if not match:
            raise ToolTreeError(f"형식 오류: {line[:80]}")
        _put(section, match.group(1), match.group(2))
    return section


def _parse_npm(lines: Sequence[str]) -> dict[str, str]:
    try:
        dependencies = json.loads("\n".join(lines))["dependencies"]
        return {name: info["version"] for name, info in dependencies.items()}
    except (ValueError, KeyError, TypeError, AttributeError):
        raise ToolTreeError("npm 전역 목록 형식 오류") from None


def _parse_files(lines: Sequence[str]) -> dict[str, str]:
    section: dict[str, str] = {}
    for line in lines:
        match = _FILE_LINE.match(line)
        if not match:
            raise ToolTreeError(f"형식 오류: {line[:80]}")
        _put(section, match.group(2), match.group(1))
    return section


def _parse_links(lines: Sequence[str]) -> dict[str, str]:
    section: dict[str, str] = {}
    for line in lines:
        path, _, target = line.partition("\t")
        if not path.startswith("/") or not target or "\t" in target:
            raise ToolTreeError(f"형식 오류: {line[:80]}")
        _put(section, path, target)
    return section


_PARSERS = {"dpkg": _parse_pairs, "npm_global": _parse_npm, "pip": _parse_pairs,
            "files": _parse_files, "links": _parse_links}


def parse_tree(text: str) -> dict[str, dict[str, str]]:
    """수집 출력을 절별 사전으로 만든다. 절이 빠지거나 비면 도구가 없는 것이라 거부한다."""
    blocks: list[tuple[str, list[str]]] = []
    for line in text.splitlines():
        if line.startswith(SECTION_MARK):
            blocks.append((line[len(SECTION_MARK):], []))
        elif not blocks:
            raise ToolTreeError(f"절 밖의 출력: {line[:80]}")
        else:
            blocks[-1][1].append(line)
    if tuple(name for name, _ in blocks) != SECTIONS:
        raise ToolTreeError(f"절 구성이 다름: {[name for name, _ in blocks]}")
    tree = {name: _PARSERS[name](lines) for name, lines in blocks}
    empty = [name for name in SECTIONS if not tree[name]]
    if empty:
        raise ToolTreeError(f"빈 절: {empty}")
    return tree


def tree_sha256(tree: Tree) -> str:
    canonical = json.dumps(tree, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def compare_trees(baseline: Tree, ours: Tree) -> dict[str, list[dict]]:
    """절별 차이. 한쪽에만 있는 항목은 다른 쪽 값이 null이다."""
    differences: dict[str, list[dict]] = {}
    for name in SECTIONS:
        rows = [{"key": key, "baseline": baseline[name].get(key), "ours": ours[name].get(key)}
                for key in sorted(baseline[name].keys() | ours[name].keys())
                if baseline[name].get(key) != ours[name].get(key)]
        if rows:
            differences[name] = rows
    return differences


def resolve_image_id(reference: str, run: DockerRun) -> str:
    result = run(["image", "inspect", "--format", "{{.Id}}", reference], INSPECT_TIMEOUT_SECONDS, MAX_INSPECT_BYTES)
    image_id = result.stdout.decode("ascii", "replace").strip()
    if result.returncode != 0 or not _IMAGE_ID.match(image_id):
        raise ToolTreeError(f"이미지 ID를 못 얻음: {reference}")
    return image_id


def collect_tree(image_id: str, run: DockerRun) -> dict[str, dict[str, str]]:
    """망 없는 1회용 컨테이너에서 수집한다. 내려받기로 다른 이미지를 재지 않게 내용 ID만 받는다."""
    if not _IMAGE_ID.match(image_id):
        raise ToolTreeError(f"내용 ID가 아님: {image_id[:80]}")
    args = ["run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--tmpfs", SCRATCH_TMPFS,
            "--env", f"npm_config_cache={SCRATCH_TMPFS}", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true", "--entrypoint", "sh", image_id, "-c", collect_script()]
    result = run(args, COLLECT_TIMEOUT_SECONDS, MAX_TREE_BYTES)
    if result.returncode != 0:
        raise ToolTreeError(f"수집 실패(exit {result.returncode}): {image_id}")
    try:
        return parse_tree(result.stdout.decode("utf-8"))
    except UnicodeDecodeError:
        raise ToolTreeError(f"수집 출력이 UTF-8이 아님: {image_id}") from None


def build_report(baseline_ref: str, ours_ref: str, run: DockerRun) -> dict:
    baseline_id, ours_id = resolve_image_id(baseline_ref, run), resolve_image_id(ours_ref, run)
    if baseline_id == ours_id:
        raise ToolTreeError("두 이미지가 같은 이미지다. 대조가 아무것도 증명하지 못한다")
    baseline, ours = collect_tree(baseline_id, run), collect_tree(ours_id, run)
    differences = compare_trees(baseline, ours)
    return {
        "format": REPORT_FORMAT,
        "same": not differences,
        "baseline": {"image_id": baseline_id, "tree_sha256": tree_sha256(baseline)},
        "ours": {"image_id": ours_id, "tree_sha256": tree_sha256(ours)},
        "differences": differences,
    }


def _docker_runner() -> DockerRun:
    env = client_environment(os.environ, {})

    def run(args: Sequence[str], timeout: float, max_stdout: int) -> BoundedResult:
        return run_docker_bounded(args, env, timeout, max_stdout)

    return run


def _write_report(path: str, report: dict) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as out:
        json.dump(report, out, ensure_ascii=False, indent=1)
        out.write("\n")


def _print_summary(report: dict) -> None:
    if report["same"]:
        print(f"도구 트리 같음: {report['ours']['tree_sha256']}")
        return
    counts = ", ".join(f"{name} {len(rows)}" for name, rows in report["differences"].items())
    print(f"도구 트리 다름: {counts}")
    for name in ("dpkg", "npm_global", "pip"):
        for row in report["differences"].get(name, ()):
            print(f"  {name} {row['key']}: {row['baseline']} → {row['ours']}")


def main(argv: list[str] | None = None, run: DockerRun | None = None) -> int:
    parser = argparse.ArgumentParser(description="엔진 이미지 두 개의 도구 트리 대조")
    parser.add_argument("--baseline", required=True, help="baseline 엔진 이미지(태그 또는 내용 ID)")
    parser.add_argument("--ours", required=True, help="개선 후 엔진 이미지(태그 또는 내용 ID)")
    parser.add_argument("--out", required=True, help="대조 결과를 쓸 새 파일(있으면 거부)")
    args = parser.parse_args(argv)
    if os.path.lexists(args.out):
        print(f"{args.out}: 이미 있음 — 덮어쓰지 않는다", file=sys.stderr)
        return 2
    try:
        report = build_report(args.baseline, args.ours, run if run is not None else _docker_runner())
    except (ToolTreeError, DockerError) as err:
        print(f"도구 트리 대조 실패: {err}", file=sys.stderr)
        return 2
    try:
        _write_report(args.out, report)
    except FileExistsError:
        print(f"{args.out}: 이미 있음 — 덮어쓰지 않는다", file=sys.stderr)
        return 2
    _print_summary(report)
    return 0 if report["same"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
