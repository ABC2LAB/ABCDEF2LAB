"""계측기 경로 가림의 Python 판(path_mask.py) 시험. Go 시험 벡터와 같은 값을 내야 한다."""

from __future__ import annotations

import json
import os
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from path_mask import KEY_BYTES, PATH_PROFILE, STATIC_SUFFIXES, PathKeyError, key_ref, mask_path, read_path_key  # noqa: E402

GO_DIR = Path(__file__).resolve().parents[3] / "cmd/eval-meter"


class MaskPathTests(unittest.TestCase):
    def test_matches_the_go_vectors(self):
        vectors = json.loads((GO_DIR / "testdata/path-mask-v2.json").read_text(encoding="utf-8"))
        self.assertEqual(vectors["profile"], PATH_PROFILE)
        key = bytes.fromhex(vectors["key_hex"])
        self.assertEqual(key_ref(key), vectors["key_ref"])
        self.assertGreaterEqual(len(vectors["vectors"]), 20)
        for case in vectors["vectors"]:
            with self.subTest(case["name"]):
                self.assertEqual(mask_path(case["path"], key), (case["path_pattern"], case["masked_segments"]))

    def test_segment_count_bound(self):
        key = bytes(KEY_BYTES)
        self.assertEqual(mask_path("/1" * 32 + "/", key), ("/{id}" * 32 + "/", 32))
        self.assertEqual(mask_path("/a" * 33, key), ("/{too_long}", 33))
        with self.assertRaises(PathKeyError):
            mask_path("/a", key[:-1])

    def test_static_suffixes_match_the_meter(self):
        go = (GO_DIR / "paths.go").read_text(encoding="utf-8")
        listed = re.search(r"var staticSuffixes = \[\]string\{(.*?)\}", go, re.S).group(1)
        self.assertEqual(tuple(re.findall(r'"([^"]+)"', listed)), STATIC_SUFFIXES)
        self.assertIn('pathKeyRefLabel = "abc2lab.path-key-ref/v1"', go)


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "mkfifo"), "POSIX 전용")
class ReadPathKeyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def key_file(self, name, data, mode=0o600):
        path = Path(self.tmp.name, name)
        path.write_bytes(data)
        os.chmod(path, mode)
        return str(path)

    def test_reads_only_a_private_32_byte_file(self):
        good = self.key_file("good", bytes(range(KEY_BYTES)))
        self.assertEqual(read_path_key(good), bytes(range(KEY_BYTES)))
        link = Path(self.tmp.name, "link")
        link.symlink_to(good)
        fifo = Path(self.tmp.name, "fifo")
        os.mkfifo(fifo, 0o600)
        for name, path in (("readable", self.key_file("open", bytes(KEY_BYTES), 0o644)),
                           ("owner exec", self.key_file("exec", bytes(KEY_BYTES), 0o700)),
                           ("short", self.key_file("short", bytes(KEY_BYTES - 1))),
                           ("long", self.key_file("long", bytes(KEY_BYTES + 1))),
                           ("symlink", str(link)), ("fifo", str(fifo)), ("directory", self.tmp.name),
                           ("missing", str(Path(self.tmp.name, "none")))):
            with self.subTest(name), self.assertRaises(PathKeyError):
                read_path_key(path)

    def test_read_errors_become_key_errors(self):
        # 네트워크 마운트의 EIO 같은 오류가 호출자 밖으로 새지 않는다(#378 리뷰).
        good = self.key_file("good", bytes(range(KEY_BYTES)))
        for target in ("fstat", "fdopen"):
            with self.subTest(target), patch(f"path_mask.os.{target}", side_effect=OSError(5, "EIO")), \
                    self.assertRaises(PathKeyError) as caught:
                read_path_key(good)
            self.assertEqual(str(caught.exception), "path_key_unavailable")


if __name__ == "__main__":
    unittest.main()
