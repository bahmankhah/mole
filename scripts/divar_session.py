"""
divar_session.py — base Divar HTTP session built from the Playwright cookie jar.

Auth cookies (sAccessToken, sRefreshToken, ...) are NOT loaded here — those
come from cookie_pool.acquire() and are layered on top per request, so each
phone fetch can rotate to a different account.
"""

import json
import logging
from pathlib import Path

import requests


log = logging.getLogger("after_crawl")


COOKIE_FILE = Path(__file__).parent / ".cookies" / "divar.ir.json"

USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)


def _read_jar() -> dict:
    """Return Playwright-export cookies keyed by name. Empty if missing/invalid."""
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


def make_base_session() -> requests.Session:
    """
    Build a requests.Session pre-loaded with non-auth Divar cookies
    (did, cdid, _ga, theme, ...). Caller layers auth cookies on top.
    """
    session = requests.Session()
    session.headers.update({
        "user-agent": USER_AGENT,
        "accept-language": "en-US,en;q=0.9",
    })
    jar = _read_jar()
    for c in jar.values():
        session.cookies.set(
            c["name"],
            c.get("value", ""),
            domain=c.get("domain", ".divar.ir"),
            path=c.get("path", "/"),
        )
    log.info("session: loaded base jar=%d", len(jar))
    return session


def apply_auth_cookies(session: requests.Session, auth: dict) -> None:
    """Overlay {name: value} auth cookies onto the session."""
    for name, value in auth.items():
        session.cookies.set(name, value, domain=".divar.ir", path="/")
