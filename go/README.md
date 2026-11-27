# Cyzora SDK for Go

This package is the Go client for the Cyzora M-Pesa payment rail. It supports charges
(STK push), hosted checkout, payouts, wallets, payout channels and banks, charge exports,
public statement verification, and webhook verification. The implementation uses only
the standard library and requires Go 1.21 or newer.

The tracking contract comes from the local API reference. Amounts on the wire are
integer cents of whole shillings, so KES 2,500 is `250000`. Charges and checkout
sessions round odd cents up to the next shilling, whereas payouts reject any amount
that is not a multiple of 100 cents. Every money-moving request should carry a stable
idempotency key, and the smallest payout is KES 30.

## Installation

```bash
go get github.com/meshackbahati/cyzora-sdk/go
```

When working from this repository, the module is named
`github.com/meshackbahati/cyzora-sdk/go` and can be imported directly from the `go/`
directory.

```go
import "github.com/meshackbahati/cyzora-sdk/go"
```

## Creating a client

```go
client := cyzora.New(
    os.Getenv("CYZORA_SECRET_KEY"),
    cyzora.WithWebhookSecret(os.Getenv("CYZORA_WEBHOOK_SECRET")),
)
```

`New` accepts functional options:

| Option | Behaviour |
|---|---|
| `WithBaseURL(u string)` | Overrides the API base. Defaults to `CYZORA_API_BASE`, then `https://api.cyzora.co.ke/api/v1`. |
| `WithWebhookSecret(s string)` | Adds a signing secret. Multiple calls add multiple secrets. Defaults to comma-separated `CYZORA_WEBHOOK_SECRET`. |
| `WithHTTPClient(h *http.Client)` | Overrides the HTTP client. The default has a 30 second timeout. |
| `WithMaxRetries(n int)` | Sets transient retries. Defaults to 2. |

Secret keys and webhook secrets must remain on the server. The public statement
endpoint is called without an `X-API-Key` header.

## Quick start: charge a customer

```go
charge, err := client.CreateCharge(ctx, cyzora.ChargeParams{
    AmountKes:      250,
    Phone:          "0712345678",
    Network:        "mpesa",
    AccountRef:     "INV-1001",
    IdempotencyKey: "INV-1001",
    Metadata:       map[string]interface{}{"order_id": "1001"},
})
if err != nil {
    return err
}
log.Println(charge["id"], charge["status"])
```

The charge starts as `pending` and ends as `succeeded`, `failed`, or `timeout`. A 202
response may carry a `warning` alongside the charge when the rail is unsure whether the
prompt went out. In that case the charge is pending and its webhook is authoritative, so
the request must not be repeated.

## Charges

```go
sameCharge, err := client.GetCharge(ctx, chargeID)
csvText, err := client.ExportCharges(ctx, map[string]string{
    "from":   "2026-10-01",
    "to":     "2026-10-07",
    "status": "succeeded",
})
```

`ChargeParams` has these fields:

| Field | Type | Notes |
|---|---|---|
| `AmountKes` | `float64` | Amount in shillings. Odd cents are rounded up to the next shilling. |
| `AmountCents` | `int64` | Alternative to `AmountKes`. Odd cents are also rounded up. |
| `Phone` | `string` | Customer number, normalized to `254XXXXXXXXX`. |
| `Network` | `string` | `mpesa`, `airtel`, or `tkash`. Defaults to `mpesa`. |
| `AccountRef` | `string` | Order or invoice reference, truncated to 64 characters. |
| `Description` | `string` | Deprecated and not sent: the rail does not store a description on STK charges. Use `AccountRef` or `Metadata`. |
| `Metadata` | `map[string]interface{}` | Up to 20 key and value pairs returned on the charge and in webhooks. Values are sent as text, so numbers and booleans are converted for you. |
| `IdempotencyKey` | `string` | Stable key, normally the order or invoice reference. |

`NormalizePhone` converts `0712345678`, `+254712345678`, or `712345678` to
`254712345678`. An invalid number is reported as an error before any request is sent.
`ExportCharges` returns the CSV response as a string.

## Hosted checkout

```go
session, err := client.CreateCheckoutSession(ctx, cyzora.CheckoutParams{
    AmountKes:      2500,
    AccountRef:     "Order 1042",
    Description:    "2 x Margherita pizza",
    CallbackURL:    "https://shop.example.com/orders/1042/thanks",
    CancelURL:      "https://shop.example.com/cart",
    CustomerPhone:  "0712345678",
    CustomerEmail:  "buyer@example.com",
    Metadata:       map[string]interface{}{"order_id": "1042"},
    ExpiresIn:      1800,
    IdempotencyKey: "order-1042",
})
if err != nil {
    return err
}

checkoutURL := session["checkout_url"].(string)
confirmed, err := client.VerifyTransaction(ctx, session["reference"].(string))
```

`CheckoutParams` supports `AmountKes`, `AccountRef`, `Description`, `CallbackURL`,
`CancelURL`, `CustomerPhone`, `CustomerEmail`, `Metadata`, `ExpiresIn`, and
`IdempotencyKey`. `ExpiresIn` must be between 300 and 86400 seconds when supplied.
Redirect URLs must use HTTPS except on localhost, and they must be on an allowed domain
if the account has configured redirect domains.

The returned session starts as `awaiting_payment`. A customer who returns through
`callback_url` has not necessarily paid; only the webhook or `VerifyTransaction` is
authoritative. The same session can be opened in a popup with
`https://checkout.cyzora.co.ke/inline.js`, and the browser callback still requires
server-side confirmation.

## Payouts

```go
_, err := client.CreatePayout(ctx, cyzora.PayoutParams{
    AmountKes:       1500,
    Wallet:          "collections",
    DestinationType: "mobile",
    Phone:           "0712345678",
    IdempotencyKey:  "WD-9001",
})
```

`PayoutParams` has these fields: `AmountKes`, `AmountCents`, `Wallet`, `ChannelID`,
`DestinationType`, `Phone`, `ShortCode`, `AccountRef`, `BankCode`, `AccountNumber`,
`AccountName`, and `IdempotencyKey`. Supported destination types are `mobile`, `till`,
`paybill`, and `bank`. A saved destination is selected with `ChannelID` instead of
destination fields.

The client validates the amount and destination before sending the request. Fractional
shillings are rejected, the amount must be at least KES 30, mobile payouts require a
valid phone number, till payouts require `ShortCode`, paybill payouts require `ShortCode`
and `AccountRef`, and bank payouts require `BankCode`, `AccountNumber`, and
`AccountName`. When `Wallet` is omitted, the SDK uses `CYZORA_PAYOUT_WALLET` if present;
otherwise the rail uses the account's configured API payout wallet.

Direct destinations are only permitted when the account owner has enabled API payouts
in Settings. Until then the rail returns 403 with the code `raw_destination_disabled`.
Synchronous responses usually report `processing`, so a payout is settled only when the
`payout.paid` webhook arrives.

```go
fee, err := client.PayoutFeePreview(ctx, 2500, "paybill", "collections")
banks, err := client.ListPayoutBanks(ctx)
```

The fee preview uses the rail's `destination` query parameter and should carry the same
wallet as the payout so that the preview matches the charge. Bank codes used by
`BankCode` come from `ListPayoutBanks`.

## Wallets, payout lists, references, and exports

```go
balances, err := client.GetWallets(ctx)
channels, err := client.ListPayoutChannels(ctx)
charges, err := client.ListCharges(ctx, cyzora.ListChargesParams{Status: "succeeded", FromDate: "2026-10-01"})
payouts, err := client.ListPayouts(ctx, nil) // []map[string]interface{}, newest first
payout, err := client.GetPayout(ctx, payouts[0]["id"].(string))
statement, err := client.VerifyStatement(ctx, "K7PX3MZQ9H2D")
```

`ListCharges`, `ListPayouts` and `ListPayoutBatches` return `[]map[string]interface{}`
because the rail answers with JSON arrays. `ListPayouts` is not filtered by the rail yet.

## Moving money between wallets

```go
preview, err := client.PreviewWalletMove(ctx, 5000, 0) // fee and resulting balances
move, err := client.MoveToPayouts(ctx, cyzora.WalletMoveParams{AmountKes: 5000, IdempotencyKey: "move-2026-11-27"})
history, err := client.ListWalletTransfers(ctx, 20)    // {"data": [...]}

// Parent businesses can top up a subsidiary's Payouts wallet.
topup, err := client.CreateSubsidiaryTransfer(ctx, cyzora.SubsidiaryTransferParams{
    SubsidiaryID: subID, AmountKes: 2000, FromWallet: "collections", IdempotencyKey: "sub-topup-1",
})
```

Wallet moves use real balances, so test keys are refused. Bulk payout batches are
created in the dashboard; `ListPayoutBatches` and `GetPayoutBatch` read them. For any
endpoint that returns an array, `RequestInto` decodes into a value you pass.

Wallet balances and payout amounts are returned in cents as
`map[string]interface{}` values. `VerifyStatement` calls the public statement endpoint
and deliberately omits the API key. It returns validity, business name, period,
generated time, balances in and out, transaction count, and the statement's SHA-256
digest.

## Webhooks

Cyzora signs every webhook with `X-Cyzora-Signature`, a hexadecimal HMAC-SHA256 of the
raw request body, and also sends the event name in `X-Cyzora-Event`. Verify against the
raw bytes before parsing JSON, and reply with any 2xx status. Failed deliveries are
retried after 10 seconds, 30 seconds, 2 minutes, and 10 minutes.

```go
func webhookHandler(c *cyzora.Client) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        raw, err := io.ReadAll(r.Body)
        if err != nil {
            http.Error(w, "invalid body", http.StatusBadRequest)
            return
        }

        event, err := c.ConstructWebhookEvent(
            raw,
            r.Header.Get("X-Cyzora-Signature"),
            r.Header.Get("X-Cyzora-Event"),
        )
        if err != nil {
            http.Error(w, "invalid signature", http.StatusUnauthorized)
            return
        }

        switch event["event"] {
        case cyzora.EventChargeSucceeded:
            markInvoicePaid(event)
        case cyzora.EventPayoutPaid:
            markWithdrawalPaid(event)
        case cyzora.EventWebhookTest:
            // Delivery test, no money moved.
        default:
            // Log unknown events.
        }

        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(`{"received":true}`))
    }
}
```

`VerifyWebhookSignature(rawBody []byte, signature string) bool` checks every configured
secret with `hmac.Equal`. `ConstructWebhookEvent(rawBody []byte, signature string,
eventHeader ...string)` verifies and parses the body. When an event header is passed, it
must match the body's `event`; otherwise an `*Error` with status 400 is returned. The
event constants are `EventChargeSucceeded`, `EventChargeFailed`, `EventChargeExpired`,
`EventPayoutPaid`, `EventPayoutFailed`, and `EventWebhookTest`.

Webhook payloads are flat. A charge event carries `event`, `charge_id`, `reference`,
`account_ref`, `amount`, `currency`, `status`, `mpesa_receipt`, `metadata`, and
`livemode`. A payout event carries `event`, `payout_id`, `channel_id`, `amount`, `fee`,
`status`, and `transaction_id`. There is no `data` wrapper. `charge.failed` covers
failures, timeouts, and cancellations, and checkout retries are indicated by
`retryable: true`.

Multiple webhook secrets are supported:

```go
client := cyzora.New(secretKey,
    cyzora.WithWebhookSecret("whsec_a..."),
    cyzora.WithWebhookSecret("whsec_b..."),
)
```

Verification succeeds when the signature matches any configured secret.

## Escape hatch and retries

Any endpoint can be called through `Request`, which applies the same signing, retry,
and parsing rules as the typed methods.

```go
out, err := client.Request(ctx, "POST", "/charges", map[string]interface{}{
    "amount": 25000,
    "phone":  "254712345678",
    "network": "airtel",
}, "k1", nil)

newEndpoint, err := client.Request(ctx, "GET", "/some-new-endpoint", nil, "", nil)
```

The path is appended to the base URL, which already includes `/api/v1`. Public endpoints
can be called with `PublicRequest(ctx, method, path, query)`, which omits the API key.
For a CSV response, use `ExportCharges` or decode the response through `Request` only
when the endpoint returns JSON.

Transient failures, namely network errors, HTTP 5xx, and HTTP 429, are retried with
exponential backoff. A 409 returned for a request with an idempotency key is also
retried, since the documentation advises waiting and reusing that key. Retries happen
only for GET requests or requests carrying an idempotency key, so a charge or payout
cannot be duplicated by a retry.

## Errors

Non-2xx responses and transport failures return `*cyzora.Error`, which exposes
`Message`, `Status`, and `Body`.

```go
if _, err := client.CreatePayout(ctx, cyzora.PayoutParams{
    AmountKes: 10, Phone: "0712345678",
}); err != nil {
    var apiErr *cyzora.Error
    if errors.As(err, &apiErr) {
        log.Println(apiErr.Status, apiErr.Message, apiErr.Body)
    }
}
```

The documented status codes are 400 for bad requests, 401 for a missing or invalid
key, 403 for an unverified business or disabled direct payouts, 404 for unknown
resources, 409 for conflicts, 410 for expired links, 422 for unverified channels, and
429 for the per-key rate limit of 100 requests per minute.

## Helpers and constants

```go
cyzora.NormalizePhone("0712345678") // 254712345678
cents, err := cyzora.ToCents(2500.4) // 250100
kes := cyzora.FromCents(250000)      // 2500
cyzora.PayoutMinCents                // 3000
cyzora.DefaultBaseURL                // https://api.cyzora.co.ke/api/v1
```

Payout amounts are validated inside `CreatePayout` and `PayoutFeePreview`, so callers
do not have to convert amounts themselves.

## Test mode

Test keys start with `sk_test_`. The documented test phones are `254700000001`, which
succeeds, `254700000002`, which fails for low balance, `254700000003`, which fails
because the customer cancelled, and `254700000004`, which times out. Test webhooks are
signed like live webhooks and carry `"livemode": false`. Test payouts are simulated,
do not change balances, and are not listed by `GET /payouts`.

## API surface

| Method | Endpoint |
|---|---|
| `CreateCharge` | `POST /charges` |
| `GetCharge` | `GET /charges/:id` |
| `ListCharges` | `GET /charges` |
| `ExportCharges` | `GET /charges/export` |
| `CreateCheckoutSession` | `POST /transactions/initialize` |
| `VerifyTransaction` | `GET /transactions/verify/:ref` |
| `CreatePayout` | `POST /payouts` |
| `GetPayout` | `GET /payouts/:id` |
| `ListPayouts` | `GET /payouts` |
| `ListPayoutBatches` | `GET /payouts/batches` |
| `GetPayoutBatch` | `GET /payouts/batches/:id` |
| `PayoutFeePreview` | `GET /payouts/fee-preview` |
| `GetWallets` | `GET /wallets` |
| `PreviewWalletMove` | `GET /wallets/move/preview` |
| `MoveToPayouts` | `POST /wallets/move` |
| `ListWalletTransfers` | `GET /wallets/transfers` |
| `ListSubsidiaries` | `GET /wallets/subsidiaries` |
| `PreviewSubsidiaryTransfer` | `GET /wallets/subsidiary-transfers/preview` |
| `CreateSubsidiaryTransfer` | `POST /wallets/subsidiary-transfers` |
| `ListSubsidiaryTransfers` | `GET /wallets/subsidiary-transfers` |
| `ListPayoutChannels` | `GET /payout-channels` |
| `ListPayoutBanks` | `GET /payout-banks` |
| `VerifyStatement` | `GET /public/statements/verify/:code` |
| `VerifyWebhookSignature` | Local HMAC check |
| `ConstructWebhookEvent` | Verify and parse webhook body |
| `Request`, `RequestInto`, `PublicRequest` | Generic escape hatches |

## Development

```bash
go vet ./...
go test ./...
go build ./...
```

The root repository also provides `README.md` for the shared contract and
`EXAMPLES.md` for cross-language examples.
