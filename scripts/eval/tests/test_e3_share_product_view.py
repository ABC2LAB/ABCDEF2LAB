"""제품 control-policy 보기(E③ 꺼진 회차)를 러너 검사에 그대로 넣는 묶음 시험(#716 이동찬 리뷰).

고정 파일은 Go 시험 TestControlPolicyViewE3OffMatchesRunnerFixture가 실제 제품 보기 응답(실행 발급과 같은 몫 함수)과
대조한다. 이 시험은 같은 파일을 러너 checked_control_policy·check_official_e3_share에 넣는다.
"""

from __future__ import annotations

import copy
import json
import os
from pathlib import Path
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_split_run import RunFailure, check_official_e3_share, checked_control_policy  # noqa: E402

ROOT = Path(__file__).resolve().parents[3]
FIXTURE = ROOT / "cmd/first-controlled-diagnosis/testdata/control-policy-view-e3-off.json"
DRAFT = "live"  # 고정 파일의 policy_ref는 policy-<초안>이다


class ProductViewRunnerBinding(unittest.TestCase):
    def setUp(self):
        self.view = json.loads(FIXTURE.read_text(encoding="utf-8"))

    def test_issued_zero_share_passes_runner_checks(self):
        checked = checked_control_policy(self.view, DRAFT)
        check_official_e3_share(checked["limits"])
        self.assertEqual((checked["limits"]["e3_confirmation_requests"], checked["limits"]["e3_confirmation_timeout_ms"]), (0, 0))

    def _rejected(self, change, code):
        view = copy.deepcopy(self.view)
        change(view["limits"])
        with self.assertRaises(RunFailure) as caught:
            check_official_e3_share(checked_control_policy(view, DRAFT)["limits"])
        self.assertEqual(caught.exception.code, code)

    def test_missing_share_is_unconfirmed(self):
        def drop(limits):
            del limits["e3_confirmation_requests"], limits["e3_confirmation_timeout_ms"]
        self._rejected(drop, "e3_share_unconfirmed")

    def test_nonzero_share_is_rejected(self):
        self._rejected(lambda limits: limits.update(e3_confirmation_requests=5, e3_confirmation_timeout_ms=50000), "e3_share_not_zero")

    def test_half_share_is_invalid(self):
        self._rejected(lambda limits: limits.pop("e3_confirmation_timeout_ms"), "control_policy_limits_invalid")


if __name__ == "__main__":
    unittest.main()
