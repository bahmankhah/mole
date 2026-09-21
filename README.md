# Mole — Web Crawler & Information Retrieval Engine

Mole is a high-performance, concurrent web crawler and information retrieval system built in Go with Python-based auxiliary services. It provides full-domain crawling, subdomain discovery, phrase detection, keyword search, and optional semantic (vector) search powered by sentence-transformers and FAISS. It exposes both a web UI (Gin + HTML templates) and a JSON REST API.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Project Structure](#project-structure)
3. [Technology Stack](#technology-stack)
4. [Database Schema](#database-schema)
5. [Core Components](#core-components)
   - [Crawler Engine](#crawler-engine)
   - [Job Manager](#job-manager)
   - [Modules](#modules)
6. [Features](#features)
   - [Web Crawling](#web-crawling)
   - [Subdomain Discovery](#subdomain-discovery)
   - [Phrase Detection](#phrase-detection)
   - [Keyword Search (N-Gram)](#keyword-search-n-gram)
   - [Semantic Search (Vector)](#semantic-search-vector)
   - [Headless Browser Rendering](#headless-browser-rendering)
   - [URL Normalization & Deduplication](#url-normalization--deduplication)
   - [Robots.txt & Sitemap Compliance](#robotstxt--sitemap-compliance)
   - [Per-Job Settings Override](#per-job-settings-override)
   - [Job Lifecycle Management](#job-lifecycle-management)
7. [API Reference](#api-reference)
8. [Web UI Pages](#web-ui-pages)
9. [Configuration](#configuration)
10. [Deployment](#deployment)
11. [Python Environment Setup](#python-environment-setup)

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│                        Gin HTTP Server                           │
│         (Web UI templates + JSON REST API on :5050)              │
├───────────────────┬──────────────────────────────────────────────┤
│   Handlers        │             Job Manager                      │
│   (handlers.go)   │             (manager.go)                     │
├───────────────────┴──────────────┬───────────────────────────────┤
│                                  │                               │
│          Crawler Engine          │     Subdomain Scanner         │
│          (engine.go)             │     (subdomain_scanner.go)    │
│                                  │                               │
├───────┬──────┬──────┬────────┬───┴────┬──────────┬──────────────┤
│Fetcher│Link  │Phrase│Sitemap │Robots  │ DB       │ Semantic     │
│(HTTP/ │Extr. │Det.  │Parser  │Parser  │ Frontier │ Searcher     │
│Headl.)│      │      │        │        │          │              │
├───────┴──────┴──────┴────────┴────────┴──────────┴──────────────┤
│                     MySQL 8.0 (GORM ORM)                         │
├──────────────────────────────────────────────────────────────────┤
│              Python Subprocess Services (optional)                │
│     headless_fetch.py (Playwright)  │  semantic_embed.py (FAISS) │
└──────────────────────────────────────────────────────────────────┘
```

The system follows a modular, pipeline-based architecture. The **Job Manager** orchestrates job lifecycle. The **Crawler Engine** runs concurrent workers that pull URLs from a **DB-backed Frontier**, fetch pages via an **HTTP Fetcher** (or **Headless Fetcher**), extract links, detect phrases, and optionally generate embeddings. All state is persisted in MySQL.

---

## Project Structure

```
.
├── main.go                       # Application entry point, Gin router setup, route registration
├── config.yaml                   # Runtime configuration (database, crawler, server, subdomain)
├── go.mod                        # Go module definition and dependencies
├── Makefile                      # Build, test, Docker, and utility commands
├── Dockerfile                    # Multi-stage Docker build (Go binary + Alpine)
├── docker-compose.yml            # Docker Compose for MySQL + crawler service
│
├── config/
│   └── config.go                 # Configuration structs, YAML loading, env overrides, defaults
│
├── models/
│   └── models.go                 # GORM model definitions for all database tables
│
├── database/
│   └── database.go               # Database connection, migrations, index creation, seeding
│
├── crawler/
│   └── engine.go                 # Core crawler engine: workers, crawl loop, content processing
│
├── jobs/
│   └── manager.go                # Job lifecycle manager: create, start, stop, pause, resume, stats
│
├── handlers/
│   └── handlers.go               # Gin HTTP handlers for all web and API routes
│
├── modules/
│   ├── interfaces.go             # Module interfaces (Module, Fetcher, LinkExtractor, PhraseDetector, etc.)
│   ├── fetcher.go                # HTTPFetcher (Chrome-mimicking TLS) + HeadlessFetcher (Playwright subprocess)
│   ├── db_frontier.go            # Database-backed URL frontier with random surfer model
│   ├── frontier.go               # In-memory random surfer frontier (alternative implementation)
│   ├── link_extractor.go         # HTML link extraction, sitemap/robots.txt parsing, text extraction
│   ├── url_cleaner.go            # URL normalization, tracking param removal, deduplication hashing
│   ├── phrase_detector.go        # Regex-based phrase detection in content, URLs, and anchor text
│   ├── duplicate_detector.go     # In-memory exact duplicate detector (hash set)
│   ├── subdomain_scanner.go      # DNS-based subdomain enumeration with concurrent lookups
│   └── semantic_search.go        # Embedding generation, FAISS index management, semantic search
│
├── plugins/
│   ├── plugins.go                # Discovers after-crawl / after-job plugins from this folder
│   ├── headless_fetch.py         # Playwright-based headless Chromium page renderer
│   ├── semantic_embed.py         # Sentence-transformer embedding + FAISS index/search service
│   ├── stem_lemma.py             # Stemming / lemmatization helper
│   ├── requirements.txt          # Python dependencies (playwright, sentence-transformers, faiss-cpu, numpy)
│   ├── setup_python.sh           # Automated Python venv creation and dependency installation
│   └── <plugin-id>/              # One folder per after-crawl / after-job plugin
│       ├── plugin.json           # Name, description, optional hook filenames
│       ├── after_crawl.py        # Optional: runs after each crawled page (JSON on stdin)
│       └── after_job.py          # Optional: runs ~2 minutes after the job ends (--job-id)
│
├── templates/
│   ├── index.html                # Dashboard: recent jobs, discovery jobs, matches, phrases
│   ├── job.html                  # Single job detail: stats, pages, matches, settings editor
│   ├── discovery.html            # Discovery job detail: subdomains list
│   ├── search.html               # Search page: keyword and semantic search interface
│   └── phrases.html              # Phrase management page: add, toggle, delete phrases
│
├── static/
│   └── js/
│       └── tailwind.js           # Tailwind CSS (CDN build) for UI styling
│
└── data/
    └── faiss/
        └── pages.index           # FAISS vector index file (generated at runtime)
```

---

## Technology Stack

| Layer | Technology | Purpose |
|---|---|---|
| **Language** | Go 1.21 | Core application, concurrency, HTTP server |
| **Web Framework** | Gin v1.9 | HTTP routing, middleware, template rendering |
| **ORM** | GORM v1.25 | Database operations, migrations, model mapping |
| **Database** | MySQL 8.0 | Persistent storage for all crawl data |
| **HTML Parsing** | goquery (PuerkitoBio) | DOM traversal, link extraction, text extraction |
| **Configuration** | gopkg.in/yaml.v3 | YAML config file parsing |
| **UUID** | google/uuid | Unique job identifiers |
| **Python Runtime** | Python 3.x | Subprocess services for headless rendering and embeddings |
| **Headless Browser** | Playwright (Python) | JavaScript-rendered page fetching via Chromium |
| **Embeddings** | sentence-transformers | Text-to-vector encoding (default: `paraphrase-multilingual-MiniLM-L12-v2`) |
| **Vector Index** | FAISS (faiss-cpu) | Approximate nearest neighbor search for semantic search |
| **Containerization** | Docker + Docker Compose | Deployment packaging |
| **UI** | Tailwind CSS | Frontend styling via HTML templates |

---

## Database Schema

The application uses **8 tables**, all managed via GORM `AutoMigrate`. The database is MySQL with `utf8mb4` character set.

### `discovery_jobs`

Tracks subdomain discovery operations for a root domain.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `VARCHAR(36)` | PK | UUID, auto-generated |
| `domain` | `VARCHAR(255)` | INDEX, NOT NULL | Target root domain (e.g. `example.com`) |
| `status` | `VARCHAR(20)` | INDEX, default `pending` | `pending`, `running`, `paused`, `completed`, `failed`, `cancelled` |
| `subdomains_found` | `INT` | default `0` | Count of discovered subdomains |
| `created_at` | `DATETIME` | | Creation timestamp |
| `updated_at` | `DATETIME` | | Last update timestamp |
| `started_at` | `DATETIME` | nullable | When discovery began |
| `completed_at` | `DATETIME` | nullable | When discovery finished |
| `error_message` | `TEXT` | | Error details if failed |

### `crawl_jobs`

Tracks individual crawl operations. Each crawl targets a specific URL/domain.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `VARCHAR(36)` | PK | UUID, auto-generated |
| `discovery_job_id` | `VARCHAR(36)` | INDEX | FK to `discovery_jobs.id` (optional, if spawned from discovery) |
| `target_url` | `VARCHAR(512)` | NOT NULL | The seed URL to begin crawling |
| `domain` | `VARCHAR(255)` | INDEX, NOT NULL | Base domain extracted from target URL |
| `status` | `VARCHAR(20)` | INDEX, default `pending` | `pending`, `running`, `paused`, `completed`, `failed`, `cancelled` |
| `total_urls` | `INT` | default `0` | Total URLs known (frontier + crawled) |
| `crawled_urls` | `INT` | default `0` | Count of URLs successfully fetched |
| `found_matches` | `INT` | default `0` | Total phrase match count |
| `max_depth` | `INT` | default `10` | Maximum crawl depth for this job |
| `settings` | `JSON` | nullable | Per-job settings override (serialized `JobSettings`) |
| `created_at` | `DATETIME` | | Creation timestamp |
| `updated_at` | `DATETIME` | | Last update timestamp |
| `started_at` | `DATETIME` | nullable | When crawl began |
| `completed_at` | `DATETIME` | nullable | When crawl finished |
| `error_message` | `TEXT` | | Error details if failed |

### `subdomains`

Stores discovered subdomains for a domain.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `UINT` | PK, auto-increment | |
| `discovery_job_id` | `VARCHAR(36)` | INDEX, NOT NULL, FK → `discovery_jobs` ON DELETE CASCADE | Parent discovery job |
| `domain` | `VARCHAR(255)` | INDEX, NOT NULL | Root domain |
| `subdomain` | `VARCHAR(255)` | NOT NULL | Full subdomain name (e.g. `api.example.com`) |
| `full_url` | `VARCHAR(512)` | | HTTPS URL of the subdomain |
| `ip_address` | `VARCHAR(45)` | | Resolved IP address |
| `is_active` | `BOOL` | default `true` | Whether the subdomain resolved |
| `crawl_job_id` | `VARCHAR(36)` | INDEX, FK → `crawl_jobs` ON DELETE SET NULL | Linked crawl job (if crawl was created from this subdomain) |
| `created_at` | `DATETIME` | | Discovery timestamp |

### `crawled_pages`

Stores metadata for every page the crawler has fetched.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `UINT` | PK, auto-increment | |
| `crawl_job_id` | `VARCHAR(36)` | UNIQUE(`crawl_job_id`,`url_hash`), NOT NULL, FK → `crawl_jobs` ON DELETE CASCADE | Parent crawl job |
| `url` | `VARCHAR(2048)` | INDEX (prefix 255) | Original URL as fetched |
| `url_hash` | `VARCHAR(64)` | UNIQUE (composite with `crawl_job_id`) | SHA-256 hash of the normalized URL |
| `doc_hash` | `VARCHAR(64)` | INDEX (composite with `crawl_job_id`) | SHA-256 hash of the response body (for content dedup) |
| `normalized_url` | `VARCHAR(2048)` | | Cleaned/normalized version of the URL |
| `title` | `VARCHAR(512)` | | HTML `<title>` extracted from content |
| `status_code` | `INT` | | HTTP response status code |
| `content_type` | `VARCHAR(128)` | | Response Content-Type header |
| `content_length` | `BIGINT` | | Response body size in bytes |
| `depth` | `INT` | | Crawl depth from seed URL |
| `parent_url` | `VARCHAR(2048)` | | URL of the page that linked to this one |
| `crawled_at` | `DATETIME` | | When the page was fetched |
| `response_time` | `BIGINT` | | Response time in milliseconds |
| `error_message` | `TEXT` | | Error if fetch failed |
| `is_archived` | `BOOL` | INDEX, default `false` | Soft-delete flag |

**Custom indexes:**
- `idx_doc_crawl_job` on `(doc_hash, crawl_job_id)` — fast content duplicate detection

### `frontier_urls`

The URL frontier queue. Pending URLs to be crawled, managed by the DB Frontier module. Completed URLs are **deleted** from this table (they exist in `crawled_pages`).

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `UINT` | PK, auto-increment | |
| `crawl_job_id` | `VARCHAR(36)` | INDEX (composite `idx_job_status`), UNIQUE (composite with `url_hash`), NOT NULL, FK → `crawl_jobs` ON DELETE CASCADE | Parent crawl job |
| `url` | `VARCHAR(2048)` | NOT NULL | Raw URL |
| `url_hash` | `VARCHAR(64)` | UNIQUE (composite with `crawl_job_id`) | SHA-256 hash of the normalized URL |
| `normalized_url` | `VARCHAR(2048)` | | Cleaned URL |
| `depth` | `INT` | | Crawl depth |
| `priority` | `INT` | INDEX, default `0` | Priority score (reserved for future use) |
| `status` | `VARCHAR(20)` | INDEX (composite `idx_job_status`), default `pending` | `pending`, `processing`, `failed` |
| `parent_url` | `VARCHAR(2048)` | | Referrer URL |
| `anchor_text` | `TEXT` | | Anchor text of the link pointing to this URL |
| `retry_count` | `INT` | default `0` | Number of fetch retries |
| `created_at` | `DATETIME` | | When added to frontier |
| `updated_at` | `DATETIME` | | Last status change |

### `search_phrases`

User-defined phrases to search for during crawling.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `UINT` | PK, auto-increment | |
| `phrase` | `VARCHAR(255)` | UNIQUE INDEX, NOT NULL | The search phrase text |
| `is_active` | `BOOL` | default `true` | Whether this phrase is currently active |
| `created_at` | `DATETIME` | | Creation timestamp |

### `phrase_matches`

Records every occurrence of a search phrase found during crawling.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `UINT` | PK, auto-increment | |
| `crawl_job_id` | `VARCHAR(36)` | INDEX, NOT NULL, FK → `crawl_jobs` ON DELETE CASCADE | Parent crawl job |
| `page_id` | `UINT` | INDEX, FK → `crawled_pages` ON DELETE CASCADE | The crawled page where the match was found |
| `search_phrase_id` | `UINT` | INDEX, FK → `search_phrases` ON DELETE SET NULL | Linked search phrase (nullable) |
| `url` | `VARCHAR(2048)` | NOT NULL | URL where the phrase was found |
| `phrase` | `VARCHAR(255)` | INDEX (`idx_phrase`), NOT NULL | The matched phrase text |
| `match_type` | `VARCHAR(20)` | INDEX, default `content` | `content` (page body), `url` (the URL itself), `anchor` (link anchor text) |
| `context` | `TEXT` | | ~100 characters of surrounding text for context |
| `occurrences` | `INT` | default `1` | Number of times the phrase appears |
| `found_at` | `DATETIME` | | Discovery timestamp |
| `is_archived` | `BOOL` | INDEX, default `false` | Soft-delete flag |

**Custom indexes:**
- `idx_phrase_match_phrase_url` on `(phrase(255), url(255))` — optimized search performance

### `page_embeddings`

Stores vector embeddings for semantic search.

| Column | Type | Constraints | Description |
|---|---|---|---|
| `id` | `UINT` | PK, auto-increment | |
| `crawl_job_id` | `VARCHAR(36)` | INDEX, NOT NULL, FK → `crawl_jobs` ON DELETE CASCADE | Parent crawl job |
| `page_id` | `UINT` | UNIQUE INDEX, NOT NULL, FK → `crawled_pages` ON DELETE CASCADE | The source page |
| `url` | `VARCHAR(2048)` | NOT NULL | Page URL |
| `title` | `VARCHAR(512)` | | Page title |
| `embedding` | `MEDIUMBLOB` | | Serialized float32 vector (384 dimensions × 4 bytes = 1,536 bytes typically) |
| `text_hash` | `VARCHAR(64)` | | SHA-256 hash of the embedded text (avoids re-embedding identical content) |
| `created_at` | `DATETIME` | | Embedding generation timestamp |

### Entity Relationship Summary

```
discovery_jobs 1──N subdomains N──1 crawl_jobs
crawl_jobs     1──N crawled_pages
crawl_jobs     1──N frontier_urls
crawl_jobs     1──N phrase_matches
crawl_jobs     1──N page_embeddings
crawled_pages  1──N phrase_matches
crawled_pages  1──1 page_embeddings
search_phrases 1──N phrase_matches
```

---

## Core Components

### Crawler Engine

**File:** `crawler/engine.go` (940 lines)

The engine is the heart of the system. It manages concurrent crawl workers and orchestrates the entire fetch-extract-detect-store pipeline.

**Lifecycle:**

1. **Initialization** — `NewEngine()` creates all modules: HTTPFetcher/HeadlessFetcher, URLCleaner, HTMLLinkExtractor, SimplePhraseDetector, SitemapParser, RobotsParser, DBFrontier, and SemanticSearcher.
2. **Start** — `Start(job)` transitions to `StateRunning`, merges per-job settings into the effective config, recreates the fetcher, resets robots rules, seeds the frontier, spawns `N` worker goroutines (default 10), starts a monitor goroutine, and a completion watcher.
3. **Worker loop** — Each worker:
   - Pulls a URL from the DB frontier using the random surfer model
   - Checks depth limit, robots.txt compliance, max pages limit
   - Calls `Fetcher.Fetch()` to retrieve the page
   - Saves the `CrawledPage` record (with URL hash and doc hash dedup)
   - Calls `processContent()` which:
     - Extracts text content and runs phrase detection (content, URL, anchor text)
     - Extracts links from HTML and filters by base domain
     - Processes sitemaps and robots.txt for URL discovery
     - Optionally generates semantic embeddings in a background goroutine
   - Sleeps for a randomized politeness delay (±1 second jitter)
   - Exits after 30 consecutive empty frontier checks (~15 seconds of inactivity)
4. **Completion** — When all workers exit naturally, `watchForCompletion()` marks the job as completed and optionally rebuilds the FAISS index.
5. **Stop/Pause/Resume** — Atomic state transitions (`StateRunning` ↔ `StatePaused` ↔ `StateStopping` → `StateIdle`). Pause holds workers in a 100ms sleep loop. Resume resets stuck "processing" URLs back to "pending".

**State Machine:**

```
StateIdle ──Start──▶ StateRunning ──Pause──▶ StatePaused
     ▲                     │                      │
     │                     Stop                  Resume
     │                     ▼                      │
     └───────────── StateStopping ◀───────────────┘
```

**Content Processing Pipeline:**

```
Fetch URL → Check Content-Type → Save CrawledPage → Extract Text
     │                                                     │
     │                                                     ├─▶ Detect Phrases (content)
     │                                                     ├─▶ Detect Phrases (URL)
     │                                                     ├─▶ Detect Phrases (anchor text)
     │                                                     └─▶ Generate Embedding (async, optional)
     │
     └─ If HTML → Extract Links → Filter by domain → Add to DB Frontier
     └─ If Sitemap XML → Parse URLs → Add to DB Frontier
     └─ If robots.txt → Parse rules → Store disallow/allow + discover sitemaps
```

**Deduplication Strategy:**

1. **URL dedup** — SHA-256 hash of the normalized URL. Composite unique index `(crawl_job_id, url_hash)` on both `frontier_urls` and `crawled_pages`. Uses `ON CONFLICT DO NOTHING` upserts.
2. **Content dedup** — SHA-256 hash of the response body (`doc_hash`). If `skip_content_duplicates` is enabled, pages with identical body hashes within a job are skipped (the page is recorded but not re-processed).

### Job Manager

**File:** `jobs/manager.go` (888 lines)

Central orchestrator for job lifecycle and data access. Wraps the crawler engine and subdomain scanner.

**Responsibilities:**
- **Job CRUD** — Create, read, update, delete crawl jobs and discovery jobs
- **Job control** — Start, stop, pause, resume crawl jobs (delegates to engine)
- **Subdomain discovery** — Creates a `DiscoveryJob`, runs `SubdomainScanner` in a goroutine, saves results
- **Data queries** — Paginated retrieval of crawled pages, phrase matches, stats
- **Phrase management** — CRUD for search phrases with match/URL count aggregation
- **Search** — Keyword search via n-gram matching, semantic search via FAISS
- **Stale job cleanup** — On startup, marks any `running`/`paused` jobs as `cancelled` (from previous crashes)
- **Job duplication** — Deep-copies job configuration for re-crawling
- **Settings management** — Per-job settings override with reset-to-defaults support

### Modules

All modules implement the base `Module` interface (`Name()`, `Initialize()`, `Shutdown()`).

#### HTTPFetcher (`modules/fetcher.go`)

Standard HTTP client that mimics a real Chrome browser to avoid bot detection:

- **TLS fingerprint** — Custom cipher suite ordering matching Chrome 131, TLS 1.2–1.3, curve preferences (X25519, P256, P384)
- **User-Agent rotation** — Pool of 7 real browser User-Agent strings (Chrome, Firefox, Safari, Edge), randomly selected per request
- **Chrome-like headers** — Sets `Sec-Ch-Ua`, `Sec-Fetch-*`, `Upgrade-Insecure-Requests`, `Accept-Language`, `Cache-Control`
- **Connection pooling** — 100 max idle connections, 10 per host, 20 max per host, HTTP/2 support
- **Gzip handling** — Automatic gzip decompression safety net
- **Cookie jar** — Maintains cookies across redirects
- **Redirect handling** — Follows up to 10 redirects, forwarding original headers
- **Body limit** — 10 MB max response body

#### HeadlessFetcher (`modules/fetcher.go`)

For JavaScript-heavy SPAs. Delegates to `plugins/headless_fetch.py` via subprocess:

- Launches Playwright Chromium in headless mode
- Waits for `domcontentloaded`, then optionally waits for a CSS selector
- Waits for `networkidle` state (up to 10s) for dynamic content
- Returns the fully rendered HTML
- Auto-detects the Python binary (checks project venv → system PATH)
- Timeout: script timeout + 15s buffer for browser startup/teardown

#### DBFrontier (`modules/db_frontier.go`)

Database-backed URL frontier implementing the **random surfer model**:

- **Random URL selection** — Instead of FIFO, uses a random selection strategy. Picks a random ID in the range `[min_id, max_id]` of pending rows, then finds the nearest pending URL. This creates `O(1)` random access vs `O(n)` from `ORDER BY RAND()` or large-offset `LIMIT`.
- **Teleport probability** (default 0.2) — 20% chance of selecting a random URL from the entire frontier instead of following linked URLs sequentially. Based on the PageRank random surfer model.
- **URL filtering** — Configurable file extension skip list (images, documents, archives, media, fonts, binaries). Optional regex-based include/exclude URL patterns.
- **Seed URL handling** — Seed URLs (target URL, robots.txt, sitemap.xml, sitemap_index.xml) bypass extension and URL pattern filters. Discovery seeds are only added for root URLs (not deep paths or SPA fragment routes).
- **SPA fragment support** — Detects meaningful URL fragments (e.g. `#/search?q=foo`) and preserves them through normalization.
- **Upsert insertion** — Uses `ON CONFLICT DO NOTHING` to prevent duplicate frontier entries without race conditions.
- **Cross-check dedup** — Before adding a URL, checks both `frontier_urls` and `crawled_pages` tables to prevent re-crawling.
- **Status management** — `pending` → `processing` → deleted (on success) or `failed`/re-queued (on failure with retry count).
- **Recovery** — `ResetProcessingURLs()` moves stuck "processing" URLs back to "pending" on resume or recovery.

#### URLCleaner (`modules/url_cleaner.go`)

Comprehensive URL normalization and cleaning:

- **Scheme normalization** — Defaults to `https://`, only allows `http`/`https`
- **Case normalization** — Lowercases scheme and host
- **Default port removal** — Strips `:80` (HTTP) and `:443` (HTTPS)
- **Fragment removal** — Strips `#fragment` (with SPA exception via `ProcessURLKeepFragment`)
- **Tracking parameter removal** — Strips 50+ known tracking parameters: UTM (`utm_source`, `utm_medium`, etc.), ad networks (`fbclid`, `gclid`, `msclkid`, `zanpid`), analytics (`_ga`, `_gl`, `_hsenc`), site-specific (`redirect_url`, `callback_url`, `return_url`, etc.)
- **Per-job extra tracking params** — Jobs can specify additional params to strip
- **Query parameter sorting** — Alphabetical for consistency
- **Path normalization** — Removes duplicate slashes, trailing slashes (except root)
- **Domain extraction** — `ExtractDomain()` for full hostname, `ExtractBaseDomain()` for registrable domain (handles `.co.uk`, `.com.br`, etc.)
- **URL hashing** — SHA-256 of the normalized URL for deduplication

#### HTMLLinkExtractor (`modules/link_extractor.go`)

Extracts links from HTML using goquery DOM traversal. Covers a comprehensive set of sources:

| Source | HTML Elements | Details |
|---|---|---|
| Hyperlinks | `<a href>` | With anchor text preservation and deduplication |
| Resources | `<img src>`, `<script src>`, `<iframe src>` | Static assets |
| Stylesheets | `<link href>` | CSS and other linked resources |
| Forms | `<form action>` | Form submission targets |
| Data attributes | `[data-href]`, `[data-url]`, `[data-src]`, `[data-link]` | SPA/JavaScript integration |
| SEO | `<link rel="canonical">`, `<link rel="alternate">` | Canonical and alternate URLs |
| Meta refresh | `<meta http-equiv="refresh" content="0;url=...">` | Page redirects |
| OpenGraph | `<meta property="og:*">`, `<meta name="twitter:*">` | Social media URLs |
| Responsive images | `[srcset]` | All candidates from srcset attribute |
| Inline scripts | `<script>` text content | Bare URL regex extraction (up to 200 URLs per block, max 512KB scripts) |

Also provides:
- **Text extraction** — `ExtractTextContent()` strips `<script>`, `<style>`, `<noscript>` and normalizes whitespace
- **Title extraction** — `ExtractTitle()` reads the `<title>` tag

#### SitemapParser (`modules/link_extractor.go`)

Parses XML sitemaps and sitemap index files:

- Extracts URLs from `<url><loc>` tags
- Extracts nested sitemap references from `<sitemap><loc>` tags
- Normalizes all discovered URLs through the URLCleaner

#### RobotsParser (`modules/link_extractor.go`)

Parses `robots.txt` with compliance enforcement:

- Extracts `Sitemap:` directives → added to frontier for discovery
- Extracts `Allow:` and `Disallow:` paths for the `*` user-agent
- **Path-prefix matching** — When checking compliance, the longest matching `Disallow` path wins, unless a longer `Allow` path overrides it
- Rules are cached per-domain in the engine's `robotRules` map

#### SimplePhraseDetector (`modules/phrase_detector.go`)

Finds user-defined phrases in crawled content using regex matching:

- Compiles each phrase into a case-insensitive `(?i)` regex (with `QuoteMeta` escaping for special characters)
- Detects in three contexts: **content** (page body text), **URL** (the page URL), **anchor** (link anchor text pointing to the page)
- Returns occurrence count and ~100-character context window around the first match
- UTF-8 safe context extraction (never cuts multi-byte characters)

#### SubdomainScanner (`modules/subdomain_scanner.go`)

DNS-based subdomain enumeration:

- Tests ~100+ common subdomain prefixes (www, mail, api, cdn, admin, blog, dev, staging, etc.) via DNS A-record lookups
- Uses configurable DNS servers (default: Google 8.8.8.8/8.8.4.4, Cloudflare 1.1.1.1)
- Concurrent lookups with configurable parallelism (default: 50 concurrent)
- Per-lookup timeout (default: 5 seconds)
- Reports discovered subdomains via callback with IP address

#### SemanticSearcher (`modules/semantic_search.go`)

Vector-based semantic search using sentence-transformers and FAISS:

- **Embedding generation** — Calls `plugins/semantic_embed.py` with `command: embed`. Uses `paraphrase-multilingual-MiniLM-L12-v2` model (384-dimensional, multilingual). Truncates page content to ~1,000 words. Prepends page title to the text.
- **Content hashing** — SHA-256 of the embedded text prevents re-embedding identical content.
- **Storage** — Embeddings stored as `MEDIUMBLOB` in MySQL (float64 → float32 serialization, 4 bytes per dimension). Upserted on `page_id` conflict.
- **Index building** — Calls `plugins/semantic_embed.py` with `command: index`. Reads all embeddings from DB, builds a FAISS `IndexIDMap(IndexFlatIP)` index (inner product = cosine similarity for normalized vectors), writes to `data/faiss/pages.index`.
- **Search** — Calls `plugins/semantic_embed.py` with `command: search`. Embeds the query text, searches the FAISS index for top-K nearest neighbors, returns page IDs with similarity scores. Results are enriched with page metadata and domain info from the database.
- **Auto-rebuild** — FAISS index is automatically rebuilt when a crawl job completes with semantic search enabled.
- **Environment handling** — Strips proxy environment variables (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`) before calling Python to prevent interference with model downloads.

#### ExactDuplicateDetector (`modules/duplicate_detector.go`)

In-memory hash set for fast URL deduplication:

- Thread-safe with `sync.RWMutex`
- Atomic `IsDuplicateOrMark()` for check-and-mark in one operation
- `LoadFromHashes()` for resuming crawls from existing state
- Used by the in-memory `RandomSurferFrontier` (the DB frontier uses database-level dedup instead)

---

## Features

### Web Crawling

The core crawling pipeline:

1. **Seed URL** → Added to the DB frontier. For root domains, also seeds `robots.txt`, `sitemap.xml`, `sitemap_index.xml`.
2. **Concurrent workers** (configurable, default 10) → Each pulls a URL from the frontier using the random surfer selection model.
3. **Fetch** → HTTP GET with Chrome-mimicking TLS and headers, or headless Chromium for JS-rendered pages.
4. **Content-Type check** → Only processes `text/html`, `application/xhtml+xml`, `*xml`, `text/plain`, `application/json`, RSS, Atom.
5. **Save** → Stores `CrawledPage` with URL hash dedup (upsert) and optional content dedup (doc hash).
6. **Extract links** → From HTML elements, data attributes, meta tags, srcset, inline scripts.
7. **Filter links** → Only same base domain. Extension filter. URL include/exclude patterns.
8. **Add to frontier** → Normalized, deduplicated, at `depth + 1`.
9. **Detect phrases** → In page text, URL, and anchor text.
10. **Generate embedding** → Async, if semantic search is enabled.
11. **Politeness delay** → Randomized delay (base ± 1 second jitter) between requests.

**Resumability:** Jobs can be paused and resumed. The DB frontier preserves state. Stuck "processing" URLs are reset to "pending" on resume.

### Subdomain Discovery

DNS enumeration to discover subdomains before or during crawling:

1. User submits a domain (e.g. `example.com`)
2. Creates a `DiscoveryJob` with status `running`
3. Tests 100+ common subdomain prefixes via DNS A-record lookups
4. 50 concurrent DNS resolution workers
5. Each discovered subdomain is saved with its IP address
6. From the discovery results, individual crawl jobs can be created per subdomain

### Phrase Detection

Three-dimensional phrase matching:

1. **Content matching** — Searches the extracted text content of each HTML page (stripped of scripts/styles/noscript)
2. **URL matching** — Searches the URL itself for phrases
3. **Anchor text matching** — Searches the anchor text of links pointing to each page

Each match records: phrase, match type (`content`/`url`/`anchor`), occurrence count, ~100-char context snippet, timestamp. Uses case-insensitive regex with `regexp.QuoteMeta` for safe matching of special characters.

### Keyword Search (N-Gram)

Post-crawl search across all recorded phrase matches:

1. User enters a query (e.g. "web scraping tools")
2. System generates all possible n-grams: `["web scraping tools", "web scraping", "scraping tools", "web", "scraping", "tools"]`
3. Searches `phrase_matches` table with `LIKE %ngram%` conditions (OR combined)
4. Results grouped by `(url, phrase, match_type)` with summed occurrences
5. Paginated, ordered by total occurrences descending

### Semantic Search (Vector)

AI-powered similarity search across all crawled pages:

1. **During crawl** — Each HTML page's text (title + first 1,000 words) is embedded using `paraphrase-multilingual-MiniLM-L12-v2` (384-dim vectors). Stored in `page_embeddings` table.
2. **Index build** — After crawl completes, FAISS `IndexFlatIP` index is built from all embeddings. Stored at `data/faiss/pages.index`.
3. **Search** — User query is embedded with the same model, then searched against the FAISS index using inner product (cosine similarity for normalized vectors). Returns top-K results with similarity scores (0–1).

**Dependencies:** Python 3, `sentence-transformers`, `faiss-cpu`, `numpy`. Optional — disabled by default (`enable_semantic_search: false`).

### Headless Browser Rendering

For JavaScript-heavy SPAs that don't render content server-side:

1. Enabled via `use_headless_browser: true` in config or per-job settings
2. Go launches `plugins/headless_fetch.py` as a subprocess
3. Playwright launches headless Chromium with anti-detection flags (`--no-sandbox`, etc.)
4. Navigates to URL, waits for `domcontentloaded`
5. Optionally waits for a CSS selector (`headless_wait_selector`)
6. Waits for network to settle (`networkidle`, up to 10s)
7. Returns fully rendered HTML as JSON

**Dependencies:** Python 3, `playwright`. Requires `playwright install chromium` after pip install.

### URL Normalization & Deduplication

Multi-layer dedup prevents crawling the same content twice:

1. **URL normalization** — Lowercase, strip tracking params, sort query params, remove default ports, remove fragments, remove trailing slashes → canonical URL form
2. **URL hash** — SHA-256 of normalized URL → composite unique index per job
3. **Content hash** — SHA-256 of response body → skip pages with identical content (optional, via `skip_content_duplicates`)
4. **Frontier check** — Before adding a URL, checks both `frontier_urls` (pending/processing/failed) and `crawled_pages` (already fetched)
5. **Upsert** — `ON CONFLICT DO NOTHING` on the composite unique index `(crawl_job_id, url_hash)` → race-condition safe

### Robots.txt & Sitemap Compliance

- **robots.txt parsing** — Extracts `Allow`, `Disallow` (for `User-agent: *`) and `Sitemap` directives
- **Path-prefix matching** — Longest matching rule wins; `Allow` overrides `Disallow` if more specific
- **Configurable** — `respect_robots_txt: true/false` in config or per-job settings
- **Sitemap discovery** — URLs from `Sitemap:` directives are added to the frontier
- **Sitemap parsing** — Handles both sitemap XML (`<url><loc>`) and sitemap index files (`<sitemap><loc>`)

### Per-Job Settings Override

Each crawl job can override global crawler config with a JSON `settings` field:

| Setting | Type | Description |
|---|---|---|
| `max_concurrent_requests` | int | Worker count |
| `request_timeout_seconds` | int | HTTP timeout |
| `politeness_delay_ms` | int | Delay between requests |
| `max_depth` | int | Maximum crawl depth |
| `max_pages` | int | Maximum pages to crawl (0 = unlimited) |
| `user_agent` | string | Custom User-Agent |
| `max_retries` | int | Retry count on failure |
| `respect_robots_txt` | bool | Robots.txt compliance |
| `skip_content_duplicates` | bool | Content dedup |
| `skip_extensions` | string[] | File extensions to skip |
| `url_include_patterns` | string[] | Regex allowlist (if set, only matching URLs are crawled) |
| `url_exclude_patterns` | string[] | Regex denylist |
| `extra_tracking_params` | string[] | Additional URL params to strip |
| `use_headless_browser` | bool | Enable Playwright rendering |
| `headless_wait_selector` | string | CSS selector to wait for |
| `enable_semantic_search` | bool | Generate embeddings |
| `after_crawl_plugin` | string | Plugin id under `plugins/` to run after each crawled page (empty = disabled) |
| `after_job_plugin` | string | Plugin id under `plugins/` to run ~2 minutes after the job ends (empty = disabled) |

Null/omitted values fall back to global defaults. Settings can be updated via API for `pending` jobs and reset to defaults.

### Job Lifecycle Management

```
Created (pending) ──Start──▶ Running ──Pause──▶ Paused
       │                        │                   │
       │                       Stop              Resume
       │                        ▼                   │
       │                   Completed ◀──────────────┘
       │                   (or Cancelled/Failed)
       │
       └──Delete──▶ (cascades to pages, matches, frontier, subdomains, embeddings)
```

- **Duplicate job** — Creates a new `pending` job copying the source job's target URL, domain, max depth, and settings (deep-copied)
- **Stale cleanup** — On server restart, all `running`/`paused` jobs are marked as `cancelled`
- **Soft-delete** — `crawled_pages` and `phrase_matches` have `is_archived` flags
- **Cascade delete** — Deleting a job removes all associated pages, matches, frontier URLs, and subdomains

---

## API Reference

Base URL: `http://<host>:<port>/api`

### Jobs

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/api/jobs` | Create a new crawl job. Body: `{"target_url": "...", "max_depth": 10, "settings": {...}}` |
| `GET` | `/api/jobs` | List all crawl jobs. Query: `limit`, `offset` |
| `GET` | `/api/jobs/:id` | Get job details with subdomains, matches, pages, stats |
| `POST` | `/api/jobs/:id/start` | Start a pending/cancelled crawl job |
| `POST` | `/api/jobs/stop` | Stop the currently running job |
| `POST` | `/api/jobs/pause` | Pause the currently running job |
| `POST` | `/api/jobs/resume` | Resume the currently paused job |
| `POST` | `/api/jobs/:id/duplicate` | Duplicate a job's configuration into a new pending job |
| `DELETE` | `/api/jobs/:id` | Delete a job and all associated data |
| `PUT` | `/api/jobs/:id/settings` | Update settings for a pending job. Body: `{...settings}` or `{"reset": true}` |
| `GET` | `/api/settings/defaults` | Get default crawler settings |
| `GET` | `/api/plugins` | List after-crawl / after-job plugins discovered from `plugins/` |

### Discovery

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/api/discovery` | Create and start subdomain discovery. Body: `{"domain": "example.com"}` |
| `GET` | `/api/discovery` | List all discovery jobs |
| `GET` | `/api/discovery/:id` | Get discovery job with subdomains |

### Subdomains

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/api/jobs/:id/subdomains/discover` | Start subdomain discovery for a crawl job's domain |
| `GET` | `/api/jobs/:id/subdomains` | Get subdomains for a job |
| `POST` | `/api/subdomains/:id/crawl` | Create crawl job from a subdomain. Body: `{"max_depth": 10, "auto_start": true}` |

### Pages & Matches

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/jobs/:id/pages` | Get crawled pages for a job. Query: `limit`, `offset` |
| `GET` | `/api/matches` | Get phrase matches. Query: `job_id`, `limit`, `offset` |

### Search

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/search` | Search phrase matches. Query: `q`, `mode` (`keyword`/`semantic`), `limit`, `offset` |

### Semantic Search

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/api/semantic/rebuild` | Trigger FAISS index rebuild (async) |
| `GET` | `/api/semantic/stats` | Get semantic search stats (embedding count, index status) |

### Phrases

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/phrases` | List all search phrases |
| `POST` | `/api/phrases` | Add a phrase. Body: `{"phrase": "..."}` |
| `PUT` | `/api/phrases/:id` | Update phrase active status. Body: `{"is_active": true/false}` |
| `DELETE` | `/api/phrases/:id` | Delete a phrase |

### Stats

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/stats` | Get engine statistics and active job info |

---

## Web UI Pages

| Route | Template | Description |
|---|---|---|
| `/` | `index.html` | Dashboard with recent crawl jobs, discovery jobs, phrase matches, and search phrase summary |
| `/jobs/:id` | `job.html` | Single job detail page with live stats, crawled pages list, phrase matches, and a settings editor |
| `/discovery/:id` | `discovery.html` | Discovery job detail with list of discovered subdomains and option to create crawl jobs |
| `/search` | `search.html` | Unified search interface supporting both keyword (n-gram) and semantic (vector) search modes |
| `/phrases` | `phrases.html` | Phrase management page: add new phrases, toggle active/inactive, delete, view match counts per phrase |

All pages use Tailwind CSS for styling and are server-rendered with Go's `html/template`.

---

## Configuration

Configuration is loaded from `config.yaml` (or the path specified in `CONFIG_PATH` env var) with environment variable overrides.

### `config.yaml` Structure

```yaml
database:
  host: localhost          # MySQL host
  port: "3306"             # MySQL port
  user: root               # MySQL user
  password: "1234"         # MySQL password
  dbname: crawler_db       # Database name

server:
  host: 0.0.0.0            # HTTP listen address
  port: "5050"             # HTTP listen port

crawler:
  max_concurrent_requests: 10       # Number of concurrent worker goroutines
  request_timeout: 30s              # HTTP request timeout
  politeness_delay: 0s              # Base delay between requests (±1s jitter)
  teleport_probability: 0.2         # Random surfer teleport probability (0.0–1.0)
  max_depth: 10                     # Maximum crawl depth from seed
  max_pages: 0                      # Max pages per job (0 = unlimited)
  user_agent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"  # Default User-Agent (rotated with pool)
  max_retries: 3                    # Max retries on fetch failure
  respect_robots_txt: false         # Honor robots.txt rules
  skip_content_duplicates: true     # Skip pages with identical body hash
  use_headless_browser: false       # Use Playwright for JS rendering
  headless_wait_selector: ""        # CSS selector to wait for (headless mode)
  enable_semantic_search: false     # Generate embeddings + FAISS index
  after_crawl_plugin: ""            # Plugin folder under plugins/ (empty = disabled)
  after_job_plugin: ""
  embedding_model: "paraphrase-multilingual-MiniLM-L12-v2"  # Sentence-transformer model
  skip_extensions:                  # File extensions to skip
    - .jpg
    - .png
    - .pdf
    - .zip
    # ... (images, documents, archives, media, fonts, binaries)

subdomain:
  dns_servers:                      # DNS servers for subdomain scanning
    - 8.8.8.8:53
    - 8.8.4.4:53
    - 1.1.1.1:53
  concurrent_lookups: 50            # Concurrent DNS workers
  timeout: 5s                       # Per-lookup timeout
```

### Environment Variable Overrides

| Variable | Overrides |
|---|---|
| `CONFIG_PATH` | Path to config file |
| `DB_HOST` | `database.host` |
| `DB_PORT` | `database.port` |
| `DB_USER` | `database.user` |
| `DB_PASSWORD` | `database.password` |
| `DB_NAME` | `database.dbname` |
| `SERVER_HOST` | `server.host` |
| `SERVER_PORT` | `server.port` |

---

## Deployment

### Local Development

```bash
# 1. Prerequisites: Go 1.21+, MySQL 8.0

# 2. Create database
mysql -u root -p -e "CREATE DATABASE IF NOT EXISTS crawler_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# 3. Build and run
make build
./crawler

# Or in one step:
make run
```

### Docker Compose

```bash
# Start MySQL + crawler
docker-compose up -d

# View logs
docker-compose logs -f

# Stop
docker-compose down
```

The Docker setup runs:
- **MySQL 8.0** on port 3306 with health checks
- **Crawler** on port 8080, auto-migrates database on startup

### Makefile Commands

| Command | Description |
|---|---|
| `make build` | Download deps + build binary |
| `make run` | Build and run |
| `make dev` | Run with hot reload (requires `air`) |
| `make test` | Run all tests |
| `make test-coverage` | Run tests with HTML coverage report |
| `make docker-build` | Build Docker image |
| `make docker-up` | Start Docker Compose services |
| `make docker-down` | Stop Docker Compose services |
| `make docker-logs` | Tail Docker Compose logs |
| `make setup-mysql` | Create local MySQL database |
| `make lint` | Run golangci-lint |
| `make fmt` | Format all Go files |

---

## Python Environment Setup

Required only for **headless browser rendering** and/or **semantic search**.

```bash
# Automated setup (creates venv, installs deps)
chmod +x plugins/setup_python.sh
./plugins/setup_python.sh

# Manual setup
cd plugins
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt

# For headless browser support
playwright install chromium
```

The Go server auto-detects the Python venv at `plugins/.venv/bin/python3`. No manual activation needed.

### Python Dependencies

| Package | Version | Purpose |
|---|---|---|
| `playwright` | ≥1.40 | Headless Chromium browser for JS rendering |
| `sentence-transformers` | ≥2.2 | Text embedding models |
| `faiss-cpu` | ≥1.7 | Approximate nearest neighbor vector search |
| `numpy` | ≥1.24 | Numerical operations for embedding serialization |
