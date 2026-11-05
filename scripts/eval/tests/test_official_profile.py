"""공식 비교 선언(평가 프로토콜 §5.1·§6)이 검증기를 통과하고 고정 근거와 맞는지 묶는다."""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from profile_fingerprint import generate_fingerprints  # noqa: E402

PROFILES = Path(__file__).resolve().parents[3] / "docs/evaluation/profiles"
OFFICIAL = PROFILES / "official-35b.json"
EVIDENCE = PROFILES / "evidence/openrouter-endpoints-qwen3.6-35b-a3b-20261104.json"
CARD = PROFILES / "evidence/qwen3.6-35b-a3b-model-card-20261104.json"
PARAMS = PROFILES / "evidence/openrouter-endpoint-params-qwen3.6-35b-a3b-20261104.json"


class TestOfficialProfile(unittest.TestCase):
    def setUp(self):
        self.profile = json.loads(OFFICIAL.read_text(encoding="utf-8"))
        self.evidence = json.loads(EVIDENCE.read_text(encoding="utf-8"))

    def test_declaration_passes_the_v5_validator(self):
        self.assertEqual(generate_fingerprints(self.profile).format, "abc2lab-eval-profile-v5")

    def test_provider_is_the_single_endpoint_found_at_pin_time(self):
        llm = self.profile["environment"]["llm"]
        self.assertEqual((llm["model"], llm["provider"], llm["fallbacks"]),
                         (self.evidence["model"], self.evidence["pinned_slug"], False))
        matched = self.evidence["matched_endpoints"]
        # 접두 매칭이라 slug가 엔드포인트 하나·정밀도 하나에만 걸려야 정밀도의 요청 고정 근거가 된다(§6).
        self.assertEqual(len(matched), 1)
        self.assertEqual(sum(1 for row in self.evidence["all_endpoints"]
                             if row["tag"] == llm["provider"] or row["tag"].startswith(llm["provider"] + "/")), 1)
        self.assertTrue(matched[0]["seed"] and matched[0]["temperature"] and matched[0]["tools"])

    def test_sampling_follows_the_model_card_and_the_provider_supports_it(self):
        card = json.loads(CARD.read_text(encoding="utf-8"))
        chosen = card["recommended"][card["chosen_mode"]]
        self.assertEqual(card["chosen"], chosen)
        environment = self.profile["environment"]
        # 일곱 값 전부 카드 권장값·고정 seed와 같아야 한다. temperature 0(greedy)으로 돌아가지 않는다.
        self.assertEqual((environment["temperature"], environment["seed"]), (chosen["temperature"], card["seed"]))
        self.assertEqual(environment["sampling"], {k: v for k, v in chosen.items() if k != "temperature"})
        self.assertNotEqual(environment["temperature"], 0)
        self.assertTrue(card["provider_supports"] and all(card["provider_supports"].values()))
        self.assertEqual(set(card["provider_supports"]), {*chosen, "seed"})

    def test_pinned_endpoint_lists_every_parameter_the_gateway_sends(self):
        # 게이트웨이가 require_parameters:true를 넣으므로, 지원 목록에 없는 값이 하나라도 있으면 요청이 실패한다(§6).
        params = json.loads(PARAMS.read_text(encoding="utf-8"))
        environment = self.profile["environment"]
        self.assertEqual((params["model"], params["pinned_slug"]),
                         (environment["llm"]["model"], environment["llm"]["provider"]))
        sent = {"temperature", "seed", *environment["sampling"]}
        self.assertEqual(set(params["pinned_parameters"]), sent)
        self.assertEqual(len(params["matched_endpoints"]), 1)
        endpoint = params["matched_endpoints"][0]
        self.assertEqual(endpoint["tag"], environment["llm"]["provider"])
        self.assertTrue(sent <= set(endpoint["supported_parameters"]))
        self.assertTrue(endpoint["pinned_parameters_supported"])
        # 엔진이 보내는 파라미터(tools 등)도 require_parameters:true의 검사를 받는다.
        self.assertTrue(set(params["engine_parameters"]) <= set(endpoint["supported_parameters"]))
        self.assertTrue(endpoint["engine_parameters_supported"])
        self.assertIn("tools", params["engine_parameters"])

    def test_target_image_is_pinned_by_digest_and_fixture_is_anonymous(self):
        self.assertRegex(self.profile["environment"]["target_image"], r"^bkimminich/juice-shop:v15\.0\.0@sha256:[0-9a-f]{64}$")
        fixture = self.profile["fixture"]
        # 익명 준비면 다계정 IDOR 검증기가 돌지 않으므로 주장 범위에서 IDOR 효과를 뺀다(§5.1).
        self.assertEqual((fixture["accounts"], fixture["resources"]), ([], []))


if __name__ == "__main__":
    unittest.main()
