"""Select web validation without skipping the enclosing GitHub check."""

import argparse
import re
import subprocess
import sys

ROOT_INPUTS = {
    b"package.json",
    b"package-lock.json",
    b"npm-shrinkwrap.json",
    b"yarn.lock",
    b"pnpm-lock.yaml",
    b"pnpm-workspace.yaml",
    b"bun.lock",
    b"bun.lockb",
    b".npmrc",
    b".yarnrc.yml",
    b".nvmrc",
    b".node-version",
    b"vercel.json",
    b"vercel.toml",
    b"vercel.ts",
    b"turbo.json",
    b".github/workflows/ci.yml",
    b"scripts/ci/web-changed.py",
    b"scripts/ci/test_web_changed.py",
    # 제품 이미지의 UI 빌드·컨텍스트 변경도 기존 Web 검증에 연결한다.
    b"deploy/product/Dockerfile",
    b"deploy/product/Dockerfile.dockerignore",
}


def web_changed(base, head):
    # Missing history must cost a build, never hide an unvalidated frontend.
    if any(not re.fullmatch(r"[0-9a-fA-F]{40}", sha or "") or
           sha == "0" * 40 for sha in (base, head)):
        return True
    result = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", "-z", base, head, "--"],
        capture_output=True,
        check=False,
    )
    if result.returncode:
        print("Web change history unavailable; running validation.", file=sys.stderr)
        return True
    return any(
        path.startswith(b"web/") or path in ROOT_INPUTS
        for path in result.stdout.split(b"\0") if path
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="")
    parser.add_argument("--head", required=True)
    args = parser.parse_args()
    print("true" if web_changed(args.base, args.head) else "false")


if __name__ == "__main__":
    main()
