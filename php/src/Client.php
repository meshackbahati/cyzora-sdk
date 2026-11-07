<?php
declare(strict_types=1);

namespace Cyzora;

/**
 * Cyzora SDK for PHP 7.4+ (uses cURL; no external dependencies).
 *
 * Cyzora is an M-Pesa payment rail. Amounts on the wire are integer cents of
 * whole shillings (KES 2,500 -> 250000). Every money-moving POST should carry an
 * idempotency key so a retry cannot double-charge or double-pay.
 *
 *   $cyzora = new \Cyzora\Client('sk_live_...', ['webhookSecret' => 'whsec_...']);
 *   $charge = $cyzora->createCharge(100, '0712345678', ['accountRef' => 'INV-1', 'idempotencyKey' => 'INV-1']);
 */

class CyzoraException extends \RuntimeException
{
    public ?int $status = null;
    public $body = null;

    public function __construct(string $message, ?int $status = null, $body = null)
    {
        parent::__construct($message);
        $this->status = $status;
        $this->body = $body;
    }
}

class Client
{
    public const DEFAULT_BASE_URL = 'https://api.cyzora.co.ke/api/v1';
    public const PAYOUT_MIN_CENTS = 3000; // KES 30

    private string $secretKey;
    private string $baseUrl;
    /** @var string[] */
    private array $webhookSecrets;
    private int $timeout;

    /**
     * @param array{baseUrl?:string, webhookSecret?:string|string[], timeout?:int} $options
     */
    public function __construct(string $secretKey, array $options = [])
    {
        if ($secretKey === '') {
            throw new CyzoraException('secretKey is required');
        }
        $this->secretKey = $secretKey;
        $this->baseUrl = rtrim($options['baseUrl'] ?? self::DEFAULT_BASE_URL, '/');
        $this->timeout = (int)($options['timeout'] ?? 30);
        $ws = $options['webhookSecret'] ?? null;
        if ($ws === null) {
            $this->webhookSecrets = [];
        } elseif (is_array($ws)) {
            $this->webhookSecrets = array_values(array_filter($ws));
        } else {
            $this->webhookSecrets = [$ws];
        }
    }

    // -- helpers ----------------------------------------------------------
    public static function normalizePhone(string $phone): ?string
    {
        $p = preg_replace('/[\s\-+]/', '', $phone) ?? '';
        if ($p === '') {
            return null;
        }
        if ($p[0] === '0') {
            $p = '254' . substr($p, 1);
        } elseif ($p[0] === '7' || $p[0] === '1') {
            $p = '254' . $p;
        }
        return preg_match('/^254[17]\d{8}$/', $p) === 1 ? $p : null;
    }

    public static function toCents($amountKes): int
    {
        $kes = (int)round((float)$amountKes);
        if ($kes < 1) {
            throw new CyzoraException('Amount must be at least KES 1.');
        }
        return $kes * 100;
    }

    public static function fromCents($cents): float
    {
        return round((float)($cents ?: 0)) / 100;
    }

    // -- transport --------------------------------------------------------
    /**
     * @param array<string,mixed>|null $body
     * @param array<string,mixed>|null $query
     * @return array<string,mixed>
     */
    private function request(string $path, string $method = 'GET', ?array $body = null, ?string $idempotencyKey = null, ?array $query = null): array
    {
        $url = $this->baseUrl . $path;
        if ($query) {
            $clean = array_filter($query, static fn($v) => $v !== null && $v !== '');
            if ($clean) {
                $url .= '?' . http_build_query($clean);
            }
        }
        $headers = ['X-API-Key: ' . $this->secretKey, 'Accept: application/json'];
        $payload = null;
        if ($body !== null) {
            $headers[] = 'Content-Type: application/json';
            $payload = json_encode($body);
        }
        if ($idempotencyKey) {
            $headers[] = 'Idempotency-Key: ' . substr((string)$idempotencyKey, 0, 128);
        }

        $ch = curl_init($url);
        if ($ch === false) {
            throw new CyzoraException('Could not initialize cURL');
        }
        curl_setopt_array($ch, [
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_HTTPHEADER => $headers,
            CURLOPT_TIMEOUT => $this->timeout,
        ]);
        if ($payload !== null) {
            curl_setopt($ch, CURLOPT_POSTFIELDS, $payload);
        }
        $response = curl_exec($ch);
        if ($response === false) {
            $err = curl_error($ch);
            curl_close($ch);
            throw new CyzoraException('Could not reach Cyzora: ' . $err);
        }
        $status = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
        curl_close($ch);

        $data = [];
        $text = (string)$response;
        if ($text !== '') {
            $decoded = json_decode($text, true);
            $data = is_array($decoded) ? $decoded : ['raw' => $text];
        }
        if ($status < 200 || $status >= 300) {
            $msg = $data['error'] ?? $data['message'] ?? ('Cyzora request failed (' . $status . ')');
            throw new CyzoraException((string)$msg, $status, $data);
        }
        return $data;
    }

    // -- charges ----------------------------------------------------------
    /**
     * @param array{network?:string, accountRef?:string, description?:string, metadata?:array<string,mixed>, idempotencyKey?:string, amountCents?:int} $options
     * @return array<string,mixed>
     */
    public function createCharge($amountKes, string $phone, array $options = []): array
    {
        $normalized = self::normalizePhone($phone);
        if ($normalized === null) {
            throw new CyzoraException('Enter a valid Safaricom phone number (e.g. 0712 345 678).');
        }
        $cents = $options['amountCents'] ?? self::toCents($amountKes);
        if ((int)$cents < 100) {
            throw new CyzoraException('Amount must be a whole number of shillings, at least KES 1.');
        }
        $body = [
            'amount' => (int)$cents,
            'phone' => $normalized,
            'network' => $options['network'] ?? 'mpesa',
        ];
        if (!empty($options['accountRef'])) {
            $body['account_ref'] = substr((string)$options['accountRef'], 0, 64);
        }
        if (!empty($options['description'])) {
            $body['description'] = substr((string)$options['description'], 0, 200);
        }
        if (!empty($options['metadata'])) {
            $body['metadata'] = $options['metadata'];
        }
        return $this->request('/charges', 'POST', $body, $options['idempotencyKey'] ?? null);
    }

    /** @return array<string,mixed> */
    public function getCharge(string $chargeId): array
    {
        return $this->request('/charges/' . rawurlencode($chargeId));
    }

    /**
     * @param array<string,mixed> $options
     * @return array<string,mixed>
     */
    public function createCheckoutSession($amountKes, array $options = []): array
    {
        $body = ['amount' => self::toCents($amountKes)];
        foreach ([
            'accountRef' => 'account_ref', 'description' => 'description', 'callbackUrl' => 'callback_url',
            'cancelUrl' => 'cancel_url', 'customerEmail' => 'customer_email', 'metadata' => 'metadata',
        ] as $opt => $field) {
            if (isset($options[$opt])) {
                $body[$field] = $options[$opt];
            }
        }
        if (!empty($options['customerPhone'])) {
            $body['customer_phone'] = self::normalizePhone((string)$options['customerPhone']) ?? $options['customerPhone'];
        }
        if (isset($options['expiresIn'])) {
            $body['expires_in'] = (int)$options['expiresIn'];
        }
        return $this->request('/transactions/initialize', 'POST', $body, $options['idempotencyKey'] ?? null);
    }

    /** @return array<string,mixed> */
    public function verifyTransaction(string $reference): array
    {
        return $this->request('/transactions/verify/' . rawurlencode($reference));
    }

    // -- payouts ----------------------------------------------------------
    /**
     * @param array{wallet?:string, channelId?:string, destinationType?:string, phone?:string, shortCode?:string, accountRef?:string, bankCode?:string, accountNumber?:string, accountName?:string, idempotencyKey?:string} $options
     * @return array<string,mixed>
     */
    public function createPayout($amountKes, array $options = []): array
    {
        $cents = self::toCents($amountKes);
        if ($cents < self::PAYOUT_MIN_CENTS) {
            throw new CyzoraException('The smallest payout is KES 30.');
        }
        $body = ['amount' => $cents];
        if (!empty($options['wallet'])) {
            $body['wallet'] = $options['wallet'];
        }
        if (!empty($options['channelId'])) {
            $body['channel_id'] = $options['channelId'];
        } else {
            $type = $options['destinationType'] ?? 'mobile';
            $body['destination_type'] = $type;
            if ($type === 'mobile') {
                $body['phone'] = self::normalizePhone((string)($options['phone'] ?? '')) ?? ($options['phone'] ?? null);
            } elseif ($type === 'till') {
                $body['short_code'] = $options['shortCode'] ?? null;
            } elseif ($type === 'paybill') {
                $body['short_code'] = $options['shortCode'] ?? null;
                $body['account_ref'] = $options['accountRef'] ?? null;
            } elseif ($type === 'bank') {
                $body['bank_code'] = $options['bankCode'] ?? null;
                $body['account_number'] = $options['accountNumber'] ?? null;
                $body['account_name'] = $options['accountName'] ?? null;
            }
        }
        return $this->request('/payouts', 'POST', $body, $options['idempotencyKey'] ?? null);
    }

    /** @return array<string,mixed> */
    public function getPayout(string $payoutId): array
    {
        return $this->request('/payouts/' . rawurlencode($payoutId));
    }

    /** @return array<string,mixed> */
    public function getWallets(): array
    {
        return $this->request('/wallets');
    }

    /** @return array<string,mixed> */
    public function listPayoutChannels(): array
    {
        return $this->request('/payout-channels');
    }

    /** @return array<string,mixed> */
    public function listPayoutBanks(): array
    {
        return $this->request('/payout-banks');
    }

    /** @return array<string,mixed> */
    public function payoutFeePreview($amountKes, string $destinationType = 'mobile'): array
    {
        return $this->request('/payouts/fee-preview', 'GET', null, null, ['amount' => self::toCents($amountKes), 'destination_type' => $destinationType]);
    }

    // -- webhooks ---------------------------------------------------------
    /**
     * Verify a webhook signature. $rawBody must be the exact bytes received and
     * $signature the X-Cyzora-Signature header (hex HMAC-SHA256). Returns true if it
     * matches any configured webhook secret (timing-safe).
     */
    public function verifyWebhookSignature(string $rawBody, ?string $signature): bool
    {
        if ($this->webhookSecrets === [] || !$signature || $rawBody === '') {
            return false;
        }
        $sig = trim($signature);
        foreach ($this->webhookSecrets as $secret) {
            $expected = hash_hmac('sha256', $rawBody, $secret);
            if (hash_equals($expected, $sig)) {
                return true;
            }
        }
        return false;
    }
}
