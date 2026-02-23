#!/usr/bin/env python3
"""
Headless browser page fetcher for the crawler engine.

Called by Go as a subprocess. Fetches a URL using a real Chromium browser,
waits for JavaScript to render, and returns the fully rendered HTML.

Usage:
    python3 headless_fetch.py <url> [--timeout <seconds>] [--wait-selector <css>] [--user-agent <ua>]

Output (JSON to stdout):
    {
        "url": "https://...",          // final URL after redirects
        "status_code": 200,
        "content_type": "text/html",
        "body": "<html>...</html>",    // rendered HTML
        "error": ""                    // non-empty on failure
    }

Requirements:
    pip install playwright
    playwright install chromium
"""

import argparse
import json
import sys
import time as _time
import re as _re


def _is_spa_url(url: str) -> bool:
    """Detect if a URL is likely an SPA route (hash-based or known SPA patterns)."""
    return "#/" in url or "/#" in url


def _body_size(page) -> int:
    """Return the approximate text length of the current page body."""
    try:
        return page.evaluate("document.body ? document.body.innerText.length : 0")
    except Exception:
        return 0


def fetch(url: str, timeout_ms: int, wait_selector: str | None, user_agent: str | None, chrome_path: str | None = None) -> dict:
    """Fetch a URL with a headless Chromium browser and return rendered content."""
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        return {
            "url": url,
            "status_code": 0,
            "content_type": "",
            "body": "",
            "error": "playwright is not installed. Run: pip install playwright && playwright install chromium",
        }

    result = {
        "url": url,
        "status_code": 0,
        "content_type": "",
        "body": "",
        "error": "",
    }

    is_spa = _is_spa_url(url)

    try:
        with sync_playwright() as p:
            launch_opts = {
                "headless": True,
                "args": [
                    "--no-sandbox",
                    "--disable-setuid-sandbox",
                    "--disable-dev-shm-usage",
                    "--disable-gpu",
                    # Reduce bot-detection fingerprint
                    "--disable-blink-features=AutomationControlled",
                ],
            }
            # Use system Chrome if a path is provided or auto-detect it
            if chrome_path:
                launch_opts["executable_path"] = chrome_path
            else:
                # Auto-detect system Chrome/Chromium
                import shutil
                for candidate in ["google-chrome", "google-chrome-stable", "chromium-browser", "chromium"]:
                    path = shutil.which(candidate)
                    if path:
                        launch_opts["executable_path"] = path
                        break

            browser = p.chromium.launch(**launch_opts)

            context_opts = {
                "ignore_https_errors": True,
                "java_script_enabled": True,
                # Common viewport so pages don't serve mobile/bot variants
                "viewport": {"width": 1920, "height": 1080},
                "locale": "en-US",
            }
            if user_agent:
                context_opts["user_agent"] = user_agent

            context = browser.new_context(**context_opts)

            # Remove the navigator.webdriver flag that many bot-detectors check
            context.add_init_script("""
                Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
            """)

            page = context.new_page()

            # --- Step 1: Navigate ---
            # For SPAs, wait for "load" (all resources) instead of just "domcontentloaded"
            # because the SPA shell loads fast but the app JS needs all resources ready.
            wait_event = "load" if is_spa else "domcontentloaded"
            response = page.goto(url, wait_until=wait_event, timeout=timeout_ms)

            if response:
                result["status_code"] = response.status
                result["content_type"] = response.headers.get("content-type", "text/html")
                result["url"] = response.url
            else:
                result["status_code"] = 200
                result["content_type"] = "text/html"

            # --- Step 2: Wait for specific selector (if provided) ---
            if wait_selector:
                try:
                    page.wait_for_selector(wait_selector, timeout=timeout_ms)
                except Exception:
                    # Selector not found within timeout — continue with what we have
                    pass

            # --- Step 3: Wait for network to settle ---
            # Use up to 60% of the total timeout for networkidle (min 15s, max timeout)
            idle_timeout = max(15000, int(timeout_ms * 0.6))
            idle_timeout = min(idle_timeout, timeout_ms)
            try:
                page.wait_for_load_state("networkidle", timeout=idle_timeout)
            except Exception:
                pass

            # --- Step 4: SPA extra wait ---
            # For SPA/hash URLs, the router activates AFTER page load. Give the
            # framework time to render the route and fetch API data.
            if is_spa and not wait_selector:
                # Poll for meaningful content: wait until body has > 200 chars
                # of text, checking every 500ms for up to 10 seconds.
                deadline = _time.time() + min(10, timeout_ms / 1000 * 0.3)
                while _time.time() < deadline:
                    if _body_size(page) > 200:
                        break
                    _time.sleep(0.5)
                # One more small settle after content appears
                _time.sleep(1)

            # --- Step 5: Final settle ---
            # Small fixed delay for any last-moment rendering (CSS transitions, etc.)
            _time.sleep(0.5)

            # Get the fully rendered HTML
            result["body"] = page.content()

            browser.close()

    except Exception as e:
        result["error"] = str(e)

    return result


def main():
    parser = argparse.ArgumentParser(description="Headless browser page fetcher")
    parser.add_argument("url", help="URL to fetch")
    parser.add_argument("--timeout", type=int, default=30, help="Timeout in seconds")
    parser.add_argument("--wait-selector", default=None, help="CSS selector to wait for")
    parser.add_argument("--user-agent", default=None, help="Custom User-Agent string")
    parser.add_argument("--chrome-path", default=None, help="Path to Chrome/Chromium executable")
    args = parser.parse_args()

    timeout_ms = args.timeout * 1000
    result = fetch(args.url, timeout_ms, args.wait_selector, args.user_agent, args.chrome_path)

    # Write JSON to stdout — Go reads this
    json.dump(result, sys.stdout, ensure_ascii=False)
    sys.stdout.flush()


if __name__ == "__main__":
    main()
