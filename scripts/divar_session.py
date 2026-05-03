"""
divar_session.py — shared Divar HTTP session with cookie-jar persistence.

Cookies live in scripts/.cookies/divar.ir.json (Playwright-style export):
    {"cookies": [{"name", "value", "domain", "path", "expires", ...}, ...],
     "origins": []}

Multiple after_crawl.py workers can run in parallel, so refresh() takes an
exclusive flock on a sidecar .lock file before touching the cookie file.
"""

import fcntl
import json
import logging
import os
from pathlib import Path

import requests


log = logging.getLogger("after_crawl")


COOKIE_FILE = Path(__file__).parent / ".cookies" / "divar.ir.json"
AUTH_COOKIE_FILE = Path(__file__).parent / ".cookies" / "divar_auth.cookie"
LOCK_FILE = Path(str(AUTH_COOKIE_FILE) + ".lock")
REFRESH_URL = "https://api.divar.ir/v8/authenticate/session/refresh"

# Cookies that the refresh endpoint rotates; we only persist these back to
# divar_auth.cookie. Anything else (did, cdid, _ga, theme, ...) stays in
# divar.ir.json (Playwright export) and is left alone.
AUTH_COOKIE_NAMES = {
    "sAccessToken",
    "sRefreshToken",
    "sFrontToken",
    "sIdRefreshToken",
    "token",
    "csid",
    "sAntiCsrf",
}

USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)


def _read_jar() -> dict:
    """Return dict keyed by cookie name. Empty dict if the file is missing or invalid."""
    if not COOKIE_FILE.exists():
        return {}
    try:
        with open(COOKIE_FILE, "r", encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, json.JSONDecodeError):
        return {}
    out: dict = {}
    for c in (doc.get("cookies") or []):
        name = c.get("name")
        if name:
            out[name] = c
    return out


def _read_auth_header() -> dict:
    """
    Parse scripts/.cookies/divar_auth.cookie. The file should contain a raw
    HTTP Cookie header value (one line, "name=value; name=value; ..."), exactly
    what you copy from DevTools → Network → Request Headers → cookie.

    Returns a dict {name: value}. Missing file → empty dict.
    """
    if not AUTH_COOKIE_FILE.exists():
        return {}
    try:
        text = AUTH_COOKIE_FILE.read_text(encoding="utf-8").strip()
    except OSError:
        return {}
    if not text:
        return {}
    # Tolerate a leading "Cookie:" or "cookie:" prefix in case the user pasted the whole line.
    if text.lower().startswith("cookie:"):
        text = text.split(":", 1)[1].strip()
    out: dict = {}
    for piece in text.split(";"):
        piece = piece.strip()
        if not piece or "=" not in piece:
            continue
        name, value = piece.split("=", 1)
        out[name.strip()] = value.strip()
    return out


def _write_auth_header(name_to_value: dict) -> None:
    """Persist auth cookies back as a raw `name=value; ...` header line."""
    AUTH_COOKIE_FILE.parent.mkdir(parents=True, exist_ok=True)
    line = "; ".join(f"{n}={v}" for n, v in name_to_value.items() if v is not None)
    tmp = str(AUTH_COOKIE_FILE) + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(line + "\n")
    os.replace(tmp, AUTH_COOKIE_FILE)


def _apply_jar_to_session(session: requests.Session, jar: dict) -> None:
    for c in jar.values():
        session.cookies.set(
            c["name"],
            c.get("value", ""),
            domain=c.get("domain", ".divar.ir"),
            path=c.get("path", "/"),
        )


def make_session() -> requests.Session:
    """
    Build a requests.Session pre-loaded with Divar cookies.

    Two sources, layered:
      1. scripts/.cookies/divar.ir.json   — Playwright export from the crawl
                                            (did, cdid, _ga, theme, ...).
      2. scripts/.cookies/divar_auth.cookie — raw HTTP Cookie header you paste
                                            after logging in (sAccessToken,
                                            sRefreshToken, ...). Wins on conflict.
    """
    session = requests.Session()
    session.headers.update({
        "user-agent": USER_AGENT,
        "accept-language": "en-US,en;q=0.9",
    })
    jar = _read_jar()
    _apply_jar_to_session(session, jar)
    auth = _read_auth_header()
    for name, value in auth.items():
        session.cookies.set(name, value, domain=".divar.ir", path="/")
    log.info("session: loaded jar=%d auth=%d auth_file_exists=%s",
             len(jar), len(auth), AUTH_COOKIE_FILE.exists())
    if not auth:
        log.warning("session: %s missing or empty — phone fetch will likely 401",
                    AUTH_COOKIE_FILE)
    return session


def refresh(session: requests.Session) -> bool:
    """
    Refresh the Divar session via /v8/authenticate/session/refresh.

    Acquires an exclusive flock so concurrent workers don't stomp each other.
    On success: rotated cookies are merged into the live session AND written
    back to scripts/.cookies/divar_auth.cookie as a raw Cookie header line —
    ready for the next process to read. Returns True on 2xx.
    """
    LOCK_FILE.parent.mkdir(parents=True, exist_ok=True)
    with open(LOCK_FILE, "w") as lf:
        fcntl.flock(lf.fileno(), fcntl.LOCK_EX)
        try:
            # Re-read latest auth — another worker may have refreshed already.
            current_auth = _read_auth_header()
            for name, value in current_auth.items():
                session.cookies.set(name, value, domain=".divar.ir", path="/")

            headers = {
                "accept": "*/*",
                "cache-control": "no-cache",
                "content-length": "0",
                "origin": "https://divar.ir",
                "pragma": "no-cache",
                "priority": "u=1, i",
                "referer": "https://divar.ir/",
                "rid": "passwordless",
                "sec-ch-ua": '"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"',
                "sec-ch-ua-mobile": "?0",
                "sec-ch-ua-platform": '"Linux"',
                "sec-fetch-dest": "empty",
                "sec-fetch-mode": "cors",
                "sec-fetch-site": "same-site",
                "st-auth-mode": "cookie",
            }
            log.info("refresh: POST %s", REFRESH_URL)
            resp = session.post(REFRESH_URL, headers=headers, timeout=15)
            log.info("refresh: HTTP %s body=%s", resp.status_code, (resp.text or "")[:200])
            if resp.status_code // 100 != 2:
                raise RuntimeError(
                    f"refresh failed: HTTP {resp.status_code} body={resp.text[:200]!r}"
                )

            # Divar returns the rotated front token via a custom `Front-Token`
            # response header (not Set-Cookie). Promote it to the sFrontToken
            # cookie so subsequent requests carry it.
            front_token = resp.headers.get("Front-Token") or resp.headers.get("front-token")
            if front_token:
                session.cookies.set("sFrontToken", front_token, domain=".divar.ir", path="/")

            # Merge: keep every existing auth cookie, overlay anything the
            # session jar now holds whose name is in the auth set.
            merged = dict(current_auth)
            for c in session.cookies:
                if c.name in AUTH_COOKIE_NAMES and c.value:
                    merged[c.name] = c.value
            _write_auth_header(merged)
            return True
        finally:
            fcntl.flock(lf.fileno(), fcntl.LOCK_UN)


def request_with_refresh(session: requests.Session, method: str, url: str, **kwargs):
    """
    Perform an HTTP request with one automatic refresh+retry on auth failure
    or non-JSON response. The caller is responsible for parsing the response.

    "Auth failure" = HTTP 401 or 403.
    "Non-JSON" = caller passes expect_json=True and resp.json() raises.
    """
    expect_json = kwargs.pop("expect_json", False)

    def _looks_invalid(resp: requests.Response) -> bool:
        if resp.status_code in (401, 403):
            return True
        if expect_json:
            try:
                resp.json()
            except ValueError:
                return True
        return False

    resp = session.request(method, url, **kwargs)
    if not _looks_invalid(resp):
        return resp

    log.info("request_with_refresh: invalid resp HTTP=%s url=%s; refreshing",
             resp.status_code, url)
    try:
        refresh(session)
    except Exception as e:
        log.warning("request_with_refresh: refresh failed: %s", e)
        return resp

    return session.request(method, url, **kwargs)
