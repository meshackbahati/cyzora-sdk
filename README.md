# Cyzora SDK

Client libraries for the **Cyzora** M-Pesa payment rail — charges (STK push), hosted
checkout, payouts, wallets and webhook verification. Available for **TypeScript/Node.js**,
**Python**, **PHP** and **Go**.

> Amounts on the wire are **integer cents of whole shillings** (KES 2,500 → `250000`).
> Every money-moving request should carry an **idempotency key** so a retry cannot
> double-charge or double-pay. The smallest payout is **KES 30**.

## Install

| Language | Install |
|---|---|
| TypeScript / Node | `npm install cyzora` (or copy `typescript/src/index.ts`) |
| Python | `pip install cyzora` (or copy `python/cyzora/__init__.py`) |
| PHP | `composer require cyzora/cyzora` (or copy `php/src/Client.php`) |
| Go | `go get github.com/meshackbahati/cyzora-sdk/go` |

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
console.log(charge.id, charge.status); // -> send STK prompt
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
Bank destination: `{ destinationType: "bank", bankCode, accountNumber, accountName }`.
Payouts are **final only when the `payout.paid` webhook arrives** — do not treat the
synchronous response as settled unless `status === "paid"`.

## Wallets & references

```ts
await cyzora.getWallets();           // collections / collections_pending / payouts (cents)
await cyzora.listPayoutBanks();      // bank codes for a payout form
await cyzora.payoutFeePreview(1500); // provider cut for a payout
```

## Webhooks

Cyzora signs each webhook with `X-Cyzora-Signature` = hex **HMAC-SHA256(rawBody, whsec_…)**.
Always verify against the **raw request body** before trusting it. Multiple secrets are
supported (one per registered endpoint).

Events: `charge.succeeded`, `charge.failed`, `charge.expired`, `payout.paid`, `payout.failed`.

```ts
// Express — mount a raw body parser on the webhook route
app.post("/webhook", express.raw({ type: "*/*" }), (req, res) => {
  const ok = cyzora.verifyWebhookSignature(req.body, req.headers["x-cyzora-signature"]);
  if (!ok) return res.status(401).send("bad signature");
  const event = JSON.parse(req.body.toString());
  // handle event.event / event.data ...
  res.json({ received: true });
});
```

```python
ok = client.verify_webhook_signature(raw_body_bytes, request.headers.get("X-Cyzora-Signature"))
```

```php
$ok = $cyzora->verifyWebhookSignature($rawBody, $_SERVER['HTTP_X_CYZORA_SIGNATURE'] ?? null);
```

```go
ok := client.VerifyWebhookSignature(rawBody, r.Header.Get("X-Cyzora-Signature"))
```

## API surface (all languages)

| Method | Endpoint |
|---|---|
| `createCharge` | `POST /charges` |
| `getCharge` | `GET /charges/:id` |
| `createCheckoutSession` | `POST /transactions/initialize` |
| `verifyTransaction` | `GET /transactions/verify/:ref` |
| `createPayout` | `POST /payouts` |
| `getPayout` | `GET /payouts/:id` |
| `getWallets` | `GET /wallets` |
| `listPayoutChannels` | `GET /payout-channels` |
| `listPayoutBanks` | `GET /payout-banks` |
| `payoutFeePreview` | `GET /payouts/fee-preview` |
| `verifyWebhookSignature` | local HMAC check |

Helpers exported everywhere: `normalizePhone`, `toCents`, `fromCents`, `PAYOUT_MIN_CENTS`.

## Configuration

| Env var | Meaning |
|---|---|
| `CYZORA_SECRET_KEY` | `sk_live_…` secret key (required) |
| `CYZORA_WEBHOOK_SECRET` | `whsec_…` signing secret (comma-separate for several) |
| `CYZORA_API_BASE` | Base URL override (default `https://api.cyzora.co.ke/api/v1`) |
| `CYZORA_PAYOUT_WALLET` | Payout source wallet (default `collections`) |

## Rules of the rail (read before integrating)

- **Whole shillings only.** The SDK floors/rounds to whole shillings; never send decimals.
- **Idempotency.** Pass a stable `idempotencyKey` on every charge and payout.
- **Webhook is the source of truth.** A charge/payout is pending until its webhook arrives.
  On a timeout, leave it pending and reconcile — do not mark it failed (the rail may still
  complete it).
- **Payout minimum is KES 30.** Reject smaller withdrawals before calling the API.
- **Keep keys server-side.** Never ship `sk_live_`/`whsec_` to a browser or client app.

## Repository layout

```
typescript/   Node.js + TypeScript SDK (src/index.ts, package.json, tsconfig.json)
python/       Python SDK (cyzora/__init__.py, pyproject.toml) — stdlib only
php/          PHP SDK (src/Client.php, composer.json) — cURL, no deps
go/           Go SDK (cyzora.go, go.mod) — stdlib only
```

## License

MIT
