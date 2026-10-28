"""correlation — 평가 전용 GT 상관 테스트.

GT fixture는 손으로 만든 합성 규칙이고 실행 중 임시 디렉터리에만 쓴다. type·resource_ref도
합성 이름이다(대상 앱 엔드포인트 주장 아님). parse_scan_output(#18)과 독립으로 dict·속성
객체 레코드 둘 다로 검증한다. 매칭 키 = resource_ref(type:id)의 type 접두부 + vuln_class.

실행: scripts/eval 에서  python -I -m unittest discover -s tests
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace

EVAL_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, EVAL_DIR)

from correlation import DEFAULT_PATH, CorrelationError, load_correlation  # noqa: E402
from ground_truth import Axis, core_gt  # noqa: E402

_AXIS_NAME = {Axis.HORIZONTAL: "horizontal", Axis.VERTICAL: "vertical"}

# 합성 type 매핑 — 코어 키 몇 개에만 준다. 나머지는 빈 배열(unmapped).
_SYNTHETIC = {
    "basketAccessChallenge": {"types": ["basket"]},
    "forgedReviewChallenge": {"types": ["review"], "vuln_class": "idor"},
    "adminSectionChallenge": {"types": ["admin-panel"]},
}


def _rules(overrides: dict | None = None) -> list[dict]:
    """코어 GT 9종 전부를 담은 합성 규칙. overrides로 챌린지별 필드를 덮는다."""
    overrides = overrides if overrides is not None else _SYNTHETIC
    rules = []
    for c in core_gt():
        rule = {"challenge": c.key, "axis": _AXIS_NAME[c.axis], "vuln_class": None, "types": []}
        rule.update(overrides.get(c.key, {}))
        rules.append(rule)
    return rules


class _GtFixture(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)

    def load(self, payload: object = None, raw: str | None = None):
        if payload is None and raw is None:
            payload = {"version": 1, "rules": _rules()}
        path = os.path.join(self._tmp.name, "gt.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(raw if raw is not None else json.dumps(payload))
        return load_correlation(path)

    def load_error(self, payload: object = None, *fragments: str, raw: str | None = None) -> str:
        with self.assertRaises(CorrelationError) as ctx:
            self.load(payload, raw=raw)
        for frag in fragments:
            self.assertIn(frag, str(ctx.exception))
        return str(ctx.exception)


def _rec(resource_ref: object, vuln_class: object = "idor") -> dict:
    # axis도 계약 필드라 실어 두지만 매칭엔 안 쓰인다(매칭 키는 type+vuln_class).
    return {"axis": "horizontal", "vuln_class": vuln_class, "resource_ref": resource_ref}


class TestNormal(_GtFixture):
    def test_matches_dict_and_attribute_records(self) -> None:
        """계약 필드 모양이면 dict든 속성 객체든 같은 키 — 파서 타입에 묶이지 않는다."""
        table = self.load()
        self.assertEqual(table.correlate(_rec("basket:7")), "basketAccessChallenge")
        attr = SimpleNamespace(axis="horizontal", vuln_class="idor", resource_ref="basket:7")
        self.assertEqual(table.correlate(attr), "basketAccessChallenge")

    def test_type_prefix_ignores_id(self) -> None:
        """같은 type이면 id가 뭐든 같은 챌린지 — id엔 안 건다."""
        table = self.load()
        keys = {table.correlate(_rec(ref)) for ref in ("basket:1", "basket:2", "basket:abc")}
        self.assertEqual(keys, {"basketAccessChallenge"})

    def test_type_may_contain_colons(self) -> None:
        """type은 콜론을 품을 수 있다 — 마지막 콜론 앞이 type, 뒤가 id."""
        table = self.load({"rules": _rules({"basketAccessChallenge": {"types": ["rest:basket"]}})})
        self.assertEqual(table.correlate(_rec("rest:basket:42")), "basketAccessChallenge")
        self.assertIsNone(table.correlate(_rec("rest:basket")))  # type 'rest', id 'basket' — 미등록

    def test_vuln_class_constraint(self) -> None:
        table = self.load()
        self.assertEqual(table.correlate(_rec("review:3")), "forgedReviewChallenge")
        self.assertEqual(table.correlate(_rec("admin-panel:main")), "adminSectionChallenge")

    def test_unmapped_lists_typeless_challenges(self) -> None:
        table = self.load()
        expected = sorted(c.key for c in core_gt() if c.key not in _SYNTHETIC)
        self.assertEqual(sorted(table.unmapped), expected)


class TestNoMatchIsOutOfList(_GtFixture):
    def test_type_not_in_gt(self) -> None:
        self.assertIsNone(self.load().correlate(_rec("orders:9")))

    def test_vuln_class_mismatch(self) -> None:
        self.assertIsNone(self.load().correlate(_rec("review:3", vuln_class="xss")))

    def test_malformed_resource_ref(self) -> None:
        table = self.load()
        for ref in ("", ":", "basket", ":42", None, 42):
            self.assertIsNone(table.correlate(_rec(ref)), ref)


class TestAmbiguity(_GtFixture):
    def test_two_rules_sharing_type_rejected_at_load(self) -> None:
        """두 챌린지가 같은 type을 쓰면 로드에서 모호로 막는다(런타임 전에)."""
        overrides = dict(_SYNTHETIC)
        overrides["basketManipulateChallenge"] = {"types": ["basket"]}
        self.load_error({"rules": _rules(overrides)}, "type 'basket'", "모호")


class TestLoadValidation(_GtFixture):
    def test_challenge_not_in_core_gt(self) -> None:
        rules = _rules() + [{"challenge": "notAChallenge", "axis": "horizontal", "vuln_class": None, "types": []}]
        self.load_error({"rules": rules}, "코어 GT에 없는 챌린지: 'notAChallenge'")

    def test_bac_only_challenge_rejected(self) -> None:
        """BAC category엔 있어도 코어 9종이 아니면 거부 — 기준은 코어 GT."""
        rules = _rules() + [{"challenge": "csrfChallenge", "axis": "horizontal", "vuln_class": None, "types": []}]
        self.load_error({"rules": rules}, "'csrfChallenge'")

    def test_missing_core_challenge(self) -> None:
        rules = [r for r in _rules() if r["challenge"] != "dataExportChallenge"]
        self.load_error({"rules": rules}, "코어 GT 챌린지 누락", "dataExportChallenge")

    def test_duplicate_challenge(self) -> None:
        rules = _rules() + [_rules()[0]]
        self.load_error({"rules": rules}, "챌린지 중복")

    def test_axis_must_match_ground_truth(self) -> None:
        rules = _rules()
        for r in rules:
            if r["challenge"] == "basketAccessChallenge":
                r["axis"] = "vertical"
        self.load_error({"rules": rules}, "GT 축 HORIZONTAL과 다름")

    def test_unknown_rule_field(self) -> None:
        rules = _rules()
        rules[0]["resource_patterns"] = ["basket/*"]
        self.load_error({"rules": rules}, "모르는 필드: 'resource_patterns'")

    def test_bad_type(self) -> None:
        self.load_error({"rules": _rules({"basketAccessChallenge": {"types": [""]}})}, "잘못된 type")

    def test_broken_json_and_shape(self) -> None:
        self.load_error(None, "JSON 파싱 실패", raw='{"rules": [')
        self.load_error({"items": []}, "rules 배열이 없음")

    def test_errors_aggregated(self) -> None:
        rules = [r for r in _rules() if r["challenge"] != "dataExportChallenge"]
        rules[0]["axis"] = "sideways"
        msg = self.load_error({"rules": rules}, "axis 부정", "코어 GT 챌린지 누락")
        self.assertIn("오류 2건", msg)


class TestRepoGtData(unittest.TestCase):
    def test_repo_gt_file_covers_core_nine(self) -> None:
        table = load_correlation(DEFAULT_PATH)
        self.assertEqual(sorted(r.challenge for r in table.rules), sorted(c.key for c in core_gt()))

    def test_gt_not_referenced_outside_eval(self) -> None:
        """GT 상관은 평가 전용 — 엔진·다른 경로에서 참조 금지(절대규칙 7)."""
        git = shutil.which("git")
        if git is None:
            self.skipTest("git 없음 — 참조 검사 생략")
        repo = subprocess.run(
            [git, "-C", EVAL_DIR, "rev-parse", "--show-toplevel"], capture_output=True, text=True, check=True
        ).stdout.strip()
        found = subprocess.run(
            [git, "-C", repo, "grep", "--untracked", "-l", "-E",
             r"gt_correlation|from correlation|import correlation", "--", ".", ":!scripts/eval"],
            capture_output=True, text=True,
        )
        self.assertEqual(found.stdout.strip(), "", f"평가 밖에서 GT 상관 참조: {found.stdout}")


if __name__ == "__main__":
    unittest.main()
