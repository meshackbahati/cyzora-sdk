# Cyzora SDK for TypeScript and Node.js

This package is the TypeScript and Node.js client for the Cyzora M-Pesa payment rail.
It wraps charges (STK push), hosted checkout, payouts, wallets, payout channels and
banks, charge exports, public statement verification, and webhook verification. The
client uses the global `fetch` implementation and has no runtime dependencies beyond
Node.js itself.

The tracking contract comes from the local API reference. Amounts on the wire are
integer cents of whole shillings, so KES 2,500 is `250000`. Charges and checkout
sessions round odd cents up to the next shilling, whereas payouts reject any amount
that is not a multiple of 100 cents. Every money-moving request should carry a stable
idempotency key, and the smallest payout is KES 30.

## Requirements and installation

Node.js 18 or newer is required because the client uses global `fetch`. The package is
ESM only, so consumers should use `import` rather than `require`.

```bash
npm install cyzora
```

When working from this repository, install the development dependencies and build the
package before importing it:

```bash
cd typescript
npm install
npm run build
```

The build writes JavaScript and declaration files into `dist/`. The package name is
`cyzora`, and both named and default exports are available.

## Creating a client

```ts
import { Cyzora } from "cyzora";

const cyzora = new Cyzora({
  secretKey: process.env.CYZORA_SECRET_KEY!,        // sk_live_... or sk_test_...
  webhookSecrets: process.env.CYZORA_WEBHOOK_SECRET, // whsec_..., string or string[]
});
```

`CyzoraOptions` has the following shape:

| Option | Type | Behaviour |
|---|---|---|
| `secretKey` | `string` | Required secret key. It is sent in `X-API-Key`. |
| `baseUrl` | `string` | Optional API base. Defaults to `CYZORA_API_BASE`, then `https://api.cyzora.co.ke/api/v1`. |
| `webhookSecrets` | `string \| string[]` | Signing secrets. Defaults to `CYZORA_WEBHOOK_SECRET`, which may be comma-separated. |
| `fetchImpl` | `typeof fetch` | Optional fetch override, useful for tests or older runtimes. |
| `maxRetries` | `number` | Automatic retries on transient failures. Defaults to 2. |

A missing secret key or webhook secret is never read from a browser. Keep both values
on the server and inject them through the environment.

## Quick start: charge a customer

```ts
const charge = await cyzora.createCharge({
  amountKes: 250,
  phone: "0712345678",
  network: "mpesa",
  accountRef: "INV-1001",
  idempotencyKey: "INV-1001",
  metadata: { orderId: "1001" },
});

console.log(charge.id, charge.status);
```

`createCharge` sends an STK prompt. The charge normally starts as `pending` and ends as
`succeeded`, `failed`, or `timeout`. A 202 response may contain a `warning` alongside the
charge when the rail is unsure whether the prompt went out; in that case the charge is
still pending and its webhook is authoritative, so the request must not be repeated.

## Charges

```ts
const charge = await cyzora.createCharge(params);
const sameCharge = await cyzora.getCharge(charge.id);
const csv = await cyzora.exportCharges({
  from: "2026-10-01",
  to: "2026-10-07",
  status: "succeeded",
});
```

`ChargeParams` accepts the following fields:

| Field | Type | Notes |
|---|---|---|
| `amountKes` | `number` | Amount in shillings. Odd cents are rounded up to the next shilling. |
| `amountCents` | `number` | Alternative to `amountKes`. Odd cents are also rounded up. |
| `phone` | `string` | Customer number, normalized to `254XXXXXXXXX`. |
| `network` | `'mpesa' \| 'airtel' \| 'tkash'` | Defaults to `mpesa`. |
| `accountRef` | `string` | Order or invoice reference, truncated to 64 characters. |
| `description` | `string` | Deprecated and not sent: the rail does not store a description on STK charges. Use `accountRef` or `metadata`. |
| `metadata` | `Record<string, unknown>` | Up to 20 key and value pairs returned on the charge and in webhooks. Values are sent as text, so numbers and booleans are converted for you. |
| `idempotencyKey` | `string` | Stable key, normally the order or invoice reference. |

`exportCharges` returns the CSV response as a string. Valid export statuses include
`succeeded`, `pending`, `failed`, `timeout`, `expired`, and `awaiting_payment`.

Phone numbers are normalized from `0712345678`, `+254712345678`, or `712345678` to
`254712345678`. An invalid number raises `CyzoraError` before any request is sent.

## Hosted checkout

```ts
const session = await cyzora.createCheckoutSession({
  amountKes: 2500,
  accountRef: "Order 1042",
  description: "2 x Margherita pizza",
  callbackUrl: "https://shop.example.com/orders/1042/thanks",
  cancelUrl: "https://shop.example.com/cart",
  customerPhone: "0712345678",
  customerEmail: "buyer@example.com",
  metadata: { order_id: "1042" },
  expiresIn: 1800,
  idempotencyKey: "order-1042",
});

// Redirect to session.checkout_url, then confirm with the webhook or the verify call.
const confirmed = await cyzora.verifyTransaction(session.reference);
```

`CheckoutParams` supports `amountKes`, `accountRef`, `description`, `callbackUrl`,
`cancelUrl`, `customerPhone`, `customerEmail`, `metadata`, `expiresIn`, and
`idempotencyKey`. `expiresIn` must be between 300 and 86400 seconds when supplied.
Redirect URLs must use HTTPS except on localhost, and they must be on an allowed domain
if the account has configured redirect domains.

The returned session starts as `awaiting_payment` and may end as `succeeded`,
`charge.expired`, `failed`, or `timeout`. A customer who returns through `callback_url`
has not necessarily paid; only the webhook or `verifyTransaction` is authoritative.

The same session can be opened in a popup without a server redirect:

```html
<script src="https://checkout.cyzora.co.ke/inline.js"></script>
<script>
  CyzoraCheckout.open({
    reference: "chg_9f8a2c1b4d3e5a6b7c8d",
    onSuccess: (result) => console.log(result.receipt),
    onClose: (result) => console.log(result.status),
  });
</script>
```

The popup callback is not proof of payment. If the customer closes it while a prompt is
out, the payment may still succeed, so reconciliation must happen on the server.

## Payouts

```ts
await cyzora.createPayout({
  amountKes: 1500,
  wallet: "collections",
  destinationType: "mobile",
  phone: "0712345678",
  idempotencyKey: "WD-9001",
});
```

`PayoutParams` accepts `amountKes`, `amountCents`, `wallet`, `channelId`,
`destinationType`, `phone`, `shortCode`, `accountRef`, `bankCode`, `accountNumber`,
`accountName`, and `idempotencyKey`. Supported destination types are `mobile`, `till`,
`paybill`, and `bank`. A saved destination is selected with `channelId` instead of the
destination fields.

The client validates the amount and the destination before sending the request.
Fractional shillings are rejected, the amount must be at least KES 30, mobile payouts
require a valid phone number, till payouts require `shortCode`, paybill payouts require
`shortCode` and `accountRef`, and bank payouts require `bankCode`, `accountNumber`, and
`accountName`. When `wallet` is omitted, the SDK uses `CYZORA_PAYOUT_WALLET` if present;
otherwise the rail uses the account's configured API payout wallet.

Direct destinations are only permitted when the account owner has enabled API payouts
in Settings. Until then the rail returns 403 with the code `raw_destination_disabled`.
Synchronous responses usually report `processing`, so a payout is settled only when the
`payout.paid` webhook arrives.

```ts
const fee = await cyzora.payoutFeePreview(2500, "paybill", "collections");
const banks = await cyzora.listPayoutBanks();
```

The fee preview uses the rail's `destination` query parameter and should carry the same
wallet as the payout so that the preview matches the charge. Bank codes used by
`bankCode` come from `listPayoutBanks()`.

## Wallets, payout lists, references, and exports

```ts
const balances = await cyzora.getWallets();
const channels = await cyzora.listPayoutChannels();
const charges = await cyzora.listCharges({ status: "succeeded", fromDate: "2026-10-01" });
const payouts = await cyzora.listPayouts();          // a plain array, newest first
const payout = await cyzora.getPayout(payouts[0].id);
const statement = await cyzora.verifyStatement("K7PX3MZQ9H2D");
```

`listCharges` and `listPayouts` return arrays. `listCharges` filters by `status`,
`fromDate`, `toDate` and `mode` (`live`, `test` or `all`); `listPayouts` is not filtered
by the rail yet. Wallet balances and payout amounts are returned in cents.

## Moving money between wallets

```ts
await cyzora.previewWalletMove(5000);                            // fee and resulting balances
await cyzora.moveToPayouts(5000, { idempotencyKey: "move-2026-11-27" });
await cyzora.listWalletTransfers(20);                            // { data: [...] }

// Parent businesses can top up a subsidiary's Payouts wallet.
const { data: subs } = await cyzora.listSubsidiaries();
await cyzora.previewSubsidiaryTransfer({ subsidiaryId: subs[0].id, amountKes: 2000, fromWallet: "collections" });
await cyzora.createSubsidiaryTransfer({
  subsidiaryId: subs[0].id, amountKes: 2000, fromWallet: "collections", idempotencyKey: "sub-topup-1",
});
```

Wallet moves use real balances, so test keys are refused. Bulk payout batches are
created in the dashboard; `listPayoutBatches()` and `getPayoutBatch(id)` read them. `verifyStatement` calls the
public statement endpoint and deliberately omits the API key. It returns validity,
business name, period, generated time, balances in and out, transaction count, and the
statement's SHA-256 digest.

## Webhooks

Cyzora signs every webhook with `X-Cyzora-Signature`, a hexadecimal HMAC-SHA256 of the
raw request body, and also sends the event name in `X-Cyzora-Event`. Verify against the
raw bytes before parsing JSON, and reply with any 2xx status. Failed deliveries are
retried after 10 seconds, 30 seconds, 2 minutes, and 10 minutes.

```ts
import express from "express";
import { Cyzora, WebhookEvents } from "cyzora";

const app = express();

app.post("/webhooks/cyzora", express.raw({ type: "*/*" }), (req, res) => {
  let event;
  try {
    event = cyzora.constructWebhookEvent(
      req.body,
      req.headers["x-cyzora-signature"] as string,
      req.headers["x-cyzora-event"] as string,
    );
  } catch {
    return res.status(401).send("invalid signature");
  }

  switch (event.event) {
    case WebhookEvents.ChargeSucceeded: break;
    case WebhookEvents.ChargeFailed: break;
    case WebhookEvents.ChargeExpired: break;
    case WebhookEvents.PayoutPaid: break;
    case WebhookEvents.PayoutFailed: break;
    case WebhookEvents.WebhookTest: break;
    default: break;
  }
  res.json({ received: true });
});
```

`verifyWebhookSignature(rawBody, signature)` returns a boolean and checks every
configured secret in constant time. `constructWebhookEvent(rawBody, signature, eventHeader?)`
verifies, parses, and optionally checks that `eventHeader` matches the body's `event`.
The third argument is optional; when supplied, a mismatch raises `CyzoraError`.
`WebhookEvents` contains `ChargeSucceeded`, `ChargeFailed`, `ChargeExpired`,
`PayoutPaid`, `PayoutFailed`, and `WebhookTest`.

Webhook payloads are flat. A charge event carries `event`, `charge_id`, `reference`,
`account_ref`, `amount`, `currency`, `status`, `mpesa_receipt`, `metadata`, and
`livemode`. A payout event carries `event`, `payout_id`, `channel_id`, `amount`, `fee`,
`status`, and `transaction_id`. There is no `data` wrapper. `charge.failed` covers
failures, timeouts, and cancellations, and checkout retries are indicated by
`retryable: true`.

Multiple webhook secrets are supported:

```ts
const cyzora = new Cyzora({
  secretKey: process.env.CYZORA_SECRET_KEY!,
  webhookSecrets: ["whsec_a...", "whsec_b..."],
});
```

Verification succeeds when the signature matches any configured secret.

## Escape hatch and retries

Any endpoint can be called through `request`, which applies the same signing, retry,
and parsing rules as the typed methods.

```ts
await cyzora.request("POST", "/charges", {
  body: { amount: 25000, phone: "254712345678", network: "airtel" },
  idempotencyKey: "k1",
});
const result = await cyzora.request<{ id: string }>("GET", "/some-new-endpoint");
const csv = await cyzora.request<string>("GET", "/charges/export", { raw: true });
```

The path is appended to the base URL, which already includes `/api/v1`. The options
object accepts `body`, `idempotencyKey`, `query`, `auth`, and `raw`. Set `auth: false`
for public endpoints and `raw: true` to receive the response text instead of JSON.

Transient failures, namely network errors, HTTP 5xx, and HTTP 429, are retried with
exponential backoff. A 409 returned for a request with an idempotency key is also
retried, since the documentation advises waiting and reusing that key. Retries happen
only for GET requests or requests carrying an idempotency key, so a charge or payout
cannot be duplicated by a retry.

## Errors

Any non-2xx response or transport failure raises `CyzoraError`, which exposes `message`,
`status`, and `body`.

```ts
import { CyzoraError } from "cyzora";

try {
  await cyzora.createPayout({ amountKes: 10, phone: "0712345678" });
} catch (error) {
  if (error instanceof CyzoraError) {
    console.error(error.status, error.message, error.body);
  }
}
```

The documented status codes are 400 for bad requests, 401 for a missing or invalid
key, 403 for an unverified business or disabled direct payouts, 404 for unknown
resources, 409 for conflicts, 410 for expired links, 422 for unverified channels, and
429 for the per-key rate limit of 100 requests per minute.

## Helpers and exported types

```ts
import {
  normalizePhone,
  toCents,
  fromCents,
  PAYOUT_MIN_CENTS,
  WebhookEvents,
} from "cyzora";

normalizePhone("0712345678"); // 254712345678
toCents(2500.4);              // 250100
fromCents(250000);            // 2500
PAYOUT_MIN_CENTS;             // 3000
```

The package also exports `Network`, `DestinationType`, `ChargeParams`,
`CheckoutParams`, `PayoutParams`, `CyzoraOptions`, `WebhookEvent`, and the default
`Cyzora` class. Payout amounts are validated inside `createPayout` and
`payoutFeePreview`.

## Test mode

Test keys start with `sk_test_`. The documented test phones are `254700000001`, which
succeeds, `254700000002`, which fails for low balance, `254700000003`, which fails
because the customer cancelled, and `254700000004`, which times out. Test webhooks are
signed like live webhooks and carry `"livemode": false`. Test payouts are simulated,
do not change balances, and are not listed by `GET /payouts`.

## API surface

| Method | Endpoint |
|---|---|
| `createCharge` | `POST /charges` |
| `getCharge` | `GET /charges/:id` |
| `listCharges` | `GET /charges` |
| `exportCharges` | `GET /charges/export` |
| `createCheckoutSession` | `POST /transactions/initialize` |
| `verifyTransaction` | `GET /transactions/verify/:ref` |
| `createPayout` | `POST /payouts` |
| `getPayout` | `GET /payouts/:id` |
| `listPayouts` | `GET /payouts` |
| `listPayoutBatches` | `GET /payouts/batches` |
| `getPayoutBatch` | `GET /payouts/batches/:id` |
| `payoutFeePreview` | `GET /payouts/fee-preview` |
| `getWallets` | `GET /wallets` |
| `previewWalletMove` | `GET /wallets/move/preview` |
| `moveToPayouts` | `POST /wallets/move` |
| `listWalletTransfers` | `GET /wallets/transfers` |
| `listSubsidiaries` | `GET /wallets/subsidiaries` |
| `previewSubsidiaryTransfer` | `GET /wallets/subsidiary-transfers/preview` |
| `createSubsidiaryTransfer` | `POST /wallets/subsidiary-transfers` |
| `listSubsidiaryTransfers` | `GET /wallets/subsidiary-transfers` |
| `listPayoutChannels` | `GET /payout-channels` |
| `listPayoutBanks` | `GET /payout-banks` |
| `verifyStatement` | `GET /public/statements/verify/:code` |
| `verifyWebhookSignature` | Local HMAC check |
| `constructWebhookEvent` | Verify and parse webhook body |
| `request` | Generic escape hatch |

## Development

```bash
npm run build   # compile TypeScript into dist/
npm test        # build, then run node:test suites in tests/
```

The root repository also provides `README.md` for the shared contract and
`EXAMPLES.md` for cross-language examples.
