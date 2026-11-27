# Cyzora SDK — Examples

Runnable patterns for the common flows in each language. Replace `sk_live_...` and
`whsec_...` with your own keys, keep them server-side, and start with a `sk_test_` key.

---

## 1. Charge a customer (M-Pesa STK push)

### TypeScript / Node
```ts
import { Cyzora } from "cyzora";
const cyzora = new Cyzora({ secretKey: process.env.CYZORA_SECRET_KEY! });

const charge = await cyzora.createCharge({
  amountKes: 250,
  phone: "0712345678",
  accountRef: "INV-1001",
  network: "mpesa", // mpesa, airtel or tkash
  idempotencyKey: "INV-1001", // stable per logical charge, so retries are safe
  metadata: { orderId: "1001" },
});
// charge.status is typically "pending" until the customer enters their PIN.
```

### Python
```python
from cyzora import Cyzora
client = Cyzora(os.environ["CYZORA_SECRET_KEY"])
charge = client.create_charge(250, "0712345678", account_ref="INV-1001", idempotency_key="INV-1001")
```

### PHP
```php
$cyzora = new \Cyzora\Client(getenv('CYZORA_SECRET_KEY'));
$charge = $cyzora->createCharge(250, '0712345678', ['accountRef' => 'INV-1001', 'idempotencyKey' => 'INV-1001']);
```

### Go
```go
client := cyzora.New(os.Getenv("CYZORA_SECRET_KEY"))
charge, err := client.CreateCharge(ctx, cyzora.ChargeParams{
    AmountKes: 250, Phone: "0712345678", AccountRef: "INV-1001", IdempotencyKey: "INV-1001",
})
```

Test the flow with `254700000001`, which succeeds in test mode. Other documented test
phones simulate low balance (`254700000002`), cancellation (`254700000003`) and timeout
(`254700000004`).

---

## 2. Hosted checkout

```ts
const session = await cyzora.createCheckoutSession({
  amountKes: 2500,
  accountRef: "Order 1042",
  description: "2 x Margherita pizza",
  callbackUrl: "https://shop.example.com/orders/1042/thanks",
  cancelUrl: "https://shop.example.com/cart",
  customerPhone: "0712345678",
  metadata: { order_id: "1042" },
  expiresIn: 1800,
  idempotencyKey: "order-1042",
});
// Redirect the customer to session.checkout_url.
// Confirm with the webhook or cyzora.verifyTransaction(session.reference).
```

The customer returns with `?reference=...&status=success`, but that redirect is not
proof of payment. The same session can be opened with
`CyzoraCheckout.open({ reference })` from `https://checkout.cyzora.co.ke/inline.js`.

---

## 3. Handle the webhook (verify, parse, act)

The webhook is the source of truth for settlement. Always verify the signature against
the raw request body before trusting it, and reply with a 2xx status. Payloads are flat,
with fields such as `event`, `charge_id`, `reference`, `amount` and `metadata` at the
top level. There is no `data` wrapper.

### TypeScript / Node (Express)
```ts
import express from "express";
import { Cyzora, WebhookEvents } from "cyzora";

const app = express();
const cyzora = new Cyzora({ secretKey: process.env.CYZORA_SECRET_KEY!, webhookSecrets: process.env.CYZORA_WEBHOOK_SECRET });

// Mount a raw body parser so the signature matches the exact bytes.
app.post("/webhooks/cyzora", express.raw({ type: "application/json" }), (req, res) => {
  let event;
  try {
    event = cyzora.constructWebhookEvent(
      req.body,
      req.headers["x-cyzora-signature"] as string,
      req.headers["x-cyzora-event"] as string,
    );
  } catch {
    return res.status(400).send("invalid signature");
  }
  switch (event.event) {
    case WebhookEvents.ChargeSucceeded: markInvoicePaid(event); break;
    case WebhookEvents.ChargeFailed:    markInvoiceFailed(event); break;
    case WebhookEvents.PayoutPaid:      markWithdrawalPaid(event); break;
    case WebhookEvents.PayoutFailed:    markWithdrawalFailed(event); break;
    case WebhookEvents.WebhookTest:     break; // delivery test, no money moved
    default: /* log unknown events */ break;
  }
  res.json({ received: true });
});
```

### Python (Flask)
```python
from cyzora import Cyzora, WEBHOOK_EVENTS
client = Cyzora(SECRET_KEY, webhook_secret=os.environ["CYZORA_WEBHOOK_SECRET"])

@app.post("/webhooks/cyzora")
def webhook():
    raw = request.get_data()  # exact bytes
    try:
        event = client.construct_webhook_event(
            raw, request.headers.get("X-Cyzora-Signature"), request.headers.get("X-Cyzora-Event")
        )
    except CyzoraError:
        return "invalid signature", 400
    if event["event"] == WEBHOOK_EVENTS["charge_succeeded"]:
        mark_invoice_paid(event)
    return {"received": True}
```

### PHP
```php
$raw = file_get_contents('php://input');
try {
    $event = $cyzora->constructWebhookEvent(
        $raw, $_SERVER['HTTP_X_CYZORA_SIGNATURE'] ?? null, $_SERVER['HTTP_X_CYZORA_EVENT'] ?? null
    );
} catch (\Cyzora\CyzoraException $e) {
    http_response_code(400); exit('invalid signature');
}
if ($event['event'] === 'charge.succeeded') { markInvoicePaid($event); }
```

### Go
```go
func webhookHandler(c *cyzora.Client) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        raw, _ := io.ReadAll(r.Body)
        event, err := c.ConstructWebhookEvent(
            raw, r.Header.Get("X-Cyzora-Signature"), r.Header.Get("X-Cyzora-Event"),
        )
        if err != nil { http.Error(w, "invalid signature", 400); return }
        switch event["event"] {
        case cyzora.EventChargeSucceeded: markInvoicePaid(event)
        case cyzora.EventPayoutPaid:      markWithdrawalPaid(event)
        case cyzora.EventWebhookTest:     // delivery test
        }
        w.Write([]byte(`{"received":true}`))
    }
}
```

---

## 4. Payout (send money out)

```ts
await cyzora.createPayout({
  amountKes: 1500, wallet: "collections",
  destinationType: "mobile", phone: "0712345678",
  idempotencyKey: "WD-9001",
});
// Saved channel instead: { amountKes: 1500, channelId: "ch_1a2b3c4d5e6f7a8b", idempotencyKey: "WD-9001" }
// Bank: { destinationType: "bank", bankCode, accountNumber, accountName }
```

Amounts must be whole shillings and at least KES 30. The payout is final only when the
`payout.paid` webhook arrives; the create response usually says `processing`.

---

## 5. Balances, fees, references and exports

```ts
await cyzora.getWallets();                 // collections / collections_pending / payouts (cents)
await cyzora.listPayoutChannels();         // saved destinations
await cyzora.listPayoutBanks();            // bank codes for a payout form
await cyzora.payoutFeePreview(2500, "paybill", "collections");
await cyzora.listCharges({ status: "succeeded", fromDate: "2026-10-01" }); // array
await cyzora.listPayouts();                // plain array, newest first
await cyzora.previewWalletMove(5000);      // Collections -> Payouts, fee shown
await cyzora.moveToPayouts(5000, { idempotencyKey: "move-2026-11-27" });
await cyzora.exportCharges({ from: "2026-10-01", to: "2026-10-07", status: "succeeded" }); // CSV text
await cyzora.verifyStatement("K7PX3MZQ9H2D"); // public, no API key
```

---

## 6. Using a brand-new endpoint before the SDK wraps it

```ts
const result = await cyzora.request("GET", "/new-feature");
```
```python
client.request("GET", "/new-feature")
```
```php
$cyzora->rawRequest('GET', '/new-feature');
```
```go
client.Request(ctx, "GET", "/new-feature", nil, "", nil)
```
The path is appended to the base URL, which already includes `/api/v1`.
