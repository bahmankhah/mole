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
import time
import requests


DATA_DIR = os.path.join(os.path.dirname(__file__), "after_crawl_data")
ARCHIVE_DIR = os.path.join(DATA_DIR, "archive")

# Ingestion endpoint — set via env var; sample default kept obviously fake.
API_URL = os.environ.get("AFTER_JOB_API_URL", "http://194.56.148.85:8000/api/v1/ads/bulk-import-with-users")
API_KEY = os.environ.get("AFTER_JOB_API_KEY", "flMliOzL_tJDz67xgaV57c-AqcjK6hAE-7PEXuZ0wrs")
APP_ID = os.environ.get("AFTER_JOB_APP_ID", "6144412de54d4d709e3ddfbf7b9233f1")
CHUNK_SIZE = int(os.environ.get("AFTER_JOB_CHUNK_SIZE", "30"))
CHUNK_DELAY_SEC = int(os.environ.get("AFTER_JOB_CHUNK_DELAY_SEC", "30"))

# SMS sending toggle. False -> log only, no HTTP call.
SEND_SMS = False

# When True, only entries that have a user/phone are POSTed to the ingest API.
ONLY_WITH_PHONE = False

# sms.ir bulk endpoint reused for one-mobile-at-a-time sends.
SMS_API_URL = "https://api.sms.ir/v1/send/bulk"
SMS_SEND_DELAY_SEC = float(os.environ.get("AFTER_JOB_SMS_DELAY_SEC", "1.5"))
SMS_API_KEY = os.environ.get(
    "SMS_IR_API_KEY",
    "fixtU3DEsfsSoCvhaaa3UBKnFZbAIqmKFPaoXRuZTfDW5JEm",
)
SMS_LINE_NUMBER = int(os.environ.get("SMS_IR_LINE_NUMBER", "50003181890144"))

# Per-ad message template. {ref} is replaced with the ad's token.
SMS_TEMPLATE = os.environ.get(
    "AFTER_JOB_SMS_TEMPLATE",
    "سلام همشهری. آگهی‌تون رو دیدم.\n"
    "من با این ربات خیلی سریع جنسامو فروختم، شما هم امتحان کنید:\n"
    "ble.ir/aginobot?start=c-{ref}",
)


def load_source(job_id: str | None = None, file_path: str | None = None) -> tuple[str, dict]:
    if file_path:
        path = file_path if os.path.isabs(file_path) else os.path.join(DATA_DIR, file_path)
    else:
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


def post_chunk(chunk: list[dict]) -> dict:
    """POST one chunk. Raises on non-2xx. Returns parsed response."""
    headers = {"X-API-Key": API_KEY, "Content-Type": "application/json"}
    resp = requests.post(API_URL, json={"app_id": APP_ID, "data": chunk},
                         headers=headers, timeout=120)
    if not resp.ok:
        print(f"[after_job] HTTP {resp.status_code} body: {resp.text[:1000]}",
              file=sys.stderr, flush=True)
    resp.raise_for_status()
    return resp.json()


DUPLICATE_ERR_RE = "duplicate"


def post_in_chunks(entries: list[dict], chunk_size: int) -> tuple[list[dict], list[dict]]:
    """POST entries in sequential chunks. Returns (success_items, failed_items)."""
    success_items: list[dict] = []
    failed_items: list[dict] = []
    total = len(entries)
    for i in range(0, total, chunk_size):
        chunk = entries[i:i + chunk_size]
        idx = i // chunk_size + 1
        n_chunks = (total + chunk_size - 1) // chunk_size
        print(f"[after_job] chunk {idx}/{n_chunks}: posting {len(chunk)} entries", flush=True)
        resp = post_chunk(chunk)
        s_items = (resp or {}).get("success_items") or []
        f_items = (resp or {}).get("failed_items") or []
        success_items.extend(s_items)
        failed_items.extend(f_items)
        sc = (resp or {}).get("success_count")
        fc = (resp or {}).get("failed_count")
        print(f"[after_job] chunk {idx}/{n_chunks}: success={sc} failed={fc}", flush=True)
        # Surface failure reasons (capped to first 5 per chunk to avoid log flood).
        for fi in f_items[:5]:
            print(f"[after_job]   fail token={fi.get('token')} error={fi.get('error')}",
                  flush=True)
        if len(f_items) > 5:
            print(f"[after_job]   ... +{len(f_items) - 5} more failures", flush=True)
        if idx < n_chunks and CHUNK_DELAY_SEC > 0:
            print(f"[after_job] sleeping {CHUNK_DELAY_SEC}s before next chunk", flush=True)
            time.sleep(CHUNK_DELAY_SEC)
    return success_items, failed_items


def send_sms_per_phone(pairs: list[tuple[str, str]]) -> None:
    """
    Send a different message to each phone, one HTTP request per recipient,
    with a small sleep between sends to avoid rate-limit / throttling.
    When SEND_SMS is False, log the intent and return without HTTP.
    Per-send failures are logged but do not abort the loop.
    """
    if not pairs:
        return
    if not SEND_SMS:
        sample = pairs[0]
        print(f"[after_job] SMS DISABLED — would send {len(pairs)} per-phone msgs. "
              f"sample phone={sample[0]} msg={sample[1]!r}", flush=True)
        return
    headers = {"x-api-key": SMS_API_KEY, "Content-Type": "application/json"}
    sent = 0
    for i, (phone, message) in enumerate(pairs):
        payload = {
            "lineNumber": SMS_LINE_NUMBER,
            "messageText": message,
            "mobiles": [phone],
        }
        try:
            resp = requests.post(SMS_API_URL, json=payload, headers=headers, timeout=30)
            resp.raise_for_status()
            sent += 1
            print(f"[after_job] SMS {i+1}/{len(pairs)} sent to {phone}: "
                  f"status={resp.status_code}", flush=True)
        except Exception as e:
            print(f"[after_job] SMS {i+1}/{len(pairs)} FAILED to {phone}: {e}",
                  file=sys.stderr, flush=True)
        if i < len(pairs) - 1 and SMS_SEND_DELAY_SEC > 0:
            time.sleep(SMS_SEND_DELAY_SEC)
    print(f"[after_job] SMS loop done. sent={sent}/{len(pairs)}", flush=True)


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


def run(job_id: str | None = None, file_path: str | None = None) -> int:
    src_path, doc = load_source(job_id=job_id, file_path=file_path)
    label = job_id or os.path.splitext(os.path.basename(src_path))[0]
    all_entries = doc.get("data", [])
    if not all_entries:
        print(f"[after_job] No entries for {label}; nothing to do.")
        return 0

    if ONLY_WITH_PHONE:
        entries = [
            e for e in all_entries
            if ((e.get("user") or {}).get("phone"))
        ]
        skipped = len(all_entries) - len(entries)
        print(f"[after_job] {label}: ONLY_WITH_PHONE=True — keeping {len(entries)}/"
              f"{len(all_entries)} (skipped {skipped} without phone)")
    else:
        entries = all_entries

    if not entries:
        print(f"[after_job] No entries with phone for {label}; nothing to do.")
        return 0

    print(f"[after_job] {label}: posting {len(entries)} entries to {API_URL} "
          f"in chunks of {CHUNK_SIZE}")
    try:
        api_items, failed_items = post_in_chunks(entries, CHUNK_SIZE)
    except Exception as e:
        print(f"[after_job] ERROR: API call failed: {e}", file=sys.stderr)
        return 1

    print(f"[after_job] API returned success={len(api_items)} failed={len(failed_items)}")

    # Index source entries by ad.token for fast lookup.
    by_token: dict[str, dict] = {}
    for entry in entries:
        token = ((entry.get("ad") or {}).get("token"))
        if token:
            by_token[token] = entry

    processed_tokens: set[str] = set()
    # Duplicates are permanent — already in DB. Mark processed so they archive.
    for fi in failed_items:
        token = fi.get("token")
        err = (fi.get("error") or "").lower()
        if token and token in by_token and DUPLICATE_ERR_RE in err:
            processed_tokens.add(token)

    sms_targets: list[tuple[str, str]] = []  # (token, phone)
    for item in api_items:
        token = item.get("token")
        phone = item.get("phone")
        if not token or token not in by_token:
            continue
        if not phone:
            # API processed it but didn't include a phone — count as done, no SMS.
            processed_tokens.add(token)
            continue
        sms_targets.append((token, phone))

    # SMS sending disabled — uncomment to re-enable.
    # sms_pairs: list[tuple[str, str]] = [
    #     (phone, SMS_TEMPLATE.format(ref=token)) for token, phone in sms_targets
    # ]
    # try:
    #     send_sms_per_phone(sms_pairs)
    # except Exception as e:
    #     print(f"[after_job] Per-phone SMS failed for {len(sms_pairs)} phones: {e}",
    #           file=sys.stderr)
    # Mark all phone-bearing items processed so they archive even without SMS.
    for token, _ in sms_targets:
        processed_tokens.add(token)
    print(f"[after_job] SMS phase skipped (commented out). "
          f"Would have sent to {len(sms_targets)} phones.")

    processed_entries = [by_token[t] for t in processed_tokens]
    remaining_entries = [e for e in entries
                         if ((e.get("ad") or {}).get("token")) not in processed_tokens]

    archive_path = archive_entries(label, processed_entries)
    write_remaining(src_path, remaining_entries)

    print(f"[after_job] Done. processed={len(processed_entries)} "
          f"remaining={len(remaining_entries)} archive={archive_path}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--job-id", help="Job UUID; loads after_crawl_data/<job-id>.json")
    g.add_argument("--file", dest="file_path",
                   help="Path to a JSON file (absolute, or relative to after_crawl_data/)")
    args = ap.parse_args()
    return run(job_id=args.job_id, file_path=args.file_path)


if __name__ == "__main__":
    sys.exit(main())
