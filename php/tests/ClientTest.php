<?php
declare(strict_types=1);

use Cyzora\Client;
use Cyzora\CyzoraException;

require __DIR__ . '/../src/Client.php';

function check(bool $condition, string $message): void
{
    if (!$condition) {
        throw new RuntimeException($message);
    }
}

check(Client::toCents(2500.4) === 250100, 'charges must round odd cents up');
check(Client::toPayoutCents(1500) === 150000, 'whole payout shillings');
check(Client::normalizePhone('0712345678') === '254712345678', 'phone normalization');
check(in_array('webhook.test', Client::WEBHOOK_EVENTS, true), 'webhook.test is a known event');

try {
    Client::toPayoutCents(1500.5);
    throw new RuntimeException('fractional payout should be refused');
} catch (CyzoraException $e) {
    check(strpos($e->getMessage(), 'whole shillings') !== false, 'payout error message');
}

putenv('CYZORA_API_BASE=https://example.test/api/v1/');
putenv('CYZORA_WEBHOOK_SECRET=whsec_a, whsec_b');
$client = new Client('sk_live_x');
$base = (new ReflectionProperty(Client::class, 'baseUrl'));
$base->setAccessible(true);
$secrets = (new ReflectionProperty(Client::class, 'webhookSecrets'));
$secrets->setAccessible(true);
check($base->getValue($client) === 'https://example.test/api/v1', 'base URL from environment');
check($secrets->getValue($client) === ['whsec_a', 'whsec_b'], 'webhook secrets from environment');

$raw = "\xff\xfe{\"event\":\"charge.succeeded\"}";
check($client->verifyWebhookSignature($raw, hash_hmac('sha256', $raw, 'whsec_b')), 'signature covers raw bytes');

$jsonRaw = '{"event":"charge.succeeded","note":"café"}';
$sig = hash_hmac('sha256', $jsonRaw, 'whsec_b');
check($client->constructWebhookEvent($jsonRaw, $sig, 'charge.succeeded')['event'] === 'charge.succeeded', 'event header matches');
try {
    $client->constructWebhookEvent($jsonRaw, $sig, 'payout.paid');
    throw new RuntimeException('mismatched event header should be refused');
} catch (CyzoraException $e) {
    check($e->status === 400, 'event mismatch status');
}

try {
    $client->createCharge(250, 'not-a-phone');
    throw new RuntimeException('invalid phone should be refused');
} catch (CyzoraException $e) {
    check(strpos($e->getMessage(), 'M-Pesa') !== false, 'phone error message');
}

try {
    $client->createPayout(29, ['phone' => '0712345678']);
    throw new RuntimeException('payout below minimum should be refused');
} catch (CyzoraException $e) {
    check(strpos($e->getMessage(), 'KES 30') !== false, 'minimum error message');
}

try {
    $client->createPayout(1500, ['destinationType' => 'paybill', 'shortCode' => '100000']);
    throw new RuntimeException('paybill without account reference should be refused');
} catch (CyzoraException $e) {
    check(strpos($e->getMessage(), 'accountRef') !== false, 'destination error message');
}

// Capture requests instead of sending them.
class CapturingClient extends Client
{
    /** @var array<int,array<string,mixed>> */
    public array $calls = [];

    protected function request(string $path, string $method = 'GET', ?array $body = null, ?string $idempotencyKey = null, ?array $query = null, bool $auth = true, bool $raw = false)
    {
        $this->calls[] = compact('path', 'method', 'body', 'idempotencyKey', 'query');
        return [];
    }
}

$cap = new CapturingClient('sk_live_x');
$cap->createCharge(100, '0712345678', [
    'description' => 'ignored',
    'metadata' => ['order_id' => 1001, 'paid' => false, 'skip' => null, 'cart' => ['a' => 1]],
]);
$last = end($cap->calls);
check(!isset($last['body']['description']), 'charge description is not sent');
check($last['body']['metadata'] === ['order_id' => '1001', 'paid' => 'false', 'cart' => '{"a":1}'], 'metadata values become strings');

try {
    $cap->createCharge(100, '0712345678', ['metadata' => array_fill_keys(array_map(fn($i) => "k$i", range(0, 20)), 1)]);
    throw new RuntimeException('more than 20 metadata keys should be refused');
} catch (CyzoraException $e) {
    check(strpos($e->getMessage(), '20 keys') !== false, 'metadata cap message');
}

$cap->listCharges(['status' => 'succeeded', 'fromDate' => '2026-10-01', 'toDate' => '2026-10-31', 'mode' => 'all']);
$last = end($cap->calls);
check($last['path'] === '/charges' && $last['query'] === ['status' => 'succeeded', 'from_date' => '2026-10-01', 'to_date' => '2026-10-31', 'mode' => 'all'], 'listCharges query names');

$cap->previewWalletMove(1500.5);
check(end($cap->calls)['query'] === ['amount' => 150050], 'wallet move preview exact cents');

$cap->moveToPayouts(2000, ['idempotencyKey' => 'MV-1']);
$last = end($cap->calls);
check($last['path'] === '/wallets/move' && $last['body'] === ['amount' => 200000] && $last['idempotencyKey'] === 'MV-1', 'wallet move payload');

$cap->createSubsidiaryTransfer('sub_1', 500, ['fromWallet' => 'payouts', 'idempotencyKey' => 'ST-1']);
check(end($cap->calls)['body'] === ['subsidiary_id' => 'sub_1', 'amount' => 50000, 'from_wallet' => 'payouts'], 'subsidiary transfer payload');

$cap->getPayoutBatch('b 1');
check(end($cap->calls)['path'] === '/payouts/batches/b%201', 'batch id is encoded');

foreach ([fn() => $cap->moveToPayouts(0), fn() => $cap->createSubsidiaryTransfer('', 500)] as $bad) {
    try {
        $bad();
        throw new RuntimeException('invalid wallet move should be refused');
    } catch (CyzoraException $e) {
        // expected
    }
}

echo "PHP SDK tests passed\n";
