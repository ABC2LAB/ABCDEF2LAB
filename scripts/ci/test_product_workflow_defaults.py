"""제품 워크플로 기본값 파일(deploy/product/workflow-defaults.json) 시험.

이 파일 하나가 `target_policy.workflow`의 제품 기본값 정본이다(2026-11-17 사용자 결정). README 설치 예시와 평가 러너는 이
파일을 그대로 쓰고 사본을 두지 않는다. 여기서는 파일 모양, 코드 상한(diagnosisflow/contract.go) 안인지, README 예시가 파일과
같은지를 본다. 파일이 제품 검사(Limits.Valid)를 통과하는지는 diagnosisflow Go 시험이 본다.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DEFAULTS = ROOT / "deploy/product/workflow-defaults.json"
README = ROOT / "deploy/product/README.md"
CONTRACT = ROOT / "diagnosisflow/contract.go"
FIELDS = {"format", "max_pages", "max_endpoints", "max_response_bytes", "allow_reversible_writes"}


def go_int(text, name):
    found = re.search(rf"\nconst {name} = (\d+)\n", text)
    if not found:
        raise AssertionError("Go 상수를 찾지 못함: " + name)
    return int(found.group(1))


class WorkflowDefaultsTests(unittest.TestCase):
    def setUp(self):
        self.defaults = json.loads(DEFAULTS.read_text(encoding="utf-8"))

    def test_shape_and_code_limits(self):
        self.assertEqual(set(self.defaults), FIELDS)
        contract = CONTRACT.read_text(encoding="utf-8")
        version = re.search(r'\nconst Version = "([^"]+)"\n', contract).group(1)
        self.assertEqual(self.defaults["format"], version)
        for name, cap in (("max_pages", go_int(contract, "MaxWorkflowPages")),
                          ("max_endpoints", go_int(contract, "MaxWorkflowEndpoints"))):
            with self.subTest(field=name):
                value = self.defaults[name]
                self.assertTrue(type(value) is int and 1 <= value <= cap)
        self.assertTrue(type(self.defaults["max_response_bytes"]) is int and 4096 <= self.defaults["max_response_bytes"] <= 2 << 20)
        # 고객 설치 기본값은 쓰기를 끈다. 평가 O의 쓰기 승인은 회차 설정이다(이 파일을 바꾸지 않는다).
        self.assertIs(self.defaults["allow_reversible_writes"], False)

    def test_readme_example_equals_the_defaults_file(self):
        text = README.read_text(encoding="utf-8")
        examples = [json.loads(block) for block in re.findall(r"```json\n(.*?)\n```", text, re.S)
                    if '"workflow"' in block and '"max_requests"' in block]
        self.assertEqual(len(examples), 1, "워크플로 설치 예시 블록은 하나여야 한다")
        self.assertEqual(examples[0]["workflow"], self.defaults)


if __name__ == "__main__":
    unittest.main()
