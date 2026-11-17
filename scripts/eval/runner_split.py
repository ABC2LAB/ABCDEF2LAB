"""탐색 분리 배치(개선 후 O) 평가 회차의 주소와 경계 설정(탐색 분리 배치 결정안 §2·§3 B(a), 순서 4-7a).

주소는 평가 overlay(deploy/eval/compose.split.yaml)와 같아야 한다. scripts/ci/test_product_eval_split.py가 overlay 렌더 결과와
이 상수를 대조한다. 제품 망 /24마다 같은 호스트 번호를 쓰므로 제품 주소는 앞 세 자리만 바꿔 평가 주소가 된다.

경계 설정은 제품 예시(deploy/product/explore-boundary.example.json)를 틀로 쓴다. 상한 칸은 제품 예시 그대로 두고(B(a): 상한은
제품 기본값), 주소만 평가 대역으로 옮기며, 평가 회차 전용 칸(경계 원자료·평가 upstream, 계약 §4.1·§4.2)을 더한다.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import re
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


# 대상 이름: 점 하나 이상인 소문자 DNS 이름(평가 대상 이름, overlay EVAL_TARGET_NAME). IP 리터럴은 따로 거부한다.
_TARGET_NAME = re.compile(r"(?=.{1,253}\Z)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\Z")


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


# ── 설치 입력·대상 등록(4-7a, 2026-11-17 사용자 결정) ──────────────────────────────────────────────────────────────
INSTALL_FORMAT = "abc2lab.product-install/v2"
TARGET_POLICY_FORMAT = "abc2lab.product-target-policy/v1"
DATABASE_NAME = "abc2lab"
# compose가 PRODUCT_POSTGRES_PASSWORD_FILE을 넣는 installer 안 경로(deploy/product/compose.yaml).
DATABASE_PASSWORD_FILE = "/tmp/product-private/postgres-password"
# 워크플로 상한의 제품 기본값 정본. 러너는 값을 사본으로 두지 않고 이 파일을 읽어 지문에 SHA·값을 남긴다.
WORKFLOW_DEFAULTS = REPO_ROOT / "deploy" / "product" / "workflow-defaults.json"
WORKFLOW_FIELDS = frozenset({"format", "max_pages", "max_endpoints", "max_response_bytes", "allow_reversible_writes"})
# 탐색 경계를 켠 설치의 target_policy.exploration 블록(deploy/product/README.md와 같다, 시험이 대조).
EXPLORATION_BLOCK = {"control_socket": "/run/explore-control/control.sock", "direct_proxy": "http://explore-direct:3129",
                     "boundary_ca_file": "/tmp/product-private/explore-ca.pem", "mcp_url": "http://explore-mcp:8931/mcp"}
# "리셋 대상 넓은 승인"(평가 O 회차 설정): 제품 대상 등록이 받는 최대 범위다(diagnosisprep 등록 검사: GET·HEAD, 되돌릴 수
# 있는 쓰기 승인 때만 POST·PUT·PATCH). DELETE·OPTIONS는 제품이 받지 않아 O 통제 비용으로 사유별 거부 수에 남는다.
WIDE_METHODS = ("GET", "HEAD", "POST", "PUT", "PATCH")
READ_METHODS = ("GET", "HEAD")
# 대상 등록의 읽기 전용 근거 글(제품은 20자 이상을 요구한다). 평가 대상은 회차마다 새로 띄우는 리셋 대상이다.
REGISTRATION_BASIS = "평가 회차 전용 연습 대상: 회차마다 새 컨테이너로 띄우고 회차 뒤 지운다(리셋 대상)."


def read_workflow_defaults(path: Path = WORKFLOW_DEFAULTS) -> tuple[dict, str]:
    """(워크플로 기본값, 파일 SHA-256). 칸이 정확히 다섯이 아니면 거부한다(값 범위는 제품 검사·CI 시험이 본다)."""
    try:
        raw = path.read_bytes()
        value = json.loads(raw.decode("utf-8"))
    except (OSError, UnicodeError, ValueError):
        raise SplitInputError("split_workflow_defaults_invalid") from None
    if not isinstance(value, dict) or set(value) != WORKFLOW_FIELDS:
        raise SplitInputError("split_workflow_defaults_invalid")
    return value, hashlib.sha256(raw).hexdigest()


def install_input(*, admin_password: str, manifest: dict, meter_port: int, workflow_defaults: dict, wide_writes: bool,
                  valid_for_hours: int, max_runs: int) -> dict:
    """평가 O 회차의 제품 설치 입력(v2). 결정안 B(a): 상한 칸(max_requests·max_targets·runtime_limits)은 넣지 않아 제품
    기본값을 쓴다. 워크플로 블록은 제품 기본값 파일 그대로이고, 쓰기 허용만 회차 설정(wide_writes)을 따른다. 대상 망은
    평가 대역이고 http 대상(C22)이라 허용 포트는 계측기 본 입구 포트 하나, DNS 증명은 overlay의 fixture DNS로 한다.
    모델 키는 여기 넣지 않는다(설치 뒤 제품 API로 넣는다). 관리자 비밀번호는 호출자가 회차마다 만든 값이다."""
    if (not isinstance(admin_password, str) or len(admin_password) < 12 or not isinstance(manifest, dict)
            or manifest.get("version") != 3 or type(meter_port) is not int or not 0 < meter_port < 65536
            or type(wide_writes) is not bool or type(valid_for_hours) is not int or valid_for_hours < 1
            or type(max_runs) is not int or max_runs < 1):
        raise SplitInputError("split_install_input_invalid")
    if not isinstance(workflow_defaults, dict) or set(workflow_defaults) != WORKFLOW_FIELDS:
        raise SplitInputError("split_workflow_defaults_invalid")
    workflow = dict(workflow_defaults, allow_reversible_writes=wide_writes)
    return {"format": INSTALL_FORMAT, "database_host": DATABASE_IP, "database_name": DATABASE_NAME,
            "database_password_file": DATABASE_PASSWORD_FILE, "admin_password": admin_password,
            "valid_for_hours": valid_for_hours, "max_runs": max_runs,
            "target_policy": {"format": TARGET_POLICY_FORMAT, "allowed_ports": [meter_port], "private_cidrs": [TARGET_CIDR],
                              "resolver_address": f"{FIXTURE_DNS_IP}:{FIXTURE_DNS_PORT}", "workflow": workflow,
                              "exploration": dict(EXPLORATION_BLOCK)},
            "openrouter_manifest": manifest}


def _is_ip(value: str) -> bool:
    try:
        ipaddress.ip_address(value)
    except ValueError:
        return False
    return True


def target_registration(*, target_name: str, meter_port: int, requests: int, valid_hours: int, wide_writes: bool) -> dict:
    """평가 O 회차의 제품 대상 등록 본문. 경로는 대상 전체(/), 제외 경로 없음(원본 B에 없음). 넓은 승인이면 제품 최대
    메서드 5개와 되돌릴 수 있는 쓰기, 아니면 읽기 두 개. 요청 수는 제품 카탈로그가 알려 준 회차 상한을 그대로 넣는다(화면과
    같다). 대상은 http(C22)이고 이름은 overlay에서 계측기 본 입구로 풀린다."""
    if (not isinstance(target_name, str) or not _TARGET_NAME.fullmatch(target_name) or _is_ip(target_name) or type(meter_port) is not int
            or not 0 < meter_port < 65536 or type(requests) is not int or requests < 1 or type(valid_hours) is not int
            or valid_hours < 1 or type(wide_writes) is not bool):
        raise SplitInputError("split_registration_invalid")
    body = {"url": f"http://{target_name}:{meter_port}/", "path_prefixes": ["/"],
            "methods": list(WIDE_METHODS if wide_writes else READ_METHODS), "readonly_basis": REGISTRATION_BASIS,
            "requests": requests, "valid_hours": valid_hours}
    if wide_writes:
        body["reversible_writes"] = True
    return body


def split_conditions(*, workflow_defaults: dict, workflow_sha256: str, install: dict, registration: dict) -> dict:
    """서명 회차 기록(run_conditions)에 남길 O 회차의 설치·등록 조건(4-7c에서 연결, 지문 v3(4-9)이 같은 값을 쓴다).
    워크플로 기본값 파일의 SHA와 값, 실제로 넣은 워크플로 블록, 대상 등록 범위(경로·메서드·쓰기·요청 수·유효 시간)를
    그대로 적는다. 제외 경로는 원본 B에 없어 비운다(2026-11-17 결정)."""
    workflow = install["target_policy"]["workflow"]
    if (set(workflow) != WORKFLOW_FIELDS or {k: v for k, v in workflow.items() if k != "allow_reversible_writes"}
            != {k: v for k, v in workflow_defaults.items() if k != "allow_reversible_writes"}
            or workflow["allow_reversible_writes"] is not registration.get("reversible_writes", False)):
        raise SplitInputError("split_conditions_mismatch")  # 설치 워크플로와 등록 쓰기 승인이 같은 회차 설정이어야 한다
    return {"workflow_defaults_sha256": workflow_sha256, "workflow_defaults": dict(workflow_defaults),
            "workflow": dict(workflow),
            "registration": {"path_prefixes": list(registration["path_prefixes"]), "excluded_paths": [],
                             "methods": list(registration["methods"]),
                             "reversible_writes": registration.get("reversible_writes", False),
                             "requests": registration["requests"], "valid_hours": registration["valid_hours"]}}
