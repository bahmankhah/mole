# SDKs: PHP, JavaScript/TypeScript, Python

The SDKs wrap the [customer API](customer-api.md). They are how developers try the service in five minutes and how the MCP server and the n8n node talk to it, so they come before any other developer-facing product. (The WordPress plugin has its own small client on `wp_remote_request`: WordPress plugins must run on PHP 7.4 and should not bundle Guzzle, which conflicts with other plugins.)

Order: **PHP** first (Laravel and PHP developers are the largest group in Iran), **JavaScript/TypeScript** second (the MCP server and n8n node are built on it), **Python** third (AI and data teams).

## 1. Shared design

Every SDK has the same surface, with each language's naming style:

| Method | API call | Notes |
|---|---|---|
| `scrape(url, options)` | `POST /v1/scrape` | Returns the page; `options.maxAge` for the cache |
| `batchScrape(urls, options)` | `POST /v1/batch/scrape` then poll | Waits until done (or `timeout`) and returns all results, following `next_cursor` |
| `startBatchScrape(urls, options)` / `getBatchScrape(id)` / `getBatchScrapeResults(id)` | | The non-blocking form |
| `crawl(url, options)` / `startCrawl` / `getCrawl` / `getCrawlResults` / `cancelCrawl` | `/v1/crawl…` | Same pattern |
| `map(url, options)` | `POST /v1/map` | |
| `search(query, options)` | `POST /v1/search` | `options.scrapeOptions` scrapes the results |
| `monitors.create/list/get/update/delete/addUrls/removeUrls/changes/run` | `/v1/monitors…` | |
| `account()`, `usage(from, to)`, `recipes()` | | |
| `verifyWebhook(rawBody, signatureHeader, secret, toleranceSeconds = 300)` | — | Same scheme as the engine: `t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`; constant-time compare |

Behaviour all three must share:
* **Auth:** API key from the constructor or the `<BRAND>_API_KEY` environment variable; base URL overridable (for the enterprise edition and for tests).
* **Errors:** one exception/error class per API `code` (`InsufficientCreditsError`, `PlanRestrictedError`, `RateLimitedError` with `retryAfter`, …), each carrying `code`, `message`, `messageFa`, HTTP status and the request id.
* **Retries:** on connection errors, `429` (honouring `Retry-After`), `502` and `503`, with exponential backoff and jitter, 3 attempts by default. Creating calls send an auto-generated `Idempotency-Key`, so retries never create duplicates.
* **Polling:** waiting helpers poll with backoff from 2 s up to 15 s and stop at a timeout, returning the partial state instead of throwing it away.
* **Unicode:** never escape Persian text (PHP: `JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES`), and keep ZWNJ (U+200C) intact.
* **No dependencies beyond the standard HTTP stack:** PHP uses Guzzle 7 or any PSR-18 client; JS uses `fetch` (Node 18+, browsers, Deno, Bun); Python uses `httpx` with a sync and an async client.
* **Typed:** PHP 8.1 readonly DTOs plus array access for forward compatibility; TypeScript types generated from the API's `openapi.yaml`; Python `TypedDict`/dataclasses.

## 2. Packages

| Language | Package | Repository layout |
|---|---|---|
| PHP | `<brand>/<brand>-php` on Packagist, plus `<brand>/<brand>-laravel` (service provider, facade, config, webhook middleware) | `src/Client.php`, `src/Resources/*`, `src/Exceptions/*`, `src/Webhook.php` |
| JS/TS | `@<brand>/sdk` on npm, ESM and CJS builds | `src/client.ts`, `src/resources/*`, `src/errors.ts`, `src/webhook.ts` |
| Python | `<brand>` on PyPI | `<brand>/client.py`, `<brand>/async_client.py`, `<brand>/errors.py`, `<brand>/webhook.py` |

**Distribution from Iran:** publishing to Packagist, npm and PyPI works from Iran, but some developers cannot reach these registries reliably. Mirror every release as a downloadable archive on your own domain and in the docs ("install from a file"), and keep source on GitHub with a mirror on an Iranian Git host.

## 3. Example (PHP)

```php
$client = new Brand\Client(getenv('BRAND_API_KEY'));

$page = $client->scrape('https://shop.example.ir/product/123', [
    'formats' => ['markdown', 'product'],
    'extract' => ['recipe' => 'woocommerce-product'],
    'max_age' => 86400,
]);
echo $page->extracted['price']['amount'];
echo $page->product['availability'] ?? 'unknown'; // with 'formats' => ['product']

$batch = $client->batchScrape($urls, ['formats' => ['product']], timeout: 600);
foreach ($batch->results as $result) { /* … */ }

// In a webhook controller:
Brand\Webhook::verify($request->getContent(), $request->header('X-Webhook-Signature'), $secret);
```

## 4. Testing and release

* **Contract tests:** each SDK runs its test suite against a mock server generated from `openapi.yaml` (Prism), and a small live suite against the sandbox team (`mk_test_` key) in CI every night.
* **Webhook test vectors:** one JSON file shared by all three SDKs and by Laravel: secret, timestamp, body and expected signature. All four must agree.
* **Versioning:** SemVer; a minor release for every new API field; a CHANGELOG written for customers.
* **Docs:** every dashboard code snippet ("copy as PHP / JS / Python / curl") is generated from the same examples the SDK tests run, so snippets never rot.

## 5. Done when (end of week 8)

PHP and JS packages are published with full coverage of section 1, contract tests green, the Laravel package's webhook middleware used by the dashboard's own example app, and the MCP server and n8n node using the JS package. Python can follow in week 9 or 10.
