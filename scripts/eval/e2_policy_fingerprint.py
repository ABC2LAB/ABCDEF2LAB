"""검토한 정책 템플릿의 내용 지문을 회차 없이 계산한다. 공식 프로파일의 `e2.policy_fingerprint_sha256`에 넣을 값이다.

회차 값 자리에는 내용 지문에 들어가지 않는 필드의 자리값만 넣는다. 서비스 응답을 예상값으로 쓰지 않는다.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

# `python3 -I`로 실행하면 스크립트 디렉터리가 import 경로에 없다.
sys.path.insert(0, str(Path(__file__).resolve().parent))

from e2_prepare import CONTAINER_STATE_DIR, TIME_FORMAT, E2PrepareError, E2RunResources, approval_window, render_policy  # noqa: E402

_DIGEST_LINE = re.compile(rb"[0-9a-f]{64}\n\Z")
_PLACEHOLDER_WINDOW = timedelta(hours=1)
_PLACEHOLDER_DIGEST = "0" * 64
# 문서용 대역이라 실제 주소와 겹치지 않는다. 지문에는 들어가지 않는다.
_PLACEHOLDER_IP = "192.0.2.1"


def expected_fingerprint(template: Path, tool: Path, timeout: int, now: datetime | None = None) -> str:
    current = now or datetime.now(timezone.utc)
    limit = (current + 2 * _PLACEHOLDER_WINDOW).strftime(TIME_FORMAT)
    valid_from, valid_until = approval_window(current, int(_PLACEHOLDER_WINDOW.total_seconds()), limit)
    rendered = render_policy(template, E2RunResources("placeholder-run", valid_from, valid_until, CONTAINER_STATE_DIR, "/placeholder/helper",
                                                      _PLACEHOLDER_DIGEST, _PLACEHOLDER_DIGEST, _PLACEHOLDER_DIGEST, _PLACEHOLDER_IP))
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "policy.json"
        path.write_bytes(rendered)
        os.chmod(path, 0o600)
        try:
            result = subprocess.run([str(tool), "fingerprint", "--policy-file", str(path)], check=True, stdin=subprocess.DEVNULL, capture_output=True,
                                    env={}, timeout=timeout)
        except (OSError, ValueError, subprocess.SubprocessError):
            raise E2PrepareError("fingerprint_tool_failed") from None
    if not _DIGEST_LINE.fullmatch(result.stdout):
        raise E2PrepareError("fingerprint_output_invalid")
    return result.stdout.decode("ascii").strip()


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="E2 policy template content fingerprint")
    parser.add_argument("--template", type=Path, required=True)
    parser.add_argument("--tool", type=Path, required=True)
    parser.add_argument("--timeout-seconds", type=int, required=True)
    args = parser.parse_args(argv)
    try:
        print(expected_fingerprint(args.template, args.tool, args.timeout_seconds))
    except E2PrepareError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
