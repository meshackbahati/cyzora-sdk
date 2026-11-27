import hashlib
import hmac
import json
import os
import unittest
from unittest.mock import patch

from cyzora import (
    Cyzora,
    CyzoraError,
    from_cents,
    normalize_phone,
    to_cents,
    to_payout_cents,
    PAYOUT_MIN_CENTS,
    WEBHOOK_EVENTS,
)


def sign(secret: str, body: bytes) -> str:
    return hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


class FakeResponse:
    def __init__(self, body, status=200):
        self.body = body.encode() if isinstance(body, str) else body
        self.status = status

    def read(self):
        return self.body

    def getcode(self):
        return self.status

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


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
        # The docs require odd cents to be rounded up to the next shilling.
        self.assertEqual(to_cents(2500.4), 250100)
        self.assertEqual(from_cents(250000), 2500.0)
        with self.assertRaises(CyzoraError):
            to_cents(0)

    def test_payout_cents_rejects_fractional_shillings(self):
        self.assertEqual(to_payout_cents(1500), 150000)
        for amount in (1500.5, 1500.01):
            with self.assertRaises(CyzoraError):
                to_payout_cents(amount)

    def test_payout_minimum_constant(self):
        self.assertEqual(PAYOUT_MIN_CENTS, 3000)

    def test_webhook_events_include_test_event(self):
        self.assertEqual(WEBHOOK_EVENTS["webhook_test"], "webhook.test")


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
        payload = {"event": "payout.paid", "charge_id": "ch_1", "amount": 150000}
        body = json.dumps(payload).encode()
        event = self.client.construct_webhook_event(body, sign("whsec_a", body), "payout.paid")
        self.assertEqual(event["event"], "payout.paid")
        self.assertEqual(event["amount"], 150000)

    def test_event_header_must_match_body(self):
        body = b'{"event":"charge.succeeded"}'
        with self.assertRaises(CyzoraError):
            self.client.construct_webhook_event(body, sign("whsec_a", body), "payout.paid")

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

    def test_environment_defaults(self):
        with patch.dict(os.environ, {
            "CYZORA_API_BASE": "https://example.test/api/v1/",
            "CYZORA_WEBHOOK_SECRET": "whsec_one, whsec_two",
        }):
            c = Cyzora("sk_live_x")
        self.assertEqual(c.base_url, "https://example.test/api/v1")
        self.assertEqual(c.webhook_secrets, ["whsec_one", "whsec_two"])


class RequestTest(unittest.TestCase):
    def setUp(self):
        self.client = Cyzora("sk_live_x", base_url="https://example.test/api/v1")

    def test_charge_payload_and_headers(self):
        response = FakeResponse('{"id":"chg_1","status":"pending"}')
        with patch("urllib.request.urlopen", return_value=response) as call:
            self.client.create_charge(250.4, "0712345678", network="tkash", idempotency_key="INV-1")
        req = call.call_args.args[0]
        self.assertEqual(req.full_url, "https://example.test/api/v1/charges")
        self.assertEqual(req.get_method(), "POST")
        self.assertEqual(req.get_header("X-api-key"), "sk_live_x")
        self.assertEqual(req.get_header("Idempotency-key"), "INV-1")
        self.assertEqual(json.loads(req.data), {
            "amount": 25100, "phone": "254712345678", "network": "tkash",
        })

    def test_payout_requires_destination_fields(self):
        with patch("urllib.request.urlopen", return_value=FakeResponse("{}")):
            with self.assertRaises(CyzoraError):
                self.client.create_payout(1500, destination_type="paybill", short_code="100000")

    def test_fee_preview_uses_destination_parameter(self):
        with patch("urllib.request.urlopen", return_value=FakeResponse("{}")) as call:
            self.client.payout_fee_preview(2500, "paybill", "collections")
        self.assertEqual(
            call.call_args.args[0].full_url,
            "https://example.test/api/v1/payouts/fee-preview?amount=250000&destination=paybill&wallet=collections",
        )

    def test_statement_verification_sends_no_api_key(self):
        with patch("urllib.request.urlopen", return_value=FakeResponse('{"valid":true}')) as call:
            result = self.client.verify_statement("K7PX3MZQ9H2D")
        req = call.call_args.args[0]
        self.assertTrue(result["valid"])
        self.assertEqual(req.full_url, "https://example.test/api/v1/public/statements/verify/K7PX3MZQ9H2D")
        self.assertIsNone(req.get_header("X-api-key"))

    def test_export_returns_raw_csv(self):
        csv = "date,amount\n2026-10-01,2500.00\n"
        with patch("urllib.request.urlopen", return_value=FakeResponse(csv)) as call:
            result = self.client.export_charges({"from": "2026-10-01", "status": "succeeded"})
        self.assertEqual(result, csv)
        self.assertIn("status=succeeded", call.call_args.args[0].full_url)


    def test_metadata_values_become_strings_and_description_is_not_sent(self):
        with patch("urllib.request.urlopen", return_value=FakeResponse("{}")) as call:
            self.client.create_charge(100, "0712345678", description="ignored",
                                      metadata={"order_id": 1001, "paid": False, "skip": None, "cart": {"a": 1}})
        body = json.loads(call.call_args.args[0].data)
        self.assertNotIn("description", body)
        self.assertEqual(body["metadata"], {"order_id": "1001", "paid": "false", "cart": '{"a":1}'})

    def test_metadata_key_cap(self):
        with self.assertRaises(CyzoraError):
            self.client.create_charge(100, "0712345678", metadata={f"k{i}": i for i in range(21)})

    def test_list_charges_query_names(self):
        with patch("urllib.request.urlopen", return_value=FakeResponse("[]")) as call:
            result = self.client.list_charges(status="succeeded", from_date="2026-10-01", to_date="2026-10-31", mode="all")
        self.assertEqual(result, [])
        self.assertEqual(call.call_args.args[0].full_url,
                         "https://example.test/api/v1/charges?status=succeeded&from_date=2026-10-01&to_date=2026-10-31&mode=all")

    def test_wallet_move_and_subsidiary_transfer(self):
        with patch("urllib.request.urlopen", return_value=FakeResponse("{}")) as call:
            self.client.preview_wallet_move(1500.5)
            self.assertEqual(call.call_args.args[0].full_url, "https://example.test/api/v1/wallets/move/preview?amount=150050")
            self.client.move_to_payouts(2000, idempotency_key="MV-1")
            req = call.call_args.args[0]
            self.assertEqual(json.loads(req.data), {"amount": 200000})
            self.assertEqual(req.get_header("Idempotency-key"), "MV-1")
            self.client.create_subsidiary_transfer("sub_1", 500, from_wallet="payouts", idempotency_key="ST-1")
            self.assertEqual(json.loads(call.call_args.args[0].data),
                             {"subsidiary_id": "sub_1", "amount": 50000, "from_wallet": "payouts"})
            self.client.get_payout_batch("b 1")
            self.assertEqual(call.call_args.args[0].full_url, "https://example.test/api/v1/payouts/batches/b%201")
        with self.assertRaises(CyzoraError):
            self.client.move_to_payouts(0)
        with self.assertRaises(CyzoraError):
            self.client.create_subsidiary_transfer("", 500)


if __name__ == "__main__":
    unittest.main()
