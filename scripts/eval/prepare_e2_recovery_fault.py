#!/usr/bin/env python3
"""Create a test-only Go overlay that pauses just before recovery record link.

Run in a disposable integration worktree. Build the recovery binary with the
generated Go overlay, then layer it on that worktree's product image. No fault
switch or delay is added to the product binary or committed Go source.
"""

import argparse
import json
from pathlib import Path


MARKER = "TEST_FAULT_BEFORE_LINK"
NEEDLE = "\tif os.Link(tmp, filepath.Join(dir, name)) != nil {\n"
INJECTION = (
    '\tif name == "operator-recovery.json" {\n'
    f'\t\t_, _ = os.Stderr.WriteString("{MARKER}\\n")\n'
    "\t\tselect {}\n"
    "\t}\n"
)


def prepare(root: Path, output: Path) -> None:
    source = (root / "e2host" / "recovery.go").resolve(strict=True)
    output = output.resolve()
    if output == root or root in output.parents or output.exists():
        raise ValueError("output must be a new directory outside the worktree")
    original = source.read_text(encoding="utf-8")
    if original.count(NEEDLE) != 1:
        raise ValueError("recovery link point changed; review the harness")
    output.mkdir(mode=0o700, parents=False)
    patched = output / "recovery-fault.go"
    patched.write_text(original.replace(NEEDLE, INJECTION + NEEDLE), encoding="utf-8")
    (output / "overlay.json").write_text(
        json.dumps({"Replace": {str(source): str(patched)}}), encoding="utf-8"
    )
    (output / "Dockerfile").write_text(
        'ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n'
        'COPY --chmod=755 first-controlled-diagnosis /app/first-controlled-diagnosis\n'
        'LABEL org.abc2lab.test-fault="e2-recovery-before-link"\n',
        encoding="utf-8",
    )


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path, help="New temporary directory outside the worktree")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    prepare(root, args.output)


if __name__ == "__main__":
    main()
