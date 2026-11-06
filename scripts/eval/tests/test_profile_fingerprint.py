"""검토된 비민감 선언의 합성 프로파일만 사용한다. 실제 계정·대상·모델은 사용하지 않는다."""

from __future__ import annotations

import copy
import itertools
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from dataclasses import asdict
from decimal import Decimal, localcontext
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import preflight
import profile_fingerprint
from profile_fingerprint import FORMAT, MAX_INPUT_BYTES, ProfileError, generate_fingerprints


def api_llm():
    return {"backend": "api", "base_url": "https://llm-api.example/api/v1", "model": "example-org/example-model",
            "provider": "example-provider", "fallbacks": False}


def local_llm():
    return {"backend": "local", "base_url": "http://llm-host:11434/v1", "model": "example-model:fixed-tag",
            "hostname": "llm-host", "host_ip": "172.17.0.1", "model_digest": "a" * 64, "context_length": 32768, "server_version": "0.32.6"}


LOCAL_SAMPLING = {"top_p": 0.95, "presence_penalty": 0.5, "frequency_penalty": 0.25}


def use_llm(payload, llm):
    """백엔드마다 샘플링 묶음이 달라서 llm과 함께 맞춘다."""
    payload["environment"]["llm"] = llm
    if llm["backend"] == "local":
        payload["environment"]["sampling"] = dict(LOCAL_SAMPLING)


def profile():
    return {
        "version": 5,
        "fixture": {
            "preparation_id": "read_fixture", "preparation_revision": 1,
            "accounts": [{"label": "owner", "role": "basic"}, {"label": "peer", "role": "basic"}],
            "resources": [
                {"label": "owned_record", "type": "record", "owner": "owner", "count": 1,
                 "shared": False, "delegated": False},
                {"label": "peer_record", "type": "record", "owner": "peer", "count": 2,
                 "shared": False, "delegated": False},
            ],
        },
        "environment": {
            "network_mode": "dedicated_network", "llm": api_llm(),
            "temperature": 0, "seed": 42, "target_image": "example/target:15.0.0",
            "worker_count": 1, "context_window_k": 32, "time_budget_seconds": 1200, "request_budget": 100,
            "run_storage": {"engine_db_tmpfs": "1g", "engine_data_tmpfs": "512m", "tmpfs_min_free": "16m"},
            "run_drain": {"grace_seconds": 30, "settle_seconds": 2},
            "sampling": {"top_p": 0.95, "top_k": 20, "min_p": 0.0, "presence_penalty": 1.5, "repetition_penalty": 1.0},
        },
        "scope": {"target_origin": "http://eval-meter:8080", "methods": ["GET", "HEAD"],
                  "paths": ["/api/records/{object}", "/api/status"], "vuln_classes": ["idor", "bola"]},
    }


class TestProfileFingerprint(unittest.TestCase):
    def reject(self, payload, expected):
        with self.assertRaises(ProfileError) as caught:
            generate_fingerprints(payload)
        self.assertIn(expected, str(caught.exception))
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))

    def test_v6_fixed_canonical_vectors(self):
        # v6: 환경에 context_window_k가 들어가고 format 문자열이 바뀌어 세 지문 모두 v5와 다르다.
        self.assertEqual(asdict(generate_fingerprints(profile())), {
            "format": "abc2lab-eval-profile-v6",
            "fixture_fingerprint": "71361e6c23dc6b8dd79b2b05a215bb37914fa67f08dfc7c72f41b10ce6025766",
            "environment_fingerprint": "368701e3c458f026c9942efee20c8740f8ab79eb607ed57eac30620d1272634d",
            "scope_fingerprint": "b277c8844f50f6158cb61a7e3bd3f1d0b1b7ddd5a07d31b0aa8497d178e5191d",
        })

    def test_digest_fields_fit_current_preflight_without_changing_source_pins(self):
        from preflight import check_preflight

        payload = profile()
        fingerprints = asdict(generate_fingerprints(payload))
        controls = payload["environment"] | {"target_origin": payload["scope"]["target_origin"]}
        controls.update({key: value for key, value in fingerprints.items() if key.endswith("_fingerprint")})
        check_preflight({
            "pins": {"baseline_source_sha": "1" * 40, "target_image": controls["target_image"]},
            "baseline": controls | {"source_sha": "1" * 40},
            "ours": controls | {"source_sha": "2" * 40},
        })

    def test_valid_profile_returns_only_typed_digests_and_does_not_mutate_or_execute(self):
        payload = profile()
        before = copy.deepcopy(payload)
        with patch("socket.create_connection", side_effect=AssertionError("network")), \
                patch("subprocess.Popen", side_effect=AssertionError("process")), \
                patch("os.system", side_effect=AssertionError("shell")):
            result = generate_fingerprints(payload)
        self.assertEqual(result.format, FORMAT)
        self.assertEqual(payload, before)
        digests = [result.fixture_fingerprint, result.environment_fingerprint, result.scope_fingerprint]
        self.assertEqual(len(set(digests)), 3)
        self.assertTrue(all(re.fullmatch("[0-9a-f]{64}", digest) for digest in digests))
        text = json.dumps(asdict(result))
        self.assertNotIn("example-model", text)
        self.assertNotIn("llm-api.example", text)
        self.assertNotIn("eval-meter", text)

    def test_dictionary_and_set_array_order_do_not_affect_fingerprints(self):
        payload = profile()
        changed = json.loads(json.dumps(payload), object_pairs_hook=lambda pairs: dict(reversed(pairs)))
        for field in ("accounts", "resources"):
            changed["fixture"][field].reverse()
        for field in ("methods", "paths", "vuln_classes"):
            changed["scope"][field].reverse()
        self.assertEqual(generate_fingerprints(payload), generate_fingerprints(changed))

    def test_equivalent_numeric_representations_match_without_rounding_context(self):
        payload = profile()
        changed = copy.deepcopy(payload)
        changed["environment"].update(temperature=Decimal("-0.000"), time_budget_seconds=Decimal("1.200E+3"))
        self.assertEqual(generate_fingerprints(payload), generate_fingerprints(changed))
        payload["environment"].update(temperature=Decimal("0.1234567890123456789012345678901"))
        expected = generate_fingerprints(payload)
        with localcontext() as context:
            context.prec = 2
            self.assertEqual(expected, generate_fingerprints(payload))
        changed = copy.deepcopy(payload)
        changed["environment"]["temperature"] = Decimal("0.1234567890123456789012345678902")
        self.assertNotEqual(expected.environment_fingerprint, generate_fingerprints(changed).environment_fingerprint)

    def test_each_environment_condition_changes_only_environment_digest(self):
        expected = generate_fingerprints(profile())
        changes = {"llm": api_llm() | {"provider": "other-provider"},
                   "temperature": 0.25, "seed": 43, "target_image": "example/target:16.0.0",
                   "worker_count": 2, "context_window_k": 64, "time_budget_seconds": 600, "request_budget": 101,
                   # tmpfs 하한이 다르면 같은 측정값으로도 완료·무효가 갈린다(#140 리뷰) → 환경 지문이 달라야 한다.
                   "run_storage": {"engine_db_tmpfs": "1g", "engine_data_tmpfs": "512m", "tmpfs_min_free": "32m"},
                   # 봉인 뒤 유예·정착 대기는 post 관측 시점과 취소 여부를 바꾼다(#153 리뷰) → 환경 지문이 달라야 한다.
                   "run_drain": {"grace_seconds": 31, "settle_seconds": 2}}
        cases = [*changes.items(), ("run_drain", {"grace_seconds": 30, "settle_seconds": 3}),
                 # 샘플링은 게이트웨이가 요청마다 넣는 비교 조건이다(평가 프로토콜 §6).
                 ("sampling", {"top_p": 0.8, "top_k": 20, "min_p": 0.0, "presence_penalty": 1.5,
                               "repetition_penalty": 1.0}),
                 ("sampling", {"top_p": 0.95, "top_k": 20, "min_p": 0.0, "presence_penalty": -1.5,
                               "repetition_penalty": 1.0})]
        for field, value in cases:
            payload = profile()
            payload["environment"][field] = value
            result = generate_fingerprints(payload)
            self.assertNotEqual(expected.environment_fingerprint, result.environment_fingerprint, field)
            self.assertEqual(expected.fixture_fingerprint, result.fixture_fingerprint)
            self.assertEqual(expected.scope_fingerprint, result.scope_fingerprint)
        # API 고정 조건과 백엔드 종류가 각각 환경 지문을 바꾼다(baseline·ours 지문 대조의 근거).
        for llm in (api_llm() | {"model": "example-org/other-model"},
                    api_llm() | {"base_url": "https://other-api.example/api/v1"}, local_llm()):
            payload = profile()
            use_llm(payload, llm)
            self.assertNotEqual(expected.environment_fingerprint, generate_fingerprints(payload).environment_fingerprint)

    def test_context_window_stays_in_the_engine_range_and_under_the_server(self):
        for value in (None, True, "32", 31, 1001, 32.0, 0, -1):
            with self.subTest(value=value):
                payload = profile()
                payload["environment"]["context_window_k"] = value
                with self.assertRaisesRegex(ProfileError, "environment.context_window_k: INVALID_CONTEXT_WINDOW"):
                    generate_fingerprints(payload)
        local = profile()
        local["environment"]["llm"] = local_llm() | {"context_length": 64000}
        local["environment"]["sampling"] = {"top_p": 0.95, "presence_penalty": 1.5, "frequency_penalty": 0.0}
        local["environment"]["context_window_k"] = 64  # 경계: 서버 컨텍스트와 정확히 같으면 통과
        generate_fingerprints(local)
        local["environment"]["context_window_k"] = 65
        with self.assertRaisesRegex(ProfileError, "environment.context_window_k: ABOVE_SERVER_CONTEXT"):
            generate_fingerprints(local)
        api = profile()
        api["environment"]["context_window_k"] = 1000  # API는 서버 컨텍스트 선언이 없다
        generate_fingerprints(api)

    def test_each_llm_subfield_changes_only_environment_digest(self):
        # llm 객체가 한 값으로 묶여 비교되더라도, 하위 필드 하나하나가 지문에 들어가야 한다.
        cases = {
            "api.base_url": (api_llm(), {"base_url": "https://llm-api.example/api/v2"}),
            "api.model": (api_llm(), {"model": "example-org/other-model"}),
            "api.provider": (api_llm(), {"provider": "other-provider"}),
            "local.base_url": (local_llm(), {"base_url": "http://llm-host:11435/v1"}),
            "local.model": (local_llm(), {"model": "other-model:fixed-tag"}),
            # hostname은 base_url 호스트와 같아야 하므로 둘을 함께 바꾼다.
            "local.hostname": (local_llm(), {"hostname": "other-llm", "base_url": "http://other-llm:11434/v1"}),
            "local.host_ip": (local_llm(), {"host_ip": "10.0.0.5"}),
            "local.model_digest": (local_llm(), {"model_digest": "b" * 64}),
            "local.context_length": (local_llm(), {"context_length": 65536}),
            "local.server_version": (local_llm(), {"server_version": "0.33.0"}),
        }
        for name, (base, change) in cases.items():
            with self.subTest(field=name):
                payload = profile()
                use_llm(payload, base)
                before = generate_fingerprints(payload)
                use_llm(payload, base | change)
                after = generate_fingerprints(payload)
                self.assertNotEqual(before.environment_fingerprint, after.environment_fingerprint)
                self.assertEqual(before.fixture_fingerprint, after.fixture_fingerprint)
                self.assertEqual(before.scope_fingerprint, after.scope_fingerprint)

    def test_each_scope_condition_changes_scope_and_environment(self):
        expected = generate_fingerprints(profile())
        changes = {"target_origin": "http://eval-meter:8081", "methods": ["GET"],
                   "paths": ["/api/records/{object}"], "vuln_classes": ["idor"]}
        for field, value in changes.items():
            payload = profile()
            payload["scope"][field] = value
            result = generate_fingerprints(payload)
            self.assertNotEqual(expected.scope_fingerprint, result.scope_fingerprint, field)
            self.assertNotEqual(expected.environment_fingerprint, result.environment_fingerprint, field)
            self.assertEqual(expected.fixture_fingerprint, result.fixture_fingerprint)

    def test_recipe_role_and_resource_conditions_change_fixture(self):
        expected = generate_fingerprints(profile())
        changes = []
        for field, value in (("preparation_id", "other_recipe"), ("preparation_revision", 2)):
            payload = profile()
            payload["fixture"][field] = value
            changes.append(payload)
        payload = profile()
        payload["fixture"]["accounts"][1]["role"] = "privileged"
        changes.append(payload)
        for field, value in (("label", "other_record"), ("type", "document"), ("owner", "peer"),
                             ("count", 2), ("shared", True), ("delegated", True)):
            payload = profile()
            payload["fixture"]["resources"][0][field] = value
            changes.append(payload)
        for payload in changes:
            result = generate_fingerprints(payload)
            self.assertNotEqual(expected.fixture_fingerprint, result.fixture_fingerprint)
            self.assertEqual(expected.environment_fingerprint, result.environment_fingerprint)

    def test_explicit_anonymous_fixture_without_resources_is_supported(self):
        payload = profile()
        payload["fixture"].update(accounts=[], resources=[])
        self.assertRegex(generate_fingerprints(payload).fixture_fingerprint, "[0-9a-f]{64}")

    def test_missing_or_wrong_root_sections_and_versions_fail(self):
        for value in (None, True, 1, [], "SYNTHETIC-PRIVATE"):
            self.reject(value, "EXPECTED_OBJECT")
        for field in profile():
            payload = profile()
            del payload[field]
            self.reject(payload, "MISSING_FIELDS")
        for value in (None, True, 0, 1, 2, 3, 4, 6, "5"):  # v4(sampling 없음)·v3(run_drain 없음)·v2는 v5와 섞지 않는다
            self.reject(profile() | {"version": value}, "UNSUPPORTED_VERSION")
        for field in ("fixture", "environment", "scope"):
            payload = profile()
            payload[field] = []
            self.reject(payload, "EXPECTED_OBJECT")

    def test_missing_fields_at_each_schema_boundary_fail(self):
        for section in ("fixture", "environment", "scope"):
            for field in profile()[section]:
                payload = profile()
                del payload[section][field]
                self.reject(payload, "MISSING_FIELDS")
        for section in ("accounts", "resources"):
            for field in profile()["fixture"][section][0]:
                payload = profile()
                del payload["fixture"][section][0][field]
                self.reject(payload, "MISSING_FIELDS")

    def test_forbidden_metadata_and_private_fields_fail_before_any_hash(self):
        forbidden = ("password", "token", "cookie", "authorization", "username", "account_id", "canary",
                     "source_sha", "run_id", "reset_generation", "instance_id", "execution_id", "artifact_path",
                     "SYNTHETIC-PRIVATE-KEY")
        for section in (None, "fixture", "environment", "scope", "accounts", "resources"):
            for field in forbidden:
                payload = profile()
                if section is None:
                    owner = payload
                elif section in ("accounts", "resources"):
                    owner = payload["fixture"][section][0]
                else:
                    owner = payload[section]
                owner[field] = "SYNTHETIC-PRIVATE-VALUE"
                with patch("profile_fingerprint.hashlib.sha256", side_effect=AssertionError("hashed invalid data")):
                    self.reject(payload, "UNKNOWN_FIELDS")

    def test_bad_accounts_resources_and_reference_links_are_rejected(self):
        for field in ("accounts", "resources"):
            for value in (None, {}, [None]):
                payload = profile()
                payload["fixture"][field] = value
                self.reject(payload, "EXPECTED_")
            payload = profile()
            payload["fixture"][field].append(payload["fixture"][field][0].copy())
            self.reject(payload, "DUPLICATE")
        payload = profile()
        payload["fixture"]["resources"][0]["owner"] = "unknown"
        self.reject(payload, "UNKNOWN_ACCOUNT_LABEL")
        payload["fixture"]["resources"][0]["owner"] = "owner@example.invalid"
        self.reject(payload, "EXPECTED_LOGICAL_LABEL")
        for field in ("shared", "delegated"):
            for value in (None, 0, "false"):
                payload = profile()
                payload["fixture"]["resources"][0][field] = value
                self.reject(payload, "EXPECTED_BOOL")

    def test_logical_labels_exclude_original_account_urls_and_control_characters(self):
        for value in (None, 1, "", "user@example.invalid", "account/123", "http://target.invalid", "with space",
                      "UPPERCASE", "x" * 65, "bad\nlabel", "한글"):
            payload = profile()
            payload["fixture"]["accounts"][0]["label"] = value
            self.reject(payload, "EXPECTED_LOGICAL_LABEL")

    def test_scope_arrays_must_be_nonempty_valid_and_unique(self):
        for field in ("methods", "paths", "vuln_classes"):
            for value in (None, [], {}, "GET"):
                payload = profile()
                payload["scope"][field] = value
                self.reject(payload, "EXPECTED_NONEMPTY_ARRAY")
            payload = profile()
            payload["scope"][field].append(payload["scope"][field][0])
            self.reject(payload, "DUPLICATE")
        for value in (None, [], "get", "CONNECT", "SYNTHETIC-PRIVATE"):
            payload = profile()
            payload["scope"]["methods"] = [value]
            self.reject(payload, "INVALID_METHOD")

    def test_paths_are_explicit_templates_without_query_credentials_or_traversal(self):
        for value in (None, "", "relative", "//host/path", "/x?token=SYNTHETIC-PRIVATE", "/x#fragment",
                      "/x%2fy", "/../x", "/x/./y", "/x\\y", "/x/*", "/x/{BAD}", "/x/{id}.json",
                      "/x//y", "/x//", "/x\n", "/x/" + "a" * 512):
            payload = profile()
            payload["scope"]["paths"] = [value]
            self.reject(payload, "INVALID_PATH_TEMPLATE")
        payload = profile()
        payload["scope"]["paths"] = ["/", "/v1/items/{item}/", "/static-name.json"]
        generate_fingerprints(payload)

    def test_target_origin_must_be_the_meter_service_origin(self):
        for value in (None, "", "http://127.0.0.1:3000", "http://localhost:3000", "https://eval-meter:8080",
                      "http://eval-meter", "http://eval-meter:8080/v1", "http://eval-meter.example:8080",
                      "http://Eval-Meter:8080", "http://user:SYNTHETIC-PRIVATE@eval-meter:8080",
                      "http://eval-meter:8080?", "http://eval-meter:0", "http://eval-meter:8080\n"):
            payload = profile()
            payload["scope"]["target_origin"] = value
            self.reject(payload, "INVALID_TARGET_ORIGIN")
        payload = profile()
        payload["scope"]["target_origin"] = "http://eval-meter:8080/"
        generate_fingerprints(payload)

    def test_network_mode_and_llm_object_are_strict(self):
        for value in (None, "loopback_only", "", 1):
            payload = profile()
            payload["environment"]["network_mode"] = value
            self.reject(payload, "UNSUPPORTED_NETWORK_MODE")
        for value in (None, [], api_llm() | {"fallbacks": True}, api_llm() | {"base_url": "http://llm-api.example/v1"},
                      api_llm() | {"model": "example-model"}, api_llm() | {"hostname": "llm-host"},
                      local_llm() | {"host_ip": "127.0.0.1"}, local_llm() | {"host_ip": "203.0.113.5"},
                      local_llm() | {"model_digest": "A" * 64}, local_llm() | {"model_digest": "a" * 63},
                      local_llm() | {"context_length": 0}, local_llm() | {"context_length": True},
                      local_llm() | {"context_length": 5 * 1024 * 1024}, local_llm() | {"server_version": "latest"},
                      {k: v for k, v in local_llm().items() if k != "context_length"},
                      local_llm() | {"base_url": "http://other-host:11434/v1"}, api_llm() | {"backend": ["api"]},
                      api_llm() | {"model": "SYNTHETIC-PRIVATE/latest"}):
            payload = profile()
            payload["environment"]["llm"] = value
            self.reject(payload, "INVALID_LLM")
        payload = profile()
        use_llm(payload, local_llm())
        generate_fingerprints(payload)

    def test_local_sampling_is_its_own_set_inside_the_environment_digest(self):
        payload = profile()
        use_llm(payload, local_llm())
        before = generate_fingerprints(payload)
        for field, value in (("top_p", 0.9), ("presence_penalty", 1.0), ("frequency_penalty", 1.0)):
            changed = copy.deepcopy(payload)
            changed["environment"]["sampling"][field] = value
            after = generate_fingerprints(changed)
            self.assertNotEqual(before.environment_fingerprint, after.environment_fingerprint, field)
            self.assertEqual(before.fixture_fingerprint, after.fixture_fingerprint, field)
            self.assertEqual(before.scope_fingerprint, after.scope_fingerprint, field)
        api_set = profile()["environment"]["sampling"]
        for sampling in (api_set, {}, {**LOCAL_SAMPLING, "top_k": 20},
                         {k: v for k, v in LOCAL_SAMPLING.items() if k != "frequency_penalty"}):
            wrong = copy.deepcopy(payload)
            wrong["environment"]["sampling"] = sampling
            self.reject(wrong, "INVALID_SAMPLING")
        wrong = profile()
        wrong["environment"]["sampling"] = dict(LOCAL_SAMPLING)
        self.reject(wrong, "INVALID_SAMPLING")
        for field, value in (("top_p", 0), ("top_p", 1.5), ("presence_penalty", 3), ("frequency_penalty", -3)):
            wrong = copy.deepcopy(payload)
            wrong["environment"]["sampling"][field] = value
            self.reject(wrong, "INVALID_NUMBER")

    def test_run_storage_validation_does_not_drift(self):
        # tmpfs 크기·하한: 프로파일 지문·사전검사·러너 설정이 같은 값을 받아야 선언과 실제 compose·판정이 어긋나지 않는다.
        import runner_config
        sizes = ("1", "16", "16k", "16m", "1g", "999999999", "999999999g", "0", "016", "1G", "1.5m", "1mb", "-1", "",
                 " 1m", "1m ", "1t", "1000000000")
        for text in sizes:
            with self.subTest(size=text):
                ok = bool(profile_fingerprint._STORAGE_SIZE.fullmatch(text))
                self.assertEqual(ok, bool(preflight._STORAGE_SIZE.fullmatch(text)))
                self.assertEqual(ok, bool(runner_config._TMPFS_SIZE.fullmatch(text)))
                if ok:
                    self.assertEqual(profile_fingerprint._storage_bytes(text), runner_config.size_bytes(text))
                    self.assertEqual(preflight._storage_bytes(text), runner_config.size_bytes(text))
        for db, data, low in itertools.product(("1g", "512m", "16m"), ("512m", "16m", "1024k"), ("8m", "16m", "512m")):
            value = {"engine_db_tmpfs": db, "engine_data_tmpfs": data, "tmpfs_min_free": low}
            with self.subTest(value=value):
                self.assertEqual(profile_fingerprint._run_storage(value), preflight._run_storage(value))
                expected = runner_config.size_bytes(low) < min(runner_config.size_bytes(db), runner_config.size_bytes(data))
                self.assertEqual(profile_fingerprint._run_storage(value), expected)
        for bad in (None, [], {}, {"engine_db_tmpfs": "1g", "engine_data_tmpfs": "512m"},
                    {"engine_db_tmpfs": "1g", "engine_data_tmpfs": "512m", "tmpfs_min_free": "8m", "extra": "1"},
                    {"engine_db_tmpfs": 1, "engine_data_tmpfs": "512m", "tmpfs_min_free": "8m"}):
            with self.subTest(bad=bad):
                self.assertFalse(profile_fingerprint._run_storage(bad))
                self.assertFalse(preflight._run_storage(bad))

    def test_run_drain_validation_does_not_drift(self):
        # 유예·정착 대기: 프로파일 지문·사전검사·러너 설정·계측기가 같은 범위를 받아야 선언한 정책으로 회차가 돈다.
        import runner_config
        self.assertEqual(profile_fingerprint.MAX_DRAIN_GRACE_SECONDS, runner_config.MAX_DRAIN_GRACE_SECONDS)
        self.assertEqual(preflight.MAX_DRAIN_GRACE_SECONDS, runner_config.MAX_DRAIN_GRACE_SECONDS)
        self.assertEqual(profile_fingerprint.MAX_SETTLE_SECONDS, runner_config.MAX_RUN_SECONDS)
        self.assertEqual(preflight.MAX_SETTLE_SECONDS, runner_config.MAX_RUN_SECONDS)
        for grace in (0, 1, 30, 600, 601, True, 1.0, "30", None, -1):
            for settle in (0, 1, 2, 3600, 3601, False, 2.0):
                value = {"grace_seconds": grace, "settle_seconds": settle}
                with self.subTest(value=value):
                    expected = (type(grace) is int and 1 <= grace <= 600 and type(settle) is int
                                and 1 <= settle <= 3600)
                    self.assertEqual(profile_fingerprint._run_drain(value), expected)
                    self.assertEqual(preflight._run_drain(value), expected)
        for bad in (None, [], {}, {"grace_seconds": 30}, {"grace_seconds": 30, "settle_seconds": 2, "extra": 1}):
            with self.subTest(bad=bad):
                self.assertFalse(profile_fingerprint._run_drain(bad))
                self.assertFalse(preflight._run_drain(bad))
        payload = profile()
        payload["environment"]["run_drain"] = {"grace_seconds": 0, "settle_seconds": 2}
        self.reject(payload, "INVALID_RUN_DRAIN")
        del payload["environment"]["run_drain"]
        self.reject(payload, "MISSING_FIELDS")

    def test_sampling_validation_does_not_drift(self):
        # 다섯 값: 프로파일 지문·사전검사·게이트웨이(sampling.go)가 같은 범위를 받아야 선언한 샘플링으로 회차가 돈다.
        go = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/sampling.go").read_text(encoding="utf-8")
        self.assertIn(f"maxTopK              = {profile_fingerprint.MAX_TOP_K}\n", go)
        self.assertIn(f"maxPenalty           = {profile_fingerprint.MAX_PENALTY}\n", go)
        self.assertEqual((preflight.MAX_TOP_K, preflight.MAX_PENALTY, preflight.SAMPLING_FIELDS),
                         (profile_fingerprint.MAX_TOP_K, profile_fingerprint.MAX_PENALTY,
                          profile_fingerprint.SAMPLING_FIELDS))
        self.assertEqual(preflight.LOCAL_SAMPLING_FIELDS, profile_fingerprint.LOCAL_SAMPLING_FIELDS)
        # 게이트웨이가 local에 넣는 키는 선언의 세 값과 temperature·seed다.
        struct = go[go.index("type localSamplingParams struct"):go.index("// parseLocalSampling")]
        self.assertEqual(set(re.findall(r'json:"(\w+)"', struct)),
                         {"temperature", "seed", *profile_fingerprint.LOCAL_SAMPLING_FIELDS})
        base = {"top_p": 0.95, "top_k": 20, "min_p": 0.0, "presence_penalty": 1.5, "repetition_penalty": 1.0}
        changes = {"top_p": (0, 0.0001, 1, 1.0001, -1, True, "0.9", None, float("nan")),
                   "top_k": (0, 1, 1000, 1001, 20.0, True, "20", -1),
                   "min_p": (-0.0001, 0, 1, 1.0001, False, float("inf")),
                   "presence_penalty": (-2.0001, -2, 0, 2, 2.0001, "1.5"),
                   "repetition_penalty": (0, 0.0001, 2, 2.0001, -1, None)}
        for name, values in changes.items():
            for value in values:
                case = dict(base, **{name: value})
                with self.subTest(field=name, value=value):
                    payload = profile()
                    payload["environment"]["sampling"] = case
                    try:
                        generate_fingerprints(payload)
                        accepted = True
                    except ProfileError:
                        accepted = False
                    self.assertEqual(accepted, preflight._sampling(case))
        for bad in (None, [], {}, dict(base, extra=1), {k: v for k, v in base.items() if k != "min_p"}):
            with self.subTest(bad=bad):
                self.assertFalse(preflight._sampling(bad))
                payload = profile()
                payload["environment"]["sampling"] = bad
                self.reject(payload, "INVALID_SAMPLING")

    def test_url_and_llm_validation_does_not_drift_from_preflight(self):
        # 두 검증기는 같은 정의 블록을 갖는다. 소스가 갈라지면 여기서 먼저 걸린다.
        def block(module):
            source = Path(module.__file__).read_text(encoding="utf-8")
            start = source.index("def _split_url(")
            return source[start:source.index("\ndef ", source.index("def _llm(") + 1)]
        self.assertEqual(block(preflight), block(profile_fingerprint))
        for name in ("_NETWORK_MODES", "_LLM_FIELDS", "MAX_URL_LENGTH"):
            self.assertEqual(getattr(preflight, name), getattr(profile_fingerprint, name), name)
        for name in ("_DNS_LABEL", "_MODEL_SEGMENT", "_PROVIDER", "_PLACEHOLDER"):
            self.assertEqual(getattr(preflight, name).pattern, getattr(profile_fingerprint, name).pattern, name)
        self.assertEqual(preflight._LOCAL_NETWORKS, profile_fingerprint._LOCAL_NETWORKS)
        # 게이트웨이(Go)가 요청에 그대로 넣는 slug와 같은 문법이어야 선언과 실제 고정이 어긋나지 않는다.
        gateway = (Path(__file__).resolve().parents[3] / "cmd" / "eval-meter" / "gateway.go").read_text(encoding="utf-8")
        go_pattern = re.search(r"providerSlugRe\s*=\s*regexp\.MustCompile\(`\^(.*)\$`\)", gateway).group(1)
        self.assertEqual(go_pattern.replace("(/", "(?:/") + "\\Z", preflight._PROVIDER.pattern)
        origins = ("http://eval-meter:8080", "http://eval-meter:8080/", "http://127.0.0.1:3000",
                   "http://eval-meter", "https://eval-meter:8080", "http://eval-meter:8080/${X}",
                   "http://eval-meter:8080/<v1>", "http://todo:8080", "http://eval-meter:8080" + "/" * 600)
        for value in origins:
            with self.subTest(origin=value):
                self.assertEqual(preflight._meter_origin(value), profile_fingerprint._meter_origin(value))
        llms = (api_llm(), local_llm(), api_llm() | {"fallbacks": True}, local_llm() | {"host_ip": "10.0.0.1"},
                local_llm() | {"host_ip": "127.0.0.1"}, api_llm() | {"provider": "todo"},
                api_llm() | {"provider": "deepinfra/fp8"}, api_llm() | {"provider": "deepinfra/fp8/x"},
                api_llm() | {"model": "example-org/example-model:free"}, {"backend": "api"}, None)
        for value in llms:
            with self.subTest(llm=value):
                self.assertEqual(preflight._llm(value), profile_fingerprint._llm(value))

    def test_model_and_image_are_explicit_non_latest_references(self):
        for value in (None, "", "example", "example:latest", "example:LATEST", "example:TODO", "<model>", "example:with space"):
            payload = profile()
            payload["environment"]["target_image"] = value
            self.reject(payload, "EXPECTED_PINNED_REFERENCE")
        for image in ("example:latest@sha256:" + "a" * 64, "example@sha256:" + "a" * 63):
            payload = profile()
            payload["environment"]["target_image"] = image
            self.reject(payload, "EXPECTED_PINNED_REFERENCE")
        payload = profile()
        payload["environment"]["target_image"] = "registry.example:5000/target:15.0.0@sha256:" + "a" * 64
        generate_fingerprints(payload)

    def test_integer_fields_reject_bool_float_negative_and_out_of_range(self):
        for field in ("seed", "worker_count", "request_budget"):
            for value in (None, True, "1", -1, 1.0, 1 << 63):
                payload = profile()
                payload["environment"][field] = value
                self.reject(payload, "INVALID_INTEGER")
        for path in ("preparation_revision", "count"):
            payload = profile()
            owner = payload["fixture"] if path == "preparation_revision" else payload["fixture"]["resources"][0]
            owner[path] = 0
            self.reject(payload, "INVALID_INTEGER")
        payload = profile()
        payload["environment"]["seed"] = 0
        generate_fingerprints(payload)

    def test_numeric_fields_reject_unknown_nonfinite_and_excessive_precision(self):
        for field in ("temperature", "time_budget_seconds"):
            for value in (None, True, "0.1", -1, float("nan"), float("inf"), Decimal("NaN"),
                          Decimal("1e9999"), Decimal("1e-9999"), Decimal("0." + "1" * 129)):
                payload = profile()
                payload["environment"][field] = value
                self.reject(payload, ": ")
        payload = profile()
        payload["environment"]["time_budget_seconds"] = 0
        self.reject(payload, "INVALID_NUMBER")


class TestProfileCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "profile.json"
        self.path.write_text(json.dumps(profile()), encoding="utf-8")

    def cli(self, *extra, include_profile=True):
        script = Path(__file__).resolve().parents[1] / "profile_fingerprint.py"
        arguments = [sys.executable, "-I", "-B", str(script)]
        if include_profile:
            arguments.append(str(self.path))
        return subprocess.run([*arguments, *extra],
                              capture_output=True, text=True, check=False)

    def test_invalid_cli_arguments_never_echo_values(self):
        cases = [self.cli("--token=SYNTHETIC-PRIVATE"), self.cli("SYNTHETIC-PRIVATE"), self.cli(include_profile=False)]
        for result in cases:
            self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "cli: INVALID_ARGUMENTS\n"))

    def test_cli_only_outputs_digests_and_does_not_write_or_modify_files(self):
        before = self.path.read_bytes()
        result = self.cli()
        self.assertEqual((result.returncode, result.stderr), (0, ""))
        self.assertEqual(json.loads(result.stdout), asdict(generate_fingerprints(profile())))
        self.assertNotIn("example-model", result.stdout)
        self.assertNotIn("127.0.0.1", result.stdout)
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual(list(self.path.parent.iterdir()), [self.path])

    def test_cli_retains_decimal_precision_beyond_float_precision(self):
        text = self.path.read_text(encoding="utf-8")
        self.path.write_text(text.replace('"temperature": 0', '"temperature": 0.1234567890123456789012345678901'), encoding="utf-8")
        expected = profile()
        expected["environment"]["temperature"] = Decimal("0.1234567890123456789012345678901")
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), asdict(generate_fingerprints(expected)))

    def test_cli_never_prints_partial_digests_or_private_input_on_failure(self):
        payload = profile()
        payload["environment"]["SYNTHETIC-PRIVATE-KEY"] = "SYNTHETIC-PRIVATE-VALUE"
        self.path.write_text(json.dumps(payload), encoding="utf-8")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout), (2, ""))
        self.assertEqual(result.stderr, "environment: UNKNOWN_FIELDS\n")

    def test_invalid_utf8_or_malformed_json_is_sanitized(self):
        for raw in (b"\xff", b'{"SYNTHETIC-PRIVATE":', b"[" * 2000 + b"]" * 2000):
            self.path.write_bytes(raw)
            result = self.cli()
            self.assertEqual((result.returncode, result.stdout), (2, ""))
            self.assertNotIn("SYNTHETIC-PRIVATE", result.stderr)
            self.assertNotIn("Traceback", result.stderr)

    def test_duplicate_valid_temperature_is_rejected_with_exact_sanitized_code(self):
        raw = json.dumps(profile(), separators=(",", ":"))
        raw = raw.replace('"temperature":0', '"temperature":0,"temperature":0.7', 1)
        self.path.write_text(raw, encoding="utf-8")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout, result.stderr),
                         (2, "", "json: DUPLICATE_KEY\n"))

    def test_nonfinite_valid_temperature_is_rejected_with_exact_sanitized_code(self):
        raw = json.dumps(profile(), separators=(",", ":"))
        raw = raw.replace('"temperature":0', '"temperature":NaN', 1)
        self.path.write_text(raw, encoding="utf-8")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout, result.stderr),
                         (2, "", "json: NONFINITE_NUMBER\n"))

    def test_size_limit_and_missing_files_return_sanitized_exit_two(self):
        before = self.path.read_bytes()
        self.path.write_bytes(before + b" " * (MAX_INPUT_BYTES - len(before)))
        self.assertEqual(self.cli().returncode, 0)
        with self.path.open("ab") as stream:
            stream.write(b" ")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "json: TOO_LARGE\n"))
        self.path.unlink()
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "json: READ_FAILED\n"))


if __name__ == "__main__":
    unittest.main()
