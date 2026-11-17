"""탐색 분리 배치(개선 후 O) 평가 회차의 주소와 경계 설정(탐색 분리 배치 결정안 §2·§3 B(a), 순서 4-7a).

주소는 평가 overlay(deploy/eval/compose.split.yaml)와 같아야 한다. scripts/ci/test_product_eval_split.py가 overlay 렌더 결과와
이 상수를 대조한다. 제품 망 /24마다 같은 호스트 번호를 쓰므로 제품 주소는 앞 세 자리만 바꿔 평가 주소가 된다.

경계 설정은 제품 예시(deploy/product/explore-boundary.example.json)를 틀로 쓴다. 상한 칸은 제품 예시 그대로 두고(B(a): 상한은
제품 기본값), 주소만 평가 대역으로 옮기며, 평가 회차 전용 칸(경계 원자료·평가 upstream, 계약 §4.1·§4.2)을 더한다.
"""

from __future__ import annotations

import ipaddress
import json
from pathlib import Path

from runner_config import REPO_ROOT

EVAL_BAND = ipaddress.ip_network("10.252.0.0/20")
# 제품 /24 앞 세 자리 → 평가 /24 앞 세 자리(호스트 번호는 그대로).
PREFIX_MAP = {"172.31.250.": "10.252.0.", "172.31.252.": "10.252.2.", "172.31.253.": "10.252.3.",
              "172.31.254.": "10.252.4.", "172.31.255.": "10.252.5.", "172.31.247.": "10.252.7.",
              "172.31.248.": "10.252.8."}
DATABASE_IP = "10.252.2.10"
FIXTURE_DNS_IP = "10.252.2.40"
FIXTURE_DNS_PORT = 5353
TARGET_CIDR = "10.252.3.0/24"
# 계측기 본 입구(대상 이름이 풀리는 주소, target 망)와 탐색 입구(경계 평가 upstream, eval_explore_ingress 망).
METER_MAIN_IP = "10.252.3.20"
METER_EXPLORE_IP = "10.252.11.20"
# 경계 원자료 디렉터리(overlay init이 경계 uid 소유 0700으로 만든다, 계약 §4.1 결정 B).
BOUNDARY_EVIDENCE_DIR = "/evidence/run"
BOUNDARY_TEMPLATE = REPO_ROOT / "deploy" / "product" / "explore-boundary.example.json"
BOUNDARY_FORMAT = "jindo.explore-boundary-config/v1"
# 제품 예시에서 주소를 담는 칸. 이 밖의 칸에 제품 대역 주소가 있으면 옮기지 않고 거부한다.
_ADDRESS_FIELDS = ("browser_listen", "direct_listen")
_ADDRESS_LISTS = ("control_endpoints",)


class SplitInputError(ValueError):
    """분리 배치 입력을 만들 수 없음. 고정 코드만 담는다."""


def to_eval_address(value: str) -> str:
    """제품 대역 주소(호스트:포트 포함)를 평가 대역 주소로 옮긴다. 알려진 제품 /24가 아니면 거부한다."""
    for product, evaluation in PREFIX_MAP.items():
        if not value.startswith(product):
            continue
        moved = evaluation + value[len(product):]
        host, _, port = moved.partition(":")
        try:
            in_band = ipaddress.ip_address(host) in EVAL_BAND
        except ValueError:
            in_band = False
        if in_band and (not port or port.isdecimal()):
            return moved
        break
    raise SplitInputError("split_address_unknown")


def boundary_config(template: dict, explore_port: int, evidence_max_bytes: int) -> dict:
    """제품 예시 경계 설정에서 평가 회차 경계 설정을 만든다. 상한 칸은 그대로, 주소만 평가 대역, 평가 전용 칸을 더한다."""
    if not isinstance(template, dict) or template.get("format") != BOUNDARY_FORMAT:
        raise SplitInputError("split_boundary_template_invalid")
    if any(name in template for name in ("evidence_dir", "evidence_max_bytes", "eval_upstream")):
        raise SplitInputError("split_boundary_template_invalid")  # 제품 예시에는 평가 칸이 없어야 한다
    if type(explore_port) is not int or not 0 < explore_port < 65536 or type(evidence_max_bytes) is not int or evidence_max_bytes < 1:
        raise SplitInputError("split_boundary_input_invalid")
    config = json.loads(json.dumps(template))
    for name in _ADDRESS_FIELDS:
        if not isinstance(config.get(name), str):
            raise SplitInputError("split_boundary_template_invalid")
        config[name] = to_eval_address(config[name])
    for name in _ADDRESS_LISTS:
        values = config.get(name)
        if not isinstance(values, list) or not all(isinstance(value, str) for value in values):
            raise SplitInputError("split_boundary_template_invalid")
        config[name] = [to_eval_address(value) for value in values]
    # 주소 칸 밖에 제품 대역 주소가 남으면(예시에 새 주소 칸이 생김) 조용히 두지 않고 거부한다.
    if "172.31." in json.dumps(config):
        raise SplitInputError("split_address_unknown")
    config.update({"evidence_dir": BOUNDARY_EVIDENCE_DIR, "evidence_max_bytes": evidence_max_bytes,
                   "eval_upstream": f"{METER_EXPLORE_IP}:{explore_port}"})
    return config


def read_boundary_template(path: Path = BOUNDARY_TEMPLATE) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, ValueError):
        raise SplitInputError("split_boundary_template_invalid") from None
