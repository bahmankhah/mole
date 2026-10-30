# n8n community node

n8n is widely used in Iran, mostly self-hosted, so there is no dependency on a foreign SaaS. A node puts scraping, search and monitoring into automations without code: "when a competitor's price changes, post it to our Bale group and update the Google Sheet", "every morning, search the news for our brand and summarise it". Agencies that build these automations for clients are a good reseller channel.

## 1. Package

* `n8n-nodes-<brand>` on npm (the `n8n-nodes-` prefix and the `n8n-community-node-package` keyword make it installable from Settings → Community Nodes).
* TypeScript, built on the n8n node starter, with the JS SDK ([sdks.md](sdks.md)) for HTTP, retries and errors.
* Offline install for servers without npm access: `npm install /path/to/n8n-nodes-<brand>-x.y.z.tgz` in `~/.n8n/nodes`, with the tarball downloadable from the docs.

## 2. Credentials

`<Brand>Api`: API key (password field) and base URL (default `https://api.<brand>.ir`, changeable for the enterprise edition). The credential test calls `GET /v1/account`.

## 3. Nodes

**`<Brand>` (action node)**, with resources and operations:

| Resource | Operations |
|---|---|
| Page | Scrape (URL, formats including the detectors Product, Article, Business, Job posting, Event, FAQ; recipe, render, max age) |
| Batch | Start, Get status, Get results (all pages, or page by page), Scrape and wait |
| Crawl | Start, Get status, Get results, Cancel |
| Site | Map |
| Web | Search (with optional scrape of the results) |
| Monitor | Create, Get, Update, Delete, Add URLs, Remove URLs, Get changes |

Output one n8n item per page or result, with fields flattened for easy use: `url`, `title`, `markdown`, `product.price.amount`, `product.availability`, `extracted.price.amount`, and so on. Large Markdown goes in the JSON and is never written to binary data unless the user asks for "Download as file". The node supports "Continue on fail" with the API's error code and message on the item.

**`<Brand> Trigger` (webhook trigger)**, with events `monitor.changed`, `monitor.failed`, `batch.completed`, `crawl.completed`, `credits.low`. On activation it registers its webhook URL with `POST /v1/webhooks` (and deletes it on deactivation), stores the returned secret in the node's static data, and verifies `X-Webhook-Signature` on every call. An n8n instance that is not reachable from the internet cannot receive webhooks; the node's description says so and points to the polling alternative (a Schedule Trigger plus "Monitor → Get changes" with a cursor).

## 4. Templates

Publish ready workflows next to the node, in Persian and English:
1. Competitor price change → Bale message + Google Sheets row.
2. Daily Persian news search for a keyword → LLM summary → email.
3. New product page on a competitor site (crawl + change detection) → Bale.
4. Batch scrape a sheet of URLs → write the extracted fields back to the sheet.

## 5. Testing and done when (end of week 8)

Unit tests for every operation against the mocked API, then a manual run of the four templates on a self-hosted n8n (current stable version) with a sandbox key, including the trigger's register and unregister cycle. Done when the package passes `npm run lint` from the starter (n8n's community-node rules) and the templates run end to end.
