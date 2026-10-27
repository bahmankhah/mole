# Mole — crawling, scraping and change-monitoring engine

Mole crawls websites, renders JavaScript pages in a headless browser, turns pages into clean Markdown, extracts structured data (CSS / XPath / LLM), detects changes between runs and delivers results through a REST API, signed webhooks and CSV/JSON exports. It is built to be the engine behind a multi-tenant crawling service: a control plane (for example a Laravel app) owns users, plans and billing, and talks to Mole over the versioned `/v1` API.

It understands Persian content: prices in تومان/ریال with Persian digits and «هزار/میلیون», Jalali dates, Arabic/Persian letter variants. It also routes `.ir` sites and international sites through different proxies and workers.

---

## Contents

1. [Quick start](#quick-start)
2. [Concepts](#concepts)
3. [Architecture](#architecture)
4. [Command-line interface](#command-line-interface)
5. [HTTP API and webhooks](#http-api-and-webhooks) → [docs/laravel-integration.md](docs/laravel-integration.md)
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

Requirements: Go 1.22, MySQL 8, Python 3.10+ (for the headless browser).

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
| **Queue** | Starting a job sets it to `queued`. Any worker with a free slot claims it. Higher `priority` goes first, then older jobs. |
| **Worker** | A Mole process that runs jobs (`mole serve` or `mole worker`). Many workers can share one database. Each holds a *lease* on its running jobs and renews it every 5 s; if a worker dies, its jobs are requeued when the lease expires and continue from the saved frontier. |
| **Owner ref** | An opaque id (e.g. a Laravel team id) set by the control plane. Mole uses it only for fairness (`worker.max_running_per_owner`), usage reports and bulk deletion. |
| **Region** | Pins a job to workers started with `--region`, e.g. a worker with an Iranian IP for `.ir` sites. `"auto"` derives the region from `network.proxy_routes`. |
| **Limits** | Per-job caps on pages, requests, bytes, browser seconds, LLM tokens and duration. A job that reaches one stops cleanly with `stop_reason` such as `limit:max_pages`. |
| **Usage** | What a job consumed (requests, pages, bytes, browser ms, LLM tokens, screenshots, proxy requests). Control planes bill from it. |
| **Page content** | Per-page derived output: Markdown, HTML, text, links, metadata, extracted fields, LLM output, screenshot, browser action results. |
| **Schedule** | A cron expression plus a job template. Each run is compared with the previous run (change detection). |
| **Change** | `new`, `changed` (with a line diff) or `removed` page compared with a baseline job. |

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
│  content: Markdown, metadata,        │                                        │
│           CSS/XPath extraction, LLM  │  crawler.Scrape / crawler.Map (sync)   │
├──────────────────────────────────────────────────────────────────────────────┤
│ MySQL (jobs, frontier, pages, content, changes, schedules, outbox)           │
│ Object storage: local disk or S3-compatible (screenshots, offloaded bodies)  │
└──────────────────────────────────────────────────────────────────────────────┘
```

| Package | Responsibility |
|---|---|
| `cmd/` | Cobra CLI (`serve`, `worker`, `scrape`, `map`, `crawl`, `jobs`, `schedules`, `owners`, `doctor`, …) |
| `app/` | Wires config, database, storage, engine and manager |
| `jobs/` | Job lifecycle, queue/leases, scheduler, webhooks, schedules, results/export, owners |
| `crawler/` | Shared engine, per-job runs, fetch strategy, page processing, scrape/map |
| `netx/` | SSRF guard, proxy selection/routing, TLS-fingerprinted HTTP/1.1 + HTTP/2 transport, per-host rate limiter |
| `content/` | Metadata, readability main content, HTML→Markdown, structured extraction, LLM client |
| `modules/` | Frontier, link/sitemap/robots parsing, URL cleaning, phrases, Persian text and Jalali dates, text diff, browser pool, stemming, semantic search, recon scanners |
| `storage/` | Local and S3 object stores |
| `handlers/` | `/v1` API, UI handlers and pages, auth |
| `plugins/` | `headless_fetch.py` (browser server), embeddings, stemming, after-crawl/after-job plugins |

### Fetching strategy

1. Wait for the per-host rate limiter. It is shared by all jobs in the process, so two customers crawling the same site do not overload it together. `429`/`503` responses with `Retry-After` pause that host.
2. Choose a proxy: job proxies, then the job's pool, then routing rules (`.ir` → Iranian pool), then the default pool. A sticky proxy keeps one exit IP per site.
3. Fetch with **HTTP** using a browser TLS fingerprint (Chrome by default; HTTP/2 when the server supports it), or with the **headless browser** from a pool of long-lived Chromium processes.
4. With **browser fallback**, an HTTP response that looks like a CDN/WAF challenge (ArvanCloud, Cloudflare, openresty) or an unrendered SPA shell is fetched again in the browser.
5. Every destination, including redirects, is checked by the SSRF guard (see [Security](#security)).

---

## Command-line interface

`mole --help` and `mole <command> --help` describe every flag. Global flags: `-c/--config`, `-o/--output table|json`, `-v/--verbose` (engine logs). Running `mole` without a command starts the server.

| Command | Purpose |
|---|---|
| `mole serve [--addr --no-ui --no-worker --region --max-jobs]` | Web UI, `/api`, `/v1` and a worker |
| `mole worker [--region --max-jobs --health-addr]` | Worker only (scale out; one per region) |
| `mole scrape <url>` | One page → Markdown/text/HTML/links/metadata/extracted JSON. `--browser`, `--fallback`, `--main-only`, `-f markdown,links`, `--extract fields.json`, `--actions actions.json`, `--screenshot shot.png --full-page`, `--llm-prompt`, `--proxy`, `--fingerprint`, `-H "Name: value"`, `--settings settings.json` |
| `mole map <url>` | URLs from robots.txt, sitemaps and the start page. `--search`, `--limit`, `--subdomains`, `--no-sitemap` |
| `mole crawl <url>` | Crawl and wait (Ctrl+C pauses). `-d`, `--max-pages`, `--max-duration`, `-f`, `--include/--exclude`, `--extract`, `--compare-with <job>`, `--owner`, `--priority`, `--region`, `--webhook`, `--export csv --out file`, `--detach` |
| `mole jobs list/get/watch/pages/changes/export` | Inspect jobs (`export --format csv\|json\|jsonl`) |
| `mole jobs start/pause/resume/cancel/delete/duplicate <id>…` | Control jobs from any machine; the owning worker applies the change within seconds |
| `mole schedules list/get/create/run/enable/disable/delete/next` | Recurring crawls (`create <url> --cron "0 */6 * * *" --tz Asia/Tehran`) |
| `mole owners usage <ref> [--from --to]` / `owners delete <ref>` | Billing data / account deletion |
| `mole migrate`, `mole doctor`, `mole config show\|validate`, `mole plugins list`, `mole version`, `mole completion bash` | Operations |

Extraction schema file (`--extract fields.json`):

```json
[
  {"name": "title", "selector": "h1"},
  {"name": "price", "selector": ".price", "type": "price"},
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
| Output | `formats` (`markdown`, `html`, `text`, `links`, `metadata`), `only_main_content`, `save_text_content` |
| Extraction | `extract_fields`, `extract_url_match` (regex), `llm_prompt`, `llm_schema` |
| Matching | `phrases` (job-specific; otherwise the global phrase list), `enable_word_extraction`, `use_crawl_phrases_only`, `enable_stemming`, `enable_lemmatization`, `default_language`, `enable_semantic_search` |
| Monitoring | `change_detection`, `compare_with_job_id` (schedules compare with their previous run automatically) |
| Plugins | `after_crawl_plugin`, `after_job_plugin` (refused for `/v1` clients unless `plugins.allow_in_api`) |

**Extraction field types:** `text` (default), `html` (outer HTML), `attr` (needs `attr`), `url` (absolute; `href`/`src` by default), `number`, `price` (`{"amount", "currency": "IRT|IRR|USD|…", "raw"}`), `date` (RFC3339; reads `datetime`/`content` attributes, ISO and Jalali dates), `exists` (bool), `list` (an array of objects built from `fields`, relative to each match). `multiple: true` returns every match; `regex` keeps the first capture group. Selectors are CSS, or XPath prefixed with `xpath:`.

**Browser actions:** `{"type": "click|type|press|scroll|wait|wait_for_selector|evaluate", "selector", "value", "ms", "count", "script"}`. For example, `{"type": "scroll", "value": "bottom", "count": 5}` loads lazy content, and `evaluate` results are saved with the page.

**Metadata** includes title, description, language, direction, canonical, favicon, Open Graph/Twitter tags, `published_at`, JSON-LD and word count.

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
| `worker` | `id` (env `MOLE_WORKER_ID`), `region` (env `MOLE_WORKER_REGION`), `max_running_jobs`, `max_running_per_owner`, `lease_ttl`, `poll_interval` |
| `security` | `allow_private_networks` (env `MOLE_ALLOW_PRIVATE_NETWORKS`), `allowed_cidrs`, `max_body_bytes` |
| `network` | `tls_fingerprint`, `host_rate_limit`, `proxy_pools`, `proxy_routes` (`{match, pool, region}`), `default_proxy_pool` |
| `headless` | `pool_size` — Chromium processes per worker |
| `storage` | `driver: local\|s3`, `local_dir`, `s3.{endpoint,bucket,access_key,secret_key,use_ssl,region,prefix}`, `offload_bodies` |
| `llm` | OpenAI-compatible `base_url`, `api_key`, `model`, `timeout`, `max_input_chars` (env `MOLE_LLM_*`). Works with self-hosted models and local gateways |
| `webhooks` | `timeout`, `max_attempts`, `allow_private` |
| `plugins` | `allow_in_api`, `after_job_delay`, `timeout` |

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
* **Scale out:** run `mole worker` processes on more machines against the same MySQL. Give each a stable `worker.id` so a restarted worker immediately resumes its own jobs (others are requeued after `lease_ttl`). Use `storage.driver: s3` so every node sees the same screenshots and bodies.
* **Regions:** start Iranian-IP workers with `--region ir` and foreign ones with `--region intl`. Jobs with `"region": "auto"` follow `network.proxy_routes`. A job pinned to a region with no running worker stays queued; the System page shows this.
* **API-only engine:** `mole serve --no-ui` for an instance that only serves the control plane.
* **Docker:** see `Dockerfile` (Go binary plus Python/Playwright/Chromium) and `docker-compose.yml`. Add workers with `docker compose --profile scale up -d --scale worker=3`. Set `INSTALL_ALL_PLUGINS=true` at build time for semantic search and stemming.
* **Graceful shutdown:** `SIGTERM` stops runs and puts their jobs back in the queue. Nothing is lost, and in-flight pages are re-fetched.

---

## Security

* **SSRF protection:** user-supplied URLs, redirects and webhook targets may not reach loopback, RFC1918, link-local (cloud metadata), CGNAT, multicast or reserved ranges. For HTTP fetches the check runs on the resolved IP at connect time, which closes the DNS-rebinding gap. In the headless browser, every request is checked by hostname/IP before it is sent and the final URL is re-checked after navigation (DNS rebinding inside the browser is not covered). Exceptions go in `security.allowed_cidrs`. Turn on `allow_private_networks` only for local, single-user use.
* `/v1` requires a bearer token once `api.tokens` is set; the UI and `/api` use HTTP basic auth when `ui.username` is set. Both compare credentials in constant time.
* Webhooks are signed with HMAC-SHA256 and include a timestamp; secrets are never returned by the API.
* Python plugins execute code on workers, so `/v1` clients cannot select them unless `plugins.allow_in_api` is on.
* Port scanning and subdomain discovery are UI-only admin tools and are not exposed in `/v1`.

---

## Web UI

| Page | What it is for |
|---|---|
| **Dashboard** | Running/queued counts, throughput, workers, live runs with progress, create a crawl |
| **Crawls** | All jobs with status, owner, schedule, region, usage |
| **Crawl detail** | Live metrics; tabs for phrase matches, **Pages & Data** (every page with status, Markdown and extracted fields; filters, live refresh, CSV/JSON export), extracted words, **Changes** (diffs), **Webhooks** (delivery log, redeliver) and **Settings** (every option in this README) |
| **Page detail** | Stored body plus Markdown, extracted data, LLM output, metadata, links, screenshot, browser action results and this page's change diff |
| **Scrape & Map** | Playground for `/v1/scrape` and `/v1/map`; "Crawl whole site" turns the same settings into a job |
| **Schedules** | Cron schedules with presets, run history with change counts, run now, enable/disable |
| **System** | Workers, live runs, queue, capabilities, security warnings, API quick start |
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

`crawl_jobs` (job, settings, limits, webhook, usage, queue/lease state), `frontier_urls`, `crawled_pages`, `page_contents`, `page_changes`, `schedules`, `webhook_deliveries` (outbox), `idempotency_keys`, `worker_heartbeats`, `phrase_matches`, `search_phrases`, `page_embeddings`, `discovery_jobs`, `subdomains`, `port_scan_jobs`, `discovered_ports`. Migrations run automatically on `serve`/`worker` (and with `mole migrate`).

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
