# Laravel control plane: requirements and engine API

This document is for whoever builds the Laravel + Inertia + Vue application that sells Mole as a service. Part 1 lists what the Laravel app must do. Part 2 is the `/v1` API reference, and Part 3 covers webhooks. The engine side is complete; nothing in this document requires changes to Mole.

The products built on top of the Laravel app (customer API, WooCommerce plugin, SDKs, MCP server, n8n node, recipes, enterprise edition) are specified in [docs/products/](products/README.md).

Contents:
1. [Requirements for the Laravel app](#1-requirements-for-the-laravel-app)
2. [API reference (`/v1`)](#2-api-reference-v1)
3. [Webhooks](#3-webhooks)

---

## 1. Requirements for the Laravel app

### 1.1 Who does what

| Concern | Laravel | Mole engine |
|---|---|---|
| Users, teams, roles, login, 2FA | ✔ | — |
| Plans, credit wallet, payments (Zarinpal/IDPay), invoices | ✔ | — |
| Customer API keys and the public customer API | ✔ | — |
| Deciding *whether* a request is allowed (plan features, credits) | ✔ | — |
| Turning a plan into concrete `limits` and allowed `settings` | ✔ | — |
| Queueing, crawling, rendering, extraction, change detection | — | ✔ |
| PDF/DOCX conversion, web search, scrape cache | decides who may use them | ✔ |
| Recipes (ready-made extraction settings per site) | ✔ stores, versions, tests | runs the settings it receives |
| Customer notifications (email, SMS, Bale) | ✔ | — (sends webhooks to Laravel only) |
| Fairness between tenants | sets each team's limit (`PUT /v1/owners/{ref}/limits`) | ✔ enforces |
| Stopping a crawl exactly at its limits | sends limits | ✔ enforces |
| Measuring usage (pages, bytes, browser time, LLM tokens…) | bills from it | ✔ reports |
| Storing results (pages, Markdown, screenshots) | proxies them | ✔ stores |
| Data retention policy | ✔ decides and calls DELETE | ✔ deletes |

Mole never sees users or plans. Laravel identifies a tenant to Mole only by an opaque `owner_ref`, for example `team-42`.

### 1.2 Connecting to the engine

* **Network:** run Mole on a private network or behind a firewall that only Laravel can reach. Never expose `/v1` to customers directly.
* **Engine config:** set `api.tokens` to a long random token; store it in Laravel as `MOLE_TOKEN` and the base URL as `MOLE_URL`. Disable or password-protect the built-in UI (`ui.username`/`ui.password`, or `mole serve --no-ui`).
* **Webhooks into Laravel:** if Laravel is on a private address, set `webhooks.allow_private: true` (or add its range to `security.allowed_cidrs`).
* **Service class:** put every call in one `MoleClient` (Laravel HTTP client) with:
  * `Authorization: Bearer`;
  * a timeout (30 s for normal calls, up to 300 s for `/v1/scrape`);
  * retries with backoff on connection errors and `5xx` only;
  * mapping of the [error format](#21-conventions) to exceptions.
* **Always send `X-Owner-Ref`** with the team's owner ref on calls made for a customer. Mole then returns 404 for another team's crawl, which protects you even if a Laravel authorization check is missed.
* **Capabilities:** call `GET /v1/info` (cache it for a few minutes) to fill UI dropdowns: proxy pools, regions, fingerprints, output formats, extraction field types and selector prefixes (`xpath:`, `jsonld:`), webhook events, whether LLM extraction is available, which documents are converted (`documents`: `pdf`, `docx`), the search provider (`search_provider`, empty when `/v1/search` is off) and whether the scrape cache is on (`scrape_cache`).

### 1.3 Data model (suggested)

| Table | Key columns | Notes |
|---|---|---|
| `teams` | `id`, `owner_ref` (unique, e.g. `team-{id}`), `plan_id`, `credit_balance` | `owner_ref` never changes |
| `plans` | features and limits (see 1.5) | |
| `credit_ledger` | `team_id`, `amount`, `reason`, `crawl_id`, `mole_event_id` | append-only; every charge, reservation and refund |
| `crawls` | `id` (uuid), `team_id`, `mole_job_id`, `status`, `stop_reason`, cached `usage` JSON, `credits_reserved`, `credits_charged`, `request` JSON, timestamps | `id` doubles as Mole `external_id` and `Idempotency-Key` |
| `schedules` | `id`, `team_id`, `mole_schedule_id`, `name`, `cron`, `enabled` | |
| `mole_webhook_events` | `delivery_id` (unique), `type`, `crawl_id`, `received_at` | de-duplication |
| `api_keys` | `team_id`, hashed key, scopes, `last_used_at` | the customers' keys for your public API |
| `wordpress_sites` | `team_id`, `site_url`, `api_key_id` | for the WordPress plugin |
| `recipes` | `slug`, `version`, `host_pattern`, `settings` JSON, `test_urls` JSON, `status` | see [recipes](products/recipes.md) |
| `monitors` | `team_id`, `mole_schedule_id`, `recipe_id`, `kind` (`batch`/`crawl`), `notify` JSON | a schedule as the customer sees it (e.g. "track these 40 products daily") |
| `tracked_items` | `monitor_id`, `url`, `product_id` (WooCommerce), last values JSON | one URL of a batch monitor and its latest extracted values |
| `notification_channels` | `team_id`, `type` (`email`, `sms`, `bale`, `webhook`), `target`, `verified_at` | where alerts go |
| `alerts` | `team_id`, `monitor_id`, `url`, `fields` JSON, `sent_at` | one change notification (de-duplicated by Mole delivery id + URL) |

### 1.4 Core flows

**Create a crawl**
1. Authorize the user and validate the request against the plan (1.5). Remove any setting the plan does not allow; never forward `after_crawl_plugin`/`after_job_plugin` from customers.
2. Reserve credits for the worst case (for example `limits.max_pages × page price`) in a DB transaction. Reject if the balance is too low.
3. Insert the local `crawls` row (`status = creating`).
4. Call `POST /v1/crawls` with:
   * `owner_ref` = the team's ref;
   * `external_id` = the local crawl id;
   * `Idempotency-Key` = the local crawl id;
   * `limits` from the plan;
   * `webhook` = `{url: route('mole.webhook'), secret: config('services.mole.webhook_secret')}`.

   Because of the idempotency key, a timeout can be retried safely and returns the same Mole job.
5. Store `mole_job_id`. On a `4xx` error, release the reservation and show the error message to the user (Mole's messages are written for humans).

**Track status**
* **Webhooks are the primary channel** (Part 3). The handler must:
  1. verify the signature;
  2. de-duplicate by `X-Mole-Delivery`;
  3. find the crawl by `job.external_id`, or by `job.id` (= `mole_job_id`) for runs created by a schedule, which have no external id;
  4. update status, `stop_reason` and `usage`;
  5. return `200` quickly (dispatch slow work to a queue).
* Deliveries are at-least-once and may arrive out of order. Never move a crawl from a terminal status (`completed`/`failed`/`cancelled`) back to an active one, and ignore events whose `created_at` is older than the last applied event.
* **Fallback poller:** a Laravel scheduled task every few minutes calls `GET /v1/crawls?owner_ref=…&status=queued,running` (or `GET /v1/crawls/{id}`) for crawls without an update in 10 minutes. This covers webhook outages.
* **Live progress in the UI:** poll `GET /v1/crawls/{id}` every few seconds through a Laravel endpoint while the page is open. `data.live` has pages/min and in-flight counts. Subscribing every customer to `job.progress` is not needed.

**Finish and bill**
* On `job.completed`/`failed`/`cancelled`, compute the charge from `job.usage` (1.6). In one transaction: write a ledger entry, release the remaining reservation, and set `credits_charged`.
* `stop_reason` tells the user why a crawl ended:
  * `frontier_exhausted`: the whole site was crawled;
  * `limit:max_pages` (or another limit): a plan limit was reached — show an upgrade hint;
  * `user`: cancelled by the user.

**Show results** (always proxy through Laravel after authorization)
* Pages: `GET /v1/crawls/{id}/pages?content=1&order=desc&cursor=…`. Store `next_cursor` for "load more"; filters are `successful=1` and `failed=1`.
* One page: `GET /v1/crawls/{id}/pages/{pageId}`. Screenshot: `GET …/screenshot`, streamed through Laravel with `Content-Type: image/png`.
* Exports: stream `GET /v1/crawls/{id}/export?format=csv|jsonl|json` to the browser. Use `Http::withOptions(['stream' => true])` and a `StreamedResponse`; do not buffer large exports in memory.
* Changes: `GET /v1/crawls/{id}/changes?diff=1`.

**Control:** pause, resume, cancel and duplicate map one-to-one to `POST /v1/crawls/{id}/{action}`. Delete with `DELETE /v1/crawls/{id}`, then soft-delete locally.

**Scrape and map (synchronous):** `POST /v1/scrape` and `POST /v1/map`. Apply a per-team rate limit in Laravel (`RateLimiter`), charge from `data.usage`, and call them from a queued job if your web timeout is short. Always send `X-Owner-Ref` on scrapes: it scopes private cache entries.

**Scrape cache:** send `max_age` (seconds) on `/v1/scrape` to accept a result fetched that recently. A hit answers in milliseconds with `cached: true`, `cached_at` and **zero usage**: the engine did no work. Decide in Laravel what a hit costs. Charging the normal price (as Firecrawl does) is where most of the margin comes from; charging less is a selling point. A good default is `max_age: 172800` (2 days) for API customers and `0` for monitors, which must see fresh data. `store_in_cache: false` keeps a result out of the cache (for example for pages behind a customer's login, which are already scoped to the owner anyway).

**Batch scrape (asynchronous):** `POST /v1/batch/scrape` with `urls` (up to 10,000), `owner_ref`, `external_id`, `settings`, `limits` and `webhook`, plus an `Idempotency-Key`. It creates a job of kind `batch` that fetches exactly these URLs. Reserve credits for `count(urls) × page price`, then track and read it exactly like a crawl (`/v1/crawls/{id}`, `/pages`, `/export`, webhooks). Use it for "scrape these 300 product pages", for LLM extraction over a list of URLs (Firecrawl's `/extract`: `llm_prompt` + `llm_schema` in `settings`), and as the template of price-monitoring schedules.

**Web search:** `POST /v1/search` with `query`, `limit` (≤ 50), `lang`, `country`, and optionally `scrape: true` plus scrape `settings` and `max_age`. It answers with the provider's results and, with `scrape`, each result's scrape (or `error`). Charge a fixed price per search plus `data.usage` for the scrapes. It answers `502 search_failed` when the provider fails; do not charge then. Search runs on the API process, which needs to reach the provider (set `search.proxy` to a foreign exit if the control node is in Iran).

**Documents:** PDF and DOCX responses are converted automatically on scrapes, batches and crawls (`metadata.document_type`, `metadata.pages`). Crawls follow links to documents only with `settings.include_documents: true`. Usage reports converted pages as `document_pages`; price them separately, since they cost more CPU than HTML.

**Detectors (structured data without selectors):** add detector formats to `settings.formats` on scrapes, batches, crawls and schedules: `product`, `article`, `business`, `job_posting`, `event` and `faq`. Each result comes back under its format name: on a scrape at the top level, on crawl pages under `content`, and in exports. `GET /v1/info` → `detectors` gives each one's label, description and main fields, so the dashboard can render the same picker as the engine UI. The [README](../README.md#job-settings-reference) lists every field and the rules. They cost no extra engine resources (the page is parsed once for all of them), so no extra charge is needed. How to use each one in products:
* **`product`:** the default for "paste a product URL" and for monitors. It returns price, regular price and discount, availability, SKU/GTIN, brand, images, category, breadcrumbs, rating and seller. Fall back to `extract_fields` (a [recipe](products/recipes.md)) only for shops where `product` or its price is missing.
* **`article`:** media monitoring and news datasets. It returns headline, authors, publish and modify dates (Jalali converted), section and keywords. Combine it with `markdown` and `only_main_content` for the body.
* **`business`:** lead lists of shops and companies built from their own published contact data (phones, emails, address, geo, opening hours, social links). The engine never scrapes contacts from page text. Keep the acceptable-use policy in the product anyway, and do not resell personal data.
* **`job_posting`, `event`, `faq`:** job-market and event aggregation, and FAQ import for support bots or RAG.

**Price and change monitoring:** create a schedule whose template has `"kind": "batch"`, the product URLs (`target_url` plus `seed_urls`) and `"formats": ["product"]`. Add `extract_fields` only for shops that detection does not cover; prefer `jsonld:Product.offers.price` with CSS `fallback_selectors`, or a [recipe](products/recipes.md). Each run is compared with the previous one. Subscribe the template's webhook to `page.changed` (it is not sent by default) together with the lifecycle events you need, e.g. `"events": ["job.queued", "job.completed", "job.failed", "job.changes", "page.changed"]`. A `page.changed` webhook carries `fields`: every extracted field and every detected field (named `product.price`, `product.availability`, `event.status`, …; the FAQ as a whole as `faq`) whose value changed, with `old` and `new`. Prices compare by amount and currency; where a value was found (`sources`) is not compared. Turn those into alerts and WooCommerce updates; see [the price monitor](products/woocommerce-price-monitor.md).

**Schedules:** create through `POST /v1/schedules` with `owner_ref`, `cron`, `timezone` (default `Asia/Tehran`) and a `template` built the same way as a crawl request. Store `mole_schedule_id`. Every run creates a Mole job with `schedule_id`. Put `job.queued` in the template webhook's `events` (it is not sent by default), together with the lifecycle events you need. When it arrives with a `job.schedule_id` you don't have a row for yet, insert a local `crawls` row. The fallback poller (`GET /v1/crawls?schedule_id=…`) catches any run you missed. Enforce the plan's schedule count and minimum interval in Laravel; Mole accepts any valid cron. For the schedule form, `GET /v1/cron/preview?cron=…&timezone=…` checks an expression and returns its description ("Daily at 03:00") and next run times; it answers `400` for an invalid expression. To offer "schedule this crawl", copy the crawl's settings into a template; a webhook secret shown masked (`********`) is kept when the request carries `source_job_id` (the crawl's id). Put the link depth in `template.settings.max_depth`: it is what each run uses. The engine UI's schedule page (`/schedules/new?from_job=<id>`) is a working reference.

**Account deletion:** `DELETE /v1/owners/{owner_ref}` removes every crawl, schedule, stored artifact, owner limit and owner-scoped cache entry of the team. Then delete local data.

**Plan changes:** when a team's plan changes, call `PUT /v1/owners/{owner_ref}/limits` with `{"max_running": n}` (0 = unlimited). Workers apply it to the next job they claim, and it holds across the whole cluster. `DELETE` the limit to fall back to `worker.max_running_per_owner`.

**Retention:** a daily Laravel job deletes crawls older than the plan's retention with `DELETE /v1/crawls/{id}`.

**Usage report and reconciliation:** `GET /v1/owners/{ref}/usage?from=…&to=…` sums usage per team for a period. Use it for the monthly statement and to reconcile against your ledger.

### 1.5 Plans → engine settings

Laravel turns each plan into values it always sends, overriding anything the customer chose:

| Plan feature | Sent to Mole as |
|---|---|
| Pages per crawl | `limits.max_pages` (and cap `settings.max_pages`) |
| URLs per batch | count `urls` before calling `/v1/batch/scrape` (the engine accepts up to 10,000) |
| PDF/DOCX in crawls | allow `settings.include_documents` |
| Web search | allow `/v1/search`; cap `limit` and searches per month in Laravel |
| Crawl duration | `limits.max_duration_seconds` |
| Browser rendering allowed / browser minutes | allow `use_headless_browser`/`headless_fallback`; `limits.max_browser_seconds` |
| LLM extraction / tokens | allow `llm_prompt`; `limits.max_llm_tokens` |
| Screenshots | allow `screenshot` |
| Premium proxies (Iranian IPs, residential) | allow `proxy_pool` values per plan; strip customer `proxies` unless allowed |
| Speed | cap `max_concurrent_requests`; never allow `host_rate_limit` higher than the engine default (Mole already enforces this) |
| Priority support / faster queue | `priority` |
| Region choice | `region`: `auto`, `ir` or `intl` (only regions that `GET /v1/info` lists) |
| Concurrent crawls per team | `PUT /v1/owners/{ref}/limits` `{"max_running": n}` when the plan is bought or changed |
| Schedules count / minimum interval / retention | enforced in Laravel only |

### 1.6 Pricing from usage (example)

Every finished crawl reports `usage = {requests, pages, bytes, browser_ms, llm_tokens, screenshots, proxy_requests, document_pages, errors}`. A simple credit formula:

```
credits = pages × 1
        + document_pages × 1                 # PDF/DOCX pages converted
        + ceil(browser_ms / 1000) × 0.2      # rendering costs CPU
        + llm_tokens / 1000 × 2
        + screenshots × 0.5
        + proxy_requests × 0.5               # paid proxy bandwidth
search  = 2 per /v1/search call, plus the scrapes' usage
cache hit (cached: true) = the price of the page it replaces (usage is zero)
```

Measure your real cost per 1,000 HTTP pages, browser pages, proxy requests, document pages and LLM tokens from these numbers, and keep credit prices at 3–5× that cost.

Show prices in Toman, sell prepaid credit packs, and issue official invoices for business customers.

### 1.7 The customer-facing API

If customers get an API (the WordPress plugin needs one), Laravel exposes its own `/api/v1/...` with per-team API keys, rate limits and plan checks. Map each route onto the Mole call, and never forward a customer's key or request to Mole as-is. Rewrite `owner_ref`, `limits`, `webhook` and the forbidden settings on the server. Customer webhooks should be sent by Laravel (from its own events), not by Mole, so customers never see engine payloads or internal URLs. The full contract (routes, errors, rate-limit and credit headers, customer webhooks) is in [products/customer-api.md](products/customer-api.md); the SDKs, MCP server, n8n node and WordPress plugin are all clients of it.

### 1.8 WordPress plugin

The plugin talks only to the Laravel customer API with a site-specific key; it is specified in [products/woocommerce-price-monitor.md](products/woocommerce-price-monitor.md). Typical features map to:
* price monitoring of competitors → batch schedules with `"formats": ["product"]` (plus `extract_fields` of type `price` for shops detection misses) and `page.changed` webhooks, whose `fields` list the old and new price and stock → Laravel → alerts and the plugin (update WooCommerce prices);
* site audit → a crawl of the customer's own site, then report 4xx/5xx pages (`failed=1`), missing titles and descriptions (metadata);
* content import → Markdown/metadata of selected pages (respect copyright and the source sites' terms).

### 1.9 UI screens to build (Inertia + Vue)

Dashboard (credits, running crawls), new crawl wizard (URL → fetch mode → scope → outputs/extraction → limits, with plan-based locks and an estimated credit cost before running), crawl detail (live progress, pages and data, changes with changed fields, export), scrape playground (paste a URL, see Markdown/JSON in seconds, copy the matching code for each SDK), batch scrape (paste or upload a URL list), search playground, monitors (recipe picker → product URLs → frequency → alert channels), schedules, usage and billing, API keys, notification channels, team members, WordPress sites. Build the UI right-to-left and Persian-first with Jalali dates. The engine's own UI (`/jobs/{id}`, `/scrape`, `/schedules`) is a working reference for each screen and the payloads it sends.

### 1.10 Error handling

| Mole response | Laravel action |
|---|---|
| `400 invalid_request` / `invalid_json` | Validation problem: show `error.message` to the user, release reservations |
| `502 search_failed` | The search provider failed: do not charge; retry later or show "search is unavailable" |
| `401` | Wrong `MOLE_TOKEN`: alert the operator |
| `403 forbidden` | A plugin operation from a customer request: never forward plugin settings |
| `404 not_found` | Wrong id or owner: treat as not found |
| `409 conflict` | Invalid state (e.g. pausing a finished crawl) or an idempotent request still in progress: refresh state, retry later |
| `422 idempotency_conflict` | Same `Idempotency-Key` with a different body: a bug in Laravel; use a new key per new request |
| `5xx` / connection error | Retry with backoff (safe for create thanks to the idempotency key); keep the crawl `creating` and let the poller reconcile |

A crawl pinned to a region with no running worker stays `queued`. Show "waiting for a worker" and alert the operator (`GET /v1/workers` lists live workers and their regions).

### 1.11 Security checklist

* `MOLE_TOKEN` is only on the server; `/v1` is not reachable from the internet.
* Verify every webhook signature and its timestamp (± 5 min), and de-duplicate by delivery id.
* Always send `X-Owner-Ref` and authorize locally before every proxied call.
* Never pass plugin settings, custom proxies or raw settings from customers without a whitelist.
* Keep `security.allow_private_networks: false` on the engine (SSRF protection).

---

## 2. API reference (`/v1`)

### 2.1 Conventions

* Auth: `Authorization: Bearer <token>` (tokens in `api.tokens`). `GET /v1/health` and `/healthz` are public.
* Success: `{"data": …}`; lists add `"next_cursor"` (pass it back as `?cursor=`; empty means the last page).
* Errors: HTTP status plus `{"error": {"code": "invalid_request|invalid_json|not_found|conflict|idempotency_conflict|unauthorized|internal_error", "message": "…"}}`.
* Unknown JSON fields are rejected, including inside `settings`, so typos surface immediately.
* Send `X-Owner-Ref: <ref>` to scope a request to one owner. A crawl or schedule of another owner then returns 404, and lists are filtered.
* `POST /v1/crawls` accepts `Idempotency-Key`. A retry with the same body returns the original crawl with `Idempotent-Replayed: true`; reusing a key with a different body returns 422.

### 2.2 Endpoints

| Method & path | Description |
|---|---|
| `GET /v1/info` | Capabilities: proxy pools, routes, regions, fingerprints, formats, field types, selector prefixes, events, LLM, documents, search provider, scrape cache, plugins, default settings |
| `GET /v1/workers` | Live workers and this process's runs |
| `POST /v1/crawls` | Create a crawl ([body](#23-example-create-a-crawl)); queued unless `"start": false` |
| `POST /v1/batch/scrape` | Create a batch job: `{"urls": [...], "owner_ref", "external_id", "priority", "region", "settings", "limits", "webhook", "start"}`; accepts `Idempotency-Key`. Returns the job (`kind: "batch"`); read it with the `/v1/crawls/{id}` routes |
| `GET /v1/crawls?owner_ref=&external_id=&schedule_id=&status=queued,running&limit=&cursor=` | List, newest first |
| `GET /v1/crawls/{id}` | Detail with live progress (`live`) and change counts |
| `PATCH /v1/crawls/{id}` | Edit a crawl that is not running: `settings`, `priority`, `region`, `limits`, `webhook` |
| `POST /v1/crawls/{id}/start\|pause\|resume\|cancel\|duplicate` | Control |
| `POST /v1/crawls/{id}/after-job` `{"plugin": "…"}` | Run the job's after-job plugin again (operator only: `403 forbidden` unless `plugins.allow_in_api`). `GET /v1/crawls/{id}` reports plugin runs in `plugins` |
| `DELETE /v1/crawls/{id}` | Delete a crawl and all its data and artifacts (running crawls are cancelled first) |
| `GET /v1/crawls/{id}/pages?cursor=&limit=&content=1&body=1&successful=1&failed=1&order=desc` | Pages; `content=1` adds Markdown/metadata/extracted data, `failed=1` only errors, `order=desc` newest first |
| `GET /v1/crawls/{id}/pages/{pageId}` | One page with body and content |
| `GET /v1/crawls/{id}/pages/{pageId}/screenshot` | PNG |
| `GET /v1/crawls/{id}/changes?type=new\|changed\|removed&diff=1&cursor=` | Change detection results and counts |
| `GET /v1/crawls/{id}/export?format=csv\|json\|jsonl&successful=1` | Streamed export. CSV has a UTF-8 BOM (opens correctly in Excel) and flattens extracted fields into `extracted.<name>` columns |
| `GET /v1/crawls/{id}/webhooks` · `POST /v1/webhooks/{deliveryId}/redeliver` | Delivery log / redelivery |
| `POST /v1/scrape?timeout=120` | Synchronous single-page scrape: `{"url": "…", "settings": {…}, "max_age": 0, "store_in_cache": true}`. HTML, PDF and DOCX. Answers include `cached`/`cached_at` on cache hits |
| `POST /v1/map` | `{"url", "search", "limit", "include_subdomains", "ignore_sitemap", "settings"}` |
| `POST /v1/search?timeout=120` | `{"query", "limit", "lang", "country", "scrape", "settings", "max_age"}` → `{query, provider, results: [{url, title, description, position, scrape?, error?}], usage}` |
| `POST/GET /v1/schedules`, `GET/PUT/DELETE /v1/schedules/{id}`, `POST /v1/schedules/{id}/run\|enable\|disable` | Schedules (`{"name", "owner_ref", "cron", "timezone", "enabled", "change_detection", "skip_if_running", "template": {"kind", "target_url", "seed_urls", "max_depth", "settings", "limits", "webhook", "region", "priority"}, "source_job_id"}`). `kind: "batch"` replays a URL list (`target_url` plus `seed_urls`) |
| `GET /v1/cron/preview?cron=&timezone=&count=` | Validates a cron expression: `{"description", "next": [RFC3339…]}`, `400` when invalid |
| `GET /v1/owners/{ref}/usage?from=2026-10-01&to=2026-11-01` | Summed usage for billing |
| `GET/PUT/DELETE /v1/owners/{ref}/limits` | The owner's concurrency limit: `{"max_running": n}` (0 = unlimited). `GET` answers `{owner_ref, max_running, source: "owner"\|"default"}` |
| `DELETE /v1/owners/{ref}` | Delete every crawl, schedule and artifact of an owner |

The web UI calls the same handlers under `/api/v1`, authenticated with the UI password.

### 2.3 Example: create a crawl

```bash
curl -X POST http://localhost:5050/v1/crawls \
  -H "Authorization: Bearer $MOLE_TOKEN" -H "Idempotency-Key: crawl-7781" \
  -H "Content-Type: application/json" -d '{
  "url": "https://shop.example.ir/category/phones",
  "owner_ref": "team-42",
  "external_id": "laravel-crawl-7781",
  "max_depth": 3,
  "priority": 5,
  "region": "auto",
  "settings": {
    "url_include_patterns": ["/product/"],
    "headless_fallback": true,
    "formats": ["markdown", "metadata"],
    "only_main_content": true,
    "extract_url_match": "/product/",
    "extract_fields": [
      {"name": "name",  "selector": "h1"},
      {"name": "price", "selector": ".price", "type": "price"}
    ]
  },
  "limits":  {"max_pages": 2000, "max_duration_seconds": 3600},
  "webhook": {"url": "https://app.example.com/webhooks/mole", "secret": "s3cret"}
}'
```

The response is `201` with the crawl (`status: "queued"`). Webhook secrets are always returned as `********`; send that value back unchanged to keep the stored secret.

The top-level `max_depth` limits link depth (0 = only the start URL or URLs); without it the engine's `crawler.max_depth` applies, and `settings.max_depth` overrides both.

### 2.4 Example: batch scrape for price monitoring

```bash
curl -X POST http://localhost:5050/v1/batch/scrape \
  -H "Authorization: Bearer $MOLE_TOKEN" -H "Idempotency-Key: batch-912" \
  -H "Content-Type: application/json" -d '{
  "urls": ["https://shop-a.ir/product/123", "https://shop-b.ir/p/galaxy-a55", "https://shop-c.ir/files/catalog.pdf"],
  "owner_ref": "team-42",
  "external_id": "laravel-batch-912",
  "region": "auto",
  "settings": {
    "formats": ["product", "markdown"],
    "headless_fallback": true,
    "extract_fields": [
      {"name": "price", "selector": ".product-price", "type": "price"}
    ]
  },
  "limits": {"max_pages": 3},
  "webhook": {"url": "https://app.example.com/webhooks/mole", "secret": "s3cret",
              "events": ["job.completed", "job.failed", "job.changes", "page.changed"]}
}'
```

Each page has `product` when detection finds one, and `extracted.price` from the selector. Keep `extract_fields` for the few shops detection misses. The same body as a schedule template (`"kind": "batch"`, `target_url` = the first URL, `seed_urls` = the rest, `"cron": "0 */6 * * *"`) re-checks the prices every six hours and sends `page.changed` with the changed `fields`.

### 2.5 Example: search and scrape

```bash
curl -X POST "http://localhost:5050/v1/search?timeout=120" \
  -H "Authorization: Bearer $MOLE_TOKEN" -H "X-Owner-Ref: team-42" \
  -d '{"query": "قیمت گلکسی A55", "limit": 5, "lang": "fa", "country": "ir",
       "scrape": true, "max_age": 86400, "settings": {"formats": ["markdown"], "only_main_content": true}}'
```

---

## 3. Webhooks

Mole writes events to an outbox table and delivers them with retries: exponential backoff from 10 s up to 1 h, for at most `webhooks.max_attempts` attempts. Deliveries survive restarts, and receivers must answer `2xx`.

| Event | When |
|---|---|
| `job.queued`, `job.started`, `job.paused`, `job.completed`, `job.failed`, `job.cancelled` | Lifecycle |
| `job.progress` | Every 30 s while running |
| `job.changes` | Run finished with new/changed/removed pages (`data.changes`) |
| `page.crawled` | Every page (high volume; opt in) |
| `page.changed` | Every new or changed page when change detection is on; `data.fields` lists the extracted fields whose value changed (`[{name, old, new}]`) |

With no `events` listed you get the lifecycle events plus `job.changes`; use `["*"]` for everything. Payload:

```json
{"id": "evt uuid", "type": "job.completed", "created_at": "…",
 "job": {"id": "…", "kind": "crawl", "external_id": "…", "owner_ref": "team-42", "status": "completed",
         "stop_reason": "frontier_exhausted", "crawled_urls": 812, "usage": {"requests": 840, "bytes": 51234567, "…": 0}},
 "data": {"stop_reason": "frontier_exhausted"}}
```

A `page.changed` payload for a price change:

```json
{"id": "…", "type": "page.changed", "created_at": "…",
 "job": {"id": "…", "kind": "batch", "schedule_id": "…", "owner_ref": "team-42", "status": "running", "…": "…"},
 "data": {"page_id": 81234, "url": "https://shop-b.ir/p/galaxy-a55", "change_type": "changed",
          "added": 0, "removed": 0, "base_job_id": "…",
          "fields": [{"name": "product.availability", "old": "in_stock", "new": "out_of_stock"},
                     {"name": "product.price",
                      "old": {"amount": 19900000, "currency": "IRT", "raw": "19900000 IRT"},
                      "new": {"amount": 21000000, "currency": "IRT", "raw": "21000000 IRT"}}]}}
```

Headers: `X-Mole-Event`, `X-Mole-Delivery` (event id; use it to de-duplicate), and `X-Mole-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<unix>.<raw body>")>`. Verifying in Laravel:

```php
[$t, $v1] = sscanf($request->header('X-Mole-Signature'), 't=%d,v1=%s');
$expected = hash_hmac('sha256', $t.'.'.$request->getContent(), config('services.mole.webhook_secret'));
abort_unless(hash_equals($expected, $v1) && abs(time() - $t) < 300, 401);
```
