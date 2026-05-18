"""
cookie_pool.py — round-robin pool of Divar auth cookies.

Layout:
    scripts/.cookies/auth/<anything>      one auth cookie file per account
                                          (raw HTTP `Cookie:` header line —
                                          paste from DevTools)
    scripts/.cookies/auth_state.json      usage + expired flags + cursor
    scripts/.cookies/auth_state.json.lock flock target for concurrent workers

Each acquire() returns the next non-expired cookie set, round-robin. A cookie
is flagged expired when:
    - usage count reaches MAX_USES (default 60), OR
    - caller invokes mark_expired() after a 401/403/JWT-expired response.

Expired cookies are skipped on subsequent picks. When every cookie in the
pool is expired, pause_crawl_job() POSTs the local control API to halt the
running job.
"""

import fcntl
import json
import logging
import os
import re
from pathlib import Path

import requests


log = logging.getLogger("after_crawl")


COOKIES_DIR = Path(__file__).parent / ".cookies"
AUTH_DIR = COOKIES_DIR / "auth"
STATE_FILE = COOKIES_DIR / "auth_state.json"
LOCK_FILE = Path(str(STATE_FILE) + ".lock")

MAX_USES = 60
PAUSE_URL = "http://127.0.0.1:5050/api/jobs/pause"
EXPIRED_BODY_RE = re.compile(r"jwt.*expired|token.*expired|access token.*expired", re.IGNORECASE)


def _list_cookie_files() -> list[Path]:
    if not AUTH_DIR.exists():
        return []
    return sorted(
        p for p in AUTH_DIR.iterdir()
        if p.is_file() and not p.name.startswith(".") and not p.name.endswith(".lock")
    )


def _parse_cookie_file(path: Path) -> dict:
    """Parse a raw HTTP `Cookie:` header line into {name: value}."""
    try:
        text = path.read_text(encoding="utf-8").strip()
    except OSError:
        return {}
    if not text:
        return {}
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


def _load_state() -> dict:
    if not STATE_FILE.exists():
        return {"cursor": 0, "cookies": {}}
    try:
        with open(STATE_FILE, "r", encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, json.JSONDecodeError):
        return {"cursor": 0, "cookies": {}}
    doc.setdefault("cursor", 0)
    doc.setdefault("cookies", {})
    return doc


def _save_state(state: dict) -> None:
    STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    tmp = str(STATE_FILE) + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(state, f, ensure_ascii=False, indent=2)
    os.replace(tmp, STATE_FILE)


def _with_lock(fn):
    LOCK_FILE.parent.mkdir(parents=True, exist_ok=True)
    with open(LOCK_FILE, "w") as lf:
        fcntl.flock(lf.fileno(), fcntl.LOCK_EX)
        try:
            return fn()
        finally:
            fcntl.flock(lf.fileno(), fcntl.LOCK_UN)


def acquire() -> tuple[str, dict] | None:
    """
    Pick next non-expired cookie under flock. Increment its usage; if the
    new count hits MAX_USES, flip expired=True (this acquire still returns
    the cookie — the *next* call rotates past it).

    Returns (filename, {cookie_name: value}) or None if pool empty / all expired.
    """
    files = _list_cookie_files()
    if not files:
        log.warning("cookie_pool: no cookie files in %s", AUTH_DIR)
        return None

    def _pick():
        state = _load_state()
        cookies_state = state["cookies"]
        n = len(files)
        cursor = state["cursor"] % n
        for i in range(n):
            idx = (cursor + i) % n
            f = files[idx]
            entry = cookies_state.setdefault(f.name, {"used": 0, "expired": False})
            if entry["expired"]:
                continue
            entry["used"] += 1
            if entry["used"] >= MAX_USES:
                entry["expired"] = True
                log.info("cookie_pool: %s reached %d uses, expired",
                         f.name, MAX_USES)
            state["cursor"] = (idx + 1) % n
            _save_state(state)
            cookies = _parse_cookie_file(f)
            return f.name, cookies
        _save_state(state)
        return None

    return _with_lock(_pick)


def mark_expired(name: str) -> None:
    def _do():
        state = _load_state()
        entry = state["cookies"].setdefault(name, {"used": 0, "expired": False})
        entry["expired"] = True
        _save_state(state)
    _with_lock(_do)
    log.info("cookie_pool: %s marked expired", name)


def all_expired() -> bool:
    files = _list_cookie_files()
    if not files:
        return True

    def _check():
        state = _load_state()
        for f in files:
            entry = state["cookies"].get(f.name)
            if not entry or not entry.get("expired"):
                return False
        return True

    return _with_lock(_check)


def looks_expired(resp: requests.Response) -> bool:
    """True if response indicates auth failure (401/403 or JWT-expired body)."""
    if resp.status_code in (401, 403):
        return True
    try:
        body = resp.text or ""
    except Exception:
        return False
    return bool(EXPIRED_BODY_RE.search(body[:1000]))


def pause_crawl_job() -> bool:
    """POST the local control API to pause the currently running crawl job."""
    try:
        r = requests.post(PAUSE_URL, timeout=5)
        log.warning("cookie_pool: pause job HTTP %s body=%s",
                    r.status_code, (r.text or "")[:200])
        return r.status_code // 100 == 2
    except Exception as e:
        log.exception("cookie_pool: pause job failed: %s", e)
        return False
