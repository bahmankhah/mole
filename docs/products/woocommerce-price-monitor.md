# WooCommerce price monitor (plugin + service)

**For:** Iranian online shops on WordPress/WooCommerce, and marketplace sellers who also run a shop.
**Promise:** "Know within hours when a competitor changes a price, and set your price automatically within the limits you choose."
**Priced by:** tracked competitor URLs × check frequency (for example 100, 1,000 or 10,000 URLs, checked daily or every 6 hours), not by credits. Shop owners never see the word "credit".

The product has two halves:
1. **The service, in Laravel:** monitors, alerts, history and the dashboard. It works without WordPress, and customers on other platforms use it too.
2. **The WordPress plugin:** links competitor URLs to WooCommerce products, shows the comparison inside wp-admin and applies repricing rules.

## 1. How it works

```
WooCommerce admin ──(customer API key)──► Laravel customer API ──► Mole /v1/schedules (kind: batch)
        ▲                                      │                          │ every 6h: fetch URLs, extract,
        │  signed webhook: monitor.changed     │◄── page.changed (fields) ┘ compare with the last run
        └──────────────────────────────────────┤
                                               └──► alerts: Bale, SMS, email
```

1. The shop owner installs the plugin, pastes an API key, and adds competitor URLs to products. Each URL is checked with the engine's product detection (`formats: ["product"]`), plus a [recipe](recipes.md) for shops that detection does not cover. The plugin shows the detected price and stock at once (one `POST /v1/scrape`); when no price is found it asks the owner to report the URL so staff can add a recipe.
2. The plugin syncs the URL list to one monitor per shop (`POST /v1/monitors` and then `POST/DELETE /v1/monitors/{id}/urls`). Laravel turns it into an engine schedule with `template.kind = "batch"`, `formats: ["product"]` (plus a recipe's `extract_fields` for hosts that need one), change detection, and a webhook subscribed to `page.changed`, `job.changes`, `job.completed` and `job.failed`.
3. Each run, Mole sends `page.changed` with `fields`, for example `[{name: "product.price", old, new}, {name: "product.availability", old, new}]`, for every product whose price, regular price or availability changed. Recipe fields are named without the prefix (`price`). Prices compare by amount and currency, so a reformatted price is not a change.
4. Laravel stores the new values in `tracked_items`, writes an `alerts` row and notifies the shop (in one digest per run, not one message per product). It then sends `monitor.changed` to the plugin's webhook URL.
5. The plugin applies the product's repricing rule, or only shows a suggestion if the rule is in "suggest" mode.

`job.completed` is the run boundary: send the digest when it arrives, and report URLs that returned errors (`GET /v1/crawls/{id}/pages?failed=1`) as "could not check".

## 2. WordPress plugin

**Requirements:** PHP 7.4+, WordPress 6.x, WooCommerce 8.x. Talk to the customer API with `wp_remote_request` (a small client class inside the plugin, with the same error codes and webhook verification as the [SDKs](sdks.md); do not bundle Guzzle). Use only the WooCommerce CRUD API (`wc_get_product`, `$product->set_regular_price()`, `->set_sale_price()`, `->save()`), never direct post-meta writes, so caches, lookups and other plugins stay correct. Declare HPOS compatibility.

**Screens (RTL, Persian first, Jalali dates via the site's date settings):**
* **Settings** (WooCommerce → Price monitor): API key (stored with `update_option`, autoload off, never printed back), connection test (`GET /v1/account`), default check frequency (limited by the plan), alert channels (managed in the dashboard and linked from here).
* **Product edit screen** (a meta box, and the same fields for each variation): competitor URLs (add, remove, "check now"), last price and time per competitor, and the rule.
* **Products list column:** our price, the lowest competitor price, and a coloured difference.
* **Report page:** all products with competitors; filters "we are the most expensive", "competitor out of stock", "check failed"; CSV export.

**Repricing rules (per product, with a shop default):**

| Rule | Example |
|---|---|
| Mode | `off`, `suggest` (default), `auto` |
| Strategy | match the lowest in-stock competitor; or be X Toman / X% below it; or be X% above it |
| Floor | never below cost × (1 + minimum margin). Without a cost, `auto` is not allowed |
| Ceiling | never above the regular price, or a fixed maximum |
| Rounding | to 1,000 or 10,000 Toman, or ending in 9,000 |
| Ignore | competitors that are out of stock; changes under 1% |

Apply a rule only when the competitor's currency is known. Convert Rial and Toman (`IRR` = `IRT` × 10) to the shop's currency from `get_woocommerce_currency()`, and leave other currencies to "suggest" mode. Log every automatic change (old price, new price, competitor, rule) in a custom table that the report page shows, and add a one-click "undo".

**Webhook receiver:** `register_rest_route('<brand>/v1', '/events', …)` with `permission_callback` that verifies `X-Webhook-Signature` (HMAC-SHA256 of `"<t>.<raw body>"` with the shop's webhook secret; reject when `|now − t| > 300 s`), de-duplicates by `X-Webhook-Id` (a transient for 24 h) and answers within 2 s. The actual repricing runs in Action Scheduler (bundled with WooCommerce) so large digests do not time out the request.

**Pull fallback:** some shops block incoming requests (security plugins, firewalls). An Action Scheduler task every hour calls `GET /v1/monitors/{id}/changes?cursor=…` and applies what it missed. Store the cursor in an option.

**Distribution:** WordPress.org forbids "trialware" but allows plugins that need an external service. Publish on WordPress.org (name, privacy section explaining which data goes to the service), and also offer a ZIP from your own site, for shops that install from Iranian marketplaces such as Zhaket and RTL-Theme.

## 3. Laravel side

* **Tables:** `monitors`, `tracked_items`, `alerts`, `notification_channels`, `wordpress_sites` (see [laravel-integration.md §1.3](../laravel-integration.md)).
* **Plan limits:** tracked URLs, minimum interval and channels. Check them when URLs are added, not when a run starts.
* **One schedule per monitor.** Up to the engine's 10,000 URLs per batch (split bigger monitors). Give schedules of the same frequency random minutes (`17 */6 * * *`) so they do not all start at once.
* **History:** keep every price change per `tracked_item` (one row per change, not per check). It powers price-history charts and later becomes a dataset product.
* **First run:** a new monitor has no baseline, so its first run reports pages as `new`. Store those values as the starting point and do not alert.

## 4. Notifications

One `Notifier` interface in Laravel with a driver per channel. A channel must be verified before alerts go to it.

| Channel | How | Notes |
|---|---|---|
| Email | Laravel Mail (an Iranian SMTP relay, so mail works during international outages) | Default; one digest per run |
| SMS | Kavenegar `verify/lookup` templated messages (or another Iranian provider: Ghasedak, Melipayamak) | Use approved templates (transactional lines are not blocked by operators' advertising blacklists). Keep it short: "۳ تغییر قیمت در فروشگاه شما: …" plus a dashboard link |
| Bale | Bale's bot API (Telegram-compatible methods such as `sendMessage`; confirm the base URL and limits in Bale's developer docs) | The shop owner links their account by opening `https://ble.ir/<bot>?start=<one-time token>`; store the chat id |
| Webhook | The customer webhook system ([customer-api.md](customer-api.md)) | For the plugin, n8n and the customer's own systems |
| Eitaa, Telegram | Later | Telegram is filtered in Iran, so do not depend on it |

Rate-limit alerts per channel (at most one digest per run and 20 SMS a day by default), and honour quiet hours (no SMS 22:00–08:00 Tehran time).

## 5. Done when (end of week 8)

* A test shop with 50 products and 3 competitors each runs for 7 days on a 6-hour schedule.
* Price changes injected on a test competitor site arrive as a digest in email, SMS and Bale within one run.
* `suggest` and `auto` modes are both tested, including floor, ceiling, rounding, Rial/Toman conversion and variable products.
* Plugin removal leaves no options or tables behind (`uninstall.php`) and does not change any prices.
* The plugin passes the WordPress Plugin Check tool, and all output is escaped and every form uses nonces.
