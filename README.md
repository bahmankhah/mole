# Mole — crawling, scraping and change-monitoring engine

Mole crawls websites, renders JavaScript pages in a headless browser, turns web pages and PDF/DOCX documents into clean Markdown, extracts structured data (CSS / XPath / JSON-LD / LLM), detects changes between runs (in the text and in extracted values), searches the web and delivers results through a REST API, signed webhooks and CSV/JSON exports. It is built to be the engine behind a multi-tenant crawling service: a control plane (for example a Laravel app) owns users, plans and billing, and talks to Mole over the versioned `/v1` API.

It understands Persian content: prices in تومان/ریال with Persian digits and «هزار/میلیون», Jalali dates, Arabic/Persian letter variants. It also routes `.ir` sites and international sites through different proxies and workers.

---

## Contents

1. [Quick start](#quick-start)
2. [Concepts](#concepts)
3. [Architecture](#architecture)
4. [Command-line interface](#command-line-interface)
5. [HTTP API and webhooks](#http-api-and-webhooks) → [docs/laravel-integration.md](docs/laravel-integration.md); products built on it: [docs/products/](docs/products/README.md)
6. [Job settings reference](#job-settings-reference)
7. [Configuration](#configuration)
8. [Deployment](#deployment)
9. [Security](#security)
10. [Web UI](#web-ui)
11. [Search, phrases and recon tools](#search-phrases-and-recon-tools)
12. [Database tables](#database-tables)
13. [Development](#development)
14. [Upgrading from the single-job version](#upgrading-from-the-single-job-version)

---

## Quick start

Requirements: Go 1.22, MySQL 8, Python 3.10+ (for the headless browser), poppler-utils (`pdftotext`, for PDF conversion).

```bash
./plugins/setup_python.sh          # venv + Playwright + Chromium (needed for browser rendering)
make build                         # → bin/mole
./bin/mole migrate                 # create tables (serve/worker also migrate on start)
./bin/mole serve                   # web UI + API + worker on http://localhost:5050
./bin/mole doctor                  # check database, Python, browser, storage, LLM
```

Try it without a database:

```bash
./bin/mole scrape https://example.com                          # Markdown to stdout
./bin/mole scrape https://shop.ir/p/1 --extract fields.json -o json
./bin/mole map https://example.com --search /blog/
```

With Docker: `MOLE_API_TOKENS=secret docker compose up -d` (UI at `127.0.0.1:5050`, user `admin`).

---

## Concepts

| Term | Meaning |
|---|---|
| **Crawl job** | One crawl of a site: start URL(s), settings, limits, optional webhook. Status: `pending` → `queued` → `running` → `completed` / `failed` / `cancelled`, plus `paused`. |
| **Batch job** | A job of kind `batch` (`POST /v1/batch/scrape`, or a schedule template with `"kind": "batch"`). It fetches exactly a list of URLs: no link following and no robots.txt or sitemap discovery. It is queued, limited, metered and read like a crawl. |
| **Queue** | Starting a job sets it to `queued`. Any worker with a free slot claims it. Higher `priority` goes first, then older jobs. |
| **Worker** | A Mole process that runs jobs (`mole serve` or `mole worker`). Many workers can share one database. Each holds a *lease* on its running jobs and renews it every 5 s; if a worker dies, its jobs are requeued when the lease expires and continue from the saved frontier. |
| **Control node / joined node** | The `mole serve` instance that owns the configuration and the schema, and machines added to it with a join token (`mole join` or the install script). See [Adding worker nodes](#adding-worker-nodes). |
| **Worker state** | `active`, `cordoned` (finishes its jobs, takes no new ones) or `draining` (hands its running jobs to other workers). Set from the System page or `mole cluster cordon\|drain\|uncordon`; survives restarts. |
| **Owner ref** | An opaque id (e.g. a Laravel team id) set by the control plane. Mole uses it only for fairness (`worker.max_running_per_owner`, or the owner's own limit from `PUT /v1/owners/{ref}/limits`), usage reports, cache scoping and bulk deletion. |
| **Region** | Pins a job to workers started with `--region`, e.g. a worker with an Iranian IP for `.ir` sites. `"auto"` derives the region from `network.proxy_routes`. |
| **Limits** | Per-job caps on pages, requests, bytes, browser seconds, LLM tokens and duration. A job that reaches one stops cleanly with `stop_reason` such as `limit:max_pages`. |
| **Usage** | What a job consumed (requests, pages, bytes, browser ms, LLM tokens, screenshots, proxy requests, document pages). Control planes bill from it. |
| **Page content** | Per-page derived output: Markdown, HTML, text, links, metadata, extracted fields, LLM output, screenshot, browser action results. PDF and DOCX files are converted into Markdown, text and metadata as well. |
| **Schedule** | A cron expression plus a job template. Each run is compared with the previous run (change detection). |
| **Change** | `new`, `changed` or `removed` page compared with a baseline job. A page is `changed` when its main text changed (with a line diff) or when an extracted field changed value (`fields`, with the old and new value; prices compare by amount and currency, not by how they are written). |
| **Scrape cache** | Successful `/v1/scrape` results, reused by later requests that send `max_age`. Results of requests that send headers, a POST body, their own proxies or browser actions are only reused for the same owner. |

---

## Architecture

```
            Laravel / any control plane                  Browser (web UI)
                     │  /v1  (bearer token)                 │  /, /api, /api/v1 (basic auth)
┌────────────────────▼──────────────────────────────────────▼──────────────────┐
│ handlers  — v1 API, UI pages, auth                                           │
├──────────────────────────────────────────────────────────────────────────────┤
│ jobs.Manager — lifecycle, durable queue + leases, scheduler (cron), webhook   │
│                outbox, after-job plugins, exports, owners, idempotency        │
├──────────────────────────────────────────────────────────────────────────────┤
│ crawler.Engine (shared)              │ crawler.Run (one per running job)      │
│  netx: SSRF guard, proxy pools,      │  DB frontier, workers, robots,         │
│        uTLS fingerprints, host limit │  limits, lease renewal, control,       │
│  BrowserPool (long-lived Chromium)   │  page analysis, change detection       │
│  content: Markdown, metadata, PDF,   │                                        │
│   DOCX, CSS/XPath/JSON-LD, LLM       │  crawler.Scrape / crawler.Map (sync)   │
├──────────────────────────────────────────────────────────────────────────────┤
│ MySQL (jobs, frontier, pages, content, changes, schedules, outbox, cache)    │
│ Object storage: local disk or S3-compatible (screenshots, offloaded bodies)  │
└──────────────────────────────────────────────────────────────────────────────┘
```

| Package | Responsibility |
|---|---|
| `cmd/` | Cobra CLI (`serve`, `worker`, `scrape`, `map`, `crawl`, `jobs`, `schedules`, `owners`, `doctor`, …) |
| `app/` | Wires config, database, storage, engine and manager |
| `jobs/` | Job lifecycle, queue/leases, scheduler, webhooks, schedules, results/export, owners and their limits, scrape cache, search |
| `crawler/` | Shared engine, per-job runs, fetch strategy, page processing, scrape/map |
| `netx/` | SSRF guard, proxy selection/routing, TLS-fingerprinted HTTP/1.1 + HTTP/2 transport, per-host rate limiter |
| `content/` | Metadata, readability main content, HTML→Markdown, PDF/DOCX conversion, structured extraction (CSS, XPath, JSON-LD), extracted-value diff, LLM client |
| `search/` | Web search providers behind `/v1/search`: SearXNG, Brave Search API, Serper (Google results) |
| `modules/` | Frontier, link/sitemap/robots parsing, URL cleaning, phrases, Persian text and Jalali dates, text diff, browser pool, stemming, semantic search, recon scanners |
| `storage/` | Local and S3 object stores |
| `handlers/` | `/v1` API, UI handlers and pages, auth |
| `plugins/` | `headless_fetch.py` (browser server), embeddings, stemming, after-crawl/after-job plugins |

### Fetching strategy

1. Wait for the per-host rate limiter. It is shared by all jobs in the process, so two customers crawling the same site do not overload it together. `429`/`503` responses with `Retry-After` pause that host.
2. Choose a proxy: job proxies, then the job's pool, then routing rules (`.ir` → Iranian pool), then the default pool. A sticky proxy keeps one exit IP per site.
3. Fetch with **HTTP** using a browser TLS fingerprint (Chrome by default; HTTP/2 when the server supports it), or with the **headless browser** from a pool of long-lived Chromium processes. URLs ending in `.pdf`/`.docx` always use HTTP (a browser downloads files instead of rendering them), and a browser navigation that turns into a download is fetched again over HTTP.
4. With **browser fallback**, an HTTP response that looks like a CDN/WAF challenge (ArvanCloud, Cloudflare, openresty) or an unrendered SPA shell is fetched again in the browser.
5. Every destination, including redirects, is checked by the SSRF guard (see [Security](#security)).

**Documents.** A response that is a PDF (by Content-Type or by its `%PDF-` signature, so download links without an extension work) or a Word `.docx` file is converted to Markdown, text and metadata (title, author, dates, page count). PDFs go through poppler's `pdftotext`/`pdfinfo`; Persian text comes out in logical order without bidi control marks. DOCX is read directly (headings, lists, tables). At most `documents.max_pages` pages are converted per file. Files larger than `security.max_body_bytes` are cut off, and the page's `extract_error` says so. Crawls follow links to `.pdf`/`.docx` files only with `include_documents: true` (they are in the default `skip_extensions`). Usage counts converted pages in `document_pages`.

---

## Command-line interface

`mole --help` and `mole <command> --help` describe every flag. Global flags: `-c/--config`, `-o/--output table|json`, `-v/--verbose` (engine logs). Running `mole` without a command starts the server.

| Command | Purpose |
|---|---|
| `mole serve [--addr --no-ui --no-worker --region --max-jobs]` | Web UI, `/api`, `/v1` and a worker |
| `mole worker [--region --max-jobs --health-addr]` | Worker only (scale out; one per region) |
| `mole scrape <url>` | One page → Markdown/text/HTML/links/metadata/detected/extracted JSON. `--browser`, `--fallback`, `--main-only`, `-f markdown,links` (`-f product,article` for the detectors), `--extract fields.json`, `--actions actions.json`, `--screenshot shot.png --full-page`, `--llm-prompt`, `--proxy`, `--fingerprint`, `-H "Name: value"`, `--settings settings.json` |
| `mole map <url>` | URLs from robots.txt, sitemaps and the start page. `--search`, `--limit`, `--subdomains`, `--no-sitemap` |
| `mole crawl <url>` | Crawl and wait (Ctrl+C pauses). `-d`, `--max-pages`, `--max-duration`, `-f`, `--include/--exclude`, `--extract`, `--compare-with <job>`, `--owner`, `--priority`, `--region`, `--webhook`, `--export csv --out file`, `--detach` |
| `mole jobs list/get/watch/pages/changes/export` | Inspect jobs (`export --format csv\|json\|jsonl`) |
| `mole jobs start/pause/resume/cancel/delete/duplicate <id>…` | Control jobs from any machine; the owning worker applies the change within seconds |
| `mole schedules list/get/create/run/enable/disable/delete/next` | Recurring crawls (`create <url> --cron "0 */6 * * *" --tz Asia/Tehran`) |
| `mole owners usage <ref> [--from --to]` / `owners delete <ref>` | Billing data / account deletion |
| `mole join <control-url> --token mjt_… [--id --region --max-jobs --browser-pool --no-browser]` | Make this machine a worker of a control node (writes its config, checks database and storage) |
| `mole cluster token create\|list\|revoke`, `mole cluster nodes`, `mole cluster cordon\|drain\|uncordon <worker-id>` | Join tokens and worker nodes |
| `mole migrate`, `mole doctor` (also checks PDF support and the search provider), `mole config show\|validate`, `mole plugins list`, `mole version`, `mole completion bash` | Operations |

Extraction schema file (`--extract fields.json`):

```json
[
  {"name": "title", "selector": "h1"},
  {"name": "price", "selector": "jsonld:Product.offers.price", "type": "price",
   "fallback_selectors": [".price", "meta[property='product:price:amount']"]},
  {"name": "image", "selector": "img.main", "type": "url"},
  {"name": "date",  "selector": "xpath://time/@datetime", "type": "date"},
  {"name": "specs", "selector": "table.specs tr", "type": "list",
   "fields": [{"name": "key", "selector": "th"}, {"name": "value", "selector": "td"}]}
]
```

---

## HTTP API and webhooks

The `/v1` API reference, the webhook events and their signature, and the requirements for the Laravel control plane are in **[docs/laravel-integration.md](docs/laravel-integration.md)**. The web UI calls the same handlers under `/api/v1`, authenticated with the UI password.

---

## Job settings reference

All settings are optional; empty means "use the global default from `config.yaml`". They are the same in the API, the UI Settings tab, `--settings` files and schedule templates.

| Group | Fields |
|---|---|
| Crawl | `max_concurrent_requests`, `request_timeout_seconds`, `politeness_delay_ms` (±1 s jitter), `max_depth`, `max_pages`, `max_retries`, `user_agent`, `respect_robots_txt`, `skip_extensions`, `url_include_patterns`, `url_exclude_patterns`, `extra_tracking_params`, `skip_content_duplicates` |
| Request | `request_method` (`POST` for seed URLs), `request_body` with `{{VAR}}` placeholders, `request_body_vars`, `request_headers` |
| Network | `proxy_pool` (`""`/`auto`/`direct`/pool name), `proxies` (URLs), `sticky_proxy`, `tls_fingerprint` (`chrome`, `firefox`, `safari`, `edge`, `ios`, `go`), `host_rate_limit` (req/s, can only slow down) |
| Browser | `use_headless_browser`, `headless_fallback`, `headless_wait_selector`, `headless_render_wait`, `block_resources` (`image`, `font`, `media`, `stylesheet`), `screenshot`, `full_page_screenshot`, `browser_actions` |
| Output | `formats`: content (`markdown`, `html`, `text`, `links`, `metadata`) and detectors (`product`, `article`, `business`, `job_posting`, `event`, `faq`), `only_main_content`, `save_text_content`, `include_documents` (follow links to PDF/DOCX files) |
| Extraction | `extract_fields`, `extract_url_match` (regex; also limits the detectors), `llm_prompt`, `llm_schema` |
| Matching | `phrases` (job-specific; otherwise the global phrase list), `enable_word_extraction`, `use_crawl_phrases_only`, `enable_stemming`, `enable_lemmatization`, `default_language`, `enable_semantic_search` |
| Monitoring | `change_detection`, `compare_with_job_id` (schedules compare with their previous run automatically) |
| Plugins | `after_crawl_plugin`, `after_job_plugin` (refused for `/v1` clients unless `plugins.allow_in_api`) |

**Extraction field types:** `text` (default), `html` (outer HTML), `attr` (needs `attr`), `url` (absolute; `href`/`src` by default), `number`, `price` (`{"amount", "currency": "IRT|IRR|USD|…", "raw"}`), `date` (RFC3339; reads `datetime`/`content` attributes, ISO and Jalali dates), `exists` (bool), `list` (an array of objects built from `fields`, relative to each match). `multiple: true` returns every match; `regex` keeps the first capture group. `fallback_selectors` (up to 5) are tried in order while the field is still empty (null, an empty list, or `false` for `exists`).

**Selectors** are CSS, XPath prefixed with `xpath:`, or JSON-LD prefixed with `jsonld:`. A JSON-LD selector reads `<script type="application/ld+json">` blocks: `jsonld:Product.offers.price` finds every object whose `@type` is `Product` (at any depth, including inside `@graph`; `*` matches any type) and follows the keys after it, expanding arrays on the way. With type `price`, the offer's `priceCurrency` is used as the currency. Most shops publish this data (WooCommerce does by default), so `jsonld:` selectors keep working when a theme changes. They cannot be used with types `html`, `attr` and `list`.

**Browser actions:** `{"type": "click|type|press|scroll|wait|wait_for_selector|evaluate", "selector", "value", "ms", "count", "script"}`. For example, `{"type": "scroll", "value": "bottom", "count": 5}` loads lazy content, and `evaluate` results are saved with the page.

**Metadata** includes title, description, language, direction, canonical, favicon, Open Graph/Twitter tags, `published_at`, JSON-LD and word count.

**Detectors** are output formats that need no selectors. Each one reads the structured data sites publish for search engines and social networks: JSON-LD (including `@graph` and `@id` references), microdata, RDFa and meta tags. Each result appears under its format name, for example `product` or `article`, on scrapes, on crawl pages (`content.product`), in exports and in change detection. `sources` says where each field came from (`json_ld`, `microdata`, `rdfa`, `opengraph`, `meta`, `html`, or a shop platform). A page without such data returns nothing for that format. Every detector applies these rules:

* Items inside lists (related products, category listings, search results) are ignored.
* When a page describes several different items, the one whose `url`, `@id` or `mainEntityOfPage` is the page wins. If none matches, the page counts as a listing and returns nothing.
* Dates are RFC3339, and Jalali dates are converted.
* Absent fields are omitted.

| Format | Returns |
|---|---|
| `product` | `name`, `description`, `url`, `image`, `images`, `brand`, `sku`, `gtin`, `mpn`, `category`, `breadcrumbs`, `price`, `regular_price`, `discount_percent`, `low_price`/`high_price`/`offer_count` (variants or several offers), `availability` (`in_stock`, `out_of_stock`, `preorder`, `backorder`, `limited`, `discontinued`), `condition`, `seller`, `rating`, `best_rating`, `review_count` |
| `article` | `headline`, `description`, `type` (`NewsArticle`, `BlogPosting`, …), `authors`, `publisher`, `published_at`, `modified_at`, `section`, `keywords`, `image`, `url`, `language`, `word_count` |
| `business` | `name`, `legal_name`, `type` (`Organization`, `Dentist`, `Restaurant`, …), `description`, `url`, `logo`, `image`, `telephones`, `emails`, `address` (`street`, `locality`, `region`, `postal_code`, `country`, `text`), `geo`, `opening_hours`, `price_range`, `rating`, `review_count`, `social_links` |
| `job_posting` | `title`, `description`, `organization`, `organization_url`, `location`, `remote`, `employment_types`, `date_posted`, `valid_through`, `salary` (`min`, `max`, `currency`, `unit`), `experience`, `education`, `identifier`, `url` |
| `event` | `name`, `description`, `type`, `start_date`, `end_date`, `status` (`scheduled`, `cancelled`, `postponed`, `rescheduled`, `moved_online`), `attendance_mode` (`offline`, `online`, `mixed`), `venue`, `address`, `online_url`, `organizer`, `performers`, `image`, `url`, `price` (cheapest ticket), `availability` |
| `faq` | A list of `{"question", "answer"}` from a `FAQPage` (up to 100) |

A product page, for example:

```json
{"name": "گوشی سامسونگ A55", "price": {"amount": 12500000, "currency": "IRT", "raw": "12500000 IRT"},
 "regular_price": {"amount": 15000000, "currency": "IRT", "raw": "۱۵٬۰۰۰٬۰۰۰ تومان"}, "discount_percent": 17,
 "availability": "in_stock", "sku": "A55-128", "gtin": "8806095467528", "brand": "Samsung",
 "image": "https://shop.ir/a55.jpg", "images": ["https://shop.ir/a55.jpg"], "category": "موبایل",
 "breadcrumbs": ["خانه", "موبایل", "گوشی سامسونگ A55"], "seller": "فروشگاه نمونه", "rating": 4.6, "best_rating": 5,
 "review_count": 37, "sources": {"price": "json_ld", "regular_price": "woocommerce", "...": "..."}}
```

Rules specific to some detectors:

* **Product:** after the structured data and OpenGraph/Facebook product tags, it also reads the HTML of the default WooCommerce, Magento, PrestaShop and OpenCart product templates. That HTML often holds the price before a discount. A regular price is kept only if it is in the same currency as the price and higher than it. A price of 0 counts as "call for price" and is omitted.
* **Article:** OpenGraph `article:*` tags and the classic `author`, `pubdate` and `keywords` meta tags fill gaps, so Persian news sites without JSON-LD are covered too.
* **Business:** contact details come only from what a business publishes about itself, as schema.org data or Facebook `business:contact_data` tags. Phone numbers and emails are never scraped from page text, so private people's details are not collected. A product's brand, an article's author, a job's employer and an event's organizer are not treated as the page's business. When there are several businesses, the one with the most contact details wins: a branch beats the site-wide Organization.
* **Job posting, event and FAQ:** these read schema.org data only, which job boards, ticket sites and SEO plugins publish.

With change detection, changes are reported per field, for example `product.price`, `article.modified_at` or `event.status`, next to the extracted fields. `sources` is not compared, and the FAQ is compared as a whole (`faq`). CSV exports add `<format>.<field>` columns. For sites whose markup has none of this data, use `extract_fields`; both can run in the same job. `GET /v1/info` lists the detectors with their labels and main fields (`detectors`).

---

## Configuration

`config.yaml` (or `$CONFIG_PATH` / `-c`). Every section has defaults; `mole config show` prints the effective configuration with secrets redacted.

| Section | Key settings |
|---|---|
| `database` | MySQL connection (env: `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`) |
| `server` | `host`, `port` (env: `SERVER_HOST`, `SERVER_PORT`) |
| `crawler` | Global crawl defaults (see job settings) and Python paths |
| `api.tokens` | Bearer tokens for `/v1` (env `MOLE_API_TOKENS`, comma-separated) |
| `ui` | `enabled`, `username`, `password` (env `MOLE_UI_USERNAME`, `MOLE_UI_PASSWORD`) |
| `worker` | `id` (env `MOLE_WORKER_ID`), `region` (env `MOLE_WORKER_REGION`), `max_running_jobs`, `max_running_per_owner`, `lease_ttl`, `poll_interval`, `browser` (empty = detect at start; env `MOLE_WORKER_BROWSER`) |
| `security` | `allow_private_networks` (env `MOLE_ALLOW_PRIVATE_NETWORKS`), `allowed_cidrs`, `max_body_bytes` |
| `network` | `tls_fingerprint`, `host_rate_limit`, `proxy_pools`, `proxy_routes` (`{match, pool, region}`), `default_proxy_pool` |
| `headless` | `pool_size` — Chromium processes per worker |
| `storage` | `driver: local\|s3`, `local_dir`, `s3.{endpoint,bucket,access_key,secret_key,use_ssl,region,prefix}`, `offload_bodies` |
| `llm` | OpenAI-compatible `base_url`, `api_key`, `model`, `timeout`, `max_input_chars` (env `MOLE_LLM_*`). Works with self-hosted models and local gateways |
| `documents` | `max_pages` (PDF pages converted per file, default 500), `timeout` (per file, default 60s) |
| `cache` | Scrape cache: `enabled` (default true), `ttl` (entries are deleted after this, default 168h), `max_entry_bytes` (larger compressed results are not stored, default 8 MiB) |
| `search` | `/v1/search` provider: `provider` (`""` disables it, `searxng`, `brave`, `serper`), `base_url` (the SearXNG instance; optional endpoint override for the others), `api_key` (env `MOLE_SEARCH_API_KEY`), `proxy` (for provider requests, e.g. a foreign exit), `timeout`, `default_lang`, `default_country`. Joined nodes do not receive these settings |
| `webhooks` | `timeout`, `max_attempts`, `allow_private` |
| `plugins` | `allow_in_api`, `after_job_delay`, `timeout` |
| `cluster` | Control node: `advertise_url`, `node_database_host`, `node_database_port`, `node_s3_endpoint`, `allow_local_storage`. Joined node (written by `mole join`): `control_url` (env `MOLE_CONTROL_URL`), `join_token` (env `MOLE_JOIN_TOKEN`), `node.{id,region,max_running_jobs,browser_pool_size,browser}` |

Iran-oriented proxy routing example:

```yaml
worker: {region: ir}
network:
  proxy_pools:
    ir:   ["http://user:pass@10.0.0.5:3128"]
    intl: ["socks5://user:pass@de.example.net:1080"]
  proxy_routes:
    - {match: ".ir", pool: ir, region: ir}
    - {match: "*",  pool: intl}
security:
  allowed_cidrs: ["10.0.0.5/32"]   # only needed if a proxy or webhook receiver is on a private network
```

---

## Deployment

* **Single machine:** `mole serve` behind a reverse proxy that terminates TLS. Set `api.tokens`, a UI password, and keep `allow_private_networks: false`.
* **Scale out:** add machines as workers with a join token; see [Adding worker nodes](#adding-worker-nodes). (Workers you configure by hand against the same MySQL still work: give each a stable `worker.id` and use `storage.driver: s3`.)
* **Regions:** start Iranian-IP workers with `--region ir` and foreign ones with `--region intl`. Jobs with `"region": "auto"` follow `network.proxy_routes`. A job pinned to a region with no running worker stays queued; the System page shows this.
* **API-only engine:** `mole serve --no-ui` for an instance that only serves the control plane.
* **Docker:** see `Dockerfile` (Go binary plus Python/Playwright/Chromium and poppler-utils) and `docker-compose.yml`. Add workers with `docker compose --profile scale up -d --scale worker=3`. Set `INSTALL_ALL_PLUGINS=true` at build time for semantic search and stemming.
* **Graceful shutdown:** `SIGTERM` stops runs and puts their jobs back in the queue. Nothing is lost, and in-flight pages are re-fetched.

### Adding worker nodes

The `mole serve` instance is the **control node**. Other machines join it with a **join token** and then run as workers: they get the control node's configuration (database, storage, proxies, LLM, crawler defaults, plugins) and its binary, so you configure the cluster in one place.

**Before the first node joins**

1. **Nodes must reach MySQL.** Use a private network or a VPN (WireGuard, Tailscale) or a firewall rule for each node's IP; never open MySQL to the internet. If the control node connects to `localhost`, set `cluster.node_database_host` (and `node_database_port`) to the address nodes use.
2. **Shared object storage.** Screenshots and offloaded page bodies must be readable by every machine, so the control node refuses to hand out a configuration with `storage.driver: local`. Use `s3` (MinIO, ArvanCloud, …); set `cluster.node_s3_endpoint` if nodes reach it at another address. `cluster.allow_local_storage: true` overrides the check when you knowingly run without screenshots and offloading.
3. **Nodes must reach the control node**, over HTTPS or a private/VPN address (the answer contains secrets; `mole join` refuses plain HTTP to public addresses unless `--allow-http`). Set `cluster.advertise_url` to that URL so the commands shown to you are right.
4. Build the control binary with `make build`. It is static (`CGO_ENABLED=0`), so nodes with the same CPU architecture (linux/amd64 or linux/arm64) can run it.

**Add a node**

Create a token on the System page (**Add a node**) or with `mole cluster token create --name tehran-2 --region ir --ttl 24h`. Both print ready-to-run commands. On a new Linux server:

```bash
curl -fsSL https://mole.example.com/cluster/install.sh | sudo sh -s -- --token mjt_… [--region ir] [--max-jobs 8] [--no-browser]
```

The script installs the control node's binary and plugins in `/opt/mole`, poppler-utils for PDF conversion, the headless browser (Python venv, Playwright, Chromium; skip it with `--no-browser`), runs `mole join`, and starts the systemd service `mole-worker` as user `mole`. The node shows up on the System page within 15 seconds. **Run the same command again to upgrade the node** after you upgrade the control node. Behind a filtered network, pass mirrors through: `sudo env PIP_INDEX_URL=… PLAYWRIGHT_DOWNLOAD_HOST=… sh -s -- --token …`.

Other ways to add a node:

* Mole already installed: `mole join https://mole.example.com --token mjt_…`, then `mole worker`.
* Docker (image built from this repository at the control node's version): `docker run -d -e MOLE_CONTROL_URL=https://mole.example.com -e MOLE_JOIN_TOKEN=mjt_… mole:latest worker`.

**How a joined node behaves**

* Its config file is managed by the control node and refreshed each time `mole worker` starts. The node's own values live under `cluster.node` (set them with `mole join` flags; re-running `join` keeps them). Its region comes from the token unless you pass `--region`. Environment variables such as `DB_HOST` still override the file.
* If the control node is unreachable at start, the node starts with the configuration from its last sync. If the token was revoked or expired, it refuses to start until it joins again with a valid token.
* It never migrates the database (the control node does) and refuses to start when its version differs from the control node's (`--allow-version-mismatch` overrides). The System page marks workers that run another version.
* It checks at start whether Playwright and Chromium work. A worker without a browser never claims jobs that use the headless browser or the browser fallback (including through the global defaults); they wait for a worker that has one. Set `worker.browser` (or `MOLE_WORKER_BROWSER`) to skip the check.
* Worker ids must be unique: a worker refuses to start when another host used its id in the last 45 seconds. New nodes get the host name plus a random suffix.
* `max_running_per_owner` is enforced for the whole cluster under a database lock, so two workers never take an owner's last slot together.

**Operate nodes**

| Task | Command (or the System page) |
|---|---|
| List workers with state, browser and version | `mole cluster nodes` |
| Stop a worker from taking new jobs (running jobs finish), e.g. before maintenance | `mole cluster cordon <worker-id>` |
| Move a worker's running jobs to other workers and stop it taking new ones | `mole cluster drain <worker-id>` |
| Put it back to work | `mole cluster uncordon <worker-id>` |
| List or revoke join tokens | `mole cluster token list`, `mole cluster token revoke <id>` |
| Remove a node | `drain` it, then `systemctl disable --now mole-worker` on the node |

---

## Security

* **SSRF protection:** user-supplied URLs, redirects and webhook targets may not reach loopback, RFC1918, link-local (cloud metadata), CGNAT, multicast or reserved ranges. For HTTP fetches the check runs on the resolved IP at connect time, which closes the DNS-rebinding gap. In the headless browser, every request is checked by hostname/IP before it is sent and the final URL is re-checked after navigation (DNS rebinding inside the browser is not covered). Exceptions go in `security.allowed_cidrs`. Turn on `allow_private_networks` only for local, single-user use.
* `/v1` requires a bearer token once `api.tokens` is set; the UI and `/api` use HTTP basic auth when `ui.username` is set. Both compare credentials in constant time.
* Webhooks are signed with HMAC-SHA256 and include a timestamp; secrets are never returned by the API.
* Python plugins execute code on workers, so `/v1` clients cannot select them unless `plugins.allow_in_api` is on.
* Port scanning and subdomain discovery are UI-only admin tools and are not exposed in `/v1`.
* **Scrape cache scoping:** a cached result is shared between owners only when the request sent no `request_headers`, no POST method/body, no `proxies` and no `browser_actions` (these can carry credentials or return a customer's private pages). Other entries are keyed by `X-Owner-Ref` and deleted with the owner.
* **Documents** are converted by `pdftotext` in a separate process with a timeout and a page cap; DOCX parts are read with a size cap, so a small compressed file cannot expand without bound.
* **Join tokens** let a machine fetch the cluster configuration, including the database password and other secrets, plus the plugins with their `.env` files. Only the token's SHA-256 is stored; give tokens a short validity and revoke them when the nodes have joined. The `/cluster/*` endpoints accept join tokens only (not API tokens); `/cluster/install.sh` itself contains no secrets.

---

## Web UI

| Page | What it is for |
|---|---|
| **Dashboard** | Running/queued counts, throughput, workers, live runs with progress, create a crawl |
| **Crawls** | All jobs with status, owner, schedule, region, usage |
| **Crawl detail** | Live metrics; tabs for phrase matches, **Pages & Data** (every page with status, Markdown and extracted fields; filters, live refresh, CSV/JSON export), extracted words, **Changes** (diffs), **Webhooks** (delivery log, redeliver) and **Settings** (every option in this README); **🗓 Schedule** turns the crawl into a schedule with the same settings |
| **Page detail** | Stored body plus Markdown, extracted data, LLM output, metadata, links, screenshot, browser action results and this page's change diff |
| **Scrape & Map** | Playground for `/v1/scrape` and `/v1/map`; "Crawl whole site" turns the same settings into a job |
| **Schedules** | Schedules with their cadence in words, next runs, run history with change counts, run now, enable/disable. A schedule's page has the full crawl settings (the same form as a crawl) plus a Schedule section: crawl a site or check a list of pages, a frequency picker (hourly to monthly, or a cron expression) with a live preview of the next runs, the time zone, and run behaviour |
| **System** | Workers with state, browser and version (cordon, drain, uncordon), **Add a node** (join tokens and install commands), live runs, queue, capabilities, security warnings, API quick start |
| Search, Phrases, Subdomains, Ports | Keyword/semantic search over crawled content and the recon tools |

---

## Search, phrases and recon tools

* **Phrase detection:** global or per-job phrases are matched in page text, URLs and anchor texts. Matching normalizes Persian/Arabic letters and digits, so `كتاب` matches `کتاب` and `۱۴۰۲` matches `1402`.
* **Word extraction:** an optional per-crawl inverted index with Persian/English stemming and lemmatization (hazm/NLTK via `plugins/stem_lemma.py`).
* **Keyword search:** TF-IDF ranking over phrase matches with n-gram expansion, exact-match and URL/anchor boosts.
* **Semantic search:** multilingual sentence-transformer embeddings with a FAISS index (`plugins/semantic_embed.py`, optional).
* **Subdomain discovery** (DNS) and **port scanning** (TCP with banners and risk notes) are kept as admin tools.

Python setup for the browser, embeddings and stemming:

```bash
./plugins/setup_python.sh                       # or manually:
python3 -m venv plugins/.venv && plugins/.venv/bin/pip install -r plugins/requirements.txt
plugins/.venv/bin/python -m playwright install chromium
```

Mole finds `plugins/.venv/bin/python3` automatically. `plugins/headless_fetch.py --serve` is the long-lived browser server the engine pools; it also still works as a one-shot CLI.

### Plugins

Operator-written Python hooks live in `plugins/<id>/`:

| File | Purpose |
|---|---|
| `plugin.json` | `name`, `description`, `version`, optional `after_crawl`/`after_job` file names and `env` (the variables the plugin requires) |
| `after_crawl.py` | Runs for every fetched page; receives `{url, status_code, content_type, depth, body, job_id}` as JSON on stdin; limited by `plugins.timeout`. At most as many run at once as the job has parallel requests. |
| `after_job.py` | Runs once, `plugins.after_job_delay` after the job completes or is cancelled, with `--job-id`; 30-minute limit. Output is appended to `after_job.log` and the last 8 KB are shown on the job page. |
| `.env` | Secrets and settings as `KEY=VALUE` (gitignored; commit a `.env.example`). Passed to every run of the plugin. |

Every run gets `MOLE_PLUGIN_ID`, `MOLE_PLUGIN_DIR`, `MOLE_PLUGIN_HOOK` and `MOLE_JOB_ID`; the engine's own secrets (`DB_PASSWORD`, `MOLE_API_TOKENS`, …) are removed from the environment.

* **Same machine:** plugins often keep per-page files on disk, so the after-job run goes to the worker that ran the crawl; another worker takes it only if that worker is gone. An interrupted run is marked failed after 45 minutes.
* **Visibility:** the job page's **Plugins** tab shows after-crawl runs, failures and the last error, plus the after-job state and output, with **Run now**.
* **Commands:** `mole plugins list` shows hooks and missing environment variables, `mole plugins run-after-job <job-id> [--plugin id]` runs it in the terminal, and `mole doctor` checks every plugin's environment.
* **API:** `POST /v1/crawls/{id}/after-job` (needs `plugins.allow_in_api`).
* **Not for customers:** plugins execute arbitrary code, so `/v1` clients cannot select or run them unless `plugins.allow_in_api` is on.

---

## Database tables

`crawl_jobs` (job, kind, settings, limits, webhook, usage, queue/lease state), `frontier_urls`, `crawled_pages`, `page_contents`, `page_changes` (text diff and changed fields), `schedules`, `webhook_deliveries` (outbox), `idempotency_keys`, `worker_heartbeats`, `worker_controls` (cordon/drain state), `join_tokens`, `owner_limits`, `scrape_cache_entries`, `phrase_matches`, `search_phrases`, `page_embeddings`, `discovery_jobs`, `subdomains`, `port_scan_jobs`, `discovered_ports`. Migrations run automatically on `serve`/`worker` (and with `mole migrate`), one process at a time under a MySQL named lock. Joined nodes never migrate.

---

## Development

```bash
make build          # bin/mole
make test           # unit tests
make test-integration   # live TLS-fingerprint check and real Chromium browser-pool test
make lint           # gofmt + go vet
MOLE_SQL_DEBUG=1 ./bin/mole serve -v   # log every SQL query and HTTP request
```

The module targets Go 1.22 (`GOTOOLCHAIN=local` in the Makefile). Dependencies are pinned to versions that build on 1.22.

---

## Upgrading from the single-job version

* **Several jobs now run at once** (`worker.max_running_jobs`). "Start" queues a job instead of running it immediately, and pause, resume and stop take a job id.
* **Restarts:** jobs that were running continue automatically after a restart; they used to be parked as paused.
* **Private targets are blocked by default.** To crawl `localhost`/LAN targets, set `security.allow_private_networks: true` or list them in `security.allowed_cidrs`.
* **Per-host politeness:** `network.host_rate_limit` defaults to 2 requests/s per host across all jobs. Raise it, or set it to 0, for your own sites.
* **Headless rendering** goes through `headless.pool_size` long-lived browsers instead of one process per page, so it is much faster.
* **Logging:** SQL statements are no longer logged by default (`MOLE_SQL_DEBUG=1` restores it).
* **Binary:** the binary is `mole` (`make build` → `bin/mole`). `go run .` still starts the server.
* **`max_depth` is honoured:** the top-level `max_depth` of `/v1/crawls`, `mole crawl -d` and schedule templates used to be stored but ignored (runs used `crawler.max_depth` unless `settings.max_depth` was set). Runs now use it; `settings.max_depth` still wins. A crawl without `max_depth` uses `crawler.max_depth` as before. Links that would sit beyond the maximum depth are no longer queued.
* **PDF support** needs poppler-utils on every worker (`apt install poppler-utils`); `mole doctor` checks it.
