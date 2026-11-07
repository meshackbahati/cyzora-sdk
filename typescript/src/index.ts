/**
 * Cyzora SDK for TypeScript / Node.js (Node 18+ with global fetch).
 *
 * Cyzora is an M-Pesa payment rail. Amounts on the wire are integer cents of
 * whole shillings (KES 2,500 -> 250000). Every money-moving POST should carry an
 * idempotency key so a retry cannot double-charge or double-pay.
 */

import { createHmac, timingSafeEqual } from 'crypto';

export type Network = 'mpesa' | 'airtel';
export type DestinationType = 'mobile' | 'till' | 'paybill' | 'bank';

export interface ChargeParams {
  /** Amount in Kenyan shillings (whole or fractional; rounded to whole shillings). */
  amountKes: number;
  /** Or supply exact cents instead of amountKes. */
  amountCents?: number;
  phone: string;
  network?: Network;
  accountRef?: string;
  description?: string;
  metadata?: Record<string, unknown>;
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
  metadata?: Record<string, unknown>;
  expiresIn?: number;
  idempotencyKey?: string;
}

export interface PayoutParams {
  amountKes: number;
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
}

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

/** Convert shillings to integer cents, rounding to whole shillings first. */
export function toCents(amountKes: number): number {
  const kes = Math.round(Number(amountKes));
  if (!Number.isFinite(kes) || kes < 1) throw new CyzoraError('Amount must be at least KES 1.');
  return kes * 100;
}

/** Convert integer cents back to shillings. */
export function fromCents(cents: number): number {
  return Math.round(Number(cents || 0)) / 100;
}

export class Cyzora {
  private readonly baseUrl: string;
  private readonly secretKey: string;
  private readonly webhookSecrets: string[];
  private readonly fetchImpl: typeof fetch;

  constructor(opts: CyzoraOptions) {
    if (!opts?.secretKey) throw new CyzoraError('secretKey is required');
    this.secretKey = opts.secretKey;
    this.baseUrl = (opts.baseUrl || 'https://api.cyzora.co.ke/api/v1').replace(/\/$/, '');
    const secrets = opts.webhookSecrets
      ? Array.isArray(opts.webhookSecrets) ? opts.webhookSecrets : [opts.webhookSecrets]
      : [];
    this.webhookSecrets = secrets.filter(Boolean);
    this.fetchImpl = opts.fetchImpl || fetch;
  }

  private async request<T = any>(path: string, init: { method?: string; body?: unknown; idempotencyKey?: string; query?: Record<string, string | number | undefined> } = {}): Promise<T> {
    const url = new URL(`${this.baseUrl}${path}`);
    if (init.query) {
      for (const [k, v] of Object.entries(init.query)) if (v !== undefined && v !== null && v !== '') url.searchParams.set(k, String(v));
    }
    const headers: Record<string, string> = { 'X-API-Key': this.secretKey, Accept: 'application/json' };
    if (init.body !== undefined) headers['Content-Type'] = 'application/json';
    if (init.idempotencyKey) headers['Idempotency-Key'] = String(init.idempotencyKey).slice(0, 128);

    let res: Response;
    try {
      res = await this.fetchImpl(url, {
        method: init.method || (init.body !== undefined ? 'POST' : 'GET'),
        headers,
        body: init.body === undefined ? undefined : JSON.stringify(init.body),
      });
    } catch (e: any) {
      throw new CyzoraError(`Could not reach Cyzora: ${e?.message || e}`);
    }

    const text = await res.text();
    let data: any = {};
    if (text) { try { data = JSON.parse(text); } catch { data = { raw: text }; } }
    if (!res.ok) throw new CyzoraError(data?.error || data?.message || `Cyzora request failed (${res.status})`, res.status, data);
    return data as T;
  }

  /** Send an M-Pesa STK prompt to a customer's phone. */
  async createCharge(p: ChargeParams) {
    const phone = normalizePhone(p.phone);
    if (!phone) throw new CyzoraError('Enter a valid Safaricom phone number (e.g. 0712 345 678).');
    const cents = p.amountCents !== undefined ? Number(p.amountCents) : toCents(p.amountKes);
    if (!Number.isInteger(cents) || cents < 100) throw new CyzoraError('Amount must be a whole number of shillings, at least KES 1.');
    return this.request('/charges', {
      method: 'POST',
      idempotencyKey: p.idempotencyKey,
      body: {
        amount: cents,
        phone,
        network: p.network || 'mpesa',
        ...(p.accountRef ? { account_ref: String(p.accountRef).slice(0, 64) } : {}),
        ...(p.description ? { description: String(p.description).slice(0, 200) } : {}),
        ...(p.metadata ? { metadata: p.metadata } : {}),
      },
    });
  }

  getCharge(chargeId: string) {
    return this.request(`/charges/${encodeURIComponent(chargeId)}`);
  }

  /** Hosted checkout session (redirect flow) instead of an STK push. */
  async createCheckoutSession(p: CheckoutParams) {
    return this.request('/transactions/initialize', {
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
        ...(p.metadata ? { metadata: p.metadata } : {}),
        ...(p.expiresIn ? { expires_in: Number(p.expiresIn) } : {}),
      },
    });
  }

  verifyTransaction(reference: string) {
    return this.request(`/transactions/verify/${encodeURIComponent(reference)}`);
  }

  /** Send money out of the platform balance to a mobile/till/paybill/bank destination. */
  async createPayout(p: PayoutParams) {
    const cents = toCents(p.amountKes);
    if (cents < PAYOUT_MIN_CENTS) throw new CyzoraError('The smallest payout is KES 30.');
    const body: Record<string, unknown> = { amount: cents };
    if (p.wallet) body.wallet = p.wallet;
    if (p.channelId) body.channel_id = p.channelId;
    else {
      const type = p.destinationType || 'mobile';
      body.destination_type = type;
      if (type === 'mobile') body.phone = normalizePhone(p.phone || '') || p.phone;
      else if (type === 'till') body.short_code = p.shortCode;
      else if (type === 'paybill') { body.short_code = p.shortCode; body.account_ref = p.accountRef; }
      else if (type === 'bank') { body.bank_code = p.bankCode; body.account_number = p.accountNumber; body.account_name = p.accountName; }
    }
    return this.request('/payouts', { method: 'POST', idempotencyKey: p.idempotencyKey, body });
  }

  getPayout(payoutId: string) {
    return this.request(`/payouts/${encodeURIComponent(payoutId)}`);
  }

  /** Balances (in cents) for collections, collections_pending and payouts wallets. */
  getWallets() {
    return this.request('/wallets');
  }

  listPayoutChannels() {
    return this.request('/payout-channels');
  }

  listPayoutBanks() {
    return this.request('/payout-banks');
  }

  payoutFeePreview(amountKes: number, destinationType: DestinationType = 'mobile') {
    return this.request('/payouts/fee-preview', { query: { amount: toCents(amountKes), destination_type: destinationType } });
  }

  /**
   * Verify a webhook signature. `rawBody` must be the exact bytes received.
   * `signature` is the X-Cyzora-Signature header (hex HMAC-SHA256).
   * Returns true if it matches any configured webhook secret.
   */
  verifyWebhookSignature(rawBody: string | Buffer, signature: string | null | undefined): boolean {
    if (!this.webhookSecrets.length || !signature || !rawBody) return false;
    const body = typeof rawBody === 'string' ? rawBody : rawBody.toString('utf8');
    const sig = String(signature).trim();
    return this.webhookSecrets.some((secret) => {
      try {
        const expected = createHmac('sha256', secret).update(body).digest('hex');
        const a = Buffer.from(expected, 'utf8');
        const b = Buffer.from(sig, 'utf8');
        return a.length === b.length && timingSafeEqual(a, b);
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
  constructWebhookEvent(rawBody: string | Buffer, signature: string | null | undefined): any {
    if (!this.verifyWebhookSignature(rawBody, signature)) {
      throw new CyzoraError('Webhook signature verification failed', 401);
    }
    const body = typeof rawBody === 'string' ? rawBody : rawBody.toString('utf8');
    try {
      return JSON.parse(body);
    } catch {
      throw new CyzoraError('Webhook body is not valid JSON', 400);
    }
  }
}

export default Cyzora;
