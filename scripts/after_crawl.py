#!/usr/bin/env python3
"""
after_crawl.py — Custom after-crawl hook for the Mole crawler.

Invoked by the crawler engine after each successfully crawled page when
"Run After-Crawl Script" is enabled in the job settings.

Input  (stdin): JSON object with the following fields:
    url          (str)  — the crawled page URL
    status_code  (int)  — HTTP response status code
    content_type (str)  — response Content-Type header
    depth        (int)  — crawl depth of this page
    body         (str)  — raw response body (HTML / text)
    job_id       (str)  — UUID of the crawl job

Output (stdout): any text; logged by the engine as [AfterCrawl] <url> => <output>.
Stderr and non-zero exit codes are logged as errors but do not abort the crawl.

Example use-cases:
    - Extract and export specific data to a file or external API
    - Detect patterns / keywords not supported by the phrase engine
    - Send alerts when particular content is found
    - Feed content into an external pipeline

Runs inside scripts/.venv — add dependencies to scripts/requirements.txt
and run scripts/setup_python.sh to install them.
"""

import fcntl
import json
import logging
import re
import sys
import os

from divar_session import make_session, request_with_refresh


DATA_DIR = os.path.join(os.path.dirname(__file__), "after_crawl_data")
LOG_FILE = os.path.join(os.path.dirname(__file__), "after_crawl.log")


def _setup_logger() -> logging.Logger:
    log = logging.getLogger("after_crawl")
    if log.handlers:
        return log
    log.setLevel(logging.INFO)
    handler = logging.FileHandler(LOG_FILE, encoding="utf-8")
    handler.setFormatter(logging.Formatter(
        "%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    ))
    log.addHandler(handler)
    return log


log = _setup_logger()


def append_entry(job_id: str, entry: dict) -> None:
    """Append an entry to scripts/after_crawl_data/{job_id}.json under flock.

    File shape: {"data": [entry, entry, ...]}. The after_job.py script consumes
    the same file and may rewrite or remove it once entries are processed.
    """
    if not job_id:
        return
    os.makedirs(DATA_DIR, exist_ok=True)
    path = os.path.join(DATA_DIR, f"{job_id}.json")

    # Open with read+write, creating if needed. Lock exclusively, mutate, write.
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o644)
    try:
        with os.fdopen(fd, "r+", encoding="utf-8") as f:
            fcntl.flock(f.fileno(), fcntl.LOCK_EX)
            try:
                raw = f.read()
                if raw.strip():
                    try:
                        doc = json.loads(raw)
                        if not isinstance(doc, dict) or not isinstance(doc.get("data"), list):
                            doc = {"data": []}
                    except json.JSONDecodeError:
                        doc = {"data": []}
                else:
                    doc = {"data": []}
                doc["data"].append(entry)
                f.seek(0)
                f.truncate()
                json.dump(doc, f, ensure_ascii=False)
            finally:
                fcntl.flock(f.fileno(), fcntl.LOCK_UN)
    except Exception:
        # fdopen takes ownership of fd on success; only close if we never reached the with-block
        raise


def extract_preloaded_state(body: str) -> dict | None:
    """Extract window.__PRELOADED_STATE__ JSON object via brace-balanced scan."""
    marker = "window.__PRELOADED_STATE__"
    i = body.find(marker)
    if i < 0:
        return None
    i = body.find("{", i)
    if i < 0:
        return None
    depth = 0
    in_str = False
    esc = False
    for j in range(i, len(body)):
        c = body[j]
        if in_str:
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
            continue
        if c == '"':
            in_str = True
        elif c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                try:
                    return json.loads(body[i:j+1])
                except json.JSONDecodeError:
                    return None
    return None


def extract_ad_info(body: str) -> dict:
    """Extract ad fields from Divar __PRELOADED_STATE__ JSON."""
    info: dict = {}
    state = extract_preloaded_state(body)
    if not state:
        return info
    post = (state.get("currentPost") or {}).get("post") or {}
    seo = post.get("seo") or {}
    web = seo.get("webInfo") or {}

    info["token"] = post.get("token")
    info["title"] = web.get("title") or seo.get("title")
    city = post.get("city") or {}
    info["city"] = city.get("name") or web.get("city_persian")
    info["city_id"] = city.get("id")
    info["city_slug"] = city.get("slug")
    info["city_parent_id"] = city.get("parent")
    info["district"] = web.get("district_persian")
    info["category"] = web.get("category_slug_persian")

    analytics = post.get("analytics") or {}
    info["category_slug"] = analytics.get("cat2") or analytics.get("cat1")
    info["cat1"] = analytics.get("cat1")
    info["cat2"] = analytics.get("cat2")
    info["cat3"] = analytics.get("cat3")

    # district id from breadcrumb searchData
    for b in seo.get("breadcrumbs", []):
        ids = (((b.get("searchData") or {}).get("formData") or {}).get("districts") or {}).get("repeated_string", {}).get("value")
        if ids:
            info["district_ids"] = ids
            break
    info["breadcrumbs"] = [b.get("name") for b in seo.get("breadcrumbs", []) if b.get("name")]

    we = post.get("webengage") or {}
    if isinstance(we.get("price"), (int, float)) and we["price"]:
        info["price_value"] = int(we["price"])

    sections = post.get("sections") or {}

    # description
    for w in sections.get("DESCRIPTION", []):
        if w.get("widgetType") == "DESCRIPTION_ROW":
            txt = (((w.get("dto") or {}).get("data") or {}).get("text"))
            if txt:
                info["description"] = txt
                break

    # key/value rows: price, condition, exchange...
    fields: dict = {}
    for w in sections.get("LIST_DATA", []):
        d = ((w.get("dto") or {}).get("data") or {})
        t, v = d.get("title"), d.get("value")
        if t and v:
            fields[t] = v
    if fields:
        info["fields"] = fields
        # convenient flat aliases
        if "قیمت" in fields:
            info["price"] = fields["قیمت"]
            digits = fields["قیمت"].translate(str.maketrans("۰۱۲۳۴۵۶۷۸۹٠١٢٣٤٥٦٧٨٩", "01234567890123456789"))
            digits = re.sub(r"[^\d]", "", digits)
            if digits:
                info["price_value"] = int(digits)
        if "وضعیت" in fields:
            info["condition"] = fields["وضعیت"]

    # publish / bump dates from EXPANDABLE_SECTION under TITLE
    for w in sections.get("TITLE", []):
        if w.get("widgetType") == "EXPANDABLE_SECTION":
            d = ((w.get("dto") or {}).get("data") or {})
            info["posted_label"] = d.get("title")
            for sub in d.get("widget_list", []):
                if sub.get("widget_type") == "DESCRIPTION_ROW":
                    info["publish_info"] = ((sub.get("data") or {}).get("text"))
                    break
            break

    # images & video
    images: list = []
    video: str | None = None
    for w in sections.get("IMAGE", []):
        if w.get("widgetType") == "IMAGE_CAROUSEL":
            for it in (((w.get("dto") or {}).get("data") or {}).get("items", [])):
                u = (it.get("image") or {}).get("url")
                if u:
                    images.append(u)
                if not video and it.get("video_url"):
                    video = it.get("video_url")
    if images:
        info["images"] = images
    if video:
        info["video"] = video

    return info


def fetch_phone(body: str, url: str) -> str | None:
    """Best-effort phone fetch from Divar contact API. Returns None on any failure.

    Cookies come from scripts/.cookies/divar.ir.json. On HTTP 401/403 or a
    non-JSON response we trigger one session refresh+retry via divar_session.
    """
    match = re.search(r'"contactUUID"\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"', body)
    if not match:
        match = re.search(r'"contact_uuid"\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"', body)
    if not match:
        log.info("phone: no contactUUID in body for url=%s", url)
        return None
    uuid = match.group(1)
    token = url.split("/")[-1] if url and "/" in url else None
    if not token:
        log.info("phone: no token in url=%s", url)
        return None
    api_url = f"https://api.divar.ir/v8/postcontact/web/contact_info_v2/{token}"
    headers = {
        "accept": "application/json, text/plain, */*",
        "cache-control": "no-cache",
        "content-type": "application/json",
        "origin": "https://divar.ir",
        "pragma": "no-cache",
        "priority": "u=1, i",
        "referer": "https://divar.ir/",
        "sec-ch-ua": '"Google Chrome";v="147", "Not.A/Brand";v="8", "Chromium";v="147"',
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": '"Linux"',
        "sec-fetch-dest": "empty",
        "sec-fetch-mode": "cors",
        "sec-fetch-site": "same-site",
        "x-render-type": "CSR",
        "x-screen-size": "664x813",
    }
    payload = {"contact_uuid": uuid}
    try:
        session = make_session()
        cookie_names = sorted(c.name for c in session.cookies)
        log.info("phone: GET token=%s uuid=%s cookies=%s", token, uuid, cookie_names)
        resp = request_with_refresh(
            session, "POST", api_url,
            headers=headers, json=payload, timeout=15, expect_json=True,
        )
        body_snip = (resp.text or "")[:300].replace("\n", " ")
        log.info("phone: HTTP %s len=%d body=%s",
                 resp.status_code, len(resp.text or ""), body_snip)
        if resp.status_code // 100 != 2:
            return None
        try:
            data = resp.json()
        except ValueError as e:
            log.warning("phone: response not JSON for token=%s: %s", token, e)
            return None
    except Exception as e:
        log.exception("phone: fetch error for token=%s: %s", token, e)
        return None
    for widget in data.get("widget_list", []):
        ph = widget.get("data", {}).get("action", {}).get("payload", {}).get("phone_number")
        if ph:
            log.info("phone: extracted token=%s phone=%s", token, ph)
            return ph
    log.info("phone: no phone_number widget in response for token=%s", token)
    return None


def process(page: dict) -> str:
    """
    Process a single crawled page.

    Builds an entry of shape:
        {"ad": {...}, "user": {"phone": "..."}}   # user key only when phone fetched

    Appends it to scripts/after_crawl_data/{job_id}.json. Returns a short
    log line for the engine.
    """
    url = page.get("url", "")
    status = page.get("status_code", 0)
    depth = page.get("depth", 0)
    body = page.get("body", "")
    job_id = page.get("job_id", "")

    ad_info = extract_ad_info(body)
    if ad_info:
        ad_info["url"] = url

    if not ad_info:
        return f"status={status} depth={depth} body_len={len(body)} url={url} (no ad)"

    if not ad_info.get("token"):
        return f"skip (no token) url={url}"

    entry: dict = {"ad": ad_info}
    phone = fetch_phone(body, url)
    if phone:
        entry["user"] = {"phone": phone}

    try:
        append_entry(job_id, entry)
    except Exception as e:
        return f"ad-extracted but append failed: {e}"

    token = ad_info.get("token") or ""
    return f"appended job={job_id} token={token} phone={'yes' if phone else 'no'}"


def main():
    raw = sys.stdin.read()
    if not raw.strip():
        sys.exit(0)

    try:
        page = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"ERROR: failed to parse input JSON: {e}", file=sys.stderr)
        sys.exit(1)

    result = process(page)
    if result:
        print(result)


if __name__ == "__main__":
    main()
