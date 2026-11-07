"""
Cyzora SDK for Python 3.8+ (standard library only — no external dependencies).

Cyzora is an M-Pesa payment rail. Amounts on the wire are integer cents of whole
shillings (KES 2,500 -> 250000). Every money-moving POST should carry an
idempotency key so a retry cannot double-charge or double-pay.

    from cyzora import Cyzora

    client = Cyzora(secret_key="sk_live_...", webhook_secret="whsec_...")
    charge = client.create_charge(amount_kes=100, phone="0712345678",
                                  account_ref="INV-1", idempotency_key="INV-1")
"""

from __future__ import annotations

import hashlib
import hmac
import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, List, Optional, Union

__all__ = [
    "Cyzora", "CyzoraError", "normalize_phone", "to_cents", "from_cents",
    "PAYOUT_MIN_CENTS", "WEBHOOK_EVENTS",
]

DEFAULT_BASE_URL = "https://api.cyzora.co.ke/api/v1"
PAYOUT_MIN_CENTS = 3000  # KES 30

#: Known webhook event names. The rail evolves, so also compare raw event strings.
WEBHOOK_EVENTS = {
    "charge_succeeded": "charge.succeeded",
    "charge_failed": "charge.failed",
    "charge_expired": "charge.expired",
    "payout_paid": "payout.paid",
    "payout_failed": "payout.failed",
}

_PHONE_RE = re.compile(r"^254[17]\d{8}$")


class CyzoraError(Exception):
    """Raised for any non-2xx response or transport failure."""

    def __init__(self, message: str, status: Optional[int] = None, body: Any = None):
        super().__init__(message)
        self.message = message
        self.status = status
        self.body = body


def normalize_phone(phone: str) -> Optional[str]:
    """Normalize a Kenyan phone number to the 254XXXXXXXXX form the rail expects."""
    if not phone:
        return None
    p = re.sub(r"[\s\-+]", "", str(phone))
    if p.startswith("0"):
        p = "254" + p[1:]
    elif p.startswith("7") or p.startswith("1"):
        p = "254" + p
    return p if _PHONE_RE.match(p) else None


def to_cents(amount_kes: Union[int, float]) -> int:
    """Convert shillings to integer cents, rounding to whole shillings first."""
    kes = round(float(amount_kes))
    if kes < 1:
        raise CyzoraError("Amount must be at least KES 1.")
    return kes * 100


def from_cents(cents: Union[int, float]) -> float:
    """Convert integer cents back to shillings."""
    return round(float(cents or 0)) / 100


class Cyzora:
    def __init__(
        self,
        secret_key: str,
        base_url: str = DEFAULT_BASE_URL,
        webhook_secret: Optional[Union[str, List[str]]] = None,
        timeout: float = 30.0,
        max_retries: int = 2,
    ):
        if not secret_key:
            raise CyzoraError("secret_key is required")
        self.secret_key = secret_key
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.max_retries = max_retries
        if webhook_secret is None:
            self.webhook_secrets: List[str] = []
        elif isinstance(webhook_secret, str):
            self.webhook_secrets = [webhook_secret]
        else:
            self.webhook_secrets = [s for s in webhook_secret if s]

    # -- transport ---------------------------------------------------------
    def _request(
        self,
        path: str,
        method: str = "GET",
        body: Optional[Dict[str, Any]] = None,
        idempotency_key: Optional[str] = None,
        query: Optional[Dict[str, Any]] = None,
    ) -> Any:
        url = f"{self.base_url}{path}"
        if query:
            clean = {k: v for k, v in query.items() if v not in (None, "")}
            if clean:
                url += "?" + urllib.parse.urlencode(clean)
        headers = {"X-API-Key": self.secret_key, "Accept": "application/json"}
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode("utf-8")
        if idempotency_key:
            headers["Idempotency-Key"] = str(idempotency_key)[:128]

        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        # Retrying is only safe when the rail can dedupe: GETs, or POSTs with an idempotency key.
        can_retry = method.upper() == "GET" or bool(idempotency_key)
        attempt = 0
        while True:
            try:
                with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                    text = resp.read().decode("utf-8")
                    status = resp.getcode()
            except urllib.error.HTTPError as e:
                text = e.read().decode("utf-8", "replace")
                status = e.code
            except urllib.error.URLError as e:
                if can_retry and attempt < self.max_retries:
                    attempt += 1
                    time.sleep(0.25 * (2 ** (attempt - 1)))
                    continue
                raise CyzoraError(f"Could not reach Cyzora: {e.reason}")

            parsed: Any = {}
            if text:
                try:
                    parsed = json.loads(text)
                except json.JSONDecodeError:
                    parsed = {"raw": text}
            if status < 200 or status >= 300:
                transient = status >= 500 or status == 429
                if transient and can_retry and attempt < self.max_retries:
                    attempt += 1
                    time.sleep(0.25 * (2 ** (attempt - 1)))
                    continue
                msg = (parsed or {}).get("error") or (parsed or {}).get("message") or f"Cyzora request failed ({status})"
                raise CyzoraError(msg, status, parsed)
            return parsed

    def request(self, method: str, path: str, body: Optional[Dict[str, Any]] = None,
                idempotency_key: Optional[str] = None, query: Optional[Dict[str, Any]] = None) -> Any:
        """Escape hatch for any endpoint, including new ones the rail adds before
        this SDK ships a typed wrapper. `path` is appended to the base URL."""
        return self._request(path, method=method, body=body, idempotency_key=idempotency_key, query=query)

    # -- charges -----------------------------------------------------------
    def create_charge(
        self,
        amount_kes: Union[int, float],
        phone: str,
        network: str = "mpesa",
        account_ref: Optional[str] = None,
        description: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
        idempotency_key: Optional[str] = None,
        amount_cents: Optional[int] = None,
    ) -> Any:
        """Send an M-Pesa STK prompt to a customer's phone."""
        normalized = normalize_phone(phone)
        if not normalized:
            raise CyzoraError("Enter a valid Safaricom phone number (e.g. 0712 345 678).")
        cents = int(amount_cents) if amount_cents is not None else to_cents(amount_kes)
        if cents < 100:
            raise CyzoraError("Amount must be a whole number of shillings, at least KES 1.")
        body: Dict[str, Any] = {"amount": cents, "phone": normalized, "network": network}
        if account_ref:
            body["account_ref"] = str(account_ref)[:64]
        if description:
            body["description"] = str(description)[:200]
        if metadata:
            body["metadata"] = metadata
        return self._request("/charges", "POST", body, idempotency_key)

    def get_charge(self, charge_id: str) -> Any:
        return self._request(f"/charges/{urllib.parse.quote(charge_id, safe='')}")

    def create_checkout_session(
        self,
        amount_kes: Union[int, float],
        account_ref: Optional[str] = None,
        description: Optional[str] = None,
        callback_url: Optional[str] = None,
        cancel_url: Optional[str] = None,
        customer_phone: Optional[str] = None,
        customer_email: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
        expires_in: Optional[int] = None,
        idempotency_key: Optional[str] = None,
    ) -> Any:
        """Hosted checkout session (redirect flow) instead of an STK push."""
        body: Dict[str, Any] = {"amount": to_cents(amount_kes)}
        if account_ref:
            body["account_ref"] = str(account_ref)[:64]
        if description:
            body["description"] = str(description)[:200]
        if callback_url:
            body["callback_url"] = callback_url
        if cancel_url:
            body["cancel_url"] = cancel_url
        if customer_phone:
            body["customer_phone"] = normalize_phone(customer_phone) or customer_phone
        if customer_email:
            body["customer_email"] = customer_email
        if metadata:
            body["metadata"] = metadata
        if expires_in:
            body["expires_in"] = int(expires_in)
        return self._request("/transactions/initialize", "POST", body, idempotency_key)

    def verify_transaction(self, reference: str) -> Any:
        return self._request(f"/transactions/verify/{urllib.parse.quote(reference, safe='')}")

    # -- payouts -----------------------------------------------------------
    def create_payout(
        self,
        amount_kes: Union[int, float],
        wallet: Optional[str] = None,
        channel_id: Optional[str] = None,
        destination_type: str = "mobile",
        phone: Optional[str] = None,
        short_code: Optional[str] = None,
        account_ref: Optional[str] = None,
        bank_code: Optional[str] = None,
        account_number: Optional[str] = None,
        account_name: Optional[str] = None,
        idempotency_key: Optional[str] = None,
    ) -> Any:
        """Send money out of the platform balance to a mobile/till/paybill/bank destination."""
        cents = to_cents(amount_kes)
        if cents < PAYOUT_MIN_CENTS:
            raise CyzoraError("The smallest payout is KES 30.")
        body: Dict[str, Any] = {"amount": cents}
        if wallet:
            body["wallet"] = wallet
        if channel_id:
            body["channel_id"] = channel_id
        else:
            body["destination_type"] = destination_type
            if destination_type == "mobile":
                body["phone"] = normalize_phone(phone or "") or phone
            elif destination_type == "till":
                body["short_code"] = short_code
            elif destination_type == "paybill":
                body["short_code"] = short_code
                body["account_ref"] = account_ref
            elif destination_type == "bank":
                body["bank_code"] = bank_code
                body["account_number"] = account_number
                body["account_name"] = account_name
        return self._request("/payouts", "POST", body, idempotency_key)

    def get_payout(self, payout_id: str) -> Any:
        return self._request(f"/payouts/{urllib.parse.quote(payout_id, safe='')}")

    def get_wallets(self) -> Any:
        """Balances (in cents) for collections, collections_pending and payouts wallets."""
        return self._request("/wallets")

    def list_payout_channels(self) -> Any:
        return self._request("/payout-channels")

    def list_payout_banks(self) -> Any:
        return self._request("/payout-banks")

    def payout_fee_preview(self, amount_kes: Union[int, float], destination_type: str = "mobile") -> Any:
        return self._request("/payouts/fee-preview", query={"amount": to_cents(amount_kes), "destination_type": destination_type})

    # -- webhooks ----------------------------------------------------------
    def verify_webhook_signature(self, raw_body: Union[str, bytes], signature: Optional[str]) -> bool:
        """
        Verify a webhook signature. `raw_body` must be the exact bytes received and
        `signature` the X-Cyzora-Signature header (hex HMAC-SHA256). Returns True if it
        matches any configured webhook secret (timing-safe).
        """
        if not self.webhook_secrets or not signature or raw_body in (None, ""):
            return False
        body = raw_body.encode("utf-8") if isinstance(raw_body, str) else raw_body
        sig = str(signature).strip().encode("utf-8")
        for secret in self.webhook_secrets:
            expected = hmac.new(secret.encode("utf-8"), body, hashlib.sha256).hexdigest().encode("utf-8")
            if hmac.compare_digest(expected, sig):
                return True
        return False

    def construct_webhook_event(self, raw_body: Union[str, bytes], signature: Optional[str]) -> Any:
        """
        Verify the signature (against every configured secret) and parse the body in
        one call. Raises CyzoraError if the signature is invalid or the body is not
        JSON. `raw_body` must be the exact bytes received.
        """
        if not self.verify_webhook_signature(raw_body, signature):
            raise CyzoraError("Webhook signature verification failed", 401)
        body = raw_body.decode("utf-8") if isinstance(raw_body, (bytes, bytearray)) else raw_body
        try:
            return json.loads(body)
        except json.JSONDecodeError:
            raise CyzoraError("Webhook body is not valid JSON", 400)
