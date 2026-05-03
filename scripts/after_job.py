#!/usr/bin/env python3
"""
after_job.py — Custom after-job hook for the Mole crawler.

Scheduled by the crawler engine ~2 minutes after a job ends (completed,
cancelled, or stopped) when "Run After-Job Script" is enabled in the job
settings. It is launched detached (via setsid) so it survives a server
restart between scheduling and execution.

Invocation:
    python after_job.py --job-id <uuid>

Input file:
    scripts/after_crawl_data/{job_id}.json
    Shape: {"data": [{"ad": {...}, "user": {"phone": "..."}}, ...]}
    The "user" key is only present when after_crawl.py successfully fetched
    the phone for that ad.

Behavior:
  1. POST the whole {"data": [...]} document to AFTER_JOB_API_URL.
  2. The API returns {"data": [{"phone": "...", "token": "...", "short_code": "..."}, ...]}
     for the ads it has successfully ingested.
  3. For every response item that has a phone, look up the matching ad in the
     local file by `token`, build a short Persian SMS body, and send it.
     The SMS sender is a TODO stub — it currently just logs.
  4. Successfully processed entries are MOVED into a per-run archive file at
     scripts/after_crawl_data/archive/{job_id}-{timestamp}.json with the SAME
     {"data": [...]} shape as the source. Failed/unprocessed entries remain
     in the source file (so a retry later picks them up). If the source file
     is fully drained it is deleted.

The archive file is drop-in: replacing the source file with an archive file
will make a re-run process exactly the same set of entries again.

Runs inside scripts/.venv — add dependencies to scripts/requirements.txt and
run scripts/setup_python.sh to install them.
"""

import argparse
import datetime as dt
import json
import os
import sys
import requests


DATA_DIR = os.path.join(os.path.dirname(__file__), "after_crawl_data")
ARCHIVE_DIR = os.path.join(DATA_DIR, "archive")

# Ingestion endpoint — set via env var; sample default kept obviously fake.
API_URL = os.environ.get("AFTER_JOB_API_URL", "https://example.com/api/ingest")

# Public link prefix used in outgoing SMS. {short_code} is appended.
LINK_BASE = os.environ.get("AFTER_JOB_LINK_BASE", "example.com")


def load_source(job_id: str) -> tuple[str, dict]:
    path = os.path.join(DATA_DIR, f"{job_id}.json")
    if not os.path.exists(path):
        return path, {"data": []}
    with open(path, "r", encoding="utf-8") as f:
        try:
            doc = json.load(f)
        except json.JSONDecodeError:
            doc = {"data": []}
    if not isinstance(doc, dict) or not isinstance(doc.get("data"), list):
        doc = {"data": []}
    return path, doc


def post_to_api(doc: dict) -> dict:
    """POST the full document. Raises on non-2xx. Returns parsed response."""
    resp = requests.post(API_URL, json=doc, timeout=60)
    resp.raise_for_status()
    return resp.json()


def build_sms(ad: dict, short_code: str) -> str:
    """Compose the Persian SMS body. Short, polite, includes link."""
    title = (ad.get("title") or "آگهی شما").strip()
    # Trim very long titles so the SMS stays in one segment-ish range.
    if len(title) > 40:
        title = title[:39] + "…"
    link = f"{LINK_BASE}/{short_code}"
    return (
        f"سلام! آگهی «{title}» شما در دیوار رو دیدیم. "
        f"همین آگهی توی سامانه ما هم آماده‌ست — با یک کلیک منتشرش کن:\n{link}"
    )


def send_sms(phone: str, message: str) -> None:
    """
    TODO: integrate real SMS provider (kavenegar / melli payamak / etc).
    For now, just log the intent. Returning normally is treated as success.
    Raise an exception to signal failure (entry will stay in source file).
    """
    print(f"[after_job] SMS -> {phone}: {message}", flush=True)


def archive_entries(job_id: str, processed: list[dict]) -> str | None:
    if not processed:
        return None
    os.makedirs(ARCHIVE_DIR, exist_ok=True)
    ts = dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    path = os.path.join(ARCHIVE_DIR, f"{job_id}-{ts}.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump({"data": processed}, f, ensure_ascii=False)
    return path


def write_remaining(path: str, remaining: list[dict]) -> None:
    if remaining:
        with open(path, "w", encoding="utf-8") as f:
            json.dump({"data": remaining}, f, ensure_ascii=False)
    else:
        try:
            os.remove(path)
        except FileNotFoundError:
            pass


def run(job_id: str) -> int:
    src_path, doc = load_source(job_id)
    entries = doc.get("data", [])
    if not entries:
        print(f"[after_job] No entries for job {job_id}; nothing to do.")
        return 0

    print(f"[after_job] Job {job_id}: posting {len(entries)} entries to {API_URL}")
    try:
        api_resp = post_to_api(doc)
    except Exception as e:
        print(f"[after_job] ERROR: API call failed: {e}", file=sys.stderr)
        return 1

    api_items = (api_resp or {}).get("data") or []
    print(f"[after_job] API returned {len(api_items)} processed items")

    # Index source entries by ad.token for fast lookup.
    by_token: dict[str, dict] = {}
    for entry in entries:
        token = ((entry.get("ad") or {}).get("token"))
        if token:
            by_token[token] = entry

    processed_tokens: set[str] = set()
    for item in api_items:
        token = item.get("token")
        phone = item.get("phone")
        short_code = item.get("short_code")
        if not token or token not in by_token:
            continue
        if not phone:
            # API processed it but didn't include a phone — count as done, no SMS.
            processed_tokens.add(token)
            continue
        if not short_code:
            print(f"[after_job] Skipping token {token}: missing short_code in API response", file=sys.stderr)
            continue
        ad = by_token[token].get("ad") or {}
        message = build_sms(ad, short_code)
        try:
            send_sms(phone, message)
            processed_tokens.add(token)
        except Exception as e:
            # Per-entry failure: leave it in the source file for next run.
            print(f"[after_job] SMS failed for token {token} ({phone}): {e}", file=sys.stderr)

    processed_entries = [by_token[t] for t in processed_tokens]
    remaining_entries = [e for e in entries
                         if ((e.get("ad") or {}).get("token")) not in processed_tokens]

    archive_path = archive_entries(job_id, processed_entries)
    write_remaining(src_path, remaining_entries)

    print(f"[after_job] Done. processed={len(processed_entries)} "
          f"remaining={len(remaining_entries)} archive={archive_path}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--job-id", required=True)
    args = ap.parse_args()
    return run(args.job_id)


if __name__ == "__main__":
    sys.exit(main())
