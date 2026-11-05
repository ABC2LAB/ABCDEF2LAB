"""KG fixture 레지스트리 정규 바이트가 Go `kgfixture`와 같은지 — 공용 벡터로 대조."""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import kg_fixture_canonical as kc  # noqa: E402

VECTORS = Path(__file__).resolve().parents[3] / "kgfixture/testdata/vectors-v1.json"


def load_cases() -> list[dict]:
    vectors = json.loads(VECTORS.read_text(encoding="ascii"))
    assert vectors["format"] == "abc2lab.kg-fixture-vectors/v1"
    return vectors["cases"]


class TestSharedVectors(unittest.TestCase):
    def test_accepted_cases_match_go_digests(self) -> None:
        digests: dict[str, str] = {}
        accepted = [c for c in load_cases() if "digest" in c["expect"]]
        self.assertTrue(accepted)
        for case in accepted:
            with self.subTest(case["name"]):
                data = case["registry"].encode("utf-8")
                digest = kc.digest(data)
                self.assertEqual(digest, case["expect"]["digest"])
                snapshot_id = kc.strict_load(data)["snapshot_id"]
                self.assertEqual(kc.policy_version(snapshot_id, digest), case["expect"]["policy_version"])
                same = case["expect"].get("same_digest_as")
                if same:
                    self.assertEqual(digest, digests[same])
                digests[case["name"]] = digest

    def test_decode_rejections_match_go(self) -> None:
        rejected = [c for c in load_cases() if c["expect"].get("layer") == "decode"]
        self.assertTrue(rejected)
        for case in rejected:
            with self.subTest(case["name"]):
                with self.assertRaises(kc.RegistryDecodeError) as raised:
                    kc.strict_load(case["registry"].encode("utf-8"))
                self.assertEqual(str(raised.exception), case["expect"]["error"])

    def test_every_case_is_classified(self) -> None:
        for case in load_cases():
            expect = case["expect"]
            with self.subTest(case["name"]):
                self.assertTrue(("digest" in expect) != ("error" in expect))
                if "error" in expect:
                    self.assertIn(expect["layer"], ("decode", "schema"))

    def test_canonical_escapes_only_quote_and_backslash(self) -> None:
        value = {"b": 'say "hi" \\ /ok', "a": True}
        self.assertEqual(kc.canonical_bytes(value), b'{"a":true,"b":"say \\"hi\\" \\\\ /ok"}')

    def test_invalid_utf8_is_rejected(self) -> None:
        with self.assertRaises(kc.RegistryDecodeError):
            kc.strict_load(b'{"a":"\xff"}')


if __name__ == "__main__":
    unittest.main()
