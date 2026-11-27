# Cyzora SDK for Python

This package is the Python client for the Cyzora M-Pesa payment rail. It supports
charges (STK push), hosted checkout, payouts, wallets, payout channels and banks, charge
exports, public statement verification, and webhook verification. The implementation
uses only the Python standard library and supports Python 3.8 or newer.

The tracking contract comes from the local API reference. Amounts on the wire are
integer cents of whole shillings, so KES 2,500 is `250000`. Charges and checkout
sessions round odd cents up to the next shilling, whereas payouts reject any amount
that is not a multiple of 100 cents. Every money-moving request should carry a stable
idempotency key, and the smallest payout is KES 30.

## Installation

```bash
pip install cyzora
```

When working from this repository, the module can be imported directly from `python/`
without installation:

```bash
cd python
python -m unittest discover -s tests -v
```

## Creating a client

```python
import os
from cyzora import Cyzora

client = Cyzora(
    os.environ["CYZORA_SECRET_KEY"],
    webhook_secret=os.environ["CYZORA_WEBHOOK_SECRET"],
)
```

The constructor signature is:

```python
Cyzora(
    secret_key,
    base_url=None,
    webhook_secret=None,
    timeout=30.0,
    max_retries=2,
)
```

| Argument | Behaviour |
|---|---|
| `secret_key` | Required secret key, sent in `X-API-Key`. |
| `base_url` | Optional API base. Defaults to `CYZORA_API_BASE`, then `https://api.cyzora.co.ke/api/v1`. |
| `webhook_secret` | A secret string or a list of secrets. Defaults to `CYZORA_WEBHOOK_SECRET`, which may be comma-separated. |
| `timeout` | Per-request timeout in seconds. Defaults to 30. |
| `max_retries` | Transient retries. Defaults to 2. |

Secret keys and webhook secrets must remain on the server. They are never required by
the public statement endpoint, which the SDK calls without an `X-API-Key` header.

## Quick start: charge a customer

```python
charge = client.create_charge(
    250,
    "0712345678",
    network="mpesa",
    account_ref="INV-1001",
    idempotency_key="INV-1001",
    metadata={"order_id": "1001"},
)
print(charge["id"], charge["status"])
```

The charge starts as `pending` and ends as `succeeded`, `failed`, or `timeout`. A 202
response may carry a `warning` alongside the charge when the rail is unsure whether the
prompt went out. In that case the charge is pending and its webhook is authoritative, so
the request must not be repeated.

## Charges

```python
charge = client.create_charge(2500.4, "0712345678", amount_cents=None)
same_charge = client.get_charge(charge["id"])
csv_text = client.export_charges({
    "from": "2026-10-01",
    "to": "2026-10-07",
    "status": "succeeded",
})
```

`create_charge` accepts the following keyword arguments:

| Argument | Notes |
|---|---|
| `amount_kes` | Amount in shillings. Odd cents are rounded up to the next shilling. |
| `phone` | Customer number, normalized to `254XXXXXXXXX`. |
| `network` | `mpesa`, `airtel`, or `tkash`. Defaults to `mpesa`. |
| `account_ref` | Order or invoice reference, truncated to 64 characters. |
| `description` | Deprecated and not sent: the rail does not store a description on STK charges. Use `account_ref` or `metadata`. |
| `metadata` | Up to 20 key and value pairs returned on the charge and in webhooks. Values are sent as text, so numbers and booleans are converted for you. |
| `idempotency_key` | Stable key, normally the order or invoice reference. |
| `amount_cents` | Alternative to `amount_kes`. Odd cents are also rounded up. |

Phone numbers are normalized from `0712345678`, `+254712345678`, or `712345678` to
`254712345678`. An invalid number raises `CyzoraError` before any request is sent.
`export_charges` returns CSV text and accepts `from`, `to`, and `status` filters.

## Hosted checkout

```python
session = client.create_checkout_session(
    2500,
    account_ref="Order 1042",
    description="2 x Margherita pizza",
    callback_url="https://shop.example.com/orders/1042/thanks",
    cancel_url="https://shop.example.com/cart",
    customer_phone="0712345678",
    customer_email="buyer@example.com",
    metadata={"order_id": "1042"},
    expires_in=1800,
    idempotency_key="order-1042",
)

checkout_url = session["checkout_url"]
confirmed = client.verify_transaction(session["reference"])
```

`create_checkout_session` supports `amount_kes`, `account_ref`, `description`,
`callback_url`, `cancel_url`, `customer_phone`, `customer_email`, `metadata`,
`expires_in`, and `idempotency_key`. `expires_in` must be between 300 and 86400 seconds
when supplied. Redirect URLs must use HTTPS except on localhost, and they must be on an
allowed domain if the account has configured redirect domains.

The returned session starts as `awaiting_payment`. A customer who returns through
`callback_url` has not necessarily paid; only the webhook or `verify_transaction` is
authoritative. The same session can be opened in a popup with
`https://checkout.cyzora.co.ke/inline.js`, and the browser callback still requires
server-side confirmation.

## Payouts

```python
client.create_payout(
    1500,
    wallet="collections",
    destination_type="mobile",
    phone="0712345678",
    idempotency_key="WD-9001",
)
```

`create_payout` accepts these keyword arguments: `amount_kes`, `amount_cents`, `wallet`,
`channel_id`, `destination_type`, `phone`, `short_code`, `account_ref`, `bank_code`,
`account_number`, `account_name`, and `idempotency_key`. Supported destination types
are `mobile`, `till`, `paybill`, and `bank`. A saved destination is selected with
`channel_id` instead of destination fields.

The client validates the amount and destination before sending the request. Fractional
shillings are rejected, the amount must be at least KES 30, mobile payouts require a
valid phone number, till payouts require `short_code`, paybill payouts require
`short_code` and `account_ref`, and bank payouts require `bank_code`, `account_number`,
and `account_name`. When `wallet` is omitted, the SDK uses `CYZORA_PAYOUT_WALLET` if
present; otherwise the rail uses the account's configured API payout wallet.

Direct destinations are only permitted when the account owner has enabled API payouts
in Settings. Until then the rail returns 403 with the code `raw_destination_disabled`.
Synchronous responses usually report `processing`, so a payout is settled only when the
`payout.paid` webhook arrives.

```python
fee = client.payout_fee_preview(2500, "paybill", "collections")
banks = client.list_payout_banks()
```

The fee preview uses the rail's `destination` query parameter and should carry the same
wallet as the payout so that the preview matches the charge. Bank codes used by
`bank_code` come from `list_payout_banks()`.

## Wallets, payout lists, references, and exports

```python
balances = client.get_wallets()
channels = client.list_payout_channels()
charges = client.list_charges(status="succeeded", from_date="2026-10-01")
payouts = client.list_payouts()          # a plain list, newest first
payout = client.get_payout(payouts[0]["id"])
statement = client.verify_statement("K7PX3MZQ9H2D")
```

`list_charges` and `list_payouts` return lists. `list_charges` filters by `status`,
`from_date`, `to_date` and `mode` (`live`, `test` or `all`); `list_payouts` is not
filtered by the rail yet. Wallet balances and payout amounts are returned in cents.

## Moving money between wallets

```python
client.preview_wallet_move(5000)                       # fee and resulting balances
client.move_to_payouts(5000, idempotency_key="move-2026-11-27")
client.list_wallet_transfers(20)                       # {"data": [...]}

# Parent businesses can top up a subsidiary's Payouts wallet.
subs = client.list_subsidiaries()["data"]
client.preview_subsidiary_transfer(subs[0]["id"], 2000, from_wallet="collections")
client.create_subsidiary_transfer(subs[0]["id"], 2000, from_wallet="collections",
                                  idempotency_key="sub-topup-1")
```

Wallet moves use real balances, so test keys are refused. Bulk payout batches are
created in the dashboard; `list_payout_batches()` and `get_payout_batch(id)` read them. `verify_statement` calls the
public statement endpoint and deliberately omits the API key. It returns validity,
business name, period, generated time, balances in and out, transaction count, and the
statement's SHA-256 digest.

## Webhooks

Cyzora signs every webhook with `X-Cyzora-Signature`, a hexadecimal HMAC-SHA256 of the
raw request body, and also sends the event name in `X-Cyzora-Event`. Verify against the
raw bytes before parsing JSON, and reply with any 2xx status. Failed deliveries are
retried after 10 seconds, 30 seconds, 2 minutes, and 10 minutes.

The following example uses Flask. The route must read the exact request bytes rather
than a parsed form or JSON object.

```python
from flask import Flask, request
from cyzora import Cyzora, CyzoraError, WEBHOOK_EVENTS

app = Flask(__name__)

@app.post("/webhooks/cyzora")
def webhook():
    raw = request.get_data()
    try:
        event = client.construct_webhook_event(
            raw,
            request.headers.get("X-Cyzora-Signature"),
            request.headers.get("X-Cyzora-Event"),
        )
    except CyzoraError:
        return "invalid signature", 401

    if event["event"] == WEBHOOK_EVENTS["charge_succeeded"]:
        mark_invoice_paid(event)
    elif event["event"] == WEBHOOK_EVENTS["payout_paid"]:
        mark_withdrawal_paid(event)
    elif event["event"] == WEBHOOK_EVENTS["webhook_test"]:
        pass
    return {"received": True}
```

`verify_webhook_signature(raw_body, signature)` returns a boolean and checks every
configured secret with `hmac.compare_digest`. `construct_webhook_event(raw_body,
signature, event_header=None)` verifies, parses, and optionally checks that the header
matches the body's `event`. When the header is supplied, a mismatch raises
`CyzoraError`. `WEBHOOK_EVENTS` contains the keys `charge_succeeded`, `charge_failed`,
`charge_expired`, `payout_paid`, `payout_failed`, and `webhook_test`.

Webhook payloads are flat. A charge event carries `event`, `charge_id`, `reference`,
`account_ref`, `amount`, `currency`, `status`, `mpesa_receipt`, `metadata`, and
`livemode`. A payout event carries `event`, `payout_id`, `channel_id`, `amount`, `fee`,
`status`, and `transaction_id`. There is no `data` wrapper. `charge.failed` covers
failures, timeouts, and cancellations, and checkout retries are indicated by
`retryable: true`.

Multiple webhook secrets are supported:

```python
client = Cyzora(
    os.environ["CYZORA_SECRET_KEY"],
    webhook_secret=["whsec_a...", "whsec_b..."],
)
```

Verification succeeds when the signature matches any configured secret.

## Escape hatch and retries

Any endpoint can be called through `request`, which applies the same signing, retry,
and parsing rules as the typed methods.

```python
client.request("POST", "/charges",
               body={"amount": 25000, "phone": "254712345678", "network": "airtel"},
               idempotency_key="k1")
result = client.request("GET", "/some-new-endpoint")
csv_text = client.request("GET", "/charges/export", raw=True)
```

The path is appended to the base URL, which already includes `/api/v1`. The `auth`
argument may be set to `False` for public endpoints, and `raw=True` returns the
response text instead of decoded JSON.

Transient failures, namely network errors, HTTP 5xx, and HTTP 429, are retried with
exponential backoff. A 409 returned for a request with an idempotency key is also
retried, since the documentation advises waiting and reusing that key. Retries happen
only for GET requests or requests carrying an idempotency key, so a charge or payout
cannot be duplicated by a retry.

## Errors

Any non-2xx response or transport failure raises `CyzoraError`, which exposes
`message`, `status`, and `body`.

```python
from cyzora import CyzoraError

try:
    client.create_payout(10, phone="0712345678")
except CyzoraError as error:
    print(error.status, error.message, error.body)
```

The documented status codes are 400 for bad requests, 401 for a missing or invalid
key, 403 for an unverified business or disabled direct payouts, 404 for unknown
resources, 409 for conflicts, 410 for expired links, 422 for unverified channels, and
429 for the per-key rate limit of 100 requests per minute.

## Helpers

```python
from cyzora import (
    normalize_phone,
    to_cents,
    to_payout_cents,
    from_cents,
    PAYOUT_MIN_CENTS,
    WEBHOOK_EVENTS,
)

normalize_phone("0712345678")   # 254712345678
to_cents(2500.4)                # 250100
to_payout_cents(1500)           # 150000
from_cents(250000)              # 2500.0
PAYOUT_MIN_CENTS                # 3000
```

`to_payout_cents` raises `CyzoraError` for fractional shillings. Payout methods call it
internally, so callers do not have to convert amounts themselves.

## Test mode

Test keys start with `sk_test_`. The documented test phones are `254700000001`, which
succeeds, `254700000002`, which fails for low balance, `254700000003`, which fails
because the customer cancelled, and `254700000004`, which times out. Test webhooks are
signed like live webhooks and carry `"livemode": false`. Test payouts are simulated,
do not change balances, and are not listed by `GET /payouts`.

## API surface

| Method | Endpoint |
|---|---|
| `create_charge` | `POST /charges` |
| `get_charge` | `GET /charges/:id` |
| `list_charges` | `GET /charges` |
| `export_charges` | `GET /charges/export` |
| `create_checkout_session` | `POST /transactions/initialize` |
| `verify_transaction` | `GET /transactions/verify/:ref` |
| `create_payout` | `POST /payouts` |
| `get_payout` | `GET /payouts/:id` |
| `list_payouts` | `GET /payouts` |
| `list_payout_batches` | `GET /payouts/batches` |
| `get_payout_batch` | `GET /payouts/batches/:id` |
| `payout_fee_preview` | `GET /payouts/fee-preview` |
| `get_wallets` | `GET /wallets` |
| `preview_wallet_move` | `GET /wallets/move/preview` |
| `move_to_payouts` | `POST /wallets/move` |
| `list_wallet_transfers` | `GET /wallets/transfers` |
| `list_subsidiaries` | `GET /wallets/subsidiaries` |
| `preview_subsidiary_transfer` | `GET /wallets/subsidiary-transfers/preview` |
| `create_subsidiary_transfer` | `POST /wallets/subsidiary-transfers` |
| `list_subsidiary_transfers` | `GET /wallets/subsidiary-transfers` |
| `list_payout_channels` | `GET /payout-channels` |
| `list_payout_banks` | `GET /payout-banks` |
| `verify_statement` | `GET /public/statements/verify/:code` |
| `verify_webhook_signature` | Local HMAC check |
| `construct_webhook_event` | Verify and parse webhook body |
| `request` | Generic escape hatch |

## Development

```bash
python -m unittest discover -s tests -v
```

The root repository also provides `README.md` for the shared contract and
`EXAMPLES.md` for cross-language examples.
