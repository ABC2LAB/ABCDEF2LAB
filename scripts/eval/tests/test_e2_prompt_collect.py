"""제품 투영 응답을 회차 식별값으로 줄이는 쪽(docs/contracts/e2-prompt-projection.md). 기대값은 계약 문서에서 옮겼다."""

from __future__ import annotations

import hashlib
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_prompt_collect import E2PromptNotReady, E2PromptUnverified, parse_projection  # noqa: E402

RUN = "e2run-1"
IDENTITY = ("builtin", "a" * 64, "b" * 64, "c" * 64, "d" * 64)


def observation(sequence=1, **changes):
    return {"sequence": sequence, "activity_id": 100 + sequence, "intent_id": 7, "worker": "work#1", "version": "e2-worker-prompt/v1", "key": "e2_worker",
            "source": "builtin", "template_digest": "a" * 64, "guidance_digest": "b" * 64, "control_digest": "c" * 64, "tools_digest": "d" * 64} | changes


def document(count=2, **changes):
    return {"format": "abc2lab.e2-prompt-projection/v1", "task_id": 42, "exploration_id": 9, "run_ref": RUN, "state": "complete", "complete": True,
            "expected_count": count, "observed_count": count, "snapshot_sequence": count, "observations": [observation(n + 1) for n in range(count)]} | changes


def encode(value) -> bytes:
    return json.dumps(value).encode("utf-8")


class Base(unittest.TestCase):
    def code(self, raw: bytes, error=E2PromptUnverified) -> str:
        with self.assertRaises(error) as caught:
            parse_projection(raw, "42", RUN)
        return caught.exception.code

    def invalid(self, value) -> None:
        self.assertEqual(self.code(encode(value)), "projection_invalid")


class TestComplete(Base):
    def test_returns_the_single_identity_and_exact_byte_digest(self):
        raw = encode(document())
        result = parse_projection(raw, "42", RUN)
        self.assertEqual((result.identity, result.exploration_id, result.expected_count, result.projection_sha256),
                         (IDENTITY, 9, 2, hashlib.sha256(raw).hexdigest()))

    def test_different_identities_are_not_settled_by_majority(self):
        rows = [observation(1), observation(2), observation(3, tools_digest="e" * 64)]
        self.assertEqual(self.code(encode(document(3, observations=rows))), "run_conflict")

    def test_each_prompt_value_is_checked(self):
        for name, value, code in (("source", "db", "source_invalid"), ("template_digest", "x", "digest_invalid"), ("guidance_digest", "A" * 64, "digest_invalid"),
                                  ("control_digest", None, "digest_invalid"), ("tools_digest", 7, "digest_invalid")):
            with self.subTest(name):
                self.assertEqual(self.code(encode(document(1, observations=[observation(1, **{name: value})]))), code)


class TestStates(Base):
    def test_collecting_can_be_asked_again(self):
        raw = encode(document(state="collecting", complete=False))
        self.assertEqual(self.code(raw, E2PromptNotReady), "collecting")

    def test_other_states_are_final(self):
        for state in ("incomplete", "empty", "invalid", "conflict"):
            with self.subTest(state):
                self.assertEqual(self.code(encode(document(state=state, complete=False, observations=[]))), "state_" + state)

    def test_complete_flag_must_match_the_state(self):
        self.invalid(document(complete=False))
        self.invalid(document(state="incomplete", complete=True))
        self.invalid(document(state="collecting", complete=True))

    def test_complete_flag_is_a_boolean(self):
        self.invalid(document(complete=1))

    def test_unknown_state_is_rejected(self):
        self.invalid(document(state="done", complete=False))


class TestBinding(Base):
    def test_other_run_or_task_is_rejected(self):
        self.invalid(document(run_ref="e2run-2"))
        self.invalid(document(task_id=43))

    def test_task_id_must_be_a_json_integer(self):
        self.invalid(document(task_id="42"))
        self.invalid(document(task_id=True))
        self.invalid(document(task_id=42.0))

    def test_exploration_id_boundary(self):
        self.invalid(document(exploration_id=0))
        self.invalid(document(exploration_id=True))
        self.assertEqual(parse_projection(encode(document(exploration_id=1)), "42", RUN).exploration_id, 1)

    def test_format_is_fixed(self):
        self.invalid(document(format="abc2lab.e2-prompt-projection/v2"))


class TestCounts(Base):
    def test_complete_needs_at_least_one_observation(self):
        self.invalid(document(0))

    def test_counts_must_agree(self):
        self.invalid(document(expected_count=3))
        self.invalid(document(observed_count=1))
        self.invalid(document(snapshot_sequence=1))

    def test_observed_cannot_exceed_expected(self):
        self.invalid(document(2, state="incomplete", complete=False, observed_count=3, observations=[]))

    def test_count_limit_boundary(self):
        self.invalid(document(state="incomplete", complete=False, expected_count=10001, observed_count=0, snapshot_sequence=10001, observations=[]))
        self.assertEqual(self.code(encode(document(state="incomplete", complete=False, expected_count=10000, observed_count=0, snapshot_sequence=10000, observations=[]))), "state_incomplete")

    def test_counts_are_integers(self):
        self.invalid(document(expected_count=True))
        self.invalid(document(observed_count=2.0))
        self.invalid(document(snapshot_sequence="2"))


class TestObservations(Base):
    def one(self, **changes):
        self.invalid(document(1, observations=[observation(1, **changes)]))

    def test_sequence_must_count_from_one_without_gaps(self):
        self.invalid(document(2, observations=[observation(2), observation(1)]))
        self.invalid(document(2, observations=[observation(1), observation(3)]))
        self.invalid(document(1, observations=[observation(True)]))

    def test_activity_is_positive_and_unique(self):
        self.one(activity_id=0)
        self.one(activity_id=True)
        self.invalid(document(2, observations=[observation(1, activity_id=5), observation(2, activity_id=5)]))

    def test_intent_is_positive(self):
        self.one(intent_id=0)
        self.one(intent_id="7")

    def test_worker_form(self):
        for value in ("work#0", "worker", "work#1000000", "work#1\n", None):
            with self.subTest(repr(value)):
                self.one(worker=value)

    def test_largest_worker_is_accepted(self):
        parse_projection(encode(document(1, observations=[observation(1, worker="work#999999")])), "42", RUN)

    def test_version_and_key_are_fixed(self):
        self.one(version="e2-worker-prompt/v2")
        self.one(key="other")

    def test_observation_fields_are_closed(self):
        self.invalid(document(1, observations=[observation(1, system_digest="e" * 64)]))
        row = observation(1)
        del row["tools_digest"]
        self.invalid(document(1, observations=[row]))

    def test_observation_must_be_an_object(self):
        self.invalid(document(1, observations=["row"]))


class TestShape(Base):
    def test_fields_are_closed(self):
        self.invalid(document(extra=1))
        value = document()
        del value["observations"]
        self.invalid(value)

    def test_observations_must_be_a_list(self):
        self.invalid(document(observations={}))
        # 수집 중이어도 형식이 틀린 응답은 다시 묻지 않는다.
        self.invalid(document(state="collecting", complete=False, observations={}))

    def test_observation_must_be_an_object(self):
        self.invalid(document(observations=[1, observation(2)]))

    def test_snapshot_must_be_an_integer(self):
        self.invalid(document(snapshot_sequence=2.0))

    def test_rows_must_match_the_count(self):
        # 건수 필드는 맞는데 행만 빠진 응답. 받은 행만 비교하면 누락이 가려진다.
        value = document()
        value["observations"].pop()
        self.invalid(value)

    def test_duplicate_keys_are_rejected(self):
        # 값이 같아야 중복 검사만 시험한다. 값이 다르면 format 검사가 대신 막아 준다.
        raw = encode(document())[:-1] + b',"format":"abc2lab.e2-prompt-projection/v1"}'
        self.assertEqual(self.code(raw), "projection_invalid")

    def test_non_finite_numbers_are_rejected(self):
        self.assertEqual(self.code(encode(document()).replace(b'"exploration_id": 9', b'"exploration_id": NaN')), "projection_invalid")

    def test_not_json_or_not_utf8(self):
        for raw in (b"", b"[]", b"{", b"\xff"):
            with self.subTest(raw):
                self.assertEqual(self.code(raw), "projection_invalid")

    def test_size_boundary(self):
        self.assertEqual(self.code(encode(document()) + b" " * (8 << 20)), "projection_invalid")


if __name__ == "__main__":
    unittest.main()
