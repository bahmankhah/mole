# Customer API (served by Laravel)

This is the public API customers call with their own API keys. Every product in this folder is a client of it: the SDKs, the MCP server, the n8n node, the WooCommerce plugin and the dashboard's code snippets. It is served by the Laravel control plane, never by Mole. Laravel checks the key, the plan and the credits, rewrites the request for the engine (see [laravel-integration.md](../laravel-integration.md)) and returns a customer-safe answer.

Design goals:
* **Familiar.** Route names and request fields follow Firecrawl's v1 API where the concepts match, so customers and their tools can switch with little work.
* **Stable.** It is versioned (`/v1`) independently of the engine's `/v1`. Engine changes must never leak into it.
* **Safe.** No engine internals are exposed: no `owner_ref`, worker ids, regions, proxy pool names, plugin settings or engine URLs.

## 1. Conventions

| Topic | Rule |
|---|---|
| Base URL | `https://api.<brand>.ir/v1` (keep it on an Iranian host so it works during international outages) |
| Auth | `Authorization: Bearer mk_live_…` (test keys: `mk_test_…`, which run against a sandbox team with free, small limits) |
| Format | JSON in and out, UTF-8, Persian text unescaped. Success: `{"success": true, "data": …}`. Lists add `"next_cursor"` |
| Errors | `{"success": false, "error": {"code": "…", "message": "…", "message_fa": "…"}}` with the HTTP status below |
| Idempotency | `Idempotency-Key` header on every `POST` that creates something; same key and body → same answer |
| Rate limits | `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `Retry-After` on `429` |
| Credits | `X-Credits-Remaining` on every answer; `X-Credits-Used` on answers that charged |
| Ids | Laravel's UUIDs (`crawl_…`, `batch_…`, `mon_…` prefixes are fine); never Mole ids |
| Time | RFC 3339 in UTC; the dashboard shows Jalali dates, the API does not |

Error codes:

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_request` | Validation failed; `message` says which field |
| 401 | `unauthorized` | Missing, wrong or revoked key |
| 402 | `insufficient_credits` | The reservation for this request does not fit the balance |
| 403 | `plan_restricted` | The plan does not allow this option (e.g. browser rendering, Iranian proxies); `message` names the plan that does |
| 404 | `not_found` | Unknown id, or an id of another team |
| 409 | `conflict` | Invalid state (cancelling a finished crawl) |
| 422 | `idempotency_conflict` | Same `Idempotency-Key`, different body |
| 429 | `rate_limited` | Too many requests; see `Retry-After` |
| 502 | `upstream_failed` | Search provider or engine failure; nothing was charged |
| 503 | `unavailable` | Maintenance; retry later |

## 2. Endpoints

### Scrape

`POST /v1/scrape`

```json
{
  "url": "https://www.example.ir/product/123",
  "formats": ["markdown", "metadata", "product"],
  "only_main_content": true,
  "render": "auto",
  "wait_for": ".price",
  "actions": [{"type": "scroll", "value": "bottom", "count": 3}],
  "extract": {
    "fields": [{"name": "price", "selector": "jsonld:Product.offers.price", "type": "price"}],
    "recipe": "woocommerce-product",
    "prompt": "قیمت و موجودی محصول را استخراج کن",
    "schema": {"type": "object", "properties": {"price": {"type": "number"}}}
  },
  "screenshot": false,
  "max_age": 172800,
  "timeout": 60
}
```

Laravel maps it onto engine `settings`. `render`: `http` → no browser, `browser` → `use_headless_browser`, `auto` → `headless_fallback`. `extract.recipe` expands to the recipe's `extract_fields` ([recipes.md](recipes.md)). `extract.prompt`/`schema` → `llm_prompt`/`llm_schema`. `max_age` passes through, with the plan's default when it is missing. `formats` passes through. Besides the content formats there are the detectors, which need no selectors: `product`, `article`, `business`, `job_posting`, `event` and `faq` (fields in the engine [README](../../README.md#job-settings-reference)). Each one answers under its own key. In the playground, preselect `product` for URLs that look like product pages and `article` for news sites. Answer:

```json
{"success": true, "data": {
  "url": "…", "final_url": "…", "status_code": 200, "title": "…",
  "markdown": "…", "metadata": {"…": "…"},
  "product": {"name": "…", "price": {"amount": 18500000, "currency": "IRT", "raw": "…"}, "availability": "in_stock", "sku": "…", "sources": {"price": "json_ld"}},
  "extracted": {"price": {"amount": 18500000, "currency": "IRT"}},
  "llm_extracted": {"price": 18500000}, "cached": true, "cached_at": "…", "credits_used": 1}}
```

PDF and DOCX URLs work the same way; `metadata.document_type` and `metadata.pages` describe them.

### Batch scrape

* `POST /v1/batch/scrape`: `{"urls": [...], <the scrape options above, except max_age>, "webhook": {"url", "events", "secret"}}` → `{"id": "batch_…", "status": "queued", "total": 120}`
* `GET /v1/batch/scrape/{id}` → status, progress (`completed`, `total`), `credits_used`
* `GET /v1/batch/scrape/{id}/results?cursor=` → pages (`url`, `status_code`, `markdown`, `product`, `extracted`, `error`)
* `GET /v1/batch/scrape/{id}/export?format=csv|jsonl|json` → a file (CSV opens in Excel with Persian text)
* `DELETE /v1/batch/scrape/{id}` → cancel

### Crawl

* `POST /v1/crawl`: `{"url", "limit" (pages), "max_depth", "include_paths", "exclude_paths", "include_documents", <scrape options>, "webhook"}` → `{"id": "crawl_…"}`. `include_paths`/`exclude_paths` → `url_include_patterns`/`url_exclude_patterns`
* `GET /v1/crawl/{id}`, `GET /v1/crawl/{id}/results`, `GET /v1/crawl/{id}/export`, `DELETE /v1/crawl/{id}`
* `stop_reason` is shown in customer terms: `completed`, `page_limit` (with an upgrade hint), `time_limit`, `cancelled`

### Map

`POST /v1/map`: `{"url", "search", "limit", "include_subdomains"}` → `{"urls": [...]}`

### Search

`POST /v1/search`: `{"query", "limit" (≤ plan cap), "lang", "country", "scrape_options": {<scrape options>} | null}` → `{"results": [{"url", "title", "description", "markdown"?, "metadata"?, "error"?}]}`. Charged per query plus the scrapes. On `502` nothing is charged.

### Monitors (schedules as customers see them)

A monitor re-checks a list of URLs (or a whole site) on a schedule and notifies the customer about changes.

* `POST /v1/monitors`: `{"name", "urls": [...] | "site": "https://…", "recipe" | "extract" (default: product detection), "frequency": "hourly|every_6_hours|daily|weekly" | "cron", "notify": {"channels": ["chn_…"], "only_fields": ["price"], "min_change_percent": 1}, "webhook"}`
* `GET /v1/monitors`, `GET /v1/monitors/{id}` (with the latest values per URL), `PATCH`, `DELETE`
* `POST /v1/monitors/{id}/urls` / `DELETE /v1/monitors/{id}/urls` to add or remove URLs (Laravel updates the schedule's `seed_urls`)
* `GET /v1/monitors/{id}/changes?cursor=` → `[{url, detected_at, fields: [{name, old, new}], text_changed}]`
* `POST /v1/monitors/{id}/run` → run now (counts against the plan's manual runs)

Laravel builds a batch schedule (`template.kind = "batch"`) for URL lists and a crawl schedule for `site`, and enforces the plan's minimum interval and URL count.

### Account

* `GET /v1/account` → team, plan, credits balance, limits (concurrency, max URLs per batch, monitors, minimum interval)
* `GET /v1/usage?from=&to=` → credits by day and by product (scrape, batch, crawl, search, monitors)
* `GET/POST/DELETE /v1/webhooks` → the team's webhook endpoints (used by the n8n trigger and Zapier-like tools)
* `GET /v1/recipes` → recipes the team may use (slug, name, sites, fields)

## 3. Customer webhooks

Laravel sends them from its own events, never by forwarding Mole payloads.

Headers: `X-Webhook-Id` (unique, for de-duplication), `X-Webhook-Event`, `X-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<unix>.<raw body>")>`. That is the same scheme as Mole, so the SDK helpers verify both.

| Event | Payload `data` |
|---|---|
| `crawl.completed`, `crawl.failed` | the crawl as `GET /v1/crawl/{id}` returns it |
| `batch.completed`, `batch.failed` | the batch status |
| `monitor.changed` | `{monitor_id, url, fields: [{name, old, new}], text_changed, detected_at}` |
| `monitor.failed` | `{monitor_id, url, error}`; for example the page now answers 404, or the recipe found no price |
| `credits.low` | `{balance, threshold}` |

Deliveries are retried with backoff for 24 hours. The dashboard shows the delivery log with a "redeliver" button.

## 4. Implementation notes for Laravel

* **One controller per resource**, a `FormRequest` per endpoint, and an `EngineRequestBuilder` that turns a validated customer request plus the team's plan into the engine body. Plan checks live there, in one place.
* **Charging:** reserve before calling the engine, settle from the engine's `usage` (see the pricing formula in the integration doc), release the rest. Synchronous calls (`scrape`, `map`, `search`) settle in the same request.
* **Timeouts:** synchronous routes accept `timeout` up to 120 s. Put Octane/FPM and the reverse proxy timeouts above that, or answer `202` with a job id for long scrapes.
* **Rate limits** per key and per team (`RateLimiter::for('customer-api', …)`), with plan-based numbers.
* **Logging:** keep request id, team, route, status, credits and duration. Never store request bodies with customer cookies or headers.
* **OpenAPI:** keep an `openapi.yaml` for this API in the Laravel repo and generate the dashboard's API reference from it. The SDKs are tested against it (see [sdks.md](sdks.md)).
