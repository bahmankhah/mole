# Products built on Mole

Mole is the engine. Customers buy products built on it, through a Laravel control plane that owns users, plans, credits and payments ([laravel-integration.md](../laravel-integration.md)). This folder specifies those products and the order to build them in.

```
                  ┌─────────────────────── customers ───────────────────────┐
 Dashboard (web) · WooCommerce plugin · SDKs (PHP/JS/Python) · MCP server · n8n node
                  └──────────────────────────┬──────────────────────────────┘
                                             │ customer API keys
                         Laravel control plane: customer API, plans, credits,
                         recipes, monitors, alerts (email, SMS, Bale), billing
                                             │ engine token, owner_ref per team
                         Mole engine: /v1 crawl, batch, scrape, map, search,
                         schedules, change detection, detectors, PDF/DOCX, cache
```

Every product is a client of the [customer API](customer-api.md). None of them talks to the engine directly.

## Documents

| Document | What it specifies |
|---|---|
| [customer-api.md](customer-api.md) | The public API: routes, errors, credits and rate-limit headers, customer webhooks |
| [recipes.md](recipes.md) | Ready-made, tested extraction settings for Iranian sites (the first ones, testing, monitoring) |
| [woocommerce-price-monitor.md](woocommerce-price-monitor.md) | The price-monitoring service and WordPress plugin, repricing rules, notifications (email, SMS, Bale) |
| [sdks.md](sdks.md) | PHP, JavaScript/TypeScript and Python SDKs |
| [mcp-server.md](mcp-server.md) | Tools for AI assistants and agents |
| [n8n-node.md](n8n-node.md) | Action and trigger nodes for n8n automations |
| [enterprise-edition.md](enterprise-edition.md) | The on-premises package, licensing, sizing, and the week 9–12 pilots |

## Plan for weeks 5–12

Weeks 1–4 deliver the engine features these products rely on (done: batch scrape, PDF/DOCX, cache, search, per-owner limits, JSON-LD extraction, detectors for products, articles, businesses, jobs, events and FAQs, field-level change detection) and the Laravel MVP: auth, teams, credit wallet with an Iranian gateway, API keys, the customer API's scrape/batch/crawl/map/search routes, and the dashboard's playground and crawl screens.

| Weeks | Deliverable | Depends on | Done when |
|---|---|---|---|
| 5 | Monitors and notifications in Laravel (monitors API, Bale/SMS/email channels, digests) | Customer API, engine schedules | A batch monitor on a test site sends a digest on all three channels |
| 5–6 | First recipes for what product detection misses: `woocommerce-product` (Persian themes), 2–3 marketplaces, `news-article` | Engine `product` format, `jsonld:` selectors and fallbacks | Every recipe passes its tests and the daily canary for a week |
| 5–6 | PHP SDK and Laravel package | Customer API `openapi.yaml` | Published; contract tests green |
| 6–8 | WooCommerce price monitor plugin | Monitors, recipes, customer webhooks | The 7-day test shop run in its "done when" list passes |
| 6–7 | JS/TS SDK | `openapi.yaml` | Published; contract tests green |
| 7–8 | MCP server (stdio and hosted) | JS SDK | Works in two assistant clients; dashboard page with snippets |
| 7–8 | n8n node and four templates | JS SDK, customer webhooks | Templates run end to end on self-hosted n8n |
| 9–10 | Python SDK | `openapi.yaml` | Published |
| 9–12 | Talk to 5 e-commerce shops and 3 enterprises; run 2–3 on-premises pilots | Everything above, the offline installer | A written decision: lead with SMB price monitoring, enterprise, or both |

Later, after weeks 9–12 have shown what customers pay for: a Chrome extension (point-and-click field picker that saves a private recipe, and "monitor this page"), datasets built from monitor history (price indexes), and a recipe marketplace.

## Shared rules for every product

* **Persian first.** Right-to-left layouts, Jalali dates in the UI, Persian error messages (`message_fa`), Persian and English docs. Keep the zero-width non-joiner in text and never escape Persian in JSON.
* **Works during international outages.** Host the API, docs, package mirrors and email relay inside Iran. Nothing on a customer's critical path should depend on a foreign service.
* **Customer words, not engine words.** Customers see "pages", "monitors" and "checks". They never see `owner_ref`, workers, regions, proxy pools or credits formulas (the dashboard shows prices in Toman).
* **Safe defaults.** Respect robots.txt in crawls (`respect_robots_txt`; batch scrapes fetch the URLs a customer lists, as single scrapes do) and per-host rate limits, never offer recipes that collect personal data, and show a short acceptable-use policy at sign-up.
* **Measured.** Every product reports usage to Laravel with the team id, so cost and revenue per product are visible from the first customer.
