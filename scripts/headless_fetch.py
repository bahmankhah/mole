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

    Signs of an unrendered SPA page:
    - ng-app present but no ng-scope (AngularJS never compiled the DOM)
    - <app-root> present but empty or near-empty (Angular 2+ never rendered)
    - Multiple unresolved {{ }} template expressions (no framework signals)

    IMPORTANT: If ng-scope IS present, AngularJS compiled successfully.
    Remaining unresolved {{ }} are just lazy bindings in hidden/conditional
    sections — this is normal for large AngularJS apps.
    """
    # AngularJS — ng-scope is added when AngularJS compiles the DOM.
    if "ng-app=" in content:
        if "ng-scope" not in content:
            # Framework loaded but never compiled — definitely unrendered
            return True
        # ng-scope present → AngularJS bootstrapped OK, page is rendered
        return False
    # Angular 2+: <app-root> should be populated after bootstrap.
    # If it's empty or has only whitespace/comments, the app hasn't rendered.
    if "<app-root" in content:
        match = re.search(r"<app-root[^>]*>(.*?)</app-root>", content, re.DOTALL)
        if match:
            inner = match.group(1).strip()
            # Remove HTML comments
            inner = re.sub(r"<!--.*?-->", "", inner, flags=re.DOTALL).strip()
            if len(inner) < 50:
                return True
        return False
    # Generic SPA: unresolved template expressions (more than 10) in a small page
    # suggest the framework never ran.  Skip expressions inside <style>/<script>.
    if "{{" in content and len(content) < 50000:
        stripped = re.sub(r"<style[^>]*>.*?</style>", "", content, flags=re.DOTALL | re.IGNORECASE)
        stripped = re.sub(r"<script[^>]*>.*?</script>", "", stripped, flags=re.DOTALL | re.IGNORECASE)
        unresolved = re.findall(r"\{\{[^}]+\}\}", stripped)
        if len(unresolved) > 10:
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

            # For hash-based SPA URLs (e.g. /#/search?q=...), navigate to the
            # BASE URL without the hash first.  This lets the SPA framework
            # bootstrap on its default/home route.  We'll set the hash
            # fragment LATER (Step 4) as a genuine route change so we can
            # properly wait for the route's API calls and DOM rendering.
            if has_fragment:
                base_url = parsed._replace(fragment="").geturl()
                # Remove trailing # if present
                base_url = base_url.rstrip("#")
                nav_url = base_url
            else:
                nav_url = url

            response = page.goto(nav_url, wait_until="domcontentloaded", timeout=timeout_ms)

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
            # Framework-agnostic: we rely on two universal signals that work
            # for ANY SPA (React, Vue, Angular, Svelte, AngularJS, etc.):
            #   1) Network idle — no pending HTTP requests for 500ms
            #   2) DOM stability — no DOM mutations for 1s
            # This is exactly how a real browser "knows" a page is done.

            # Step 1: Wait for all resources (scripts, CSS, images) to load.
            try:
                page.wait_for_load_state("load", timeout=min(timeout_ms, 30000))
            except Exception:
                pass

            # Step 2: Wait for network to go idle (no requests for 500ms).
            # This catches API calls SPAs make after bootstrap.
            try:
                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 25000))
            except Exception:
                pass

            # Step 3: Wait for DOM to stop mutating — the universal SPA
            # readiness signal.  A MutationObserver resolves once no DOM
            # changes occur for 1 second.  If nothing mutates at all, it
            # resolves after 2 seconds (page was already stable).
            try:
                page.wait_for_function(
                    """() => new Promise(resolve => {
                        let timer = null;
                        const observer = new MutationObserver(() => {
                            clearTimeout(timer);
                            timer = setTimeout(() => { observer.disconnect(); resolve(true); }, 1000);
                        });
                        observer.observe(document.body, { childList: true, subtree: true, characterData: true });
                        timer = setTimeout(() => { observer.disconnect(); resolve(true); }, 2000);
                    })""",
                    timeout=min(timeout_ms, 15000),
                )
            except Exception:
                pass

            # Step 4: Hash-fragment SPA route navigation.
            # We navigated to the base URL without the hash, so the SPA
            # framework bootstrapped on its default route.  Now set the
            # actual hash fragment — this is a genuine new route change.
            # We wait for the route's API calls (networkidle) and DOM
            # rendering (mutation observer) to complete.
            if has_fragment:
                desired_hash = "#" + parsed.fragment
                page.evaluate("(h) => { window.location.hash = h; }",
                              desired_hash)
                _time.sleep(1)
                # Wait for API calls triggered by the new route
                try:
                    page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 25000))
                except Exception:
                    pass
                # Wait for DOM to stabilise after route content renders
                # (longer thresholds: 1.5s quiet for resolve, 3s max wait)
                try:
                    page.wait_for_function(
                        """() => new Promise(resolve => {
                            let t = null;
                            const o = new MutationObserver(() => {
                                clearTimeout(t);
                                t = setTimeout(() => { o.disconnect(); resolve(true); }, 1500);
                            });
                            o.observe(document.body, { childList: true, subtree: true, characterData: true });
                            t = setTimeout(() => { o.disconnect(); resolve(true); }, 3000);
                        })""",
                        timeout=min(timeout_ms, 15000),
                    )
                except Exception:
                    pass

            # Step 5: Final networkidle wait for any late API responses
            try:
                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 15000))
            except Exception:
                pass

            # Step 6: Final render delay — gives the framework time to update
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
                        page.reload(wait_until="networkidle", timeout=timeout_ms)
                    except Exception:
                        pass
                    # DOM stability check
                    try:
                        page.wait_for_function(
                            """() => new Promise(resolve => {
                                let t = null;
                                const o = new MutationObserver(() => {
                                    clearTimeout(t);
                                    t = setTimeout(() => { o.disconnect(); resolve(true); }, 1000);
                                });
                                o.observe(document.body, { childList: true, subtree: true, characterData: true });
                                t = setTimeout(() => { o.disconnect(); resolve(true); }, 2000);
                            })""",
                            timeout=min(timeout_ms, 10000),
                        )
                    except Exception:
                        pass
                    # Re-trigger hash fragment route
                    if has_fragment:
                        desired_hash = "#" + parsed.fragment
                        page.evaluate("(h) => { window.location.hash = h; }",
                                      desired_hash)
                        _time.sleep(1)
                        try:
                            page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 25000))
                        except Exception:
                            pass
                        try:
                            page.wait_for_function(
                                """() => new Promise(resolve => {
                                    let t = null;
                                    const o = new MutationObserver(() => {
                                        clearTimeout(t);
                                        t = setTimeout(() => { o.disconnect(); resolve(true); }, 1500);
                                    });
                                    o.observe(document.body, { childList: true, subtree: true, characterData: true });
                                    t = setTimeout(() => { o.disconnect(); resolve(true); }, 3000);
                                })""",
                                timeout=min(timeout_ms, 15000),
                            )
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
