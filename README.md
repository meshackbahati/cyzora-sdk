# Cyzora SDK

Client libraries for the **Cyzora** M-Pesa payment rail: charges (STK push), hosted
checkout, payouts, wallets, exports and webhook verification. Available for
**TypeScript/Node.js**, **Python**, **PHP** and **Go**.

Full runnable examples are in [EXAMPLES.md](./EXAMPLES.md).

> Amounts on the wire are **integer cents of whole shillings** (KES 2,500 → `250000`).
> Charges and checkout sessions round odd cents up to the next shilling, while payouts
> refuse amounts that are not a multiple of 100 cents. Every money-moving request
> should carry an idempotency key, and the smallest payout is **KES 30**.

## Install

| Language | Install |
|---|---|
| TypeScript / Node | `npm install cyzora` (or copy `typescript/src/index.ts`) |
| Python | `pip install cyzora` (or copy `python/cyzora/__init__.py`) |
| PHP | `composer require cyzora/cyzora` (or copy `php/src/Client.php`) |
| Go | `go get github.com/meshackbahati/cyzora-sdk/go` |

## Configuration

The constructor takes the secret key explicitly. Optional values fall back to the
environment when they are not passed, which makes server deployments simpler.

| Setting | Constructor argument | Environment fallback |
|---|---|---|
| Base URL | `baseUrl` / `base_url` / `WithBaseURL` | `CYZORA_API_BASE` (default `https://api.cyzora.co.ke/api/v1`) |
| Webhook secrets | `webhookSecrets` / `webhook_secret` / `webhookSecret` / `WithWebhookSecret` | `CYZORA_WEBHOOK_SECRET`, which may be comma-separated |
| Payout wallet | `wallet` on a payout | `CYZORA_PAYOUT_WALLET` (otherwise the rail's configured API payout wallet) |

Secret keys (`sk_live_...` and `sk_test_...`) stay on the server. Public keys
(`pk_live_...`) only identify the business to hosted checkout and cannot move money.

## Quick start

### TypeScript / Node
```ts
import { Cyzora } from "cyzora";

const cyzora = new Cyzora({
  secretKey: process.env.CYZORA_SECRET_KEY!,        // sk_live_...
  webhookSecrets: process.env.CYZORA_WEBHOOK_SECRET // whsec_... (string or string[])
});

const charge = await cyzora.createCharge({
  amountKes: 250,
  phone: "0712345678",
  accountRef: "INV-1001",
  idempotencyKey: "INV-1001",
  metadata: { orderId: "1001" }
});
console.log(charge.id, charge.status); // the STK prompt is on its way
```

### Python
```python
import os
from cyzora import Cyzora

client = Cyzora(os.environ["CYZORA_SECRET_KEY"], webhook_secret=os.environ["CYZORA_WEBHOOK_SECRET"])

charge = client.create_charge(250, "0712345678", account_ref="INV-1001", idempotency_key="INV-1001")
print(charge["id"], charge["status"])
```

### PHP
```php
$cyzora = new \Cyzora\Client(getenv('CYZORA_SECRET_KEY'), ['webhookSecret' => getenv('CYZORA_WEBHOOK_SECRET')]);

$charge = $cyzora->createCharge(250, '0712345678', [
    'accountRef' => 'INV-1001',
    'idempotencyKey' => 'INV-1001',
]);
echo $charge['id'], ' ', $charge['status'];
```

### Go
```go
client := cyzora.New(os.Getenv("CYZORA_SECRET_KEY"), cyzora.WithWebhookSecret(os.Getenv("CYZORA_WEBHOOK_SECRET")))

charge, err := client.CreateCharge(ctx, cyzora.ChargeParams{
    AmountKes: 250, Phone: "0712345678", AccountRef: "INV-1001", IdempotencyKey: "INV-1001",
})
```

## Charges and hosted checkout

`createCharge` sends an STK prompt. The accepted networks are `mpesa` (the default),
`airtel` and `tkash`, and phone numbers are normalized to `254XXXXXXXXX`. The charge
starts as `pending` and ends as `succeeded`, `failed` or `timeout`. A 202 response may
carry `{ "charge": ..., "warning": "..." }` when the rail is unsure whether the prompt
went out: the charge is pending and its webhook is authoritative, so do not send it again.

`createCheckoutSession` starts a hosted checkout session and returns `checkout_url` and
`reference`. It also accepts `callback_url`, `cancel_url`, `description`,
`customer_phone`, `customer_email`, `metadata` and `expires_in` (300 to 86400 seconds).
Redirect URLs must use HTTPS except on localhost. Confirm payment with the webhook or
`verifyTransaction`, never from the redirect alone. The same session can be opened in a
popup with `https://checkout.cyzora.co.ke/inline.js`, and the browser callback must
still be confirmed on the server.

## Payouts (send money out)

```ts
await cyzora.createPayout({
  amountKes: 1500,
  wallet: "collections",
  destinationType: "mobile",      // mobile | till | paybill | bank
  phone: "0712345678",
  idempotencyKey: "WD-9001"
});
```

A saved destination can be paid with `channelId` instead of destination fields. Direct
destinations are only allowed when the account owner has enabled API payouts in Settings;
otherwise the rail returns 403 with code `raw_destination_disabled`. Bank destinations
use `bankCode`, `accountNumber` and `accountName`, with bank codes from
`listPayoutBanks()`.

Payouts are final only when the `payout.paid` webhook arrives. The synchronous response
usually says `processing`. The fee is taken from the remaining wallet balance, not from
the amount sent, and it depends on the destination and wallet. Preview it before sending:

```ts
await cyzora.payoutFeePreview(2500, "paybill", "collections");
// GET /payouts/fee-preview?amount=250000&destination=paybill&wallet=collections
```

The query parameter is `destination`, not `destination_type`, and the wallet should match
the payout so that the preview agrees with the charge.

## Wallets, payout lists and exports

```ts
await cyzora.getWallets();            // collections / collections_pending / payouts (cents)
await cyzora.listPayoutChannels();    // saved, verified destinations
await cyzora.listPayoutBanks();       // bank codes for a payout form
await cyzora.listCharges({ status: "succeeded", fromDate: "2026-10-01" }); // array
await cyzora.listPayouts();           // plain array, newest first (not filtered by the rail yet)
await cyzora.exportCharges({ from: "2026-10-01", to: "2026-10-07", status: "succeeded" }); // CSV text
```

## Moving money between wallets

```ts
await cyzora.previewWalletMove(5000);                              // Collections -> Payouts
await cyzora.moveToPayouts(5000, { idempotencyKey: "move-2026-11-27" });
await cyzora.createSubsidiaryTransfer({                            // parent -> subsidiary
  subsidiaryId: "sub_...", amountKes: 2000, fromWallet: "collections", idempotencyKey: "sub-topup-1",
});
```
Wallet moves use real balances, so test keys are refused.

## Public statement verification

Every PDF statement carries a 12-character code. This endpoint is public and needs no
API key, so the SDK omits the `X-API-Key` header for it.

```ts
await cyzora.verifyStatement("K7PX3MZQ9H2D");
```

## Webhooks

Cyzora signs each webhook with `X-Cyzora-Signature`, a hex HMAC-SHA256 of the **raw**
request body, and also sends the event name in `X-Cyzora-Event`. Verify the signature
against the raw bytes before trusting the payload, and reply with a 2xx status. Failed
deliveries are retried after 10 seconds, 30 seconds, 2 minutes and 10 minutes.

Events: `charge.succeeded`, `charge.failed`, `charge.expired`, `payout.paid`,
`payout.failed` and `webhook.test`. The payloads are flat: `event`, `charge_id` or
`payout_id`, `reference`, `amount`, `status`, `account_ref`, `metadata`, `livemode`, and
`mpesa_receipt` or `transaction_id` where applicable. There is no `data` wrapper.
`charge.failed` covers failures, timeouts and cancellations; a checkout retry is
indicated by `retryable: true`.

```ts
// Express — mount a raw body parser on the webhook route
app.post("/webhook", express.raw({ type: "*/*" }), (req, res) => {
  const event = cyzora.constructWebhookEvent(
    req.body,
    req.headers["x-cyzora-signature"],
    req.headers["x-cyzora-event"] as string, // optional consistency check
  );
  switch (event.event) {
    case "charge.succeeded": /* mark the order paid */ break;
    case "payout.paid":      /* mark the withdrawal settled */ break;
    default:                 /* log unknown events */ break;
  }
  res.json({ received: true });
});
```

```python
event = client.construct_webhook_event(
    raw_body, request.headers.get("X-Cyzora-Signature"), request.headers.get("X-Cyzora-Event")
)
```
```php
$event = $cyzora->constructWebhookEvent($rawBody, $_SERVER['HTTP_X_CYZORA_SIGNATURE'] ?? null, $_SERVER['HTTP_X_CYZORA_EVENT'] ?? null);
```
```go
event, err := client.ConstructWebhookEvent(rawBody, r.Header.Get("X-Cyzora-Signature"), r.Header.Get("X-Cyzora-Event"))
```

`verifyWebhookSignature` accepts a string or bytes and checks every configured secret
in constant time. The third argument to `constructWebhookEvent` is optional; when it is
supplied, a mismatch with the body's `event` raises an error.

### Multiple webhook secrets

Each registered endpoint receives its own secret. Verification succeeds when the
signature matches any configured secret.

```ts
new Cyzora({ secretKey, webhookSecrets: ["whsec_a...", "whsec_b..."] });
```
```python
Cyzora(secret_key, webhook_secret=["whsec_a...", "whsec_b..."])
```
```php
new \Cyzora\Client($secretKey, ['webhookSecret' => ['whsec_a...', 'whsec_b...']]);
```
```go
cyzora.New(secretKey, cyzora.WithWebhookSecret("whsec_a..."), cyzora.WithWebhookSecret("whsec_b..."))
```

## Test mode

Test keys start with `sk_test_` and use the same endpoints without moving real money.
The documented test phones are `254700000001` (succeeds), `254700000002` (low balance),
`254700000003` (cancelled) and `254700000004` (no PIN, timeout). Test webhooks are
signed and carry `"livemode": false`. Test payouts are simulated, do not change the
balance, and are not listed by `GET /payouts`.

## API surface (all languages)

| Method | Endpoint |
|---|---|
| `createCharge` | `POST /charges` |
| `getCharge` | `GET /charges/:id` |
| `listCharges` | `GET /charges` |
| `exportCharges` | `GET /charges/export` (CSV) |
| `createCheckoutSession` | `POST /transactions/initialize` |
| `verifyTransaction` | `GET /transactions/verify/:ref` |
| `createPayout` | `POST /payouts` |
| `getPayout` | `GET /payouts/:id` |
| `listPayouts` | `GET /payouts` |
| `listPayoutBatches` / `getPayoutBatch` | `GET /payouts/batches[/:id]` (read-only) |
| `payoutFeePreview` | `GET /payouts/fee-preview` |
| `getWallets` | `GET /wallets` |
| `previewWalletMove` / `moveToPayouts` | `GET /wallets/move/preview`, `POST /wallets/move` |
| `listWalletTransfers` | `GET /wallets/transfers` |
| `listSubsidiaries` | `GET /wallets/subsidiaries` |
| `previewSubsidiaryTransfer` / `createSubsidiaryTransfer` | `GET .../preview`, `POST /wallets/subsidiary-transfers` |
| `listSubsidiaryTransfers` | `GET /wallets/subsidiary-transfers` |
| `listPayoutChannels` | `GET /payout-channels` |
| `listPayoutBanks` | `GET /payout-banks` |
| `verifyStatement` | `GET /public/statements/verify/:code` (no key) |
| `verifyWebhookSignature` | local HMAC check |
| `constructWebhookEvent` | verify + parse webhook body |

Helpers available across the SDKs include `normalizePhone`, `toCents`, `fromCents` and
`PAYOUT_MIN_CENTS`; Python and PHP also export the strict payout amount helper.
Payout methods validate whole shillings and destination fields before calling the rail.

The error body is JSON with `error` or `message`. Relevant status codes are 400 (bad
request), 401 (bad key), 403 (unverified business, direct payouts disabled or suspended
account), 404, 409 (conflict or a pending request with the same idempotency key), 410
(expired link), 422 (unverified channel) and 429 (rate limit, 100 requests per minute).

## Rules of the rail

- **Whole shillings only.** Charges and checkout sessions round odd cents up, whereas
  payouts are rejected unless the amount is a multiple of 100 cents.
- **Idempotency.** Pass a stable `idempotencyKey` on every charge and payout.
- **Webhook is the source of truth.** A charge or payout is pending until its webhook
  arrives. On a gateway timeout, leave it pending and reconcile rather than marking it
  failed, because the rail may still complete it.
- **Payout minimum is KES 30.** Reject smaller withdrawals before calling the API.
- **Keep keys server-side.** Never ship `sk_live_`/`whsec_` to a browser or client app.

## Advanced

### Call any endpoint

The rail evolves faster than SDK releases, so every client exposes a generic escape hatch
that signs, retries and parses like the typed methods.

```ts
await cyzora.request("POST", "/charges", { body: { amount: 25000, phone: "254712345678", network: "airtel" }, idempotencyKey: "k1" });
const x = await cyzora.request("GET", "/some-brand-new-endpoint");
```
```python
client.request("POST", "/charges", body={...}, idempotency_key="k1")
client.request("GET", "/some-brand-new-endpoint")
```
```php
$cyzora->rawRequest('POST', '/charges', $body, 'k1');
$cyzora->rawRequest('GET', '/some-brand-new-endpoint');
```
```go
client.Request(ctx, "POST", "/charges", body, "k1", nil)
client.Request(ctx, "GET", "/some-brand-new-endpoint", nil, "", nil)
```
The path is appended to the base URL, which already includes `/api/v1`. Public endpoints
can be called with `auth: false` (TypeScript and Python), `$auth = false` (PHP), or
`PublicRequest` (Go).

### Automatic retries

Transient failures (network errors, HTTP 5xx and 429) are retried with exponential
backoff, defaulting to two retries. A 409 returned for a request with an idempotency key
is also retried, as the documentation advises waiting and reusing the key. Retries only
happen when they are safe: GET requests, or any request carrying an idempotency key.

### Webhook event names

Constants are provided for convenience, but unknown events should be handled gracefully:
`WebhookEvents` (TS), `WEBHOOK_EVENTS` (Python), `Client::WEBHOOK_EVENTS` (PHP),
`EventChargeSucceeded`/`EventPayoutPaid`/… (Go).

## Repository layout

```
typescript/   Node.js + TypeScript SDK (src/index.ts, package.json, tsconfig.json)
python/       Python SDK (cyzora/__init__.py, pyproject.toml) — stdlib only
php/          PHP SDK (src/Client.php, composer.json) — cURL, no deps
go/           Go SDK (cyzora.go, go.mod) — stdlib only
```

## Documentation source

This SDK follows the local `Cyzora-API-Docs.pdf` reference. The PDF is supplied for
development only and is deliberately excluded from version control through `.gitignore`;
the contract above is the tracked summary of it.

## License

MIT
