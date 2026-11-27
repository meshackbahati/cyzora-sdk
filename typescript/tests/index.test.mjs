import assert from 'node:assert/strict';
import { createHmac } from 'node:crypto';
import test from 'node:test';
import { Cyzora, CyzoraError, WebhookEvents, toCents } from '../dist/index.js';

const clientWith = (body, capture) => new Cyzora({
  secretKey: 'sk_live_x',
  webhookSecrets: ['whsec_a'],
  fetchImpl: async (url, init) => {
    capture.url = String(url);
    capture.init = init;
    return { ok: true, status: 200, text: async () => body };
  },
});

test('amounts round up to whole shillings and test events are known', () => {
  assert.equal(toCents(2500.4), 250100);
  assert.equal(WebhookEvents.WebhookTest, 'webhook.test');
});

test('charge payload, phone normalization and idempotency key', async () => {
  const capture = {};
  const client = clientWith('{"id":"chg_1"}', capture);
  await client.createCharge({
    amountKes: 2500.4, phone: '0712345678', network: 'tkash', idempotencyKey: 'INV-1',
  });
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/charges');
  assert.deepEqual(JSON.parse(capture.init.body), {
    amount: 250100, phone: '254712345678', network: 'tkash',
  });
  assert.equal(capture.init.headers['Idempotency-Key'], 'INV-1');
});

test('payouts reject fractional shillings and incomplete destinations', async () => {
  const client = clientWith('{}', {});
  await assert.rejects(() => client.createPayout({ amountKes: 1500.5, phone: '0712345678' }), CyzoraError);
  await assert.rejects(() => client.createPayout({
    amountKes: 1500, destinationType: 'paybill', shortCode: '100000',
  }), CyzoraError);
});

test('fee preview uses destination and wallet query parameters', async () => {
  const capture = {};
  const client = clientWith('{}', capture);
  await client.payoutFeePreview(2500, 'paybill', 'collections');
  assert.equal(
    capture.url,
    'https://api.cyzora.co.ke/api/v1/payouts/fee-preview?amount=250000&destination=paybill&wallet=collections',
  );
});

test('statement verification is public and exports return raw CSV', async () => {
  const capture = {};
  const client = clientWith('{"valid":true}', capture);
  await client.verifyStatement('K7PX3MZQ9H2D');
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/public/statements/verify/K7PX3MZQ9H2D');
  assert.equal(capture.init.headers['X-API-Key'], undefined);

  const csvClient = clientWith('date,amount\n2026-10-01,2500.00\n', {});
  assert.equal(await csvClient.exportCharges({ status: 'succeeded' }), 'date,amount\n2026-10-01,2500.00\n');
});

test('signature checks the exact bytes and event headers must agree', () => {
  const client = clientWith('{}', {});
  const raw = Buffer.from([0xff, 0xfe, 0x00, 0x7b]);
  const sig = createHmac('sha256', 'whsec_a').update(raw).digest('hex');
  assert.equal(client.verifyWebhookSignature(raw, sig), true);
  assert.equal(client.verifyWebhookSignature(raw, sig.toUpperCase()), false);

  const body = Buffer.from('{"event":"charge.succeeded"}');
  const bodySig = createHmac('sha256', 'whsec_a').update(body).digest('hex');
  assert.equal(client.constructWebhookEvent(body, bodySig, 'charge.succeeded').event, 'charge.succeeded');
  assert.throws(() => client.constructWebhookEvent(body, bodySig, 'payout.paid'), CyzoraError);
});

test('charge metadata values become strings, description is not sent, 20-key cap', async () => {
  const capture = {};
  const client = clientWith('{"id":"chg_1"}', capture);
  await client.createCharge({
    amountKes: 100, phone: '0712345678', description: 'ignored',
    metadata: { orderId: 1001, paid: false, note: 'x', skip: null, cart: { a: 1 } },
  });
  const body = JSON.parse(capture.init.body);
  assert.equal(body.description, undefined);
  assert.deepEqual(body.metadata, { orderId: '1001', paid: 'false', note: 'x', cart: '{"a":1}' });

  const tooMany = Object.fromEntries(Array.from({ length: 21 }, (_, i) => [`k${i}`, i]));
  await assert.rejects(() => client.createCharge({ amountKes: 100, phone: '0712345678', metadata: tooMany }), CyzoraError);
});

test('listCharges sends the rail query names', async () => {
  const capture = {};
  const client = clientWith('[]', capture);
  const list = await client.listCharges({ status: 'succeeded', fromDate: '2026-10-01', toDate: '2026-10-31', mode: 'all' });
  assert.deepEqual(list, []);
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/charges?status=succeeded&from_date=2026-10-01&to_date=2026-10-31&mode=all');
});

test('wallet move and subsidiary transfers use exact cents and idempotency keys', async () => {
  const capture = {};
  const client = clientWith('{}', capture);
  await client.previewWalletMove(1500.5);
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/wallets/move/preview?amount=150050');

  await client.moveToPayouts(2000, { idempotencyKey: 'MV-1' });
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/wallets/move');
  assert.deepEqual(JSON.parse(capture.init.body), { amount: 200000 });
  assert.equal(capture.init.headers['Idempotency-Key'], 'MV-1');
  await assert.rejects(() => client.moveToPayouts(0), CyzoraError);

  await client.createSubsidiaryTransfer({ subsidiaryId: 'sub_1', amountKes: 500, fromWallet: 'payouts', idempotencyKey: 'ST-1' });
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/wallets/subsidiary-transfers');
  assert.deepEqual(JSON.parse(capture.init.body), { subsidiary_id: 'sub_1', amount: 50000, from_wallet: 'payouts' });
  await assert.rejects(() => client.createSubsidiaryTransfer({ amountKes: 500 }), CyzoraError);

  await client.previewSubsidiaryTransfer({ subsidiaryId: 'sub_1', amountKes: 500, fromWallet: 'collections' });
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/wallets/subsidiary-transfers/preview?subsidiary_id=sub_1&from_wallet=collections&amount=50000');

  await client.getPayoutBatch('b 1');
  assert.equal(capture.url, 'https://api.cyzora.co.ke/api/v1/payouts/batches/b%201');
});
