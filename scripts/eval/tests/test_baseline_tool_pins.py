"""탐색 분리 배치 B 빌드의 도구 버전 핀 패치(결정안 §4 C(a), 4-10) 시험.

B 빌드 = 원본 커밋(e53a79f) + 이 패치 하나. 원본 Dockerfile은 Playwright MCP·CLI·playwright를 `@latest`로 깔아 빌드 시점마다
도구 면이 달라진다(11/03·11/07 실측). 패치는 그 한 줄만 제품(O) 이미지와 같은 판으로 고정한다. 패치 SHA는 선언 차이로
서명한다(보존 sidecar v3, 4-9). CI는 얕은 체크아웃이라 원본 커밋이 없을 수 있다. 그래서 dev 루트 Dockerfile이 원본과
바이트까지 같다는 것을 SHA로 고정하고, 거기에 패치를 적용해 본다(원본 커밋이 있으면 그것과도 대조).
"""

from __future__ import annotations

import hashlib
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
PATCH = ROOT / "deploy" / "eval" / "baseline-tool-pins.patch"
ROOT_DOCKERFILE = ROOT / "Dockerfile"
PRODUCT_DOCKERFILE = ROOT / "deploy" / "product" / "Dockerfile"
BASELINE_COMMIT = "e53a79fe4df0c95a5b09274de8db321fa038b00b"
# e53a79f:Dockerfile의 SHA-256(`git show e53a79f:Dockerfile | sha256sum`, 2026-11-17 확인). dev 루트 Dockerfile도 같다.
BASELINE_DOCKERFILE_SHA256 = "c43eb75dd7c3a9c7c3fff38ab61aac2844dc7766d276584735066f5a1f247c51"
_NPM_LINE = re.compile(r"^RUN npm install -g ((?:@?[\w./-]+@[\w.-]+ ?)+)\\$", re.M)


def packages(text: str) -> list[dict[str, str]]:
    """Dockerfile의 `RUN npm install -g 패키지@판 ...` 줄마다 {패키지: 판}."""
    found = []
    for match in _NPM_LINE.finditer(text):
        found.append(dict(item.rsplit("@", 1) for item in match.group(1).split()))
    return found


class TestBaselineToolPins(unittest.TestCase):
    def test_root_dockerfile_is_the_baseline_original(self):
        self.assertEqual(hashlib.sha256(ROOT_DOCKERFILE.read_bytes()).hexdigest(), BASELINE_DOCKERFILE_SHA256)
        git = shutil.which("git")
        shown = subprocess.run([git, "-C", str(ROOT), "show", f"{BASELINE_COMMIT}:Dockerfile"], capture_output=True) if git else None
        if shown is None or shown.returncode != 0:
            self.skipTest("원본 커밋 없음(얕은 체크아웃)")
        self.assertEqual(hashlib.sha256(shown.stdout).hexdigest(), BASELINE_DOCKERFILE_SHA256)

    def test_patch_pins_only_the_tool_line_to_the_product_versions(self):
        git = shutil.which("git")
        if git is None:
            self.fail("git이 필요하다")
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "Dockerfile"
            target.write_bytes(ROOT_DOCKERFILE.read_bytes())
            applied = subprocess.run([git, "apply", "-p1", str(PATCH)], cwd=tmp, capture_output=True)
            self.assertEqual(applied.returncode, 0, applied.stderr.decode(errors="replace")[:200])
            before, after = ROOT_DOCKERFILE.read_text(encoding="utf-8"), target.read_text(encoding="utf-8")
        changed = [(a, b) for a, b in zip(before.splitlines(), after.splitlines()) if a != b]
        self.assertEqual(len(before.splitlines()), len(after.splitlines()))
        self.assertEqual(len(changed), 1)  # 도구 설치 줄 하나만 바뀐다
        self.assertEqual(packages(before), [{"@playwright/mcp": "latest", "@playwright/cli": "latest", "playwright": "latest"}])
        pinned = packages(after)[0]
        # 제품(O) 엔진 이미지의 같은 줄과 같은 판이다(제품이 판을 올리면 이 시험이 깨져 패치도 같이 올린다).
        product = packages(PRODUCT_DOCKERFILE.read_text(encoding="utf-8"))
        self.assertIn(pinned, product)
        self.assertTrue(all(re.fullmatch(r"\d+\.\d+\.\d+", version) for version in pinned.values()))


if __name__ == "__main__":
    unittest.main()
