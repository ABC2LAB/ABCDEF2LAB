"""E② 워커 프롬프트 식별값 대조(프로토콜 §2). 기대값은 프로토콜 문장과 엔진 구조체에서 옮긴 리터럴이다."""

from __future__ import annotations

import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_prompt_identity import PromptIdentityError, condition_prompt_identity, run_prompt_identity

ROOT = Path(__file__).resolve().parents[3]
FIELDS = ("source", "template_digest", "guidance_digest", "control_digest", "tools_digest")
SOURCES = ("builtin", "override", "builtin_invalid_override")
A, B, C, D, E = "a" * 64, "b" * 64, "c" * 64, "d" * 64, "e" * 64
IDENTITY = ("builtin", A, B, C, D)


def row(**changes: object) -> dict:
    return {"source": "builtin", "template_digest": A, "guidance_digest": B, "control_digest": C, "tools_digest": D,
            "system_digest": E, **changes}


def without(name: str) -> dict:
    observation = row()
    del observation[name]
    return observation


class RunIdentityTest(unittest.TestCase):
    def code(self, observations: list) -> str:
        with self.assertRaises(PromptIdentityError) as caught:
            run_prompt_identity(observations)
        return caught.exception.code

    def test_returns_source_and_four_digests_in_order(self):
        self.assertEqual(run_prompt_identity([row()]), IDENTITY)

    def test_many_workers_with_same_prompt_are_one_identity(self):
        self.assertEqual(run_prompt_identity([row(), row(), row()]), IDENTITY)

    def test_zero_observations_is_not_success(self):
        self.assertEqual(self.code([]), "none_observed")

    def test_system_digest_differs_per_intent_and_is_not_compared(self):
        self.assertEqual(run_prompt_identity([row(system_digest=A), row(system_digest=B)]), IDENTITY)

    def test_system_digest_is_not_required(self):
        self.assertEqual(run_prompt_identity([without("system_digest")]), IDENTITY)

    def test_each_compared_field_can_conflict(self):
        changes = {"source": "override", "template_digest": E, "guidance_digest": E, "control_digest": E, "tools_digest": E}
        for name in FIELDS:
            with self.subTest(name):
                self.assertEqual(self.code([row(), row(**{name: changes[name]})]), "run_conflict")

    def test_conflict_is_not_settled_by_latest_or_majority(self):
        other = row(tools_digest=E)
        for observations in ([row(), row(), other], [other, row(), row()]):
            with self.subTest(observations[0]["tools_digest"]):
                self.assertEqual(self.code(observations), "run_conflict")

    def test_each_missing_field_is_rejected(self):
        for name in FIELDS:
            with self.subTest(name):
                self.assertEqual(self.code([without(name)]), "field_missing")

    def test_one_bad_row_rejects_the_whole_run(self):
        self.assertEqual(self.code([row(), without("control_digest")]), "field_missing")

    def test_non_mapping_observation_is_rejected(self):
        for value in (None, "builtin", [IDENTITY]):
            with self.subTest(repr(value)):
                self.assertEqual(self.code([value]), "observation_invalid")

    def test_engine_sources_are_accepted(self):
        for source in SOURCES:
            with self.subTest(source):
                self.assertEqual(run_prompt_identity([row(source=source)])[0], source)

    def test_other_sources_are_rejected(self):
        for value in ("", "BUILTIN", "db", None, 1, ["builtin"], {"builtin": 1}):
            with self.subTest(repr(value)):
                self.assertEqual(self.code([row(source=value)]), "source_invalid")

    def test_digest_length_boundary(self):
        self.assertEqual(run_prompt_identity([row(tools_digest="f" * 64)])[4], "f" * 64)
        for length in (63, 65):
            with self.subTest(length):
                self.assertEqual(self.code([row(tools_digest="f" * length)]), "digest_invalid")

    def test_each_digest_field_is_validated(self):
        for name in FIELDS[1:]:
            with self.subTest(name):
                self.assertEqual(self.code([row(**{name: "not-a-digest"})]), "digest_invalid")

    def test_digest_is_lowercase_hex_string_only(self):
        for value in ("A" * 64, "g" * 64, A + "\n", None, 7, int("1" * 64)):
            with self.subTest(repr(value)):
                self.assertEqual(self.code([row(guidance_digest=value)]), "digest_invalid")


class ConditionIdentityTest(unittest.TestCase):
    def code(self, runs: list) -> str:
        with self.assertRaises(PromptIdentityError) as caught:
            condition_prompt_identity(runs)
        return caught.exception.code

    def test_same_prompt_across_runs(self):
        self.assertEqual(condition_prompt_identity([[row()], [row(), row()], [row(system_digest=A)]]), IDENTITY)

    def test_accepts_a_generator_of_runs(self):
        self.assertEqual(condition_prompt_identity(run for run in [[row()], [row()]]), IDENTITY)

    def test_different_prompt_between_runs_is_refused(self):
        self.assertEqual(self.code([[row()], [row(guidance_digest=E)]]), "runs_differ")

    def test_one_unobserved_run_fails_the_condition(self):
        self.assertEqual(self.code([[row()], []]), "none_observed")

    def test_one_conflicting_run_fails_the_condition(self):
        self.assertEqual(self.code([[row()], [row(), row(control_digest=E)]]), "run_conflict")

    def test_no_runs_is_not_success(self):
        self.assertEqual(self.code([]), "no_runs")


class EngineDriftTest(unittest.TestCase):
    """엔진이 필드나 source 값을 바꾸면 소비 쪽이 전부 거부하게 되므로 같이 맞춘다."""

    source = (ROOT / "agent/e2_http_prompt.go").read_text(encoding="utf-8")

    def test_engine_emits_the_compared_field_names(self):
        tags = set(re.findall(r'json:"([a-z_]+)"', self.source))
        self.assertLessEqual(set(FIELDS) | {"system_digest"}, tags)

    def test_engine_source_values_are_the_accepted_ones(self):
        emitted = set(re.findall(r'Source(?::\s*|\s*=\s*)"([a-z_]+)"', self.source))
        self.assertEqual(emitted, set(SOURCES))


if __name__ == "__main__":
    unittest.main()
