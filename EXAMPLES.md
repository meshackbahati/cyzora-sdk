# Cyzora SDK — Examples

Runnable patterns for the most common flows in each language. Replace `sk_live_...` and
`whsec_...` with your own keys (keep them server-side, never in a browser or mobile client).

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
  idempotencyKey: "INV-1001",      // stable per logical charge — makes retries safe
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

---

## 2. Handle the webhook (verify → parse → act)

The webhook is the **source of truth** for settlement. Always verify the signature against the
raw request body before trusting it.

### TypeScript / Node (Express)
```ts
import express from "express";
import { Cyzora, WebhookEvents } from "cyzora";

const app = express();
const cyzora = new Cyzora({ secretKey: process.env.CYZORA_SECRET_KEY!, webhookSecrets: process.env.CYZORA_WEBHOOK_SECRET });

// IMPORTANT: use a raw body parser so the signature matches the exact bytes.
app.post("/webhooks/cyzora", express.raw({ type: "application/json" }), (req, res) => {
  let event;
  try {
    event = cyzora.constructWebhookEvent(req.body, req.headers["x-cyzora-signature"] as string);
  } catch {
    return res.status(400).send("invalid signature");
  }
  switch (event.event) {
    case WebhookEvents.ChargeSucceeded: markInvoicePaid(event.data); break;
    case WebhookEvents.ChargeFailed:    markInvoiceFailed(event.data); break;
    case WebhookEvents.PayoutPaid:      markWithdrawalPaid(event.data); break;
    case WebhookEvents.PayoutFailed:    markWithdrawalFailed(event.data); break;
    default: /* log unknown events — the rail keeps adding more */ break;
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
        event = client.construct_webhook_event(raw, request.headers.get("X-Cyzora-Signature"))
    except CyzoraError:
        return "invalid signature", 400
    if event["event"] == WEBHOOK_EVENTS["charge_succeeded"]:
        mark_invoice_paid(event["data"])
    return {"received": True}
```

### PHP
```php
$raw = file_get_contents('php://input');
try {
    $event = $cyzora->constructWebhookEvent($raw, $_SERVER['HTTP_X_CYZORA_SIGNATURE'] ?? null);
} catch (\Cyzora\CyzoraException $e) {
    http_response_code(400); exit('invalid signature');
}
if ($event['event'] === 'charge.succeeded') { markInvoicePaid($event['data']); }
```

### Go
```go
func webhookHandler(c *cyzora.Client) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        raw, _ := io.ReadAll(r.Body)
        event, err := c.ConstructWebhookEvent(raw, r.Header.Get("X-Cyzora-Signature"))
        if err != nil { http.Error(w, "invalid signature", 400); return }
        switch event["event"] {
        case cyzora.EventChargeSucceeded: markInvoicePaid(event["data"])
        case cyzora.EventPayoutPaid:      markWithdrawalPaid(event["data"])
        }
        w.Write([]byte(`{"received":true}`))
    }
}
```

---

## 3. Payout (send money out)

```ts
await cyzora.createPayout({
  amountKes: 1500, wallet: "collections",
  destinationType: "mobile", phone: "0712345678",
  idempotencyKey: "WD-9001",
});
```
Bank: `{ destinationType: "bank", bankCode, accountNumber, accountName }`.
The payout is final only when the `payout.paid` webhook arrives.

---

## 4. Balances and references

```ts
await cyzora.getWallets();            // collections / collections_pending / payouts (cents)
await cyzora.listPayoutBanks();       // bank codes for a payout form
await cyzora.payoutFeePreview(1500);  // provider cut for a payout
```

---

## 5. Using a brand-new endpoint before the SDK wraps it

```ts
const result = await cyzora.request("GET", "/v1/new-feature");
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
