#!/usr/bin/env python3
"""
Headless browser page fetcher for the crawler engine.

Called by Go as a subprocess. Fetches a URL using a real Chromium browser,
waits for JavaScript to render, and returns the fully rendered HTML.

Usage (single-shot):
    python3 headless_fetch.py <url> [--timeout <seconds>] [--wait-selector <css>] [--user-agent <ua>]
                              [--render-wait <s>] [--cookie-dir <dir>] [--screenshot] [--full-page]
                              [--block-private-networks]

Output (JSON to stdout):
    {
        "url": "https://...",          // final URL after redirects
        "status_code": 200,
        "content_type": "text/html",
        "body": "<html>...</html>",    // rendered HTML
        "error": "",                   // non-empty on failure
        "screenshot": "",              // base64 PNG when --screenshot
        "action_results": [],
        "timings": {"total_ms": 1234}
    }

Usage (serve mode):
    python3 headless_fetch.py --serve

    Launches one browser and reads newline-delimited JSON requests from stdin,
    processing them sequentially. Writes exactly one JSON line per request to
    stdout (logs go to stderr). On startup writes {"type":"ready"}.
      {"type":"ping"}                         -> {"type":"pong"}
      {"id":..,"url":..,<options>}            -> {"id":..,<result fields as above>}
      malformed line                          -> {"id":null,"error":"bad request: ..."}
    Options: timeout, render_wait, wait_selector, user_agent, headers, proxy,
    cookie_dir, locale, timezone, screenshot, full_page, block_resources,
    block_private_networks, actions (wait, wait_for_selector, click, scroll,
    type, press, evaluate). EOF on stdin closes the browser and exits 0.

Requirements:
    pip install playwright
    playwright install chromium
"""

import argparse
import base64
import io
import ipaddress
import json
import os
import re
import socket
import sys
import time as _time
from time import monotonic as _monotonic
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


# ─────────────────────────────────────────────────────────────────────
# Browser launch / context configuration (shared by CLI and serve mode)
# ─────────────────────────────────────────────────────────────────────

_DEFAULT_UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
    "AppleWebKit/537.36 (KHTML, like Gecko) "
    "Chrome/131.0.0.0 Safari/537.36"
)

_DEFAULT_LOCALE = "en-US"
_DEFAULT_TIMEZONE = "America/New_York"

_STEALTH_SCRIPT = """
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
            """

_DOM_STABLE_1000_2000 = """() => new Promise(resolve => {
                        let timer = null;
                        const observer = new MutationObserver(() => {
                            clearTimeout(timer);
                            timer = setTimeout(() => { observer.disconnect(); resolve(true); }, 1000);
                        });
                        observer.observe(document.body, { childList: true, subtree: true, characterData: true });
                        timer = setTimeout(() => { observer.disconnect(); resolve(true); }, 2000);
                    })"""

_DOM_STABLE_1500_3000 = """() => new Promise(resolve => {
                            let t = null;
                            const o = new MutationObserver(() => {
                                clearTimeout(t);
                                t = setTimeout(() => { o.disconnect(); resolve(true); }, 1500);
                            });
                            o.observe(document.body, { childList: true, subtree: true, characterData: true });
                            t = setTimeout(() => { o.disconnect(); resolve(true); }, 3000);
                        })"""


def _launch_browser(p):
    """Launch Chromium with the stealth-friendly args used for every fetch."""
    launch_args = [
        "--no-sandbox",
        "--disable-setuid-sandbox",
        "--disable-dev-shm-usage",
        "--disable-gpu",
        "--disable-blink-features=AutomationControlled",
        "--disable-infobars",
        "--window-size=1920,1080",
        "--start-maximized",
        "--lang=en-US,en",
    ]
    chrome = os.environ.get("PLAYWRIGHT_CHROME_PATH", "/usr/bin/google-chrome")
    launch_opts = {"headless": True, "args": launch_args}
    if os.path.exists(chrome):
        launch_opts["executable_path"] = chrome
    return p.chromium.launch(**launch_opts)


def _base_headers() -> dict:
    return {
        "Accept-Language": "en-US,en;q=0.9",
        "Accept-Encoding": "gzip, deflate, br",
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
        "Sec-Ch-Ua": '"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"',
        "Sec-Ch-Ua-Mobile": "?0",
        "Sec-Ch-Ua-Platform": '"Windows"',
        "Upgrade-Insecure-Requests": "1",
    }


def _merge_headers(base: dict, extra) -> dict:
    """Merge extra headers over base, replacing keys case-insensitively."""
    out = dict(base)
    if not isinstance(extra, dict):
        return out
    for k, v in extra.items():
        if v is None:
            continue
        lk = str(k).lower()
        for existing in list(out.keys()):
            if existing.lower() == lk:
                del out[existing]
        out[str(k)] = str(v)
    return out


def _normalize_options(req: dict) -> dict:
    """Turn a request dict (serve mode) or CLI args into a normalized options dict."""
    url = req.get("url")
    if not isinstance(url, str) or not url:
        raise ValueError("missing url")

    def _num(name, default):
        v = req.get(name)
        if v is None:
            return default
        try:
            return float(v)
        except (TypeError, ValueError):
            raise ValueError(f"invalid {name}")

    timeout_s = _num("timeout", 30)
    render_wait = _num("render_wait", 5)
    if render_wait < 0:
        render_wait = 0
    actions = req.get("actions") or []
    if not isinstance(actions, list):
        raise ValueError("actions must be a list")
    block_resources = req.get("block_resources") or []
    if not isinstance(block_resources, list):
        raise ValueError("block_resources must be a list")
    proxy = req.get("proxy")
    if proxy is not None:
        if isinstance(proxy, str):
            proxy = {"server": proxy} if proxy else None
        elif not isinstance(proxy, dict) or not proxy.get("server"):
            raise ValueError("proxy must be an object with a server field")
        else:
            proxy = {k: str(v) for k, v in proxy.items() if k in ("server", "username", "password", "bypass") and v}
    return {
        "url": url,
        "timeout_ms": int(timeout_s * 1000),
        "render_wait": render_wait,
        "wait_selector": req.get("wait_selector") or None,
        "user_agent": req.get("user_agent") or None,
        "headers": req.get("headers") or {},
        "proxy": proxy,
        "cookie_dir": req.get("cookie_dir") or None,
        "locale": req.get("locale") or _DEFAULT_LOCALE,
        "timezone": req.get("timezone") or _DEFAULT_TIMEZONE,
        "screenshot": bool(req.get("screenshot")),
        "full_page": bool(req.get("full_page")),
        "block_resources": {str(x).lower() for x in block_resources},
        "block_private_networks": bool(req.get("block_private_networks")),
        "actions": actions,
    }


# ─────────────────────────────────────────────────────────────────────
# Private-network blocking
# ─────────────────────────────────────────────────────────────────────

_BLOCKED_PRIVATE_ERR = "blocked: private network address"


def _ip_is_blocked(ip) -> bool:
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped is not None:
        ip = ip.ipv4_mapped
    return (
        ip.is_private
        or ip.is_loopback
        or ip.is_link_local
        or ip.is_multicast
        or ip.is_reserved
        or ip.is_unspecified
    )


def _parse_ip_literal(host: str):
    h = host.strip("[]")
    if "%" in h:  # IPv6 zone id
        h = h.split("%", 1)[0]
    try:
        return ipaddress.ip_address(h)
    except ValueError:
        return None


class _HostGuard:
    """Decides whether a hostname points at a private network. Caches DNS."""

    def __init__(self, resolve_dns: bool):
        self.resolve_dns = resolve_dns
        self.cache: dict[str, bool] = {}

    def is_blocked_url(self, url: str) -> bool:
        try:
            parsed = urlparse(url)
        except Exception:
            return False
        if parsed.scheme not in ("http", "https", "ws", "wss", "ftp"):
            return False
        host = (parsed.hostname or "").lower()
        if not host:
            return False
        return self.is_blocked_host(host)

    def is_blocked_host(self, host: str) -> bool:
        if host in self.cache:
            return self.cache[host]
        blocked = False
        ip = _parse_ip_literal(host)
        if ip is not None:
            blocked = _ip_is_blocked(ip)
        elif self.resolve_dns:
            try:
                infos = socket.getaddrinfo(host, None)
            except Exception:
                infos = []  # unresolvable: the browser will fail on its own
            for info in infos:
                addr = _parse_ip_literal(str(info[4][0]))
                if addr is not None and _ip_is_blocked(addr):
                    blocked = True
                    break
        self.cache[host] = blocked
        return blocked


# ─────────────────────────────────────────────────────────────────────
# Post-render actions
# ─────────────────────────────────────────────────────────────────────

def _json_safe(value):
    try:
        json.dumps(value)
        return value
    except Exception:
        return str(value)


def _run_actions(page, actions: list, timeout_ms: int) -> list:
    results = []
    for raw in actions:
        act = raw if isinstance(raw, dict) else {}
        atype = str(act.get("type", "")).lower()
        entry = {"type": atype, "ok": True, "error": "", "value": None}
        try:
            ms = act.get("ms")
            act_timeout = int(ms) if ms is not None else min(timeout_ms, 10000)
            selector = act.get("selector")
            if atype == "wait":
                page.wait_for_timeout(int(ms if ms is not None else 1000))
            elif atype == "wait_for_selector":
                if not selector:
                    raise ValueError("selector required")
                page.wait_for_selector(selector, timeout=act_timeout)
            elif atype == "click":
                if not selector:
                    raise ValueError("selector required")
                page.click(selector, timeout=act_timeout)
            elif atype == "scroll":
                value = act.get("value", "bottom")
                count = max(1, int(act.get("count") or 1))
                for i in range(count):
                    if value == "bottom" or value is None:
                        page.evaluate("() => window.scrollTo(0, document.documentElement.scrollHeight || document.body.scrollHeight)")
                    elif value == "top":
                        page.evaluate("() => window.scrollTo(0, 0)")
                    else:
                        page.evaluate("(y) => window.scrollBy(0, y)", float(value))
                    page.wait_for_timeout(500)
                    if count > 1:
                        try:
                            page.wait_for_load_state("networkidle", timeout=2000)
                        except Exception:
                            pass
            elif atype == "type":
                if not selector:
                    raise ValueError("selector required")
                page.fill(selector, str(act.get("value", "")), timeout=act_timeout)
            elif atype == "press":
                key = str(act.get("value") or act.get("key") or "")
                if not key:
                    raise ValueError("value (key) required")
                if selector:
                    page.press(selector, key, timeout=act_timeout)
                else:
                    page.keyboard.press(key)
            elif atype == "evaluate":
                script = act.get("script") or act.get("value")
                if not script:
                    raise ValueError("script required")
                entry["value"] = _json_safe(page.evaluate(script))
            else:
                raise ValueError(f"unknown action type: {atype!r}")
        except Exception as e:
            entry["ok"] = False
            entry["error"] = str(e)
        results.append(entry)
    return results


# ─────────────────────────────────────────────────────────────────────
# Core fetch (shared by CLI and serve mode)
# ─────────────────────────────────────────────────────────────────────

def _new_result(url: str) -> dict:
    return {
        "url": url,
        "status_code": 0,
        "content_type": "",
        "body": "",
        "error": "",
        "screenshot": "",
        "action_results": [],
        "timings": {"total_ms": 0},
    }


def fetch_with_browser(browser, opts: dict) -> dict:
    """Fetch opts["url"] in a fresh context of an already-launched browser.

    The browser is never closed here; the context always is.
    """
    t0 = _monotonic()
    url = opts["url"]
    timeout_ms = opts["timeout_ms"]
    wait_selector = opts["wait_selector"]
    render_wait = opts["render_wait"]

    result = _new_result(url)
    context = None
    page = None
    blocked_nav = {"hit": False}

    try:
        # Resolve cookie-persistence path for this domain
        state_path = _cookie_state_path(opts["cookie_dir"], url)
        loaded_state = _load_storage_state(state_path)

        guard = None
        if opts["block_private_networks"]:
            guard = _HostGuard(resolve_dns=not opts["proxy"])
            if guard.is_blocked_url(url):
                result["error"] = _BLOCKED_PRIVATE_ERR
                return result

        # Default User-Agent if none provided
        effective_ua = opts["user_agent"] or _DEFAULT_UA

        context_opts = {
            "ignore_https_errors": True,
            "java_script_enabled": True,
            "user_agent": effective_ua,
            "viewport": {"width": 1920, "height": 1080},
            "screen": {"width": 1920, "height": 1080},
            "locale": opts["locale"],
            "timezone_id": opts["timezone"],
            "extra_http_headers": _merge_headers(_base_headers(), opts["headers"]),
        }
        if opts["proxy"]:
            context_opts["proxy"] = opts["proxy"]

        block_types = opts["block_resources"]
        use_route = bool(block_types) or guard is not None
        if use_route:
            # Service workers would bypass route interception.
            context_opts["service_workers"] = "block"

        # Reuse cookies/storage from a previous fetch (CDN challenge cookies etc.)
        if loaded_state:
            context_opts["storage_state"] = loaded_state

        context = browser.new_context(**context_opts)

        # Stealth: mask automation signals before any page loads
        context.add_init_script(_STEALTH_SCRIPT)

        if use_route:
            def _route_handler(route):
                req = route.request
                try:
                    if block_types and req.resource_type in block_types:
                        route.abort("blockedbyclient")
                        return
                    if guard is not None and guard.is_blocked_url(req.url):
                        try:
                            if req.is_navigation_request() and req.frame.parent_frame is None:
                                blocked_nav["hit"] = True
                        except Exception:
                            pass
                        route.abort("blockedbyclient")
                        return
                    route.continue_()
                except Exception:
                    # Route may already be handled / page closed.
                    pass

            context.route("**/*", _route_handler)

        def _nav_blocked(final_url: str) -> bool:
            # Redirects are not re-routed by Playwright, so re-check the
            # landing URL of every main-frame navigation.
            return guard is not None and (blocked_nav["hit"] or guard.is_blocked_url(final_url))

        def _blocked_return():
            result["status_code"] = 0
            result["body"] = ""
            result["content_type"] = ""
            result["error"] = _BLOCKED_PRIVATE_ERR
            return result

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

        try:
            response = page.goto(nav_url, wait_until="domcontentloaded", timeout=timeout_ms)
        except Exception:
            if blocked_nav["hit"]:
                return _blocked_return()
            raise

        if response:
            if _nav_blocked(response.url):
                return _blocked_return()
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
                    if _nav_blocked(page.url):
                        return _blocked_return()
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
            page.wait_for_function(_DOM_STABLE_1000_2000, timeout=min(timeout_ms, 15000))
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
                page.wait_for_function(_DOM_STABLE_1500_3000, timeout=min(timeout_ms, 15000))
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

        if _nav_blocked(page.url):
            return _blocked_return()

        # ── Final CDN challenge check ────────────────────────────────
        # Even after SPA waits, if we still have a CDN block page,
        # report it as 503 so Go can retry properly.
        if _is_cdn_challenge(result["body"], result["status_code"]):
            result["status_code"] = 503
            result["error"] = "CDN challenge page in final content"
            _save_storage_state(context, state_path)
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
                    page.wait_for_function(_DOM_STABLE_1000_2000, timeout=min(timeout_ms, 10000))
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
                        page.wait_for_function(_DOM_STABLE_1500_3000, timeout=min(timeout_ms, 15000))
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

        # ── Post-render actions ──────────────────────────────────────
        if opts["actions"]:
            result["action_results"] = _run_actions(page, opts["actions"], timeout_ms)
            try:
                page.wait_for_load_state("networkidle", timeout=min(timeout_ms, 5000))
            except Exception:
                pass
            result["body"] = page.content()
            result["url"] = page.url
            if _nav_blocked(page.url):
                return _blocked_return()
            _save_storage_state(context, state_path)

    except Exception as e:
        result["error"] = str(e)

    finally:
        if opts["screenshot"] and page is not None and result["error"] != _BLOCKED_PRIVATE_ERR:
            try:
                png = page.screenshot(full_page=opts["full_page"], type="png",
                                      timeout=min(timeout_ms, 30000))
                result["screenshot"] = base64.b64encode(png).decode("ascii")
            except Exception as e:
                sys.stderr.write(f"[headless] screenshot failed: {e}\n")
        if context is not None:
            try:
                context.close()
            except Exception:
                pass
        result["timings"] = {"total_ms": int((_monotonic() - t0) * 1000)}

    return result


def fetch(url: str, timeout_ms: int, wait_selector: str | None, user_agent: str | None,
          render_wait: int = 5, cookie_dir: str | None = None, **extra) -> dict:
    """Single-shot fetch: launch a browser, fetch one URL, close the browser."""
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        result = _new_result(url)
        result["error"] = "playwright is not installed. Run: pip install playwright && playwright install chromium"
        return result

    req = dict(extra)
    req.update({
        "url": url,
        "timeout": timeout_ms / 1000.0,
        "wait_selector": wait_selector,
        "user_agent": user_agent,
        "render_wait": render_wait,
        "cookie_dir": cookie_dir,
    })
    try:
        opts = _normalize_options(req)
    except ValueError as e:
        result = _new_result(url)
        result["error"] = f"bad request: {e}"
        return result

    result = _new_result(url)
    try:
        with sync_playwright() as p:
            browser = _launch_browser(p)
            try:
                result = fetch_with_browser(browser, opts)
            finally:
                try:
                    browser.close()
                except Exception:
                    pass
    except Exception as e:
        result["error"] = str(e)
    return result


# ─────────────────────────────────────────────────────────────────────
# Serve mode: long-lived process, NDJSON over stdin/stdout
# ─────────────────────────────────────────────────────────────────────

def _browser_alive(browser) -> bool:
    try:
        return browser is not None and browser.is_connected()
    except Exception:
        return False


def serve() -> int:
    # Keep a private handle on the real stdout and route everything else
    # (stray prints from libraries) to stderr so the protocol stays clean.
    out = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace",
                           newline="\n", line_buffering=False)
    sys.stdout = sys.stderr

    def emit(obj: dict):
        out.write(json.dumps(obj, ensure_ascii=False) + "\n")
        out.flush()

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        sys.stderr.write("[headless] playwright is not installed\n")
        return 1

    try:
        with sync_playwright() as p:
            browser = _launch_browser(p)
            emit({"type": "ready"})

            def relaunch():
                nonlocal browser
                sys.stderr.write("[headless] browser disconnected, relaunching\n")
                try:
                    browser.close()
                except Exception:
                    pass
                browser = _launch_browser(p)

            while True:
                line = sys.stdin.readline()
                if not line:
                    break  # EOF
                line = line.strip()
                if not line:
                    continue
                try:
                    req = json.loads(line)
                    if not isinstance(req, dict):
                        raise ValueError("request must be a JSON object")
                except Exception as e:
                    emit({"id": None, "error": f"bad request: {e}"})
                    continue

                req_id = req.get("id")
                rtype = req.get("type") or "fetch"
                if rtype == "ping":
                    resp = {"type": "pong"}
                    if req_id is not None:
                        resp["id"] = req_id
                    emit(resp)
                    continue
                if rtype != "fetch":
                    emit({"id": req_id, "error": f"bad request: unknown type {rtype!r}"})
                    continue

                try:
                    opts = _normalize_options(req)
                except ValueError as e:
                    result = _new_result(str(req.get("url") or ""))
                    result["id"] = req_id
                    result["error"] = f"bad request: {e}"
                    emit(result)
                    continue

                try:
                    if not _browser_alive(browser):
                        relaunch()
                    result = fetch_with_browser(browser, opts)
                    if result["error"] and not _browser_alive(browser):
                        # Browser crashed mid-request: relaunch and retry once.
                        relaunch()
                        result = fetch_with_browser(browser, opts)
                except Exception as e:
                    result = _new_result(opts["url"])
                    result["error"] = f"browser error: {e}"

                out_obj = {"id": req_id}
                out_obj.update(result)
                emit(out_obj)

            try:
                browser.close()
            except Exception:
                pass
    except BrokenPipeError:
        return 0
    return 0


def main():
    if "--serve" in sys.argv[1:]:
        sys.exit(serve())

    parser = argparse.ArgumentParser(description="Headless browser page fetcher")
    parser.add_argument("url", help="URL to fetch")
    parser.add_argument("--timeout", type=int, default=30, help="Timeout in seconds")
    parser.add_argument("--wait-selector", default=None, help="CSS selector to wait for")
    parser.add_argument("--user-agent", default=None, help="Custom User-Agent string")
    parser.add_argument("--render-wait", type=int, default=5, help="Extra seconds to wait after network idle for SPA rendering")
    parser.add_argument("--cookie-dir", default=None, help="Directory to persist cookies between fetches (per domain)")
    parser.add_argument("--screenshot", action="store_true", help="Include a base64 PNG screenshot in the output")
    parser.add_argument("--full-page", action="store_true", help="Screenshot the full page instead of the viewport")
    parser.add_argument("--block-private-networks", action="store_true", help="Refuse requests to private/loopback/link-local addresses")
    args = parser.parse_args()

    timeout_ms = args.timeout * 1000
    result = fetch(args.url, timeout_ms, args.wait_selector, args.user_agent, args.render_wait, args.cookie_dir,
                   screenshot=args.screenshot, full_page=args.full_page,
                   block_private_networks=args.block_private_networks)

    # Write JSON to stdout — Go reads this
    json.dump(result, sys.stdout, ensure_ascii=False)
    sys.stdout.flush()


if __name__ == "__main__":
    main()
