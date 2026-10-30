# MCP server

An MCP (Model Context Protocol) server lets AI assistants and agents (Claude, Cursor, VS Code agents, self-built agents on Iranian LLM gateways) scrape, search and crawl through the service as tools. Firecrawl grew largely through this channel. For Mole it also covers what foreign tools cannot: Iranian sites behind geo-blocking, Persian content, and payment in Toman.

## 1. Shape

* **Package:** `@<brand>/mcp` on npm, built in TypeScript with the official `@modelcontextprotocol/sdk` and the JS SDK ([sdks.md](sdks.md)).
* **Transports:**
  * **stdio** for local clients: `npx -y @<brand>/mcp` with `<BRAND>_API_KEY` in the environment.
  * **Streamable HTTP**, hosted at `https://mcp.<brand>.ir/mcp`, for clients that connect to remote servers. Authenticate with `Authorization: Bearer <customer API key>`, or add OAuth later when clients need it. It is one stateless Node service behind the same reverse proxy as Laravel and calls the customer API with the caller's key.
* **No engine access:** the server is a plain API client. Plans, credits and limits apply as for any other customer call.

## 2. Tools

Keep the set small, with descriptions written for a model: what the tool is for, when to prefer it, and what it costs.

| Tool | Input | Output | Notes |
|---|---|---|---|
| `scrape` | `url`, `formats` (default `["markdown"]`), `only_main_content` (default true), `render` (`auto`), `max_age` (default 2 days) | Markdown plus title and URL; metadata when asked | Works for PDF and DOCX URLs. Truncate to `max_chars` (default 40,000) and say so at the end, with the arguments to read the next part (`offset`) |
| `batch_scrape` | `urls` (≤ 50 for this tool), same options | A job id, plus results when it finishes within 60 s | Larger lists go through `check_job` |
| `check_job` | `id`, `cursor` | Status and the next page of results | For batch and crawl jobs |
| `map` | `url`, `search`, `limit` | A URL list | "Use before crawl to find the pages you need" |
| `crawl` | `url`, `limit` (default 20), `max_depth` (default 2), `include_paths`, `exclude_paths` | A job id | The description warns that crawls take minutes and use credits per page |
| `search` | `query`, `limit` (default 5), `lang`, `country`, `scrape` (default false) | Results with titles and snippets, plus Markdown when `scrape` | Default `lang: fa`, `country: ir` when the query is in Persian |
| `product` | `urls` (≤ 20) | Name, price, regular price, availability, SKU, brand and image per URL, with no LLM cost | A batch scrape with `formats: ["product"]`. The description tells the model to prefer this over `extract` for prices and stock |
| `structured_data` | `urls` (≤ 20), `types` (`article`, `business`, `job_posting`, `event`, `faq`) | What each page publishes about itself: article dates and authors, a business's contact details and hours, job, event or FAQ data | A batch scrape with those formats; no LLM cost. Prefer it over `extract` when the page is a news story, business, job, event or FAQ |
| `extract` | `urls` (≤ 10), `prompt`, `schema` | Structured JSON per URL | A batch scrape with `extract.prompt`/`schema` (engine `llm_prompt`/`llm_schema`) |
| `monitor_create` | `name`, `urls`, `recipe`, `frequency` | The monitor | Optional; only exposed when the key's plan includes monitors |

Return errors as tool results with `isError: true` and the API's Persian/English message (for example "insufficient credits: top up at …"). Never throw protocol errors for business failures, because models handle tool results better than crashes.

## 3. Implementation notes

* Register tools with zod input schemas; the SDK turns them into JSON Schema for clients.
* Read `max_age`, `max_chars` and the defaults above from environment variables, so a team can tune them without a release.
* Log tool name, duration, credits and error code. Never log page content or the API key.
* Include a `README` with copy-paste configuration for the common clients (a `mcpServers` JSON entry with `command: "npx"`, `args`, `env`), in Persian and English.

## 4. Testing

* Unit tests with the API mocked from `openapi.yaml`.
* Run the server under the MCP Inspector (`npx @modelcontextprotocol/inspector`) and check every tool's schema and error path.
* A scripted session with a real assistant on Persian tasks: "قیمت گلکسی A55 را در سه فروشگاه پیدا کن", "summarise this PDF", "list this site's blog posts". Check that the model picks the right tool and that outputs fit its context.

## 5. Done when (end of week 8)

Both transports work with a sandbox key, all tools are covered by tests, the hosted endpoint is behind rate limits and monitored, and the dashboard has a "Use with AI assistants" page with the configuration snippets.
