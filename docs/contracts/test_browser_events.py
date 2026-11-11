"""Contract examples and deliberately contradictory producer events (no Browser run)."""
import copy
from datetime import datetime
import re
import json
from pathlib import Path
import unittest

from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).parent
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
        datetime.fromisoformat(value)
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
            (READ[8], {'payload.counts.local_write_blocked': None}),
            (READ[8], {'payload.event_log_complete': False}),
        ]
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
                       'queue_full', 'queue_timeout']:
            event = copy.deepcopy(READ[5])
            event['payload']['reason'] = reason
            VALIDATOR.validate(event)
            event['attempt_ref'] = 'invented_attempt'
            self.assertTrue(list(VALIDATOR.iter_errors(event)))


if __name__ == '__main__':
    unittest.main()
