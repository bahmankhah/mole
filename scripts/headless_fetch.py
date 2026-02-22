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

def fetch(url: str, timeout_ms: int, wait_selector: str | None, user_agent: str | None) -> dict:
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

    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(
                headless=True,
                args=[
                    "--no-sandbox",
                    "--disable-setuid-sandbox",
                    "--disable-dev-shm-usage",
                    "--disable-gpu",
                ],
            )

            context_opts = {
                "ignore_https_errors": True,
                "java_script_enabled": True,
            }
            if user_agent:
                context_opts["user_agent"] = user_agent

            context = browser.new_context(**context_opts)
            page = context.new_page()

            # Navigate to URL
            response = page.goto(url, wait_until="domcontentloaded", timeout=timeout_ms)

            if response:
                result["status_code"] = response.status
                result["content_type"] = response.headers.get("content-type", "text/html")
                result["url"] = response.url
            else:
                result["status_code"] = 200
                result["content_type"] = "text/html"

            # Wait for specific selector if provided (for SPAs that load content dynamically)
            if wait_selector:
                try:
                    page.wait_for_selector(wait_selector, timeout=timeout_ms)
                except Exception:
                    # Selector not found within timeout — continue with what we have
                    pass

            # Additional wait for any in-flight network requests to settle
            try:
                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 10000))
            except Exception:
                pass

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
    args = parser.parse_args()

    timeout_ms = args.timeout * 1000
    result = fetch(args.url, timeout_ms, args.wait_selector, args.user_agent)

    # Write JSON to stdout — Go reads this
    json.dump(result, sys.stdout, ensure_ascii=False)
    sys.stdout.flush()


if __name__ == "__main__":
    main()
