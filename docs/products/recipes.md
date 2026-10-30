# Recipes: ready-made extraction for Iranian sites

A recipe is a named, versioned and tested set of engine settings that extracts the same fields from one kind of site: product name, price, availability and images from a WooCommerce shop or a big marketplace, or title, date and body from a news site. Customers pick a recipe instead of writing selectors. Recipes are what make the price monitor and the API useful to people who do not know CSS, so they are a moat, not a convenience.

Recipes live in Laravel (data, versions, tests). The engine only receives the settings a recipe expands to.

**Product detection comes first.** The engine's `product` format finds name, price, regular price, availability, SKU, brand, images and more without any selectors. It reads JSON-LD, microdata, RDFa and OpenGraph, and the HTML of WooCommerce, Magento, PrestaShop and OpenCart ([README](../../README.md#job-settings-reference)). That covers most shops, and replaces the planned `generic-product` recipe. Product recipes are for the rest: marketplaces rendered with JavaScript, custom themes without structured data, and fields detection does not return (specifications, seller lists, shipping time). A recipe can include `"product"` in `formats` next to its `extract_fields`. Laravel then takes each value from `extracted` when the recipe has it, and from `product` otherwise.

## 1. Format

```json
{
  "slug": "woocommerce-product",
  "version": 3,
  "name": "WooCommerce product page",
  "name_fa": "صفحه محصول ووکامرس",
  "hosts": ["*"],
  "url_pattern": "/(product|shop)/",
  "settings": {
    "headless_fallback": true,
    "formats": ["markdown", "metadata"],
    "only_main_content": true,
    "extract_fields": [
      {"name": "name", "selector": "jsonld:Product.name", "fallback_selectors": ["h1.product_title", "h1"]},
      {"name": "price", "selector": "jsonld:Product.offers.price", "type": "price",
       "fallback_selectors": [".summary .price ins .amount", ".summary .price .amount", "meta[property='product:price:amount']"]},
      {"name": "availability", "selector": "jsonld:Product.offers.availability", "regex": "(InStock|OutOfStock|PreOrder|BackOrder)"},
      {"name": "stock_text", "selector": ".summary .stock"},
      {"name": "sku", "selector": "jsonld:Product.sku", "fallback_selectors": [".sku"]},
      {"name": "image", "selector": "jsonld:Product.image", "type": "url", "attr": "content",
       "fallback_selectors": ["meta[property='og:image']"]}
    ]
  },
  "test_urls": [
    {"url": "https://shop-one.example.ir/product/sample", "expect": {"name": "non_empty", "price": "price", "availability": "non_empty"}}
  ],
  "status": "active",
  "maintainer": "data-team"
}
```

Rules:
* **JSON-LD first, CSS second.** `jsonld:Product.offers.price` survives theme changes; CSS selectors are fallbacks (`fallback_selectors`, up to 5, tried in order while the field is empty). Most WooCommerce shops, and many custom shops, publish schema.org `Product` data.
* **Know how fallbacks behave.** A field's `type`, `attr` and `regex` apply to every selector in its chain. So a fallback must produce the same kind of raw value. Example: `attr: "content"` reads `<meta content>` for a `meta[property='og:image']` fallback and is ignored by `jsonld:` selectors. When the fallback text has a different shape (Persian "موجود در انبار" versus schema.org `InStock`), use a separate field (`stock_text`) and normalise in Laravel.
* **Field names are a contract.** Every product recipe returns `name`, `price` (type `price`), `availability`, `sku`, `image`, the same names as the engine's `product` object, so monitors read both the same way. Laravel normalises a recipe's `availability` (schema.org `InStock`, Persian stock text) to the engine's codes (`in_stock`, `out_of_stock`, `preorder`, `backorder`, `limited`, `discontinued`). Monitors, alerts, the WooCommerce plugin and exports rely on these names, so add fields but never rename them.
* **`settings` must pass engine validation.** Laravel validates a recipe on save by calling the engine's `/v1/scrape` with it on every test URL (see testing below). Selectors are validated by the engine (invalid CSS/XPath/JSON-LD paths are rejected with a message).
* **Versioned.** A monitor records the recipe version it was created with. A new version is used by new monitors at once and by existing monitors after its tests pass for 3 days in a row.
* **`url_pattern`** becomes `extract_url_match` when a recipe is used in a crawl, so extraction runs only on product pages.

## 2. The first recipes (weeks 5–8)

Start with what most customers need for price monitoring, and check each target's terms and robots.txt before you publish a recipe for it:

| Slug | Covers | Notes |
|---|---|---|
| `woocommerce-product` | WooCommerce shops whose theme changes the default product markup | `product` detection already reads WooCommerce's JSON-LD and default HTML; this recipe adds CSS fallbacks for popular Persian themes |
| (none needed) | Shops with schema.org `Product` data (JSON-LD, microdata, RDFa), Open Graph product tags, or a WooCommerce, Magento, PrestaShop or OpenCart product template | Use the engine's `product` format; write a recipe only when a test shows a field missing |
| Two or three large marketplaces | One recipe each | Often rendered with JavaScript: set `use_headless_browser` or `headless_fallback`, and `headless_wait_selector` for the price element. Some expose JSON APIs used by their own pages; prefer the page unless the site permits API use |
| `news-article` | News sites | `jsonld:NewsArticle.headline`, `jsonld:NewsArticle.datePublished` (type `date`), `only_main_content` for the body |
| `job-posting` | Job boards that publish `JobPosting` data | `jsonld:JobPosting.title`, `…hiringOrganization.name`, `…datePosted` |

Do not build recipes that collect personal data (phone numbers, names of private sellers in classifieds). They create legal risk for you and your customers.

## 3. Testing and monitoring recipes

Sites change; recipe quality is what customers pay for.

* **On save:** Laravel scrapes every `test_url` through the engine with the recipe's settings (`max_age: 0`) and checks each expectation: `non_empty`, `price` (a price object with `amount > 0`), `number`, `url`, `equals:<value>`, `regex:<pattern>`. The recipe cannot be activated while a test fails.
* **Daily canary:** a Laravel scheduled job runs every active recipe's tests as one `/v1/batch/scrape` (owner `system`, so it does not bill a customer) and stores the results. Two failures in a row mark the recipe `degraded`, alert the maintainer (Bale or email) and show a banner on affected monitors.
* **Field fill rate:** for each recipe, and for `product` detection per shop host, track the share of monitor pages where each field was empty in the last 24 hours (from the `extracted` and `product` values Laravel already stores). A sudden drop is the earliest sign that a site changed. `product.sources` shows which source a field came from, which helps when a shop switches plugins.
* **Fixtures:** save the HTML of each test URL when the recipe is activated. A Go or PHP test can then run the selectors offline against the fixture. The engine's `content.Extract` is the reference implementation; `mole scrape <url> --extract fields.json -o json` runs it from the command line.

## 4. Admin UI (in the Laravel dashboard, staff only)

Recipe list with status and fill rates, a JSON editor with engine validation, a "test now" button showing the extracted values per test URL next to the expectations, version history with diff, and "promote to active".

## 5. Later

* **Customer recipes:** customers save their own field sets from the scrape playground or the Chrome extension's visual picker, private to the team.
* **Marketplace:** third-party recipe authors with a revenue share (as Apify does with Actors), once there is traffic to share.
* **Auto-suggest:** for an unknown shop, try the `product` format; if the price is empty, ask the LLM (`llm_prompt`) once to propose CSS selectors, validate them on two more product pages, and offer them as a private recipe.
