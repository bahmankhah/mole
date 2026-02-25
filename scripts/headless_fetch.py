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
import os
import re
import sys
import time as _time
from pathlib import Path
from urllib.parse import urlparse


def _cookie_state_path(cookie_dir: str | None, url: str) -> Path | None:
    """Return the storage-state file path for the domain, or None."""
    if not cookie_dir:
        return None
    domain = urlparse(url).hostname or "unknown"
    d = Path(cookie_dir)
    d.mkdir(parents=True, exist_ok=True)
    return d / f"{domain}.json"


def _load_storage_state(path: Path | None) -> str | None:
    """Load a Playwright storage-state JSON file if it exists and is fresh."""
    if path is None or not path.exists():
        return None
    try:
        age = _time.time() - path.stat().st_mtime
        if age > 3600:  # stale after 1 hour
            path.unlink(missing_ok=True)
            return None
        return str(path)
    except Exception:
        return None


def _save_storage_state(context, path: Path | None):
    """Persist cookies + localStorage so the next fetch reuses them."""
    if path is None:
        return
    try:
        context.storage_state(path=str(path))
    except Exception:
        pass


def _is_cdn_challenge(content: str, status_code: int = 200) -> bool:
    """Detect CDN/WAF challenge or block pages by content.

    ArvanCloud, Cloudflare, openresty etc. may return these with HTTP 200,
    403, or 503.  We must check the body, not just the status code.
    """
    if status_code in (403, 503):
        return True
    low = content.lower()
    # Very short pages are suspicious — real pages are usually > 2KB
    if len(content) < 1500:
        for sig in (
            "service temporarily unavailable",
            "service unavailable",
            "just a moment",          # Cloudflare
            "checking your browser",  # Cloudflare
            "arvan",                  # ArvanCloud
            "ddos protection",
            "access denied",
            "enable javascript and cookies",
        ):
            if sig in low:
                return True
    # Explicit openresty 503 pages can be larger if wrapped in HTML boilerplate.
    # Also catch when the server sends HTTP 200 but the body is an openresty
    # error page — this happens frequently with ArvanCloud CDN.
    if "503 service temporarily unavailable" in low and "openresty" in low:
        return True
    if "service temporarily unavailable" in low and "openresty" in low:
        return True
    # Very small pages (< 500 bytes) with error-like titles are almost always
    # CDN blocks, regardless of HTTP status code.
    if len(content) < 500 and "<title>" in low:
        for sig in ("503", "502", "403", "error", "unavailable", "blocked", "denied"):
            if sig in low:
                return True
    return False


def _is_spa_shell_unrendered(content: str) -> bool:
    """Detect an unrendered SPA shell (framework loaded but never bootstrapped).

    Signs of an unrendered AngularJS/React/Vue page:
    - ng-app present but no ng-scope (AngularJS never compiled the DOM)
    - Multiple unresolved {{ }} template expressions
    - ng-cloak still present (AngularJS hides elements until compiled)
    """
    # AngularJS — ng-scope is added by AngularJS when it compiles the DOM.
    # If ng-app is present but ng-scope is missing, the framework never ran.
    if "ng-app=" in content and "ng-scope" not in content:
        return True
    # ng-cloak: AngularJS removes this attribute/class after compilation.
    # If many ng-cloak elements remain, the app hasn't rendered.
    if "ng-app=" in content and content.count("ng-cloak") > 2:
        return True
    # Unresolved template expressions (more than 3)
    if "{{" in content:
        unresolved = re.findall(r"\{\{[^}]+\}\}", content)
        if len(unresolved) > 3:
            return True
    return False


def fetch(url: str, timeout_ms: int, wait_selector: str | None, user_agent: str | None, render_wait: int = 5, cookie_dir: str | None = None) -> dict:
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

    # Resolve cookie-persistence path for this domain
    state_path = _cookie_state_path(cookie_dir, url)
    loaded_state = _load_storage_state(state_path)

    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(
                headless=True,
                args=[
                    "--no-sandbox",
                    "--disable-setuid-sandbox",
                    "--disable-dev-shm-usage",
                    "--disable-gpu",
                    "--disable-blink-features=AutomationControlled",
                    "--disable-infobars",
                    "--window-size=1920,1080",
                    "--start-maximized",
                    "--lang=en-US,en",
                ],
            )

            # Default User-Agent if none provided
            effective_ua = user_agent or (
                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/131.0.0.0 Safari/537.36"
            )

            context_opts = {
                "ignore_https_errors": True,
                "java_script_enabled": True,
                "user_agent": effective_ua,
                "viewport": {"width": 1920, "height": 1080},
                "screen": {"width": 1920, "height": 1080},
                "locale": "en-US",
                "timezone_id": "America/New_York",
                "extra_http_headers": {
                    "Accept-Language": "en-US,en;q=0.9",
                    "Accept-Encoding": "gzip, deflate, br",
                    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
                    "Sec-Ch-Ua": '"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"',
                    "Sec-Ch-Ua-Mobile": "?0",
                    "Sec-Ch-Ua-Platform": '"Windows"',
                    "Upgrade-Insecure-Requests": "1",
                },
            }

            # Reuse cookies/storage from a previous fetch (CDN challenge cookies etc.)
            if loaded_state:
                context_opts["storage_state"] = loaded_state

            context = browser.new_context(**context_opts)

            # Stealth: mask automation signals before any page loads
            context.add_init_script("""
                // Hide webdriver flag
                Object.defineProperty(navigator, 'webdriver', { get: () => false });

                // Fake plugins array (headless has none by default)
                Object.defineProperty(navigator, 'plugins', {
                    get: () => [1, 2, 3, 4, 5],
                });

                // Fake languages
                Object.defineProperty(navigator, 'languages', {
                    get: () => ['en-US', 'en'],
                });

                // Remove headless from user agent if present
                if (navigator.userAgent.includes('Headless')) {
                    Object.defineProperty(navigator, 'userAgent', {
                        get: () => navigator.userAgent.replace('Headless', ''),
                    });
                }

                // Override permissions query to avoid detection
                const originalQuery = window.navigator.permissions.query;
                window.navigator.permissions.query = (parameters) =>
                    parameters.name === 'notifications'
                        ? Promise.resolve({ state: Notification.permission })
                        : originalQuery(parameters);

                // Fake chrome runtime object
                window.chrome = {
                    runtime: {},
                    loadTimes: function() {},
                    csi: function() {},
                    app: {},
                };
            """)

            page = context.new_page()

            # Parse the URL to detect hash fragments (SPA client-side routes).
            parsed = urlparse(url)
            has_fragment = bool(parsed.fragment)

            # Navigate to the full URL. Hash fragments are never sent to the
            # server, so the server/CDN sees the base URL regardless.
            response = page.goto(url, wait_until="domcontentloaded", timeout=timeout_ms)

            if response:
                result["status_code"] = response.status
                result["content_type"] = response.headers.get("content-type", "text/html")
                result["url"] = response.url
            else:
                result["status_code"] = 200
                result["content_type"] = "text/html"

            # ── CDN challenge detection (content-based) ─────────────────
            # CDNs like ArvanCloud / openresty sometimes serve a 503 block
            # page with HTTP 200 status.  We must check the body content,
            # not just the status code.
            initial_content = page.content()
            is_challenge = _is_cdn_challenge(initial_content, result["status_code"])

            if is_challenge:
                sys.stderr.write(
                    f"[headless] CDN challenge detected (status={result['status_code']}, "
                    f"len={len(initial_content)}), waiting for JS solve…\n"
                )
                # Wait for JS challenge to potentially resolve
                try:
                    page.wait_for_load_state("networkidle", timeout=10000)
                except Exception:
                    pass
                _time.sleep(3)

                # Save any cookies the challenge set
                _save_storage_state(context, state_path)

                # Check if in-page JS redirect solved the challenge
                post_challenge = page.content()
                if not _is_cdn_challenge(post_challenge, 200):
                    # Challenge solved via JS redirect — use current page
                    result["body"] = post_challenge
                    result["status_code"] = 200
                    result["url"] = page.url
                else:
                    # Challenge set cookies but page didn't change — retry
                    # navigation with the new cookies up to 2 more times.
                    for retry in range(1, 3):
                        sys.stderr.write(
                            f"[headless] CDN retry {retry} for {url}\n"
                        )
                        _time.sleep(2)
                        try:
                            retry_resp = page.goto(
                                url, wait_until="domcontentloaded",
                                timeout=timeout_ms,
                            )
                        except Exception:
                            continue
                        if retry_resp:
                            result["status_code"] = retry_resp.status
                            result["content_type"] = retry_resp.headers.get(
                                "content-type", "text/html"
                            )
                            result["url"] = retry_resp.url
                        try:
                            page.wait_for_load_state("load", timeout=min(timeout_ms, 30000))
                        except Exception:
                            pass
                        try:
                            page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 25000))
                        except Exception:
                            pass
                        _time.sleep(2)
                        retry_content = page.content()
                        _save_storage_state(context, state_path)
                        if not _is_cdn_challenge(retry_content, result["status_code"]):
                            result["body"] = retry_content
                            result["status_code"] = 200
                            break
                    else:
                        # All retries exhausted — report 503 so Go marks it retriable
                        result["body"] = page.content()
                        result["status_code"] = 503
                        result["error"] = "CDN challenge not solved after retries"
                        _save_storage_state(context, state_path)
                        browser.close()
                        return result

                # If we solved the challenge, continue with normal SPA wait
                # so the framework can bootstrap and render content.

            # Wait for specific selector if provided (for SPAs that load content dynamically)
            if wait_selector:
                try:
                    page.wait_for_selector(wait_selector, timeout=timeout_ms)
                except Exception:
                    # Selector not found within timeout — continue with what we have
                    pass

            # ── SPA rendering wait strategy ──────────────────────────────
            # Step 1: Wait for all resources (scripts, CSS) to finish loading.
            try:
                page.wait_for_load_state("load", timeout=min(timeout_ms, 30000))
            except Exception:
                pass

            # Step 2: Wait for initial network activity to settle.
            try:
                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 25000))
            except Exception:
                pass

            # Step 2b: For AngularJS apps, wait for the framework to bootstrap.
            # AngularJS adds the 'ng-scope' class to the root element after
            # compilation.  Poll for it with a generous timeout.
            _is_angular = 'ng-app=' in page.content()
            if _is_angular:
                try:
                    page.wait_for_selector(".ng-scope", timeout=min(timeout_ms, 15000))
                except Exception:
                    # Framework may not have bootstrapped yet; continue and
                    # the unrendered-shell retry below will handle it.
                    pass

            # Step 3: If the original URL had a hash fragment (SPA route), CDN
            # challenges or JS redirects may have stripped it during page load.
            # Check if the hash matches what we expect. If not, set it via JS.
            # SPA frameworks detect hash changes either via 'hashchange' event
            # listeners or internal polling (e.g. AngularJS polls every 100ms).
            # After setting the hash, we wait for the framework to process the
            # route change and make any API calls for the new view.
            if has_fragment:
                current_hash = page.evaluate("() => window.location.hash")
                desired_hash = "#" + parsed.fragment
                if current_hash != desired_hash:
                    page.evaluate("(h) => { window.location.hash = h; }",
                                  desired_hash)
                    # Wait for framework to detect hash change and start routing
                    _time.sleep(1)
                    # Wait for API calls triggered by the new route
                    try:
                        page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 20000))
                    except Exception:
                        pass
                    # For AngularJS: after hash change, wait for $digest cycle
                    # to process the route and render the new view.
                    if _is_angular:
                        try:
                            page.wait_for_function(
                                """() => {
                                    // Check that AngularJS has finished digesting
                                    try {
                                        var root = document.querySelector('[ng-app]') || document.querySelector('.ng-scope');
                                        if (!root) return false;
                                        var scope = angular.element(root).scope();
                                        if (!scope) return false;
                                        return !scope.$$phase;
                                    } catch(e) { return true; }
                                }""",
                                timeout=10000,
                            )
                        except Exception:
                            pass
                        _time.sleep(2)  # extra time for API response + DOM update

            # Step 4: Final networkidle wait for any late API responses
            try:
                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 15000))
            except Exception:
                pass

            # Step 5: Final render delay — gives the framework time to update
            # the DOM with the data it received from API calls.
            _time.sleep(render_wait)

            # Get the fully rendered HTML
            result["body"] = page.content()

            # ── Final CDN challenge check ────────────────────────────────
            # Even after SPA waits, if we still have a CDN block page,
            # report it as 503 so Go can retry properly.
            if _is_cdn_challenge(result["body"], result["status_code"]):
                result["status_code"] = 503
                result["error"] = "CDN challenge page in final content"
                _save_storage_state(context, state_path)
                browser.close()
                return result

            # ── SPA render verification ──────────────────────────────────
            # If the content looks like an unrendered SPA shell (e.g.
            # AngularJS never bootstrapped), retry with a full page reload.
            # This handles cases where the CDN served a stale response or
            # the framework load race was lost.
            if _is_spa_shell_unrendered(result["body"]):
                for attempt in range(1, 4):  # up to 3 retries
                    sys.stderr.write(
                        f"[headless] SPA shell unrendered (attempt {attempt}), "
                        f"retrying {url}\n"
                    )
                    # Increasing backoff: 2s, 4s, 6s between retries
                    _time.sleep(attempt * 2)
                    try:
                        page.reload(wait_until="domcontentloaded", timeout=timeout_ms)
                    except Exception:
                        pass
                    try:
                        page.wait_for_load_state("load", timeout=min(timeout_ms, 30000))
                    except Exception:
                        pass
                    try:
                        page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 25000))
                    except Exception:
                        pass
                    # Wait for AngularJS to bootstrap
                    if 'ng-app=' in page.content():
                        try:
                            page.wait_for_selector(".ng-scope", timeout=min(timeout_ms, 15000))
                        except Exception:
                            pass
                    # Re-set hash fragment if needed
                    if has_fragment:
                        current_hash = page.evaluate("() => window.location.hash")
                        desired_hash = "#" + parsed.fragment
                        if current_hash != desired_hash:
                            page.evaluate("(h) => { window.location.hash = h; }",
                                          desired_hash)
                            _time.sleep(1)
                            try:
                                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 20000))
                            except Exception:
                                pass
                    _time.sleep(render_wait)
                    result["body"] = page.content()
                    if not _is_spa_shell_unrendered(result["body"]):
                        break  # successfully rendered

            # Flag unrendered SPA shells in the result so Go can log/handle them
            if _is_spa_shell_unrendered(result["body"]):
                if not result["error"]:
                    result["error"] = "SPA shell not rendered after retries"

            # Persist cookies for future fetches
            _save_storage_state(context, state_path)

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
    parser.add_argument("--render-wait", type=int, default=5, help="Extra seconds to wait after network idle for SPA rendering")
    parser.add_argument("--cookie-dir", default=None, help="Directory to persist cookies between fetches (per domain)")
    args = parser.parse_args()

    timeout_ms = args.timeout * 1000
    result = fetch(args.url, timeout_ms, args.wait_selector, args.user_agent, args.render_wait, args.cookie_dir)

    # Write JSON to stdout — Go reads this
    json.dump(result, sys.stdout, ensure_ascii=False)
    sys.stdout.flush()


if __name__ == "__main__":
    main()
