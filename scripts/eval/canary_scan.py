"""회차 디렉터리에 합성 토큰 원문이 남았는지 본다(프로토콜 §2 원문 통과 모드의 검증 기준).

실행: python3 -I -B scripts/eval/canary_scan.py <회차 디렉터리> --canary <합성 값>

- 파일 바이트에서 값을 찾는다. 출력은 건수와 걸린 파일의 상대 경로뿐이고 값이나 파일 내용은 내지 않는다.
- 심볼릭 링크는 따라가지 않고, 읽지 못한 파일이 있으면 통과로 치지 않는다.
- 값은 합성이어야 한다. 실제 토큰을 넣지 않는다.

종료 코드: 0 없음, 1 있음, 2 입력 오류·읽기 실패.
"""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

MIN_CANARY_LENGTH = 16  # 짧으면 우연히 겹쳐 뜻 없이 걸린다


def scan(root: Path, canary: bytes) -> tuple[int, list[str], list[str]]:
    """(읽은 파일 수, 걸린 상대 경로, 읽지 못한 상대 경로)."""
    files, hits, unreadable = 0, [], []
    for base, directories, names in os.walk(root, followlinks=False):
        directories.sort()
        for name in sorted(names):
            path = Path(base, name)
            relative = str(path.relative_to(root))
            if path.is_symlink():
                continue
            try:
                data = path.read_bytes()
            except OSError:
                unreadable.append(relative)
                continue
            files += 1
            if canary in data:
                hits.append(relative)
    return files, hits, unreadable


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="회차 디렉터리의 합성 토큰 검사")
    parser.add_argument("root", type=Path)
    parser.add_argument("--canary", required=True)
    args = parser.parse_args(argv)
    if len(args.canary) < MIN_CANARY_LENGTH or not args.canary.isascii():
        print("canary_invalid", file=sys.stderr)
        return 2
    if not args.root.is_dir():
        print("root_not_directory", file=sys.stderr)
        return 2
    files, hits, unreadable = scan(args.root, args.canary.encode("ascii"))
    print(f"files={files} hits={len(hits)} unreadable={len(unreadable)}")
    for relative in hits:
        print(f"hit: {relative}")
    for relative in unreadable:
        print(f"unreadable: {relative}")
    if unreadable or files == 0:
        return 2
    return 1 if hits else 0


if __name__ == "__main__":
    sys.exit(main())
