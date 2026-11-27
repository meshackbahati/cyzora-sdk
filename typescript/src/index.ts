/**
 * Cyzora SDK for TypeScript / Node.js (Node 18+ with global fetch).
 *
 * Cyzora is an M-Pesa payment rail. Amounts on the wire are integer cents of
 * whole shillings (KES 2,500 -> 250000). Every money-moving POST should carry an
 * idempotency key so a retry cannot double-charge or double-pay.
 */

import { createHmac, timingSafeEqual } from 'crypto';

export type Network = 'mpesa' | 'airtel' | 'tkash';
export type DestinationType = 'mobile' | 'till' | 'paybill' | 'bank';

export interface ChargeParams {
  /** Amount in Kenyan shillings (whole or fractional; rounded to whole shillings). */
  amountKes: number;
  /** Or supply exact cents instead of amountKes. */
  amountCents?: number;
  phone: string;
  network?: Network;
  accountRef?: string;
  /** @deprecated The rail does not store a description on STK charges; it is no longer sent. */
  description?: string;
  /** Up to 20 keys. Values are sent as strings (numbers and booleans are converted). */
  metadata?: Record<string, unknown>;
  idempotencyKey?: string;
}

export interface ListChargesParams {
  /** succeeded | pending | failed | timeout | expired | awaiting_payment */
  status?: string;
  /** First day to include, e.g. 2026-10-01. */
  fromDate?: string;
  /** Last day to include, e.g. 2026-10-31. */
  toDate?: string;
  /** live (default) | test | all. A test key only ever sees test charges. */
  mode?: 'live' | 'test' | 'all';
}

export interface SubsidiaryTransferParams {
  subsidiaryId: string;
  amountKes: number;
  /** Or supply exact cents instead of amountKes. */
  amountCents?: number;
  /** The parent's wallet the money leaves: collections | payouts. */
  fromWallet?: 'collections' | 'payouts';
  idempotencyKey?: string;
}

export interface CheckoutParams {
  amountKes: number;
  accountRef?: string;
  description?: string;
  callbackUrl?: string;
  cancelUrl?: string;
  customerPhone?: string;
  customerEmail?: string;
  /** Up to 20 keys. Values are sent as strings (numbers and booleans are converted). */
  metadata?: Record<string, unknown>;
  expiresIn?: number;
  idempotencyKey?: string;
}

export interface PayoutParams {
  amountKes: number;
  /** Or supply exact cents instead of amountKes; must be a multiple of 100. */
  amountCents?: number;
  wallet?: string;
  channelId?: string;
  destinationType?: DestinationType;
  phone?: string;
  shortCode?: string;
  accountRef?: string;
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  idempotencyKey?: string;
}

export interface CyzoraOptions {
  /** Secret key, sk_live_... (required). */
  secretKey: string;
  /** API base. Defaults to https://api.cyzora.co.ke/api/v1 */
  baseUrl?: string;
  /** Webhook signing secret(s), whsec_... — for verifyWebhookSignature(). */
  webhookSecrets?: string | string[];
  /** Fetch implementation override (for tests / older runtimes). */
  fetchImpl?: typeof fetch;
  /** Max automatic retries on network/5xx/429 errors (default 2). Retries only happen for GETs or when an idempotency key is set. */
  maxRetries?: number;
}

/** Known webhook event names. The rail evolves, so compare against `event` strings directly too. */
export const WebhookEvents = {
  ChargeSucceeded: 'charge.succeeded',
  ChargeFailed: 'charge.failed',
  ChargeExpired: 'charge.expired',
  PayoutPaid: 'payout.paid',
  PayoutFailed: 'payout.failed',
  WebhookTest: 'webhook.test',
} as const;
export type WebhookEvent = typeof WebhookEvents[keyof typeof WebhookEvents];

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Error thrown for any non-2xx response or transport failure. */
export class CyzoraError extends Error {
  status?: number;
  body?: unknown;
  constructor(message: string, status?: number, body?: unknown) {
    super(message);
    this.name = 'CyzoraError';
    this.status = status;
    this.body = body;
  }
}

/** Smallest payout the rail accepts, in cents (KES 30). */
export const PAYOUT_MIN_CENTS = 3000;

/** Normalize a Kenyan phone number to the 254XXXXXXXXX form the rail expects. */
export function normalizePhone(phone: string): string | null {
  if (!phone) return null;
  let p = String(phone).replace(/[\s\-+]/g, '');
  if (p.startsWith('0')) p = '254' + p.slice(1);
  else if (p.startsWith('7') || p.startsWith('1')) p = '254' + p;
  return /^254[17]\d{8}$/.test(p) ? p : null;
}

/** Convert shillings to cents, rounding up to the next whole shilling as the rail does for charges. */
export function toCents(amountKes: number): number {
  const value = Number(amountKes);
  if (!Number.isFinite(value)) throw new CyzoraError('Amount must be a number.');
  const kes = Math.ceil(value);
  if (kes < 1) throw new CyzoraError('Amount must be at least KES 1.');
  return kes * 100;
}

/** Convert integer cents back to shillings. */
export function fromCents(cents: number): number {
  return Math.round(Number(cents || 0)) / 100;
}

/**
 * Payout amounts are refused by the rail unless they are a multiple of 100 cents,
 * so the SDK rejects fractional shillings instead of silently rounding them.
 */
function payoutCents(amountKes: number, amountCents?: number): number {
  const cents = amountCents !== undefined ? Number(amountCents) : Number(amountKes) * 100;
  if (!Number.isFinite(cents) || !Number.isInteger(cents) || cents % 100 !== 0) {
    throw new CyzoraError('Payout amounts must be whole shillings (a multiple of 100 cents).');
  }
  if (cents < 1) throw new CyzoraError('Amount must be at least KES 1.');
  return cents;
}

/** Most metadata keys the rail accepts on a charge or checkout session. */
export const METADATA_MAX_KEYS = 20;

/**
 * The rail stores metadata as string key/value pairs and refuses the whole
 * request (HTTP 400) if any value is a number, boolean or object. Convert
 * values to strings here so `{ orderId: 1001 }` works as expected.
 */
export function normalizeMetadata(metadata?: Record<string, unknown> | null): Record<string, string> | undefined {
  if (!metadata) return undefined;
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(metadata)) {
    if (v === undefined || v === null) continue;
    out[k] = typeof v === 'string' ? v : typeof v === 'object' ? JSON.stringify(v) : String(v);
  }
  const n = Object.keys(out).length;
  if (n > METADATA_MAX_KEYS) throw new CyzoraError(`metadata can hold at most ${METADATA_MAX_KEYS} keys (got ${n}).`);
  return n ? out : undefined;
}

/** Exact cents for wallet moves: whole cents, greater than zero. */
function moveCents(amountKes: number, amountCents?: number): number {
  const cents = amountCents !== undefined ? Number(amountCents) : Math.round(Number(amountKes) * 100);
  if (!Number.isFinite(cents) || !Number.isInteger(cents) || cents <= 0) {
    throw new CyzoraError('Amount must be greater than zero.');
  }
  return cents;
}

const env = (): Record<string, string | undefined> =>
  typeof process !== 'undefined' && process.env ? process.env : {};

interface RequestInitOptions {
  method?: string;
  body?: unknown;
  idempotencyKey?: string;
  query?: Record<string, string | number | undefined>;
  /** Public endpoints such as statement verification need no secret key. */
  auth?: boolean;
  /** Return the exact response text (for CSV exports) instead of parsed JSON. */
  raw?: boolean;
}

export class Cyzora {
  private readonly baseUrl: string;
  private readonly secretKey: string;
  private readonly webhookSecrets: string[];
  private readonly fetchImpl: typeof fetch;
  private readonly maxRetries: number;

  constructor(opts: CyzoraOptions) {
    if (!opts?.secretKey) throw new CyzoraError('secretKey is required');
    this.secretKey = opts.secretKey;
    this.baseUrl = (opts.baseUrl || env().CYZORA_API_BASE || 'https://api.cyzora.co.ke/api/v1').replace(/\/$/, '');
    const configured = opts.webhookSecrets ?? env().CYZORA_WEBHOOK_SECRET;
    const secrets = configured
      ? Array.isArray(configured) ? configured : String(configured).split(',')
      : [];
    this.webhookSecrets = secrets.map((s) => String(s).trim()).filter(Boolean);
    this.fetchImpl = opts.fetchImpl || fetch;
    this.maxRetries = opts.maxRetries ?? 2;
  }

  /**
   * Escape hatch for any endpoint — including new ones the rail adds before this
   * SDK ships a typed wrapper. `path` is appended to the base URL.
   *
   *   cyzora.request('POST', '/charges', { body: {...}, idempotencyKey: 'k' })
   *   cyzora.request('GET', '/some-new-endpoint')
   */
  request<T = any>(method: string, path: string, init: Omit<RequestInitOptions, 'method'> = {}): Promise<T> {
    return this._request<T>(path, { ...init, method });
  }

  private async _request<T = any>(path: string, init: RequestInitOptions = {}): Promise<T> {
    const url = new URL(`${this.baseUrl}${path}`);
    if (init.query) {
      for (const [k, v] of Object.entries(init.query)) if (v !== undefined && v !== null && v !== '') url.searchParams.set(k, String(v));
    }
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (init.auth !== false) headers['X-API-Key'] = this.secretKey;
    if (init.body !== undefined) headers['Content-Type'] = 'application/json';
    if (init.idempotencyKey) headers['Idempotency-Key'] = String(init.idempotencyKey).slice(0, 128);
    const method = init.method || (init.body !== undefined ? 'POST' : 'GET');
    // Retrying is only safe when the rail can dedupe: GETs, or POSTs carrying an idempotency key.
    const canRetry = method.toUpperCase() === 'GET' || !!init.idempotencyKey;
    const bodyStr = init.body === undefined ? undefined : JSON.stringify(init.body);

    let attempt = 0;
    for (;;) {
      let res: Response;
      try {
        res = await this.fetchImpl(url, { method, headers, body: bodyStr });
      } catch (e: any) {
        if (canRetry && attempt < this.maxRetries) { await sleep(2 ** attempt * 250); attempt++; continue; }
        throw new CyzoraError(`Could not reach Cyzora: ${e?.message || e}`);
      }
      const text = await res.text();
      let data: any = {};
      if (text) { try { data = JSON.parse(text); } catch { data = { raw: text }; } }
      if (!res.ok) {
        // A 409 with an idempotency key means the earlier request is still being
        // processed; the docs advise waiting and retrying with the same key.
        const transient = res.status >= 500 || res.status === 429 || (res.status === 409 && !!init.idempotencyKey);
        if (transient && canRetry && attempt < this.maxRetries) { await sleep(2 ** attempt * 250); attempt++; continue; }
        throw new CyzoraError(data?.error || data?.message || `Cyzora request failed (${res.status})`, res.status, data);
      }
      if (init.raw) return text as T;
      return data as T;
    }
  }

  /** Send an M-Pesa STK prompt to a customer's phone. */
  async createCharge(p: ChargeParams) {
    const phone = normalizePhone(p.phone);
    if (!phone) throw new CyzoraError('Enter a valid M-Pesa phone number (e.g. 0712 345 678).');
    const cents = p.amountCents !== undefined
      ? Math.ceil(Number(p.amountCents) / 100) * 100
      : toCents(p.amountKes);
    if (!Number.isFinite(cents) || !Number.isInteger(cents) || cents < 100) throw new CyzoraError('Amount must be a whole number of shillings, at least KES 1.');
    const metadata = normalizeMetadata(p.metadata);
    return this._request('/charges', {
      method: 'POST',
      idempotencyKey: p.idempotencyKey,
      body: {
        amount: cents,
        phone,
        network: p.network || 'mpesa',
        ...(p.accountRef ? { account_ref: String(p.accountRef).slice(0, 64) } : {}),
        ...(metadata ? { metadata } : {}),
      },
    });
  }

  getCharge(chargeId: string) {
    return this._request(`/charges/${encodeURIComponent(chargeId)}`);
  }

  /** List charges, newest first. Returns an array. */
  listCharges(p: ListChargesParams = {}) {
    return this._request<any[]>('/charges', {
      query: { status: p.status, from_date: p.fromDate, to_date: p.toDate, mode: p.mode },
    });
  }

  /** Download charges as CSV; from/to are dates such as 2026-10-01. */
  exportCharges(query: { from?: string; to?: string; status?: string } = {}) {
    return this._request<string>('/charges/export', { query, raw: true });
  }

  /** Hosted checkout session (redirect flow) instead of an STK push. */
  async createCheckoutSession(p: CheckoutParams) {
    const metadata = normalizeMetadata(p.metadata);
    return this._request('/transactions/initialize', {
      method: 'POST',
      idempotencyKey: p.idempotencyKey,
      body: {
        amount: toCents(p.amountKes),
        ...(p.accountRef ? { account_ref: String(p.accountRef).slice(0, 64) } : {}),
        ...(p.description ? { description: String(p.description).slice(0, 200) } : {}),
        ...(p.callbackUrl ? { callback_url: p.callbackUrl } : {}),
        ...(p.cancelUrl ? { cancel_url: p.cancelUrl } : {}),
        ...(p.customerPhone ? { customer_phone: normalizePhone(p.customerPhone) || p.customerPhone } : {}),
        ...(p.customerEmail ? { customer_email: p.customerEmail } : {}),
        ...(metadata ? { metadata } : {}),
        ...(p.expiresIn ? { expires_in: Number(p.expiresIn) } : {}),
      },
    });
  }

  verifyTransaction(reference: string) {
    return this._request(`/transactions/verify/${encodeURIComponent(reference)}`);
  }

  /** Send money out of the platform balance to a mobile/till/paybill/bank destination. */
  async createPayout(p: PayoutParams) {
    const cents = payoutCents(p.amountKes, p.amountCents);
    if (cents < PAYOUT_MIN_CENTS) throw new CyzoraError('The smallest payout is KES 30.');
    const body: Record<string, unknown> = { amount: cents };
    const wallet = p.wallet || env().CYZORA_PAYOUT_WALLET;
    if (wallet) body.wallet = wallet;
    if (p.channelId) body.channel_id = p.channelId;
    else {
      const type = p.destinationType || 'mobile';
      body.destination_type = type;
      const require = (field: string, value: unknown) => {
        if (value === undefined || value === null || String(value) === '') throw new CyzoraError(`${field} is required for ${type} payouts.`);
      };
      if (type === 'mobile') {
        const phone = normalizePhone(p.phone || '');
        if (!phone) throw new CyzoraError('phone must be a valid M-Pesa number for mobile payouts.');
        body.phone = phone;
      } else if (type === 'till') {
        require('shortCode', p.shortCode);
        body.short_code = p.shortCode;
      } else if (type === 'paybill') {
        require('shortCode', p.shortCode);
        require('accountRef', p.accountRef);
        body.short_code = p.shortCode;
        body.account_ref = p.accountRef;
      } else if (type === 'bank') {
        require('bankCode', p.bankCode);
        require('accountNumber', p.accountNumber);
        require('accountName', p.accountName);
        body.bank_code = p.bankCode;
        body.account_number = p.accountNumber;
        body.account_name = p.accountName;
      } else {
        throw new CyzoraError('destinationType must be mobile, till, paybill or bank.');
      }
    }
    return this._request('/payouts', { method: 'POST', idempotencyKey: p.idempotencyKey, body });
  }

  getPayout(payoutId: string) {
    return this._request(`/payouts/${encodeURIComponent(payoutId)}`);
  }

  /**
   * List payouts, newest first. Returns a plain array (not `{ data: [...] }`).
   * The rail does not filter this list yet; any query you pass is forwarded
   * as-is for forward compatibility.
   */
  listPayouts(query: Record<string, string | number | undefined> = {}) {
    return this._request<any[]>('/payouts', { query });
  }

  /** Bulk payout batches (created in the dashboard). Read-only by API key. */
  listPayoutBatches() {
    return this._request('/payouts/batches');
  }

  getPayoutBatch(batchId: string) {
    return this._request(`/payouts/batches/${encodeURIComponent(batchId)}`);
  }

  /** Balances (in cents) for collections, collections_pending and payouts wallets. */
  getWallets() {
    return this._request('/wallets');
  }

  /** Preview moving money from Collections into the Payouts wallet (fee and resulting balances). */
  async previewWalletMove(amountKes: number, amountCents?: number) {
    return this._request('/wallets/move/preview', { query: { amount: moveCents(amountKes, amountCents) } });
  }

  /** Move money from Collections into the Payouts wallet. Live keys only. */
  async moveToPayouts(amountKes: number, opts: { amountCents?: number; idempotencyKey?: string } = {}) {
    return this._request('/wallets/move', {
      method: 'POST',
      idempotencyKey: opts.idempotencyKey,
      body: { amount: moveCents(amountKes, opts.amountCents) },
    });
  }

  /** Past Collections -> Payouts moves. Returns `{ data: [...] }`. */
  listWalletTransfers(limit?: number) {
    return this._request('/wallets/transfers', { query: { limit } });
  }

  /** Subsidiary businesses this account can top up. Returns `{ data: [...] }`. */
  listSubsidiaries() {
    return this._request('/wallets/subsidiaries');
  }

  /** Preview topping up a subsidiary's Payouts wallet. Live keys only. */
  async previewSubsidiaryTransfer(p: Omit<SubsidiaryTransferParams, 'idempotencyKey'>) {
    if (!p?.subsidiaryId) throw new CyzoraError('subsidiaryId is required.');
    return this._request('/wallets/subsidiary-transfers/preview', {
      query: { subsidiary_id: p.subsidiaryId, from_wallet: p.fromWallet, amount: moveCents(p.amountKes, p.amountCents) },
    });
  }

  /** Top up a subsidiary's Payouts wallet from this account. Live keys only. */
  async createSubsidiaryTransfer(p: SubsidiaryTransferParams) {
    if (!p?.subsidiaryId) throw new CyzoraError('subsidiaryId is required.');
    return this._request('/wallets/subsidiary-transfers', {
      method: 'POST',
      idempotencyKey: p.idempotencyKey,
      body: {
        subsidiary_id: p.subsidiaryId,
        amount: moveCents(p.amountKes, p.amountCents),
        ...(p.fromWallet ? { from_wallet: p.fromWallet } : {}),
      },
    });
  }

  /** Past subsidiary top-ups. Returns `{ data: [...] }`. */
  listSubsidiaryTransfers(limit?: number) {
    return this._request('/wallets/subsidiary-transfers', { query: { limit } });
  }

  listPayoutChannels() {
    return this._request('/payout-channels');
  }

  listPayoutBanks() {
    return this._request('/payout-banks');
  }

  /**
   * Preview the provider fee. The rail expects `destination` (not `destination_type`)
   * and the same wallet the payout will use, otherwise the preview can differ.
   */
  payoutFeePreview(amountKes: number, destinationType: DestinationType = 'mobile', wallet?: string) {
    return this._request('/payouts/fee-preview', { query: { amount: payoutCents(amountKes), destination: destinationType, wallet } });
  }

  /** Verify a statement code without a secret key; the code is 12 characters. */
  verifyStatement(code: string) {
    return this._request(`/public/statements/verify/${encodeURIComponent(code)}`, { auth: false });
  }

  /**
   * Verify a webhook signature. `rawBody` must be the exact bytes received.
   * `signature` is the X-Cyzora-Signature header (hex HMAC-SHA256).
   * Returns true if it matches any configured webhook secret.
   */
  verifyWebhookSignature(rawBody: string | Buffer, signature: string | null | undefined): boolean {
    if (!this.webhookSecrets.length || !signature || !rawBody) return false;
    // HMAC must cover the exact bytes received; decoding and re-encoding a buffer
    // would change the signature for payloads that are not valid UTF-8.
    const body = typeof rawBody === 'string' ? Buffer.from(rawBody, 'utf8') : Buffer.from(rawBody);
    const sig = Buffer.from(String(signature).trim(), 'utf8');
    return this.webhookSecrets.some((secret) => {
      try {
        const expected = Buffer.from(createHmac('sha256', secret).update(body).digest('hex'), 'utf8');
        return expected.length === sig.length && timingSafeEqual(expected, sig);
      } catch {
        return false;
      }
    });
  }

  /**
   * Verify the signature (against every configured secret) and parse the body in
   * one call. Throws CyzoraError if the signature is invalid or the body is not
   * JSON. `rawBody` must be the exact bytes received.
   */
  constructWebhookEvent(rawBody: string | Buffer, signature: string | null | undefined, eventHeader?: string | null): any {
    if (!this.verifyWebhookSignature(rawBody, signature)) {
      throw new CyzoraError('Webhook signature verification failed', 401);
    }
    const body = typeof rawBody === 'string' ? rawBody : rawBody.toString('utf8');
    let event: any;
    try {
      event = JSON.parse(body);
    } catch {
      throw new CyzoraError('Webhook body is not valid JSON', 400);
    }
    // The rail also sends X-Cyzora-Event. When supplied, require it to agree with
    // the body so a mismatched header cannot be used to misroute an event.
    if (eventHeader && event?.event !== eventHeader) {
      throw new CyzoraError('Webhook event header does not match the body', 400);
    }
    return event;
  }
}

export default Cyzora;
