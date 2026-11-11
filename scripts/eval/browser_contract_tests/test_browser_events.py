"""Contract examples and deliberately contradictory producer events (no Browser run)."""
import copy
from datetime import datetime
import re
import json
from pathlib import Path
import unittest

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[3] / "docs" / "contracts"
SCHEMA = json.loads((ROOT / 'browser-event.schema.json').read_text())
FORMATS = FormatChecker()


@FORMATS.checks('date-time')
def utc_date_time(value):
    # jsonschema's optional RFC3339 dependency is not available on every host.
    if not isinstance(value, str):
        return True
    if not re.fullmatch(SCHEMA['properties']['at']['pattern'], value):
        return False
    try:
        datetime.strptime(value[:19], "%Y-%m-%dT%H:%M:%S")
        return True
    except ValueError:
        return False


VALIDATOR = Draft202012Validator(SCHEMA, format_checker=FORMATS)
READ = json.loads((ROOT / 'examples/browser-events.v1.json').read_text())
WRITE = json.loads((ROOT / 'examples/browser-events.approved-writes.v1.json').read_text())


class BrowserEventsContractTest(unittest.TestCase):
    def test_examples(self):
        Draft202012Validator.check_schema(SCHEMA)
        for event in READ + WRITE:
            with self.subTest(event=event['event_id']):
                VALIDATOR.validate(event)

    def test_example_stream_conservation(self):
        for events in [READ, WRITE]:
            observed = {e['request_ref'] for e in events if e['kind'] == 'request.observed'}
            terminals = [e for e in events if e['kind'] in ['attempt.result', 'ingress.rejected']]
            self.assertEqual(observed, {e['request_ref'] for e in terminals})
            self.assertEqual(len(observed), len(terminals))
            self.assertEqual([str(i) for i in range(1, len(events) + 1)], [e['seq'] for e in events])
            counts = events[-1]['payload']['counts']
            self.assertEqual(int(counts['ingress_rejected']), sum(e['kind'] == 'ingress.rejected' for e in events))
            self.assertEqual(int(counts['ingress_rejected']), sum(int(n) for n in counts['ingress_by_reason'].values()))
            self.assertEqual(len(observed), int(counts['application_attempt_results']) + int(counts['application_ingress_rejected']) + int(counts['unresolved_requests']))
            results = [e['payload'] for e in events if e['kind'] == 'attempt.result']
            self.assertEqual(int(counts['attempt_results']), len(results))
            self.assertEqual(int(counts['forward_attempted']), sum(r['forward_attempted'] for r in results))
            self.assertEqual(int(counts['local_write_blocked']), sum(
                e['kind'] == 'ingress.rejected' and e['payload']['reason'] == 'crawl_write_blocked'
                for e in events))

    def test_rejects_contradictions(self):
        cases = [
            (READ[0], {'at': 'notadateZ'}),
            (READ[0], {'at': '2026-02-30T00:00:00Z'}),
            (READ[0], {'at': '2026-11-11T24:00:00Z'}),
            (READ[0], {'at': '2026-11-11T00:00:00+00:00'}),
        ]
        for status in ['failed', 'timed_out', 'cancelled']:
            cases.append((READ[6], {'payload.status': status}))
        for status in ['stopped', 'failed']:
            cases.append((READ[8], {'payload.status': status}))
        for change in [
            {'payload.reason': 'policy_denied'},
            {'payload.decision': 'unsupported'},
            {'payload.not_sent': True},
            {'payload.forward_attempted': False},
            {'payload.response_received': False},
            {'payload.response_complete': False},
            {'payload.method': 'CONNECT'},
        ]:
            cases.append((READ[3], change))
        cases += [
            (WRITE[0], {'payload.reset_proof_ref': None}),
            (WRITE[0], {'payload.test_environment_ref': None}),
            (READ[0], {'payload.browser_profile.ca_sha256': None}),
            (READ[0], {'payload.browser_profile.chromium_flags': ['--proxy-server=secret']}),
            (READ[0], {'payload.browser_profile.cookie_storage': 'enabled'}),
            (READ[0], {'payload.browser_profile.service_workers': 'allowed'}),
            (READ[0], {'payload.browser_profile.certificate_errors_ignored': True}),
            (READ[0], {'payload.browser_profile.chromium_flags': ['--ignore-certificate-errors']}),
            (READ[0], {'payload.browser_profile.certificate_trust': 'spki_list', 'payload.browser_profile.ca_sha256': None, 'payload.browser_profile.spki_list_sha256': 'b' * 64}),
            (READ[3], {'payload.not_sent': False, 'payload.forward_attempted': False}),
            (READ[3], {'payload.reason': 'audit_failed', 'payload.audit_complete': True}),
            (READ[3], {'payload.reason': 'transport_failed'}),
            (READ[3], {'payload.reason': 'closed'}),
            (READ[8], {'payload.counts.local_write_blocked': None}),
            (READ[8], {'payload.event_log_complete': False}),
        ]
        for status, reason in [('stopped', 'user_stop'), ('failed', 'driver_failed')]:
            for flag in ['process_drained', 'proxy_drained', 'audit_complete', 'event_log_complete', 'host_correlation_complete']:
                cases.append((READ[8], {'payload.status': status, 'payload.reason': reason, 'payload.' + flag: False}))
        for key in ['seq', 'intent_id', 'payload.counts.observed_requests', 'payload.counts.ingress_by_reason.scope_blocked']:
            cases.append((READ[8], {key: '18446744073709551616'}))
        for original, changes in cases:
            event = copy.deepcopy(original)
            for key, value in changes.items():
                target = event
                parts = key.split('.')
                for part in parts[:-1]:
                    target = target[part]
                target[parts[-1]] = value
            with self.subTest(changes=changes):
                self.assertTrue(list(VALIDATOR.iter_errors(event)))

    def test_receipt_outcomes_preserve_uncertainty(self):
        # These actual E② shapes must not be forced into a fabricated not-sent result.
        for decision, reason, sent, response in [
            ('allowed', 'closed', False, False),
            ('allowed', 'transport_failed', False, False),
            ('allowed', 'unknown', True, False),
            ('allowed', 'unknown', True, True),
            ('denied', 'revoked', False, False),
            ('unsupported', 'unsupported', False, False),
        ]:
            event = copy.deepcopy(READ[3])
            event['payload'].update(decision=decision, reason=reason,
                                    forward_attempted=sent, request_written=sent,
                                    response_received=response, response_complete=False,
                                    not_sent=not sent, status_code=200 if response else None)
            with self.subTest(reason=reason, response=response):
                VALIDATOR.validate(event)

    def test_local_blocks_do_not_invent_attempts(self):
        for reason in ['crawl_write_blocked', 'preflight_blocked', 'dangerous_request',
                       'queue_full', 'queue_timeout', 'scope_blocked']:
            event = copy.deepcopy(READ[5])
            event['payload']['reason'] = reason
            VALIDATOR.validate(event)
            event['attempt_ref'] = 'invented_attempt'
            self.assertTrue(list(VALIDATOR.iter_errors(event)))

    def test_external_connect_is_locally_rejected_without_an_e2_attempt(self):
        event = copy.deepcopy(READ[5])
        event['request_ref'] = None
        event['payload'].update(layer='proxy_transport', reason='scope_blocked')
        VALIDATOR.validate(event)
        event['attempt_ref'] = 'invented-attempt'
        self.assertTrue(list(VALIDATOR.iter_errors(event)))

    def test_uint64_boundary_and_target_status_are_preserved(self):
        event = copy.deepcopy(READ[3])
        event['seq'] = '18446744073709551615'
        event['payload']['status_code'] = 999
        for at in ['2026-11-11T00:00:00Z', '2026-11-11T00:00:00.1Z', '2026-11-11T00:00:00.123456789Z']:
            event['at'] = at
            VALIDATOR.validate(event)
        event['seq'] = '18446744073709551616'
        self.assertTrue(list(VALIDATOR.iter_errors(event)))

    def test_failed_result_ack_does_not_rewrite_wire_outcome(self):
        # Call.finish records the result, sets AuditComplete=false on ACK failure,
        # and retains the successful wire Reason=complete. It is not a clean close.
        event = copy.deepcopy(READ[3])
        event['payload']['audit_complete'] = False
        VALIDATOR.validate(event)
        closed = copy.deepcopy(READ[8])
        closed['payload'].update(status='incomplete', audit_complete=False)
        VALIDATOR.validate(closed)
        closed['payload']['status'] = 'completed'
        self.assertTrue(list(VALIDATOR.iter_errors(closed)))

    def test_rejection_counts_and_exploration_conditions_are_closed(self):
        event = copy.deepcopy(READ[8])
        event['payload']['counts']['ingress_by_reason']['invented_reason'] = '0'
        self.assertTrue(list(VALIDATOR.iter_errors(event)))
        del event['payload']['counts']['ingress_by_reason']['invented_reason']
        event['payload']['exploration_limits'] = ['guessed_from_unknown_count']
        self.assertTrue(list(VALIDATOR.iter_errors(event)))
        event['payload']['exploration_limits'] = []
        VALIDATOR.validate(event)


if __name__ == '__main__':
    unittest.main()
