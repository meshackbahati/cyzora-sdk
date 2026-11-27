# Changelog

## 1.2.0 (2026-11-27)

- Charge and checkout `metadata` values are now sent as strings. The API only accepts
  string values and refused the whole request (HTTP 400) for numbers, booleans or
  objects. Nulls are dropped, objects and arrays become JSON, and more than 20 keys is
  refused before the request is sent.
- The charge `description` option is deprecated and no longer sent, because the API
  does not store a description on STK charges. Existing code that passes it keeps
  working. Hosted checkout still sends `description`.
- **Go (breaking fix):** `ListPayouts` now returns `[]map[string]interface{}`. The API
  answers with a JSON array, so the previous map return type failed on every call.
- Added `listCharges` (`GET /charges`) with `status`, `from_date`, `to_date` and `mode`
  filters in all four clients.
- Added wallet moves: `previewWalletMove`, `moveToPayouts` and `listWalletTransfers`
  (Collections to Payouts), plus `listSubsidiaries`, `previewSubsidiaryTransfer`,
  `createSubsidiaryTransfer` and `listSubsidiaryTransfers` for parent businesses. They
  take exact cents and idempotency keys, and test keys are refused by the API.
- Added read-only `listPayoutBatches` and `getPayoutBatch`. Batches are still created
  in the dashboard.
- Added `RequestInto` in Go, an escape hatch that decodes into any value so endpoints
  returning arrays work.
- Fixed README examples that read `listPayouts().data[0]`; the response is a plain
  array. The docs now note that the API does not filter the payout list yet.
- PHP `request()` is now `protected` so tests can capture payloads; public behaviour is
  unchanged. Added tests for all of the above in every language.
- Direct (non-SDK) API integrations are unaffected: this release changes only what the
  SDKs send, matching what the API already expects.

## 1.1.0

- Aligned all four clients with the local API documentation: added payout listing,
  charge CSV export, public statement verification, hosted checkout in Go, and payout
  channels and fee preview in Go.
- Corrected the fee preview query to use `destination` and `wallet`, and added wallet
  and webhook secret fallbacks to `CYZORA_PAYOUT_WALLET`, `CYZORA_API_BASE` and
  `CYZORA_WEBHOOK_SECRET`.
- Changed charge and checkout amounts to round odd cents up to the next shilling, and
  added validation that rejects fractional payout amounts and incomplete destinations.
- Added the `webhook.test` event, optional `X-Cyzora-Event` consistency checking, and
  raw-byte signature verification in TypeScript. Retries now also cover a 409 returned
  for a request with an idempotency key.
- Updated the README and examples to reflect flat webhook payloads, test-mode phones and
  the complete API surface. The local API PDF remains untracked.
