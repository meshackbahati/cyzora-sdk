import hashlib
import hmac
import json
import unittest

from cyzora import Cyzora, CyzoraError, normalize_phone, to_cents, from_cents, PAYOUT_MIN_CENTS


def sign(secret: str, body: bytes) -> str:
    return hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


class HelpersTest(unittest.TestCase):
    def test_normalize_phone(self):
        self.assertEqual(normalize_phone("0712345678"), "254712345678")
        self.assertEqual(normalize_phone("+254712345678"), "254712345678")
        self.assertEqual(normalize_phone("254712345678"), "254712345678")
        self.assertEqual(normalize_phone("712345678"), "254712345678")
        self.assertEqual(normalize_phone("0110123456"), "254110123456")
        self.assertIsNone(normalize_phone("12345"))
        self.assertIsNone(normalize_phone(""))

    def test_cents(self):
        self.assertEqual(to_cents(2500), 250000)
        self.assertEqual(to_cents(2500.4), 250000)  # rounds to whole shillings
        self.assertEqual(from_cents(250000), 2500.0)
        with self.assertRaises(CyzoraError):
            to_cents(0)

    def test_payout_minimum_constant(self):
        self.assertEqual(PAYOUT_MIN_CENTS, 3000)


class WebhookTest(unittest.TestCase):
    def setUp(self):
        self.client = Cyzora("sk_live_test", webhook_secret=["whsec_a", "whsec_b"])

    def test_verify_matches_any_secret(self):
        body = json.dumps({"event": "charge.succeeded", "data": {"id": "ch_1"}}).encode()
        self.assertTrue(self.client.verify_webhook_signature(body, sign("whsec_b", body)))
        self.assertTrue(self.client.verify_webhook_signature(body, sign("whsec_a", body)))

    def test_verify_rejects_bad_signature(self):
        body = b'{"event":"charge.succeeded"}'
        self.assertFalse(self.client.verify_webhook_signature(body, sign("whsec_wrong", body)))
        self.assertFalse(self.client.verify_webhook_signature(body, ""))
        self.assertFalse(self.client.verify_webhook_signature(b"", "deadbeef"))

    def test_construct_event_parses_when_valid(self):
        payload = {"event": "payout.paid", "data": {"id": "po_1", "amount": 150000}}
        body = json.dumps(payload).encode()
        event = self.client.construct_webhook_event(body, sign("whsec_a", body))
        self.assertEqual(event["event"], "payout.paid")
        self.assertEqual(event["data"]["amount"], 150000)

    def test_construct_event_rejects_bad_signature(self):
        body = b'{"event":"charge.succeeded"}'
        with self.assertRaises(CyzoraError):
            self.client.construct_webhook_event(body, sign("nope", body))

    def test_construct_event_rejects_bad_json(self):
        body = b"not json"
        with self.assertRaises(CyzoraError):
            self.client.construct_webhook_event(body, sign("whsec_a", body))


class ConstructionTest(unittest.TestCase):
    def test_requires_secret_key(self):
        with self.assertRaises(CyzoraError):
            Cyzora("")

    def test_single_secret_string(self):
        c = Cyzora("sk_live_x", webhook_secret="whsec_only")
        body = b'{"event":"charge.failed"}'
        self.assertTrue(c.verify_webhook_signature(body, sign("whsec_only", body)))


if __name__ == "__main__":
    unittest.main()
