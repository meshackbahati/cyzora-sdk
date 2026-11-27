# Cyzora SDK for PHP

This library is the PHP client for the Cyzora M-Pesa payment rail. It supports charges
(STK push), hosted checkout, payouts, wallets, payout channels and banks, charge exports,
public statement verification, and webhook verification. The implementation uses cURL
and has no third-party dependencies.

The tracking contract comes from the local API reference. Amounts on the wire are
integer cents of whole shillings, so KES 2,500 is `250000`. Charges and checkout
sessions round odd cents up to the next shilling, whereas payouts reject any amount
that is not a multiple of 100 cents. Every money-moving request should carry a stable
idempotency key, and the smallest payout is KES 30.

## Requirements and installation

PHP 7.4 or newer with the cURL and JSON extensions is required.

```bash
composer require cyzora/cyzora
```

When working from this repository, the class can be required directly:

```php
require __DIR__ . '/src/Client.php';
```

## Creating a client

```php
$cyzora = new \Cyzora\Client(
    getenv('CYZORA_SECRET_KEY'),
    ['webhookSecret' => getenv('CYZORA_WEBHOOK_SECRET')]
);
```

The constructor signature is:

```php
new \Cyzora\Client(string $secretKey, array $options = [])
```

| Option | Type | Behaviour |
|---|---|---|
| `baseUrl` | `string` | Optional API base. Defaults to `CYZORA_API_BASE`, then `https://api.cyzora.co.ke/api/v1`. |
| `webhookSecret` | `string \| string[]` | Signing secrets. Defaults to `CYZORA_WEBHOOK_SECRET`, which may be comma-separated. |
| `timeout` | `int` | cURL timeout in seconds. Defaults to 30. |
| `maxRetries` | `int` | Transient retries. Defaults to 2. |

Secret keys and webhook secrets must remain on the server. The public statement
endpoint is called without an `X-API-Key` header.

## Quick start: charge a customer

```php
$charge = $cyzora->createCharge(250, '0712345678', [
    'network' => 'mpesa',
    'accountRef' => 'INV-1001',
    'idempotencyKey' => 'INV-1001',
    'metadata' => ['order_id' => '1001'],
]);

echo $charge['id'], ' ', $charge['status'];
```

The charge starts as `pending` and ends as `succeeded`, `failed`, or `timeout`. A 202
response may carry a `warning` alongside the charge when the rail is unsure whether the
prompt went out. In that case the charge is pending and its webhook is authoritative, so
the request must not be repeated.

## Charges

```php
$charge = $cyzora->createCharge(2500.4, '0712345678');
$sameCharge = $cyzora->getCharge($charge['id']);
$csv = $cyzora->exportCharges([
    'from' => '2026-10-01',
    'to' => '2026-10-07',
    'status' => 'succeeded',
]);
```

The third argument to `createCharge` accepts these options:

| Option | Notes |
|---|---|
| `amountKes` | First argument. Odd cents are rounded up to the next shilling. |
| `amountCents` | Alternative to the first argument. Odd cents are also rounded up. |
| `network` | `mpesa`, `airtel`, or `tkash`. Defaults to `mpesa`. |
| `accountRef` | Order or invoice reference, truncated to 64 characters. |
| `description` | Deprecated and not sent: the rail does not store a description on STK charges. Use `accountRef` or `metadata`. |
| `metadata` | Up to 20 key and value pairs returned on the charge and in webhooks. Values are sent as text, so numbers and booleans are converted for you. |
| `idempotencyKey` | Stable key, normally the order or invoice reference. |

Phone numbers are normalized from `0712345678`, `+254712345678`, or `712345678` to
`254712345678`. An invalid number raises `CyzoraException` before any request is sent.
`exportCharges` returns CSV text and accepts `from`, `to`, and `status` filters.

## Hosted checkout

```php
$session = $cyzora->createCheckoutSession(2500, [
    'accountRef' => 'Order 1042',
    'description' => '2 x Margherita pizza',
    'callbackUrl' => 'https://shop.example.com/orders/1042/thanks',
    'cancelUrl' => 'https://shop.example.com/cart',
    'customerPhone' => '0712345678',
    'customerEmail' => 'buyer@example.com',
    'metadata' => ['order_id' => '1042'],
    'expiresIn' => 1800,
    'idempotencyKey' => 'order-1042',
]);

$checkoutUrl = $session['checkout_url'];
$confirmed = $cyzora->verifyTransaction($session['reference']);
```

`createCheckoutSession` accepts `accountRef`, `description`, `callbackUrl`, `cancelUrl`,
`customerPhone`, `customerEmail`, `metadata`, `expiresIn`, and `idempotencyKey`.
`expiresIn` must be between 300 and 86400 seconds when supplied. Redirect URLs must use
HTTPS except on localhost, and they must be on an allowed domain if the account has
configured redirect domains.

The returned session starts as `awaiting_payment`. A customer who returns through
`callback_url` has not necessarily paid; only the webhook or `verifyTransaction` is
authoritative. The same session can be opened in a popup with
`https://checkout.cyzora.co.ke/inline.js`, and the browser callback still requires
server-side confirmation.

## Payouts

```php
$cyzora->createPayout(1500, [
    'wallet' => 'collections',
    'destinationType' => 'mobile',
    'phone' => '0712345678',
    'idempotencyKey' => 'WD-9001',
]);
```

The options array accepts `amountCents`, `wallet`, `channelId`, `destinationType`,
`phone`, `shortCode`, `accountRef`, `bankCode`, `accountNumber`, `accountName`, and
`idempotencyKey`. Supported destination types are `mobile`, `till`, `paybill`, and
`bank`. A saved destination is selected with `channelId` instead of destination fields.

The client validates the amount and destination before sending the request. Fractional
shillings are rejected, the amount must be at least KES 30, mobile payouts require a
valid phone number, till payouts require `shortCode`, paybill payouts require `shortCode`
and `accountRef`, and bank payouts require `bankCode`, `accountNumber`, and
`accountName`. When `wallet` is omitted, the SDK uses `CYZORA_PAYOUT_WALLET` if present;
otherwise the rail uses the account's configured API payout wallet.

Direct destinations are only permitted when the account owner has enabled API payouts
in Settings. Until then the rail returns 403 with the code `raw_destination_disabled`.
Synchronous responses usually report `processing`, so a payout is settled only when the
`payout.paid` webhook arrives.

```php
$fee = $cyzora->payoutFeePreview(2500, 'paybill', 'collections');
$banks = $cyzora->listPayoutBanks();
```

The fee preview uses the rail's `destination` query parameter and should carry the same
wallet as the payout so that the preview matches the charge. Bank codes used by
`bankCode` come from `listPayoutBanks()`.

## Wallets, payout lists, references, and exports

```php
$balances = $cyzora->getWallets();
$channels = $cyzora->listPayoutChannels();
$charges = $cyzora->listCharges(['status' => 'succeeded', 'fromDate' => '2026-10-01']);
$payouts = $cyzora->listPayouts();          // a plain list, newest first
$payout = $cyzora->getPayout($payouts[0]['id']);
$statement = $cyzora->verifyStatement('K7PX3MZQ9H2D');
```

`listCharges` filters by `status`, `fromDate`, `toDate` and `mode` (`live`, `test` or
`all`); `listPayouts` is not filtered by the rail yet.

## Moving money between wallets

```php
$cyzora->previewWalletMove(5000);                                // fee and resulting balances
$cyzora->moveToPayouts(5000, ['idempotencyKey' => 'move-2026-11-27']);
$cyzora->listWalletTransfers(20);                                // ['data' => [...]]

// Parent businesses can top up a subsidiary's Payouts wallet.
$subs = $cyzora->listSubsidiaries()['data'];
$cyzora->previewSubsidiaryTransfer($subs[0]['id'], 2000, ['fromWallet' => 'collections']);
$cyzora->createSubsidiaryTransfer($subs[0]['id'], 2000, [
    'fromWallet' => 'collections',
    'idempotencyKey' => 'sub-topup-1',
]);
```

Wallet moves use real balances, so test keys are refused. Bulk payout batches are
created in the dashboard; `listPayoutBatches()` and `getPayoutBatch($id)` read them.

Wallet balances and payout amounts are returned in cents. `verifyStatement` calls the
public statement endpoint and deliberately omits the API key. It returns validity,
business name, period, generated time, balances in and out, transaction count, and the
statement's SHA-256 digest.

## Webhooks

Cyzora signs every webhook with `X-Cyzora-Signature`, a hexadecimal HMAC-SHA256 of the
raw request body, and also sends the event name in `X-Cyzora-Event`. Verify against the
raw bytes before parsing JSON, and reply with any 2xx status. Failed deliveries are
retried after 10 seconds, 30 seconds, 2 minutes, and 10 minutes.

Read the body with `file_get_contents('php://input')` so that the signature covers the
exact bytes received.

```php
$raw = file_get_contents('php://input');

try {
    $event = $cyzora->constructWebhookEvent(
        $raw,
        $_SERVER['HTTP_X_CYZORA_SIGNATURE'] ?? null,
        $_SERVER['HTTP_X_CYZORA_EVENT'] ?? null
    );
} catch (\Cyzora\CyzoraException $e) {
    http_response_code(401);
    exit('invalid signature');
}

switch ($event['event']) {
    case 'charge.succeeded':
        markInvoicePaid($event);
        break;
    case 'payout.paid':
        markWithdrawalPaid($event);
        break;
    case 'webhook.test':
        break;
    default:
        break;
}

http_response_code(200);
echo json_encode(['received' => true]);
```

`verifyWebhookSignature(string $rawBody, ?string $signature)` returns a boolean and uses
`hash_equals` for each configured secret. `constructWebhookEvent(string $rawBody,
?string $signature, ?string $eventHeader = null)` verifies, parses, and optionally
checks that the header matches the body's `event`. When the header is supplied, a
mismatch raises `CyzoraException` with status 400.

The constant `Client::WEBHOOK_EVENTS` contains `charge.succeeded`, `charge.failed`,
`charge.expired`, `payout.paid`, `payout.failed`, and `webhook.test`.

Webhook payloads are flat. A charge event carries `event`, `charge_id`, `reference`,
`account_ref`, `amount`, `currency`, `status`, `mpesa_receipt`, `metadata`, and
`livemode`. A payout event carries `event`, `payout_id`, `channel_id`, `amount`, `fee`,
`status`, and `transaction_id`. There is no `data` wrapper. `charge.failed` covers
failures, timeouts, and cancellations, and checkout retries are indicated by
`retryable: true`.

Multiple webhook secrets are supported:

```php
$cyzora = new \Cyzora\Client($secretKey, [
    'webhookSecret' => ['whsec_a...', 'whsec_b...'],
]);
```

Verification succeeds when the signature matches any configured secret.

## Escape hatch and retries

Any endpoint can be called through `rawRequest`, which applies the same signing, retry,
and parsing rules as the typed methods.

```php
$cyzora->rawRequest('POST', '/charges', [
    'amount' => 25000,
    'phone' => '254712345678',
    'network' => 'airtel',
], 'k1');

$result = $cyzora->rawRequest('GET', '/some-new-endpoint');
$public = $cyzora->rawRequest('GET', '/public/statements/verify/K7PX3MZQ9H2D', null, null, null, false);
$csv = $cyzora->rawRequest('GET', '/charges/export', null, null, null, true, true);
```

The path is appended to the base URL, which already includes `/api/v1`. The signature is `rawRequest(string $method, string $path, ?array $body = null, ?string $idempotencyKey = null, ?array $query = null, bool $auth = true, bool $raw = false)`. Set `$auth` to false
for public endpoints and `$raw` to true to receive the response text instead of decoded
JSON. The method returns an array or a string.

Transient failures, namely network errors, HTTP 5xx, and HTTP 429, are retried with
exponential backoff. A 409 returned for a request with an idempotency key is also
retried, since the documentation advises waiting and reusing that key. Retries happen
only for GET requests or requests carrying an idempotency key, so a charge or payout
cannot be duplicated by a retry.

## Errors

Any non-2xx response or transport failure raises `\Cyzora\CyzoraException`, which
exposes `status` and `body` in addition to the message.

```php
try {
    $cyzora->createPayout(10, ['phone' => '0712345678']);
} catch (\Cyzora\CyzoraException $e) {
    error_log($e->status . ' ' . $e->getMessage());
}
```

The documented status codes are 400 for bad requests, 401 for a missing or invalid
key, 403 for an unverified business or disabled direct payouts, 404 for unknown
resources, 409 for conflicts, 410 for expired links, 422 for unverified channels, and
429 for the per-key rate limit of 100 requests per minute.

## Helpers

```php
use Cyzora\Client;

Client::normalizePhone('0712345678'); // 254712345678
Client::toCents(2500.4);              // 250100
Client::toPayoutCents(1500);          // 150000
Client::fromCents(250000);            // 2500.0
Client::PAYOUT_MIN_CENTS;             // 3000
Client::WEBHOOK_EVENTS;               // known event names
```

`toPayoutCents` throws `CyzoraException` for fractional shillings. Payout methods call
it internally, so callers do not have to convert amounts themselves.

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
| `rawRequest` | Generic escape hatch |

## Development

```bash
php -l src/Client.php
php tests/ClientTest.php
```

The root repository also provides `README.md` for the shared contract and
`EXAMPLES.md` for cross-language examples.
